// Package image is ThothDock's image store: pulled images, their tags, and
// each image's assembled, immutable root filesystem.
//
// Layout below the data root:
//
//	blobs/sha256/<hex>          manifests, configs and layers (content-addressed)
//	images/<id-hex>/image.json  image metadata (written last)
//	images/<id-hex>/rootfs/     assembled root filesystem (read-only by policy)
//	refs.json                   tag and digest references -> image ID
//
// An image directory without image.json is an interrupted pull and is
// removed on load.
package image

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Lord1Egypt/ThothDock/internal/errdefs"
	"github.com/Lord1Egypt/ThothDock/internal/oci"
	"github.com/Lord1Egypt/ThothDock/internal/registry"
	"github.com/Lord1Egypt/ThothDock/internal/securefs"
	"github.com/Lord1Egypt/ThothDock/internal/store"
)

// Image is a pulled image.
type Image struct {
	ID             oci.Digest       `json:"id"` // config digest, as in Docker
	ManifestDigest oci.Digest       `json:"manifestDigest"`
	Config         oci.ImageConfig  `json:"config"`
	Layers         []oci.Descriptor `json:"layers"`
	Platform       oci.Platform     `json:"platform"`
	Size           int64            `json:"size"` // bytes in the assembled rootfs
	Pulled         time.Time        `json:"pulled"`
}

// Store holds images.
type Store struct {
	root  string // images/
	refs  string // refs.json
	tmp   string
	Blobs *store.Blobs

	mu     sync.Mutex
	images map[oci.Digest]*Image
	refMap map[string]oci.Digest // canonical "name:tag" / "name@digest" -> ID
	pullMu sync.Mutex
	log    *slog.Logger
}

// Open loads the store.
func Open(imagesDir, refsFile, tmpDir string, blobs *store.Blobs, log *slog.Logger) (*Store, error) {
	s := &Store{root: imagesDir, refs: refsFile, tmp: tmpDir, Blobs: blobs, images: map[oci.Digest]*Image{}, refMap: map[string]oci.Digest{}, log: log}
	entries, err := os.ReadDir(imagesDir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		dir := filepath.Join(imagesDir, e.Name())
		var img Image
		if err := store.ReadJSON(filepath.Join(dir, "image.json"), &img); err != nil {
			log.Warn("removing incomplete image directory", "dir", dir, "reason", err)
			if err := securefs.RemoveTree(dir); err != nil {
				return nil, err
			}
			continue
		}
		if img.ID.Hex() != e.Name() {
			return nil, fmt.Errorf("image directory %s holds image %s", dir, img.ID)
		}
		s.images[img.ID] = &img
	}
	if err := store.ReadJSON(refsFile, &s.refMap); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("reading %s: %w", refsFile, err)
	}
	if s.refMap == nil {
		s.refMap = map[string]oci.Digest{}
	}
	for r, id := range s.refMap {
		if s.images[id] == nil {
			delete(s.refMap, r)
		}
	}
	return s, nil
}

func (s *Store) saveRefsLocked() error { return store.WriteJSONAtomic(s.refs, s.refMap) }

// RootfsPath is the image's assembled root filesystem.
func (s *Store) RootfsPath(id oci.Digest) string { return filepath.Join(s.root, id.Hex(), "rootfs") }

// Summary is an image with its references.
type Summary struct {
	*Image
	RepoTags    []string // familiar, e.g. alpine:latest
	RepoDigests []string // familiar, e.g. alpine@sha256:...
}

func (s *Store) summaryLocked(img *Image) Summary {
	sum := Summary{Image: img}
	for r, id := range s.refMap {
		if id != img.ID {
			continue
		}
		ref, err := registry.ParseReference(r)
		if err != nil {
			continue
		}
		if ref.Digest != "" {
			sum.RepoDigests = append(sum.RepoDigests, ref.FamiliarName()+"@"+string(ref.Digest))
		} else {
			sum.RepoTags = append(sum.RepoTags, ref.FamiliarTagged())
		}
	}
	sort.Strings(sum.RepoTags)
	sort.Strings(sum.RepoDigests)
	return sum
}

// List returns every image, newest first.
func (s *Store) List() []Summary {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Summary, 0, len(s.images))
	for _, img := range s.images {
		out = append(out, s.summaryLocked(img))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pulled.After(out[j].Pulled) })
	return out
}

// Get resolves a reference ("alpine", "alpine:3", "alpine@sha256:..."), a
// full image ID, or a unique ID prefix.
func (s *Store) Get(name string) (Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	img, _, err := s.resolveLocked(name)
	if err != nil {
		return Summary{}, err
	}
	return s.summaryLocked(img), nil
}

