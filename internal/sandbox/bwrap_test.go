package sandbox

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func noArgs(int) ([]string, error) { return nil, errors.New("unexpected --args") }

// A command line shaped like the ones flatpak 1.16 passes: most options in an
// --args descriptor, the command on the real argv.
func TestFlatpakShapedCommandLine(t *testing.T) {
	inner := []string{
		"--unshare-ipc", "--unshare-pid", "--die-with-parent", "--new-session",
		"--ro-bind", "/home/thoth/.local/share/flatpak/runtime/org.freedesktop.Platform/aarch64/24.08/active/files", "/usr",
		"--ro-bind", "/home/thoth/.local/share/flatpak/app/org.example.Tool/aarch64/stable/active/files", "/app",
		"--proc", "/proc", "--dev", "/dev",
		"--dir", "/tmp", "--perms", "0700", "--dir", "/run/user/1000",
		"--symlink", "usr/lib", "/lib",
		"--perms", "0644", "--file", "11", "/etc/passwd",
		"--ro-bind-data", "12", "/.flatpak-info",
		"--setenv", "FLATPAK_ID", "org.example.Tool", "--unsetenv", "LD_PRELOAD",
		"--chdir", "/home/thoth",
		"--info-fd", "13", "--sync-fd", "14", "--seccomp", "15",
		"--bind-try", "/home/thoth/.var/app/org.example.Tool", "/home/thoth/.var/app/org.example.Tool",
	}
	s, err := Parse([]string{"--args", "10", "--", "/app/bin/tool", "--flag", "-x"}, func(fd int) ([]string, error) {
		if fd != 10 {
			t.Fatalf("read fd %d", fd)
		}
		return inner, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.Argv, []string{"/app/bin/tool", "--flag", "-x"}) {
		t.Fatalf("argv %q", s.Argv)
	}
	if s.InfoFD != 13 || s.SyncFD != 14 || s.BlockFD != -1 || !reflect.DeepEqual(s.SeccompFDs, []int{15}) {
		t.Fatalf("fds %+v", s)
	}
	if !s.DieWithParent || !s.NewSession || s.Chdir != "/home/thoth" {
		t.Fatalf("flags %+v", s)
	}
	want := []Op{
		{Kind: OpROBind, Src: inner[5], Dest: "/usr", FD: -1, Perms: -1},
		{Kind: OpROBind, Src: inner[8], Dest: "/app", FD: -1, Perms: -1},
		{Kind: OpProc, Dest: "/proc", FD: -1, Perms: -1},
		{Kind: OpDev, Dest: "/dev", FD: -1, Perms: -1},
		{Kind: OpDir, Dest: "/tmp", FD: -1, Perms: -1},
		{Kind: OpDir, Dest: "/run/user/1000", FD: -1, Perms: 0o700},
		{Kind: OpSymlink, Src: "usr/lib", Dest: "/lib", FD: -1, Perms: -1},
		{Kind: OpFile, Dest: "/etc/passwd", FD: 11, Perms: 0o644},
		{Kind: OpROBindData, Dest: "/.flatpak-info", FD: 12, Perms: -1},
		{Kind: OpBind, Src: inner[len(inner)-2], Dest: inner[len(inner)-1], FD: -1, Perms: -1, Try: true},
	}
	if !reflect.DeepEqual(s.Ops, want) {
		t.Fatalf("ops\n got %+v\nwant %+v", s.Ops, want)
	}
	if !reflect.DeepEqual(s.Env, []EnvOp{{Name: "FLATPAK_ID", Value: "org.example.Tool"}, {Unset: true, Name: "LD_PRELOAD"}}) {
		t.Fatalf("env %+v", s.Env)
	}
	if !reflect.DeepEqual(s.Ignored, []string{"--unshare-ipc", "--unshare-pid", "--seccomp 15"}) {
		t.Fatalf("ignored %q", s.Ignored)
	}
}

func TestCommandWithoutDoubleDash(t *testing.T) {
	s, err := Parse([]string{"--ro-bind", "/", "/", "sh", "-c", "--bind not an option"}, noArgs)
	if err != nil || !reflect.DeepEqual(s.Argv, []string{"sh", "-c", "--bind not an option"}) {
		t.Fatalf("%+v %v", s, err)
	}
}

func TestTmpfsTakesSizeAndPerms(t *testing.T) {
	s, err := Parse([]string{"--perms", "1777", "--size", "4096", "--tmpfs", "/tmp", "true"}, noArgs)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Ops[0]; got.Kind != OpTmpfs || got.Perms != 0o1777 || got.Size != 4096 {
		t.Fatalf("%+v", got)
	}
}

func TestRefused(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--bind", "/a"}, "needs 2"},
		{[]string{"--frobnicate", "true"}, "unknown option"},
		{[]string{"--info-fd", "x", "true"}, "not a file descriptor"},
		{[]string{"--info-fd", "-1", "true"}, "not a file descriptor"},
		{[]string{"--perms", "999", "--dir", "/d", "true"}, "not an octal mode"},
		{[]string{"--perms", "0700", "--setenv", "A", "B", "true"}, "--perms must be followed"},
		{[]string{"--size", "10", "--dir", "/d", "true"}, "--size must be followed"},
		{[]string{"--perms", "0700"}, "must be followed"},
		{[]string{"--perms", "0700", "--size", "10", "--dir", "/d", "true"}, "--size must be followed"},
		{[]string{"--ro-bind", "/", "/"}, "no command"},
		{[]string{"--args", "3", "true"}, "unexpected --args"},
	} {
		_, err := Parse(c.args, noArgs)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: got %v, want %q", c.args, err, c.want)
		}
	}
}

