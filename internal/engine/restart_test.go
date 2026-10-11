package engine

import (
	"context"
	"io"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Lord1Egypt/ThothDock/internal/errdefs"
	"github.com/Lord1Egypt/ThothDock/internal/runtime"
)

// program makes /bin/sh behave by its first argument: "exit:N" exits with N
// at once, "gated" exits with 1 when gate is closed, anything else runs until
// it is signalled. It counts starts.
func (f *fixture) program(gate ...chan struct{}) *atomic.Int32 {
	var starts atomic.Int32
	f.rt.Program("/bin/sh", func(ctx context.Context, s runtime.Spec, _ io.Reader) int {
		starts.Add(1)
		if len(s.Args) > 1 {
			if n, ok := strings.CutPrefix(s.Args[1], "exit:"); ok {
				code, _ := strconv.Atoi(n)
				return code
			}
			if s.Args[1] == "gated" {
				select {
				case <-gate[0]:
					return 1
				case <-ctx.Done():
					return 0
				}
			}
		}
		<-ctx.Done()
		return 0
	})
	return &starts
}

// holdRestarting starts a gated "always" container, lengthens its backoff
// while it runs, then lets it exit: it waits a minute in "restarting".
func (f *fixture) holdRestarting(gate chan struct{}) string {
	f.t.Helper()
	id := f.createWith(RestartPolicy{Name: PolicyAlways}, "gated")
	if err := f.e.Start(id); err != nil {
		f.t.Fatal(err)
	}
	c, _ := f.e.Lookup(id)
	c.mu.Lock()
	c.restartDelay = 30 * time.Second
	c.mu.Unlock()
	close(gate)
	eventually(f.t, "restarting", func() bool { return f.state(id).Status == StatusRestarting })
	return id
}

