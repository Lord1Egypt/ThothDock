// Package engine is ThothDock's container engine: the container object
// model, its persistence, and its lifecycle over a runtime.Runtime.
package engine

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Lord1Egypt/ThothDock/internal/errdefs"
	"github.com/Lord1Egypt/ThothDock/internal/image"
	"github.com/Lord1Egypt/ThothDock/internal/logs"
	"github.com/Lord1Egypt/ThothDock/internal/oci"
	"github.com/Lord1Egypt/ThothDock/internal/platform"
	"github.com/Lord1Egypt/ThothDock/internal/procid"
	"github.com/Lord1Egypt/ThothDock/internal/runtime"
	"github.com/Lord1Egypt/ThothDock/internal/securefs"
	"github.com/Lord1Egypt/ThothDock/internal/store"
)

// Config is the engine's policy.
type Config struct {
	// AllowedBindRoots are host directories whose contents containers may
	// bind-mount. Empty: no bind mounts at all.
	AllowedBindRoots []string
	// ResolvConf is copied into containers that set no DNS of their own.
	ResolvConf string
	// LogMaxSize is the rotation size of each container log.
	LogMaxSize int64
}

// Engine manages containers.
type Engine struct {
	Layout  platform.Layout
	Images  *image.Store
	Puller  *image.Puller
	Runtime runtime.Runtime
	cfg     Config
	log     *slog.Logger

	mu         sync.Mutex
	containers map[string]*Container
	names      map[string]string // name -> ID
	execs      map[string]*Exec
}

// Container is a live container object.
type Container struct {
	mu  sync.Mutex
	rec Record
	dir string

	logger  *logs.Logger
	proc    runtime.Process
	runDone chan struct{} // closed when the current run has ended
	stdin   *stdinBroker
	waiters []*waiter
	execs   map[string]*Exec
	gone    bool // removed
}

type waiter struct {
	cond string
	ch   chan WaitResult
}

// WaitResult is the outcome of POST /containers/{id}/wait.
type WaitResult struct {
	StatusCode int
	Error      string
}

// New loads every container and reconciles their state with reality.
func New(layout platform.Layout, images *image.Store, puller *image.Puller, rt runtime.Runtime, cfg Config, log *slog.Logger) (*Engine, error) {
	if cfg.LogMaxSize == 0 {
		cfg.LogMaxSize = 10 << 20
	}
	e := &Engine{Layout: layout, Images: images, Puller: puller, Runtime: rt, cfg: cfg, log: log,
		containers: map[string]*Container{}, names: map[string]string{}}
	entries, err := os.ReadDir(layout.Containers())
	if err != nil {
		return nil, err
	}
	for _, ent := range entries {
		dir := filepath.Join(layout.Containers(), ent.Name())
		var rec Record
		if err := store.ReadJSON(filepath.Join(dir, "config.json"), &rec); err != nil {
			log.Warn("removing incomplete container directory", "dir", dir, "reason", err)
			if err := securefs.RemoveTree(dir); err != nil {
				return nil, err
			}
			continue
		}
		if rec.ID != ent.Name() || !validID(rec.ID) {
			return nil, fmt.Errorf("container directory %s holds container %q", dir, rec.ID)
		}
		c := &Container{rec: rec, dir: dir}
		if rec.State.Status == StatusRemoving {
			log.Info("finishing interrupted removal", "id", rec.ID)
			if err := securefs.RemoveTree(dir); err != nil {
				return nil, err
			}
			continue
		}
		if err := e.reconcile(c); err != nil {
			return nil, err
		}
		if c.logger, err = logs.Open(filepath.Join(dir, "container.log"), cfg.LogMaxSize); err != nil {
			return nil, err
		}
		c.stdin = newStdinBroker()
		e.containers[rec.ID] = c
		e.names[rec.Name] = rec.ID
	}
	return e, nil
}

