package engine

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Lord1Egypt/ThothDock/internal/errdefs"
	"github.com/Lord1Egypt/ThothDock/internal/runtime"
	"github.com/Lord1Egypt/ThothDock/internal/securefs"
)

// Start runs the container's process.
func (e *Engine) Start(ref string) error {
	c, err := e.Lookup(ref)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch c.rec.State.Status {
	case StatusRunning, StatusStarting:
		return errdefs.NotModified("container already started")
	case StatusRemoving:
		return errdefs.Conflict("container %s is marked for removal and cannot be started", c.rec.ID[:12])
	}
	spec, err := e.buildSpec(c)
	if err != nil {
		e.startFailed(c, err)
		return err
	}
	c.rec.State.Status = StatusStarting
	c.rec.State.Error = ""
	if err := e.persist(c); err != nil {
		return err
	}
	proc, err := e.Runtime.Start(spec)
	if err != nil {
		e.startFailed(c, err)
		return err
	}
	st := &c.rec.State
	st.Status = StatusRunning
	st.Pid = proc.Pid()
	st.PidStart = processStart(proc.Pid())
	st.StartedAt = time.Now().UTC()
	st.ExitCode = 0
	c.proc = proc
	c.runDone = make(chan struct{})
	if in := proc.Stdin(); in != nil {
		go c.stdin.pump(in)
	}
	if err := e.persist(c); err != nil {
		e.log.Error("persisting started container", "id", c.rec.ID, "err", err)
	}
	if c.rec.Config.Tty {
		if cs := c.rec.HostConfig.ConsoleSize; cs[0] > 0 && cs[1] > 0 {
			proc.Resize(uint16(cs[1]), uint16(cs[0]))
		}
	}
	e.log.Info("container started", "id", c.rec.ID[:12], "pid", st.Pid)
	go e.monitor(c, proc, c.runDone)
	return nil
}

// startFailed records a process that could not start the way Docker does:
// the container stays created, with an exit code and error; attached
// clients are released, and an auto-remove container is removed (docker
// run --rm waits for that). Called with c.mu held.
func (e *Engine) startFailed(c *Container, err error) {
	defer func() {
		c.logger.EndRun()
		if c.rec.HostConfig.AutoRemove {
			go func() {
				if err := e.Remove(c.rec.ID, true); err != nil {
					e.log.Error("auto-removing container after a failed start", "id", c.rec.ID, "err", err)
				}
			}()
		}
	}()
	st := &c.rec.State
	st.Status = StatusCreated
	st.Error = err.Error()
	st.ExitCode = 128
	if strings.Contains(st.Error, "executable file not found") || strings.Contains(st.Error, "no such file or directory") {
		st.ExitCode = 127
	} else if strings.Contains(st.Error, "permission denied") {
		st.ExitCode = 126
	}
	if perr := e.persist(c); perr != nil {
		e.log.Error("persisting failed start", "id", c.rec.ID, "err", perr)
	}
}

func (e *Engine) buildSpec(c *Container) (runtime.Spec, error) {
	rootfs := filepath.Join(c.dir, "rootfs")
	root, err := securefs.OpenRoot(rootfs)
	if err != nil {
		return runtime.Spec{}, err
	}
	defer root.Close()
	cfg := c.rec.Config
	user, err := resolveUser(root, cfg.User)
	if err != nil {
		return runtime.Spec{}, err
	}
	env := append([]string{}, cfg.Env...)
	env = mergeEnv([]string{"HOSTNAME=" + cfg.Hostname}, env)
	if !hasEnv(env, "HOME") {
		home := user.Home
		if home == "" {
			home = "/"
		}
		env = append(env, "HOME="+home)
	}
	if cfg.Tty && !hasEnv(env, "TERM") {
		env = append(env, "TERM=xterm")
	}
	exe, err := lookPath(root, c.rec.Path, cfg.WorkingDir, env)
	if err != nil {
		return runtime.Spec{}, err
	}
	binds := []runtime.Bind{
		{Source: filepath.Join(c.dir, "hosts"), Target: "/etc/hosts"},
		{Source: filepath.Join(c.dir, "hostname"), Target: "/etc/hostname"},
		{Source: filepath.Join(c.dir, "resolv.conf"), Target: "/etc/resolv.conf"},
	}
	for _, b := range c.rec.Binds {
		binds = append(binds, runtime.Bind{Source: b.Source, Target: b.Target})
	}
	return runtime.Spec{
		ID: c.rec.ID, Rootfs: rootfs,
		Args: append([]string{exe}, c.rec.Args...), Env: env, Cwd: cfg.WorkingDir,
		UID: user.UID, GID: user.GID, Binds: binds,
		Tty: cfg.Tty, OpenStdin: cfg.OpenStdin,
		Stdout: c.logger.Writer("stdout"), Stderr: c.logger.Writer("stderr"),
	}, nil
}