// resolveLocked returns the image and, when name is a reference, its
// canonical form.
func (s *Store) resolveLocked(name string) (*Image, string, error) {
	if ref, err := registry.ParseReference(name); err == nil {
		key := ref.String()
		if ref.Digest != "" && ref.Tag != "" {
			key = ref.Name() + "@" + string(ref.Digest)
		}
		if id, ok := s.refMap[key]; ok {
			return s.images[id], key, nil
		}
	}
	h := strings.TrimPrefix(name, "sha256:")
	if len(h) > 0 && len(h) <= 64 && isHex(h) {
		var found *Image
		for id, img := range s.images {
			if strings.HasPrefix(id.Hex(), h) {
				if found != nil {
					return nil, "", errdefs.Invalid("multiple images found with provided prefix: %s", name)
				}
				found = img
			}
		}
		if found != nil {
			return found, "", nil
		}
	}
	return nil, "", errdefs.NotFound("No such image: %s", name)
}

func isHex(s string) bool {
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// DeleteItem is one line of an image delete response.
type DeleteItem struct {
	Untagged string `json:",omitempty"`
	Deleted  string `json:",omitempty"`
}

// Usage reports which containers use an image: running ones block deletion
// even when forced, stopped ones unless forced.
type Usage func(id oci.Digest) (running, stopped []string)

// Delete untags or removes an image with Docker's rules.
func (s *Store) Delete(name string, force bool, usage Usage) ([]DeleteItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	img, key, err := s.resolveLocked(name)
	if err != nil {
		return nil, err
	}
	var refs []string
	for r, id := range s.refMap {
		if id == img.ID {
			refs = append(refs, r)
		}
	}
	short := img.ID.Hex()[:12]
	if key != "" && len(refs) > 1 {
		delete(s.refMap, key)
		if err := s.saveRefsLocked(); err != nil {
			return nil, err
		}
		ref, _ := registry.ParseReference(key)
		return []DeleteItem{{Untagged: ref.FamiliarString()}}, nil
	}
	running, stopped := usage(img.ID)
	if len(running) > 0 {
		return nil, errdefs.Conflict("conflict: unable to delete %s (cannot be forced) - image is being used by running container %s", short, running[0])
	}
	if len(stopped) > 0 && !force {
		return nil, errdefs.Conflict("conflict: unable to delete %s (must be forced) - image is being used by stopped container %s", short, stopped[0])
	}
	var out []DeleteItem
	sort.Strings(refs)
	for _, r := range refs {
		ref, _ := registry.ParseReference(r)
		out = append(out, DeleteItem{Untagged: ref.FamiliarString()})
		delete(s.refMap, r)
	}
	if err := s.saveRefsLocked(); err != nil {
		return nil, err
	}
	// Metadata first: a crash leaves an incomplete directory, which Open
	// removes, never a listed image without a root filesystem.
	dir := filepath.Join(s.root, img.ID.Hex())
	if err := os.Remove(filepath.Join(dir, "image.json")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	delete(s.images, img.ID)
	if err := securefs.RemoveTree(dir); err != nil {
		return nil, err
	}
	out = append(out, DeleteItem{Deleted: string(img.ID)})
	for _, d := range s.unreferencedLocked(img) {
		if err := s.Blobs.Delete(d); err != nil {
			return nil, err
		}
		if d != img.ID {
			out = append(out, DeleteItem{Deleted: string(d)})
		}
	}
	return out, nil
}

// blobsOf lists every blob an image needs: config, manifest and layers.
func blobsOf(img *Image) []oci.Digest {
	ds := []oci.Digest{img.ID, img.ManifestDigest}
	for _, l := range img.Layers {
		ds = append(ds, l.Digest)
	}
	return ds
}

func (s *Store) unreferencedLocked(gone *Image) []oci.Digest {
	used := map[oci.Digest]bool{}
	for _, img := range s.images {
		for _, d := range blobsOf(img) {
			used[d] = true
		}
	}
	var out []oci.Digest
	for _, d := range blobsOf(gone) {
		if !used[d] {
			out = append(out, d)
			used[d] = true
		}
	}
	return out
}

// ReferencedBlobs is every blob any image needs, plus the index blobs its
// references name; for garbage collection.
func (s *Store) ReferencedBlobs() map[oci.Digest]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	used := map[oci.Digest]bool{}
	for _, img := range s.images {
		for _, d := range blobsOf(img) {
			used[d] = true
		}
	}
	for r := range s.refMap {
		if ref, err := registry.ParseReference(r); err == nil && ref.Digest != "" {
			used[ref.Digest] = true
		}
	}
	return used
}

// Count is the number of images.
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.images)
}

// Tag points the reference target (a tag, not a digest) at the image named
// by source.
func (s *Store) Tag(source, target string) error {
	ref, err := registry.ParseReference(target)
	if err != nil {
		return errdefs.Invalid("%v", err)
	}
	if ref.Digest != "" {
		return errdefs.Invalid("refusing to create a tag with a digest reference")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	img, _, err := s.resolveLocked(source)
	if err != nil {
		return err
	}
	s.refMap[ref.String()] = img.ID
	return s.saveRefsLocked()
}
