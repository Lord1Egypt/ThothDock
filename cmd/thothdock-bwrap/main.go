// Command thothdock-bwrap stands in for bubblewrap inside a ThothTerm guest.
// Flatpak runs every app through bwrap, which needs mount namespaces a PRoot
// guest cannot give it; flatpak honours FLATPAK_BWRAP, so this program takes
// bwrap's command line, reads the descriptors bwrap would read, and asks the
// ThothDock engine to build the same file system view with a PRoot of its own
// (docs/APP_STORES.md). It talks to the engine over the socket the guest's
// Docker CLI uses.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/Lord1Egypt/ThothDock/internal/sandbox"
)

// bwrapVersion is the bubblewrap whose option set this shim accepts.
const bwrapVersion = "0.12.0"

func main() {
	os.Exit(run(os.Args[1:]))
}

func fail(format string, args ...any) int {
	fmt.Fprintf(os.Stderr, "bwrap: "+format+"\n", args...)
	return 1
}

func run(args []string) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Println("bubblewrap " + bwrapVersion + " (ThothDock)")
		return 0
	}
	if len(args) == 1 && args[0] == "--help" {
		fmt.Println("usage: bwrap [OPTIONS...] [--] COMMAND [ARGS...]\nThothDock's bubblewrap stand-in; see ThothDock docs/APP_STORES.md.")
		return 0
	}
	spec, err := sandbox.Parse(args, readArgs)
	if err != nil {
		return fail("%v", err)
	}
	req := &sandbox.Request{
		Ops: spec.Ops, Env: spec.Env, BaseEnv: os.Environ(), Chdir: spec.Chdir, Argv: spec.Argv,
		UID: os.Getuid(), GID: os.Getgid(), DieWithParent: true, Files: map[int][]byte{},
	}
	total := 0
	for _, op := range spec.Ops {
		switch op.Kind {
		case sandbox.OpFile, sandbox.OpBindData, sandbox.OpROBindData:
			b, err := readFD(op.FD, sandbox.MaxFileBytes-total)
			if err != nil {
				return fail("--%s %d: %v", op.Kind, op.FD, err)
			}
			total += len(b)
			req.Files[op.FD] = b
		}
	}
	if spec.BlockFD >= 0 {
		// bwrap waits until the descriptor has data (or reaches EOF).
		var one [1]byte
		os.NewFile(uintptr(spec.BlockFD), "block-fd").Read(one[:])
	}
	if verbose() {
		for _, ig := range spec.Ignored {
			fmt.Fprintf(os.Stderr, "bwrap (ThothDock): %s is not supported under PRoot and was ignored\n", ig)
		}
	}
	conn, err := net.Dial("unix", socketPath())
	if err != nil {
		return fail("cannot reach the ThothDock engine: %v", err)
	}
	defer conn.Close()

	req.Tty = isTerminal(0) && isTerminal(1)
	if req.Tty {
		if restore, err := makeRaw(0); err == nil {
			defer restore()
		}
	}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGHUP)
	var stoppedBy atomic.Int32
	go func() {
		sig := (<-sigs).(syscall.Signal)
		stoppedBy.Store(int32(sig))
		conn.Close() // the engine kills a --die-with-parent sandbox
	}()

	code, err := sandbox.Run(conn, req, os.Stdin, os.Stdout, os.Stderr, func(s sandbox.Started) {
		info := fmt.Sprintf("{\n    \"child-pid\": %d\n}\n", s.Pid)
		if spec.InfoFD >= 0 {
			f := os.NewFile(uintptr(spec.InfoFD), "info-fd")
			f.WriteString(info)
			f.Close()
		}
		if spec.JSONStatusFD >= 0 {
			b, _ := json.Marshal(map[string]int{"child-pid": s.Pid})
			os.NewFile(uintptr(spec.JSONStatusFD), "json-status-fd").Write(append(b, '\n'))
		}
		if verbose() {
			for _, w := range s.Warnings {
				fmt.Fprintf(os.Stderr, "bwrap (ThothDock): %s\n", w)
			}
		}
	})
	if err != nil {
		if sig := stoppedBy.Load(); sig != 0 {
			return 128 + int(sig) // stopped on purpose: no error message
		}
		return fail("%v", err)
	}
	if spec.JSONStatusFD >= 0 {
		b, _ := json.Marshal(map[string]int{"exit-code": code})
		os.NewFile(uintptr(spec.JSONStatusFD), "json-status-fd").Write(append(b, '\n'))
	}
	return code
}

func verbose() bool { return os.Getenv("THOTHDOCK_BWRAP_VERBOSE") == "1" }

// socketPath is the engine's socket: THOTHDOCK_SOCKET, else DOCKER_HOST
// (unix://), else the path ThothTerm binds into the guest.
func socketPath() string {
	if p := os.Getenv("THOTHDOCK_SOCKET"); p != "" {
		return p
	}
	if h, ok := strings.CutPrefix(os.Getenv("DOCKER_HOST"), "unix://"); ok && h != "" {
		return h
	}
	return "/run/thothdock/thothdock.sock"
}

// readArgs returns the NUL-separated arguments stored in an --args descriptor.
func readArgs(fd int) ([]string, error) {
	b, err := readFD(fd, 1<<20)
	if err != nil {
		return nil, err
	}
	s := strings.TrimSuffix(string(b), "\x00")
	if s == "" {
		return nil, nil
	}
	return strings.Split(s, "\x00"), nil
}

func readFD(fd, max int) ([]byte, error) {
	if max < 0 {
		max = 0
	}
	f := os.NewFile(uintptr(fd), fmt.Sprintf("fd-%d", fd))
	if f == nil {
		return nil, errors.New("not an open descriptor")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, int64(max)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > max {
		return nil, fmt.Errorf("more than %d bytes", max)
	}
	return b, nil
}

func isTerminal(fd int) bool {
	_, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	return err == nil
}

// makeRaw puts the terminal into raw mode (cfmakeraw), as docker run -t does,
// so keys reach the sandbox's own terminal unchanged.
func makeRaw(fd int) (func(), error) {
	old, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil, err
	}
	t := *old
	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	t.Oflag &^= unix.OPOST
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	t.Cflag &^= unix.CSIZE | unix.PARENB
	t.Cflag |= unix.CS8
	t.Cc[unix.VMIN] = 1
	t.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &t); err != nil {
		return nil, err
	}
	return func() { unix.IoctlSetTermios(fd, unix.TCSETS, old) }, nil
}