func (f *fixture) createWith(policy RestartPolicy, cmd ...string) string {
	f.t.Helper()
	id, _, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image, Entrypoint: StrSlice{"sh"}, Cmd: cmd},
		HostConfig: &HostConfig{RestartPolicy: policy}}, "", "")
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *fixture) state(id string) State {
	f.t.Helper()
	c, err := f.e.Lookup(id)
	if err != nil {
		f.t.Fatal(err)
	}
	return c.Snapshot().State
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestOnFailureRetriesUpToTheLimit(t *testing.T) {
	f := newFixture(t)
	starts := f.program()
	id := f.createWith(RestartPolicy{Name: PolicyOnFailure, MaximumRetryCount: 2}, "exit:3")
	if err := f.e.Start(id); err != nil {
		t.Fatal(err)
	}
	// 1 start + 2 retries (100 ms, then 200 ms), then it stays exited.
	eventually(t, "two retries", func() bool { return starts.Load() == 3 && f.state(id).Status == StatusExited })
	time.Sleep(500 * time.Millisecond)
	if n := starts.Load(); n != 3 {
		t.Fatalf("started %d times, want 3", n)
	}
	c, _ := f.e.Lookup(id)
	if r := c.Snapshot(); r.RestartCount != 2 || r.State.ExitCode != 3 {
		t.Fatalf("restartCount %d exit %d", r.RestartCount, r.State.ExitCode)
	}
}

func TestOnFailureDoesNotRestartASuccess(t *testing.T) {
	f := newFixture(t)
	starts := f.program()
	id := f.createWith(RestartPolicy{Name: PolicyOnFailure}, "exit:0")
	f.e.Start(id)
	time.Sleep(400 * time.Millisecond)
	if starts.Load() != 1 || f.state(id).Status != StatusExited {
		t.Fatalf("starts %d state %+v", starts.Load(), f.state(id))
	}
}

func TestAlwaysRestartsUntilManuallyStopped(t *testing.T) {
	f := newFixture(t)
	starts := f.program()
	id := f.createWith(RestartPolicy{Name: PolicyAlways}, "exit:0")
	f.e.Start(id)
	eventually(t, "restarts", func() bool { return starts.Load() >= 3 })
	if err := f.e.Stop(id, nil); err != nil && errdefs.KindOf(err) != errdefs.KindNotModified {
		t.Fatal(err)
	}
	eventually(t, "stopped", func() bool { return f.state(id).Status == StatusExited })
	n := starts.Load()
	time.Sleep(700 * time.Millisecond) // longer than any pending backoff here
	if starts.Load() != n {
		t.Fatalf("restarted after a manual stop: %d -> %d", n, starts.Load())
	}
	if st := f.state(id); !st.ManuallyStopped || st.Restarting {
		t.Fatalf("%+v", st)
	}
	// docker start clears the manual stop and the policy applies again.
	f.e.Start(id)
	eventually(t, "restarts after start", func() bool { return starts.Load() >= n+2 })
	f.e.Stop(id, nil)
}

func TestStopOfARunningPolicyContainerIsFinal(t *testing.T) {
	f := newFixture(t)
	starts := f.program()
	id := f.createWith(RestartPolicy{Name: PolicyUnlessStopped}, "run")
	f.e.Start(id)
	if err := f.e.Stop(id, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if st := f.state(id); starts.Load() != 1 || st.Status != StatusExited || !st.ManuallyStopped {
		t.Fatalf("starts %d %+v", starts.Load(), st)
	}
}

func TestKillSIGKILLIsAManualStopButOtherSignalsAreNot(t *testing.T) {
	f := newFixture(t)
	starts := f.program()
	id := f.createWith(RestartPolicy{Name: PolicyAlways}, "run")
	f.e.Start(id)
	// SIGTERM ends the fake workload; the policy restarts it.
	if err := f.e.Kill(id, "TERM"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "policy restart after SIGTERM", func() bool { return starts.Load() == 2 && f.state(id).Status == StatusRunning })
	if err := f.e.Kill(id, "KILL"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "exited", func() bool { return f.state(id).Status == StatusExited })
	time.Sleep(400 * time.Millisecond)
	if starts.Load() != 2 || !f.state(id).ManuallyStopped {
		t.Fatalf("starts %d %+v", starts.Load(), f.state(id))
	}
}

func TestRestartingContainerStopKillAndRemove(t *testing.T) {
	f := newFixture(t)
	gate := make(chan struct{})
	f.program(gate)
	id := f.holdRestarting(gate)
	if st := f.state(id); !st.Restarting {
		t.Fatalf("%+v", st)
	}
	if err := f.e.Remove(id, false); errdefs.KindOf(err) != errdefs.KindConflict || !strings.Contains(err.Error(), "restarting") {
		t.Fatalf("rm of a restarting container: %v", err)
	}
	if err := f.e.Stop(id, nil); err != nil {
		t.Fatal(err)
	}
	if st := f.state(id); st.Status != StatusExited || !st.ManuallyStopped || st.Restarting {
		t.Fatalf("%+v", st)
	}
	c, _ := f.e.Lookup(id)
	c.mu.Lock()
	timer := c.restartTimer
	c.mu.Unlock()
	if timer != nil {
		t.Fatal("restart timer still armed after stop")
	}
	// rm -f of a restarting container cancels its timer and removes it.
	gate2 := make(chan struct{})
	f.program(gate2)
	id2 := f.holdRestarting(gate2)
	if err := f.e.Remove(id2, true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.e.Lookup(id2); errdefs.KindOf(err) != errdefs.KindNotFound {
		t.Fatalf("still there: %v", err)
	}
	// docker start on a restarting container starts it at once.
	gate3 := make(chan struct{})
	starts := f.program(gate3)
	id3 := f.holdRestarting(gate3)
	n := starts.Load()
	if err := f.e.Start(id3); err != nil {
		t.Fatal(err)
	}
	if st := f.state(id3); st.Status != StatusRunning || starts.Load() != n+1 {
		t.Fatalf("%+v starts %d", st, starts.Load())
	}
	f.e.Shutdown(1)
}

func TestRestoreAtDaemonStart(t *testing.T) {
	f := newFixture(t)
	starts := f.program()
	type tc struct {
		policy          RestartPolicy
		manuallyStopped bool
		exitCode        int
		neverStarted    bool
		want            bool
	}
	cases := map[string]tc{
		"always":                    {policy: RestartPolicy{Name: PolicyAlways}, want: true},
		"always-manually-stopped":   {policy: RestartPolicy{Name: PolicyAlways}, manuallyStopped: true, want: true},
		"unless-stopped":            {policy: RestartPolicy{Name: PolicyUnlessStopped}, want: true},
		"unless-stopped-stopped":    {policy: RestartPolicy{Name: PolicyUnlessStopped}, manuallyStopped: true, want: false},
		"on-failure-was-running":    {policy: RestartPolicy{Name: PolicyOnFailure}, want: true}, // killed with the daemon: 137
		"on-failure-clean-exit":     {policy: RestartPolicy{Name: PolicyOnFailure}, exitCode: 0, want: false},
		"no":                        {policy: RestartPolicy{Name: PolicyNo}, want: false},
		"always-never-started":      {policy: RestartPolicy{Name: PolicyAlways}, neverStarted: true, want: false},
		"unless-stopped-was-exited": {policy: RestartPolicy{Name: PolicyUnlessStopped}, exitCode: 2, want: true},
	}
	ids := map[string]string{}
	for name, k := range cases {
		id := f.createWith(k.policy, "run")
		ids[name] = id
		c, _ := f.e.Lookup(id)
		c.mu.Lock()
		switch {
		case k.neverStarted:
		case name == "on-failure-was-running" || name == "always" || name == "unless-stopped":
			// Running when the daemon died; the PID names no process.
			c.rec.State = State{Status: StatusRunning, Pid: 999999, PidStart: 12345, StartedAt: time.Now()}
		default:
			c.rec.State = State{Status: StatusExited, ExitCode: k.exitCode, StartedAt: time.Now(), FinishedAt: time.Now(), ManuallyStopped: k.manuallyStopped}
		}
		f.e.persist(c)
		c.mu.Unlock()
	}
	f.open() // daemon restart
	f.e.RestoreRestartPolicies()
	wantStarts := int32(0)
	for _, k := range cases {
		if k.want {
			wantStarts++
		}
	}
	eventually(t, "restores", func() bool { return starts.Load() == wantStarts })
	time.Sleep(200 * time.Millisecond)
	if starts.Load() != wantStarts {
		t.Fatalf("starts %d, want %d", starts.Load(), wantStarts)
	}
	for name, k := range cases {
		st := f.state(ids[name])
		if running := st.Status == StatusRunning; running != k.want {
			t.Errorf("%s: status %s, want running=%v", name, st.Status, k.want)
		}
	}
	f.e.Shutdown(1)
}

func TestShutdownIsNotAManualStop(t *testing.T) {
	f := newFixture(t)
	f.program()
	id := f.createWith(RestartPolicy{Name: PolicyUnlessStopped}, "run")
	f.e.Start(id)
	f.e.Shutdown(1)
	st := f.state(id)
	if st.Status != StatusExited || st.ManuallyStopped {
		t.Fatalf("after shutdown: %+v", st)
	}
	// The policy must not have restarted it while the daemon was stopping.
	time.Sleep(300 * time.Millisecond)
	if f.state(id).Status != StatusExited {
		t.Fatalf("restarted during shutdown: %+v", f.state(id))
	}
	f.open()
	f.e.RestoreRestartPolicies()
	eventually(t, "restored", func() bool { return f.state(id).Status == StatusRunning })
	f.e.Shutdown(1)
}

func TestRestartPolicyValidation(t *testing.T) {
	f := newFixture(t)
	for name, hc := range map[string]HostConfig{
		"unknown":            {RestartPolicy: RestartPolicy{Name: "sometimes"}},
		"count-with-always":  {RestartPolicy: RestartPolicy{Name: PolicyAlways, MaximumRetryCount: 3}},
		"negative-count":     {RestartPolicy: RestartPolicy{Name: PolicyOnFailure, MaximumRetryCount: -1}},
		"autoremove-and-pol": {AutoRemove: true, RestartPolicy: RestartPolicy{Name: PolicyUnlessStopped}},
	} {
		hc := hc
		if _, _, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image}, HostConfig: &hc}, "", ""); errdefs.KindOf(err) != errdefs.KindInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	id := f.create("sh")
	if err := f.e.UpdateRestartPolicy(id, RestartPolicy{Name: PolicyOnFailure, MaximumRetryCount: 5}); err != nil {
		t.Fatal(err)
	}
	f.open()
	c, _ := f.e.Lookup(id)
	if p := c.Snapshot().HostConfig.RestartPolicy; p.Name != PolicyOnFailure || p.MaximumRetryCount != 5 {
		t.Fatalf("policy not persisted: %+v", p)
	}
	if err := f.e.UpdateRestartPolicy(id, RestartPolicy{Name: "bogus"}); errdefs.KindOf(err) != errdefs.KindInvalid {
		t.Fatal(err)
	}
}

func TestNextRestartDelay(t *testing.T) {
	d := time.Duration(0)
	var got []time.Duration
	for i := 0; i < 12; i++ {
		d = nextRestartDelay(d, time.Second)
		got = append(got, d)
	}
	if got[0] != 100*time.Millisecond || got[1] != 200*time.Millisecond || got[10] != time.Minute || got[11] != time.Minute {
		t.Fatalf("%v", got)
	}
	if nextRestartDelay(time.Minute, 10*time.Second) != 100*time.Millisecond {
		t.Fatal("a healthy run did not reset the delay")
	}
}

func TestTransitionGuardRefusesIllegalMoves(t *testing.T) {
	f := newFixture(t)
	id := f.create("sh")
	c, _ := f.e.Lookup(id)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := f.e.transition(c, StatusRunning); errdefs.KindOf(err) != errdefs.KindConflict {
		t.Fatalf("created -> running allowed: %v", err)
	}
	if c.rec.State.Status != StatusCreated {
		t.Fatalf("refused transition changed the state to %s", c.rec.State.Status)
	}
	c.rec.State.Status = StatusRemoving
	if err := f.e.transition(c, StatusStarting); err == nil {
		t.Fatal("removing -> starting allowed")
	}
}

func TestLifecycleEvents(t *testing.T) {
	f := newFixture(t)
	f.program()
	_, sub := f.e.Events.Subscribe(time.Time{})
	defer sub.Close()
	id, _, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image, Entrypoint: StrSlice{"sh"}, Cmd: []string{"run"},
		Labels: map[string]string{"com.docker.compose.project": "demo"}}}, "evt", "")
	if err != nil {
		t.Fatal(err)
	}
	f.e.Start(id)
	f.e.Stop(id, nil)
	f.e.Remove(id, false)
	var got []string
	deadline := time.After(5 * time.Second)
	for len(got) < 6 {
		select {
		case ev := <-sub.C:
			if ev.Type != "container" || ev.ID != id || ev.Attrs["name"] != "evt" || ev.Attrs["com.docker.compose.project"] != "demo" {
				t.Fatalf("event %+v", ev)
			}
			a := ev.Action
			if a == "die" {
				a += ":" + ev.Attrs["exitCode"]
			}
			if a == "kill" {
				a += ":" + ev.Attrs["signal"]
			}
			got = append(got, a)
		case <-deadline:
			t.Fatalf("events so far %v", got)
		}
	}
	want := "create start kill:15 die:143 stop destroy"
	if strings.Join(got, " ") != want {
		t.Fatalf("events %q, want %q", strings.Join(got, " "), want)
	}
}

// An explicit restart is not a restart-policy restart: RestartCount stays,
// as with dockerd (Docker 29.8.1 measured 0 after docker restart).
func TestExplicitRestartDoesNotCountAsPolicyRestart(t *testing.T) {
	f := newFixture(t)
	starts := f.program()
	id := f.createWith(RestartPolicy{Name: PolicyUnlessStopped}, "run")
	if err := f.e.Start(id); err != nil {
		t.Fatal(err)
	}
	zero := 0
	if err := f.e.Restart(id, &zero); err != nil {
		t.Fatal(err)
	}
	eventually(t, "running again", func() bool { return f.state(id).Status == StatusRunning && starts.Load() == 2 })
	c, _ := f.e.Lookup(id)
	if n := c.Snapshot().RestartCount; n != 0 {
		t.Fatalf("RestartCount after an explicit restart = %d, want 0", n)
	}
}
