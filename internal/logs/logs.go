// Package logs stores container output in Docker's json-file format with
// size-based rotation, and fans live output out to attach and follow
// clients.
package logs

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"sync"
	"time"
)

// Entry is one log line ({"log":...,"stream":...,"time":...}).
type Entry struct {
	Log    string    `json:"log"`
	Stream string    `json:"stream"`
	Time   time.Time `json:"time"`
}

// Chunk is raw output as the process wrote it.
type Chunk struct {
	Stream string
	Data   []byte
}

// maxLine splits very long lines like Docker does (16 KiB).
const maxLine = 16 * 1024

// subBuffer is how far a live client may fall behind before it is cut off,
// so a stalled client can never block the container's output.
const subBuffer = 4096

// Logger is one container's log.
type Logger struct {
	mu      sync.Mutex
	path    string
	maxSize int64
	f       *os.File
	size    int64
	partial map[string][]byte
	chunks  map[chan Chunk]struct{}
	entries map[chan Entry]struct{}
	now     func() time.Time
}

// Open opens (appending) the log at path, rotating at maxSize bytes into
// path.1 (one rotated file is kept).
func Open(path string, maxSize int64) (*Logger, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &Logger{path: path, maxSize: maxSize, f: f, size: fi.Size(), partial: map[string][]byte{},
		chunks: map[chan Chunk]struct{}{}, entries: map[chan Entry]struct{}{}, now: time.Now}, nil
}

// Path is the current log file.
func (l *Logger) Path() string { return l.path }

type streamWriter struct {
	l      *Logger
	stream string
}

// Writer returns the writer for "stdout" or "stderr".
func (l *Logger) Writer(stream string) io.Writer { return &streamWriter{l: l, stream: stream} }

func (w *streamWriter) Write(p []byte) (int, error) {
	l := w.l
	l.mu.Lock()
	defer l.mu.Unlock()
	data := append([]byte(nil), p...)
	for ch := range l.chunks {
		select {
		case ch <- Chunk{Stream: w.stream, Data: data}:
		default:
			delete(l.chunks, ch)
			close(ch)
		}
	}
	buf := append(l.partial[w.stream], p...)
	for {
		i := indexNL(buf)
		if i < 0 && len(buf) < maxLine {
			break
		}
		n := i + 1
		if i < 0 || n > maxLine {
			n = maxLine
		}
		l.emitLocked(Entry{Log: string(buf[:n]), Stream: w.stream, Time: l.now().UTC()})
		buf = buf[n:]
	}
	l.partial[w.stream] = append([]byte(nil), buf...)
	return len(p), nil
}

func indexNL(b []byte) int {
	for i, c := range b {
		if c == '\n' {
			return i
		}
	}
	return -1
}

func (l *Logger) emitLocked(e Entry) {
	line, err := json.Marshal(e)
	if err != nil {
		return
	}
	line = append(line, '\n')
	if l.maxSize > 0 && l.size+int64(len(line)) > l.maxSize && l.size > 0 {
		l.rotateLocked()
	}
	if n, err := l.f.Write(line); err == nil {
		l.size += int64(n)
	}
	for ch := range l.entries {
		select {
		case ch <- e:
		default:
			delete(l.entries, ch)
			close(ch)
		}
	}
}

func (l *Logger) rotateLocked() {
	l.f.Close()
	os.Rename(l.path, l.path+".1")
	f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		// Keep writing nowhere rather than crash the container's output path.
		f, _ = os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	}
	l.f, l.size = f, 0
}

// EndRun flushes partial lines and ends every live subscription: the
// process exited and its output is complete.
func (l *Logger) EndRun() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range []string{"stdout", "stderr"} {
		if b := l.partial[s]; len(b) > 0 {
			l.emitLocked(Entry{Log: string(b), Stream: s, Time: l.now().UTC()})
		}
		delete(l.partial, s)
	}
	l.f.Sync()
	for ch := range l.chunks {
		close(ch)
	}
	for ch := range l.entries {
		close(ch)
	}
	l.chunks = map[chan Chunk]struct{}{}
	l.entries = map[chan Entry]struct{}{}
}

// SubscribeChunks receives raw output until EndRun or Unsubscribe.
func (l *Logger) SubscribeChunks() chan Chunk {
	ch := make(chan Chunk, subBuffer)
	l.mu.Lock()
	l.chunks[ch] = struct{}{}
	l.mu.Unlock()
	return ch
}

