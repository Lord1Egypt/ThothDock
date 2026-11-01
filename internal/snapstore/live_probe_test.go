package snapstore

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// Hits the real store; run with THOTHDOCK_LIVE_STORE=1.
func TestLiveStore(t *testing.T) {
	if os.Getenv("THOTHDOCK_LIVE_STORE") != "1" {
		t.Skip("set THOTHDOCK_LIVE_STORE=1")
	}
	c := New()
	c.Arch = "arm64"
	rel, err := c.Info("hello-world", "stable")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%+v", rel)
	file := filepath.Join(t.TempDir(), "hello-world.snap")
	if err := c.Fetch(rel, file, nil); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(file)
	if err != nil || len(b) != int(rel.Size) || !bytes.HasPrefix(b, []byte("hsqs")) {
		t.Fatalf("%d bytes: %v", len(b), err)
	}
	aliases, err := c.Aliases(rel.SnapID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("aliases: %v", aliases)
	gc, err := c.Info("google-cloud-cli", "stable")
	if err != nil {
		t.Fatal(err)
	}
	if a, err := c.Aliases(gc.SnapID); err != nil || a["gcloud"] != "gcloud" {
		t.Fatalf("google-cloud-cli aliases %v: %v", a, err)
	}
	found, err := c.Find("hello")
	if err != nil || len(found) == 0 {
		t.Fatal(err)
	}
	t.Logf("first: %+v", found[0])
}
