package main

import (
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Flatpak runs xdg-dbus-proxy (the filter between an app and the session or
// system bus) through bwrap, with two inherited descriptors on its command
// line: --args=FD, the proxy's own arguments NUL-separated, and --fd=FD, the
// write end of a pipe the proxy writes one byte to once it listens and exits
// when the reader goes away. A descriptor cannot cross the socket to the
// engine, so the sandboxed proxy would find both closed ("Failed to load
// --args: Bad file descriptor") and flatpak would stop with "Failed to sync
// with dbus proxy": every app that talks to a bus through the proxy failed.
// The shim reads --args here and puts the arguments on the command line, and
// plays the proxy's end of the pipe itself.

// dbusProxy is what the shim does for the proxy's pipe.
type dbusProxy struct {
	syncFD int    // the write end of flatpak's pipe
	socket string // the socket the proxy listens on
}

// planDBusProxy rewrites an xdg-dbus-proxy command line. It returns argv
// unchanged and a nil plan for any other command.
func planDBusProxy(argv []string, read func(fd int) ([]string, error)) ([]string, *dbusProxy, error) {
	if len(argv) == 0 || path.Base(argv[0]) != "xdg-dbus-proxy" {
		return argv, nil, nil
	}
	out := []string{argv[0]}
	plan := &dbusProxy{syncFD: -1}
	// --fd=FD is among the arguments --args=FD holds, so what that descriptor
	// holds is looked at in turn; it never holds another --args.
	queue := append([]string(nil), argv[1:]...)
	expanded := false
	for len(queue) > 0 {
		a := queue[0]
		queue = queue[1:]
		switch {
		case strings.HasPrefix(a, "--args="):
			fd, err := strconv.Atoi(strings.TrimPrefix(a, "--args="))
			if err != nil || fd < 3 || expanded {
				return nil, nil, fmt.Errorf("xdg-dbus-proxy: bad %s", a)
			}
			inner, err := read(fd)
			if err != nil {
				return nil, nil, fmt.Errorf("xdg-dbus-proxy %s: %v", a, err)
			}
			expanded = true
			queue = append(append([]string(nil), inner...), queue...)
		case strings.HasPrefix(a, "--fd="):
			fd, err := strconv.Atoi(strings.TrimPrefix(a, "--fd="))
			if err != nil || fd < 3 {
				return nil, nil, fmt.Errorf("xdg-dbus-proxy: bad %s", a)
			}
			plan.syncFD = fd
		default:
			out = append(out, a)
		}
	}
	// The first two arguments that are not options are the bus address and
	// the socket to listen on.
	var positional []string
	for _, a := range out[1:] {
		if !strings.HasPrefix(a, "--") {
			positional = append(positional, a)
		}
	}
	if len(positional) >= 2 {
		plan.socket = positional[1]
	}
	if plan.syncFD < 0 {
		return out, nil, nil
	}
	return out, plan, nil
}

// serve answers flatpak: one byte once the proxy's socket exists, then it
// holds the pipe until flatpak's reader is gone (the app ended) and calls
// stop, which ends the proxy. done closes when the proxy ended by itself.
func (p *dbusProxy) serve(done <-chan struct{}, stop func()) {
	f := os.NewFile(uintptr(p.syncFD), "dbus-proxy-sync")
	if f == nil {
		return
	}
	defer f.Close()
	ready := false
	for deadline := time.Now().Add(20 * time.Second); !ready && time.Now().Before(deadline); {
		if fi, err := os.Stat(p.socket); err == nil && fi.Mode()&os.ModeSocket != 0 {
			ready = true
			break
		}
		select {
		case <-done:
			return // flatpak reads end-of-file and reports the failure
		case <-time.After(20 * time.Millisecond):
		}
	}
	if !ready {
		return
	}
	if _, err := f.Write([]byte{'x'}); err != nil {
		return
	}
	fds := []unix.PollFd{{Fd: int32(f.Fd())}}
	for {
		select {
		case <-done:
			return
		default:
		}
		n, err := unix.Poll(fds, 200)
		if err != nil && err != unix.EINTR {
			return
		}
		if n > 0 && fds[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			stop()
			return
		}
	}
}
