// Package procid identifies processes by more than their pid. A pid can be
// reused; a pid together with its start time (clock ticks after boot, field
// 22 of /proc/<pid>/stat) names exactly one process for the life of the boot.
package procid

import (
	"os"
	"strconv"
	"strings"
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
