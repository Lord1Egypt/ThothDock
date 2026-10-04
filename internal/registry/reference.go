// Package registry parses image references and talks to OCI distribution
// (Docker Registry HTTP API v2) registries over HTTPS.
package registry

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Lord1Egypt/ThothDock/internal/oci"
)

const (
	// DefaultDomain is the canonical name of Docker Hub in references.
	DefaultDomain = "docker.io"
	// DockerHubHost is where Docker Hub's registry API is served.
	DockerHubHost = "registry-1.docker.io"
	DefaultTag    = "latest"
	maxNameLen    = 255
)

var (
	pathComponent = regexp.MustCompile(`^[a-z0-9]+(?:(?:\.|_|__|-+)[a-z0-9]+)*$`)
	domainRe      = regexp.MustCompile(`^(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?)*|\[[0-9a-fA-F:]+\])(?::[0-9]{1,5})?$`)
	tagRe         = regexp.MustCompile(`^[\w][\w.-]{0,127}$`)
)

// Reference is a normalized image reference.
type Reference struct {
	Domain string     // e.g. docker.io, ghcr.io, localhost:5000
	Path   string     // e.g. library/alpine
	Tag    string     // empty when only a digest was given
	Digest oci.Digest // empty unless pinned
}

// ParseReference normalizes s the way the Docker CLI does: "alpine" is
// docker.io/library/alpine:latest.
func ParseReference(s string) (Reference, error) {
	if s == "" || strings.ContainsAny(s, " \t\r\n") {
		return Reference{}, fmt.Errorf("invalid reference format: %q", s)
	}
	var ref Reference
	rest := s
	if i := strings.IndexByte(rest, '@'); i >= 0 {
		d, err := oci.ParseDigest(rest[i+1:])
		if err != nil {
			return Reference{}, fmt.Errorf("invalid reference format: %v", err)
		}
		ref.Digest = d
		rest = rest[:i]
	}
	if i := strings.LastIndexByte(rest, ':'); i >= 0 && !strings.Contains(rest[i+1:], "/") {
		ref.Tag = rest[i+1:]
		rest = rest[:i]
		if !tagRe.MatchString(ref.Tag) {
			return Reference{}, fmt.Errorf("invalid reference format: bad tag %q", ref.Tag)
		}
	}
	domain, path := DefaultDomain, rest
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		first := rest[:i]
		if strings.ContainsAny(first, ".:") || first == "localhost" || strings.ToLower(first) != first {
			domain, path = first, rest[i+1:]
		}
	}
	if domain == "index.docker.io" {
		domain = DefaultDomain
	}
	if !domainRe.MatchString(domain) {
		return Reference{}, fmt.Errorf("invalid reference format: bad registry %q", domain)
	}
	if domain == DefaultDomain && !strings.Contains(path, "/") {
		path = "library/" + path
	}
	if path == "" || len(domain)+1+len(path) > maxNameLen {
		return Reference{}, fmt.Errorf("invalid reference format: %q", s)
	}
	for _, c := range strings.Split(path, "/") {
		if !pathComponent.MatchString(c) {
			if strings.ToLower(c) != c {
				return Reference{}, fmt.Errorf("invalid reference format: repository name (%s) must be lowercase", path)
			}
			return Reference{}, fmt.Errorf("invalid reference format: %q", s)
		}
	}
	ref.Domain, ref.Path = domain, path
	if ref.Tag == "" && ref.Digest == "" {
		ref.Tag = DefaultTag
	}
	return ref, nil
}

// Name is domain/path.
func (r Reference) Name() string { return r.Domain + "/" + r.Path }

// String is the canonical form.
func (r Reference) String() string {
	s := r.Name()
	if r.Tag != "" {
		s += ":" + r.Tag
	}
	if r.Digest != "" {
		s += "@" + string(r.Digest)
	}
	return s
}

// FamiliarName drops docker.io/ and library/ like the Docker CLI.
func (r Reference) FamiliarName() string {
	if r.Domain != DefaultDomain {
		return r.Name()
	}
	return strings.TrimPrefix(r.Path, "library/")
}

// FamiliarTagged is "alpine:latest"; empty when no tag.
func (r Reference) FamiliarTagged() string {
	if r.Tag == "" {
		return ""
	}
	return r.FamiliarName() + ":" + r.Tag
}

// FamiliarString is the familiar name with tag and/or digest.
func (r Reference) FamiliarString() string {
	s := r.FamiliarName()
	if r.Tag != "" {
		s += ":" + r.Tag
	}
	if r.Digest != "" {
		s += "@" + string(r.Digest)
	}
	return s
}

// Host is the registry API host.
func (r Reference) Host() string {
	if r.Domain == DefaultDomain {
		return DockerHubHost
	}
	return r.Domain
}

// Selector is what is asked of the registry: the digest when pinned.
func (r Reference) Selector() string {
	if r.Digest != "" {
		return string(r.Digest)
	}
	return r.Tag
}
