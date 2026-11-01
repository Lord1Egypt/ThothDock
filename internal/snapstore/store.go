// Package snapstore talks to the Snap Store's public API (v2): search, the
// release of a snap on a channel for this architecture, and its download,
// checked against the SHA3-384 the store publishes for it over HTTPS.
package snapstore

import (
	"crypto/sha3"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	goruntime "runtime"
	"strings"
	"time"
)

// DefaultURL is the store's API endpoint.
const DefaultURL = "https://api.snapcraft.io"

// MaxSnapBytes bounds one download.
const MaxSnapBytes = 4 << 30

var nameRe = regexp.MustCompile(`^[a-z0-9](-?[a-z0-9])*$`)

// ValidName reports whether s is a snap name (lower-case letters, digits and
// single hyphens, at most 40 characters).
func ValidName(s string) bool { return len(s) <= 40 && nameRe.MatchString(s) }

// Client queries the store.
type Client struct {
	URL  string
	HTTP *http.Client
	Arch string // store architecture: arm64, amd64, ...
}

// New returns a client for this machine's architecture.
func New() *Client {
	arch := goruntime.GOARCH
	if arch == "arm" {
		arch = "armhf"
	}
	return &Client{URL: DefaultURL, HTTP: &http.Client{Timeout: 10 * time.Minute}, Arch: arch}
}

// Found is one search result.
type Found struct {
	Name, Summary, Version, Publisher, Base, Confinement string
}

// Release is a snap's revision on one channel for this architecture.
type Release struct {
	Name        string
	Channel     string // track/risk
	Version     string
	Revision    int
	Type        string // app, base, os, snapd, gadget, kernel
	Base        string // "" for bases; "core" when an app names none
	Confinement string // strict, classic, devmode
	URL         string
	SHA3_384    string
	Size        int64
}

func (c *Client) get(path string, v any) error {
	req, err := http.NewRequest(http.MethodGet, c.URL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Snap-Device-Series", "16")
	req.Header.Set("User-Agent", "ThothDock-snap")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			ErrorList []struct{ Message string } `json:"error-list"`
		}
		if json.Unmarshal(body, &e) == nil && len(e.ErrorList) > 0 {
			return fmt.Errorf("snap store: %s", e.ErrorList[0].Message)
		}
		return fmt.Errorf("snap store answered %s", resp.Status)
	}
	return json.Unmarshal(body, v)
}

// ErrNotFound is a snap the store does not have.
var ErrNotFound = errors.New("snap not found")

// Find searches the store.
func (c *Client) Find(query string) ([]Found, error) {
	var r struct {
		Results []struct {
			Name     string
			Revision struct{ Version, Base, Confinement string }
			Snap     struct {
				Summary   string
				Publisher struct{ Username string }
			}
		}
	}
	q := url.Values{"q": {query}, "architecture": {c.Arch}, "fields": {"summary,version,publisher,base,confinement"}}
	if err := c.get("/v2/snaps/find?"+q.Encode(), &r); err != nil {
		return nil, err
	}
	out := make([]Found, 0, len(r.Results))
	for _, x := range r.Results {
		out = append(out, Found{Name: x.Name, Summary: x.Snap.Summary, Version: x.Revision.Version,
			Publisher: x.Snap.Publisher.Username, Base: x.Revision.Base, Confinement: x.Revision.Confinement})
	}
	return out, nil
}

// ParseChannel turns "stable", "edge", "22/stable" or "latest/beta" into
// track and risk.
func ParseChannel(ch string) (track, risk string, err error) {
	if ch == "" {
		ch = "stable"
	}
	parts := strings.Split(ch, "/")
	switch len(parts) {
	case 1:
		track, risk = "latest", parts[0]
	case 2:
		track, risk = parts[0], parts[1]
	default:
		return "", "", fmt.Errorf("channel %q: use risk or track/risk", ch)
	}
	switch risk {
	case "stable", "candidate", "beta", "edge":
	default:
		return "", "", fmt.Errorf("channel %q: risk must be stable, candidate, beta or edge", ch)
	}
	return track, risk, nil
}

// Info finds the release of name on channel for this architecture.
func (c *Client) Info(name, channel string) (*Release, error) {
	if !ValidName(name) {
		return nil, fmt.Errorf("%q is not a valid snap name", name)
	}
	track, risk, err := ParseChannel(channel)
	if err != nil {
		return nil, err
	}
	var r struct {
		Name       string
		ChannelMap []struct {
			Channel                          struct{ Architecture, Track, Risk string }
			Type, Base, Confinement, Version string
			Revision                         int
			Download                         struct {
				URL      string
				SHA3_384 string `json:"sha3-384"`
				Size     int64
			}
		} `json:"channel-map"`
	}
	q := url.Values{"architecture": {c.Arch}, "fields": {"base,confinement,version,revision,download,type"}}
	if err := c.get("/v2/snaps/info/"+name+"?"+q.Encode(), &r); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("snap %q not found in the store", name)
		}
		return nil, err
	}
	var have []string
	for _, m := range r.ChannelMap {
		if m.Channel.Architecture != c.Arch {
			continue
		}
		have = append(have, m.Channel.Track+"/"+m.Channel.Risk)
		if m.Channel.Track != track || m.Channel.Risk != risk {
			continue
		}
		rel := &Release{Name: name, Channel: track + "/" + risk, Version: m.Version, Revision: m.Revision,
			Type: m.Type, Base: m.Base, Confinement: m.Confinement,
			URL: m.Download.URL, SHA3_384: strings.ToLower(m.Download.SHA3_384), Size: m.Download.Size}
		if rel.Type == "app" && rel.Base == "" {
			rel.Base = "core" // an app without a base runs on the original core
		}
		if rel.URL == "" || len(rel.SHA3_384) != 96 || rel.Size <= 0 || rel.Size > MaxSnapBytes {
			return nil, fmt.Errorf("snap store: incomplete download information for %s", name)
		}
		return rel, nil
	}
	if len(have) == 0 {
		return nil, fmt.Errorf("snap %q has no release for %s", name, c.Arch)
	}
	return nil, fmt.Errorf("snap %q has no release on %s/%s for %s (available: %s)", name, track, risk, c.Arch, strings.Join(have, ", "))
}

// Download writes the release's file to w, refusing it unless its size and
// SHA3-384 are the store's.
func (c *Client) Download(rel *Release, w io.Writer) error {
	u, err := url.Parse(rel.URL)
	if err != nil || u.Scheme != "https" {
		return fmt.Errorf("snap store: refusing a download URL that is not HTTPS: %q", rel.URL)
	}
	req, err := http.NewRequest(http.MethodGet, rel.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Snap-Device-Series", "16")
	req.Header.Set("User-Agent", "ThothDock-snap")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("snap store download answered %s", resp.Status)
	}
	h := sha3.New384()
	n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(resp.Body, rel.Size+1))
	if err != nil {
		return err
	}
	if n != rel.Size {
		return fmt.Errorf("snap %s: got %d bytes, the store says %d", rel.Name, n, rel.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != rel.SHA3_384 {
		return fmt.Errorf("snap %s: SHA3-384 %s does not match the store's %s", rel.Name, got, rel.SHA3_384)
	}
	return nil
}
