package procguard

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

// fakeProc builds a /proc with the given pid -> "ppid" for this uid.
func fakeProc(t *testing.T, ppids map[int]int) *ProcCounter {
	t.Helper()
	root := t.TempDir()
	for pid, pp := range ppids {
		dir := filepath.Join(root, strconv.Itoa(pid))
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		// comm holds spaces and parentheses on purpose.
		stat := fmt.Sprintf("%d (we ird) name) S %d 1 1 0 -1\n", pid, pp)
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	os.Mkdir(filepath.Join(root, "self"), 0o755) // not a pid
	return &ProcCounter{Root: root, UID: uint32(os.Getuid())}
}

func TestProcCounterCountsTheUIDsTreeNotItsRoot(t *testing.T) {
	// 100 is the app process (parent 1 is another uid's); 101..105 are its descendants.
	c := fakeProc(t, map[int]int{100: 1, 101: 100, 102: 101, 103: 101, 104: 100, 105: 104})
	if n, err := c.Count(); err != nil || n != 5 {
		t.Fatalf("count %d, err %v", n, err)
	}
	// Another uid's processes are not counted.
	c.UID++
	if _, err := c.Count(); err == nil {
		t.Fatal("a uid with no processes should be an error, not zero")
	}
}

func TestProcCounterOnTheRealProcAtLeastSeesChildren(t *testing.T) {
	c := NewProcCounter()
	if _, err := c.Count(); err != nil {
		t.Skip("no /proc:", err)
	}
	cmd := exec.Command("sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	if n, _ := c.Count(); n < 1 {
		t.Fatalf("count %d", n)
	}
}
