package api

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/Lord1Egypt/ThothDock/internal/errdefs"
	"github.com/Lord1Egypt/ThothDock/internal/sandbox"
)

// maxSandboxBody bounds a sandbox request: the operations plus the
// --file/--bind-data contents the shim inlines.
const maxSandboxBody = sandbox.MaxFileBytes*4/3 + 4<<20

// controlStream is the frame type of the sandbox protocol's control
// messages: {"pid":N,"warnings":[...]} first, {"exit":N} last.
const controlStream = 3

// runSandbox is POST /thothdock/sandboxes, used by the guest's bwrap shim.
// The body is a sandbox.Request; the connection is then hijacked as for
// docker exec: stdout and stderr in Docker's multiplexed framing (also with
// a TTY), control frames of type 3; the client sends stdin as frames ending
// with an end-of-input frame. A client that goes away kills a
// --die-with-parent sandbox.
func (s *Server) runSandbox(w http.ResponseWriter, r *http.Request) {
	var req sandbox.Request
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSandboxBody))
	if err := dec.Decode(&req); err != nil {
		writeError(w, errdefs.Invalid("sandbox request: %v", err))
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		writeError(w, errdefs.Invalid("connection cannot be hijacked"))
		return
	}
	out := &sandboxFrames{}
	sb, warnings, err := s.Engine.RunSandbox(&req, out.stream(1), out.stream(2))
	if err != nil {
		writeError(w, err)
		return
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		sb.Kill()
		return
	}
	defer conn.Close()
	if strings.EqualFold(r.Header.Get("Upgrade"), "tcp") {
		buf.WriteString("HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.multiplexed-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
	} else {
		buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/vnd.docker.multiplexed-stream\r\n\r\n")
	}
	if buf.Flush() != nil {
		sb.Kill()
		return
	}
	out.attach(conn)
	if out.lost {
		warnings = append(warnings, "output printed before the connection was ready exceeded 1 MiB and was dropped")
	}
	writeControl(out, map[string]any{"pid": sb.Pid(), "warnings": warnings})

	clientGone := make(chan struct{})
	go func() {
		// Stdin arrives in frames; the connection ending means the client
		// went away.
		sandbox.ReadClientFrames(buf.Reader, sb.Stdin(), req.Tty, func(w, h uint16) { sb.Resize(w, h) })
		close(clientGone)
	}()
	select {
	case <-sb.Done():
	case <-clientGone:
		if req.DieWithParent {
			sb.Kill()
		}
		<-sb.Done()
	}
	writeControl(out, map[string]any{"exit": sb.ExitCode()})
	if uc, ok := conn.(*net.UnixConn); ok {
		uc.CloseWrite()
	}
}

func writeControl(out *sandboxFrames, msg map[string]any) {
	b, _ := json.Marshal(msg)
	out.frame(controlStream, b)
}

// maxPendingOutput bounds the output kept while the connection is not yet
// attached (the process may print before the response is written).
const maxPendingOutput = 1 << 20

// sandboxFrames writes the sandbox protocol's frames: an 8-byte header
// [type,0,0,0,size BE32] before each payload, always, also with a TTY.
// Until attach, frames are kept in memory.
type sandboxFrames struct {
	mu      sync.Mutex
	w       io.Writer
	pending []byte
	lost    bool
}

func (f *sandboxFrames) frame(typ byte, p []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for len(p) > 0 {
		n := len(p)
		if n > 1<<20 {
			n = 1 << 20
		}
		var hdr [8]byte
		hdr[0] = typ
		binary.BigEndian.PutUint32(hdr[4:], uint32(n))
		if f.w == nil {
			if len(f.pending)+8+n > maxPendingOutput {
				f.lost = true
				return nil
			}
			f.pending = append(append(f.pending, hdr[:]...), p[:n]...)
		} else {
			// A client that left (a detached service) stops getting output;
			// the sandbox must not fail on its own stdout because of that.
			if _, err := f.w.Write(hdr[:]); err != nil {
				f.w = io.Discard
				return nil
			}
			if _, err := f.w.Write(p[:n]); err != nil {
				f.w = io.Discard
				return nil
			}
		}
		p = p[n:]
	}
	return nil
}

// attach sends what was kept and writes directly from then on.
func (f *sandboxFrames) attach(w io.Writer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.w = w
	w.Write(f.pending)
	f.pending = nil
}

type sandboxStream struct {
	f   *sandboxFrames
	typ byte
}

func (s sandboxStream) Write(p []byte) (int, error) {
	if err := s.f.frame(s.typ, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (f *sandboxFrames) stream(typ byte) io.Writer { return sandboxStream{f, typ} }

// listApps is GET /thothdock/apps: installed Flatpak apps with their running
// sandboxes (the Containers screen's Apps tab and the Web Panel).
func (s *Server) listApps(w http.ResponseWriter, r *http.Request) {
	writeBody(w, s.Engine.Apps())
}

// killSandbox is POST /thothdock/sandboxes/{id}/kill (the Apps tab's Stop).
func (s *Server) killSandbox(w http.ResponseWriter, r *http.Request) {
	if err := s.Engine.KillSandbox(r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// forgetSnapAddress is DELETE /thothdock/snaps/{name}/address: a removed
// snap gives its loopback address back (snap remove).
func (s *Server) forgetSnapAddress(w http.ResponseWriter, r *http.Request) {
	s.Engine.ForgetSnapAddress(r.PathValue("name"))
	w.WriteHeader(http.StatusNoContent)
}
