package api

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Lord1Egypt/ThothDock/internal/engine"
	"github.com/Lord1Egypt/ThothDock/internal/errdefs"
	"github.com/Lord1Egypt/ThothDock/internal/logs"
)

func writeBody(w http.ResponseWriter, v any) {
	json.NewEncoder(w).Encode(v)
}

// frameWriter writes Docker's stream format: raw for a TTY, otherwise
// 8-byte headers [stream,0,0,0,size(BE32)] before each payload.
type frameWriter struct {
	mu  sync.Mutex
	w   io.Writer
	tty bool
}

func streamType(s string) byte {
	if s == "stderr" {
		return 2
	}
	return 1
}

func (f *frameWriter) write(stream string, p []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tty {
		_, err := f.w.Write(p)
		return err
	}
	for len(p) > 0 {
		n := len(p)
		if n > math.MaxUint32 {
			n = math.MaxUint32
		}
		var hdr [8]byte
		hdr[0] = streamType(stream)
		binary.BigEndian.PutUint32(hdr[4:], uint32(n))
		if _, err := f.w.Write(hdr[:]); err != nil {
			return err
		}
		if _, err := f.w.Write(p[:n]); err != nil {
			return err
		}
		p = p[n:]
	}
	return nil
}

// stream adapts one named stream of f to an io.Writer.
func (f *frameWriter) stream(name string) io.Writer { return streamAdapter{f, name} }

type streamAdapter struct {
	f    *frameWriter
	name string
}

func (a streamAdapter) Write(p []byte) (int, error) {
	if err := a.f.write(a.name, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func wanted(stream string, stdout, stderr bool) bool {
	return stream == "stdout" && stdout || stream == "stderr" && stderr
}

func (s *Server) attachContainer(w http.ResponseWriter, r *http.Request) {
	c, streams, err := s.Engine.Attach(r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	wantIn, wantOut, wantErr := boolParam(r, "stdin"), boolParam(r, "stdout"), boolParam(r, "stderr")
	stream, replay := boolParam(r, "stream"), boolParam(r, "logs")
	if !stream && !replay {
		writeError(w, errdefs.Invalid("either stream or logs must be true"))
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		writeError(w, errdefs.Invalid("connection cannot be hijacked"))
		return
	}
	logger := c.Logger()
	var chunks chan logs.Chunk
	if stream {
		// Subscribe before answering so no output is lost in between.
		chunks = logger.SubscribeChunks()
		defer logger.UnsubscribeChunks(chunks)
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	ctype := "application/vnd.docker.raw-stream"
	if strings.EqualFold(r.Header.Get("Upgrade"), "tcp") {
		buf.WriteString("HTTP/1.1 101 UPGRADED\r\nContent-Type: " + ctype + "\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
	} else {
		buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: " + ctype + "\r\n\r\n")
	}
	if err := buf.Flush(); err != nil {
		return
	}
	out := &frameWriter{w: conn, tty: streams.Tty}
	if replay {
		entries, _, err := logger.Read(logs.ReadOptions{Tail: -1})
		if err == nil {
			for _, e := range entries {
				if wanted(e.Stream, wantOut, wantErr) {
					out.write(e.Stream, []byte(e.Log))
				}
			}
		}
	}
	if !stream {
		return
	}
	clientGone := make(chan struct{})
	go func() {
		defer close(clientGone)
		if wantIn && streams.Stdin != nil {
			// buf.Reader may hold input sent along with the request.
			io.Copy(streams.Stdin, buf.Reader)
			if streams.StdinOnce {
				streams.Stdin.CloseInput()
			}
		}
		// Without stdin, keep reading only to learn when the client leaves.
		io.Copy(io.Discard, buf.Reader)
	}()
	for {
		select {
		case ch, ok := <-chunks:
			if !ok {
				// The run ended and every byte was delivered.
				if cw, ok := conn.(interface{ CloseWrite() error }); ok {
					cw.CloseWrite()
				}
				waitBriefly(clientGone)
				return
			}
			if wanted(ch.Stream, wantOut, wantErr) {
				if err := out.write(ch.Stream, ch.Data); err != nil {
					return
				}
			}
		case <-clientGone:
			// A half-close (no more stdin) is normal: the CLI does it at
			// once when it sends no input. Output continues until the run
			// ends; a client that really left shows up as a write error.
			clientGone = nil
		}
	}
}

func waitBriefly(ch chan struct{}) {
	if ch == nil {
		return
	}
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
	}
}

// parseTime reads Docker's "seconds[.nanoseconds]" log timestamps.
func parseTime(v string) (time.Time, error) {
	if v == "" || v == "0" {
		return time.Time{}, nil
	}
	secStr, nsStr, _ := strings.Cut(v, ".")
	sec, err := strconv.ParseInt(secStr, 10, 64)
	if err != nil {
		return time.Time{}, errdefs.Invalid("invalid timestamp %q", v)
	}
	var ns int64
	if nsStr != "" {
		nsStr = (nsStr + "000000000")[:9]
		if ns, err = strconv.ParseInt(nsStr, 10, 64); err != nil {
			return time.Time{}, errdefs.Invalid("invalid timestamp %q", v)
		}
	}
	return time.Unix(sec, ns), nil
}

func (s *Server) containerLogs(w http.ResponseWriter, r *http.Request) {
	c, err := s.Engine.Lookup(r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	q := r.URL.Query()
	stdout, stderr := boolParam(r, "stdout"), boolParam(r, "stderr")
	if !stdout && !stderr {
		writeError(w, errdefs.Invalid("Bad parameters: you must choose at least one stream"))
		return
	}
	since, err := parseTime(q.Get("since"))
	if err != nil {
		writeError(w, err)
		return
	}
	until, err := parseTime(q.Get("until"))
	if err != nil {
		writeError(w, err)
		return
	}
	tail := -1
	if t := q.Get("tail"); t != "" && t != "all" {
		n, err := strconv.Atoi(t)
		if err != nil || n < 0 {
			writeError(w, errdefs.Invalid("invalid tail %q", t))
			return
		}
		tail = n
	}
	rec := c.Snapshot()
	follow := boolParam(r, "follow") && engine.IsRunning(rec.State.Status) && until.IsZero()
	entries, live, err := c.Logger().Read(logs.ReadOptions{Since: since, Until: until, Tail: tail, Follow: follow})
	if err != nil {
		writeError(w, err)
		return
	}
	if live != nil {
		defer c.Logger().UnsubscribeEntries(live)
	}
	timestamps := boolParam(r, "timestamps")
	w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
	w.WriteHeader(http.StatusOK)
	bw := bufio.NewWriter(w)
	out := &frameWriter{w: bw, tty: rec.Config.Tty}
	emit := func(e logs.Entry) error {
		if !wanted(e.Stream, stdout, stderr) {
			return nil
		}
		line := e.Log
		if timestamps {
			line = e.Time.Format(time.RFC3339Nano) + " " + line
		}
		return out.write(e.Stream, []byte(line))
	}
	for _, e := range entries {
		if emit(e) != nil {
			return
		}
	}
	bw.Flush()
	flush(w)
	if live == nil {
		return
	}
	for {
		select {
		case e, ok := <-live:
			if !ok {
				return
			}
			if emit(e) != nil {
				return
			}
			bw.Flush()
			flush(w)
		case <-r.Context().Done():
			return
		}
	}
}

func flush(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

var _ net.Conn = (*net.UnixConn)(nil)
