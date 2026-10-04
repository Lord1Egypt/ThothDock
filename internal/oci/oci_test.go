package oci

import "testing"

func TestParseDigest(t *testing.T) {
	good := "sha256:" + "a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4"
	if _, err := ParseDigest(good); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "sha256:", "sha256:XYZ", "md5:abcd", good[:70], "sha256:" + "A3ED95CAEB02FFE68CDD9FD84406680AE93D633CB16422D00E8A7C22955B46D4", good + "/../x"} {
		if _, err := ParseDigest(bad); err == nil {
			t.Errorf("ParseDigest(%q) accepted", bad)
		}
	}
}

func TestFromBytes(t *testing.T) {
	if FromBytes(nil) != "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatal("empty digest")
	}
}

func TestSelectManifest(t *testing.T) {
	d := func(arch, variant, ref string) Descriptor {
		desc := Descriptor{MediaType: MediaTypeOCIManifest, Digest: FromBytes([]byte(arch + variant + ref)), Platform: &Platform{OS: "linux", Architecture: arch, Variant: variant}}
		if ref != "" {
			desc.Platform = &Platform{OS: "unknown", Architecture: "unknown"}
			desc.Annotations = map[string]string{"vnd.docker.reference.type": "attestation-manifest"}
		}
		return desc
	}
	idx := Index{Manifests: []Descriptor{d("amd64", "", ""), d("", "", "att"), d("arm", "v6", ""), d("arm", "v7", ""), d("arm64", "v8", "")}}
	got, err := SelectManifest(idx, Platform{OS: "linux", Architecture: "arm64"})
	if err != nil || got.Platform.Architecture != "arm64" {
		t.Fatalf("arm64: %v %v", got, err)
	}
	got, err = SelectManifest(idx, Platform{OS: "linux", Architecture: "arm", Variant: "v7"})
	if err != nil || got.Platform.Variant != "v7" {
		t.Fatalf("arm/v7: %v %v", got, err)
	}
	if _, err := SelectManifest(idx, Platform{OS: "linux", Architecture: "riscv64"}); err == nil {
		t.Fatal("riscv64 must not match")
	}
	if _, err := SelectManifest(Index{Manifests: []Descriptor{d("", "", "att")}}, Platform{OS: "unknown", Architecture: "unknown"}); err == nil {
		t.Fatal("attestation manifests must never be selected")
	}
}

func TestParsePlatform(t *testing.T) {
	p, err := ParsePlatform("linux/aarch64/v8")
	if err != nil || p.Architecture != "arm64" || p.Variant != "" {
		t.Fatalf("%v %v", p, err)
	}
	if _, err := ParsePlatform("linux"); err == nil {
		t.Fatal("accepted linux")
	}
}

func TestLayerCompression(t *testing.T) {
	if c, err := LayerCompression(MediaTypeDockerLayerGzip); c != "gzip" || err != nil {
		t.Fatal(c, err)
	}
	if _, err := LayerCompression(MediaTypeOCILayerZstd); err == nil {
		t.Fatal("zstd must be refused explicitly")
	}
}
