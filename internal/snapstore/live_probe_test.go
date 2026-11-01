package snapstore

import (
	"bytes"
	"os"
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
	var b bytes.Buffer
	if err := c.Download(rel, &b); err != nil {
		t.Fatal(err)
	}
	if b.Len() != int(rel.Size) || !bytes.HasPrefix(b.Bytes(), []byte("hsqs")) {
		t.Fatalf("%d bytes, magic %q", b.Len(), b.Bytes()[:4])
	}
	found, err := c.Find("hello")
	if err != nil || len(found) == 0 {
		t.Fatal(err)
	}
	t.Logf("first: %+v", found[0])
}
