package registry_test

import (
	"context"
	"io"
	"testing"

	"github.com/Lord1Egypt/ThothDock/internal/oci"
	"github.com/Lord1Egypt/ThothDock/internal/registry"
	"github.com/Lord1Egypt/ThothDock/internal/registry/registrytest"
)

func TestParseReference(t *testing.T) {
	d := "sha256:" + "a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4"
	cases := map[string]string{
		"alpine":                        "docker.io/library/alpine:latest",
		"alpine:3.20":                   "docker.io/library/alpine:3.20",
		"library/alpine":                "docker.io/library/alpine:latest",
		"docker.io/library/alpine":      "docker.io/library/alpine:latest",
		"index.docker.io/user/app:v1":   "docker.io/user/app:v1",
		"ghcr.io/owner/img:tag":         "ghcr.io/owner/img:tag",
		"localhost:5000/foo":            "localhost:5000/foo:latest",
		"localhost/foo/bar:1":           "localhost/foo/bar:1",
		"alpine@" + d:                   "docker.io/library/alpine@" + d,
		"alpine:3@" + d:                 "docker.io/library/alpine:3@" + d,
		"my-registry.example:443/a/b/c": "my-registry.example:443/a/b/c:latest",
	}
	for in, want := range cases {
		r, err := registry.ParseReference(in)
		if err != nil || r.String() != want {
			t.Errorf("%q -> %q, %v; want %q", in, r.String(), err, want)
		}
	}
	for _, bad := range []string{"", "Alpine", "alpine:", "a b", "alpine@sha256:zz", "-x", "foo/../bar", "alpine:" + string(make([]byte, 200))} {
		if _, err := registry.ParseReference(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	r, _ := registry.ParseReference("alpine")
	if r.FamiliarTagged() != "alpine:latest" || r.Host() != registry.DockerHubHost {
		t.Fatal(r.FamiliarTagged(), r.Host())
	}
}

func TestClientBearerAuthAndDigests(t *testing.T) {
	reg := registrytest.New(t)
	plat := oci.Platform{OS: "linux", Architecture: "arm64"}
	idxDigest := reg.Image(t, "library/tiny", "1", plat, oci.ContainerConfig{}, true, []registrytest.File{{Name: "hello", Body: "hi"}})
	c := registry.NewClient(reg.Server.Client(), "test")
	ref, err := registry.ParseReference(reg.Host() + "/library/tiny:1")
	if err != nil {
		t.Fatal(err)
	}
	m, err := c.GetManifest(context.Background(), ref, "1", registry.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	if m.Digest != idxDigest || !oci.IsIndex(m.MediaType) {
		t.Fatalf("got %s %s", m.Digest, m.MediaType)
	}
	if _, err := c.GetManifest(context.Background(), ref, "nope", registry.Credentials{}); !registry.IsNotFound(err) {
		t.Fatalf("want not found, got %v", err)
	}
	// Asking for a digest the content does not hash to must fail.
	bad := "sha256:" + "0000000000000000000000000000000000000000000000000000000000000000"
	if _, err := c.GetManifest(context.Background(), ref, bad, registry.Credentials{}); err == nil {
		t.Fatal("unknown digest accepted")
	}
	desc := reg.PutBlob("x", []byte("blob"))
	rc, err := c.OpenBlob(context.Background(), ref, desc.Digest, registry.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "blob" {
		t.Fatalf("blob %q", b)
	}
}
