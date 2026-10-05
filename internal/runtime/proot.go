package runtime

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// PRootConfig locates Garden's PRoot. On Android these are the edition's
// nativeLibraryDir libproot.so and libproot_loader.so and its runtime
// library directory; on a Linux host, a natively built proot.
type PRootConfig struct {
	Path   string // the proot executable
	Loader string // PROOT_LOADER, when the build needs it
	LibDir string // prepended to LD_LIBRARY_PATH for proot's own libraries (talloc)
	TmpDir string // PROOT_TMP_DIR
	// LinkToSymlink emulates hard links with symlinks (--link2symlink). It
	// is required where the platform refuses hard links, as Android does for
	// app data.
	LinkToSymlink bool
	// KernelRelease, if set, is what uname reports inside containers.
	KernelRelease string
}

// PRootRuntime runs each container process as a PRoot tracee.
type PRootRuntime struct {
	cfg PRootConfig
}

// NewPRoot checks that proot is executable.
func NewPRoot(cfg PRootConfig) (*PRootRuntime, error) {
	fi, err := os.Stat(cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("proot: %w", err)
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
		return nil, fmt.Errorf("proot: %s is not an executable file", cfg.Path)
	}
	if cfg.TmpDir == "" {
		return nil, errors.New("proot: no PROOT_TMP_DIR")
	}
	if err := os.MkdirAll(cfg.TmpDir, 0o700); err != nil {
		return nil, err
	}
	r := &PRootRuntime{cfg: cfg}
	if err := r.checkOptions(); err != nil {
		return nil, err
	}
	return r, nil
}

// requiredOptions are PRoot options ThothDock cannot work without. Garden's
// PRoot (termux-derived) has them; upstream PRoot 5.1.0 as shipped by
// Debian lacks --kill-on-exit, without which a container's background
// processes would outlive it.
var requiredOptions = []string{"--kill-on-exit", "--root-id", "--change-id", "--link2symlink"}

// checkOptions reads proot --help once at startup, so an unsuitable PRoot
// fails the daemon clearly instead of every container later.
func (r *PRootRuntime) checkOptions() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.cfg.Path, "--help")
	cmd.Env = r.Env(Spec{})
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return fmt.Errorf("proot: %s --help did not finish: %w", r.cfg.Path, ctx.Err())
	}
	help := string(out)
	for _, opt := range requiredOptions {
		if !strings.Contains(help, opt) {
			if err != nil {
				return fmt.Errorf("proot: %s cannot run: %v: %s", r.cfg.Path, err, strings.TrimSpace(help))
			}
			return fmt.Errorf("proot: %s lacks %s; ThothDock needs Garden's (termux-derived) PRoot", r.cfg.Path, opt)
		}
	}
	return nil
}

func (r *PRootRuntime) Name() string { return "proot" }

// Config returns the configuration.
func (r *PRootRuntime) Config() PRootConfig { return r.cfg }

// Argv is the proot command line for spec (exported for tests and doctor).
func (r *PRootRuntime) Argv(spec Spec) []string {
	argv := []string{r.cfg.Path, "--rootfs=" + spec.Rootfs}
	if spec.UID == 0 && spec.GID == 0 {
		argv = append(argv, "--root-id")
	} else {
		argv = append(argv, fmt.Sprintf("--change-id=%d:%d", spec.UID, spec.GID))
	}
	if r.cfg.LinkToSymlink {
		argv = append(argv, "--link2symlink")
	}
	cwd := spec.Cwd
	if cwd == "" {
		cwd = "/"
	}
	argv = append(argv, "--cwd="+cwd, "--kill-on-exit")
	if r.cfg.KernelRelease != "" {
		argv = append(argv, "--kernel-release="+r.cfg.KernelRelease)
	}
	argv = append(argv, "--bind=/dev", "--bind=/proc", "--bind=/sys")
	for _, b := range spec.Binds {
		argv = append(argv, "--bind="+b.Source+":"+b.Target)
	}
	return append(argv, spec.Args...)
}

// Env is the environment proot is started with: PRoot hands its own
// environment to the workload, so this is the container's environment plus
// what proot itself needs.
func (r *PRootRuntime) Env(spec Spec) []string {
	env := make([]string, 0, len(spec.Env)+3)
	ldPath := r.cfg.LibDir
	for _, kv := range spec.Env {
		if v, ok := strings.CutPrefix(kv, "LD_LIBRARY_PATH="); ok && r.cfg.LibDir != "" {
			if v != "" {
				ldPath = r.cfg.LibDir + ":" + v
			}
			continue
		}
		if strings.HasPrefix(kv, "PROOT_") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "PROOT_TMP_DIR="+r.cfg.TmpDir)
	if r.cfg.Loader != "" {
		env = append(env, "PROOT_LOADER="+r.cfg.Loader)
	}
	if ldPath != "" {
		env = append(env, "LD_LIBRARY_PATH="+ldPath)
	}
	return env
}

type prootProc struct {
	cmd      *exec.Cmd
	pty      *os.File
	stdin    io.WriteCloser
	copies   sync.WaitGroup
	readers  []*os.File
	sigSeen  chan int
	waitOnce sync.Once
	exit     Exit
}

