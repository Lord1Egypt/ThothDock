// Package volume stores Docker-style named volumes below the data root:
//
//	volumes/<name>/volume.json   metadata (written last)
//	volumes/<name>/_data/        the volume's contents, bound into containers
//
// Names are validated with Docker's rule before they are ever used as a path
// component, so a name cannot traverse, hide a symlink or collide with
// ThothDock's own files.
package volume

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/Lord1Egypt/ThothDock/internal/errdefs"
	"github.com/Lord1Egypt/ThothDock/internal/securefs"
	"github.com/Lord1Egypt/ThothDock/internal/store"
)

var nameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]+$`)

// MaxNameLen keeps names within a file name's limit.
const MaxNameLen = 200

// ValidName applies Docker's rule: [a-zA-Z0-9][a-zA-Z0-9_.-]+ (two or more
// characters), and the file-name length limit.
func ValidName(name string) error {
	if len(name) > MaxNameLen || !nameRe.MatchString(name) {
		return errdefs.Invalid("%q includes invalid characters for a local volume name, only [a-zA-Z0-9][a-zA-Z0-9_.-] are allowed", name)
	}
	return nil
}

// Volume is a named volume.
type Volume struct {
	Name      string            `json:"name"`
	Driver    string            `json:"driver"`
	CreatedAt time.Time         `json:"createdAt"`
	Labels    map[string]string `json:"labels,omitempty"`
	Anonymous bool              `json:"anonymous,omitempty"`
}

// Store manages volumes.
type Store struct {
	dir string
	log *slog.Logger

	mu   sync.Mutex
	vols map[string]*Volume
}

// Open loads the store, removing volumes whose creation was interrupted.
func Open(dir string, log *slog.Logger) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, log: log, vols: map[string]*Volume{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		d := filepath.Join(dir, e.Name())
		var v Volume
		if ValidName(e.Name()) != nil || !e.IsDir() || store.ReadJSON(filepath.Join(d, "volume.json"), &v) != nil || v.Name != e.Name() {
			log.Warn("removing incomplete or foreign entry in the volume store", "entry", e.Name())
			if err := securefs.RemoveTree(d); err != nil {
				return nil, err
			}
			continue
		}
		s.vols[v.Name] = &v
	}
	return s, nil
}

// DataPath is the host directory holding the volume's contents.
func (s *Store) DataPath(name string) string { return filepath.Join(s.dir, name, "_data") }

func (s *Store) copyOf(v *Volume) Volume {
	c := *v
	if v.Labels != nil {
		c.Labels = map[string]string{}
		for k, x := range v.Labels {
			c.Labels[k] = x
		}
	}
	return c
}

// Create makes a volume, or returns the existing one of that name (as
// Docker does). An empty name creates an anonymous volume with a random one.
func (s *Store) Create(name, driver string, labels map[string]string, opts map[string]string) (Volume, error) {
	if driver != "" && driver != "local" {
		return Volume{}, errdefs.Invalid("volume driver %q is not available; only \"local\" exists", driver)
	}
	if len(opts) > 0 {
		return Volume{}, errdefs.Unsupported("ThothDock does not support volume driver options (bind-type, nfs or tmpfs local volumes): %v", opts)
	}
	anon := name == ""
	if anon {
		var b [32]byte
		if _, err := rand.Read(b[:]); err != nil {
			return Volume{}, err
		}
		name = hex.EncodeToString(b[:])
	}
	if err := ValidName(name); err != nil {
		return Volume{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.vols[name]; ok {
		return s.copyOf(v), nil
	}
	d := filepath.Join(s.dir, name)
	if err := os.Mkdir(d, 0o700); err != nil {
		return Volume{}, err
	}
	fail := func(err error) (Volume, error) {
		securefs.RemoveTree(d)
		return Volume{}, err
	}
	if err := os.Mkdir(filepath.Join(d, "_data"), 0o755); err != nil {
		return fail(err)
	}
	v := &Volume{Name: name, Driver: "local", CreatedAt: time.Now().UTC(), Labels: labels, Anonymous: anon}
	if err := store.WriteJSONAtomic(filepath.Join(d, "volume.json"), v); err != nil {
		return fail(err)
	}
	s.vols[name] = v
	return s.copyOf(v), nil
}

// Get returns a volume.
func (s *Store) Get(name string) (Volume, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.vols[name]
	if !ok {
		return Volume{}, errdefs.NotFound("get %s: no such volume", name)
	}
	return s.copyOf(v), nil
}

// Has reports whether the volume exists.
func (s *Store) Has(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.vols[name]
	return ok
}

// List returns volumes sorted by name.
func (s *Store) List() []Volume {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Volume, 0, len(s.vols))
	for _, v := range s.vols {
		out = append(out, s.copyOf(v))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Remove deletes a volume that no container uses. users returns the IDs of
// the containers that reference it.
func (s *Store) Remove(name string, users func(name string) []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.vols[name]; !ok {
		return errdefs.NotFound("get %s: no such volume", name)
	}
	if u := users(name); len(u) > 0 {
		return errdefs.Conflict("remove %s: volume is in use - %v", name, u)
	}
	return s.removeLocked(name)
}

func (s *Store) removeLocked(name string) error {
	d := filepath.Join(s.dir, name)
	// Metadata first: a crash leaves a directory without volume.json, which
	// Open removes, never a listed volume without its data.
	if err := os.Remove(filepath.Join(d, "volume.json")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	delete(s.vols, name)
	return securefs.RemoveTree(d)
}

// Prune removes every volume no container uses and returns their names.
func (s *Store) Prune(users func(name string) []string, match func(Volume) bool) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var gone []string
	names := make([]string, 0, len(s.vols))
	for n := range s.vols {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		v := s.vols[n]
		if len(users(n)) > 0 || (match != nil && !match(*v)) {
			continue
		}
		if err := s.removeLocked(n); err != nil {
			return gone, fmt.Errorf("pruning %s: %w", n, err)
		}
		gone = append(gone, n)
	}
	return gone, nil
}
