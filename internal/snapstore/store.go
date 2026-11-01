// Package snapstore talks to the Snap Store's public API (v2): search, the
// release of a snap on a channel for this architecture, and its download,
// checked against the SHA3-384 the store publishes for it over HTTPS.
package snapstore

import (
	"context"
	"crypto/sha3"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	goruntime "runtime"
	"sort"
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
	HTTP *http.Client // its Timeout bounds API calls, not downloads
	Arch string       // store architecture: arm64, amd64, ...
	// Stall is how long a download may receive nothing before it is dropped
	// and resumed; zero means DefaultStall.
	Stall time.Duration
}

// DefaultStall: a download is bounded by progress, never by total time, so
// a large snap on a slow link still finishes.
const DefaultStall = 60 * time.Second

// fetchAttempts is how many dropped connections one Fetch resumes from.
const fetchAttempts = 8

// New returns a client for this machine's architecture.
func New() *Client {
	arch := goruntime.GOARCH
	if arch == "arm" {
		arch = "armhf"
	}
	return &Client{URL: DefaultURL, HTTP: &http.Client{Timeout: 2 * time.Minute}, Arch: arch}
}

// Found is one search result.
type Found struct {
	Name, Summary, Version, Publisher, Base, Confinement string
}

// Release is a snap's revision on one channel for this architecture.
type Release struct {
	Name        string
	SnapID      string
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
	body, err := c.getRaw(path, "")
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}

