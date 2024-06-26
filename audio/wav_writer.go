package audio

import (
	"encoding/binary"
	"io"
)

type WavWriter struct {
	SampleRate  int
	BitDepth    int
	NumChans    int
	AudioFormat int

	W              io.Writer
	headersWritten bool
	dataSize       int
}

func NewWavWriter(w io.Writer, dataSize int) *WavWriter {
	return &WavWriter{
		SampleRate:  8000,
		BitDepth:    16,
		NumChans:    1,
		AudioFormat: 0,
		dataSize:    dataSize,
	}
}

func (ww *WavWriter) Write(audio []byte) (int, error) {
	if ww.headersWritten {
		return ww.W.Write(audio)
	}

	w := ww.W
	// WAV header constants
	const (
		headerSize   = 44
		fmtChunkSize = 16
	)

	audioFormat := ww.AudioFormat
	numChannels := ww.NumChans
	bitsPerSample := ww.BitDepth
	sampleRate := ww.SampleRate
	// Calculate file size
	fileSize := ww.dataSize + headerSize - 8

	// Create the header
	header := make([]byte, headerSize)
	copy(header[0:4], []byte("RIFF"))
	binary.LittleEndian.PutUint32(header[4:8], uint32(fileSize))
	copy(header[8:12], []byte("WAVE"))

	// fmt subchunk
	copy(header[12:16], []byte("fmt "))
	binary.LittleEndian.PutUint32(header[16:20], fmtChunkSize)
	binary.LittleEndian.PutUint16(header[20:22], uint16(audioFormat))
	binary.LittleEndian.PutUint16(header[22:24], uint16(numChannels))
	binary.LittleEndian.PutUint32(header[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(header[28:32], uint32(sampleRate*bitsPerSample*numChannels/8)) // Byte rate calculation
	binary.LittleEndian.PutUint16(header[32:34], uint16(bitsPerSample*numChannels/8))            // Block align
	binary.LittleEndian.PutUint16(header[34:36], uint16(bitsPerSample))

	// data chunk
	copy(header[36:40], []byte("data"))
	binary.LittleEndian.PutUint32(header[40:44], uint32(ww.dataSize))

	// Combine header and audio payload
	n, err := w.Write(header)
	if err != nil {
		return 0, err
	}
	ww.headersWritten = true

	nn, err := w.Write(audio)
	return n + nn, err
}
