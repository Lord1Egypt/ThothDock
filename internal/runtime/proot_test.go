package runtime

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The PRoot tests run a real proot against the host's own root filesystem
// as the guest. Set THOTHDOCK_TEST_PROOT to a proot binary
// (scripts/build-host-proot.sh builds one).
func testPRoot(t *testing.T) *PRootRuntime {
	path := os.Getenv("THOTHDOCK_TEST_PROOT")
	if path == "" {
		t.Skip("THOTHDOCK_TEST_PROOT not set")
	}
	r, err := NewPRoot(PRootConfig{Path: path, TmpDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuf) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func run(t *testing.T, r Runtime, spec Spec) (string, string, Exit) {
	var out, errb syncBuf
	spec.Stdout, spec.Stderr = &out, &errb
	if spec.Rootfs == "" {
		spec.Rootfs = "/"
	}
	p, err := r.Start(spec)
	if err != nil {
		t.Fatal(err)
	}
	ex := p.Wait()
	return out.String(), errb.String(), ex
}

func TestPRootCapturesStdoutStderrExit(t *testing.T) {
	r := testPRoot(t)
	out, errOut, ex := run(t, r, Spec{Args: []string{"/bin/sh", "-c", "echo out; echo err >&2; exit 3"}, Env: []string{"PATH=/usr/bin:/bin"}})
	if out != "out\n" || errOut != "err\n" || ex.Code != 3 {
		t.Fatalf("out=%q err=%q exit=%+v", out, errOut, ex)
	}
}

func TestPRootEnvAndCwd(t *testing.T) {
	r := testPRoot(t)
	out, _, ex := run(t, r, Spec{Args: []string{"/bin/sh", "-c", `echo "$FOO $PWD"; env | grep -c ^PROOT_LOADER= || true`}, Env: []string{"FOO=bar", "PATH=/bin:/usr/bin"}, Cwd: "/tmp"})
	if ex.Code != 0 || out != "bar /tmp\n0\n" {
		t.Fatalf("out=%q exit=%+v", out, ex)
	}
}

func TestPRootSignalExitCodes(t *testing.T) {
	r := testPRoot(t)
	for _, tc := range []struct {
		sig  syscall.Signal
		want int
	}{{syscall.SIGTERM, 143}, {syscall.SIGINT, 130}} {
		var out, errb syncBuf
		p, err := r.Start(Spec{Rootfs: "/", Args: []string{"/bin/sleep", "30"}, Stdout: &out, Stderr: &errb})
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(300 * time.Millisecond)
		if err := p.Signal(tc.sig); err != nil {
			t.Fatal(err)
		}
		ex := p.Wait()
		if ex.Code != tc.want || strings.Contains(errb.String(), "proot info") {
			t.Fatalf("%v: exit=%+v stderr=%q", tc.sig, ex, errb.String())
		}
	}
}

// A shell loop killed by SIGTERM reported 0 in the golden build: PRoot's exit
// status was the stale 0 of the last "sleep" child.
func TestPRootSignalledShellLoopExits143(t *testing.T) {
	r := testPRoot(t)
	for i := 0; i < 3; i++ {
		var out, errb syncBuf
		p, err := r.Start(Spec{Rootfs: "/", Args: []string{"/bin/sh", "-c", "while true; do sleep 1; done"}, Stdout: &out, Stderr: &errb})
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(1500 * time.Millisecond)
		p.Signal(syscall.SIGTERM)
		if ex := p.Wait(); ex.Code != 143 || strings.Contains(errb.String(), "proot info") {
			t.Fatalf("exit=%+v stderr=%q", ex, errb.String())
		}
	}
	// A workload that traps TERM and exits 0 on its own still reports 0.
	var out, errb syncBuf
	p, err := r.Start(Spec{Rootfs: "/", Args: []string{"/bin/sh", "-c", "trap 'exit 0' TERM; while true; do sleep 1; done"}, Stdout: &out, Stderr: &errb})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	p.Signal(syscall.SIGTERM)
	if ex := p.Wait(); ex.Code != 0 {
		t.Fatalf("trapped exit=%+v", ex)
	}
}

func TestPRootKillTakesTheWholeTree(t *testing.T) {
	r := testPRoot(t)
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "child.pid")
	var out, errb syncBuf
	// The workload ignores SIGTERM and starts a background child.
	p, err := r.Start(Spec{Rootfs: "/", Args: []string{"/bin/sh", "-c", "trap '' TERM; sleep 60 & echo $! > " + pidfile + "; wait"}, Stdout: &out, Stderr: &errb})
	if err != nil {
		t.Fatal(err)
	}
	var childPid int
	for i := 0; i < 50 && childPid == 0; i++ {
		time.Sleep(100 * time.Millisecond)
		b, _ := os.ReadFile(pidfile)
		childPid = atoiTrim(string(b))
	}
	if childPid == 0 {
		t.Fatal("child never started")
	}
	start := time.Now()
	p.Kill()
	ex := p.Wait()
	if ex.Code != 137 || time.Since(start) > 2*time.Second {
		t.Fatalf("exit=%+v after %v", ex, time.Since(start))
	}
	// The child is gone (no orphan survives proot).
	time.Sleep(200 * time.Millisecond)
	if b, err := os.ReadFile("/proc/" + strconv.Itoa(childPid) + "/stat"); err == nil && !strings.Contains(string(b), ") Z ") {
		t.Fatalf("background child %d survived: %s", childPid, b)
	}
}

func TestPRootStdin(t *testing.T) {
	r := testPRoot(t)
	var out, errb syncBuf
	p, err := r.Start(Spec{Rootfs: "/", Args: []string{"/bin/cat"}, OpenStdin: true, Stdout: &out, Stderr: &errb})
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(p.Stdin(), "piped\n")
	p.Stdin().Close()
	if ex := p.Wait(); ex.Code != 0 || out.String() != "piped\n" {
		t.Fatalf("exit=%+v out=%q", ex, out.String())
	}
}

func TestPRootTTY(t *testing.T) {
	r := testPRoot(t)
	var out syncBuf
	p, err := r.Start(Spec{Rootfs: "/", Args: []string{"/bin/sh", "-c", "test -t 0 && test -t 1 && sleep 0.5 && stty size"}, Tty: true, OpenStdin: true, Stdout: &out})
	if err != nil {
		t.Fatal(err)
	}
	p.Resize(100, 40)
	ex := p.Wait()
	if ex.Code != 0 || !strings.Contains(out.String(), "40 100") {
		t.Fatalf("exit=%+v out=%q", ex, out.String())
	}
}

func atoiTrim(s string) int {
	n := 0
	for _, c := range strings.TrimSpace(s) {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func TestFakeRuntime(t *testing.T) {
	f := NewFake()
	out, _, ex := run(t, f, Spec{Args: []string{"/missing"}})
	if ex.Code != 127 || out != "" {
		t.Fatalf("%+v", ex)
	}
}

func TestPRootOptionCheck(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "proot")
	// An upstream-style PRoot without --kill-on-exit is refused at startup.
	os.WriteFile(fake, []byte("#!/bin/sh\necho \"  -r *path*, --rootfs=*path*\n  -0, --root-id\n  -i *string*, --change-id=*string*\n  --link2symlink\"\n"), 0o755)
	if _, err := NewPRoot(PRootConfig{Path: fake, TmpDir: dir}); err == nil || !strings.Contains(err.Error(), "lacks --kill-on-exit") {
		t.Fatalf("want missing-option error, got %v", err)
	}
	os.WriteFile(fake, []byte("#!/bin/sh\necho \"--kill-on-exit --root-id --change-id --link2symlink\"\n"), 0o755)
	if _, err := NewPRoot(PRootConfig{Path: fake, TmpDir: dir}); err != nil {
		t.Fatal(err)
	}
}
