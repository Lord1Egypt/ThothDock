package image_test

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Lord1Egypt/ThothDock/internal/errdefs"
	"github.com/Lord1Egypt/ThothDock/internal/image"
	"github.com/Lord1Egypt/ThothDock/internal/oci"
	"github.com/Lord1Egypt/ThothDock/internal/platform"
	"github.com/Lord1Egypt/ThothDock/internal/registry"
	"github.com/Lord1Egypt/ThothDock/internal/registry/registrytest"
	"github.com/Lord1Egypt/ThothDock/internal/store"
)

var arm64 = oci.Platform{OS: "linux", Architecture: "arm64"}

type env struct {
	t      *testing.T
	layout platform.Layout
	reg    *registrytest.Registry
	store  *image.Store
	puller *image.Puller
}

func newEnv(t *testing.T) *env {
	e := &env{t: t, layout: platform.Layout{Root: t.TempDir()}, reg: registrytest.New(t)}
	if err := e.layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	e.reopen()
	return e
}

func (e *env) reopen() {
	blobs := store.NewBlobs(e.layout.Blobs(), e.layout.Tmp())
	s, err := image.Open(e.layout.Images(), filepath.Join(e.layout.Root, "refs.json"), e.layout.Tmp(), blobs, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		e.t.Fatal(err)
	}
	e.store = s
	e.puller = &image.Puller{Store: s, Client: registry.NewClient(e.reg.Server.Client(), "test")}
}

func (e *env) pull(name string) (image.Summary, []image.Progress, error) {
	var ps []image.Progress
	sum, _, err := e.puller.Pull(context.Background(), e.reg.Host()+"/"+name, image.PullOptions{Platform: arm64, Progress: func(p image.Progress) { ps = append(ps, p) }})
	return sum, ps, err
}

var base = []registrytest.File{{Name: "bin/", Type: tar.TypeDir}, {Name: "bin/sh", Body: "#!shell", Mode: 0o755}, {Name: "etc/", Type: tar.TypeDir}, {Name: "etc/os-release", Body: "ID=tiny"}}

func TestPullIndexSelectsPlatformAndAssembles(t *testing.T) {
	e := newEnv(t)
	cfg := oci.ContainerConfig{Cmd: []string{"/bin/sh"}, Env: []string{"PATH=/bin"}}
	e.reg.Image(t, "library/tiny", "1", arm64, cfg, true, base, []registrytest.File{{Name: "etc/.wh.os-release"}, {Name: "etc/motd", Body: "hi"}})
	sum, _, err := e.pull("library/tiny:1")
	if err != nil {
		t.Fatal(err)
	}
	if sum.Platform.Architecture != "arm64" || sum.Config.Config.Cmd[0] != "/bin/sh" {
		t.Fatalf("%+v", sum.Image)
	}
	root := e.store.RootfsPath(sum.ID)
	if b, _ := os.ReadFile(filepath.Join(root, "etc/motd")); string(b) != "hi" {
		t.Fatalf("motd %q", b)
	}
	if _, err := os.Stat(filepath.Join(root, "etc/os-release")); !os.IsNotExist(err) {
		t.Fatal("whiteout not applied")
	}
	if len(sum.RepoTags) != 1 || !strings.HasSuffix(sum.RepoTags[0], "/library/tiny:1") || len(sum.RepoDigests) != 1 {
		t.Fatalf("refs %v %v", sum.RepoTags, sum.RepoDigests)
	}
	// Every stored blob verifies.
	ds, _ := e.store.Blobs.List()
	for _, d := range ds {
		if err := e.store.Blobs.Verify(d); err != nil {
			t.Fatal(err)
		}
	}
	// Lookups by tag, ID and ID prefix agree; restart keeps the image.
	e.reopen()
	for _, n := range []string{e.reg.Host() + "/library/tiny:1", string(sum.ID), sum.ID.Hex()[:12]} {
		got, err := e.store.Get(n)
		if err != nil || got.ID != sum.ID {
			t.Fatalf("Get(%q) = %v, %v", n, got.ID, err)
		}
	}
}

func TestPullRefusesCorruptLayer(t *testing.T) {
	e := newEnv(t)
	e.reg.Image(t, "library/bad", "1", arm64, oci.ContainerConfig{}, false, base)
	var m oci.Manifest
	mf, _ := registry.NewClient(e.reg.Server.Client(), "").GetManifest(context.Background(), mustRef(t, e.reg.Host()+"/library/bad:1"), "1", registry.Credentials{})
	json.Unmarshal(mf.Data, &m)
	e.reg.Corrupt[m.Layers[0].Digest] = []byte(strings.Repeat("x", int(m.Layers[0].Size)))
	_, _, err := e.pull("library/bad:1")
	if !errors.Is(err, store.ErrDigestMismatch) {
		t.Fatalf("want digest mismatch, got %v", err)
	}
	if e.store.Blobs.Has(m.Layers[0].Digest, -1) || e.store.Count() != 0 {
		t.Fatal("corrupt blob or image was stored")
	}
	// Wrong size is refused the same way.
	e.reg.Corrupt[m.Layers[0].Digest] = []byte("short")
	if _, _, err := e.pull("library/bad:1"); !errors.Is(err, store.ErrSizeMismatch) {
		t.Fatalf("want size mismatch, got %v", err)
	}
}

