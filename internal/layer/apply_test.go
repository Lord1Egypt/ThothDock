package layer

import (
	"archive/tar"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type ent struct {
	name, link, body string
	typ              byte
	mode             int64
}

func file(name, body string) ent { return ent{name: name, body: body, typ: tar.TypeReg, mode: 0o644} }
func dir(name string) ent        { return ent{name: name, typ: tar.TypeDir, mode: 0o755} }
func sym(name, target string) ent {
	return ent{name: name, link: target, typ: tar.TypeSymlink, mode: 0o777}
}
func hard(name, target string) ent { return ent{name: name, link: target, typ: tar.TypeLink} }

func mkTar(t *testing.T, ents ...ent) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range ents {
		h := &tar.Header{Name: e.name, Linkname: e.link, Typeflag: e.typ, Mode: e.mode, Format: tar.FormatPAX}
		if e.typ == tar.TypeReg {
			h.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if e.typ == tar.TypeReg {
			tw.Write([]byte(e.body))
		}
	}
	tw.Close()
	return &buf
}

// sandbox returns a root to extract into and an "outside" directory holding
// a secret that no layer may touch.
func sandbox(t *testing.T) (root, outside string) {
	base := t.TempDir()
	root = filepath.Join(base, "root")
	outside = filepath.Join(base, "outside")
	os.Mkdir(root, 0o700)
	os.Mkdir(outside, 0o700)
	os.WriteFile(filepath.Join(outside, "secret"), []byte("untouched"), 0o600)
	return root, outside
}

func assertOutsideIntact(t *testing.T, outside string) {
	t.Helper()
	entries, _ := os.ReadDir(outside)
	if len(entries) != 1 || entries[0].Name() != "secret" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("outside directory changed: %v", names)
	}
	b, err := os.ReadFile(filepath.Join(outside, "secret"))
	if err != nil || string(b) != "untouched" {
		t.Fatalf("outside secret modified: %q %v", b, err)
	}
}

func apply(t *testing.T, root string, ents ...ent) (Stats, error) {
	t.Helper()
	return Apply(root, mkTar(t, ents...))
}

func wantUnsafe(t *testing.T, err error) {
	t.Helper()
	var ue *UnsafeEntryError
	if !errors.As(err, &ue) {
		t.Fatalf("want UnsafeEntryError, got %v", err)
	}
}

func TestRejectsDotDotTraversal(t *testing.T) {
	root, outside := sandbox(t)
	for _, name := range []string{"../outside/secret", "a/../../outside/secret", "a/b/../../../x"} {
		_, err := apply(t, root, file(name, "pwned"))
		wantUnsafe(t, err)
	}
	assertOutsideIntact(t, outside)
}

func TestRejectsAbsolutePath(t *testing.T) {
	root, outside := sandbox(t)
	_, err := apply(t, root, file(filepath.Join(outside, "secret"), "pwned"))
	wantUnsafe(t, err)
	assertOutsideIntact(t, outside)
}

func TestRejectsNUL(t *testing.T) {
	_, err := cleanName("a\x00b")
	wantUnsafe(t, err)
}

func TestAbsoluteSymlinkParentStaysInRoot(t *testing.T) {
	root, outside := sandbox(t)
	_, err := apply(t, root, sym("evil", outside), file("evil/secret", "pwned"), file("evil/new", "x"))
	if err != nil {
		t.Fatal(err)
	}
	assertOutsideIntact(t, outside)
	// chroot semantics: the write landed below the root, at the same path.
	b, err := os.ReadFile(filepath.Join(root, outside, "secret"))
	if err != nil || string(b) != "pwned" {
		t.Fatalf("expected in-root copy, got %q %v", b, err)
	}
}

func TestRelativeSymlinkParentCannotClimb(t *testing.T) {
	root, outside := sandbox(t)
	_, err := apply(t, root, sym("evil", "../../../../../../../outside"), file("evil/secret", "pwned"))
	if err != nil {
		t.Fatal(err)
	}
	assertOutsideIntact(t, outside)
	if _, err := os.Stat(filepath.Join(root, "outside", "secret")); err != nil {
		t.Fatalf("expected write clamped at root: %v", err)
	}
}

