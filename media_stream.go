package media

type MediaStreamer interface {
	MediaStream(s *MediaSession) error
}

// TODO buid basic handling of media session
// - logger
// - mic
// - speaker
