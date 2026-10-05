package engine

import (
	"io"
	"sort"
	"sync"
	"time"

	"github.com/Lord1Egypt/ThothDock/internal/errdefs"
	"github.com/Lord1Egypt/ThothDock/internal/runtime"
)

// maxExecsPerContainer bounds the exec records kept for one container.
const maxExecsPerContainer = 256

// ExecConfig is the POST /containers/{id}/exec body (API v1.41).
type ExecConfig struct {
	AttachStdin  bool     `json:"AttachStdin"`
	AttachStdout bool     `json:"AttachStdout"`
	AttachStderr bool     `json:"AttachStderr"`
	DetachKeys   string   `json:"DetachKeys"`
	Tty          bool     `json:"Tty"`
	Env          []string `json:"Env"`
	Cmd          StrSlice `json:"Cmd"`
	Privileged   bool     `json:"Privileged"`
	User         string   `json:"User"`
	WorkingDir   string   `json:"WorkingDir"`
}

// Exec is one process started inside a running container's root filesystem
// and environment. It is a second PRoot tracee, not a member of the
// container's process tree: PRoot has no PID namespace, so `ps` inside the
// container does not list it and it cannot be signalled from there.
type Exec struct {
	ID          string
	ContainerID string
	Config      ExecConfig
	Created     time.Time

	mu       sync.Mutex
	started  bool
	running  bool
	exitCode int
	hasExit  bool
	pid      int
	proc     runtime.Process
	done     chan struct{}
}

// ExecState is a consistent snapshot of an Exec for the inspect endpoint.
type ExecState struct {
	ID, ContainerID string
	Config          ExecConfig
	Running         bool
	ExitCode        *int
	Pid             int
}

// Snapshot copies the exec's state.
func (x *Exec) Snapshot() ExecState {
	x.mu.Lock()
	defer x.mu.Unlock()
	st := ExecState{ID: x.ID, ContainerID: x.ContainerID, Config: x.Config, Running: x.running, Pid: x.pid}
	if x.hasExit {
		c := x.exitCode
		st.ExitCode = &c
	}
	return st
}

// Stdin is the process's input, nil unless the exec attached stdin.
func (x *Exec) Stdin() io.WriteCloser {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.proc == nil {
		return nil
	}
	return x.proc.Stdin()
}

// Resize sets the TTY size.
func (x *Exec) Resize(h, w int) error {
	if h <= 0 || w <= 0 || h > 0xffff || w > 0xffff {
		return errdefs.Invalid("invalid terminal size %dx%d", w, h)
	}
	x.mu.Lock()
	p := x.proc
	x.mu.Unlock()
	if p == nil {
		return errdefs.Conflict("exec instance %s is not running", x.ID[:12])
	}
	return p.Resize(uint16(w), uint16(h))
}

// Done is closed when the process has ended and its exit code is recorded.
func (x *Exec) Done() <-chan struct{} { return x.done }

// ExecCreate prepares an exec in a running container.
func (e *Engine) ExecCreate(ref string, cfg ExecConfig) (string, error) {
	if len(cfg.Cmd) == 0 {
		return "", errdefs.Invalid("No exec command specified")
	}
	if cfg.Privileged {
		return "", unsupported("privileged exec", "PRoot cannot grant kernel privileges")
	}
	if cfg.WorkingDir != "" && cfg.WorkingDir[0] != '/' {
		return "", errdefs.Invalid("the working directory '%s' is invalid, it needs to be an absolute path", cfg.WorkingDir)
	}
	for _, kv := range cfg.Env {
		if kv == "" || kv[0] == '=' {
			return "", errdefs.Invalid("invalid environment variable %q", kv)
		}
	}
	c, err := e.Lookup(ref)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.proc == nil {
		return "", errdefs.Conflict("container %s is not running", c.rec.ID)
	}
	if c.execs == nil {
		c.execs = map[string]*Exec{}
	}
	if len(c.execs) >= maxExecsPerContainer && !pruneFinishedExecs(c) {
		return "", errdefs.Conflict("too many exec instances for container %s", c.rec.ID[:12])
	}
	x := &Exec{ID: newID(), ContainerID: c.rec.ID, Config: cfg, Created: time.Now().UTC(), done: make(chan struct{})}
	c.execs[x.ID] = x
	e.mu.Lock()
	if e.execs == nil {
		e.execs = map[string]*Exec{}
	}
	e.execs[x.ID] = x
	e.mu.Unlock()
	return x.ID, nil
}

// pruneFinishedExecs drops the oldest finished exec; false when none finished.
func pruneFinishedExecs(c *Container) bool {
	var finished []*Exec
	for _, x := range c.execs {
		x.mu.Lock()
		if x.hasExit {
			finished = append(finished, x)
		}
		x.mu.Unlock()
	}
	if len(finished) == 0 {
		return false
	}
	sort.Slice(finished, func(i, j int) bool { return finished[i].Created.Before(finished[j].Created) })
	delete(c.execs, finished[0].ID)
	return true
}

// ExecGet finds an exec by its full ID.
func (e *Engine) ExecGet(id string) (*Exec, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	x, ok := e.execs[id]
	if !ok {
		return nil, errdefs.NotFound("No such exec instance: %s", id)
	}
	return x, nil
}

// ExecStart runs the exec. Output goes to stdout/stderr (io.Discard when the
// caller detaches). It returns once the process is started; Done reports the
// end.
func (e *Engine) ExecStart(x *Exec, stdout, stderr io.Writer) error {
	x.mu.Lock()
	if x.started {
		x.mu.Unlock()
		return errdefs.Conflict("exec instance %s has already been started", x.ID[:12])
	}
	x.started = true
	x.mu.Unlock()

	fail := func(err error) error {
		x.mu.Lock()
		x.running, x.exitCode, x.hasExit = false, 126, true
		x.mu.Unlock()
		close(x.done)
		return err
	}
	c, err := e.Lookup(x.ContainerID)
	if err != nil {
		return fail(err)
	}
	c.mu.Lock()
	if c.proc == nil {
		c.mu.Unlock()
		return fail(errdefs.Conflict("container %s is not running", x.ContainerID))
	}
	spec, err := e.buildSpecFor(c, procParams{
		User: x.Config.User, WorkDir: x.Config.WorkingDir, ExtraEnv: x.Config.Env,
		Argv: []string(x.Config.Cmd), Tty: x.Config.Tty, OpenStdin: x.Config.AttachStdin,
		Stdout: stdout, Stderr: stderr,
	})
	if err != nil {
		c.mu.Unlock()
		return fail(err)
	}
	spec.ID = x.ID
	proc, err := e.Runtime.Start(spec)
	if err != nil {
		c.mu.Unlock()
		return fail(err)
	}
	x.mu.Lock()
	x.proc, x.running, x.pid = proc, true, proc.Pid()
	x.mu.Unlock()
	c.mu.Unlock()
	go func() {
		ex := proc.Wait()
		x.mu.Lock()
		x.running, x.exitCode, x.hasExit = false, ex.Code, true
		x.mu.Unlock()
		close(x.done)
	}()
	return nil
}

// killExecsLocked ends every running exec of c; the container is stopping.
func (c *Container) killExecsLocked() {
	for _, x := range c.execs {
		x.mu.Lock()
		p := x.proc
		running := x.running
		x.mu.Unlock()
		if running && p != nil {
			p.Kill()
		}
	}
}

// forgetExecs drops a removed container's exec records.
func (e *Engine) forgetExecs(c *Container) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for id := range c.execs {
		delete(e.execs, id)
	}
}
