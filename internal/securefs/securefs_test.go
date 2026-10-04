package securefs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenFileFollowsSymlinksInsideRootOnly(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	os.MkdirAll(filepath.Join(root, "real/etc"), 0o700)
	os.WriteFile(filepath.Join(root, "real/etc/passwd"), []byte("inside"), 0o600)
	os.WriteFile(filepath.Join(base, "secret"), []byte("outside"), 0o600)
	os.Symlink("/real/etc", filepath.Join(root, "etc"))                   // absolute, in-root
	os.Symlink("../../../../secret", filepath.Join(root, "up"))           // climbs
	os.Symlink(filepath.Join(base, "secret"), filepath.Join(root, "abs")) // host path
	os.Symlink("loop", filepath.Join(root, "loop"))
	r, err := OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if b, err := r.ReadFile("/etc/passwd", 100); err != nil || string(b) != "inside" {
		t.Fatalf("%q %v", b, err)
	}
	for _, p := range []string{"/up", "/abs"} {
		if b, err := r.ReadFile(p, 100); err == nil {
			t.Fatalf("%s escaped: %q", p, b)
		}
	}
	if _, err := r.ReadFile("/loop", 100); err == nil {
		t.Fatal("symlink loop accepted")
	}
	if err := r.MkdirAll("/up/made"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "secret/made")); err != nil {
		t.Fatalf("mkdir through a climbing symlink must land inside: %v", err)
	}
}

func TestRemoveTreeNeverFollows(t *testing.T) {
	base := t.TempDir()
	victim := filepath.Join(base, "victim")
	os.Mkdir(victim, 0o700)
	os.WriteFile(filepath.Join(victim, "keep"), nil, 0o600)
	tree := filepath.Join(base, "tree")
	os.MkdirAll(filepath.Join(tree, "ro/deep"), 0o700)
	os.Symlink(victim, filepath.Join(tree, "link"))
	os.Chmod(filepath.Join(tree, "ro"), 0o500)
	if err := RemoveTree(tree); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tree); !os.IsNotExist(err) {
		t.Fatal("tree left")
	}
	if _, err := os.Stat(filepath.Join(victim, "keep")); err != nil {
		t.Fatal("symlink target was removed")
	}
}
