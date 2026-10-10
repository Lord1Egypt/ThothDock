package engine

import (
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Lord1Egypt/ThothDock/internal/errdefs"
	"github.com/Lord1Egypt/ThothDock/internal/procid"
	"github.com/Lord1Egypt/ThothDock/internal/runtime"
	"github.com/Lord1Egypt/ThothDock/internal/securefs"
)

type startMode int

const (
	startAPI    startMode = iota // docker start/restart: clears a manual stop
	startPolicy                  // a restart policy: the timer or the restore at daemon start
)

// Start runs the container's process.
func (e *Engine) Start(ref string) error {
	c, err := e.Lookup(ref)
	if err != nil {
		return err
	}
	return e.start(c, startAPI)
}

func (e *Engine) start(c *Container, mode startMode) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return e.startLocked(c, mode)
}

func (e *Engine) startLocked(c *Container, mode startMode) error {
	if c.gone {
		return errdefs.NotFound("No such container: %s", c.rec.ID[:12])
	}
	switch c.rec.State.Status {
	case StatusRunning, StatusStarting:
		return errdefs.NotModified("container already started")
	case StatusRemoving:
		return errdefs.Conflict("container %s is marked for removal and cannot be started", c.rec.ID[:12])
	case StatusRestarting:
		// docker start on a container waiting for its policy: start it now.
		c.cancelRestartLocked()
	}
	if mode == startAPI {
		c.rec.State.ManuallyStopped = false
		c.restartDelay = 0
	}
	if len(c.rec.Networks) == 0 && c.rec.NetIP != "" {
		// Disconnected from its last network while it ran: back to the
		// device network.
		e.addrs.Release(c.rec.NetIP)
		c.rec.NetIP = ""
	}
	spec, err := e.buildSpec(c)
	if err != nil {
		e.startFailed(c, mode, err)
		return err
	}
	if err := e.transition(c, StatusStarting); err != nil {
		return err
	}
	c.rec.State.Error = ""
	if err := e.persist(c); err != nil {
		return err
	}
	fws, assigns, err := e.openPorts(c)
	if err != nil {
		e.startFailed(c, mode, err)
		return err
	}
	proc, err := e.Runtime.Start(spec)
	if err != nil {
		for _, f := range fws {
			f.Close()
		}
		e.startFailed(c, mode, err)
		return err
	}
	c.forwarders, c.ports = fws, assigns
	if err := e.transition(c, StatusRunning); err != nil {
		proc.Kill()
		return err
	}
	st := &c.rec.State
	st.Pid = proc.Pid()
	st.PidStart = procid.StartTime(proc.Pid())
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
	e.containerEvent(c, "start")
	go e.monitor(c, proc, c.runDone)
	return nil
}

// startFailed records a process that could not start the way Docker does:
// the container keeps its state (created, or exited if it ran before), with
// an exit code and error; attached clients are released, and an auto-remove
// container is removed (docker run --rm waits for that). A start that a
// restart policy made counts as a failed run, so the policy decides again
// (with a longer delay). Called with c.mu held.
func (e *Engine) startFailed(c *Container, mode startMode, err error) {
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
	if st.StartedAt.IsZero() {
		st.Status = StatusCreated
	} else {
		st.Status = StatusExited
	}
	st.Restarting = false
	st.Error = err.Error()
	st.ExitCode = 128
	if strings.Contains(st.Error, "executable file not found") || strings.Contains(st.Error, "no such file or directory") {
		st.ExitCode = 127
	} else if strings.Contains(st.Error, "permission denied") {
		st.ExitCode = 126
	}
	if mode == startPolicy && st.Status == StatusExited && !c.rec.HostConfig.AutoRemove && !e.shuttingDown.Load() &&
		shouldRestart(c.rec.HostConfig.RestartPolicy, st.ExitCode, st.ManuallyStopped, c.rec.RestartCount, false) {
		e.scheduleRestartLocked(c, 0)
	}
	if perr := e.persist(c); perr != nil {
		e.log.Error("persisting failed start", "id", c.rec.ID, "err", perr)
	}
}

