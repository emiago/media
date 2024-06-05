package media

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/emiago/media/sdp"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

// RTP session is RTP ReadWriter with control (RTCP) reporting
// Session is identified by network address and port pair to which data should be sent and received
// Participant can be part of multiple rtp sessions. One for audio, one for media.
// So for different MediaSession (audio,video etc) different RTP session needs to be created
// TODO
// RTPSession can be different type:
// - unicast or multicase
// - replicated unicast (mixers audio)
// RTP session is attached to RTPReader and RTPWriter
// Now:
// - Designed for UNICAST sessions
// - Acts as RTP Reader Writer
// - Only makes sense for Reporting Quality mediaw
// Roadmap:
// - Can handle multiple SSRC from Reader
// - Multiple RTCP Recever Blocks
type RTPSession struct {
	// Keep pointers at top to reduce GC
	Sess *MediaSession

	rtcpTicker *time.Ticker
	rtcpMU     sync.Mutex

	readStats  RTPReadStats
	writeStats RTPWriteStats

	// RTP codec
	codec codec
	// Experimental
	// this intercepts reading or writing rtcp packet. Allows manipulation
	OnReadRTCP  func(pkt rtcp.Packet, rtpStats RTPReadStats)
	OnWriteRTCP func(pkt rtcp.Packet, rtpStats RTPWriteStats)
}

type RTPReadStats struct {
	SSRC                   uint32
	FirstPktSequenceNumber uint16
	LastSequenceNumber     uint16
	lastSeq                RTPExtendedSequenceNumber
	// tracks first pkt seq in this interval to calculate loss of packets
	IntervalFirstPktSeqNum uint16
	IntervalTotalPackets   uint16

	TotalPackets uint64

	// RTP reading stats
	sampleRate       uint32
	lastRTPTime      time.Time
	lastRTPTimestamp uint32
	jitter           float64

	// Reading RTCP stats
	lastSenderReportNTP       uint64
	lastSenderReportRecvTime  time.Time
	lastReceptionReportSeqNum uint32

	// Round TRIP Time based on LSR and DLSR
	RTT time.Duration
}

type RTPWriteStats struct {
	SSRC                uint32
	lastPacketTime      time.Time
	lastPacketTimestamp uint32
	// RTCP stats
	PacketsCount uint32
	OctetCount   uint32
}

// RTP session creates new RTP reader/writer from session
func NewRTPSession(sess *MediaSession) *RTPSession {
	return &RTPSession{
		Sess:       sess,
		rtcpTicker: time.NewTicker(5 * time.Second),
		codec:      codecFromSession(sess),
	}
}

func (s *RTPSession) Close() error {
	s.rtcpTicker.Stop()

	// STOP Media SESS?
	//s.Reader.Sess.Close()

	return nil
}

// Monitor starts reading RTCP and monitoring media quality
func (s *RTPSession) Monitor() {
	go s.readRTCP()
}

func (s *RTPSession) ReadRTP(b []byte, readPkt *rtp.Packet) error {
	if err := s.Sess.ReadRTP(b, readPkt); err != nil {
		return err
	}

	// pktArrival := time.Now()
	stats := &s.readStats
	now := time.Now()

	// For now we only track latest SSRC
	if stats.SSRC != readPkt.SSRC {
		// For now we will reset all our stats.
		// We expect that SSRC only changed but MULTI RTP stream per one session are not fully supported!
		*stats = RTPReadStats{
			SSRC:                   readPkt.SSRC,
			FirstPktSequenceNumber: readPkt.SequenceNumber,
		}
		stats.lastSeq.InitSeq(readPkt.SequenceNumber)
	} else {
		stats.lastSeq.UpdateSeq(readPkt.SequenceNumber)
		sampleRate := s.codec.sampleRate

		// Jitter here will mostly be incorrect as Reading RTP can be faster slower
		// and not actually dictated by sampling (clock)
		// https://datatracker.ietf.org/doc/html/rfc3550#page-39
		Sij := readPkt.Timestamp - stats.lastRTPTimestamp
		Rij := now.Sub(stats.lastRTPTime)
		D := Rij.Seconds()*float64(sampleRate) - float64(Sij)
		if D < 0 {
			D = -D
		}
		stats.jitter = stats.jitter + (D-stats.jitter)/16
	}
	stats.IntervalTotalPackets++
	stats.TotalPackets++
	stats.LastSequenceNumber = readPkt.SequenceNumber
	// stats.clockRTPTimestamp+=

	if stats.IntervalFirstPktSeqNum == 0 {
		stats.IntervalFirstPktSeqNum = readPkt.SequenceNumber
	}

	stats.lastRTPTime = now
	stats.lastRTPTimestamp = readPkt.Timestamp

	select {
	case t := <-s.rtcpTicker.C:
		s.writeRTCP(t, stats.SSRC)
		stats.IntervalFirstPktSeqNum = 0
		stats.IntervalTotalPackets = 0
	default:
	}

	return nil
}

