package snapstore

import (
	"bytes"
	"crypto/sha3"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fakeStore(t *testing.T, blob []byte, sum string) (*Client, *httptest.Server) {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Snap-Device-Series") != "16" {
			http.Error(w, "series", 400)
			return
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/v2/snaps/info/tool"):
			fmt.Fprintf(w, `{"name":"tool","snap-id":"abcdefghijklmnopqrstuvwxyz012345","channel-map":[
			 {"channel":{"architecture":"amd64","track":"latest","risk":"stable"},"type":"app","version":"9","revision":9,
			  "download":{"url":"%[1]s/dl/amd64","sha3-384":"%[2]s","size":%[3]d}},
			 {"channel":{"architecture":"arm64","track":"latest","risk":"stable"},"type":"app","base":null,"confinement":"strict","version":"1.0","revision":7,
			  "download":{"url":"%[1]s/dl/tool","sha3-384":"%[2]s","size":%[3]d}},
			 {"channel":{"architecture":"arm64","track":"2","risk":"edge"},"type":"app","base":"core22","confinement":"classic","version":"2.0-dev","revision":8,
			  "download":{"url":"%[1]s/dl/tool","sha3-384":"%[2]s","size":%[3]d}}]}`, srv.URL, sum, len(blob))
		case r.URL.Path == "/dl/tool":
			w.Write(blob)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return &Client{URL: srv.URL, HTTP: srv.Client(), Arch: "arm64"}, srv
}

func sum384(b []byte) string { h := sha3.Sum384(b); return hex.EncodeToString(h[:]) }

func TestInfoPicksArchAndChannel(t *testing.T) {
	blob := []byte("hsqs-fake-snap")
	c, _ := fakeStore(t, blob, sum384(blob))
	rel, err := c.Info("tool", "")
	if err != nil {
		t.Fatal(err)
	}
	if rel.Revision != 7 || rel.Base != "core" || rel.Confinement != "strict" || rel.Channel != "latest/stable" {
		t.Fatalf("%+v", rel)
	}
	rel, err = c.Info("tool", "2/edge")
	if err != nil || rel.Revision != 8 || rel.Base != "core22" || rel.Confinement != "classic" {
		t.Fatalf("%+v %v", rel, err)
	}
	if _, err := c.Info("tool", "beta"); err == nil || !strings.Contains(err.Error(), "available: latest/stable, 2/edge") {
		t.Fatalf("missing channel: %v", err)
	}
	if _, err := c.Info("nope", ""); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unknown snap: %v", err)
	}
	if rel.SnapID != "abcdefghijklmnopqrstuvwxyz012345" {
		t.Fatalf("snap id %q", rel.SnapID)
	}
	file := filepath.Join(t.TempDir(), "tool.snap")
	if err := c.Fetch(rel, file, nil); err != nil {
		t.Fatalf("download: %v", err)
	}
	if b, _ := os.ReadFile(file); string(b) != string(blob) {
		t.Fatalf("got %q", b)
	}
	if _, err := os.Stat(file + ".partial"); !os.IsNotExist(err) {
		t.Fatalf("partial file left: %v", err)
	}
}

func TestDownloadRefusesWrongChecksumSizeAndURL(t *testing.T) {
	blob := []byte("hsqs-fake-snap")
	c, srv := fakeStore(t, blob, strings.Repeat("0", 96))
	rel, err := c.Info("tool", "stable")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "tool.snap")
	if err := c.Fetch(rel, file, nil); err == nil || !strings.Contains(err.Error(), "SHA3-384") {
		t.Fatalf("bad checksum accepted: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("a refused download left %v", entries)
	}
	good := *rel
	good.SHA3_384 = sum384(blob)
	short := good
	short.Size = int64(len(blob)) + 5
	if err := c.Fetch(&short, file, nil); err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("bad size accepted: %v", err)
	}
	plain := good
	plain.URL = strings.Replace(srv.URL, "https://", "http://", 1) + "/dl/tool"
	if err := c.Fetch(&plain, file, nil); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("http URL accepted: %v", err)
	}
}

