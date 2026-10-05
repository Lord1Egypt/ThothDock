package main

import (
	"fmt"
	"github.com/Lord1Egypt/ThothDock/internal/platform"
	"github.com/Lord1Egypt/ThothDock/internal/procid"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestListenUnixOwnerOnlyAndStaleReplaced(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sock")
	os.Mkdir(dir, 0o755)
	sock := filepath.Join(dir, "d.sock")
	// A stale socket from a crashed daemon.
	stale, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	stale.Close()
	ln, fi, err := listenUnix(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode %v", fi.Mode())
	}
	if d, _ := os.Stat(dir); d.Mode().Perm() != 0o700 {
		t.Fatalf("directory not tightened: %v", d.Mode())
	}
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	removeOwnSocket(sock, fi)
	if _, err := os.Lstat(sock); !os.IsNotExist(err) {
		t.Fatal("own socket not removed")
	}
}

func TestListenUnixRefusesSymlinkDirAndSymlinkSocketPath(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	os.Mkdir(real, 0o700)
	link := filepath.Join(base, "link")
	os.Symlink(real, link)
	if _, _, err := listenUnix(filepath.Join(link, "d.sock")); err == nil {
		t.Fatal("symlinked socket directory accepted")
	}
	// A symlink planted at the socket path is refused and its target untouched.
	victim := filepath.Join(base, "victim")
	os.WriteFile(victim, []byte("keep"), 0o600)
	sock := filepath.Join(real, "d.sock")
	os.Symlink(victim, sock)
	if _, _, err := listenUnix(sock); err == nil {
		t.Fatal("symlink at the socket path accepted")
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Fatal("symlink target modified")
	}
	if es, _ := os.ReadDir(real); len(es) != 1 {
		t.Fatalf("temporary socket left behind: %v", es)
	}
	os.Remove(sock)
	ln, fi, err := listenUnix(sock)
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	// Someone replaced our socket after start: shutdown leaves theirs alone.
	os.Remove(sock)
	os.WriteFile(sock, []byte("other"), 0o600)
	removeOwnSocket(sock, fi)
	if _, err := os.Lstat(sock); err != nil {
		t.Fatal("removed a file that was not our socket")
	}
}

func TestRootLockAllowsOneDaemon(t *testing.T) {
	l := platform.Layout{Root: t.TempDir()}
	if err := l.Ensure(); err != nil {
		t.Fatal(err)
	}
	first, err := lockRoot(l)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := lockRoot(l); err == nil {
		second.Close()
		t.Fatal("a second daemon got the lock")
	}
	first.Close()
	again, err := lockRoot(l)
	if err != nil {
		t.Fatalf("lock not released with the holder: %v", err)
	}
	again.Close()
}

func TestPidFileRecordsIdentity(t *testing.T) {
	l := platform.Layout{Root: t.TempDir()}
	l.Ensure()
	if err := writePidFile(l); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(pidFile(l))
	var pid int
	var start uint64
	if _, err := fmt.Sscanf(string(b), "%d %d", &pid, &start); err != nil || pid != os.Getpid() || !procid.Alive(pid, start) {
		t.Fatalf("pid file %q", b)
	}
}
