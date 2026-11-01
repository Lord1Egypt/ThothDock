package engine

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Lord1Egypt/ThothDock/internal/errdefs"
	"github.com/Lord1Egypt/ThothDock/internal/runtime"
	"github.com/Lord1Egypt/ThothDock/internal/sandbox"
	"github.com/Lord1Egypt/ThothDock/internal/securefs"
)

// Sandbox is a running Flatpak (bwrap) sandbox: a process the engine runs
// in a root built from the request, like a container without an image.
type Sandbox struct {
	ID      string
	AppID   string // FLATPAK_ID, or "snap:<snap>.<app>" for a snap, if any
	Argv    []string
	NetIP   string
	Started time.Time

	proc runtime.Process
	done chan struct{}
	mu   sync.Mutex
	exit int
}

// Pid is the sandbox process.
func (s *Sandbox) Pid() int { return s.proc.Pid() }

// Stdin is the process's standard input.
func (s *Sandbox) Stdin() io.WriteCloser { return s.proc.Stdin() }

// Resize sets the sandbox's terminal size; a no-op without a TTY.
func (s *Sandbox) Resize(w, h uint16) error { return s.proc.Resize(w, h) }

// Kill ends the sandbox and everything it started.
func (s *Sandbox) Kill() error { return s.proc.Kill() }

// Done is closed when the sandbox has ended; ExitCode is then valid.
func (s *Sandbox) Done() <-chan struct{} { return s.done }

// ExitCode is the exit status (128+signal when killed).
func (s *Sandbox) ExitCode() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exit
}

// SandboxInfo describes a running sandbox (for the Apps view and tests).
type SandboxInfo struct {
	ID      string
	AppID   string
	Pid     int
	Argv    []string
	NetIP   string
	Started time.Time
}

// RunSandbox builds and starts a sandbox. Its output goes to stdout and
// stderr; warnings name what PRoot cannot honour.
func (e *Engine) RunSandbox(req *sandbox.Request, stdout, stderr io.Writer) (*Sandbox, []string, error) {
	if e.cfg.GuestRoot == "" {
		return nil, nil, errdefs.Forbidden("sandboxes need the guest's root file system (thothdock serve --guest-root)")
	}
	if req.UID < 0 || req.UID > 65535 || req.GID < 0 || req.GID > 65535 {
		return nil, nil, errdefs.Invalid("sandbox: uid/gid out of range")
	}
	guest, err := securefs.OpenRoot(e.cfg.GuestRoot)
	if err != nil {
		return nil, nil, err
	}
	defer guest.Close()
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, nil, err
	}
	id := hex.EncodeToString(raw[:])
	base := filepath.Join(e.Layout.Root, "sandboxes", id)
	dir := filepath.Join(base, "root")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	cleanup := func() { securefs.RemoveTree(base) }
	prep, err := sandbox.Prepare(req, guest, dir)
	if err != nil {
		cleanup()
		return nil, nil, errdefs.Invalid("%v", err)
	}
	var ip string
	if e.supportsNetIP() {
		// The same pool as containers on user networks: one address can
		// never belong to two workloads.
		if ip, err = e.addrs.Allocate("sandbox:" + id); err != nil {
			cleanup()
			return nil, nil, errdefs.Conflict("sandbox: no loopback address left: %v", err)
		}
	}
	binds := make([]runtime.Bind, len(prep.Binds))
	for i, b := range prep.Binds {
		binds[i] = runtime.Bind{Source: b.Source, Target: b.Target}
	}
	proc, err := e.Runtime.Start(runtime.Spec{
		ID: id, Rootfs: prep.Root, Args: prep.Argv, Env: prep.Env, Cwd: prep.Cwd,
		UID: req.UID, GID: req.GID, Binds: binds, Tty: req.Tty, OpenStdin: true,
		NetIP: ip, Stdout: stdout, Stderr: stderr,
	})
	if err != nil {
		if ip != "" {
			e.addrs.Release(ip)
		}
		cleanup()
		return nil, nil, err
	}
	if req.Tty && req.Width > 0 && req.Height > 0 {
		proc.Resize(req.Width, req.Height) // a PTY starts at 0x0: TUIs would draw nothing
	}
	appID := envValue(prep.Env, "FLATPAK_ID")
	if snapApp := envValue(prep.Env, "THOTHDOCK_SNAP_APP"); appID == "" && snapApp != "" {
		appID = "snap:" + snapApp
	}
	s := &Sandbox{ID: id, AppID: appID, Argv: prep.Argv, NetIP: ip, Started: time.Now(), proc: proc, done: make(chan struct{})}
	e.mu.Lock()
	e.sandboxes[id] = s
	e.mu.Unlock()
	if len(prep.Warnings) > 0 {
		e.log.Info("sandbox started with limits", "id", id[:12], "argv0", prep.Argv[0], "warnings", prep.Warnings)
	}
	go func() {
		ex := proc.Wait()
		s.mu.Lock()
		s.exit = ex.Code
		s.mu.Unlock()
		e.mu.Lock()
		delete(e.sandboxes, id)
		e.mu.Unlock()
		if ip != "" {
			e.addrs.Release(ip)
		}
		cleanup()
		close(s.done)
	}()
	return s, prep.Warnings, nil
}

// Sandboxes lists the running sandboxes, oldest first.
func (e *Engine) Sandboxes() []SandboxInfo {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]SandboxInfo, 0, len(e.sandboxes))
	for _, s := range e.sandboxes {
		out = append(out, SandboxInfo{ID: s.ID, AppID: s.AppID, Pid: s.Pid(), Argv: s.Argv, NetIP: s.NetIP, Started: s.Started})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out
}

// killSandboxes ends every sandbox; the daemon is stopping.
func (e *Engine) killSandboxes() {
	e.mu.Lock()
	list := make([]*Sandbox, 0, len(e.sandboxes))
	for _, s := range e.sandboxes {
		list = append(list, s)
	}
	e.mu.Unlock()
	for _, s := range list {
		s.Kill()
		<-s.done
	}
}

func envValue(env []string, key string) string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			return v
		}
	}
	return ""
}