func (s *RTPSession) WriteRTP(pkt *rtp.Packet) error {
	// Do not write if we are creating RTCP packet
	s.rtcpMU.Lock()
	defer s.rtcpMU.Unlock()

	if err := s.Sess.WriteRTP(pkt); err != nil {
		return err
	}

	writeStats := &s.writeStats
	// For now we only track latest SSRC
	if writeStats.SSRC != pkt.SSRC {
		*writeStats = RTPWriteStats{
			SSRC: pkt.SSRC,
		}
	}

	writeStats.PacketsCount++
	writeStats.OctetCount += uint32(len(pkt.Payload))
	writeStats.lastPacketTime = time.Now()
	writeStats.lastPacketTimestamp = pkt.Timestamp
	return nil
}

func (s *RTPSession) readRTCP() {
	sess := s.Sess
	buf := make([]rtcp.Packet, 5) // What would be more correct value?
	for {
		n, err := sess.ReadRTCP(buf)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				sess.log.Debug().Msg("RTP session RTCP reader exit")
				return
			}

			sess.log.Error().Err(err).Msg("RTP session RTCP reader stopped with error")
			return
		}

		for _, pkt := range buf[:n] {
			s.readRTCPPacket(pkt)
		}
	}
}

func (s *RTPSession) readRTCPPacket(pkt rtcp.Packet) {
	s.rtcpMU.Lock()
	now := time.Now()

	// Add interceptor
	if s.OnReadRTCP != nil {
		stats := s.readStats
		s.rtcpMU.Unlock()
		s.OnReadRTCP(pkt, stats)
		s.rtcpMU.Lock()
	}

	switch p := pkt.(type) {
	case *rtcp.SenderReport:
		s.readStats.lastSenderReportNTP = p.NTPTime
		s.readStats.lastSenderReportRecvTime = now

		for _, rr := range p.Reports {
			s.readReceptionReport(rr, now)
		}

	case *rtcp.ReceiverReport:
		for _, rr := range p.Reports {
			s.readReceptionReport(rr, now)
		}
	}
	s.rtcpMU.Unlock()

}

func (s *RTPSession) readReceptionReport(rr rtcp.ReceptionReport, now time.Time) {
	// For now only use single SSRC
	if rr.SSRC != s.writeStats.SSRC {
		s.Sess.log.Warn().Uint32("ssrc", rr.SSRC).Uint32("expected", s.readStats.SSRC).Msg("Reception report SSRC does not match our internal")
		return
	}

	// compute jitter
	// https://tools.ietf.org/html/rfc3550#page-39
	// Round trip time
	rtt := now.Sub(NTPToTime(uint64(rr.LastSenderReport)))
	rtt -= time.Duration(rr.Delay) * time.Second / 65356
	s.readStats.RTT = rtt
	// used to calc fraction lost
	s.readStats.lastReceptionReportSeqNum = rr.LastSequenceNumber
}