func (c *Client) getRaw(path, accept string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, c.URL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Snap-Device-Series", "16")
	req.Header.Set("User-Agent", "ThothDock-snap")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			ErrorList []struct{ Message string } `json:"error-list"`
		}
		if json.Unmarshal(body, &e) == nil && len(e.ErrorList) > 0 {
			return nil, fmt.Errorf("snap store: %s", e.ErrorList[0].Message)
		}
		return nil, fmt.Errorf("snap store answered %s", resp.Status)
	}
	return body, nil
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
		SnapID     string `json:"snap-id"`
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
		rel := &Release{Name: name, SnapID: r.SnapID, Channel: track + "/" + risk, Version: m.Version, Revision: m.Revision,
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

// Fetch downloads rel to file. The bytes arrive in file+".partial" and are
// kept when the connection drops: a dropped or stalled connection (nothing
// for c.Stall) is resumed with an HTTP Range, here up to fetchAttempts times
// and by the next Fetch after that. file appears only once its size and
// SHA3-384 are the store's; a mismatch discards the partial file. progress,
// if set, is called with the bytes on disk after each write.
func (c *Client) Fetch(rel *Release, file string, progress func(done int64)) error {
	u, err := url.Parse(rel.URL)
	if err != nil || u.Scheme != "https" {
		return fmt.Errorf("snap store: refusing a download URL that is not HTTPS: %q", rel.URL)
	}
	part := file + ".partial"
	f, err := os.OpenFile(part, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha3.New384()
	have, err := io.Copy(h, f)
	if err != nil {
		return err
	}
	if have > rel.Size {
		if have, err = restart(f, h); err != nil {
			return err
		}
	}
	for attempt := 0; have < rel.Size; attempt++ {
		var retry bool
		have, retry, err = c.fetchFrom(rel, f, h, have, progress)
		if err == nil {
			break
		}
		if !retry || attempt+1 == fetchAttempts {
			return err
		}
	}
	if have != rel.Size {
		os.Remove(part)
		return fmt.Errorf("snap %s: got %d bytes, the store says %d", rel.Name, have, rel.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != rel.SHA3_384 {
		os.Remove(part)
		return fmt.Errorf("snap %s: SHA3-384 %s does not match the store's %s", rel.Name, got, rel.SHA3_384)
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(part, file)
}

// fetchFrom appends the rest of rel's file to f from offset have. retry
// reports an error the connection caused rather than the store or the disk.
func (c *Client) fetchFrom(rel *Release, f *os.File, h hash.Hash, have int64,
	progress func(int64)) (int64, bool, error) {
	stall := c.Stall
	if stall <= 0 {
		stall = DefaultStall
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watchdog := time.AfterFunc(stall, cancel)
	defer watchdog.Stop()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rel.URL, nil)
	if err != nil {
		return have, false, err
	}
	req.Header.Set("Snap-Device-Series", "16")
	req.Header.Set("User-Agent", "ThothDock-snap")
	if have > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
	}
	dl := *c.HTTP
	dl.Timeout = 0 // the watchdog bounds the download instead
	resp, err := dl.Do(req)
	if err != nil {
		return have, true, stalled(ctx, stall, err)
	}
	defer resp.Body.Close()
	switch {
	case have > 0 && resp.StatusCode == http.StatusPartialContent &&
		strings.HasPrefix(resp.Header.Get("Content-Range"), fmt.Sprintf("bytes %d-", have)):
	case resp.StatusCode == http.StatusOK:
		if have, err = restart(f, h); err != nil {
			return have, false, err
		}
	default:
		return have, resp.StatusCode >= 500, fmt.Errorf("snap store download answered %s", resp.Status)
	}
	body := io.LimitReader(resp.Body, rel.Size-have+1)
	buf := make([]byte, 256<<10)
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			watchdog.Reset(stall)
			if _, err := f.Write(buf[:n]); err != nil {
				return have, false, err
			}
			h.Write(buf[:n])
			have += int64(n)
			if progress != nil {
				progress(have)
			}
		}
		if rerr == io.EOF {
			if have < rel.Size {
				return have, true, fmt.Errorf("snap %s: connection closed at %d of %d bytes", rel.Name, have, rel.Size)
			}
			return have, false, nil
		}
		if rerr != nil {
			return have, true, stalled(ctx, stall, rerr)
		}
	}
}

// restart empties f for a download from the first byte.
func restart(f *os.File, h hash.Hash) (int64, error) {
	h.Reset()
	if err := f.Truncate(0); err != nil {
		return 0, err
	}
	_, err := f.Seek(0, io.SeekStart)
	return 0, err
}

func stalled(ctx context.Context, stall time.Duration, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("no data from the Snap Store for %s", stall)
	}
	return err
}

var (
	snapIDRe  = regexp.MustCompile(`^[A-Za-z0-9]{32}$`)
	aliasRe   = regexp.MustCompile(`^[a-zA-Z0-9][-_.a-zA-Z0-9]*$`)
	appNameRe = regexp.MustCompile(`^[a-zA-Z0-9](?:-?[a-zA-Z0-9])*$`)
)

// Aliases returns the command aliases the store grants a snap (alias ->
// app), as snapd sets them up on install: "gcloud" for
// google-cloud-cli.gcloud. They come from the snap's snap-declaration
// assertion, fetched over HTTPS from the same store as the download and its
// SHA3-384; its signature is not checked here, as snapd would.
func (c *Client) Aliases(snapID string) (map[string]string, error) {
	if !snapIDRe.MatchString(snapID) {
		return nil, fmt.Errorf("snap store: bad snap id %q", snapID)
	}
	body, err := c.getRaw("/v2/assertions/snap-declaration/16/"+snapID, "application/x.ubuntu.assertion")
	if err != nil {
		return nil, err
	}
	return ParseDeclarationAliases(body), nil
}

// ParseDeclarationAliases reads the aliases of a snap-declaration's headers:
// the "aliases" list of name/target maps, or, in older declarations, the
// "auto-aliases" list, where each alias names the app of the same name.
// Entries that are not a valid alias and app name are dropped.
func ParseDeclarationAliases(assertion []byte) map[string]string {
	head, _, _ := strings.Cut(strings.ReplaceAll(string(assertion), "\r\n", "\n"), "\n\n")
	lists := map[string][]map[string]string{}
	var cur string
	for _, line := range strings.Split(head, "\n") {
		if line == "" {
			continue
		}
		if line[0] != ' ' {
			cur = ""
			if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(v) == "" &&
				(k == "aliases" || k == "auto-aliases") {
				cur = k
			}
			continue
		}
		if cur == "" {
			continue
		}
		t := strings.TrimSpace(line)
		switch {
		case t == "-":
			lists[cur] = append(lists[cur], map[string]string{})
		case strings.HasPrefix(t, "- "):
			lists[cur] = append(lists[cur], map[string]string{"": strings.TrimSpace(t[2:])})
		default:
			if k, v, ok := strings.Cut(t, ":"); ok && len(lists[cur]) > 0 {
				lists[cur][len(lists[cur])-1][k] = strings.TrimSpace(v)
			}
		}
	}
	out := map[string]string{}
	add := func(alias, app string) {
		if aliasRe.MatchString(alias) && appNameRe.MatchString(app) {
			out[alias] = app
		}
	}
	if l, ok := lists["aliases"]; ok {
		for _, e := range l {
			add(e["name"], e["target"])
		}
	} else {
		for _, e := range lists["auto-aliases"] {
			add(e[""], e[""])
		}
	}
	return out
}

// SortedAliases lists m's aliases in order.
func SortedAliases(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for a := range m {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}
