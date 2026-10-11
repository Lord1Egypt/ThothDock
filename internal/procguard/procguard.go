// Package procguard watches how many processes the app owns, because Android
// can kill an app's child processes (and with them the engine) when too many
// are alive. It measures on demand only: there is no goroutine, ticker or
// timer; callers sample at container lifecycle events and when a status page
// is read.
//
// What is and is not known: Android's ActivityManager trims "phantom
// processes" (children of app processes) when their number passes a cap. The
// cap is a device_config value that an app cannot read; AOSP's default is 32
// and this package uses it as a labelled reference, never as a promise of a
// safe budget. The measured count is the app's own processes, so processes of
// other apps are not visible here and real pressure can be higher.
package procguard

import (
	"fmt"
	"sync"
	"time"
)

// Level is the observed pressure.
type Level int

const (
	// Unknown: no measurement is possible (not an Android app, /proc unreadable).
	Unknown Level = iota
	OK
	Elevated
	High
)

func (l Level) String() string {
	switch l {
	case OK:
		return "ok"
	case Elevated:
		return "elevated"
	case High:
		return "high"
	}
	return "unknown"
}

const (
	// DefaultCap is AOSP's default for max_phantom_processes.
	DefaultCap = 32
	// PerContainerEstimate is the fewest processes a PRoot container adds
	// (loader, PRoot and the container's own process); only used to project
	// containers that exist but have not started yet.
	PerContainerEstimate = 3
	// hysteresis keeps the level from flapping around a threshold.
	hysteresis = 2
	// MaxAge is how old a cached observation may be when a status page
	// reads it before sampling again.
	MaxAge = 10 * time.Second
)

// Counter reports the number of processes Android would track for the app.
type Counter interface {
	Count() (int, error)
}

// Snapshot is one observation.
type Snapshot struct {
	Level Level
	Count int       // processes observed; meaningless when Level is Unknown
	Cap   int       // the reference cap the level is judged against
	At    time.Time // zero if never observed
	Err   string    // why the level is Unknown
}

// Monitor holds the latest observation. The zero value is not usable; use New.
type Monitor struct {
	counter Counter
	cap     int
	now     func() time.Time

	mu       sync.Mutex
	last     Snapshot
	onChange func(Snapshot)
}

// New returns a Monitor judging counter's readings against cap. A nil counter
// or a cap below 1 gives a monitor that always reports Unknown.
func New(counter Counter, cap int, now func() time.Time) *Monitor {
	if now == nil {
		now = time.Now
	}
	return &Monitor{counter: counter, cap: cap, now: now}
}

// OnChange registers f, called (outside the lock) whenever an observation
// changes the level or moves the count by three or more since the last call.
func (m *Monitor) OnChange(f func(Snapshot)) { m.mu.Lock(); m.onChange = f; m.mu.Unlock() }

// thresholds returns the counts at which Elevated and High start.
func thresholds(cap int) (elevated, high int) {
	return (cap*3 + 3) / 4, (cap*9 + 9) / 10 // ceil(0.75*cap), ceil(0.9*cap)
}

// Observe takes a new measurement.
func (m *Monitor) Observe() Snapshot {
	if m == nil {
		return Snapshot{Err: "process guard is off"}
	}
	if m.counter == nil || m.cap < 1 {
		return m.store(Snapshot{Cap: m.cap, At: m.now(), Err: "not running as an Android app"})
	}
	n, err := m.counter.Count()
	s := Snapshot{Cap: m.cap, At: m.now()}
	if err != nil {
		s.Err = err.Error()
		return m.store(s)
	}
	m.mu.Lock()
	prev := m.last.Level
	m.mu.Unlock()
	s.Count, s.Level = n, level(n, m.cap, prev)
	return m.store(s)
}

// level applies the thresholds with hysteresis relative to the previous level.
func level(n, cap int, prev Level) Level {
	elevated, high := thresholds(cap)
	switch {
	case n >= high:
		return High
	case prev == High && n >= high-hysteresis:
		return High
	case n >= elevated:
		return Elevated
	case (prev == Elevated || prev == High) && n >= elevated-hysteresis:
		return Elevated
	}
	return OK
}

func (m *Monitor) store(s Snapshot) Snapshot {
	m.mu.Lock()
	prev := m.last
	m.last = s
	f := m.onChange
	m.mu.Unlock()
	if f != nil && (s.Level != prev.Level || abs(s.Count-prev.Count) >= 3) {
		f(s)
	}
	return s
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// Last returns the cached observation without measuring (zero At: none yet).
func (m *Monitor) Last() Snapshot {
	if m == nil {
		return Snapshot{Err: "process guard is off"}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.last
}

// Recent returns the cached observation if it is younger than maxAge, else
// measures again.
func (m *Monitor) Recent(maxAge time.Duration) Snapshot {
	if m == nil {
		return Snapshot{Err: "process guard is off"}
	}
	if s := m.Last(); !s.At.IsZero() && m.now().Sub(s.At) < maxAge && s.At.Before(m.now().Add(time.Second)) {
		return s
	}
	return m.Observe()
}

// Message is the user-facing warning for s, or "" when there is nothing to
// say. pending is the number of containers that exist but have not started
// (an estimate of the burst that is about to happen).
func Message(s Snapshot, pending int) string {
	if s.Level == Unknown || s.Cap < 1 {
		return ""
	}
	elevated, high := thresholds(s.Cap)
	projected := s.Count + pending*PerContainerEstimate
	pl := Level(OK)
	switch {
	case projected >= high:
		pl = High
	case projected >= elevated:
		pl = Elevated
	}
	if s.Level < Elevated && pl < Elevated {
		return ""
	}
	const advice = "Android process pressure may cause background containers or the engine to be terminated. Consider stopping unused workloads before starting additional containers."
	basis := fmt.Sprintf("ThothTerm currently has %d processes; Android's default limit on such processes is %d (this device's value cannot be read, and other apps' processes may count too).", s.Count, s.Cap)
	if pending > 0 && projected > s.Count {
		basis += fmt.Sprintf(" %d created container(s) have not started yet; estimated at least %d more processes (about %d each).", pending, pending*PerContainerEstimate, PerContainerEstimate)
	}
	return advice + " " + basis
}
