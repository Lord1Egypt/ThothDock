package main

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/Lord1Egypt/ThothDock/internal/platform"
	"github.com/Lord1Egypt/ThothDock/internal/procid"
	"github.com/Lord1Egypt/ThothDock/internal/store"
)

func pidFile(l platform.Layout) string { return filepath.Join(l.Run(), "thothdock.pid") }

// writePidFile records "<pid> <start time>" (under the root lock) so a
// supervisor that lost track of the daemon -- an Android app restarted by
// the system -- can find and stop it. The start time makes the record name
// one process: a reused pid has a different start time, and a supervisor
// must never signal a pid whose start time does not match.
func writePidFile(l platform.Layout) error {
	self := os.Getpid()
	rec := strconv.Itoa(self) + " " + strconv.FormatUint(procid.StartTime(self), 10) + "\n"
	return store.WriteFileAtomic(pidFile(l), []byte(rec), 0o600)
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