func (s *RTPSession) writeRTCP(now time.Time, ssrc uint32) {
	var pkt rtcp.Packet

	// If there is no writer in session (a=recvonly) then generate only receiver report
	// otherwise always go with sender report with reception reports
	s.rtcpMU.Lock()
	if s.Sess.Mode == sdp.ModeRecvonly {

		rr := rtcp.ReceiverReport{}
		s.parseReceiverReport(&rr, now, ssrc)

		pkt = &rr
	} else {
		sr := rtcp.SenderReport{}
		s.parseSenderReport(&sr, now, ssrc)

		pkt = &sr
	}

	// Add interceptor
	if s.OnWriteRTCP != nil {
		stats := s.writeStats
		s.rtcpMU.Unlock()
		s.OnWriteRTCP(pkt, stats)
	} else {
		s.rtcpMU.Unlock()
	}

	if err := s.Sess.WriteRTCP(pkt); err != nil {
		s.Sess.log.Error().Err(err).Msg("Failed to write RTCP")
	}
}

func (s *RTPSession) parseReceiverReport(receiverReport *rtcp.ReceiverReport, now time.Time, ssrc uint32) {
	receptionReport := rtcp.ReceptionReport{}
	s.parseReceptionReport(&receptionReport, now)

	*receiverReport = rtcp.ReceiverReport{
		SSRC:    ssrc,
		Reports: []rtcp.ReceptionReport{receptionReport},
	}
}

func (s *RTPSession) parseSenderReport(senderReport *rtcp.SenderReport, now time.Time, ssrc uint32) {
	receptionReport := rtcp.ReceptionReport{}
	s.parseReceptionReport(&receptionReport, now)

	// Write stats
	writeStats := &s.writeStats
	rtpTimestampOffset := now.Sub(writeStats.lastPacketTime).Seconds() * float64(s.codec.sampleRate)
	fmt.Println(writeStats.lastPacketTimestamp, rtpTimestampOffset)
	// Same as asterisk
	// Sender Report should contain Receiver Report if user acts as sender and receiver
	// Otherwise on Read we should generate only receiver Report
	*senderReport = rtcp.SenderReport{
		SSRC:        ssrc,
		NTPTime:     NTPTimestamp(now),
		RTPTime:     writeStats.lastPacketTimestamp + uint32(rtpTimestampOffset),
		PacketCount: writeStats.PacketsCount,
		OctetCount:  writeStats.OctetCount,
		Reports:     []rtcp.ReceptionReport{receptionReport},
	}
}

func (s *RTPSession) parseReceptionReport(receptionReport *rtcp.ReceptionReport, now time.Time) {
	var sequenceCycles uint16 = 0 // TODO have sequence cycles handled

	// Read stats
	readStats := &s.readStats
	lastExtendedSeq := &readStats.lastSeq

	// fraction loss is caluclated as packets loss / number expected in interval as fixed point number with point number at the left edge
	receivedLastSeq := int64(lastExtendedSeq.ReadExtendedSeq())
	readIntervalExpectedPkts := receivedLastSeq - int64(readStats.IntervalFirstPktSeqNum)
	readIntervalLost := max(readIntervalExpectedPkts-int64(readStats.IntervalTotalPackets), 0)
	fractionLost := float64(readIntervalLost) / float64(readIntervalExpectedPkts) // Can be negative

	// Watch OUT FOR -1
	expectedPkts := uint64(receivedLastSeq) - uint64(readStats.FirstPktSequenceNumber)

	// interarrival jitter
	// network transit time
	lastReceivedSenderReportTime := readStats.lastSenderReportRecvTime
	delay := now.Sub(lastReceivedSenderReportTime)
	// TODO handle multiple SSRC
	*receptionReport = rtcp.ReceptionReport{
		SSRC:               readStats.SSRC,
		FractionLost:       uint8(max(fractionLost*256, 0)),                         // Can be negative. Saturate to zero
		TotalLost:          uint32(min(expectedPkts-readStats.TotalPackets, 1<<32)), // Saturate to largest 32 bit value
		LastSequenceNumber: uint32(sequenceCycles)<<16 + uint32(readStats.LastSequenceNumber),
		Jitter:             uint32(readStats.jitter),                    // TODO
		LastSenderReport:   uint32(readStats.lastSenderReportNTP >> 16), // LSR
		Delay:              uint32(delay.Seconds() * 65356),             // DLSR
	}
}

func FractionLostFloat(f uint8) float64 {
	return float64(f) / 256
}