// monitor waits for the process, records its exit and wakes waiters.
func (e *Engine) monitor(c *Container, proc runtime.Process, done chan struct{}) {
	ex := proc.Wait()
	c.logger.EndRun()
	c.mu.Lock()
	st := &c.rec.State
	st.Status = StatusExited
	st.ExitCode = ex.Code
	st.FinishedAt = time.Now().UTC()
	st.Pid, st.PidStart = 0, 0
	c.proc = nil
	c.stdin.end()
	c.stdin = newStdinBroker()
	if err := e.persist(c); err != nil {
		e.log.Error("persisting exited container", "id", c.rec.ID, "err", err)
	}
	c.notifyLocked(WaitResult{StatusCode: ex.Code}, "not-running", "next-exit")
	autoRemove := c.rec.HostConfig.AutoRemove
	close(done)
	c.mu.Unlock()
	e.log.Info("container exited", "id", c.rec.ID[:12], "code", ex.Code)
	if autoRemove {
		if err := e.Remove(c.rec.ID, false); err != nil {
			e.log.Error("auto-removing container", "id", c.rec.ID, "err", err)
		}
	}
}

func (c *Container) notifyLocked(r WaitResult, conds ...string) {
	kept := c.waiters[:0]
	for _, w := range c.waiters {
		match := false
		for _, cond := range conds {
			match = match || w.cond == cond
		}
		if match {
			w.ch <- r
		} else {
			kept = append(kept, w)
		}
	}
	c.waiters = kept
}

// Wait registers for condition "not-running" (default), "next-exit" or
// "removed". The channel yields exactly one result.
func (e *Engine) Wait(ref, cond string) (<-chan WaitResult, func(), error) {
	if cond == "" {
		cond = "not-running"
	}
	if cond != "not-running" && cond != "next-exit" && cond != "removed" {
		return nil, nil, errdefs.Invalid("invalid condition: %q", cond)
	}
	c, err := e.Lookup(ref)
	if err != nil {
		return nil, nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan WaitResult, 1)
	if cond == "not-running" && !IsRunning(c.rec.State.Status) {
		ch <- WaitResult{StatusCode: c.rec.State.ExitCode}
		return ch, func() {}, nil
	}
	w := &waiter{cond: cond, ch: ch}
	c.waiters = append(c.waiters, w)
	cancel := func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		for i, x := range c.waiters {
			if x == w {
				c.waiters = append(c.waiters[:i], c.waiters[i+1:]...)
				return
			}
		}
	}
	return ch, cancel, nil
}

// ParseSignal reads "SIGTERM", "TERM" or "15".
func ParseSignal(s string) (syscall.Signal, error) {
	if s == "" {
		return syscall.SIGKILL, nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		if n <= 0 || n > 64 {
			return 0, errdefs.Invalid("invalid signal: %s", s)
		}
		return syscall.Signal(n), nil
	}
	name := strings.ToUpper(strings.TrimPrefix(strings.ToUpper(s), "SIG"))
	if sig, ok := signals[name]; ok {
		return sig, nil
	}
	return 0, errdefs.Invalid("invalid signal: %s", s)
}

var signals = map[string]syscall.Signal{
	"ABRT": syscall.SIGABRT, "ALRM": syscall.SIGALRM, "BUS": syscall.SIGBUS, "CHLD": syscall.SIGCHLD,
	"CONT": syscall.SIGCONT, "FPE": syscall.SIGFPE, "HUP": syscall.SIGHUP, "ILL": syscall.SIGILL,
	"INT": syscall.SIGINT, "IO": syscall.SIGIO, "KILL": syscall.SIGKILL, "PIPE": syscall.SIGPIPE,
	"PROF": syscall.SIGPROF, "PWR": syscall.SIGPWR, "QUIT": syscall.SIGQUIT, "SEGV": syscall.SIGSEGV,
	"STOP": syscall.SIGSTOP, "SYS": syscall.SIGSYS, "TERM": syscall.SIGTERM, "TRAP": syscall.SIGTRAP,
	"TSTP": syscall.SIGTSTP, "TTIN": syscall.SIGTTIN, "TTOU": syscall.SIGTTOU, "URG": syscall.SIGURG,
	"USR1": syscall.SIGUSR1, "USR2": syscall.SIGUSR2, "VTALRM": syscall.SIGVTALRM, "WINCH": syscall.SIGWINCH,
	"XCPU": syscall.SIGXCPU, "XFSZ": syscall.SIGXFSZ,
}

// Kill signals a running container.
func (e *Engine) Kill(ref, signal string) error {
	sig, err := ParseSignal(signal)
	if err != nil {
		return err
	}
	c, err := e.Lookup(ref)
	if err != nil {
		return err
	}
	c.mu.Lock()
	proc := c.proc
	c.mu.Unlock()
	if proc == nil {
		return errdefs.Conflict("Container %s is not running", c.rec.ID[:12])
	}
	if sig == syscall.SIGKILL {
		return proc.Kill()
	}
	return proc.Signal(sig)
}