func TestChainedSymlinksAndLoops(t *testing.T) {
	root, outside := sandbox(t)
	_, err := apply(t, root, sym("a", "b"), sym("b", "c"), sym("c", outside), file("a/x", "1"))
	if err != nil {
		t.Fatal(err)
	}
	assertOutsideIntact(t, outside)
	root2, _ := sandbox(t)
	_, err = apply(t, root2, sym("l1", "l2"), sym("l2", "l1"), file("l1/x", "1"))
	if err == nil || !strings.Contains(err.Error(), "too many levels") {
		t.Fatalf("want ELOOP, got %v", err)
	}
}

func TestDanglingSymlinkIsReplacedNotFollowed(t *testing.T) {
	root, outside := sandbox(t)
	target := filepath.Join(outside, "created-by-layer")
	_, err := apply(t, root, sym("x", target), file("x", "data"))
	if err != nil {
		t.Fatal(err)
	}
	assertOutsideIntact(t, outside)
	fi, err := os.Lstat(filepath.Join(root, "x"))
	if err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("x should be a regular file: %v %v", fi, err)
	}
}

func TestSymlinkToSecretIsNeverRead(t *testing.T) {
	root, outside := sandbox(t)
	_, err := apply(t, root, sym("s", filepath.Join(outside, "secret")))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.Readlink(filepath.Join(root, "s"))
	if got != filepath.Join(outside, "secret") {
		t.Fatalf("symlink target must be stored verbatim, got %q", got)
	}
	assertOutsideIntact(t, outside)
}

func TestHardlinkEscapes(t *testing.T) {
	root, outside := sandbox(t)
	_, err := apply(t, root, hard("h", "../outside/secret"))
	wantUnsafe(t, err)
	_, err = apply(t, root, hard("h", filepath.Join(outside, "secret")))
	wantUnsafe(t, err)
	// Through a symlinked parent the target resolves inside the root, where
	// the secret does not exist.
	_, err = apply(t, root, sym("up", outside), hard("h", "up/secret"))
	wantUnsafe(t, err)
	assertOutsideIntact(t, outside)
	if _, err := os.Lstat(filepath.Join(root, "h")); err == nil {
		t.Fatal("hard link must not exist")
	}
}

func TestHardlinkToSymlinkLinksTheSymlink(t *testing.T) {
	root, outside := sandbox(t)
	_, err := apply(t, root, sym("s", filepath.Join(outside, "secret")), hard("h", "s"))
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(filepath.Join(root, "h"))
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("h must be the symlink itself: %v %v", fi, err)
	}
	assertOutsideIntact(t, outside)
}

func TestHardlinkToDirectoryRejected(t *testing.T) {
	root, _ := sandbox(t)
	_, err := apply(t, root, dir("d"), hard("h", "d"))
	wantUnsafe(t, err)
}

