package sandbox

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Lord1Egypt/ThothDock/internal/securefs"
)

// fakeGuest builds a guest root with a runtime, an app and the symlink kinds
// a real Debian guest under PRoot has.
func fakeGuest(t *testing.T) *securefs.Root {
	t.Helper()
	g := t.TempDir()
	mk := func(p string) { os.MkdirAll(filepath.Join(g, p), 0o755) }
	wr := func(p, body string, mode os.FileMode) {
		mk(filepath.Dir(p))
		os.WriteFile(filepath.Join(g, p), []byte(body), mode)
	}
	wr("home/thoth/.local/share/flatpak/runtime/rt/files/bin/sh", "#!", 0o755)
	wr("home/thoth/.local/share/flatpak/app/org.example.Tool/files/bin/tool", "#!", 0o755)
	wr("home/thoth/.local/share/flatpak/app/org.example.Tool/files/bin/data.txt", "x", 0o644)
	wr("etc/secret", "guest-only", 0o600)
	// A command that is an absolute symlink into /app (LibreOffice's /app/bin/libreoffice).
	os.Symlink("/app/bin/tool", filepath.Join(g, "home/thoth/.local/share/flatpak/app/org.example.Tool/files/bin/tool-link"))
	mk("home/thoth/.var/app/org.example.Tool")
	// Relative, absolute-guest, escaping, and link2symlink (absolute host path) links.
	os.Symlink(".local/share/flatpak/app", filepath.Join(g, "home/thoth/apps"))
	os.Symlink("/home/thoth/.local/share/flatpak/runtime/rt", filepath.Join(g, "rtlink"))
	os.Symlink("../../../../../../../../etc", filepath.Join(g, "home/thoth/escape"))
	// link2symlink stands in for a hard-linked file with a symlink to an absolute host path.
	os.Symlink(filepath.Join(g, "home/thoth/.local/share/flatpak/runtime/rt/files/bin/sh"), filepath.Join(g, "l2s-sh"))
	r, err := securefs.OpenRoot(g)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func TestResolveGuestStaysInside(t *testing.T) {
	g := fakeGuest(t)
	for p, want := range map[string]string{
		"/home/thoth/apps/org.example.Tool": "home/thoth/.local/share/flatpak/app/org.example.Tool",
		"/rtlink/files":                     "home/thoth/.local/share/flatpak/runtime/rt/files",
		"/home/thoth/escape/secret":         "etc/secret", // ".." stops at the guest root
		"/l2s-sh":                           "home/thoth/.local/share/flatpak/runtime/rt/files/bin/sh",
	} {
		host, _, err := ResolveGuest(g, p)
		if err != nil || host != filepath.Join(g.Path(), want) {
			t.Errorf("%s: got %q %v, want %s", p, host, err, want)
		}
	}
	if _, _, err := ResolveGuest(g, "/nope/x"); err == nil {
		t.Error("missing path resolved")
	}
}

func TestPrepareFlatpakShapedRequest(t *testing.T) {
	g := fakeGuest(t)
	dir := t.TempDir()
	req := &Request{
		Ops: []Op{
			{Kind: OpROBind, Src: "/rtlink/files", Dest: "/usr", FD: -1, Perms: -1},
			{Kind: OpROBind, Src: "/home/thoth/apps/org.example.Tool/files", Dest: "/app", FD: -1, Perms: -1},
			{Kind: OpProc, Dest: "/proc", FD: -1, Perms: -1},
			{Kind: OpDev, Dest: "/dev", FD: -1, Perms: -1},
			{Kind: OpDir, Dest: "/run/user/1000", FD: -1, Perms: 0o700},
			{Kind: OpSymlink, Src: "usr/bin", Dest: "/bin", FD: -1, Perms: -1},
			{Kind: OpFile, Dest: "/etc/passwd", FD: 11, Perms: 0o644},
			{Kind: OpROBindData, Dest: "/.flatpak-info", FD: 12, Perms: -1},
			{Kind: OpBind, Src: "/home/thoth/.var/app/org.example.Tool", Dest: "/home/thoth/.var/app/org.example.Tool", FD: -1, Perms: -1},
			{Kind: OpBind, Src: "/home/thoth/missing", Dest: "/x", FD: -1, Perms: -1, Try: true},
			{Kind: OpRemountRO, Dest: "/usr", FD: -1, Perms: -1},
		},
		Env:     []EnvOp{{Name: "PATH", Value: "/app/bin:/usr/bin"}, {Unset: true, Name: "LD_PRELOAD"}, {Name: "FLATPAK_ID", Value: "org.example.Tool"}},
		BaseEnv: []string{"HOME=/home/thoth", "LD_PRELOAD=/evil.so", "PATH=/bin"},
		Chdir:   "/home/thoth",
		Argv:    []string{"tool", "--flag"},
		Files:   map[int][]byte{11: []byte("thoth:x:1000:1000::/home/thoth:/bin/sh\n"), 12: []byte("[Application]\n")},
	}
	p, err := Prepare(req, g, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.Argv, []string{"/usr/bin/sh", "-c", `exec "$0" "$@"`, "/app/bin/tool", "--flag"}) {
		t.Fatalf("argv %q", p.Argv)
	}
	want := []Bind{
		{g.Path(), g.Path()}, // link2symlink targets resolve
		{filepath.Join(g.Path(), "home/thoth/.local/share/flatpak/runtime/rt/files"), "/usr"},
		{filepath.Join(g.Path(), "home/thoth/.local/share/flatpak/app/org.example.Tool/files"), "/app"},
		{filepath.Join(g.Path(), "home/thoth/.var/app/org.example.Tool"), "/home/thoth/.var/app/org.example.Tool"},
	}
	if !reflect.DeepEqual(p.Binds, want) {
		t.Fatalf("binds\n got %v\nwant %v", p.Binds, want)
	}
	if !reflect.DeepEqual(p.Env, []string{"HOME=/home/thoth", "PATH=/app/bin:/usr/bin", "FLATPAK_ID=org.example.Tool"}) {
		t.Fatalf("env %q", p.Env)
	}
	for f, mode := range map[string]os.FileMode{"etc/passwd": 0o644, ".flatpak-info": 0o400, "run/user/1000": 0o700 | os.ModeDir} {
		fi, err := os.Lstat(filepath.Join(dir, f))
		if err != nil || fi.Mode() != mode {
			t.Errorf("%s: %v %v, want %v", f, fi.Mode(), err, mode)
		}
	}
	if l, _ := os.Readlink(filepath.Join(dir, "bin")); l != "usr/bin" {
		t.Errorf("symlink /bin -> %q", l)
	}
	for _, mp := range []string{"usr", "app", "home/thoth/.var/app/org.example.Tool"} {
		if fi, err := os.Stat(filepath.Join(dir, mp)); err != nil || !fi.IsDir() {
			t.Errorf("mount point %s missing", mp)
		}
	}
	if len(p.Warnings) != 3 { // two read-only binds, one remount-ro
		t.Errorf("warnings %q", p.Warnings)
	}
}

func TestPrepareRefuses(t *testing.T) {
	g := fakeGuest(t)
	base := func(op Op) *Request { return &Request{Ops: []Op{op}, Argv: []string{"/bin/true"}} }
	for _, c := range []struct {
		req  *Request
		want string
	}{
		{base(Op{Kind: OpBind, Src: "/home/thoth/missing", Dest: "/x", FD: -1, Perms: -1}), "bind source"},
		{base(Op{Kind: OpROBindFD, FD: 3, Dest: "/x", Perms: -1}), "cannot be passed"},
		{base(Op{Kind: OpProc, Dest: "/elsewhere", FD: -1, Perms: -1}), "only supported"},
		{base(Op{Kind: OpDir, Dest: "relative", FD: -1, Perms: -1}), "not an absolute path"},
		{base(Op{Kind: OpTmpfs, Dest: "/", FD: -1, Perms: -1}), "sandbox root"},
		{base(Op{Kind: OpFile, Dest: "/f", FD: 9, Perms: -1}), "no contents"},
		{&Request{Argv: []string{"nosuchtool"}}, "command not found"},
		{&Request{Argv: []string{"/bin/true"}, Files: map[int][]byte{3: make([]byte, MaxFileBytes+1)}}, "bytes of file data"},
	} {
		_, err := Prepare(c.req, g, t.TempDir())
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: got %v, want %q", c.req.Ops, err, c.want)
		}
	}
}

