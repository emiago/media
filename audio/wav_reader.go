package audio

import (
	"errors"
	"io"

	"github.com/go-audio/riff"
)

// WavReader allows reading wav from any io.Reader
// Streaming is supported
type WavReader struct {
	riff.Parser
	DataSize int

	chunkData *riff.Chunk
}

func NewWavReader(r io.Reader) *WavReader {
	parser := riff.New(r)
	reader := WavReader{Parser: *parser}
	return &reader
}

// ReadHeaders reads until data chunk
func (r *WavReader) ReadHeaders() error {
	if err := r.readHeaders(); err != nil {
		return err
	}

	return r.readDataChunk()
}

func (r *WavReader) readHeaders() error {
	// TODO make sure that reader has buffered at least 44 bytes
	if err := r.Parser.ParseHeaders(); err != nil {
		return err
	}
	for {
		chunk, err := r.NextChunk()
		if err != nil {
			if errors.Is(err, io.ErrShortBuffer) {
				continue
			}
			return err
		}

		if chunk.ID != riff.FmtID {
			chunk.Drain()
			continue
		}
		return chunk.DecodeWavHeader(&r.Parser)
	}
}

func (r *WavReader) readDataChunk() error {
	// if r.Size == 0 {
	// 	r.Parser.ParseHeaders()
	// }

	for {
		chunk, err := r.NextChunk()
		if err != nil {

			return err
		}

		if chunk.ID != riff.DataFormatID {
			chunk.Drain()
			continue
		}
		r.chunkData = chunk
		r.DataSize = chunk.Size
		return nil
	}
}

// Read returns PCM underneath
func (r *WavReader) Read(buf []byte) (n int, err error) {
	if r.chunkData != nil {
		return r.chunkData.Read(buf)
	}

	if err := r.readDataChunk(); err != nil {
		return 0, err
	}
	return r.chunkData.Read(buf)
}
