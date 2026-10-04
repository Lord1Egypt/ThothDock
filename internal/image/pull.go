package image

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Lord1Egypt/ThothDock/internal/errdefs"
	"github.com/Lord1Egypt/ThothDock/internal/layer"
	"github.com/Lord1Egypt/ThothDock/internal/oci"
	"github.com/Lord1Egypt/ThothDock/internal/registry"
	"github.com/Lord1Egypt/ThothDock/internal/securefs"
	"github.com/Lord1Egypt/ThothDock/internal/store"
)

const maxConfigSize = 8 << 20

// Progress is one Docker JSON progress message.
type Progress struct {
	ID      string
	Status  string
	Current int64
	Total   int64
}

// PullOptions configure a pull.
type PullOptions struct {
	Platform oci.Platform
	Creds    registry.Credentials
	Progress func(Progress)
}

// Puller fetches images from registries into a Store.
type Puller struct {
	Store  *Store
	Client *registry.Client
}

func short(d oci.Digest) string { return d.Hex()[:12] }

// Pull fetches name for opts.Platform. Every blob is size- and
// digest-checked before it enters the blob store; every layer is checked
// against the config's diff IDs while it is applied to a private staging
// directory; the image becomes visible only after all of that succeeded.
func (p *Puller) Pull(ctx context.Context, name string, opts PullOptions) (Summary, string, error) {
	ref, err := registry.ParseReference(name)
	if err != nil {
		return Summary{}, "", errdefs.Invalid("%v", err)
	}
	progress := opts.Progress
	if progress == nil {
		progress = func(Progress) {}
	}
	s := p.Store
	s.pullMu.Lock()
	defer s.pullMu.Unlock()

	top, err := p.Client.GetManifest(ctx, ref, ref.Selector(), opts.Creds)
	if err != nil {
		if registry.IsNotFound(err) {
			return Summary{}, "", errdefs.NotFound("pull access denied for %s, repository does not exist or may require authentication: %v", ref.FamiliarName(), err)
		}
		return Summary{}, "", err
	}
	progress(Progress{ID: ref.Selector(), Status: "Pulling from " + familiarRepo(ref)})
	if _, err := s.Blobs.Put(top.Data); err != nil {
		return Summary{}, "", err
	}
	manifest := top
	if oci.IsIndex(top.MediaType) {
		var idx oci.Index
		if err := json.Unmarshal(top.Data, &idx); err != nil {
			return Summary{}, "", fmt.Errorf("decoding index: %w", err)
		}
		desc, err := oci.SelectManifest(idx, opts.Platform)
		if err != nil {
			return Summary{}, "", errdefs.NotFound("%v", err)
		}
		if _, err := oci.ParseDigest(string(desc.Digest)); err != nil {
			return Summary{}, "", err
		}
		manifest, err = p.Client.GetManifest(ctx, ref, string(desc.Digest), opts.Creds)
		if err != nil {
			return Summary{}, "", err
		}
		if int64(len(manifest.Data)) != desc.Size {
			return Summary{}, "", fmt.Errorf("manifest %s: size %d, index says %d", desc.Digest, len(manifest.Data), desc.Size)
		}
		if !oci.IsManifest(manifest.MediaType) {
			return Summary{}, "", fmt.Errorf("index entry %s is not an image manifest", desc.Digest)
		}
		if _, err := s.Blobs.Put(manifest.Data); err != nil {
			return Summary{}, "", err
		}
	}
	var m oci.Manifest
	if err := json.Unmarshal(manifest.Data, &m); err != nil {
		return Summary{}, "", fmt.Errorf("decoding manifest: %w", err)
	}
	if m.SchemaVersion != 2 {
		return Summary{}, "", fmt.Errorf("unsupported manifest schema version %d", m.SchemaVersion)
	}
	if _, err := oci.ParseDigest(string(m.Config.Digest)); err != nil {
		return Summary{}, "", fmt.Errorf("config: %w", err)
	}
	for _, l := range m.Layers {
		if _, err := oci.ParseDigest(string(l.Digest)); err != nil {
			return Summary{}, "", fmt.Errorf("layer: %w", err)
		}
		if _, err := oci.LayerCompression(l.MediaType); err != nil {
			return Summary{}, "", errdefs.Unsupported("ThothDock cannot apply layer %s: %v", short(l.Digest), err)
		}
	}

	// Config.
	if m.Config.Size <= 0 || m.Config.Size > maxConfigSize {
		return Summary{}, "", fmt.Errorf("config size %d out of range", m.Config.Size)
	}
	if err := p.fetch(ctx, ref, m.Config, opts.Creds, nil); err != nil {
		return Summary{}, "", err
	}
	cfgBytes, err := s.Blobs.Read(m.Config.Digest, maxConfigSize)
	if err != nil {
		return Summary{}, "", err
	}
	var cfg oci.ImageConfig
	if err := json.Unmarshal(cfgBytes, &cfg); err != nil {
		return Summary{}, "", fmt.Errorf("decoding image config: %w", err)
	}
	plat := oci.Normalize(oci.Platform{OS: cfg.OS, Architecture: cfg.Architecture, Variant: cfg.Variant})
	if !oci.Matches(opts.Platform, plat) {
		return Summary{}, "", errdefs.Invalid("image %s is %s, not %s", ref.FamiliarString(), plat, opts.Platform)
	}
	if len(cfg.RootFS.DiffIDs) != len(m.Layers) {
		return Summary{}, "", fmt.Errorf("manifest has %d layers but config lists %d diff IDs", len(m.Layers), len(cfg.RootFS.DiffIDs))
	}
	for _, d := range cfg.RootFS.DiffIDs {
		if _, err := oci.ParseDigest(string(d)); err != nil {
			return Summary{}, "", fmt.Errorf("diff ID: %w", err)
		}
	}
	id := m.Config.Digest

	s.mu.Lock()
	_, exists := s.images[id]
	s.mu.Unlock()
	status := "Image is up to date for " + ref.FamiliarString()
	if !exists {
		status = "Downloaded newer image for " + ref.FamiliarString()
		for _, l := range m.Layers {
			sid := short(l.Digest)
			if s.Blobs.Has(l.Digest, l.Size) {
				progress(Progress{ID: sid, Status: "Already exists"})
				continue
			}
			progress(Progress{ID: sid, Status: "Pulling fs layer"})
		}
		for _, l := range m.Layers {
			if s.Blobs.Has(l.Digest, l.Size) {
				continue
			}
			sid := short(l.Digest)
			if err := p.fetch(ctx, ref, l, opts.Creds, func(cur int64) {
				progress(Progress{ID: sid, Status: "Downloading", Current: cur, Total: l.Size})
			}); err != nil {
				return Summary{}, "", err
			}
			progress(Progress{ID: sid, Status: "Verifying Checksum"})
			progress(Progress{ID: sid, Status: "Download complete"})
		}
		size, err := p.assemble(ctx, id, m.Layers, cfg.RootFS.DiffIDs, progress)
		if err != nil {
			return Summary{}, "", err
		}
		img := &Image{ID: id, ManifestDigest: manifest.Digest, Config: cfg, Layers: m.Layers, Platform: plat, Size: size, Pulled: time.Now().UTC()}
		if err := store.WriteJSONAtomic(filepath.Join(s.root, id.Hex(), "image.json"), img); err != nil {
			return Summary{}, "", err
		}
		s.mu.Lock()
		s.images[id] = img
		s.mu.Unlock()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if ref.Tag != "" {
		tagged := registry.Reference{Domain: ref.Domain, Path: ref.Path, Tag: ref.Tag}
		if prev, ok := s.refMap[tagged.String()]; ok && prev != id {
			status = "Downloaded newer image for " + ref.FamiliarString()
		}
		s.refMap[tagged.String()] = id
	}
	s.refMap[ref.Name()+"@"+string(top.Digest)] = id
	if err := s.saveRefsLocked(); err != nil {
		return Summary{}, "", err
	}
	progress(Progress{Status: "Digest: " + string(top.Digest)})
	return s.summaryLocked(s.images[id]), status, nil
}

func familiarRepo(ref registry.Reference) string {
	if ref.Domain == registry.DefaultDomain {
		return ref.Path
	}
	return ref.Name()
}

type countingReader struct {
	r    io.Reader
	n    int64
	last time.Time
	cb   func(int64)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if c.cb != nil && (time.Since(c.last) > 250*time.Millisecond || err == io.EOF) {
		c.last = time.Now()
		c.cb(c.n)
	}
	return n, err
}

func (p *Puller) fetch(ctx context.Context, ref registry.Reference, d oci.Descriptor, creds registry.Credentials, cb func(int64)) error {
	if p.Store.Blobs.Has(d.Digest, d.Size) {
		return nil
	}
	if d.Size < 0 {
		return fmt.Errorf("blob %s has negative size", d.Digest)
	}
	rc, err := p.Client.OpenBlob(ctx, ref, d.Digest, creds)
	if err != nil {
		return err
	}
	defer rc.Close()
	return p.Store.Blobs.Ingest(&countingReader{r: rc, cb: cb}, d.Digest, d.Size)
}

// assemble applies the layers in order to a staging directory and renames
// it into place. A digest or diff ID mismatch, an unsafe entry or any error
// discards the staging directory.
func (p *Puller) assemble(ctx context.Context, id oci.Digest, layers []oci.Descriptor, diffIDs []oci.Digest, progress func(Progress)) (int64, error) {
	s := p.Store
	staging, err := os.MkdirTemp(s.tmp, "rootfs-")
	if err != nil {
		return 0, err
	}
	ok := false
	defer func() {
		if !ok {
			securefs.RemoveTree(staging)
		}
	}()
	rootfs := filepath.Join(staging, "rootfs")
	if err := os.Mkdir(rootfs, 0o755); err != nil {
		return 0, err
	}
	var size int64
	for i, l := range layers {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		sid := short(l.Digest)
		progress(Progress{ID: sid, Status: "Extracting"})
		st, err := applyVerified(s.Blobs, l, diffIDs[i], rootfs)
		if err != nil {
			return 0, fmt.Errorf("layer %s: %w", sid, err)
		}
		size += st.Bytes
		progress(Progress{ID: sid, Status: "Pull complete"})
	}
	dir := filepath.Join(s.root, id.Hex())
	if err := os.Mkdir(dir, 0o700); err != nil {
		return 0, err
	}
	if err := os.Rename(rootfs, filepath.Join(dir, "rootfs")); err != nil {
		os.Remove(dir)
		return 0, err
	}
	ok = true
	securefs.RemoveTree(staging)
	return size, store.SyncDir(dir)
}

// applyVerified re-hashes the stored blob, then applies it while hashing
// both the compressed bytes (they must still match) and the uncompressed
// tar (it must match the diff ID).
func applyVerified(blobs *store.Blobs, l oci.Descriptor, diffID oci.Digest, rootfs string) (layer.Stats, error) {
	if err := blobs.Verify(l.Digest); err != nil {
		return layer.Stats{}, err
	}
	comp, _ := oci.LayerCompression(l.MediaType)
	f, err := os.Open(blobs.Path(l.Digest))
	if err != nil {
		return layer.Stats{}, err
	}
	defer f.Close()
	blobDigest := oci.NewDigester()
	teed := io.TeeReader(bufio.NewReader(f), blobDigest)
	tarStream := teed
	if comp == "gzip" {
		zr, err := gzip.NewReader(tarStream)
		if err != nil {
			return layer.Stats{}, err
		}
		defer zr.Close()
		tarStream = zr
	}
	diff := oci.NewDigester()
	st, err := layer.Apply(rootfs, io.TeeReader(tarStream, diff))
	if err != nil {
		return st, err
	}
	// Consume anything after the gzip stream so the blob hash is complete.
	if _, err := io.Copy(io.Discard, teed); err != nil {
		return st, err
	}
	if got := blobDigest.Digest(); got != l.Digest {
		return st, fmt.Errorf("%w: blob changed while applying (%s)", store.ErrDigestMismatch, got)
	}
	if got := diff.Digest(); got != diffID {
		return st, fmt.Errorf("%w: uncompressed layer hashes to %s, config says %s", store.ErrDigestMismatch, got, diffID)
	}
	return st, nil
}