// A bind through a symlink that climbs out with ".." lands inside the guest.
func TestEscapingSymlinkBindStaysInGuest(t *testing.T) {
	g := fakeGuest(t)
	p, err := Prepare(&Request{Ops: []Op{{Kind: OpROBind, Src: "/home/thoth/escape", Dest: "/e", FD: -1, Perms: -1}}, Argv: []string{"/bin/true"}}, g, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if p.Binds[1].Source != filepath.Join(g.Path(), "etc") {
		t.Fatalf("bind source %s escaped the guest", p.Binds[1].Source)
	}
}

// flatpak's triggers: "--ro-bind / / --proc /proc --dev /dev --bind D D".
func TestRootBindUsesTheGuestDirectoryAndChangesNothing(t *testing.T) {
	g := fakeGuest(t)
	before := listTree(t, g.Path())
	p, err := Prepare(&Request{Ops: []Op{
		{Kind: OpROBind, Src: "/", Dest: "/", FD: -1, Perms: -1},
		{Kind: OpProc, Dest: "/proc", FD: -1, Perms: -1},
		{Kind: OpDev, Dest: "/dev", FD: -1, Perms: -1},
		{Kind: OpBind, Src: "/home/thoth/.var", Dest: "/home/thoth/.var", FD: -1, Perms: -1},
	}, Argv: []string{"/home/thoth/.local/share/flatpak/runtime/rt/files/bin/sh"}}, g, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if p.Root != g.Path() || len(p.Binds) != 1 || p.Binds[0].Target != "/home/thoth/.var" {
		t.Fatalf("root %s binds %v", p.Root, p.Binds)
	}
	if after := listTree(t, g.Path()); !reflect.DeepEqual(before, after) {
		t.Fatalf("the guest changed:\n%v\n%v", before, after)
	}
	for _, op := range []Op{
		{Kind: OpDir, Dest: "/brand-new", FD: -1, Perms: -1},
		{Kind: OpFile, Dest: "/etc/passwd", FD: 3, Perms: -1},
		{Kind: OpSymlink, Src: "x", Dest: "/y", FD: -1, Perms: -1},
		{Kind: OpBind, Src: "/etc", Dest: "/not-there", FD: -1, Perms: -1},
	} {
		_, err := Prepare(&Request{Ops: []Op{{Kind: OpROBind, Src: "/", Dest: "/", FD: -1, Perms: -1}, op},
			Argv: []string{"/bin/true"}, Files: map[int][]byte{3: []byte("x")}}, g, t.TempDir())
		if err == nil {
			t.Errorf("%+v accepted in a guest-rooted sandbox", op)
		}
	}
	if after := listTree(t, g.Path()); !reflect.DeepEqual(before, after) {
		t.Fatal("a refused operation changed the guest")
	}
}

func listTree(t *testing.T, dir string) []string {
	var out []string
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil {
			out = append(out, strings.TrimPrefix(p, dir)+" "+fi.Mode().String())
		}
		return nil
	})
	return out
}

