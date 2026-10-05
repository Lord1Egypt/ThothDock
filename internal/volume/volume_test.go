package volume

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Lord1Egypt/ThothDock/internal/errdefs"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func open(t *testing.T) (*Store, string) {
	dir := filepath.Join(t.TempDir(), "volumes")
	s, err := Open(dir, quiet)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func TestHostileNamesRefused(t *testing.T) {
	s, dir := open(t)
	outside := filepath.Join(filepath.Dir(dir), "outside")
	os.Mkdir(outside, 0o700)
	bad := []string{"", "a", ".", "..", "../outside", "a/b", "/abs", "-x", ".hidden", "a b", "a\x00b", "a\nb", "tab\tx", "volume.json",
		strings.Repeat("a", MaxNameLen+1), "naïve", "x;rm", "x$(id)", "con:fig"}
	for _, n := range bad {
		if n == "" || n == "volume.json" {
			continue // "" is an anonymous volume; volume.json is a valid, harmless name (it is a directory)
		}
		if _, err := s.Create(n, "", nil, nil); errdefs.KindOf(err) != errdefs.KindInvalid {
			t.Errorf("name %q: %v", n, err)
		}
	}
	if es, _ := os.ReadDir(outside); len(es) != 0 {
		t.Fatalf("something was created outside the store: %v", es)
	}
	if es, _ := os.ReadDir(dir); len(es) != 0 {
		t.Fatalf("a refused name left entries: %v", es)
	}
	// Valid edge names are fine and stay inside the store.
	for _, n := range []string{"ab", "A1", "a.b-c_d", "volume.json", "a..b"} {
		if _, err := s.Create(n, "", nil, nil); err != nil {
			t.Errorf("valid name %q refused: %v", n, err)
		}
	}
}

func TestCreateIdempotentLabelsAndLayout(t *testing.T) {
	s, dir := open(t)
	v, err := s.Create("goldenvol", "local", map[string]string{"k": "v"}, nil)
	if err != nil || v.Driver != "local" || v.Labels["k"] != "v" {
		t.Fatalf("%+v %v", v, err)
	}
	fi, err := os.Lstat(s.DataPath("goldenvol"))
	if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("no data directory")
	}
	if fi, _ := os.Stat(filepath.Join(dir, "goldenvol")); fi.Mode().Perm() != 0o700 {
		t.Fatalf("volume directory mode %v", fi.Mode())
	}
	again, err := s.Create("goldenvol", "", map[string]string{"other": "x"}, nil)
	if err != nil || again.Labels["k"] != "v" || !again.CreatedAt.Equal(v.CreatedAt) {
		t.Fatalf("second create must return the existing volume: %+v %v", again, err)
	}
	if _, err := s.Create("x1", "nfs", nil, nil); errdefs.KindOf(err) != errdefs.KindInvalid {
		t.Fatal("unknown driver accepted")
	}
	if _, err := s.Create("x2", "local", nil, map[string]string{"type": "tmpfs"}); errdefs.KindOf(err) != errdefs.KindUnsupported {
		t.Fatal("driver options accepted")
	}
	anon, err := s.Create("", "", nil, nil)
	if err != nil || len(anon.Name) != 64 || !anon.Anonymous {
		t.Fatalf("%+v %v", anon, err)
	}
	// Mutating the returned copy never changes the store.
	v.Labels["k"] = "changed"
	if g, _ := s.Get("goldenvol"); g.Labels["k"] != "v" {
		t.Fatal("store aliased the caller's map")
	}
}

func TestRemoveInUsePersistenceAndPrune(t *testing.T) {
	s, dir := open(t)
	s.Create("keep", "", nil, nil)
	s.Create("drop", "", nil, nil)
	os.WriteFile(filepath.Join(s.DataPath("keep"), "f"), []byte("persistent"), 0o644)
	users := func(n string) []string {
		if n == "keep" {
			return []string{"c1"}
		}
		return nil
	}
	if err := s.Remove("keep", users); errdefs.KindOf(err) != errdefs.KindConflict || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("in-use volume removed: %v", err)
	}
	if err := s.Remove("nope", users); errdefs.KindOf(err) != errdefs.KindNotFound {
		t.Fatal(err)
	}
	gone, err := s.Prune(users, nil)
	if err != nil || len(gone) != 1 || gone[0] != "drop" {
		t.Fatalf("%v %v", gone, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "drop")); !os.IsNotExist(err) {
		t.Fatal("pruned volume left on disk")
	}
	// Restart: contents and metadata survive.
	s2, err := Open(dir, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(s2.DataPath("keep"), "f")); string(b) != "persistent" {
		t.Fatalf("data lost: %q", b)
	}
	if err := s2.Remove("keep", func(string) []string { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(s2.List()) != 0 {
		t.Fatal("still listed")
	}
}

func TestOpenCleansInterruptedAndForeignEntriesWithoutFollowingLinks(t *testing.T) {
	s, dir := open(t)
	s.Create("good", "", nil, nil)
	victim := filepath.Join(filepath.Dir(dir), "victim")
	os.Mkdir(victim, 0o700)
	os.WriteFile(filepath.Join(victim, "precious"), []byte("x"), 0o600)
	os.Mkdir(filepath.Join(dir, "halfmade"), 0o700) // no volume.json
	os.MkdirAll(filepath.Join(dir, "halfmade", "_data"), 0o755)
	os.Symlink(victim, filepath.Join(dir, "linked")) // a planted symlink with a valid name
	os.WriteFile(filepath.Join(dir, "stray"), []byte("file"), 0o600)
	s2, err := Open(dir, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if l := s2.List(); len(l) != 1 || l[0].Name != "good" {
		t.Fatalf("%+v", l)
	}
	for _, n := range []string{"halfmade", "linked", "stray"} {
		if _, err := os.Lstat(filepath.Join(dir, n)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s was not cleaned", n)
		}
	}
	if b, err := os.ReadFile(filepath.Join(victim, "precious")); err != nil || string(b) != "x" {
		t.Fatal("cleanup followed a symlink out of the store")
	}
}
