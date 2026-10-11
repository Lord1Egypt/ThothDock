package engine

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Lord1Egypt/ThothDock/internal/image"
	"github.com/Lord1Egypt/ThothDock/internal/platform"
	"github.com/Lord1Egypt/ThothDock/internal/runtime"
	"github.com/Lord1Egypt/ThothDock/internal/store"
)

// TestOpensARealV011DataRoot opens a data root written by ThothDock v0.1.1
// (built by tests/regression/upgrade-from-v0.1.1.sh; skipped without it) and
// checks that nothing a user had is lost or changed by the upgrade.
func TestOpensARealV011DataRoot(t *testing.T) {
	src := os.Getenv("THOTHDOCK_V011_ROOT")
	if src == "" {
		t.Skip("THOTHDOCK_V011_ROOT not set")
	}
	root := t.TempDir()
	if out, err := exec.Command("cp", "-a", src+"/.", root).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	l := platform.Layout{Root: root}
	if err := l.Ensure(); err != nil {
		t.Fatal(err)
	}
	is, err := image.Open(l.Images(), filepath.Join(l.Root, "refs.json"), l.Tmp(), store.NewBlobs(l.Blobs(), l.Tmp()), quiet)
	if err != nil {
		t.Fatal(err)
	}
	rt := runtime.NewFake()
	rt.Program("/bin/sh", func(ctx context.Context, s runtime.Spec, _ io.Reader) int { return 0 })
	e, err := New(l, is, &image.Puller{Store: is}, rt, Config{}, quiet)
	if err != nil {
		t.Fatalf("a v0.1.1 data root does not open: %v", err)
	}
	recs := map[string]Record{}
	for _, r := range e.List() {
		recs[r.Name] = r
	}
	if len(recs) != 2 || recs["legacy-a"].ID == "" || recs["legacy-b"].ID == "" {
		t.Fatalf("containers after upgrade: %v", recs)
	}
	if a := recs["legacy-a"]; a.State.Status != StatusCreated || a.HostConfig.RestartPolicy.Name != "" {
		t.Fatalf("legacy-a: %+v", a.State)
	}
	if b := recs["legacy-b"]; b.State.Status != StatusExited || b.State.ExitCode != 3 {
		t.Fatalf("legacy-b: %+v", b.State)
	}
	if is.Count() != 1 {
		t.Fatalf("images: %d", is.Count())
	}
	data, err := os.ReadFile(filepath.Join(l.Volumes(), "legvol", "_data", "legacy.txt"))
	if err != nil {
		data, err = os.ReadFile(filepath.Join(l.Volumes(), "legvol", "legacy.txt"))
	}
	if err != nil || string(data) != "kept" {
		t.Fatalf("named volume data: %q %v", data, err)
	}
	e.RestoreRestartPolicies() // no policies: nothing may start by itself
	if run, _, _ := e.Counts(); run != 0 {
		t.Fatalf("%d containers started on their own", run)
	}
	// And the new engine can run what the old one created.
	if err := e.Start(recs["legacy-a"].ID); err != nil {
		t.Fatalf("start after upgrade: %v", err)
	}
	waitFor(t, e, recs["legacy-a"].ID, "")
	if r, _ := e.Lookup(recs["legacy-a"].ID); r.Snapshot().State.Status != StatusExited {
		t.Fatalf("legacy-a did not run: %+v", r.Snapshot().State)
	}
}