// Stop sends the stop signal, then SIGKILL after timeout seconds.
func (e *Engine) Stop(ref string, timeout *int) error {
	c, err := e.Lookup(ref)
	if err != nil {
		return err
	}
	c.mu.Lock()
	proc, done := c.proc, c.runDone
	stopSignal, stopTimeout := c.rec.Config.StopSignal, c.rec.Config.StopTimeout
	c.mu.Unlock()
	if proc == nil {
		return errdefs.NotModified("container already stopped")
	}
	t := 10
	if stopTimeout != nil {
		t = *stopTimeout
	}
	if timeout != nil {
		t = *timeout
	}
	sig := syscall.SIGTERM
	if stopSignal != "" {
		if s, err := ParseSignal(stopSignal); err == nil {
			sig = s
		}
	}
	stopProcess(proc, done, sig, t)
	return nil
}

func stopProcess(proc runtime.Process, done chan struct{}, sig syscall.Signal, timeout int) {
	proc.Signal(sig)
	if timeout < 0 {
		<-done // Docker: a negative timeout waits indefinitely
		return
	}
	select {
	case <-done:
		return
	case <-time.After(time.Duration(timeout) * time.Second):
	}
	proc.Kill()
	<-done
}

// Restart stops (if running) and starts.
func (e *Engine) Restart(ref string, timeout *int) error {
	if err := e.Stop(ref, timeout); err != nil && errdefs.KindOf(err) != errdefs.KindNotModified {
		return err
	}
	c, err := e.Lookup(ref)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.rec.RestartCount++
	c.mu.Unlock()
	return e.Start(ref)
}

// Resize sets the TTY size of a running container.
func (e *Engine) Resize(ref string, h, w int) error {
	if h <= 0 || w <= 0 || h > 0xffff || w > 0xffff {
		return errdefs.Invalid("invalid terminal size %dx%d", w, h)
	}
	c, err := e.Lookup(ref)
	if err != nil {
		return err
	}
	c.mu.Lock()
	proc := c.proc
	c.mu.Unlock()
	if proc == nil {
		return errdefs.Conflict("Container %s is not running", c.rec.ID[:12])
	}
	return proc.Resize(uint16(w), uint16(h))
}

// Remove deletes a container; force kills a running one first.
func (e *Engine) Remove(ref string, force bool) error {
	c, err := e.Lookup(ref)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if c.gone || c.rec.State.Status == StatusRemoving {
		c.mu.Unlock()
		return errdefs.Conflict("removal of container %s is already in progress", c.rec.ID[:12])
	}
	if proc, done := c.proc, c.runDone; proc != nil {
		if !force {
			c.mu.Unlock()
			return errdefs.Conflict("You cannot remove a running container %s. Stop the container before attempting removal or force remove", c.rec.ID)
		}
		c.mu.Unlock()
		proc.Kill()
		<-done
		c.mu.Lock()
	}
	c.rec.State.Status = StatusRemoving
	if err := e.persist(c); err != nil {
		c.mu.Unlock()
		return err
	}
	c.logger.Close()
	if err := securefs.RemoveTree(c.dir); err != nil {
		c.rec.State.Status = StatusFailed
		c.rec.State.Error = fmt.Sprintf("removal failed: %v", err)
		e.persist(c)
		c.mu.Unlock()
		return err
	}
	c.gone = true
	c.notifyLocked(WaitResult{StatusCode: c.rec.State.ExitCode}, "not-running", "next-exit", "removed")
	c.mu.Unlock()
	e.mu.Lock()
	delete(e.containers, c.rec.ID)
	if e.names[c.rec.Name] == c.rec.ID {
		delete(e.names, c.rec.Name)
	}
	e.mu.Unlock()
	e.log.Info("container removed", "id", c.rec.ID[:12])
	return nil
}

// AttachStreams is what an attach client needs.
type AttachStreams struct {
	Tty       bool
	StdinOnce bool
	// Stdin accepts the client's input; nil unless the container has
	// OpenStdin.
	Stdin *stdinBroker
}

// Attach returns a container's streams. Like dockerd it accepts a stopped
// container (docker start -a attaches before starting); output is
// subscribed by the caller from Logger().
func (e *Engine) Attach(ref string) (*Container, AttachStreams, error) {
	c, err := e.Lookup(ref)
	if err != nil {
		return nil, AttachStreams{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gone || c.rec.State.Status == StatusRemoving {
		return nil, AttachStreams{}, errdefs.Conflict("container %s is being removed", c.rec.ID[:12])
	}
	as := AttachStreams{Tty: c.rec.Config.Tty, StdinOnce: c.rec.Config.StdinOnce}
	if c.rec.Config.OpenStdin {
		as.Stdin = c.stdin
	}
	return c, as, nil
}

// Shutdown stops every running container in parallel (stop signal, then
// SIGKILL after timeout) so nothing outlives the daemon.
func (e *Engine) Shutdown(timeout int) {
	var wg sync.WaitGroup
	for _, r := range e.List() {
		if IsRunning(r.State.Status) {
			e.log.Info("stopping container for shutdown", "id", r.ID[:12])
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				t := timeout
				e.Stop(id, &t)
			}(r.ID)
		}
	}
	wg.Wait()
}

// Counts returns running, stopped and total containers.
func (e *Engine) Counts() (running, stopped, total int) {
	for _, r := range e.List() {
		total++
		if IsRunning(r.State.Status) {
			running++
		} else {
			stopped++
		}
	}
	return
}
