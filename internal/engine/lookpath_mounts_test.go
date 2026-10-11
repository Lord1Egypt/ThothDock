package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Lord1Egypt/ThothDock/internal/securefs"
)

// A program that exists only in a bind mount or volume is found, as Docker
// finds it (docker run -v ./bin:/mnt image /mnt/prog); before, the lookup
// only looked in the image and failed with 127.
func TestLookPathFindsProgramsInsideMounts(t *testing.T) {
	rootDir, hostDir, deeper := t.TempDir(), t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(rootDir, "mnt"), 0o755)
	os.WriteFile(filepath.Join(hostDir, "prog"), []byte("#!"), 0o755)
	os.WriteFile(filepath.Join(hostDir, "data"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(hostDir, "sub"), 0o755)
	os.WriteFile(filepath.Join(deeper, "tool"), []byte("#!"), 0o755)
	root, err := securefs.OpenRoot(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	mounts := []BindRecord{{Source: hostDir, Target: "/mnt"}, {Source: deeper, Target: "/mnt/sub"}}

	if p, err := lookPath(root, mounts, "/mnt/prog", "/", nil); err != nil || p != "/mnt/prog" {
		t.Fatalf("absolute path in a mount: %q %v", p, err)
	}
	if p, err := lookPath(root, mounts, "prog", "/", []string{"PATH=/usr/bin:/mnt"}); err != nil || p != "/mnt/prog" {
		t.Fatalf("PATH search into a mount: %q %v", p, err)
	}
	if _, err := lookPath(root, mounts, "/mnt/sub/tool", "/", nil); err != nil {
		t.Fatalf("the deepest mount wins: %v", err)
	}
	if _, err := lookPath(root, mounts, "/mnt/data", "/", nil); err == nil {
		t.Fatal("a file without an execute bit must be refused (126)")
	}
	if _, err := lookPath(root, mounts, "/mnt/missing", "/", nil); err == nil {
		t.Fatal("a missing program must be refused (127)")
	}
	if _, err := lookPath(root, nil, "/mnt/prog", "/", nil); err == nil {
		t.Fatal("without the mount the image has no /mnt/prog")
	}
	// A target that merely shares a prefix is not the mount.
	if _, ok := mountedPath(mounts, "/mntx/prog"); ok {
		t.Fatal("/mntx is not under /mnt")
	}
}
