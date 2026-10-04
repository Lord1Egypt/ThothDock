package oci

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"strings"
)

// Digest is a content digest in "sha256:<64 lowercase hex>" form. ThothDock
// accepts only sha256: it is what every registry serves, and refusing the
// rest is safer than half-supporting them.
type Digest string

// ParseDigest validates s.
func ParseDigest(s string) (Digest, error) {
	algo, hexPart, ok := strings.Cut(s, ":")
	if !ok {
		return "", fmt.Errorf("invalid digest %q: missing algorithm", s)
	}
	if algo != "sha256" {
		return "", fmt.Errorf("unsupported digest algorithm %q", algo)
	}
	if len(hexPart) != 64 {
		return "", fmt.Errorf("invalid sha256 digest %q: wrong length", s)
	}
	for _, c := range hexPart {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return "", fmt.Errorf("invalid sha256 digest %q: not lowercase hex", s)
		}
	}
	return Digest(s), nil
}

// Hex is the digest without its algorithm prefix.
func (d Digest) Hex() string { return strings.TrimPrefix(string(d), "sha256:") }

func (d Digest) String() string { return string(d) }

// FromBytes computes the sha256 digest of b.
func FromBytes(b []byte) Digest {
	sum := sha256.Sum256(b)
	return Digest("sha256:" + hex.EncodeToString(sum[:]))
}

// Digester hashes a stream.
type Digester struct{ h hash.Hash }

func NewDigester() *Digester { return &Digester{h: sha256.New()} }

func (d *Digester) Write(p []byte) (int, error) { return d.h.Write(p) }

func (d *Digester) Digest() Digest {
	return Digest("sha256:" + hex.EncodeToString(d.h.Sum(nil)))
}
