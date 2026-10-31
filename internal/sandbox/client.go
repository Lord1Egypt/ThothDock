package sandbox

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

// Frame types the client sends: stdin data, then one end-of-input frame.
const (
	StdinFrame    byte = 0
	StdinEOFFrame byte = 4
)

func writeFrame(w io.Writer, typ byte, p []byte) error {
	var hdr [8]byte
	hdr[0] = typ
	binary.BigEndian.PutUint32(hdr[4:], uint32(len(p)))
	_, err := w.Write(append(hdr[:], p...))
	return err
}

// ReadClientFrames delivers the client's stdin frames to in (closing it at
// end of input unless keepOpen) and returns when the connection ends.
func ReadClientFrames(r io.Reader, in io.WriteCloser, keepOpen bool) {
	var hdr [8]byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return
		}
		n := binary.BigEndian.Uint32(hdr[4:])
		if n > 1<<20 {
			return
		}
		switch hdr[0] {
		case StdinFrame:
			if in != nil {
				if _, err := io.CopyN(in, r, int64(n)); err != nil {
					in = nil // the process closed its stdin; keep reading frames
				}
			} else if _, err := io.CopyN(io.Discard, r, int64(n)); err != nil {
				return
			}
		case StdinEOFFrame:
			if in != nil && !keepOpen {
				in.Close()
			}
			in = nil
		default:
			return
		}
	}
}

// Started is what the engine reports once the sandbox process runs.
type Started struct {
	Pid      int      `json:"pid"`
	Warnings []string `json:"warnings"`
}

// Run sends req over conn (a fresh connection to the engine's socket), then
// relays stdin to the sandbox and its output to stdout and stderr until it
// ends. onStart is called with the sandbox's pid before any output is
// awaited further. It returns the sandbox's exit code.
func Run(conn net.Conn, req *Request, stdin io.Reader, stdout, stderr io.Writer, onStart func(Started)) (int, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return -1, err
	}
	hdr := "POST /thothdock/sandboxes HTTP/1.1\r\nHost: thothdock\r\nUpgrade: tcp\r\nConnection: Upgrade\r\n" +
		"Content-Type: application/json\r\nContent-Length: " + fmt.Sprint(len(body)) + "\r\n\r\n"
	if _, err := conn.Write(append([]byte(hdr), body...)); err != nil {
		return -1, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		return -1, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols && resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		var e struct{ Message string }
		if json.Unmarshal(msg, &e) == nil && e.Message != "" {
			return -1, errors.New(e.Message)
		}
		return -1, fmt.Errorf("engine answered %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	// Stdin goes in frames ending with an end-of-input frame: the
	// connection itself stays open, so its end always means the client went
	// away (which kills a --die-with-parent sandbox).
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := stdin.Read(buf)
			if n > 0 && writeFrame(conn, StdinFrame, buf[:n]) != nil {
				return
			}
			if err != nil {
				writeFrame(conn, StdinEOFFrame, nil)
				return
			}
		}
	}()
	var head [8]byte
	for {
		if _, err := io.ReadFull(br, head[:]); err != nil {
			return -1, fmt.Errorf("sandbox stream ended without an exit status: %w", err)
		}
		n := binary.BigEndian.Uint32(head[4:])
		switch head[0] {
		case 1, 2:
			w := stdout
			if head[0] == 2 {
				w = stderr
			}
			if _, err := io.CopyN(w, br, int64(n)); err != nil {
				return -1, err
			}
		case 3:
			if n > 1<<20 {
				return -1, errors.New("sandbox control message too large")
			}
			msg := make([]byte, n)
			if _, err := io.ReadFull(br, msg); err != nil {
				return -1, err
			}
			var c struct {
				Pid      *int     `json:"pid"`
				Warnings []string `json:"warnings"`
				Exit     *int     `json:"exit"`
			}
			if err := json.NewDecoder(bytes.NewReader(msg)).Decode(&c); err != nil {
				return -1, err
			}
			if c.Pid != nil && onStart != nil {
				onStart(Started{Pid: *c.Pid, Warnings: c.Warnings})
			}
			if c.Exit != nil {
				return *c.Exit, nil
			}
		default:
			return -1, fmt.Errorf("unknown sandbox frame type %d", head[0])
		}
	}
}
