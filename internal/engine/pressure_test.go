package engine

import (
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Lord1Egypt/ThothDock/internal/procguard"
)

type countFake struct{ n atomic.Int64 }

func (c *countFake) Count() (int, error) { return int(c.n.Load()), nil }

// pressureWarnings keeps only Process Guard's warnings (the host's own DNS
// notice may also be in the list).
func pressureWarnings(w []string) []string {
	var out []string
	for _, x := range w {
		if strings.Contains(x, "process pressure") {
			out = append(out, x)
		}
	}
	return out
}

func withPressure(f *fixture, n int) *countFake {
	c := &countFake{}
	c.n.Store(int64(n))
	f.cfg.Pressure = procguard.New(c, 32, nil)
	f.cfg.Pressure.OnChange(f.e.RecordPressure)
	f.open()
	f.cfg.Pressure.OnChange(f.e.RecordPressure)
	return c
}

func TestCreateWarnsOnlyUnderObservedPressureAndChangesNothing(t *testing.T) {
	f := newFixture(t)
	c := withPressure(f, 10)
	_, w, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image}}, "calm", "")
	if w = pressureWarnings(w); err != nil || len(w) != 0 {
		t.Fatalf("calm: %v %v", err, w)
	}
	c.n.Store(30)
	before := len(f.e.List())
	id, w, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image}}, "busy", "")
	if err != nil {
		t.Fatal(err)
	}
	if w = pressureWarnings(w); len(w) != 1 || !strings.Contains(w[0], "Android process pressure may cause background containers or the engine to be terminated") {
		t.Fatalf("warnings %v", w)
	}
	if len(f.e.List()) != before+1 || f.state(id).Status != StatusCreated {
		t.Fatal("the warning changed container state")
	}
	// Existing containers are untouched too.
	for _, r := range f.e.List() {
		if r.State.Status != StatusCreated {
			t.Fatalf("%s is %s", r.Name, r.State.Status)
		}
	}
}

func TestAComposeBurstIsWarnedBeforeTheCountIsHigh(t *testing.T) {
	f := newFixture(t)
	withPressure(f, 20)
	var warned int
	for i := 0; i < 6; i++ {
		_, w, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image}}, "", "")
		if err != nil {
			t.Fatal(err)
		}
		if w = pressureWarnings(w); len(w) > 0 {
			warned++
			if !strings.Contains(w[0], "have not started yet") {
				t.Fatalf("burst warning lacks the estimate: %q", w[0])
			}
		}
		if i == 0 && warned != 0 {
			t.Fatal("warned on the first container")
		}
	}
	if warned == 0 {
		t.Fatal("six created containers on 20 processes were never warned about")
	}
}

func TestStartIsNeverRefusedAndMissingInformationIsSilent(t *testing.T) {
	f := newFixture(t)
	f.program()
	c := withPressure(f, 45) // far over the reference cap
	id := f.createWith(RestartPolicy{}, "run")
	if err := f.e.Start(id); err != nil {
		t.Fatalf("a start was refused under pressure: %v", err)
	}
	eventually(t, "running", func() bool { return f.state(id).Status == StatusRunning })
	// No monitor: nothing at all.
	g := newFixture(t)
	if _, w, _ := g.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: g.image}}, "", ""); len(pressureWarnings(w)) != 0 {
		t.Fatalf("warned without a monitor: %v", w)
	}
	_ = c
	f.e.Shutdown(1)
}

func TestConcurrentLifecycleOperationsUnderPressure(t *testing.T) {
	f := newFixture(t)
	f.program()
	withPressure(f, 31)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := f.createWith(RestartPolicy{}, "run")
			if err := f.e.Start(id); err != nil {
				t.Error(err)
				return
			}
			f.e.PressureWarning()
			t.Log(f.e.Recovery().PreviousExit)
			f.e.Stop(id, nil)
		}()
	}
	wg.Wait()
	f.e.Shutdown(1)
}

func TestMeasuringStartsNoBackgroundWork(t *testing.T) {
	f := newFixture(t)
	f.program()
	withPressure(f, 12)
	time.Sleep(50 * time.Millisecond)
	before := runtime.NumGoroutine()
	for i := 0; i < 50; i++ {
		f.e.PressureWarning()
		f.e.Pressure().Recent(procguard.MaxAge)
	}
	time.Sleep(50 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("goroutines %d -> %d after 50 observations", before, after)
	}
}

func TestPressureIsPersistedOnChangeForTheNextDaemon(t *testing.T) {
	f := newFixture(t)
	c := withPressure(f, 12)
	f.e.Pressure().Observe()
	c.n.Store(27)
	f.e.Pressure().Observe()
	rec := ReadLastPressure(f.e.PressureFile())
	if rec == nil || rec.Count != 27 || rec.Level != "elevated" || rec.Cap != 32 {
		t.Fatalf("%+v", rec)
	}
	if fi, _ := os.Stat(f.e.PressureFile()); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
}

func TestRecoveryReportsInterruptedAndRestoredContainers(t *testing.T) {
	f := newFixture(t)
	f.program()
	keep := f.createWith(RestartPolicy{Name: PolicyUnlessStopped}, "run")
	plain := f.createWith(RestartPolicy{}, "run")
	for _, id := range []string{keep, plain} {
		c, _ := f.e.Lookup(id)
		c.mu.Lock()
		c.rec.State = State{Status: StatusRunning, Pid: 999999, PidStart: 12345, StartedAt: time.Now()}
		f.e.persist(c)
		c.mu.Unlock()
	}
	f.open() // the daemon was killed and starts again
	code := 137
	last := &PressureRecord{Level: "high", Count: 31, Cap: 32}
	f.e.SetRecovery(Recovery{At: time.Now(), PreviousExit: "unclean", PreviousExitCode: &code, LastPressure: last,
		Reason: DescribeRecovery(true, &code, last)})
	r := f.e.Recovery()
	if len(r.Interrupted) != 2 || !r.Restoring || r.ReasonKnown {
		t.Fatalf("%+v", r)
	}
	f.e.RestoreRestartPolicies()
	eventually(t, "restored", func() bool { return !f.e.Recovery().Restoring })
	r = f.e.Recovery()
	keepName := ""
	for _, rec := range f.e.List() {
		if rec.ID == keep {
			keepName = rec.Name
		}
	}
	if len(r.Restored) != 1 || r.Restored[0] != keepName {
		t.Fatalf("restored %v, want [%s]", r.Restored, keepName)
	}
	if !strings.Contains(r.Reason, "consistent with Android trimming") || !strings.Contains(r.Reason, "cannot confirm") {
		t.Fatalf("reason %q", r.Reason)
	}
	if f.state(plain).Status == StatusRunning {
		t.Fatal("a container without a restart policy was started")
	}
	if _, err := os.Stat(f.e.RecoveryFile()); err != nil {
		t.Fatal(err)
	}
	f.e.Shutdown(1)
}

func TestDescribeRecoveryNeverClaimsACause(t *testing.T) {
	k := 137
	for _, s := range []string{
		DescribeRecovery(true, nil, nil),
		DescribeRecovery(true, &k, nil),
		DescribeRecovery(true, &k, &PressureRecord{Level: "ok", Count: 20, Cap: 32}),
		DescribeRecovery(true, &k, &PressureRecord{Level: "high", Count: 31, Cap: 32}),
	} {
		if strings.Contains(s, "was caused by") || strings.Contains(s, "because") {
			t.Fatalf("claims a cause: %q", s)
		}
	}
	if !strings.Contains(DescribeRecovery(false, nil, nil), "cleanly") {
		t.Fatal("clean start")
	}
}