// procParams is what differs between a container's main process and an
// exec'd one; everything else (root filesystem, binds, base environment,
// user resolution, executable lookup) is shared.
type procParams struct {
	User      string   // "" = the container's user
	WorkDir   string   // "" = the container's working directory
	ExtraEnv  []string // overlaid on the container's environment
	Argv      []string // argv[0] is looked up in the container's PATH
	Tty       bool
	OpenStdin bool
	Stdout    io.Writer
	Stderr    io.Writer
}

func (e *Engine) buildSpec(c *Container) (runtime.Spec, error) {
	return e.buildSpecFor(c, procParams{
		Argv: append([]string{c.rec.Path}, c.rec.Args...),
		Tty:  c.rec.Config.Tty, OpenStdin: c.rec.Config.OpenStdin,
		Stdout: c.logger.Writer("stdout"), Stderr: c.logger.Writer("stderr"),
	})
}

func (e *Engine) buildSpecFor(c *Container, p procParams) (runtime.Spec, error) {
	rootfs := filepath.Join(c.dir, "rootfs")
	root, err := securefs.OpenRoot(rootfs)
	if err != nil {
		return runtime.Spec{}, err
	}
	defer root.Close()
	cfg := c.rec.Config
	userSpec := cfg.User
	if p.User != "" {
		userSpec = p.User
	}
	user, err := resolveUser(root, userSpec)
	if err != nil {
		return runtime.Spec{}, err
	}
	env := append([]string{}, cfg.Env...)
	env = mergeEnv([]string{"HOSTNAME=" + cfg.Hostname}, env)
	env = mergeEnv(env, p.ExtraEnv)
	if !hasEnv(env, "HOME") || (p.User != "" && !hasEnvIn(p.ExtraEnv, "HOME")) {
		home := user.Home
		if home == "" {
			home = "/"
		}
		env = mergeEnv(env, []string{"HOME=" + home})
	}
	if p.Tty && !hasEnv(env, "TERM") {
		env = append(env, "TERM=xterm")
	}
	cwd := cfg.WorkingDir
	if p.WorkDir != "" {
		cwd = p.WorkDir
	}
	if len(p.Argv) == 0 {
		return runtime.Spec{}, errdefs.Invalid("no command specified")
	}
	exe, err := lookPath(root, p.Argv[0], cwd, env)
	if err != nil {
		return runtime.Spec{}, err
	}
	binds := []runtime.Bind{
		{Source: filepath.Join(c.dir, "hosts"), Target: "/etc/hosts"},
		{Source: filepath.Join(c.dir, "hostname"), Target: "/etc/hostname"},
		{Source: filepath.Join(c.dir, "resolv.conf"), Target: "/etc/resolv.conf"},
	}
	mounts, err := e.bindSources(c.rec.Binds)
	if err != nil {
		return runtime.Spec{}, err
	}
	for _, b := range mounts {
		binds = append(binds, runtime.Bind{Source: b.Source, Target: b.Target})
	}
	return runtime.Spec{
		ID: c.rec.ID, Rootfs: rootfs,
		Args: append([]string{exe}, p.Argv[1:]...), Env: env, Cwd: cwd,
		UID: user.UID, GID: user.GID, Binds: binds, NetIP: c.rec.NetIP,
		Tty: p.Tty, OpenStdin: p.OpenStdin,
		Stdout: p.Stdout, Stderr: p.Stderr,
	}, nil
}

func hasEnvIn(env []string, key string) bool { return hasEnv(env, key) }