func TestNamesAndChannels(t *testing.T) {
	for _, ok := range []string{"hello", "core22", "my-tool", "a"} {
		if !ValidName(ok) {
			t.Errorf("%s refused", ok)
		}
	}
	for _, bad := range []string{"", "Hello", "-a", "a-", "a--b", "a/b", "../x", strings.Repeat("a", 41)} {
		if ValidName(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, bad := range []string{"nightly", "a/b/c", "latest/sometimes"} {
		if _, _, err := ParseChannel(bad); err == nil {
			t.Errorf("channel %q accepted", bad)
		}
	}
}

// flaky serves blob, but the first connections send only part of it and then
// hang until the client gives up, as a stalled mobile or Wi-Fi link does.
func flaky(t *testing.T, blob []byte, cut []int, ranges bool) (*Client, *Release, *atomic.Int32, *[]string) {
	var hits atomic.Int32
	var seen []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(hits.Add(1))
		seen = append(seen, r.Header.Get("Range"))
		start := 0
		if rg := r.Header.Get("Range"); ranges && rg != "" {
			start, _ = strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rg, "bytes="), "-"))
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(blob)-1, len(blob)))
			w.WriteHeader(http.StatusPartialContent)
		}
		if n <= len(cut) {
			w.Write(blob[start:cut[n-1]])
			w.(http.Flusher).Flush()
			<-r.Context().Done() // stall
			return
		}
		w.Write(blob[start:])
	}))
	t.Cleanup(srv.Close)
	c := &Client{URL: srv.URL, HTTP: srv.Client(), Arch: "arm64", Stall: 200 * time.Millisecond}
	rel := &Release{Name: "tool", URL: srv.URL + "/dl", SHA3_384: sum384(blob), Size: int64(len(blob))}
	return c, rel, &hits, &seen
}

func TestFetchResumesAStalledDownload(t *testing.T) {
	blob := bytes.Repeat([]byte("0123456789abcdef"), 4096)
	c, rel, hits, seen := flaky(t, blob, []int{10000, 30000}, true)
	file := filepath.Join(t.TempDir(), "tool.snap")
	var last int64
	if err := c.Fetch(rel, file, func(n int64) { last = n }); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(file); !bytes.Equal(b, blob) {
		t.Fatal("content differs")
	}
	if hits.Load() != 3 || (*seen)[0] != "" || (*seen)[1] != "bytes=10000-" || (*seen)[2] != "bytes=30000-" {
		t.Fatalf("requests %d %q", hits.Load(), *seen)
	}
	if last != int64(len(blob)) {
		t.Fatalf("progress ended at %d", last)
	}
}

func TestFetchStartsOverWhenTheServerIgnoresRange(t *testing.T) {
	blob := bytes.Repeat([]byte("xyz"), 5000)
	c, rel, _, _ := flaky(t, blob, []int{4000}, false)
	file := filepath.Join(t.TempDir(), "tool.snap")
	if err := c.Fetch(rel, file, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(file); !bytes.Equal(b, blob) {
		t.Fatal("content differs")
	}
}

func TestFetchKeepsThePartialFileForTheNextRun(t *testing.T) {
	blob := bytes.Repeat([]byte("snap"), 8000)
	cuts := make([]int, fetchAttempts)
	for i := range cuts {
		cuts[i] = 1000 * (i + 1)
	}
	c, rel, _, _ := flaky(t, blob, cuts, true)
	file := filepath.Join(t.TempDir(), "tool.snap")
	err := c.Fetch(rel, file, nil)
	if err == nil || !strings.Contains(err.Error(), "no data from the Snap Store") {
		t.Fatalf("expected a stall error, got %v", err)
	}
	if st, err := os.Stat(file + ".partial"); err != nil || st.Size() != int64(cuts[len(cuts)-1]) {
		t.Fatalf("partial file: %v %v", st, err)
	}
	if err := c.Fetch(rel, file, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(file); !bytes.Equal(b, blob) {
		t.Fatal("content differs")
	}
}

func TestDeclarationAliases(t *testing.T) {
	decl := "type: snap-declaration\nauthority-id: canonical\nrevision: 2\nseries: 16\n" +
		"snap-id: pcLToi6ZMhWz3VzItZIgJf7Qgc1AswoH\naliases:\n  -\n    name: bq\n    target: bq\n" +
		"  -\n    name: gcloud\n    target: gcloud\n  -\n    name: ../evil\n    target: gcloud\n" +
		"  -\n    name: ok\n    target: bad/app\nauto-aliases:\n  - bq\n  - gcloud\n" +
		"publisher-id: x\nsnap-name: google-cloud-cli\n\nAcLBUgQAAQoABgUCYf0k\n"
	got := ParseDeclarationAliases([]byte(decl))
	if len(got) != 2 || got["bq"] != "bq" || got["gcloud"] != "gcloud" {
		t.Fatalf("%v", got)
	}
	legacy := "type: snap-declaration\nauto-aliases:\n  - lxc\n  - -bad\nsnap-name: lxd\n\nsig\n"
	if got := ParseDeclarationAliases([]byte(legacy)); len(got) != 1 || got["lxc"] != "lxc" {
		t.Fatalf("legacy %v", got)
	}
	if got := ParseDeclarationAliases([]byte("type: snap-declaration\nsnap-name: x\n\nsig\n")); len(got) != 0 {
		t.Fatalf("none %v", got)
	}
	// Signature lines after the blank line are never read as headers.
	if got := ParseDeclarationAliases([]byte("type: snap-declaration\n\naliases:\n  -\n    name: a\n    target: a\n")); len(got) != 0 {
		t.Fatalf("body read as headers: %v", got)
	}
	if _, err := (&Client{}).Aliases("../../x"); err == nil {
		t.Fatal("bad snap id accepted")
	}
}
