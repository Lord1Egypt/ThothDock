package procid

import (
	"os"
	"os/exec"
	"syscall"
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

func startSleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	return cmd
}

func TestKillGroupRefusesAnotherProcessWithTheSamePid(t *testing.T) {
	cmd := startSleeper(t)
	pid := cmd.Process.Pid
	start := StartTime(pid)
	if KillGroup(pid, start+1) {
		t.Fatal("killed a process whose start time differs")
	}
	if KillGroup(pid, 0) {
		t.Fatal("killed with no recorded start time")
	}
	if !Alive(pid, start) {
		t.Fatal("process died although the identity did not match")
	}
}

func TestKillGroupKillsTheVerifiedProcess(t *testing.T) {
	cmd := startSleeper(t)
	pid := cmd.Process.Pid
	if !KillGroup(pid, StartTime(pid)) {
		t.Fatal("verified process was not signalled")
	}
	err := cmd.Wait()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != -1 {
		t.Fatalf("sleep ended with %v, want killed by a signal", err)
	}
	if KillGroup(pid, 1) {
		t.Fatal("signalled a pid that no longer exists")
	}
}
