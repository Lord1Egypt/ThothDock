// Package procid identifies processes by more than their pid. A pid can be
// reused; a pid together with its start time (clock ticks after boot, field
// 22 of /proc/<pid>/stat) names exactly one process for the life of the boot.
package procid

import (
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// StartTime is the start time of pid, or 0 when it does not exist.
func StartTime(pid int) uint64 {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	s := string(b)
	// The command name is in parentheses and may contain spaces or ")".
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 20 {
		return 0
	}
	v, _ := strconv.ParseUint(f[19], 10, 64)
	return v
}

// Cmdline0 is argv[0] of pid, or "" when unreadable.
func Cmdline0(pid int) string {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return ""
	}
	if i := strings.IndexByte(string(b), 0); i >= 0 {
		return string(b[:i])
	}
	return string(b)
}

// Alive reports whether pid is the process that started at start.
func Alive(pid int, start uint64) bool {
	return pid > 0 && start != 0 && StartTime(pid) == start
}

// KillGroup sends SIGKILL to the process group led by pid and to pid, but
// only while pid is still the process that started at start. A pidfd taken
// before the identity check pins that process: it is killed through the
// pidfd, and the group is signalled only while the pidfd shows it has not
// exited, so a pid recycled after the check is never signalled. It reports
// whether the process was alive and signalled.
func KillGroup(pid int, start uint64) bool {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return false
	}
	defer unix.Close(fd)
	if !Alive(pid, start) {
		return false
	}
	if !pidfdRunning(fd) {
		return false
	}
	syscall.Kill(-pid, syscall.SIGKILL)
	return unix.PidfdSendSignal(fd, syscall.SIGKILL, nil, 0) == nil
}

// pidfdRunning is false once the process behind fd has terminated.
func pidfdRunning(fd int) bool {
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	n, err := unix.Poll(fds, 0)
	return err == nil && n == 0
}
