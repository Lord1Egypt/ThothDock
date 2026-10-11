package procguard

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

type fakeCounter struct {
	n   int
	err error
}

func (f *fakeCounter) Count() (int, error) { return f.n, f.err }

func monitor(c Counter) (*Monitor, *time.Time) {
	t := time.Unix(1_000_000, 0)
	return New(c, 32, func() time.Time { return t }), &t
}

func TestLowObservedPressureSaysNothing(t *testing.T) {
	m, _ := monitor(&fakeCounter{n: 12})
	s := m.Observe()
	if s.Level != OK || s.Count != 12 || Message(s, 0) != "" {
		t.Fatalf("%+v %q", s, Message(s, 0))
	}
}

func TestTheOwnersFourContainersAreNotAWarning(t *testing.T) {
	// 22 processes ran for hours without a kill (one-hour soak, 61/61 samples).
	m, _ := monitor(&fakeCounter{n: 22})
	if s := m.Observe(); s.Level != OK || Message(s, 0) != "" {
		t.Fatalf("%+v", s)
	}
}

func TestHighObservedPressureWarns(t *testing.T) {
	m, _ := monitor(&fakeCounter{n: 30})
	s := m.Observe()
	msg := Message(s, 0)
	if s.Level != High || !strings.Contains(msg, "Android process pressure may cause background containers or the engine to be terminated") ||
		!strings.Contains(msg, "30 processes") || !strings.Contains(msg, "default limit") {
		t.Fatalf("%+v %q", s, msg)
	}
	if strings.Contains(msg, "remaining") || strings.Contains(msg, "safe") {
		t.Fatalf("the warning claims a budget: %q", msg)
	}
}

func TestMissingInformationIsUnknownNotZero(t *testing.T) {
	for name, m := range map[string]*Monitor{
		"counter error": New(&fakeCounter{err: errors.New("denied")}, 32, nil),
		"no counter":    New(nil, 32, nil),
		"no cap":        New(&fakeCounter{n: 99}, 0, nil),
		"nil monitor":   nil,
	} {
		s := m.Observe()
		if s.Level != Unknown || s.Err == "" || Message(s, 5) != "" {
			t.Fatalf("%s: %+v", name, s)
		}
	}
}

func TestThresholdsAndHysteresis(t *testing.T) {
	e, h := thresholds(32)
	if e != 24 || h != 29 {
		t.Fatalf("thresholds %d %d", e, h)
	}
	c := &fakeCounter{}
	m, _ := monitor(c)
	seq := []struct {
		n    int
		want Level
	}{{23, OK}, {24, Elevated}, {23, Elevated}, {22, Elevated}, {21, OK}, {29, High}, {28, High}, {27, High}, {26, Elevated}, {22, Elevated}, {21, OK}}
	for _, st := range seq {
		c.n = st.n
		if got := m.Observe().Level; got != st.want {
			t.Fatalf("n=%d: %v, want %v", st.n, got, st.want)
		}
	}
}

func TestPendingContainersProjectABurst(t *testing.T) {
	m, _ := monitor(&fakeCounter{n: 18})
	s := m.Observe()
	if Message(s, 1) != "" {
		t.Fatal("one container on 18 should not warn")
	}
	msg := Message(s, 3)
	if !strings.Contains(msg, "3 created container(s)") || !strings.Contains(msg, "estimated") {
		t.Fatalf("%q", msg)
	}
}

func TestStaleMeasurementsAreRefreshedFreshOnesReused(t *testing.T) {
	c := &fakeCounter{n: 10}
	m, now := monitor(c)
	m.Observe()
	c.n = 25
	if s := m.Recent(MaxAge); s.Count != 10 {
		t.Fatalf("fresh observation not reused: %d", s.Count)
	}
	*now = now.Add(MaxAge + time.Second)
	if s := m.Recent(MaxAge); s.Count != 25 || s.Level != Elevated {
		t.Fatalf("stale observation reused: %+v", s)
	}
	if s := New(c, 32, nil).Last(); !s.At.IsZero() {
		t.Fatal("Last measured")
	}
}

func TestOnChangeFiresOnlyOnRealChanges(t *testing.T) {
	c := &fakeCounter{n: 10}
	m, _ := monitor(c)
	var got []int
	m.OnChange(func(s Snapshot) { got = append(got, s.Count) })
	m.Observe() // level Unknown -> OK
	m.Observe()
	c.n = 11
	m.Observe()
	c.n = 14
	m.Observe()
	if len(got) != 2 || got[0] != 10 || got[1] != 14 {
		t.Fatalf("callbacks %v", got)
	}
}

func TestConcurrentObservationsAreSafe(t *testing.T) {
	c := &fakeCounter{n: 20}
	m, _ := monitor(c)
	done := make(chan struct{})
	for i := 0; i < 16; i++ {
		go func() {
			for j := 0; j < 200; j++ {
				m.Observe()
				m.Recent(MaxAge)
				Message(m.Last(), 2)
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 16; i++ {
		<-done
	}
}

func TestProcCounterCountsDescendantsOfOurUIDNotTheRoot(t *testing.T) {
	c := NewProcCounter()
	before, err := c.Count()
	if err != nil {
		t.Skip("no /proc:", err)
	}
	cmds := make([]*exec.Cmd, 3)
	for i := range cmds {
		cmds[i] = exec.Command("sleep", "5")
		if err := cmds[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		for _, c := range cmds {
			c.Process.Kill()
			c.Wait()
		}
	}()
	after, _ := c.Count()
	if after-before != 3 {
		t.Fatalf("count went from %d to %d with 3 children (this test's own pid %d)", before, after, os.Getpid())
	}
}
