package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Lord1Egypt/ThothDock/internal/oci"
)

func TestWriteFileAtomicReplacesAndLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")
	for _, v := range []string{"one", "two"} {
		if err := WriteJSONAtomic(p, map[string]string{"v": v}); err != nil {
			t.Fatal(err)
		}
	}
	var got map[string]string
	if err := ReadJSON(p, &got); err != nil || got["v"] != "two" {
		t.Fatal(got, err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatal(fi.Mode())
	}
	if es, _ := os.ReadDir(dir); len(es) != 1 {
		t.Fatalf("temp files left: %v", es)
	}
}

func TestBlobIngestVerifies(t *testing.T) {
	dir := t.TempDir()
	b := NewBlobs(filepath.Join(dir, "blobs"), dir)
	os.Mkdir(filepath.Join(dir, "blobs"), 0o700)
	data := []byte("content")
	d := oci.FromBytes(data)
	if err := b.Ingest(bytes.NewReader([]byte("tampered")), d, int64(len("tampered"))); !errors.Is(err, ErrDigestMismatch) {
		t.Fatal(err)
	}
	if err := b.Ingest(bytes.NewReader(append(data, 'x')), d, int64(len(data))); !errors.Is(err, ErrSizeMismatch) {
		t.Fatal(err)
	}
	if err := b.Ingest(bytes.NewReader(data[:3]), d, int64(len(data))); !errors.Is(err, ErrSizeMismatch) {
		t.Fatal(err)
	}
	if b.Has(d, -1) {
		t.Fatal("rejected content became visible")
	}
	if err := b.Ingest(bytes.NewReader(data), d, int64(len(data))); err != nil {
		t.Fatal(err)
	}
	if got, err := b.Read(d, 100); err != nil || string(got) != "content" {
		t.Fatal(got, err)
	}
	// Corruption at rest is detected.
	os.Chmod(b.Path(d), 0o600)
	os.WriteFile(b.Path(d), []byte("rotted!"), 0o600)
	if err := b.Verify(d); !errors.Is(err, ErrDigestMismatch) {
		t.Fatal(err)
	}
	if _, err := b.Read(d, 100); !errors.Is(err, ErrDigestMismatch) {
		t.Fatal(err)
	}
	if es, _ := os.ReadDir(dir); len(es) != 1 {
		t.Fatalf("staging files left: %v", es)
	}
}