// monitor waits for the process, records its exit, applies the restart
// policy and wakes waiters.
func (e *Engine) monitor(c *Container, proc runtime.Process, done chan struct{}) {
	ex := proc.Wait()
	c.logger.EndRun()
	c.mu.Lock()
	st := &c.rec.State
	ranFor := time.Since(st.StartedAt)
	if err := e.transition(c, StatusExited); err != nil {
		st.Status = StatusExited // the process is gone whatever the record said
	}
	st.ExitCode = ex.Code
	st.FinishedAt = time.Now().UTC()
	st.Pid, st.PidStart = 0, 0
	c.proc = nil
	c.closePortsLocked()
	c.killExecsLocked()
	c.stdin.end()
	c.stdin = newStdinBroker()
	e.containerEvent(c, "die", exitCodeAttr(ex.Code)...)
	autoRemove := c.rec.HostConfig.AutoRemove
	restart := !autoRemove && !e.shuttingDown.Load() &&
		shouldRestart(c.rec.HostConfig.RestartPolicy, ex.Code, st.ManuallyStopped, c.rec.RestartCount, false)
	if restart {
		e.scheduleRestartLocked(c, ranFor)
	}
	if err := e.persist(c); err != nil {
		e.log.Error("persisting exited container", "id", c.rec.ID, "err", err)
	}
	if restart {
		// As in dockerd, a restarting container still counts as running:
		// only next-exit waiters (docker run) see this exit.
		c.notifyLocked(WaitResult{StatusCode: ex.Code}, "next-exit")
	} else {
		c.notifyLocked(WaitResult{StatusCode: ex.Code}, "not-running", "next-exit")
	}
	close(done)
	c.mu.Unlock()
	e.log.Info("container exited", "id", c.rec.ID[:12], "code", ex.Code, "restart", restart)
	if autoRemove {
		if err := e.Remove(c.rec.ID, false); err != nil {
			e.log.Error("auto-removing container", "id", c.rec.ID, "err", err)
		}
	}
}

// scheduleRestartLocked moves c to restarting and arms its restart timer.
// Nothing runs until the timer fires. Called with c.mu held.
func (e *Engine) scheduleRestartLocked(c *Container, ranFor time.Duration) {
	if err := e.transition(c, StatusRestarting); err != nil {
		return
	}
	c.restartDelay = nextRestartDelay(c.restartDelay, ranFor)
	e.log.Info("container will restart", "id", c.rec.ID[:12], "policy", c.rec.HostConfig.RestartPolicy.Name, "in", c.restartDelay)
	c.restartTimer = time.AfterFunc(c.restartDelay, func() { e.policyRestart(c) })
}

// policyRestart is the restart timer firing. It starts the container only if
// it is still waiting: a stop, kill or removal in between cancelled it.
func (e *Engine) policyRestart(c *Container) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gone || c.rec.State.Status != StatusRestarting || e.shuttingDown.Load() {
		return
	}
	c.restartTimer = nil
	c.rec.RestartCount++
	if err := e.startLocked(c, startPolicy); err != nil {
		e.log.Warn("restart-policy start failed", "id", c.rec.ID[:12], "err", err)
	}
}

// cancelRestartLocked disarms a pending policy restart. Called with c.mu held.
func (c *Container) cancelRestartLocked() {
	if c.restartTimer != nil {
		c.restartTimer.Stop()
		c.restartTimer = nil
	}
}

// stopRestartingLocked ends a container that waits for its policy restart:
// it stays exited. Called with c.mu held.
func (e *Engine) stopRestartingLocked(c *Container, manual bool) {
	c.cancelRestartLocked()
	e.transition(c, StatusExited)
	if manual {
		c.rec.State.ManuallyStopped = true
	}
	if err := e.persist(c); err != nil {
		e.log.Error("persisting stopped container", "id", c.rec.ID, "err", err)
	}
	c.notifyLocked(WaitResult{StatusCode: c.rec.State.ExitCode}, "not-running")
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

// Kill signals a running container. SIGKILL also counts as a manual stop,
// so a restart policy does not bring the container back.
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
	if c.rec.State.Status == StatusRestarting {
		e.containerEvent(c, "kill", "signal", strconv.Itoa(int(sig)))
		e.stopRestartingLocked(c, sig == syscall.SIGKILL)
		c.mu.Unlock()
		return nil
	}
	proc := c.proc
	if proc != nil {
		e.containerEvent(c, "kill", "signal", strconv.Itoa(int(sig)))
		if sig == syscall.SIGKILL && !c.rec.State.ManuallyStopped {
			c.rec.State.ManuallyStopped = true
			e.persist(c)
		}
	}
	c.mu.Unlock()
	if proc == nil {
		return errdefs.Conflict("Container %s is not running", c.rec.ID[:12])
	}
	if sig == syscall.SIGKILL {
		return proc.Kill()
	}
	return proc.Signal(sig)
}

// Stop sends the stop signal, then SIGKILL after timeout seconds. It is a
// manual stop: the restart policy does not restart the container.
func (e *Engine) Stop(ref string, timeout *int) error {
	c, err := e.Lookup(ref)
	if err != nil {
		return err
	}
	return e.stop(c, timeout, true)
}