// Flatpak asks for GPU, sound and input nodes with --dev-bind /dev/X /dev/X.
// They are not in the guest's file tree; the runtime's /dev is the sandbox's.
func TestDeviceNodesUnderDevAreNotBoundFromTheGuest(t *testing.T) {
	g := fakeGuest(t)
	for _, dev := range []string{"/dev/dri", "/dev/snd", "/dev/input", "/dev/kvm"} {
		p, err := Prepare(&Request{
			Ops:  []Op{{Kind: OpDevBind, Src: dev, Dest: dev, FD: -1, Perms: -1}},
			Argv: []string{"/home/thoth/.local/share/flatpak/app/org.example.Tool/files/bin/tool"},
		}, g, t.TempDir())
		if err != nil {
			t.Fatalf("%s: %v", dev, err)
		}
		for _, b := range p.Binds {
			if b.Target == dev {
				t.Errorf("%s was bound: %+v", dev, b)
			}
		}
	}
	// Only the device tree is special: a plain bind of a missing path still fails.
	_, err := Prepare(&Request{
		Ops:  []Op{{Kind: OpBind, Src: "/dev/dri", Dest: "/dev/dri", FD: -1, Perms: -1}},
		Argv: []string{"/bin/true"},
	}, g, t.TempDir())
	if err == nil {
		t.Error("a --bind of a missing /dev path was accepted")
	}
}

// /app/bin/libreoffice is a symlink to /app/libreoffice/program/soffice: the
// target names a path in the /app bind, which the guest alone cannot follow.
func TestPrepareFollowsACommandSymlinkIntoAnotherBind(t *testing.T) {
	g := fakeGuest(t)
	req := &Request{
		Ops: []Op{
			{Kind: OpROBind, Src: "/rtlink/files", Dest: "/usr", FD: -1, Perms: -1},
			{Kind: OpROBind, Src: "/home/thoth/apps/org.example.Tool/files", Dest: "/app", FD: -1, Perms: -1},
		},
		Env:  []EnvOp{{Name: "PATH", Value: "/app/bin:/usr/bin"}},
		Argv: []string{"tool-link", "x"},
	}
	p, err := Prepare(req, g, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Argv[len(p.Argv)-2:]; !reflect.DeepEqual(got, []string{"/app/bin/tool-link", "x"}) {
		t.Fatalf("argv %q", p.Argv)
	}
	req.Argv = []string{"missing-tool"}
	if _, err := Prepare(req, g, t.TempDir()); err == nil {
		t.Fatal("a command that is nowhere was found")
	}
}

// A sandbox that is not a Flatpak one (no /app) keeps its command as it is.
func TestPrepareRunsOnlyAppsThroughTheShell(t *testing.T) {
	g := fakeGuest(t)
	req := &Request{
		Ops:  []Op{{Kind: OpROBind, Src: "/rtlink/files", Dest: "/usr", FD: -1, Perms: -1}},
		Env:  []EnvOp{{Name: "PATH", Value: "/usr/bin"}},
		Argv: []string{"/usr/bin/sh", "-c", "true"},
	}
	p, err := Prepare(req, g, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.Argv, []string{"/usr/bin/sh", "-c", "true"}) {
		t.Fatalf("argv %q", p.Argv)
	}
}
