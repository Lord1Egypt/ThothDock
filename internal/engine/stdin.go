package engine

import (
	"errors"
	"io"
	"sync"
)

var errRunEnded = errors.New("container process ended")

// stdinBroker carries attach clients' input to the process of one run. An
// attach may come before start (docker run does that); its input waits in
// the pipe until the process exists.
type stdinBroker struct {
	pr     *io.PipeReader
	pw     *io.PipeWriter
	mu     sync.Mutex
	closed bool
}

func newStdinBroker() *stdinBroker {
	pr, pw := io.Pipe()
	return &stdinBroker{pr: pr, pw: pw}
}

// Write is used by attach clients.
func (b *stdinBroker) Write(p []byte) (int, error) { return b.pw.Write(p) }

// CloseInput ends the process's input (StdinOnce, or client EOF).
func (b *stdinBroker) CloseInput() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed {
		b.closed = true
		b.pw.Close()
	}
}

// pump copies into the process's stdin until input ends or the run ends.
func (b *stdinBroker) pump(dst io.WriteCloser) {
	io.Copy(dst, b.pr)
	dst.Close()
}

// end releases any writer or pump still blocked.
func (b *stdinBroker) end() {
	b.pr.CloseWithError(errRunEnded)
	b.pw.CloseWithError(errRunEnded)
}
