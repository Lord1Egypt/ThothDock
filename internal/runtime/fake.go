package runtime

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"syscall"
)

// FakeFunc is a fake workload. It must return when ctx is cancelled.
type FakeFunc func(ctx context.Context, spec Spec, stdin io.Reader) int

// FakeRuntime runs Go functions as "processes", keyed by Args[0].
type FakeRuntime struct {
	mu       sync.Mutex
	programs map[string]FakeFunc
	nextPid  atomic.Int64
	Started  []Spec
	// NoNetIP makes the fake behave like a PRoot without --net-ip.
	NoNetIP bool
}

// NewFake returns an empty fake runtime.
func NewFake() *FakeRuntime {
	f := &FakeRuntime{programs: map[string]FakeFunc{}}
	// Above the kernel's pid_max ceiling (2^22): a fake PID can never name a
	// real process, so recovery code that kills by PID never hits one.
	f.nextPid.Store(1 << 30)
	return f
}

func (f *FakeRuntime) Name() string { return "fake" }

// SupportsNetIP is true unless NoNetIP is set.
func (f *FakeRuntime) SupportsNetIP() bool { return !f.NoNetIP }

// Program registers fn for an executable path.
func (f *FakeRuntime) Program(path string, fn FakeFunc) {
	f.mu.Lock()
	f.programs[path] = fn
	f.mu.Unlock()
}

type fakeProc struct {
	pid    int
	cancel context.CancelFunc
	sig    atomic.Int32
	done   chan Exit
	stdin  *io.PipeWriter
	exit   Exit
	once   sync.Once
}

func (f *FakeRuntime) Start(spec Spec) (Process, error) {
	f.mu.Lock()
	fn := f.programs[spec.Args[0]]
	f.Started = append(f.Started, spec)
	f.mu.Unlock()
	if fn == nil {
		fn = func(context.Context, Spec, io.Reader) int { return 127 }
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &fakeProc{pid: int(f.nextPid.Add(1)), cancel: cancel, done: make(chan Exit, 1)}
	var stdin io.Reader = eofReader{}
	if spec.OpenStdin {
		r, w := io.Pipe()
		stdin, p.stdin = r, w
	}
	go func() {
		code := fn(ctx, spec, stdin)
		if s := p.sig.Load(); s != 0 {
			p.done <- Exit{Code: 128 + int(s), Signal: syscall.Signal(s)}
			return
		}
		p.done <- Exit{Code: code}
	}()
	return p, nil
}

type eofReader struct{}

func (eofReader) Read([]byte) (int, error) { return 0, io.EOF }

func (p *fakeProc) Pid() int { return p.pid }
func (p *fakeProc) Stdin() io.WriteCloser {
	if p.stdin == nil {
		return nil
	}
	return p.stdin
}
func (p *fakeProc) Signal(sig syscall.Signal) error {
	if sig == 0 {
		return nil
	}
	p.sig.CompareAndSwap(0, int32(sig))
	p.cancel()
	return nil
}
func (p *fakeProc) Kill() error              { return p.Signal(syscall.SIGKILL) }
func (p *fakeProc) Resize(w, h uint16) error { return nil }
func (p *fakeProc) Wait() Exit {
	p.once.Do(func() { p.exit = <-p.done })
	return p.exit
}