func (r *PRootRuntime) Start(spec Spec) (Process, error) {
	if len(spec.Args) == 0 || !strings.HasPrefix(spec.Args[0], "/") {
		return nil, errors.New("proot: the command must be an absolute guest path")
	}
	cmd := exec.Command(r.cfg.Path)
	cmd.Args = r.Argv(spec)
	cmd.Env = r.Env(spec)
	cmd.Dir = r.cfg.TmpDir
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	p := &prootProc{cmd: cmd, sigSeen: make(chan int, 1)}
	var childEnds []*os.File
	closeChildEnds := func() {
		for _, f := range childEnds {
			f.Close()
		}
	}
	if spec.Tty {
		master, slave, err := openPTY()
		if err != nil {
			return nil, fmt.Errorf("proot: allocating a pseudo-terminal: %w", err)
		}
		p.pty = master
		cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
		cmd.SysProcAttr.Setsid = true
		cmd.SysProcAttr.Setctty = true
		cmd.SysProcAttr.Ctty = 0
		childEnds = append(childEnds, slave)
		if spec.OpenStdin {
			p.stdin = nopCloser{master}
		}
	} else {
		cmd.SysProcAttr.Setpgid = true
		outR, outW, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		errR, errW, err := os.Pipe()
		if err != nil {
			outR.Close()
			outW.Close()
			return nil, err
		}
		cmd.Stdout, cmd.Stderr = outW, errW
		childEnds = append(childEnds, outW, errW)
		p.readers = append(p.readers, outR, errR)
		if spec.OpenStdin {
			inR, inW, err := os.Pipe()
			if err != nil {
				closeChildEnds()
				outR.Close()
				errR.Close()
				return nil, err
			}
			cmd.Stdin = inR
			childEnds = append(childEnds, inR)
			p.stdin = inW
		} else {
			devnull, err := os.Open(os.DevNull)
			if err != nil {
				return nil, err
			}
			cmd.Stdin = devnull
			childEnds = append(childEnds, devnull)
		}
	}
	if err := cmd.Start(); err != nil {
		closeChildEnds()
		for _, f := range p.readers {
			f.Close()
		}
		if p.pty != nil {
			p.pty.Close()
		}
		return nil, fmt.Errorf("proot: %w", err)
	}
	closeChildEnds()
	if spec.Tty {
		p.copies.Add(1)
		go func() {
			defer p.copies.Done()
			copyPTY(spec.Stdout, p.pty)
		}()
	} else {
		p.copies.Add(2)
		go func() {
			defer p.copies.Done()
			io.Copy(spec.Stdout, p.readers[0])
		}()
		go func() {
			defer p.copies.Done()
			filterPRootInfo(spec.Stderr, p.readers[1], p.sigSeen)
		}()
	}
	return p, nil
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

// copyPTY copies until the PTY reports EIO (every slave closed).
func copyPTY(dst io.Writer, master *os.File) {
	buf := make([]byte, 32*1024)
	for {
		n, err := master.Read(buf)
		if n > 0 {
			dst.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// PRoot reports a workload killed by a signal on its own stderr and exits
// 255. The line is removed from the container's stderr and turned into
// Docker's exit code, 128+signal.
var prootSignalLine = regexp.MustCompile(`^proot info: vpid 1: terminated with signal ([0-9]+)$`)

func filterPRootInfo(dst io.Writer, src io.Reader, sig chan<- int) {
	br := bufio.NewReaderSize(src, 64*1024)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if m := prootSignalLine.FindSubmatch(bytes.TrimRight(line, "\n")); m != nil {
				n, _ := strconv.Atoi(string(m[1]))
				select {
				case sig <- n:
				default:
				}
			} else {
				dst.Write(line)
			}
		}
		if err != nil {
			return
		}
	}
}

func (p *prootProc) Pid() int { return p.cmd.Process.Pid }

func (p *prootProc) Stdin() io.WriteCloser { return p.stdin }

func (p *prootProc) Signal(sig syscall.Signal) error {
	// The workload shares proot's process group (session, with a TTY).
	err := syscall.Kill(-p.cmd.Process.Pid, sig)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}

func (p *prootProc) Kill() error {
	// SIGKILL to proot is enough: --kill-on-exit and PTRACE_O_EXITKILL take
	// every tracee with it. The group is signalled too for good measure.
	syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	err := p.cmd.Process.Signal(syscall.SIGKILL)
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

func (p *prootProc) Resize(w, h uint16) error {
	if p.pty == nil {
		return nil
	}
	return unix.IoctlSetWinsize(int(p.pty.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Col: w, Row: h})
}

// outputGrace bounds how long Wait waits for output after proot exited.
// --kill-on-exit ends every tracee, so the pipes normally reach EOF at once.
const outputGrace = 3 * time.Second

func (p *prootProc) Wait() Exit {
	p.waitOnce.Do(func() {
		err := p.cmd.Wait()
		done := make(chan struct{})
		go func() {
			p.copies.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(outputGrace):
			for _, f := range p.readers {
				f.Close()
			}
			if p.pty != nil {
				p.pty.Close()
			}
			<-done
		}
		for _, f := range p.readers {
			f.Close()
		}
		if p.pty != nil {
			p.pty.Close()
		}
		if p.stdin != nil {
			p.stdin.Close()
		}
		p.exit = exitOf(p.cmd, err, p.sigSeen)
	})
	return p.exit
}

func exitOf(cmd *exec.Cmd, err error, sigSeen chan int) Exit {
	ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok {
		return Exit{Code: 255}
	}
	if ws.Signaled() {
		// proot itself was killed (Kill, or the OOM killer).
		return Exit{Code: 128 + int(ws.Signal()), Signal: ws.Signal()}
	}
	// PRoot only refreshes its exit status when a tracee exits normally, so
	// a workload killed by a signal leaves 255 (nothing exited) or the stale
	// status of an earlier child (a shell loop whose last "sleep" returned 0).
	// The vpid 1 line is authoritative either way.
	select {
	case n := <-sigSeen:
		return Exit{Code: 128 + n, Signal: syscall.Signal(n)}
	default:
	}
	return Exit{Code: ws.ExitStatus()}
}
