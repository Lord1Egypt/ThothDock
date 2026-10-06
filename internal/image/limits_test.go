package image_test

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Lord1Egypt/ThothDock/internal/image"
	"github.com/Lord1Egypt/ThothDock/internal/oci"
	"github.com/Lord1Egypt/ThothDock/internal/registry/registrytest"
)

const mib = 1 << 20

func zeros(name string, n int) registrytest.File {
	return registrytest.File{Name: name, Body: strings.Repeat("\x00", n)}
}

func (e *env) limited(l image.Limits) {
	e.puller.Limits = l
}

func TestLimitLayerCount(t *testing.T) {
	e := newEnv(t)
	var layers [][]registrytest.File
	for i := 0; i < 5; i++ {
		layers = append(layers, []registrytest.File{{Name: fmt.Sprintf("f%d", i), Body: "x"}})
	}
	e.reg.Image(t, "library/many", "1", arm64, oci.ContainerConfig{}, false, layers...)
	e.limited(image.Limits{MaxLayers: 4})
	_, _, err := e.pull("library/many:1")
	if err == nil || !strings.Contains(err.Error(), "5 layers; the limit is 4") {
		t.Fatalf("want a layer-count error, got %v", err)
	}
	assertNoImagesOrStaging(t, e)
	// Refused from the manifest: not one layer was downloaded.
	if l, _ := e.store.Blobs.List(); len(l) > 1 { // the manifest only
		t.Fatalf("%d blobs stored; layers were downloaded before the count was checked", len(l))
	}
	e.limited(image.Limits{MaxLayers: -1})
	if _, _, err := e.pull("library/many:1"); err != nil {
		t.Fatalf("a negative limit must disable it: %v", err)
	}
}

func TestLimitDecompressionBomb(t *testing.T) {
	e := newEnv(t)
	// 8 MiB of zeros gzips to a few KiB.
	e.reg.Image(t, "library/bomb", "1", arm64, oci.ContainerConfig{}, false, []registrytest.File{zeros("big", 8*mib)})
	e.limited(image.Limits{MaxLayerBytes: 1 * mib})
	_, _, err := e.pull("library/bomb:1")
	if err == nil || !strings.Contains(err.Error(), "uncompressed size limit") || !strings.Contains(err.Error(), "--max-layer-bytes") {
		t.Fatalf("want an uncompressed-size error naming the option, got %v", err)
	}
	assertNoImagesOrStaging(t, e)
}

func TestLimitExtractedBytesAcrossLayers(t *testing.T) {
	e := newEnv(t)
	e.reg.Image(t, "library/two", "1", arm64, oci.ContainerConfig{}, false,
		[]registrytest.File{zeros("a", 2*mib)}, []registrytest.File{zeros("b", 2*mib)})
	e.limited(image.Limits{MaxExtractedBytes: 3 * mib})
	_, _, err := e.pull("library/two:1")
	if err == nil || !strings.Contains(err.Error(), "extracted size limit") || !strings.Contains(err.Error(), "partial result was discarded") {
		t.Fatalf("want an extracted-size error, got %v", err)
	}
	assertNoImagesOrStaging(t, e)
	// The same image fits under a ceiling that covers both layers.
	e.limited(image.Limits{MaxExtractedBytes: 5 * mib})
	if _, _, err := e.pull("library/two:1"); err != nil {
		t.Fatalf("image under the ceiling was refused: %v", err)
	}
}

func TestLimitEntries(t *testing.T) {
	e := newEnv(t)
	var files []registrytest.File
	for i := 0; i < 50; i++ {
		files = append(files, registrytest.File{Name: fmt.Sprintf("d%d", i), Body: "x"})
	}
	e.reg.Image(t, "library/files", "1", arm64, oci.ContainerConfig{}, false, files)
	e.limited(image.Limits{MaxEntries: 10})
	if _, _, err := e.pull("library/files:1"); err == nil || !strings.Contains(err.Error(), "entry count limit") {
		t.Fatalf("want an entry-count error, got %v", err)
	}
	assertNoImagesOrStaging(t, e)
}

func TestLimitCompressedTotalFromManifest(t *testing.T) {
	e := newEnv(t)
	// Incompressible content, so the compressed size is about the real size.
	var b strings.Builder
	for i := 0; b.Len() < 1*mib; i++ {
		fmt.Fprintf(&b, "%x%x", i*2654435761, i*40503)
	}
	e.reg.Image(t, "library/large", "1", arm64, oci.ContainerConfig{}, false, []registrytest.File{{Name: "r", Body: b.String()}})
	e.limited(image.Limits{MaxCompressedBytes: 100 * 1024})
	if _, _, err := e.pull("library/large:1"); err == nil || !strings.Contains(err.Error(), "--max-compressed-bytes") {
		t.Fatalf("want a compressed-size error, got %v", err)
	}
	assertNoImagesOrStaging(t, e)
}

func TestFreeSpaceChecksBeforeDownload(t *testing.T) {
	e := newEnv(t)
	e.reg.Image(t, "library/tiny", "1", arm64, oci.ContainerConfig{}, false, base)
	defer image.SetFreeBytes(func(string) (uint64, bool) { return 100 << 20, true })()
	e.limited(image.Limits{MinFreeBytes: 1 << 30})
	_, _, err := e.pull("library/tiny:1")
	if !errors.Is(err, image.ErrNoSpace) {
		t.Fatalf("want ErrNoSpace, got %v", err)
	}
	assertNoImagesOrStaging(t, e)
}

// The disk fills while a layer is being extracted: the pull stops and the
// staging directory is removed.
func TestFreeSpaceChecksWhileExtracting(t *testing.T) {
	e := newEnv(t)
	e.reg.Image(t, "library/fill", "1", arm64, oci.ContainerConfig{}, false, []registrytest.File{zeros("big", 40*mib)})
	var calls atomic.Int32
	defer image.SetFreeBytes(func(string) (uint64, bool) {
		if calls.Add(1) <= 2 { // before the download, before assembling
			return 10 << 30, true
		}
		return 1 << 20, true
	})()
	e.limited(image.Limits{MinFreeBytes: 512 * mib})
	_, _, err := e.pull("library/fill:1")
	if !errors.Is(err, image.ErrNoSpace) {
		t.Fatalf("want ErrNoSpace while extracting, got %v", err)
	}
	if calls.Load() < 3 {
		t.Fatal("free space was never re-checked during extraction")
	}
	assertNoImagesOrStaging(t, e)
}

func TestFreeSpaceUnknownIsNotAnError(t *testing.T) {
	e := newEnv(t)
	e.reg.Image(t, "library/tiny", "1", arm64, oci.ContainerConfig{}, false, base)
	defer image.SetFreeBytes(func(string) (uint64, bool) { return 0, false })()
	if _, _, err := e.pull("library/tiny:1"); err != nil {
		t.Fatal(err)
	}
}
