package media

import (
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtp"
)

// RTP Writer packetize any payload before pushing to active media session
// It creates SSRC as identifier and all packets sent will be with this SSRC
// For multiple streams, multiple RTP Writer needs to be created
type RTPWriter struct {
	RTPSession *RTPSession
	Sess       *MediaSession

	// After each write this is set as packet.
	LastPacket rtp.Packet
	OnRTP      func(pkt *rtp.Packet)

	// This properties are read only or can be changed only after creating writer
	PayloadType uint8
	SSRC        uint32
	SampleRate  uint32

	// Internals
	// clock rate is decided based on media
	sampleRateTimestamp uint32
	closed              atomic.Bool
	clockTicker         *time.Ticker
	seqWriter           RTPExtendedSequenceNumber
	nextTimestamp       uint32
	initTimestamp       uint32
}

// RTP writer packetize payload in RTP packet before passing on media session
// Not having:
// - random Timestamp
// - allow different clock rate
// - CSRC contribution source
// - Silence detection and marker set
// updateClockRate- Padding and encryyption
func NewRTPWriter(sess *RTPSession) *RTPWriter {
	w := NewRTPWriterMedia(sess.Sess)
	// We need to add our SSRC due to sender report, which can be empty until data comes
	// It is expected that nothing travels yet through rtp session
	sess.writeStats.SSRC = w.SSRC
	sess.writeStats.sampleRate = w.SampleRate
	w.RTPSession = sess
	return w
}

// NewRTPWriterMedia is left for backward compability. It does not add RTCP reporting
// RTPSession should be used for media quality reporting
func NewRTPWriterMedia(sess *MediaSession) *RTPWriter {
	codec := codecFromSession(sess)

	w := RTPWriter{
		Sess:        sess,
		seqWriter:   NewRTPSequencer(),
		PayloadType: codec.payloadType,
		SampleRate:  codec.sampleRate,
		SSRC:        rand.Uint32(),
		// initTimestamp: rand.Uint32(), // TODO random start timestamp
		// MTU:         1500,

		// TODO: CSRC CSRC is contribution source identifiers.
		// This is set when media is passed trough mixer/translators and original SSRC wants to be preserverd
	}

	w.nextTimestamp = w.initTimestamp
	w.updateClockRate(codec)

	return &w
}

func (w *RTPWriter) updateClockRate(cod codec) {
	w.sampleRateTimestamp = cod.sampleTimestamp()
	if w.clockTicker != nil {
		w.clockTicker.Stop()
	}
	w.clockTicker = time.NewTicker(cod.sampleDur)
}

// Write implements io.Writer and does payload RTP packetization
// Media clock rate is determined
// For more control or dynamic payload WriteSamples can be used
// It is not thread safe and order of payload frames is required
// Has no capabilities (yet):
// - MTU UDP limit handling
// - Media clock rate of payload is consistent
// - Packet loss detection
// - RTCP generating
func (p *RTPWriter) Write(b []byte) (int, error) {
	n, err := p.WriteSamples(b, p.sampleRateTimestamp, p.nextTimestamp == p.initTimestamp, p.PayloadType)
	<-p.clockTicker.C
	return n, err
}

func (p *RTPWriter) WriteSamples(payload []byte, clockRateTimestamp uint32, marker bool, payloadType uint8) (int, error) {
	pkt := rtp.Packet{
		Header: rtp.Header{
			Version:     2,
			Padding:     false,
			Extension:   false,
			Marker:      marker,
			PayloadType: payloadType,
			// Timestamp should increase linear and monotonic for media clock
			// Payload must be in same clock rate
			// TODO: what about wrapp arround
			Timestamp:      p.nextTimestamp,
			SequenceNumber: p.seqWriter.NextSeqNumber(),
			SSRC:           p.SSRC,
			CSRC:           []uint32{},
		},
		Payload: payload,
	}

	if p.OnRTP != nil {
		p.OnRTP(&pkt)
	}

	p.LastPacket = pkt
	p.nextTimestamp += clockRateTimestamp

	if p.RTPSession != nil {
		err := p.RTPSession.WriteRTP(&pkt)
		return len(pkt.Payload), err
	}

	err := p.Sess.WriteRTP(&pkt)
	return len(pkt.Payload), err
}

// Experimental
//
// RTPWriterConcurent allows updating RTPSession on RTPWriter and more (in case of regonation)
type RTPWriterConcurent struct {
	*RTPWriter
	mu sync.Mutex
}

func (w *RTPWriterConcurent) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.RTPWriter.Write(b)
}

func (w *RTPWriterConcurent) SetRTPSession(rtpSess *RTPSession) {
	codec := codecFromSession(rtpSess.Sess)
	w.mu.Lock()
	w.RTPWriter.RTPSession = rtpSess
	w.PayloadType = codec.payloadType
	w.SampleRate = codec.sampleRate
	w.updateClockRate(codec)
	w.mu.Unlock()
}
