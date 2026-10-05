package api

import (
	"io"
	"net/http"
	"strings"

	"github.com/Lord1Egypt/ThothDock/internal/engine"
	"github.com/Lord1Egypt/ThothDock/internal/errdefs"
)

func (s *Server) execCreate(w http.ResponseWriter, r *http.Request) {
	var cfg engine.ExecConfig
	if err := decodeBody(r, &cfg); err != nil {
		writeError(w, err)
		return
	}
	id, err := s.Engine.ExecCreate(r.PathValue("id"), cfg)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"Id": id})
}

func (s *Server) execInspect(w http.ResponseWriter, r *http.Request) {
	x, err := s.Engine.ExecGet(r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	st := x.Snapshot()
	var exit any
	if st.ExitCode != nil {
		exit = *st.ExitCode
	}
	cmd := []string(st.Config.Cmd)
	writeJSON(w, http.StatusOK, map[string]any{
		"ID": st.ID, "Running": st.Running, "ExitCode": exit, "ProcessConfig": map[string]any{
			"privileged": false, "user": st.Config.User, "tty": st.Config.Tty,
			"entrypoint": cmd[0], "arguments": cmd[1:],
		},
		"OpenStdin": st.Config.AttachStdin, "OpenStderr": st.Config.AttachStderr, "OpenStdout": st.Config.AttachStdout,
		"CanRemove": !st.Running, "ContainerID": st.ContainerID, "DetachKeys": "", "Pid": st.Pid,
	})
}

func (s *Server) execResize(w http.ResponseWriter, r *http.Request) {
	x, err := s.Engine.ExecGet(r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	h, err1 := intParam(r, "h")
	wd, err2 := intParam(r, "w")
	if err1 != nil || err2 != nil || h == nil || wd == nil {
		writeError(w, errdefs.Invalid("resize needs integer h and w"))
		return
	}
	if err := x.Resize(*h, *wd); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// execStart runs the exec. Detached, it answers 200 and lets the process run.
// Attached, it hijacks the connection like attach: Docker's multiplexed
// stream, or raw bytes with a TTY, closed after the process ended and its
// exit code was recorded (the CLI reads it from /exec/{id}/json next).
func (s *Server) execStart(w http.ResponseWriter, r *http.Request) {
	x, err := s.Engine.ExecGet(r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	var body struct {
		Detach bool `json:"Detach"`
		Tty    bool `json:"Tty"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeError(w, err)
		return
	}
	st := x.Snapshot()
	if body.Detach {
		if err := s.Engine.ExecStart(x, io.Discard, io.Discard); err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		writeError(w, errdefs.Invalid("connection cannot be hijacked"))
		return
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	ctype := "application/vnd.docker.raw-stream"
	out := &frameWriter{w: conn, tty: st.Config.Tty}
	stdout, stderr := io.Writer(out.stream("stdout")), io.Writer(out.stream("stderr"))
	if !st.Config.AttachStdout {
		stdout = io.Discard
	}
	if !st.Config.AttachStderr {
		stderr = io.Discard
	}
	// Upgrade first, as dockerd does: the client only understands an
	// upgrade response, and reads a start failure from the stream and the
	// exec's exit code (126) from /exec/{id}/json.
	if strings.EqualFold(r.Header.Get("Upgrade"), "tcp") {
		buf.WriteString("HTTP/1.1 101 UPGRADED\r\nContent-Type: " + ctype + "\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
	} else {
		buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: " + ctype + "\r\n\r\n")
	}
	if buf.Flush() != nil {
		return
	}
	if err := s.Engine.ExecStart(x, stdout, stderr); err != nil {
		out.write("stderr", []byte("ThothDock exec failed: "+err.Error()+"\r\n"))
		return
	}
	if st.Config.AttachStdin {
		go func() {
			if in := x.Stdin(); in != nil {
				io.Copy(in, buf.Reader)
				if !st.Config.Tty {
					in.Close() // client EOF: the process sees end of input
				}
			}
		}()
	}
	// A client that vanishes ends the exec's output, not the exec; the
	// process keeps running until done, as under dockerd.
	<-x.Done()
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	}
}
