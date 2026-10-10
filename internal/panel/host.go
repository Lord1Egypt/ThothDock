package panel

import (
	"bufio"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// hostFigures are the device figures an unprivileged app can read. A
// figure the platform withholds is left out rather than guessed.
func hostFigures(dataRoot string) map[string]any {
	out := map[string]any{}
	if f, err := os.Open("/proc/meminfo"); err == nil {
		mem := map[string]int64{}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) >= 2 {
				if v, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
					mem[strings.TrimSuffix(fields[0], ":")] = v * 1024
				}
			}
		}
		f.Close()
		if mem["MemTotal"] > 0 {
			out["memTotal"], out["memAvailable"] = mem["MemTotal"], mem["MemAvailable"]
		}
	}
	var st unix.Statfs_t
	if dataRoot != "" && unix.Statfs(dataRoot, &st) == nil {
		out["storageTotal"] = int64(st.Blocks) * int64(st.Bsize)
		out["storageFree"] = int64(st.Bavail) * int64(st.Bsize)
	}
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		if fields := strings.Fields(string(b)); len(fields) >= 3 {
			out["load"] = fields[:3]
		}
	}
	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		if fields := strings.Fields(string(b)); len(fields) >= 1 {
			if v, err := strconv.ParseFloat(fields[0], 64); err == nil {
				out["uptimeSeconds"] = int64(v)
			}
		}
	}
	return out
}
