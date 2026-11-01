package snapstore

import (
	"bytes"
	"crypto/sha3"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
			fmt.Fprintf(w, `{"name":"tool","channel-map":[
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
	var b bytes.Buffer
	if err := c.Download(rel, &b); err != nil || b.String() != string(blob) {
		t.Fatalf("download: %v", err)
	}
}

func TestDownloadRefusesWrongChecksumSizeAndURL(t *testing.T) {
	blob := []byte("hsqs-fake-snap")
	c, srv := fakeStore(t, blob, strings.Repeat("0", 96))
	rel, err := c.Info("tool", "stable")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Download(rel, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "SHA3-384") {
		t.Fatalf("bad checksum accepted: %v", err)
	}
	good := *rel
	good.SHA3_384 = sum384(blob)
	short := good
	short.Size = int64(len(blob)) + 5
	if err := c.Download(&short, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("bad size accepted: %v", err)
	}
	plain := good
	plain.URL = strings.Replace(srv.URL, "https://", "http://", 1) + "/dl/tool"
	if err := c.Download(&plain, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "HTTPS") {
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
