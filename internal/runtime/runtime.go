// Package runtime starts and supervises container processes. ThothDock's
// engine talks only to the Runtime interface; PRootRuntime executes through
// Garden's PRoot, FakeRuntime is deterministic for tests.
package runtime

import (
	"io"
	"syscall"
)

// Bind is a host directory made visible at a guest path.
type Bind struct {
	Source string // canonical host path, already approved by policy
	Target string // absolute guest path
}

// Spec is everything needed to run one process inside a container rootfs.
type Spec struct {
	ID     string
	Rootfs string
	// Args[0] is an absolute guest path the engine resolved through PATH.
	Args []string
	Env  []string
	Cwd  string
	UID  int
	GID  int
	// Binds are applied in order after the runtime's own (/dev, /proc, /sys).
	Binds []Bind
	Tty   bool
	// OpenStdin keeps a writable stdin; otherwise stdin is /dev/null.
	OpenStdin bool
	// Stdout receives output (the whole PTY stream with Tty); Stderr
	// receives standard error when not Tty.
	Stdout io.Writer
	Stderr io.Writer
}

// Exit is how a process ended.
type Exit struct {
	Code   int // Docker convention: 128+signal when killed by a signal
	Signal syscall.Signal
}

// Process is a running container process.
type Process interface {
	Pid() int
	// Stdin is nil unless Spec.OpenStdin.
	Stdin() io.WriteCloser
	// Signal delivers sig to the process group (workload and its children).
	Signal(sig syscall.Signal) error
	// Kill ends the process and everything it started, unconditionally.
	Kill() error
	// Resize sets the PTY window size; a no-op without a TTY.
	Resize(width, height uint16) error
	// Wait blocks until the process ended and its output was delivered.
	Wait() Exit
}

// Runtime starts processes.
type Runtime interface {
	Name() string
	Start(spec Spec) (Process, error)
}
