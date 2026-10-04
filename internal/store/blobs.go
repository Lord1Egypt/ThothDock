package store

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/Lord1Egypt/ThothDock/internal/oci"
)

// ErrDigestMismatch reports content whose digest differs from the expected one.
var ErrDigestMismatch = errors.New("digest mismatch")

// ErrSizeMismatch reports content whose size differs from the expected one.
var ErrSizeMismatch = errors.New("size mismatch")

// Blobs is a content-addressed store: a blob lives at <dir>/<hex> and is
// only ever placed there after its bytes were hashed and matched.
type Blobs struct {
	dir string
	tmp string
}

// NewBlobs uses dir for blobs and tmp (same filesystem) for staging.
func NewBlobs(dir, tmp string) *Blobs { return &Blobs{dir: dir, tmp: tmp} }

// Path is where blob d lives.
func (b *Blobs) Path(d oci.Digest) string { return filepath.Join(b.dir, d.Hex()) }

// Has reports whether d is stored with the expected size (size < 0 skips
// the size check). It does not re-hash; Verify does.
func (b *Blobs) Has(d oci.Digest, size int64) bool {
	fi, err := os.Lstat(b.Path(d))
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	return size < 0 || fi.Size() == size
}

// Ingest stores the content of r as blob want. It reads at most size bytes
// (size < 0: unbounded), and the blob becomes visible only when both the
// size and the sha256 match. Anything else leaves the store unchanged.
func (b *Blobs) Ingest(r io.Reader, want oci.Digest, size int64) error {
	f, err := os.CreateTemp(b.tmp, "blob-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		f.Close()
		os.Remove(tmp)
	}()
	dg := oci.NewDigester()
	src := r
	if size >= 0 {
		// One byte more than declared so an oversized stream is detected.
		src = io.LimitReader(r, size+1)
	}
	n, err := io.Copy(io.MultiWriter(f, dg), src)
	if err != nil {
		return err
	}
	if size >= 0 && n != size {
		return fmt.Errorf("%w: blob %s: got %d bytes, want %d", ErrSizeMismatch, want, n, size)
	}
	if got := dg.Digest(); got != want {
		return fmt.Errorf("%w: blob %s: content hashes to %s", ErrDigestMismatch, want, got)
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Chmod(0o400); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, b.Path(want)); err != nil {
		return err
	}
	return SyncDir(b.dir)
}

// Put stores data as a blob and returns its digest.
func (b *Blobs) Put(data []byte) (oci.Digest, error) {
	d := oci.FromBytes(data)
	if b.Has(d, int64(len(data))) {
		return d, nil
	}
	return d, b.Ingest(bytes.NewReader(data), d, int64(len(data)))
}

// Read returns a whole blob after verifying its digest.
func (b *Blobs) Read(d oci.Digest, max int64) ([]byte, error) {
	f, err := os.Open(b.Path(d))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("blob %s exceeds %d bytes", d, max)
	}
	if got := oci.FromBytes(data); got != d {
		return nil, fmt.Errorf("%w: stored blob %s hashes to %s", ErrDigestMismatch, d, got)
	}
	return data, nil
}

// Verify re-hashes a stored blob.
func (b *Blobs) Verify(d oci.Digest) error {
	f, err := os.Open(b.Path(d))
	if err != nil {
		return err
	}
	defer f.Close()
	dg := oci.NewDigester()
	if _, err := io.Copy(dg, f); err != nil {
		return err
	}
	if got := dg.Digest(); got != d {
		return fmt.Errorf("%w: stored blob %s hashes to %s", ErrDigestMismatch, d, got)
	}
	return nil
}

// Delete removes a blob; a missing blob is not an error.
func (b *Blobs) Delete(d oci.Digest) error {
	err := os.Remove(b.Path(d))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// List returns every stored blob digest. Names that are not a valid digest
// (stray files) are skipped.
func (b *Blobs) List() ([]oci.Digest, error) {
	entries, err := os.ReadDir(b.dir)
	if err != nil {
		return nil, err
	}
	var out []oci.Digest
	for _, e := range entries {
		if d, err := oci.ParseDigest("sha256:" + e.Name()); err == nil {
			out = append(out, d)
		}
	}
	return out, nil
}
