package oci

import (
	"fmt"
	"runtime"
	"strings"
)

// HostPlatform is the platform ThothDock runs containers for: the daemon's
// own (PRoot executes natively; there is no emulation).
func HostPlatform() Platform {
	p := Platform{OS: "linux", Architecture: runtime.GOARCH}
	if p.Architecture == "arm" {
		p.Variant = "v7"
	}
	return Normalize(p)
}

// ParsePlatform reads "os/arch[/variant]".
func ParsePlatform(s string) (Platform, error) {
	parts := strings.Split(strings.ToLower(s), "/")
	if len(parts) < 2 || len(parts) > 3 || parts[0] == "" || parts[1] == "" {
		return Platform{}, fmt.Errorf("invalid platform %q: want os/arch[/variant]", s)
	}
	p := Platform{OS: parts[0], Architecture: parts[1]}
	if len(parts) == 3 {
		p.Variant = parts[2]
	}
	return Normalize(p), nil
}

// Normalize applies the containerd conventions: aarch64 is arm64, arm64's
// v8 variant is the default and dropped, x86_64 is amd64.
func Normalize(p Platform) Platform {
	switch p.Architecture {
	case "aarch64":
		p.Architecture = "arm64"
	case "x86_64", "x86-64":
		p.Architecture = "amd64"
	case "armhf":
		p.Architecture, p.Variant = "arm", "v7"
	}
	if p.Architecture == "arm64" && p.Variant == "v8" {
		p.Variant = ""
	}
	if p.Architecture == "amd64" && p.Variant == "v1" {
		p.Variant = ""
	}
	return p
}

// Matches reports whether candidate can run on want.
func Matches(want, candidate Platform) bool {
	w, c := Normalize(want), Normalize(candidate)
	if w.OS != c.OS || w.Architecture != c.Architecture {
		return false
	}
	return w.Variant == "" || c.Variant == "" || w.Variant == c.Variant
}

// SelectManifest picks the manifest for want from an index. Attestation
// manifests (BuildKit's "unknown/unknown" provenance entries) never match.
func SelectManifest(idx Index, want Platform) (Descriptor, error) {
	var avail []string
	for _, d := range idx.Manifests {
		if d.Platform == nil || !IsManifest(d.MediaType) {
			continue
		}
		if d.Annotations["vnd.docker.reference.type"] == "attestation-manifest" {
			continue
		}
		avail = append(avail, d.Platform.String())
		if Matches(want, *d.Platform) {
			return d, nil
		}
	}
	return Descriptor{}, fmt.Errorf("no matching manifest for %s in the manifest list entries (available: %s)",
		want, strings.Join(avail, ", "))
}
