package api

import (
	"bytes"
	"context"
	"encoding/json"
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

// A guest with one Flatpak app (a link2symlink entry included) and a runtime.
func flatpakGuest(t *testing.T) string {
	g := sandboxGuest(t)
	fp := filepath.Join(g, "home/thoth/.local/share/flatpak")
	dep := filepath.Join(fp, "app/org.example.Tool/aarch64/stable/abc123")
	os.MkdirAll(filepath.Join(dep, "files/share/applications"), 0o755)
	os.MkdirAll(filepath.Join(dep, "files/bin"), 0o755)
	os.WriteFile(filepath.Join(dep, "metadata"), []byte("[Application]\nname=org.example.Tool\nruntime=org.freedesktop.Platform/aarch64/24.08\n"), 0o644)
	os.WriteFile(filepath.Join(dep, "files/share/applications/org.example.Tool.desktop"), []byte("[Desktop Entry]\nName=Example Tool\nName[ar]=أداة\n"), 0o644)
	os.WriteFile(filepath.Join(dep, "files/bin/tool"), make([]byte, 1000), 0o755)
	obj := filepath.Join(fp, "repo/objects/e6/.l2s.deadbeef.file0001")
	os.MkdirAll(filepath.Dir(obj), 0o755)
	os.WriteFile(obj, make([]byte, 4000), 0o644)
	os.Symlink(obj, filepath.Join(dep, "files/bin/hardlinked"))
	os.Symlink("abc123", filepath.Join(fp, "app/org.example.Tool/aarch64/stable/active"))
	rt := filepath.Join(fp, "runtime/org.freedesktop.Platform/aarch64/24.08/r1")
	os.MkdirAll(rt, 0o755)
	os.Symlink("r1", filepath.Join(fp, "runtime/org.freedesktop.Platform/aarch64/24.08/active"))
	return g
}

func TestAppsListsFlatpakAppsAndStopsTheirSandboxes(t *testing.T) {
	f := newAPIWith(t, engine.Config{GuestRoot: flatpakGuest(t)})
	f.rt.Program("/usr/bin/hello", func(ctx context.Context, s runtime.Spec, in io.Reader) int {
		<-ctx.Done()
		return 137
	})
	req := helloRequest()
	req.Env = append(req.Env, sandbox.EnvOp{Name: "FLATPAK_ID", Value: "org.example.Tool"})
	started := make(chan sandbox.Started, 1)
	done := make(chan int, 1)
	go func() {
		code, _ := sandbox.Run(dialAPI(t, f), req, nil, io.Discard, io.Discard, func(s sandbox.Started) { started <- s })
		done <- code
	}()
	<-started

	resp, body := f.do(t, "GET", "/thothdock/apps", "")
	var view engine.AppsView
	if resp.StatusCode != 200 || json.Unmarshal([]byte(body), &view) != nil {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if len(view.Apps) != 1 || view.Runtimes != 1 || len(view.Sandboxes) != 1 {
		t.Fatalf("view %+v", view)
	}
	a := view.Apps[0]
	if a.ID != "org.example.Tool" || a.Name != "Example Tool" || a.Branch != "stable" || a.Arch != "aarch64" ||
		a.Installation != "user:thoth" || a.Runtime != "org.freedesktop.Platform/aarch64/24.08" || a.SizeBytes < 5000 || a.SizeBytes > 7000 {
		t.Fatalf("app %+v", a)
	}
	if len(a.Running) != 1 || a.Running[0].AppID != "org.example.Tool" {
		t.Fatalf("running %+v", a.Running)
	}
	resp, body = f.do(t, "POST", "/thothdock/sandboxes/"+a.Running[0].ID+"/kill", "")
	if resp.StatusCode != 204 {
		t.Fatalf("kill %d %s", resp.StatusCode, body)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("sandbox not stopped")
	}
	resp, _ = f.do(t, "POST", "/thothdock/sandboxes/nope/kill", "")
	if resp.StatusCode != 404 {
		t.Fatalf("unknown sandbox: %d", resp.StatusCode)
	}
}

func TestAppsListsSnapsAndTheirServices(t *testing.T) {
	g := sandboxGuest(t)
	os.MkdirAll(filepath.Join(g, "var/lib/thothdock-snaps"), 0o755)
	os.WriteFile(filepath.Join(g, "var/lib/thothdock-snaps/state.json"), []byte(`{"snaps":{
		"core22":{"revision":2988,"version":"20260909","channel":"latest/stable","type":"base"},
		"web-srv":{"revision":7,"version":"1.0","channel":"latest/stable","type":"app","base":"core22","confinement":"strict"},
		"../bad":{"revision":1,"type":"app"}}}`), 0o644)
	os.MkdirAll(filepath.Join(g, "snap/web-srv/7/bin"), 0o755)
	os.WriteFile(filepath.Join(g, "snap/web-srv/7/bin/serve"), make([]byte, 2048), 0o755)
	f := newAPIWith(t, engine.Config{GuestRoot: g})
	f.rt.Program("/usr/bin/hello", func(ctx context.Context, s runtime.Spec, in io.Reader) int { <-ctx.Done(); return 0 })
	req := helloRequest()
	req.BaseEnv = []string{"THOTHDOCK_SNAP_APP=web-srv.serve"}
	started := make(chan struct{})
	go sandbox.Run(dialAPI(t, f), req, nil, io.Discard, io.Discard, func(sandbox.Started) { close(started) })
	<-started
	_, body := f.do(t, "GET", "/thothdock/apps", "")
	var view engine.AppsView
	if err := json.Unmarshal([]byte(body), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Apps) != 1 || view.Runtimes != 1 {
		t.Fatalf("view %+v", view)
	}
	a := view.Apps[0]
	if a.Kind != "snap" || a.ID != "web-srv" || a.Runtime != "core22" || a.SizeBytes != 2048 || len(a.Running) != 1 || a.Running[0].AppID != "snap:web-srv.serve" {
		t.Fatalf("snap %+v", a)
	}
}

// A snap's sandboxes share one loopback address, as its processes share
// localhost under snapd; another snap, and a Flatpak app, get their own.
func TestSnapSandboxesShareOneAddress(t *testing.T) {
	g := sandboxGuest(t)
	f := newAPIWith(t, engine.Config{GuestRoot: g})
	f.rt.Program("/usr/bin/hello", func(ctx context.Context, s runtime.Spec, in io.Reader) int { <-ctx.Done(); return 0 })
	run := func(env ...string) {
		req := helloRequest()
		req.BaseEnv = env
		started := make(chan struct{})
		go sandbox.Run(dialAPI(t, f), req, nil, io.Discard, io.Discard, func(sandbox.Started) { close(started) })
		<-started
	}
	run("THOTHDOCK_SNAP_APP=redis.server")
	run("THOTHDOCK_SNAP_APP=redis.cli")
	run("THOTHDOCK_SNAP_APP=mosquitto.mosquitto")
	run("FLATPAK_ID=org.example.App")
	byApp := map[string][]string{}
	for _, sb := range f.engineOf().Sandboxes() {
		byApp[sb.AppID] = append(byApp[sb.AppID], sb.NetIP)
	}
	server, cli := byApp["snap:redis.server"][0], byApp["snap:redis.cli"][0]
	if server == "" || server != cli {
		t.Fatalf("redis.server %q and redis.cli %q must share an address", server, cli)
	}
	seen := map[string]string{server: "redis"}
	for app, ips := range byApp {
		if strings.HasPrefix(app, "snap:redis.") {
			continue
		}
		if prev, dup := seen[ips[0]]; dup || ips[0] == "" {
			t.Errorf("%s has %q, also used by %s", app, ips[0], prev)
		}
		seen[ips[0]] = app
	}
}
