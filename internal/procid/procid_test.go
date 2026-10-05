package procid

import (
	"os"
	"os/exec"
	"testing"
)

func TestStartTimeIdentifiesOneProcess(t *testing.T) {
	self := os.Getpid()
	st := StartTime(self)
	if st == 0 || !Alive(self, st) {
		t.Fatalf("self start time %d", st)
	}
	// A stale record naming a LIVE but unrelated process (pid reuse): the
	// start time differs, so nothing may be signalled.
	if Alive(self, st+1) || Alive(1, st) {
		t.Fatal("a stale record matched an unrelated live process")
	}
	if Alive(self, 0) || Alive(0, st) || Alive(-5, st) {
		t.Fatal("invalid record accepted")
	}
	cmd := exec.Command("sleep", "30")
	cmd.Start()
	pid := cmd.Process.Pid
	pst := StartTime(pid)
	cmd.Process.Kill()
	cmd.Wait()
	if StartTime(pid) != 0 || Alive(pid, pst) {
		t.Fatal("dead process reported alive")
	}
	if Cmdline0(self) == "" {
		t.Fatal("no cmdline")
	}
}