func (e *Engine) stop(c *Container, timeout *int, manual bool) error {
	c.mu.Lock()
	if c.rec.State.Status == StatusRestarting {
		e.stopRestartingLocked(c, manual)
		e.containerEvent(c, "stop")
		c.mu.Unlock()
		return nil
	}
	proc, done := c.proc, c.runDone
	stopSignal, stopTimeout := c.rec.Config.StopSignal, c.rec.Config.StopTimeout
	if proc != nil && manual && !c.rec.State.ManuallyStopped {
		// Recorded before the process exits, so the monitor never restarts it.
		c.rec.State.ManuallyStopped = true
		if err := e.persist(c); err != nil {
			e.log.Error("persisting manual stop", "id", c.rec.ID, "err", err)
		}
	}
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
	c.mu.Lock()
	e.containerEvent(c, "kill", "signal", strconv.Itoa(int(sig)))
	c.mu.Unlock()
	stopProcess(proc, done, sig, t)
	c.mu.Lock()
	e.containerEvent(c, "stop")
	c.mu.Unlock()
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
	defer c.mu.Unlock()
	c.rec.RestartCount++
	if err := e.startLocked(c, startAPI); err != nil {
		return err
	}
	e.containerEvent(c, "restart")
	return nil
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
	if c.rec.State.Status == StatusRestarting {
		if !force {
			c.mu.Unlock()
			return errdefs.Conflict("You cannot remove a restarting container %s. Stop the container before attempting removal or force remove", c.rec.ID)
		}
		c.cancelRestartLocked()
	}
	if proc, done := c.proc, c.runDone; proc != nil {
		if !force {
			c.mu.Unlock()
			return errdefs.Conflict("You cannot remove a running container %s. Stop the container before attempting removal or force remove", c.rec.ID)
		}
		// Killed for removal: never restarted by its policy.
		c.rec.State.ManuallyStopped = true
		c.mu.Unlock()
		proc.Kill()
		<-done
		c.mu.Lock()
		c.cancelRestartLocked()
	}
	if err := e.transition(c, StatusRemoving); err != nil {
		c.mu.Unlock()
		return err
	}
	if err := e.persist(c); err != nil {
		c.mu.Unlock()
		return err
	}
	c.logger.Close()
	if err := securefs.RemoveTree(c.dir); err != nil {
		e.transition(c, StatusFailed)
		c.rec.State.Error = fmt.Sprintf("removal failed: %v", err)
		e.persist(c)
		c.mu.Unlock()
		return err
	}
	c.gone = true
	e.forgetExecs(c)
	if c.rec.NetIP != "" {
		e.addrs.Release(c.rec.NetIP)
	}
	netIDs := netSet(c.rec.Networks)
	c.notifyLocked(WaitResult{StatusCode: c.rec.State.ExitCode}, "not-running", "next-exit", "removed")
	e.containerEvent(c, "destroy")
	c.mu.Unlock()
	e.mu.Lock()
	delete(e.containers, c.rec.ID)
	if e.names[c.rec.Name] == c.rec.ID {
		delete(e.names, c.rec.Name)
	}
	e.mu.Unlock()
	if len(netIDs) > 0 {
		e.syncHosts(netIDs)
	}
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
// SIGKILL after timeout) so nothing outlives the daemon. It is not a manual
// stop: restart policies bring the containers back when the daemon starts.
func (e *Engine) Shutdown(timeout int) {
	e.shuttingDown.Store(true)
	var wg sync.WaitGroup
	for _, r := range e.List() {
		if IsRunning(r.State.Status) {
			e.log.Info("stopping container for shutdown", "id", r.ID[:12])
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				c, err := e.Lookup(id)
				if err != nil {
					return
				}
				t := timeout
				e.stop(c, &t, false)
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

// UpdateRestartPolicy changes a container's restart policy (docker update
// --restart). It takes effect at the container's next exit.
func (e *Engine) UpdateRestartPolicy(ref string, p RestartPolicy) error {
	c, err := e.Lookup(ref)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := validateRestartPolicy(p, c.rec.HostConfig.AutoRemove); err != nil {
		return err
	}
	c.rec.HostConfig.RestartPolicy = p
	if err := e.persist(c); err != nil {
		return err
	}
	e.containerEvent(c, "update")
	return nil
}