func TestArgsCannotRecurseForever(t *testing.T) {
	_, err := Parse([]string{"--args", "3", "true"}, func(int) ([]string, error) { return []string{"--args", "3"}, nil })
	if err == nil || !strings.Contains(err.Error(), "nested too deeply") {
		t.Fatalf("got %v", err)
	}
}

func TestCommandInsideArgsIsRefused(t *testing.T) {
	_, err := Parse([]string{"--args", "3"}, func(int) ([]string, error) { return []string{"--dir", "/d", "true"}, nil })
	if err == nil || !strings.Contains(err.Error(), "must follow the options") {
		t.Fatalf("got %v", err)
	}
}

// The table holds exactly the options of bubblewrap 0.12.0 (`bwrap --help` on
// Debian trixie), except --help and --version, which the shim answers itself.
func TestOptionTableMatchesBwrap(t *testing.T) {
	documented := strings.Fields(`--add-seccomp-fd --args --argv0 --as-pid-1 --assert-userns-disabled
	--bind --bind-data --bind-fd --bind-try --block-fd --cap-add --cap-drop --chdir --chmod --clearenv
	--dev --dev-bind --dev-bind-try --die-with-parent --dir --disable-userns --exec-label --file
	--file-label --gid --hostname --info-fd --json-status-fd --level-prefix --lock-file --mqueue
	--new-session --not-a-security-boundary --overlay --overlay-src --perms --pidns --proc --remount-ro
	--ro-bind --ro-bind-data --ro-bind-fd --ro-bind-try --ro-overlay --seccomp --setenv --share-net
	--size --symlink --sync-fd --tmp-overlay --tmpfs --uid --unsetenv --unshare-all --unshare-cgroup
	--unshare-cgroup-try --unshare-ipc --unshare-net --unshare-pid --unshare-user --unshare-user-try
	--unshare-uts --userns --userns-block-fd --userns2`)
	for _, o := range documented {
		if _, ok := arity[o]; !ok {
			t.Errorf("%s missing", o)
		}
	}
	if len(arity) != len(documented) {
		t.Errorf("table has %d options, bwrap 0.12 documents %d", len(arity), len(documented))
	}
}

func TestOverlaysAreRefused(t *testing.T) {
	_, err := Parse([]string{"--overlay-src", "/a", "--tmp-overlay", "/b", "true"}, noArgs)
	if err == nil || !strings.Contains(err.Error(), "overlayfs") {
		t.Fatalf("got %v", err)
	}
}