// UnsubscribeChunks stops a raw subscription.
func (l *Logger) UnsubscribeChunks(ch chan Chunk) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.chunks[ch]; ok {
		delete(l.chunks, ch)
		close(ch)
	}
}

// UnsubscribeEntries stops a follow subscription.
func (l *Logger) UnsubscribeEntries(ch chan Entry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.entries[ch]; ok {
		delete(l.entries, ch)
		close(ch)
	}
}

// ReadOptions filter stored entries.
type ReadOptions struct {
	Since, Until time.Time
	Tail         int // < 0: all
	Follow       bool
}

// Read returns stored entries and, when following, a channel of new ones
// that starts exactly where the stored ones end. follow must be false
// unless the container is running.
func (l *Logger) Read(opts ReadOptions) ([]Entry, chan Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var all []Entry
	for _, p := range []string{l.path + ".1", l.path} {
		es, err := readFile(p)
		if err != nil {
			return nil, nil, err
		}
		all = append(all, es...)
	}
	var out []Entry
	for _, e := range all {
		if !opts.Since.IsZero() && e.Time.Before(opts.Since) {
			continue
		}
		if !opts.Until.IsZero() && e.Time.After(opts.Until) {
			continue
		}
		out = append(out, e)
	}
	if opts.Tail >= 0 && len(out) > opts.Tail {
		out = out[len(out)-opts.Tail:]
	}
	if !opts.Follow {
		return out, nil, nil
	}
	ch := make(chan Entry, subBuffer)
	l.entries[ch] = struct{}{}
	return out, ch, nil
}

func readFile(p string) ([]Entry, error) {
	f, err := os.Open(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out, sc.Err()
}

// Close closes the file.
func (l *Logger) Close() error {
	l.EndRun()
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}

// maxTailBytes bounds the memory a tail request may use, whatever N is.
const maxTailBytes = 8 << 20

// Scan streams the stored entries matching opts to emit, oldest first, without
// holding them all in memory:
//
//   - with opts.Tail >= 0 only the newest entries are kept (at most Tail, and at
//     most maxTailBytes of text), then emitted;
//   - otherwise each entry is emitted as it is read.
//
// The logger's lock is held only to open the files and, when following, to
// subscribe -- never while emit runs -- so a slow client cannot stall the
// container's output. The returned channel (opts.Follow) starts exactly where
// the scan ends.
func (l *Logger) Scan(opts ReadOptions, emit func(Entry) error) (chan Entry, error) {
	l.mu.Lock()
	var files []*os.File
	var limits []int64
	closeAll := func() {
		for _, f := range files {
			f.Close()
		}
	}
	for i, p := range []string{l.path + ".1", l.path} {
		f, err := os.Open(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			closeAll()
			l.mu.Unlock()
			return nil, err
		}
		files = append(files, f)
		limit := int64(-1) // the rotated file is complete
		if i == 1 {
			limit = l.size // the live file: only what exists now
		}
		limits = append(limits, limit)
	}
	var live chan Entry
	if opts.Follow {
		live = make(chan Entry, subBuffer)
		l.entries[live] = struct{}{}
	}
	l.mu.Unlock()
	defer closeAll()

	keep := func(e Entry) bool {
		return (opts.Since.IsZero() || !e.Time.Before(opts.Since)) && (opts.Until.IsZero() || !e.Time.After(opts.Until))
	}
	var ring []Entry
	ringBytes := 0
	fail := func(err error) (chan Entry, error) {
		if live != nil {
			l.UnsubscribeEntries(live)
		}
		return nil, err
	}
	for i, f := range files {
		var r io.Reader = f
		if limits[i] >= 0 {
			r = io.LimitReader(f, limits[i])
		}
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		for sc.Scan() {
			var e Entry
			if json.Unmarshal(sc.Bytes(), &e) != nil || !keep(e) {
				continue
			}
			if opts.Tail < 0 {
				if err := emit(e); err != nil {
					return fail(err)
				}
				continue
			}
			if opts.Tail == 0 {
				continue
			}
			ring = append(ring, e)
			ringBytes += len(e.Log) + 64
			for len(ring) > opts.Tail || ringBytes > maxTailBytes {
				ringBytes -= len(ring[0].Log) + 64
				ring = ring[1:]
			}
		}
		if err := sc.Err(); err != nil {
			return fail(err)
		}
	}
	for _, e := range ring {
		if err := emit(e); err != nil {
			return fail(err)
		}
	}
	return live, nil
}
