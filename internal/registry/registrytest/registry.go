// Package registrytest is an in-process HTTPS registry for tests. It
// requires bearer tokens like Docker Hub and can be told to serve corrupted
// content.
package registrytest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Lord1Egypt/ThothDock/internal/oci"
)

// Registry is a fake registry.
type Registry struct {
	Server    *httptest.Server
	mu        sync.Mutex
	manifests map[string][]byte // repo + " " + selector
	types     map[string]string
	blobs     map[oci.Digest][]byte
	// Corrupt replaces the bytes served for a blob (digest unchanged).
	Corrupt map[oci.Digest][]byte
	// BlobRequests counts blob downloads.
	BlobRequests int
}

const token = "test-token"

// New starts a registry.
func New(t *testing.T) *Registry {
	r := &Registry{manifests: map[string][]byte{}, types: map[string]string{}, blobs: map[oci.Digest][]byte{}, Corrupt: map[oci.Digest][]byte{}}
	r.Server = httptest.NewTLSServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.Server.Close)
	return r
}

// Host is the registry's host:port.
func (r *Registry) Host() string { return strings.TrimPrefix(r.Server.URL, "https://") }

func (r *Registry) serve(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path == "/token" {
		json.NewEncoder(w).Encode(map[string]string{"token": token})
		return
	}
	if req.Header.Get("Authorization") != "Bearer "+token {
		w.Header().Set("WWW-Authenticate", `Bearer realm="`+r.Server.URL+`/token",service="test"`)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	p := strings.TrimPrefix(req.URL.Path, "/v2/")
	r.mu.Lock()
	defer r.mu.Unlock()
	if i := strings.LastIndex(p, "/manifests/"); i >= 0 {
		key := p[:i] + " " + p[i+len("/manifests/"):]
		data, ok := r.manifests[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"errors":[{"code":"MANIFEST_UNKNOWN","message":"manifest unknown"}]}`))
			return
		}
		w.Header().Set("Content-Type", r.types[key])
		w.Header().Set("Docker-Content-Digest", string(oci.FromBytes(data)))
		w.Write(data)
		return
	}
	if i := strings.LastIndex(p, "/blobs/"); i >= 0 {
		d := oci.Digest(p[i+len("/blobs/"):])
		data, ok := r.blobs[d]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"errors":[{"code":"BLOB_UNKNOWN","message":"blob unknown"}]}`))
			return
		}
		r.BlobRequests++
		if c, ok := r.Corrupt[d]; ok {
			data = c
		}
		w.Write(data)
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

// PutBlob stores a blob and returns its descriptor.
func (r *Registry) PutBlob(mediaType string, data []byte) oci.Descriptor {
	d := oci.FromBytes(data)
	r.mu.Lock()
	r.blobs[d] = data
	r.mu.Unlock()
	return oci.Descriptor{MediaType: mediaType, Digest: d, Size: int64(len(data))}
}

// PutManifest stores a manifest under each selector (tag), and by digest.
func (r *Registry) PutManifest(repo, mediaType string, data []byte, tags ...string) oci.Digest {
	d := oci.FromBytes(data)
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range append(tags, string(d)) {
		r.manifests[repo+" "+s] = data
		r.types[repo+" "+s] = mediaType
	}
	return d
}

// File is a layer entry.
type File struct {
	Name, Body, Link string
	Type             byte
	Mode             int64
}

// Layer builds a gzip layer and its diff ID.
func Layer(t *testing.T, files ...File) (gz []byte, diffID oci.Digest) {
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	for _, f := range files {
		typ := f.Type
		if typ == 0 {
			typ = tar.TypeReg
		}
		mode := f.Mode
		if mode == 0 {
			mode = 0o644
			if typ == tar.TypeDir {
				mode = 0o755
			}
		}
		h := &tar.Header{Name: f.Name, Typeflag: typ, Mode: mode, Linkname: f.Link}
		if typ == tar.TypeReg {
			h.Size = int64(len(f.Body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(f.Body))
	}
	tw.Close()
	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	zw.Write(raw.Bytes())
	zw.Close()
	return out.Bytes(), oci.FromBytes(raw.Bytes())
}

// Image pushes a single-platform image made of layers and returns the
// manifest digest. With index set, the manifest is wrapped in an OCI index
// listing it for platform plus a decoy amd64/arm entry.
func (r *Registry) Image(t *testing.T, repo, tag string, platform oci.Platform, cfg oci.ContainerConfig, index bool, layers ...[]File) oci.Digest {
	var descs []oci.Descriptor
	var diffIDs []oci.Digest
	for _, files := range layers {
		gz, diff := Layer(t, files...)
		descs = append(descs, r.PutBlob(oci.MediaTypeOCILayerGzip, gz))
		diffIDs = append(diffIDs, diff)
	}
	conf := oci.ImageConfig{Architecture: platform.Architecture, OS: platform.OS, Variant: platform.Variant, Config: cfg, RootFS: oci.RootFS{Type: "layers", DiffIDs: diffIDs}}
	cb, _ := json.Marshal(conf)
	cdesc := r.PutBlob(oci.MediaTypeOCIConfig, cb)
	m, _ := json.Marshal(oci.Manifest{SchemaVersion: 2, MediaType: oci.MediaTypeOCIManifest, Config: cdesc, Layers: descs})
	if !index {
		return r.PutManifest(repo, oci.MediaTypeOCIManifest, m, tag)
	}
	md := r.PutManifest(repo, oci.MediaTypeOCIManifest, m)
	decoy, _ := json.Marshal(oci.Manifest{SchemaVersion: 2, MediaType: oci.MediaTypeOCIManifest, Config: cdesc})
	dd := r.PutManifest(repo, oci.MediaTypeOCIManifest, decoy)
	other := oci.Platform{OS: "linux", Architecture: "s390x"}
	idx, _ := json.Marshal(oci.Index{SchemaVersion: 2, MediaType: oci.MediaTypeOCIIndex, Manifests: []oci.Descriptor{
		{MediaType: oci.MediaTypeOCIManifest, Digest: dd, Size: int64(len(decoy)), Platform: &other},
		{MediaType: oci.MediaTypeOCIManifest, Digest: md, Size: int64(len(m)), Platform: &platform},
	}})
	return r.PutManifest(repo, oci.MediaTypeOCIIndex, idx, tag)
}