// reconcile never trusts a persisted "running": ThothDock cannot reattach
// to a previous daemon's process output, so a survivor is killed and every
// formerly running container is recorded as exited.
func (e *Engine) reconcile(c *Container) error {
	st := &c.rec.State
	if st.Status != StatusRunning && st.Status != StatusStarting {
		return nil
	}
	if st.Pid > 0 && procid.Alive(st.Pid, st.PidStart) {
		e.log.Warn("killing container process left by a previous daemon", "id", c.rec.ID, "pid", st.Pid)
		syscall.Kill(-st.Pid, syscall.SIGKILL)
		syscall.Kill(st.Pid, syscall.SIGKILL)
	}
	st.Status = StatusExited
	st.ExitCode = 137
	st.Error = "ThothDock daemon restarted; the container's process did not survive"
	st.FinishedAt = time.Now().UTC()
	st.Pid, st.PidStart = 0, 0
	return e.persist(c)
}

func (e *Engine) persist(c *Container) error {
	return store.WriteJSONAtomic(filepath.Join(c.dir, "config.json"), &c.rec)
}

var idRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

func validID(id string) bool { return idRe.MatchString(id) }

var nameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]+$`)

func newID() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

var (
	adjectives = []string{"amber", "ancient", "bold", "bright", "calm", "clever", "cosmic", "eager", "gentle", "golden", "hidden", "lucid", "noble", "patient", "quiet", "radiant", "serene", "silent", "swift", "wise"}
	sages      = []string{"imhotep", "hypatia", "thoth", "nefertiti", "ptolemy", "euclid", "ahmose", "hatshepsut", "khufu", "alhazen", "seshat", "maat", "akhenaten", "ramses", "ibnsina", "khwarizmi", "tiye", "djoser", "neith", "anhur"}
)

func pick(list []string) string {
	n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(list))))
	return list[n.Int64()]
}

func (e *Engine) generateNameLocked() string {
	for i := 0; ; i++ {
		n := pick(adjectives) + "_" + pick(sages)
		if i > 20 {
			n += strconv.Itoa(i)
		}
		if _, taken := e.names[n]; !taken {
			return n
		}
	}
}

// Lookup resolves a full ID, a name (with or without "/") or a unique ID
// prefix.
func (e *Engine) Lookup(ref string) (*Container, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lookupLocked(ref)
}

func (e *Engine) lookupLocked(ref string) (*Container, error) {
	if ref == "" {
		return nil, errdefs.Invalid("empty container reference")
	}
	if c, ok := e.containers[ref]; ok {
		return c, nil
	}
	if id, ok := e.names[strings.TrimPrefix(ref, "/")]; ok {
		return e.containers[id], nil
	}
	var found *Container
	for id, c := range e.containers {
		if strings.HasPrefix(id, ref) {
			if found != nil {
				return nil, errdefs.Invalid("multiple IDs found with provided prefix: %s", ref)
			}
			found = c
		}
	}
	if found == nil {
		return nil, errdefs.NotFound("No such container: %s", ref)
	}
	return found, nil
}

// Snapshot is a consistent copy of a container's record.
func (c *Container) Snapshot() Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rec
}

// Dir is the container's directory.
func (c *Container) Dir() string { return c.dir }

// Logger is the container's log.
func (c *Container) Logger() *logs.Logger { return c.logger }

// List returns container records, newest first.
func (e *Engine) List() []Record {
	e.mu.Lock()
	cs := make([]*Container, 0, len(e.containers))
	for _, c := range e.containers {
		cs = append(cs, c)
	}
	e.mu.Unlock()
	out := make([]Record, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Snapshot())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

// ImageUsage reports containers using an image, for image deletion.
func (e *Engine) ImageUsage(id oci.Digest) (running, stopped []string) {
	for _, r := range e.List() {
		if r.Image != id {
			continue
		}
		if r.State.Status == StatusRunning || r.State.Status == StatusStarting {
			running = append(running, r.ID[:12])
		} else {
			stopped = append(stopped, r.ID[:12])
		}
	}
	return running, stopped
}

// IsRunning reports whether the status counts as running.
func IsRunning(status string) bool { return status == StatusRunning || status == StatusStarting }

func ignoreNotExist(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
