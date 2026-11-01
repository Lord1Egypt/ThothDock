// Package termutil holds the terminal handling the guest-side commands
// (thothdock-bwrap, thothdock-snap) share: raw mode and window sizes.
package termutil

import (
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"
)

// IsTerminal reports whether fd is a terminal.
func IsTerminal(fd int) bool {
	_, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	return err == nil
}

// MakeRaw puts the terminal into raw mode (cfmakeraw), as docker run -t does,
// so keys reach the sandbox's own terminal unchanged. It returns the restore.
func MakeRaw(fd int) (func(), error) {
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

// Size is fd's window size in cells, or 0, 0.
func Size(fd int) (w, h uint16) {
	ws, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
	if err != nil {
		return 0, 0
	}
	return ws.Col, ws.Row
}

// Resizes delivers fd's new size on every SIGWINCH (latest size wins).
func Resizes(fd int) <-chan [2]uint16 {
	out := make(chan [2]uint16, 1)
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	go func() {
		for range winch {
			if w, h := Size(fd); w > 0 && h > 0 {
				select {
				case out <- [2]uint16{w, h}:
				default:
				}
			}
		}
	}()
	return out
}
