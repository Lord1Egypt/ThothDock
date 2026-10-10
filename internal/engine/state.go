package engine

import (
	"strconv"
	"time"

	"github.com/Lord1Egypt/ThothDock/internal/errdefs"
	"github.com/Lord1Egypt/ThothDock/internal/events"
)

// allowed is the container state machine (docs/nextgen/SYSTEM_LLD.md §1):
// allowed[from][to]. Recovery at daemon start is the only writer that may
// leave it, because the process it describes no longer exists.
var allowed = map[string]map[string]bool{
	StatusCreated:    {StatusStarting: true, StatusRemoving: true},
	StatusStarting:   {StatusRunning: true, StatusCreated: true, StatusExited: true, StatusRestarting: true},
	StatusRunning:    {StatusExited: true, StatusRestarting: true, StatusRemoving: true},
	StatusExited:     {StatusStarting: true, StatusRestarting: true, StatusRemoving: true},
	StatusRestarting: {StatusStarting: true, StatusExited: true, StatusRemoving: true},
	StatusRemoving:   {StatusFailed: true},
	StatusFailed:     {StatusStarting: true, StatusRemoving: true},
}

// transition moves c to status `to`, refusing a move the state machine does
// not allow. Called with c.mu held.
func (e *Engine) transition(c *Container, to string) error {
	from := c.rec.State.Status
	if !allowed[from][to] {
		e.log.Error("refused container state transition", "id", c.rec.ID[:12], "from", from, "to", to)
		return errdefs.Conflict("container %s cannot go from %s to %s", c.rec.ID[:12], from, to)
	}
	c.rec.State.Status = to
	c.rec.State.Restarting = to == StatusRestarting
	return nil
}

// Restart policy names (HostConfig.RestartPolicy.Name).
const (
	PolicyNo            = "no"
	PolicyAlways        = "always"
	PolicyUnlessStopped = "unless-stopped"
	PolicyOnFailure     = "on-failure"
)

// shouldRestart is dockerd's restart decision (ADR-0004). atDaemonStart is
// true when the engine restores containers after it starts: there, "always"
// ignores a manual stop, as dockerd does.
func shouldRestart(p RestartPolicy, exitCode int, manuallyStopped bool, restartCount int, atDaemonStart bool) bool {
	if manuallyStopped && !(atDaemonStart && p.Name == PolicyAlways) {
		return false
	}
	switch p.Name {
	case PolicyAlways, PolicyUnlessStopped:
		return true
	case PolicyOnFailure:
		return exitCode != 0 && (p.MaximumRetryCount == 0 || restartCount < p.MaximumRetryCount)
	}
	return false
}

const (
	minRestartDelay = 100 * time.Millisecond
	maxRestartDelay = time.Minute
	// healthyRun resets the delay: a container that ran this long before
	// it exited is not crash-looping.
	healthyRun = 10 * time.Second
)

// nextRestartDelay doubles the previous delay up to a minute, and starts
// again from 100 ms after a healthy run (dockerd's restart manager).
func nextRestartDelay(previous, ranFor time.Duration) time.Duration {
	if previous == 0 || ranFor >= healthyRun {
		return minRestartDelay
	}
	return min(previous*2, maxRestartDelay)
}

// containerEvent publishes a container event with dockerd's attributes:
// name, image and every label, plus extra. Called with c.mu held.
func (e *Engine) containerEvent(c *Container, action string, extra ...string) {
	attrs := map[string]string{"name": c.rec.Name, "image": c.rec.ImageRef}
	for k, v := range c.rec.Config.Labels {
		attrs[k] = v
	}
	for i := 0; i+1 < len(extra); i += 2 {
		attrs[extra[i]] = extra[i+1]
	}
	e.Events.Publish(events.Event{Type: "container", Action: action, ID: c.rec.ID, Attrs: attrs})
}

func exitCodeAttr(code int) []string { return []string{"exitCode", strconv.Itoa(code)} }
