package engine

import (
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/Lord1Egypt/ThothDock/internal/procguard"
	"github.com/Lord1Egypt/ThothDock/internal/store"
)

// PressureRecord is the last process-pressure observation, kept on disk so
// the next daemon can say what the previous one saw before it ended.
type PressureRecord struct {
	Level string    `json:"level"`
	Count int       `json:"count"`
	Cap   int       `json:"cap"`
	At    time.Time `json:"at"`
}

// Recovery describes how this daemon started relative to the previous one.
// It reports facts; it never claims to know why Android ended a process.
type Recovery struct {
	At time.Time `json:"at"`
	// PreviousExit is "unclean" when the previous daemon left its pid file
	// behind, else "clean" (a first start is "clean" too).
	PreviousExit string `json:"previousExit"`
	// PreviousExitCode is what the Android app saw when the daemon died.
	PreviousExitCode *int   `json:"previousExitCode,omitempty"`
	ReasonKnown      bool   `json:"reasonKnown"`
	Reason           string `json:"reason"`
	// LastPressure is the last observation the previous daemon recorded.
	LastPressure *PressureRecord `json:"lastPressure,omitempty"`
	// Interrupted lists the containers that were running when the previous
	// daemon ended; Restored those a restart policy has started again.
	Interrupted []string `json:"interrupted"`
	Restored    []string `json:"restored"`
	// Restoring is true while restart policies are still being applied.
	Restoring bool `json:"restoring"`
}

// pressureState is the engine's side of Process Guard.
type pressureState struct {
	mon *procguard.Monitor

	mu       sync.Mutex
	recovery Recovery
}

// Pressure returns the engine's process monitor (nil when none is configured).
func (e *Engine) Pressure() *procguard.Monitor { return e.cfg.Pressure }

// PendingCount is the number of created, not yet started containers.
func (e *Engine) PendingCount() int { return e.pending() }

// pending counts containers that exist but have never started: the burst a
// Compose project creates before it starts anything.
func (e *Engine) pending() int {
	n := 0
	for _, r := range e.List() {
		if r.State.Status == StatusCreated {
			n++
		}
	}
	return n
}

// PressureWarning measures now and returns the user-facing warning, or "".
func (e *Engine) PressureWarning() string {
	if e.cfg.Pressure == nil {
		return ""
	}
	return procguard.Message(e.cfg.Pressure.Observe(), e.pending())
}

// observePressure refreshes the cached observation after a lifecycle event;
// the monitor's OnChange hook persists and logs it.
func (e *Engine) observePressure() {
	if e.cfg.Pressure != nil {
		e.cfg.Pressure.Observe()
	}
}

// PressureFile is where the last observation is kept.
func (e *Engine) PressureFile() string { return filepath.Join(e.Layout.Run(), "pressure.json") }

// RecordPressure persists s and logs a warning when it is elevated.
func (e *Engine) RecordPressure(s procguard.Snapshot) {
	if s.Level == procguard.Unknown {
		return
	}
	rec := PressureRecord{Level: s.Level.String(), Count: s.Count, Cap: s.Cap, At: s.At.UTC()}
	if err := store.WriteJSONAtomic(e.PressureFile(), &rec); err != nil {
		e.log.Warn("recording process pressure", "err", err)
	}
	if s.Level >= procguard.Elevated {
		e.log.Warn("Android process pressure", "level", s.Level.String(), "processes", s.Count, "reference_cap", s.Cap)
	} else {
		e.log.Info("process pressure", "level", s.Level.String(), "processes", s.Count)
	}
}

// ReadLastPressure returns what the previous daemon recorded, if anything.
func ReadLastPressure(path string) *PressureRecord {
	var rec PressureRecord
	if err := store.ReadJSON(path, &rec); err != nil || rec.Level == "" {
		return nil
	}
	return &rec
}

// RecoveryFile is where the latest recovery report is kept.
func (e *Engine) RecoveryFile() string { return filepath.Join(e.Layout.Root, "recovery.json") }

// SetRecovery records how this start relates to the previous run.
// Interrupted is filled from what reconcile found.
func (e *Engine) SetRecovery(r Recovery) {
	e.mu.Lock()
	restoring := len(e.restore) > 0
	e.mu.Unlock()
	e.press.mu.Lock()
	r.Interrupted = append([]string{}, e.interrupted...)
	r.Restored = []string{}
	r.Restoring = restoring
	e.press.recovery = r
	e.press.mu.Unlock()
	e.persistRecovery()
}

// Recovery returns the report for this start.
func (e *Engine) Recovery() Recovery {
	e.press.mu.Lock()
	defer e.press.mu.Unlock()
	r := e.press.recovery
	r.Interrupted = append([]string{}, r.Interrupted...)
	r.Restored = append([]string{}, r.Restored...)
	return r
}

func (e *Engine) persistRecovery() {
	r := e.Recovery()
	if r.At.IsZero() {
		return
	}
	if err := store.WriteJSONAtomic(e.RecoveryFile(), &r); err != nil {
		e.log.Warn("recording the recovery report", "err", err)
	}
}

func (e *Engine) noteRestored(name string) {
	e.press.mu.Lock()
	e.press.recovery.Restored = append(e.press.recovery.Restored, name)
	e.press.mu.Unlock()
}

func (e *Engine) doneRestoring() {
	e.press.mu.Lock()
	e.press.recovery.Restoring = false
	e.press.mu.Unlock()
	e.persistRecovery()
}

// DescribeRecovery builds the reason text from facts only.
func DescribeRecovery(unclean bool, exitCode *int, last *PressureRecord) string {
	if !unclean {
		return "The previous daemon shut down cleanly."
	}
	killed := exitCode != nil && (*exitCode == 137 || *exitCode == -9)
	switch {
	case killed && last != nil && last.Level != "ok":
		return fmt.Sprintf("The previous daemon was killed (exit %d, SIGKILL). The last process count it recorded was %d against Android's default limit of %d (%s). That is consistent with Android trimming background processes, but the app cannot confirm who sent the signal.",
			*exitCode, last.Count, last.Cap, last.Level)
	case killed:
		return fmt.Sprintf("The previous daemon was killed (exit %d, SIGKILL); the cause was not recorded. Android trimming processes, the low-memory killer and a force-stop all look like this.", *exitCode)
	case exitCode != nil:
		return fmt.Sprintf("The previous daemon ended abnormally (exit %d); no cause was recorded.", *exitCode)
	}
	return "The previous daemon did not shut down cleanly and Android gave the app no reason; a force-stop, a crash or a device restart would look the same."
}
