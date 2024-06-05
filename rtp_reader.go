package media

import (
	"errors"
	"fmt"
	"io"
	"net"

	"github.com/emiago/media/sdp"
	"github.com/pion/rtp"
)

// RTP Writer packetize any payload before pushing to active media session
type RTPReader struct {
	Sess       *MediaSession
	RTPSession *RTPSession

	// Deprecated
	//
	// Should not be used
	OnRTP func(pkt *rtp.Packet)

	// PacketHeader is stored after calling Read this will be stored before returning
	PacketHeader rtp.Header
	PayloadType  uint8

	seqReader RTPExtendedSequenceNumber

	unreadPayload []byte
	unread        int

	pktBuffer chan []byte

	// We want to track our last SSRC.
	lastSSRC uint32
}

// RTP reader consumes samples of audio from RTP session
// Use NewRTPSession to construct RTP session
func NewRTPReader(sess *RTPSession) *RTPReader {
	r := NewRTPReaderMedia(sess.Sess)
	r.RTPSession = sess
	return r
}

// NewRTPWriterMedia is left for backward compability. It does not add RTCP reporting
// It talks directly to network
func NewRTPReaderMedia(sess *MediaSession) *RTPReader {
	f := sess.Formats[0]
	var payloadType uint8 = sdp.FormatNumeric(f)
	switch f {
	case sdp.FORMAT_TYPE_ALAW:
	case sdp.FORMAT_TYPE_ULAW:
		// TODO more support
	default:
		sess.log.Warn().Str("format", f).Msg("Unsupported format. Using default clock rate")
	}

	w := RTPReader{
		Sess:          sess,
		unreadPayload: []byte{},
		PayloadType:   payloadType,
		OnRTP:         func(pkt *rtp.Packet) {},

		pktBuffer: make(chan []byte, 100),
		seqReader: RTPExtendedSequenceNumber{},
	}

	return &w
}

// Read Implements io.Reader and extracts Payload from RTP packet
// has no input queue or sorting control of packets
// Buffer is used for reading headers and Headers are stored in PacketHeader
//
// NOTE: Consider that if you are passsing smaller buffer than RTP payload, io.ErrShortBuffer is returned
func (r *RTPReader) Read(b []byte) (int, error) {
	if r.unread > 0 {
		n := r.readPayload(b, r.unreadPayload)
		return n, nil
	}

	var n int
	var err error

	pkt := rtp.Packet{}
	if r.RTPSession != nil {
		if err := r.RTPSession.ReadRTP(b, &pkt); err != nil {
			if errors.Is(err, net.ErrClosed) {
				return 0, io.EOF
			}
			return 0, err
		}
	} else {
		// Reuse read buffer.
		n, err = r.Sess.ReadRTPRaw(b)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return 0, io.EOF
			}
			return 0, err
		}

		// NOTE: pkt after unmarshall will hold reference on b buffer.
		// Caller should do copy of PacketHeader if it reuses buffer
		if err := pkt.Unmarshal(b[:n]); err != nil {
			return 0, err
		}
	}

	if r.PayloadType != pkt.PayloadType {
		return 0, fmt.Errorf("payload type does not match. expected=%d, actual=%d", r.PayloadType, pkt.PayloadType)
	}

	// If we are tracking this source, do check are we keep getting pkts in sequence
	if r.lastSSRC == pkt.SSRC {
		prevSeq := r.seqReader.ReadExtendedSeq()
		if err := r.seqReader.UpdateSeq(pkt.SequenceNumber); err != nil {
			r.Sess.log.Warn().Msg(err.Error())
		}

		newSeq := r.seqReader.ReadExtendedSeq()
		if prevSeq+1 != newSeq {
			r.Sess.log.Warn().Uint64("expected", prevSeq+1).Uint64("actual", newSeq).Uint16("real", pkt.SequenceNumber).Msg("Out of order pkt received")
		}
	} else {
		r.seqReader.InitSeq(pkt.SequenceNumber)
	}

	r.lastSSRC = pkt.SSRC
	r.PacketHeader = pkt.Header
	r.OnRTP(&pkt)

	return r.readPayload(b, pkt.Payload), nil
}

func (r *RTPReader) readPayload(b []byte, payload []byte) int {
	n := copy(b, payload)
	if n < len(payload) {
		r.unreadPayload = payload[n:]
		r.unread = len(payload) - n
	} else {
		r.unread = 0
	}
	return n
}
