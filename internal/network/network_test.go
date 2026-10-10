package network

import (
	"path/filepath"
	"testing"

	"github.com/Lord1Egypt/ThothDock/internal/errdefs"
)

func TestCreateLookupListRemovePersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "networks.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.Create(CreateRequest{Name: "demo_default", Labels: map[string]string{"com.docker.compose.project": "demo"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(n.ID) != 64 || n.Driver != "bridge" {
		t.Fatalf("%+v", n)
	}
	for _, ref := range []string{"demo_default", n.ID, n.ID[:6]} {
		if got, err := s.Lookup(ref); err != nil || got.ID != n.ID {
			t.Fatalf("lookup %q: %v", ref, err)
		}
	}
	if _, err := s.Create(CreateRequest{Name: "demo_default"}); errdefs.KindOf(err) != errdefs.KindConflict {
		t.Fatalf("duplicate: %v", err)
	}
	for _, bad := range []string{"bridge", "host", "none"} {
		if _, err := s.Create(CreateRequest{Name: bad}); errdefs.KindOf(err) != errdefs.KindForbidden {
			t.Fatalf("builtin %s: %v", bad, err)
		}
	}
	for _, bad := range []string{"", "-x", "a b", "../x", "x/y"} {
		if _, err := s.Create(CreateRequest{Name: bad}); errdefs.KindOf(err) != errdefs.KindInvalid {
			t.Fatalf("name %q: %v", bad, err)
		}
	}
	if _, err := s.Create(CreateRequest{Name: "ov", Driver: "overlay"}); errdefs.KindOf(err) != errdefs.KindUnsupported {
		t.Fatalf("overlay: %v", err)
	}
	if l := s.List(); len(l) != 4 || l[0].Name != "bridge" || l[3].Name != "demo_default" {
		t.Fatalf("list %+v", l)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := again.Lookup("demo_default"); err != nil || got.Labels["com.docker.compose.project"] != "demo" {
		t.Fatalf("not persisted: %+v %v", got, err)
	}
	if err := again.Remove(n.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := again.Lookup("demo_default"); errdefs.KindOf(err) != errdefs.KindNotFound {
		t.Fatalf("after remove: %v", err)
	}
	if b, err := again.Lookup("bridge"); err != nil || !b.Builtin {
		t.Fatalf("builtin lookup: %+v %v", b, err)
	}
}

func TestPool(t *testing.T) {
	p := NewPool()
	a, _ := p.Allocate("c1")
	b, _ := p.Allocate("c2")
	if a != "127.77.0.2" || b != "127.77.0.3" {
		t.Fatalf("%s %s", a, b)
	}
	p.Release(a)
	if c, _ := p.Allocate("c3"); c != "127.77.0.2" {
		t.Fatalf("lowest free not reused: %s", c)
	}
	if err := p.Reserve("127.77.0.3", "other"); errdefs.KindOf(err) != errdefs.KindConflict {
		t.Fatalf("double reserve: %v", err)
	}
	if err := p.Reserve("127.77.0.3", "c2"); err != nil {
		t.Fatalf("idempotent reserve: %v", err)
	}
	for _, bad := range []string{"127.77.0.1", "127.77.255.255", "10.0.0.1", "x"} {
		if err := p.Reserve(bad, "z"); err == nil {
			t.Fatalf("reserved %s", bad)
		}
	}
}

func TestShiftAndModes(t *testing.T) {
	for in, want := range map[int]int{0: 0, 1: 30001, 80: 30080, 1023: 31023, 1024: 1024, 8080: 8080} {
		if got := Shift(in); got != want {
			t.Fatalf("Shift(%d) = %d", in, got)
		}
	}
	for mode, want := range map[string]bool{"": true, "default": true, "bridge": true, "host": true, "none": false, "demo_default": false, "container:x": false} {
		if IsDeviceMode(mode) != want {
			t.Fatalf("IsDeviceMode(%q)", mode)
		}
	}
}
