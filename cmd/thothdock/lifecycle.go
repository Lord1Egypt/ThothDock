package main

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/Lord1Egypt/ThothDock/internal/platform"
	"github.com/Lord1Egypt/ThothDock/internal/store"
)

func pidFile(l platform.Layout) string { return filepath.Join(l.Run(), "thothdock.pid") }

// writePidFile records the daemon's pid (under the root lock) so a
// supervisor that lost track of it -- an Android app restarted by the
// system -- can find and stop it.
func writePidFile(l platform.Layout) error {
	return store.WriteFileAtomic(pidFile(l), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600)
}

// exitWhenParentDies asks the kernel for SIGTERM when the parent goes away,
// which starts the ordinary graceful shutdown.
func exitWhenParentDies() error {
	if err := unix.Prctl(unix.PR_SET_PDEATHSIG, uintptr(syscall.SIGTERM), 0, 0, 0); err != nil {
		return err
	}
	if os.Getppid() == 1 {
		return errors.New("--exit-with-parent: the parent has already exited")
	}
	return nil
}