func TestPullRefusesWrongDiffID(t *testing.T) {
	e := newEnv(t)
	gz, _ := registrytest.Layer(t, base...)
	l := e.reg.PutBlob(oci.MediaTypeOCILayerGzip, gz)
	cfg, _ := json.Marshal(oci.ImageConfig{OS: "linux", Architecture: "arm64", RootFS: oci.RootFS{Type: "layers", DiffIDs: []oci.Digest{oci.FromBytes([]byte("other"))}}})
	c := e.reg.PutBlob(oci.MediaTypeOCIConfig, cfg)
	m, _ := json.Marshal(oci.Manifest{SchemaVersion: 2, MediaType: oci.MediaTypeOCIManifest, Config: c, Layers: []oci.Descriptor{l}})
	e.reg.PutManifest("library/diff", oci.MediaTypeOCIManifest, m, "1")
	if _, _, err := e.pull("library/diff:1"); !errors.Is(err, store.ErrDigestMismatch) {
		t.Fatalf("want diff ID mismatch, got %v", err)
	}
	assertNoImagesOrStaging(t, e)
}

func TestPullRefusesMaliciousLayer(t *testing.T) {
	e := newEnv(t)
	e.reg.Image(t, "library/evil", "1", arm64, oci.ContainerConfig{}, false, base, []registrytest.File{{Name: "../../escape", Body: "x"}})
	_, _, err := e.pull("library/evil:1")
	if err == nil || !strings.Contains(err.Error(), "unsafe layer entry") {
		t.Fatalf("want unsafe entry error, got %v", err)
	}
	assertNoImagesOrStaging(t, e)
	if _, err := os.Stat(filepath.Join(e.layout.Root, "..", "escape")); err == nil {
		t.Fatal("escape written")
	}
}

func TestPullWrongPlatformRefused(t *testing.T) {
	e := newEnv(t)
	e.reg.Image(t, "library/amd", "1", oci.Platform{OS: "linux", Architecture: "amd64"}, oci.ContainerConfig{}, true, base)
	_, _, err := e.pull("library/amd:1")
	if errdefs.KindOf(err) != errdefs.KindNotFound || !strings.Contains(err.Error(), "no matching manifest for linux/arm64") {
		t.Fatalf("got %v", err)
	}
}

func TestSharedLayersDownloadedOnce(t *testing.T) {
	e := newEnv(t)
	e.reg.Image(t, "library/a", "1", arm64, oci.ContainerConfig{}, false, base)
	e.reg.Image(t, "library/b", "1", arm64, oci.ContainerConfig{Cmd: []string{"b"}}, false, base, []registrytest.File{{Name: "b", Body: "b"}})
	if _, _, err := e.pull("library/a:1"); err != nil {
		t.Fatal(err)
	}
	before := e.reg.BlobRequests
	_, ps, err := e.pull("library/b:1")
	if err != nil {
		t.Fatal(err)
	}
	if e.reg.BlobRequests-before != 2 { // config + the new layer only
		t.Fatalf("downloaded %d blobs, want 2", e.reg.BlobRequests-before)
	}
	found := false
	for _, p := range ps {
		found = found || p.Status == "Already exists"
	}
	if !found {
		t.Fatal("no 'Already exists' progress")
	}
}

func TestDeleteUntagsThenRemoves(t *testing.T) {
	e := newEnv(t)
	e.reg.Image(t, "library/a", "1", arm64, oci.ContainerConfig{}, false, base)
	sum, _, err := e.pull("library/a:1")
	if err != nil {
		t.Fatal(err)
	}
	none := func(oci.Digest) ([]string, []string) { return nil, nil }
	stopped := func(oci.Digest) ([]string, []string) { return nil, []string{"c1"} }
	running := func(oci.Digest) ([]string, []string) { return []string{"c1"}, nil }
	// Two references (tag + digest): deleting the tag only untags.
	items, err := e.store.Delete(e.reg.Host()+"/library/a:1", false, none)
	if err != nil || len(items) != 1 || items[0].Untagged == "" {
		t.Fatalf("%v %v", items, err)
	}
	if _, err := e.store.Delete(string(sum.ID), false, stopped); errdefs.KindOf(err) != errdefs.KindConflict {
		t.Fatalf("stopped user must block: %v", err)
	}
	if _, err := e.store.Delete(string(sum.ID), true, running); errdefs.KindOf(err) != errdefs.KindConflict {
		t.Fatalf("running user must block even forced: %v", err)
	}
	items, err = e.store.Delete(string(sum.ID), true, stopped)
	if err != nil || items[len(items)-1].Deleted == "" && items[1].Deleted != string(sum.ID) {
		t.Fatalf("%v %v", items, err)
	}
	if ds, _ := e.store.Blobs.List(); len(ds) != 0 {
		t.Fatalf("blobs left: %v", ds)
	}
	assertNoImagesOrStaging(t, e)
}

func TestInterruptedPullRemovedOnOpen(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.layout.Images(), strings.Repeat("a", 64))
	os.MkdirAll(filepath.Join(dir, "rootfs", "etc"), 0o700)
	e.reopen()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("incomplete image directory kept")
	}
}

func assertNoImagesOrStaging(t *testing.T, e *env) {
	t.Helper()
	if n, _ := os.ReadDir(e.layout.Images()); len(n) != 0 {
		t.Fatalf("images dir not empty: %v", n)
	}
	if n, _ := os.ReadDir(e.layout.Tmp()); len(n) != 0 {
		t.Fatalf("tmp dir not empty: %v", n)
	}
}

func mustRef(t *testing.T, s string) registry.Reference {
	r, err := registry.ParseReference(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
