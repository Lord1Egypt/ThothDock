package api

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Lord1Egypt/ThothDock/internal/engine"
	"github.com/Lord1Egypt/ThothDock/internal/runtime"
	"github.com/Lord1Egypt/ThothDock/internal/sandbox"
)

func sandboxGuest(t *testing.T) string {
	g := t.TempDir()
	os.MkdirAll(filepath.Join(g, "usr/bin"), 0o755)
	os.WriteFile(filepath.Join(g, "usr/bin/hello"), []byte("#!"), 0o755)
	return g
}

func dialAPI(t *testing.T, f *apiFixture) net.Conn {
	c, err := net.Dial("tcp", strings.TrimPrefix(f.srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func helloRequest() *sandbox.Request {
	return &sandbox.Request{
		Ops:  []sandbox.Op{{Kind: sandbox.OpROBind, Src: "/usr", Dest: "/usr", FD: -1, Perms: -1}},
		Env:  []sandbox.EnvOp{{Name: "PATH", Value: "/usr/bin"}},
		Argv: []string{"hello", "world"},
		UID:  1000, GID: 1000,
	}
}

func TestSandboxRunsOverTheSocket(t *testing.T) {
	g := sandboxGuest(t)
	f := newAPIWith(t, engine.Config{GuestRoot: g})
	var seen runtime.Spec
	f.rt.Program("/usr/bin/hello", func(ctx context.Context, s runtime.Spec, in io.Reader) int {
		seen = s
		fmt.Fprintf(s.Stdout, "hi %s\n", s.Args[1])
		b, _ := io.ReadAll(in)
		fmt.Fprintf(s.Stderr, "stdin=%q", b)
		return 7
	})
	var out, errOut bytes.Buffer
	var started sandbox.Started
	code, err := sandbox.Run(dialAPI(t, f), helloRequest(), strings.NewReader("ping"), &out, &errOut,
		func(s sandbox.Started) { started = s })
	if err != nil {
		t.Fatal(err)
	}
	if code != 7 || out.String() != "hi world\n" || errOut.String() != `stdin="ping"` {
		t.Fatalf("code %d out %q err %q", code, out.String(), errOut.String())
	}
	if started.Pid <= 0 || len(started.Warnings) != 1 || !strings.Contains(started.Warnings[0], "read-only") {
		t.Fatalf("started %+v", started)
	}
	if seen.Args[0] != "/usr/bin/hello" || seen.UID != 1000 || len(seen.Binds) != 2 || seen.Binds[1].Source != filepath.Join(g, "usr") {
		t.Fatalf("spec %+v", seen)
	}
	waitFor(t, func() bool { return len(f.engineOf().Sandboxes()) == 0 })
	if left, _ := os.ReadDir(filepath.Join(f.engineOf().Layout.Root, "sandboxes")); len(left) != 0 {
		t.Fatalf("sandbox roots left: %v", left)
	}
}

func TestSandboxDiesWithItsClient(t *testing.T) {
	f := newAPIWith(t, engine.Config{GuestRoot: sandboxGuest(t)})
	ended := make(chan struct{})
	f.rt.Program("/usr/bin/hello", func(ctx context.Context, s runtime.Spec, in io.Reader) int {
		<-ctx.Done()
		close(ended)
		return 137
	})
	req := helloRequest()
	req.DieWithParent = true
	conn := dialAPI(t, f)
	go sandbox.Run(conn, req, nil, io.Discard, io.Discard, func(sandbox.Started) { conn.Close() })
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		t.Fatal("sandbox outlived its client")
	}
	waitFor(t, func() bool { return len(f.engineOf().Sandboxes()) == 0 })
}

func TestSandboxNeedsTheGuestRoot(t *testing.T) {
	f := newAPI(t)
	_, err := sandbox.Run(dialAPI(t, f), helloRequest(), nil, io.Discard, io.Discard, nil)
	if err == nil || !strings.Contains(err.Error(), "--guest-root") {
		t.Fatalf("got %v", err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}
