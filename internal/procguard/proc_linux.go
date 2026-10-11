package procguard

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// ProcCounter counts the processes of one uid under Root (normally /proc),
// not counting the roots of the uid's process trees: on Android that is the
// app process itself (its parent, zygote, belongs to another uid), which
// Android does not list among the app's child ("phantom") processes. Zombies
// count: Android drops them from its list only when they are reaped.
type ProcCounter struct {
	Root string
	UID  uint32
}

// NewProcCounter counts this process's uid under /proc.
func NewProcCounter() *ProcCounter {
	return &ProcCounter{Root: "/proc", UID: uint32(os.Getuid())}
}

func (c *ProcCounter) Count() (int, error) {
	ents, err := os.ReadDir(c.Root)
	if err != nil {
		return 0, err
	}
	ppid := map[int]int{}
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || !e.IsDir() {
			continue
		}
		fi, err := os.Stat(c.Root + "/" + e.Name())
		if err != nil {
			continue
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); !ok || st.Uid != c.UID {
			continue
		}
		b, err := os.ReadFile(c.Root + "/" + e.Name() + "/stat")
		if err != nil {
			continue // gone, or not ours after all
		}
		// pid (comm) state ppid ...: comm may hold spaces and parentheses.
		s := string(b)
		i := strings.LastIndexByte(s, ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(s[i+1:])
		if len(f) < 2 {
			continue
		}
		pp, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		ppid[pid] = pp
	}
	if len(ppid) == 0 {
		return 0, errors.New("no readable processes of this uid")
	}
	n := 0
	for _, pp := range ppid {
		if _, ours := ppid[pp]; ours {
			n++
		}
	}
	return n, nil
}