func TestHardlinkShared(t *testing.T) {
	root, _ := sandbox(t)
	st, err := apply(t, root, file("a", "same"), hard("b", "a"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Hardlinks != 1 {
		t.Fatalf("stats %+v", st)
	}
	b, _ := os.ReadFile(filepath.Join(root, "b"))
	if string(b) != "same" {
		t.Fatalf("b = %q", b)
	}
}

func TestWhiteoutRemovesOnlyItsTarget(t *testing.T) {
	root, _ := sandbox(t)
	if _, err := apply(t, root, dir("etc"), file("etc/a", "1"), file("etc/b", "2"), dir("etc/sub"), file("etc/sub/x", "3")); err != nil {
		t.Fatal(err)
	}
	if _, err := apply(t, root, file("etc/.wh.a", ""), file("etc/.wh.sub", "")); err != nil {
		t.Fatal(err)
	}
	assertTree(t, root, "etc", "etc/b")
}

func TestWhiteoutDoesNotRemoveSameLayerEntry(t *testing.T) {
	root, _ := sandbox(t)
	if _, err := apply(t, root, file("new", "1"), file(".wh.new", "")); err != nil {
		t.Fatal(err)
	}
	assertTree(t, root, "new")
}

func TestMaliciousWhiteouts(t *testing.T) {
	root, outside := sandbox(t)
	for _, n := range []string{".wh..", ".wh...", "a/.wh.."} {
		_, err := apply(t, root, file(n, ""))
		wantUnsafe(t, err)
	}
	_, err := apply(t, root, file("../outside/.wh.secret", ""))
	wantUnsafe(t, err)
	// A whiteout through a symlink to the outside directory resolves inside
	// the root and finds nothing to delete there.
	if _, err := apply(t, root, sym("link", outside), file("link/.wh.secret", "")); err != nil {
		t.Fatal(err)
	}
	if _, err := apply(t, root, file(".wh.link", "")); err != nil {
		t.Fatal(err)
	}
	assertOutsideIntact(t, outside)
}

func TestOpaqueDirectory(t *testing.T) {
	root, _ := sandbox(t)
	if _, err := apply(t, root, dir("a"), file("a/x", "lower"), dir("a/y"), file("a/y/z", "lower"), file("keep", "k")); err != nil {
		t.Fatal(err)
	}
	// Entries before and after the marker both survive; all lower content
	// below a, including below a/y, disappears.
	if _, err := apply(t, root, dir("a"), file("a/new", "upper"), file("a/.wh..wh..opq", ""), file("a/y/w", "upper")); err != nil {
		t.Fatal(err)
	}
	assertTree(t, root, "a", "a/new", "a/y", "a/y/w", "keep")
}

func TestReplaceFileWithDirAndBack(t *testing.T) {
	root, _ := sandbox(t)
	if _, err := apply(t, root, file("p", "file")); err != nil {
		t.Fatal(err)
	}
	if _, err := apply(t, root, dir("p"), file("p/q", "x")); err != nil {
		t.Fatal(err)
	}
	if _, err := apply(t, root, file("p", "file again")); err != nil {
		t.Fatal(err)
	}
	assertTree(t, root, "p")
}

func TestPrivilegeBitsStripped(t *testing.T) {
	root, _ := sandbox(t)
	e := file("suid", "x")
	e.mode = 0o4755
	d := dir("sticky")
	d.mode = 0o1777
	if _, err := apply(t, root, e, d); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Lstat(filepath.Join(root, "suid"))
	if fi.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 || fi.Mode().Perm() != 0o755 {
		t.Fatalf("suid mode %v", fi.Mode())
	}
	fi, _ = os.Lstat(filepath.Join(root, "sticky"))
	if fi.Mode()&os.ModeSticky != 0 || fi.Mode().Perm() != 0o777 {
		t.Fatalf("sticky mode %v", fi.Mode())
	}
}

func TestDeviceNodesSkipped(t *testing.T) {
	root, _ := sandbox(t)
	st, err := apply(t, root, ent{name: "dev/null", typ: tar.TypeChar, mode: 0o666}, ent{name: "fifo", typ: tar.TypeFifo, mode: 0o644})
	if err != nil {
		t.Fatal(err)
	}
	if st.SkippedSpecial != 2 {
		t.Fatalf("stats %+v", st)
	}
}

func TestTruncatedLayerFails(t *testing.T) {
	root, _ := sandbox(t)
	buf := mkTar(t, file("big", strings.Repeat("x", 4096)))
	if _, err := Apply(root, bytes.NewReader(buf.Bytes()[:1024])); err == nil {
		t.Fatal("truncated layer must fail")
	}
}

func TestCopyTreeIndependent(t *testing.T) {
	base := t.TempDir()
	img := filepath.Join(base, "img")
	os.Mkdir(img, 0o700)
	if _, err := apply(t, img, dir("etc"), file("etc/conf", "orig"), hard("etc/conf2", "etc/conf"), sym("etc/link", "/etc/conf")); err != nil {
		t.Fatal(err)
	}
	c1, c2 := filepath.Join(base, "c1"), filepath.Join(base, "c2")
	st, err := CopyTree(img, c1)
	if err != nil {
		t.Fatal(err)
	}
	if st.Hardlinks != 1 || st.Symlinks != 1 {
		t.Fatalf("copy stats %+v", st)
	}
	if _, err := CopyTree(img, c2); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(c1, "etc/conf"), []byte("changed by c1"), 0o644)
	for _, p := range []string{filepath.Join(img, "etc/conf"), filepath.Join(c2, "etc/conf")} {
		if b, _ := os.ReadFile(p); string(b) != "orig" {
			t.Fatalf("%s changed: %q", p, b)
		}
	}
	// Within one container the hard link is preserved.
	if b, _ := os.ReadFile(filepath.Join(c1, "etc/conf2")); string(b) != "changed by c1" {
		t.Fatalf("hard link not preserved in copy: %q", b)
	}
}

func assertTree(t *testing.T, root string, want ...string) {
	t.Helper()
	var got []string
	filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if p != root {
			rel, _ := filepath.Rel(root, p)
			got = append(got, rel)
		}
		return nil
	})
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tree = %v, want %v", got, want)
	}
}
