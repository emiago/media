package media

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"

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
	codec := codecFromSession(sess)

	w := RTPReader{
		Sess:        sess,
		PayloadType: codec.payloadType,
		OnRTP:       func(pkt *rtp.Packet) {},

		seqReader:     RTPExtendedSequenceNumber{},
		unreadPayload: make([]byte, RTPBufSize),
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
		n := r.readPayload(b, r.unreadPayload[:r.unread])
		return n, nil
	}

	var n int
	var err error

	// For io.ReadAll buffer size is constantly changing and starts small
	// Normally user should > RTPBufSize
	// Use unread buffer and still avoid alloc
	buf := b
	if len(b) < RTPBufSize {
		r.Sess.log.Debug().Msg("Read RTP buf is < RTPBufSize. Using internal")
		buf = r.unreadPayload
	}

	pkt := rtp.Packet{}
	if r.RTPSession != nil {
		if err := r.RTPSession.ReadRTP(buf, &pkt); err != nil {
			if errors.Is(err, net.ErrClosed) {
				return 0, io.EOF
			}
			return 0, err
		}
	} else {

		// Reuse read buffer.
		n, err = r.Sess.ReadRTPRaw(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return 0, io.EOF
			}
			return 0, err
		}

		// NOTE: pkt after unmarshall will hold reference on b buffer.
		// Caller should do copy of PacketHeader if it reuses buffer
		if err := pkt.Unmarshal(buf[:n]); err != nil {
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

	size := min(len(b), len(buf))
	n = r.readPayload(buf[:size], pkt.Payload)
	return n, nil
}

func (r *RTPReader) readPayload(b []byte, payload []byte) int {
	n := copy(b, payload)
	if n < len(payload) {
		written := copy(r.unreadPayload, payload[n:])
		if written < len(payload[n:]) {
			r.Sess.log.Error().Msg("Payload is huge, it will be unread")
		}
		r.unread = written
	} else {
		r.unread = 0
	}
	return n
}

// Experimental
//
// RTPReaderConcurent allows updating RTPSession on RTPWriter and more (in case of regonation)
type RTPReaderConcurent struct {
	*RTPReader
	mu sync.Mutex
}

func (r *RTPReaderConcurent) Read(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.RTPReader.Read(b)
}

func (r *RTPReaderConcurent) SetRTPSession(rtpSess *RTPSession) {
	codec := codecFromSession(rtpSess.Sess)
	r.mu.Lock()
	r.RTPSession = rtpSess
	r.PayloadType = codec.payloadType
	r.mu.Unlock()
}
