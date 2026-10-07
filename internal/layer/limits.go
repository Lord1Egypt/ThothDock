package layer

import (
	"errors"
	"fmt"
	"io"
)

// Limits bound what applying one layer may consume. A zero field is not
// enforced; the caller (image.Puller) turns its configuration into these.
type Limits struct {
	// MaxStreamBytes bounds the uncompressed tar stream the layer expands
	// to, headers and padding included: the decompression-bomb ceiling.
	MaxStreamBytes int64
	// MaxFileBytes bounds the bytes this layer may write into the root
	// filesystem: file contents plus hard links materialized as copies.
	MaxFileBytes int64
	// MaxEntries bounds the tar entries (files, directories, links).
	MaxEntries int64
	// CheckSpace is called after every spaceCheckInterval bytes written
	// and before a hard-link copy; an error aborts the layer.
	CheckSpace func() error
}

// spaceCheckInterval is how many written bytes pass between CheckSpace calls.
const spaceCheckInterval = 32 << 20

// LimitError is returned when a layer exceeds one of its Limits.
type LimitError struct {
	What  string // "uncompressed size", "extracted size" or "entry count"
	Limit int64
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("layer exceeds the %s limit of %d", e.What, e.Limit)
}

// IsLimit reports whether err is, or wraps, a LimitError.
func IsLimit(err error) bool {
	var le *LimitError
	return errors.As(err, &le)
}

// streamLimiter fails reads once more than max bytes were delivered.
type streamLimiter struct {
	r   io.Reader
	n   int64
	max int64
}

func (s *streamLimiter) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	s.n += int64(n)
	if s.n > s.max {
		return n, &LimitError{What: "uncompressed size", Limit: s.max}
	}
	return n, err
}

// spaceWriter counts written bytes and polls CheckSpace.
type spaceWriter struct {
	w     io.Writer
	a     *applier
	since int64
}

func (s *spaceWriter) Write(p []byte) (int, error) {
	n, err := s.w.Write(p)
	s.since += int64(n)
	if err == nil && s.a.lim.CheckSpace != nil && s.since >= spaceCheckInterval {
		s.since = 0
		err = s.a.lim.CheckSpace()
	}
	return n, err
}
