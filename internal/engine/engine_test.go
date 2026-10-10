package engine

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Lord1Egypt/ThothDock/internal/errdefs"
	"github.com/Lord1Egypt/ThothDock/internal/image"
	"github.com/Lord1Egypt/ThothDock/internal/logs"
	"github.com/Lord1Egypt/ThothDock/internal/oci"
	"github.com/Lord1Egypt/ThothDock/internal/platform"
	"github.com/Lord1Egypt/ThothDock/internal/registry"
	"github.com/Lord1Egypt/ThothDock/internal/registry/registrytest"
	"github.com/Lord1Egypt/ThothDock/internal/runtime"
	"github.com/Lord1Egypt/ThothDock/internal/store"
)

type fixture struct {
	t      *testing.T
	layout platform.Layout
	reg    *registrytest.Registry
	rt     *runtime.FakeRuntime
	e      *Engine
	image  string
	cfg    Config
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func newFixture(t *testing.T) *fixture {
	f := &fixture{t: t, layout: platform.Layout{Root: t.TempDir()}, reg: registrytest.New(t), rt: runtime.NewFake()}
	if err := f.layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	f.reg.Image(t, "library/tiny", "1", oci.HostPlatform(), oci.ContainerConfig{Cmd: []string{"sh"}, Env: []string{"PATH=/bin"}}, true,
		[]registrytest.File{
			{Name: "bin/", Type: tar.TypeDir}, {Name: "bin/sh", Body: "#!", Mode: 0o755}, {Name: "bin/noexec", Body: "x", Mode: 0o644}, {Name: "bin/echo", Body: "#!", Mode: 0o755},
			{Name: "etc/", Type: tar.TypeDir}, {Name: "etc/passwd", Body: "root:x:0:0:root:/root:/bin/sh\nnobody:x:65534:65534:nobody:/nonexistent:/bin/false\n"},
			{Name: "data", Body: "original"},
		})
	f.image = f.reg.Host() + "/library/tiny:1"
	f.open()
	if _, _, err := f.e.Puller.Pull(context.Background(), f.image, image.PullOptions{Platform: oci.HostPlatform()}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) open() {
	blobs := store.NewBlobs(f.layout.Blobs(), f.layout.Tmp())
	is, err := image.Open(f.layout.Images(), filepath.Join(f.layout.Root, "refs.json"), f.layout.Tmp(), blobs, quiet)
	if err != nil {
		f.t.Fatal(err)
	}
	p := &image.Puller{Store: is, Client: registry.NewClient(f.reg.Server.Client(), "t")}
	e, err := New(f.layout, is, p, f.rt, f.cfg, quiet)
	if err != nil {
		f.t.Fatal(err)
	}
	f.e = e
}

func (f *fixture) create(cmd ...string) string {
	f.t.Helper()
	id, _, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image, Cmd: cmd}}, "", "")
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

func waitFor(t *testing.T, e *Engine, id, cond string) WaitResult {
	t.Helper()
	ch, _, err := e.Wait(id, cond)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("wait timed out")
	}
	return WaitResult{}
}

func TestLifecycleCreateStartExitLogsRemove(t *testing.T) {
	f := newFixture(t)
	f.rt.Program("/bin/sh", func(ctx context.Context, s runtime.Spec, _ io.Reader) int {
		fmt.Fprintf(s.Stdout, "hello %s\n", strings.Join(s.Args[1:], " "))
		fmt.Fprint(s.Stderr, "warn\n")
		return 3
	})
	id := f.create("sh", "-c", "x")
	if r := f.e.List()[0]; r.State.Status != StatusCreated || r.Path != "sh" {
		t.Fatalf("%+v", r)
	}
	next, _, _ := f.e.Wait(id, "next-exit")
	if err := f.e.Start(id); err != nil {
		t.Fatal(err)
	}
	if r := <-next; r.StatusCode != 3 {
		t.Fatalf("exit %d", r.StatusCode)
	}
	if r := waitFor(t, f.e, id, "not-running"); r.StatusCode != 3 {
		t.Fatal(r)
	}
	c, _ := f.e.Lookup(id[:8])
	es, _, _ := c.Logger().Read(logsAll())
	if len(es) != 2 || es[0].Log != "hello -c x\n" || es[1].Stream != "stderr" {
		t.Fatalf("logs %+v", es)
	}
	spec := f.rt.Started[0]
	if spec.Args[0] != "/bin/sh" || spec.Cwd != "/" || !contains(spec.Env, "HOME=/root") || !contains(spec.Env, "HOSTNAME="+id[:12]) {
		t.Fatalf("spec %+v", spec)
	}
	removed, _, _ := f.e.Wait(id, "removed")
	if err := f.e.Remove(id, false); err != nil {
		t.Fatal(err)
	}
	<-removed
	if _, err := os.Stat(filepath.Join(f.layout.Containers(), id)); !os.IsNotExist(err) {
		t.Fatal("container directory left behind")
	}
	if _, err := f.e.Lookup(id); errdefs.KindOf(err) != errdefs.KindNotFound {
		t.Fatal(err)
	}
}

func TestTwoContainersFromOneImageAreIndependent(t *testing.T) {
	f := newFixture(t)
	f.rt.Program("/bin/sh", func(ctx context.Context, s runtime.Spec, _ io.Reader) int {
		// The "process" writes into its own root filesystem.
		os.WriteFile(filepath.Join(s.Rootfs, "data"), []byte("changed by "+s.ID[:6]), 0o644)
		return 0
	})
	a, b := f.create("sh"), f.create("sh")
	f.e.Start(a)
	waitFor(t, f.e, a, "not-running")
	read := func(p string) string { d, _ := os.ReadFile(p); return string(d) }
	if got := read(filepath.Join(f.layout.Containers(), a, "rootfs/data")); got != "changed by "+a[:6] {
		t.Fatal(got)
	}
	if got := read(filepath.Join(f.layout.Containers(), b, "rootfs/data")); got != "original" {
		t.Fatalf("container b sees a's write: %q", got)
	}
	img, _ := f.e.Images.Get(f.image)
	if got := read(filepath.Join(f.e.Images.RootfsPath(img.ID), "data")); got != "original" {
		t.Fatalf("image modified: %q", got)
	}
}

func TestStopKillAndSignals(t *testing.T) {
	f := newFixture(t)
	f.rt.Program("/bin/sh", func(ctx context.Context, s runtime.Spec, _ io.Reader) int { <-ctx.Done(); return 0 })
	id := f.create("sh")
	f.e.Start(id)
	if err := f.e.Start(id); errdefs.KindOf(err) != errdefs.KindNotModified {
		t.Fatalf("second start: %v", err)
	}
	if err := f.e.Remove(id, false); errdefs.KindOf(err) != errdefs.KindConflict {
		t.Fatalf("remove running: %v", err)
	}
	zero := 0
	if err := f.e.Stop(id, &zero); err != nil {
		t.Fatal(err)
	}
	// The stop signal goes first even with t=0; this workload dies on it.
	if r := waitFor(t, f.e, id, ""); r.StatusCode != 143 {
		t.Fatalf("stop: %d", r.StatusCode)
	}
	if err := f.e.Stop(id, nil); errdefs.KindOf(err) != errdefs.KindNotModified {
		t.Fatal(err)
	}
	if err := f.e.Kill(id, "TERM"); errdefs.KindOf(err) != errdefs.KindConflict {
		t.Fatal(err)
	}
	f.e.Start(id)
	if err := f.e.Kill(id, "SIGUSR1"); err != nil {
		t.Fatal(err)
	}
	if r := waitFor(t, f.e, id, ""); r.StatusCode != 128+10 {
		t.Fatal(r.StatusCode)
	}
	if _, err := ParseSignal("SIGNOPE"); err == nil {
		t.Fatal("bad signal accepted")
	}
	f.e.Start(id)
	if err := f.e.Remove(id, true); err != nil {
		t.Fatal(err)
	}
}

func TestStartFailuresLikeDocker(t *testing.T) {
	f := newFixture(t)
	for cmd, code := range map[string]int{"missing": 127, "/bin/noexec": 126} {
		id := f.create(cmd)
		err := f.e.Start(id)
		if err == nil {
			t.Fatalf("%s started", cmd)
		}
		r := f.e.List()[0]
		if r.State.Status != StatusCreated || r.State.ExitCode != code {
			t.Fatalf("%s: %v %+v", cmd, err, r.State)
		}
	}
	id, _, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image, User: "ghost"}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.e.Start(id); err == nil || !strings.Contains(err.Error(), "unable to find user ghost") {
		t.Fatal(err)
	}
}

func TestUserMapping(t *testing.T) {
	f := newFixture(t)
	id, _, _ := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image, User: "nobody"}}, "", "")
	f.e.Start(id)
	waitFor(t, f.e, id, "")
	s := f.rt.Started[0]
	if s.UID != 65534 || s.GID != 65534 || !contains(s.Env, "HOME=/nonexistent") {
		t.Fatalf("%+v", s)
	}
}

func TestStaleRunningStateIsReconciled(t *testing.T) {
	f := newFixture(t)
	id := f.create("sh")
	c, _ := f.e.Lookup(id)
	c.mu.Lock()
	c.rec.State = State{Status: StatusRunning, Pid: 999999, PidStart: 12345, StartedAt: time.Now()}
	f.e.persist(c)
	c.mu.Unlock()
	f.open() // daemon restart
	r := f.e.List()[0]
	if r.State.Status != StatusExited || r.State.Pid != 0 || !strings.Contains(r.State.Error, "restarted") {
		t.Fatalf("%+v", r.State)
	}
}

func TestInterruptedCreateAndRemoveRecovered(t *testing.T) {
	f := newFixture(t)
	partial := filepath.Join(f.layout.Containers(), strings.Repeat("b", 64))
	os.MkdirAll(filepath.Join(partial, "rootfs"), 0o700)
	id := f.create("sh")
	c, _ := f.e.Lookup(id)
	c.mu.Lock()
	c.rec.State.Status = StatusRemoving
	f.e.persist(c)
	c.mu.Unlock()
	f.open()
	if n, _ := os.ReadDir(f.layout.Containers()); len(n) != 0 || len(f.e.List()) != 0 {
		t.Fatalf("left: %v", n)
	}
}

func TestNamesAndLookup(t *testing.T) {
	f := newFixture(t)
	id, _, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image}}, "web", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image}}, "/web", ""); errdefs.KindOf(err) != errdefs.KindConflict {
		t.Fatalf("duplicate name: %v", err)
	}
	for _, bad := range []string{"-x", "a b", "../x", "x/y"} {
		if _, _, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image}}, bad, ""); errdefs.KindOf(err) != errdefs.KindInvalid {
			t.Fatalf("name %q: %v", bad, err)
		}
	}
	for _, ref := range []string{"web", "/web", id, id[:5]} {
		if c, err := f.e.Lookup(ref); err != nil || c.rec.ID != id {
			t.Fatalf("lookup %q: %v", ref, err)
		}
	}
	if _, _, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: "nope:1"}}, "", ""); errdefs.KindOf(err) != errdefs.KindNotFound {
		t.Fatalf("missing image: %v", err)
	}
}

func TestUnsupportedFeaturesAreRefused(t *testing.T) {
	f := newFixture(t)
	one := int64(1)
	tr := true
	cases := map[string]HostConfig{
		"privileged": {Privileged: true}, "memory": {Memory: 1 << 20}, "cpus": {NanoCpus: 1e9},
		"pids": {PidsLimit: &one}, "caps": {CapAdd: []string{"NET_ADMIN"}}, "readonly": {ReadonlyRootfs: true},
		"network-none": {NetworkMode: "none"}, "init": {Init: &tr}, "named-volume-ro": {Binds: []string{"data1:/vol:ro"}},
		"ro-bind": {Binds: []string{"/tmp:/x:ro"}},
	}
	for name, hc := range cases {
		hc := hc
		_, _, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image}, HostConfig: &hc}, "", "")
		if k := errdefs.KindOf(err); k != errdefs.KindUnsupported {
			t.Errorf("%s: kind %v err %v", name, k, err)
		}
	}
	if len(f.e.List()) != 0 {
		t.Fatal("refused creates left containers")
	}
	_, warnings, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image}, HostConfig: &HostConfig{NetworkMode: "bridge"}}, "", "")
	if err != nil || len(warnings) == 0 || !strings.Contains(warnings[0], "device network") {
		t.Fatalf("%v %v", warnings, err)
	}
}

func TestBindPolicy(t *testing.T) {
	allowed := t.TempDir()
	os.Mkdir(filepath.Join(allowed, "proj"), 0o700)
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(allowed, "sneaky"))
	f := newFixture(t)
	f.cfg.AllowedBindRoots = []string{allowed}
	f.open()
	ok := HostConfig{Binds: []string{filepath.Join(allowed, "proj") + ":/work"}}
	id, _, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image}, HostConfig: &ok}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	f.e.Start(id)
	waitFor(t, f.e, id, "")
	if b := f.rt.Started[0].Binds; b[len(b)-1].Target != "/work" {
		t.Fatalf("%+v", b)
	}
	for _, bad := range []string{
		filepath.Join(allowed, "sneaky") + ":/x", // symlink out of the allowed root
		outside + ":/x",
		filepath.Join(allowed, "proj/../../") + ":/x",
		f.layout.Root + ":/x",
		filepath.Join(allowed, "proj") + ":/",
		filepath.Join(allowed, "proj") + ":relative",
	} {
		hc := HostConfig{Binds: []string{bad}}
		if _, _, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image}, HostConfig: &hc}, "", ""); err == nil {
			t.Errorf("bind %q accepted", bad)
		}
	}
	// No allowed roots: no binds at all.
	f.cfg.AllowedBindRoots = nil
	f.open()
	if _, _, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image}, HostConfig: &ok}, "", ""); errdefs.KindOf(err) != errdefs.KindForbidden {
		t.Fatal(err)
	}
}

func TestAutoRemove(t *testing.T) {
	f := newFixture(t)
	f.rt.Program("/bin/sh", func(context.Context, runtime.Spec, io.Reader) int { return 5 })
	id, _, _ := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image}, HostConfig: &HostConfig{AutoRemove: true}}, "", "")
	removed, _, _ := f.e.Wait(id, "removed")
	f.e.Start(id)
	select {
	case r := <-removed:
		if r.StatusCode != 5 {
			t.Fatal(r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("not removed")
	}
	if len(f.e.List()) != 0 {
		t.Fatal("still listed")
	}
}

func TestEntrypointCmdMerge(t *testing.T) {
	f := newFixture(t)
	// Overriding the entrypoint drops the image's Cmd, as Docker does.
	id, _, _ := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image, Entrypoint: StrSlice{"/bin/sh", "-c"}}}, "", "")
	r := f.e.List()[0]
	if r.ID != id || r.Path != "/bin/sh" || strings.Join(r.Args, ",") != "-c" {
		t.Fatalf("%q %q", r.Path, r.Args)
	}
	// Cmd alone keeps the image entrypoint (none here) and replaces Cmd.
	f.create("echo", "hi")
	if r := f.e.List()[0]; r.Path != "echo" || strings.Join(r.Args, ",") != "hi" {
		t.Fatalf("%q %q", r.Path, r.Args)
	}
}

func TestStdinReachesProcess(t *testing.T) {
	f := newFixture(t)
	f.rt.Program("/bin/sh", func(ctx context.Context, s runtime.Spec, in io.Reader) int {
		b, _ := io.ReadAll(in)
		s.Stdout.Write(b)
		return 0
	})
	id, _, _ := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image, OpenStdin: true, StdinOnce: true}}, "", "")
	_, as, err := f.e.Attach(id)
	if err != nil || as.Stdin == nil {
		t.Fatal(err)
	}
	go func() {
		as.Stdin.Write([]byte("typed before start\n"))
		as.Stdin.CloseInput()
	}()
	f.e.Start(id)
	waitFor(t, f.e, id, "")
	c, _ := f.e.Lookup(id)
	es, _, _ := c.Logger().Read(logsAll())
	if len(es) != 1 || es[0].Log != "typed before start\n" {
		t.Fatalf("%+v", es)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func logsAll() logsReadOptions { return logsReadOptions{Tail: -1} }

type logsReadOptions = logs.ReadOptions

func TestHostsResolvAndHostnameFiles(t *testing.T) {
	f := newFixture(t)
	hc := HostConfig{Dns: []string{"9.9.9.9"}, DnsSearch: []string{"example.org"}, ExtraHosts: []string{"db:10.0.0.5"}}
	id, _, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image, Hostname: "box"}, HostConfig: &hc}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(f.layout.Containers(), id)
	read := func(n string) string { b, _ := os.ReadFile(filepath.Join(dir, n)); return string(b) }
	if r := read("resolv.conf"); r != "nameserver 9.9.9.9\nsearch example.org\n" {
		t.Fatalf("resolv.conf %q", r)
	}
	if h := read("hosts"); !strings.Contains(h, "127.0.1.1\tbox\n") || !strings.HasSuffix(h, "10.0.0.5\tdb\n") {
		t.Fatalf("hosts %q", h)
	}
	if read("hostname") != "box\n" {
		t.Fatal(read("hostname"))
	}
	f.e.Start(id)
	waitFor(t, f.e, id, "")
	s := f.rt.Started[0]
	targets := map[string]bool{}
	for _, b := range s.Binds {
		targets[b.Target] = true
	}
	if !targets["/etc/hosts"] || !targets["/etc/resolv.conf"] || !targets["/etc/hostname"] || !contains(s.Env, "HOSTNAME=box") {
		t.Fatalf("%+v", s)
	}
	for _, bad := range []HostConfig{{Dns: []string{"not-an-ip"}}, {ExtraHosts: []string{"x:y"}}, {ExtraHosts: []string{"evil\nline:1.2.3.4"}}} {
		bad := bad
		if _, _, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image}, HostConfig: &bad}, "", ""); errdefs.KindOf(err) != errdefs.KindInvalid {
			t.Fatalf("%+v accepted: %v", bad, err)
		}
	}
}

func TestExecRunsInTheContainerRootfsAndEnvironment(t *testing.T) {
	f := newFixture(t)
	f.rt.Program("/bin/sh", func(ctx context.Context, s runtime.Spec, _ io.Reader) int { <-ctx.Done(); return 0 })
	f.rt.Program("/bin/echo", func(ctx context.Context, s runtime.Spec, in io.Reader) int {
		fmt.Fprintf(s.Stdout, "args=%s cwd=%s uid=%d env=%v\n", strings.Join(s.Args[1:], ","), s.Cwd, s.UID, hasKV(s.Env, "X=1"))
		fmt.Fprint(s.Stderr, "e\n")
		return 7
	})
	id := f.create("sh")
	// Not running: refused.
	if _, err := f.e.ExecCreate(id, ExecConfig{Cmd: StrSlice{"echo"}}); errdefs.KindOf(err) != errdefs.KindConflict {
		t.Fatalf("exec in a stopped container: %v", err)
	}
	f.e.Start(id)
	defer f.e.Remove(id, true)
	if _, err := f.e.ExecCreate(id, ExecConfig{}); errdefs.KindOf(err) != errdefs.KindInvalid {
		t.Fatalf("no command: %v", err)
	}
	if _, err := f.e.ExecCreate(id, ExecConfig{Cmd: StrSlice{"echo"}, Privileged: true}); errdefs.KindOf(err) != errdefs.KindUnsupported {
		t.Fatalf("privileged: %v", err)
	}
	xid, err := f.e.ExecCreate(id[:12], ExecConfig{Cmd: StrSlice{"echo", "a", "b"}, Env: []string{"X=1"}, WorkingDir: "/etc", User: "nobody", AttachStdout: true, AttachStderr: true})
	if err != nil {
		t.Fatal(err)
	}
	x, _ := f.e.ExecGet(xid)
	var out, errb strings.Builder
	if err := f.e.ExecStart(x, &out, &errb); err != nil {
		t.Fatal(err)
	}
	<-x.Done()
	st := x.Snapshot()
	if out.String() != "args=a,b cwd=/etc uid=65534 env=true\n" || errb.String() != "e\n" || st.ExitCode == nil || *st.ExitCode != 7 || st.Running {
		t.Fatalf("out=%q err=%q state=%+v", out.String(), errb.String(), st)
	}
	if err := f.e.ExecStart(x, &out, &errb); errdefs.KindOf(err) != errdefs.KindConflict {
		t.Fatalf("second start: %v", err)
	}
	if _, err := f.e.ExecGet("nope"); errdefs.KindOf(err) != errdefs.KindNotFound {
		t.Fatal(err)
	}
	// A command that does not exist is a start failure with Docker's text.
	bad, _ := f.e.ExecCreate(id, ExecConfig{Cmd: StrSlice{"nosuchcmd"}})
	bx, _ := f.e.ExecGet(bad)
	if err := f.e.ExecStart(bx, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "executable file not found") {
		t.Fatalf("%v", err)
	}
	if st := bx.Snapshot(); st.ExitCode == nil || *st.ExitCode != 126 {
		t.Fatalf("%+v", st)
	}
}

func TestExecIsKilledWhenTheContainerStopsAndForgottenOnRemove(t *testing.T) {
	f := newFixture(t)
	f.rt.Program("/bin/sh", func(ctx context.Context, s runtime.Spec, _ io.Reader) int { <-ctx.Done(); return 0 })
	f.rt.Program("/bin/echo", func(ctx context.Context, s runtime.Spec, in io.Reader) int { <-ctx.Done(); return 0 })
	id := f.create("sh")
	f.e.Start(id)
	xid, _ := f.e.ExecCreate(id, ExecConfig{Cmd: StrSlice{"echo"}})
	x, _ := f.e.ExecGet(xid)
	if err := f.e.ExecStart(x, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !x.Snapshot().Running {
		t.Fatal("exec not running")
	}
	zero := 0
	f.e.Stop(id, &zero)
	select {
	case <-x.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("exec outlived its container")
	}
	if st := x.Snapshot(); st.Running || st.ExitCode == nil || *st.ExitCode != 137 {
		t.Fatalf("%+v", st)
	}
	f.e.Remove(id, false)
	if _, err := f.e.ExecGet(xid); errdefs.KindOf(err) != errdefs.KindNotFound {
		t.Fatalf("exec record kept after the container was removed: %v", err)
	}
}

func TestExecStdin(t *testing.T) {
	f := newFixture(t)
	f.rt.Program("/bin/sh", func(ctx context.Context, s runtime.Spec, _ io.Reader) int { <-ctx.Done(); return 0 })
	f.rt.Program("/bin/echo", func(ctx context.Context, s runtime.Spec, in io.Reader) int {
		b, _ := io.ReadAll(in)
		s.Stdout.Write(b)
		return 0
	})
	id := f.create("sh")
	f.e.Start(id)
	defer f.e.Remove(id, true)
	xid, _ := f.e.ExecCreate(id, ExecConfig{Cmd: StrSlice{"echo"}, AttachStdin: true, AttachStdout: true})
	x, _ := f.e.ExecGet(xid)
	var out strings.Builder
	f.e.ExecStart(x, &out, io.Discard)
	in := x.Stdin()
	io.WriteString(in, "typed")
	in.Close()
	<-x.Done()
	if out.String() != "typed" {
		t.Fatalf("%q", out.String())
	}
}

func hasKV(env []string, kv string) bool { return contains(env, kv) }

func volumeHC(binds ...string) *HostConfig { return &HostConfig{Binds: binds} }

func TestNamedVolumePersistsAcrossContainers(t *testing.T) {
	f := newFixture(t)
	writer := func(ctx context.Context, s runtime.Spec, _ io.Reader) int {
		for _, b := range s.Binds {
			if b.Target == "/vol" {
				os.WriteFile(filepath.Join(b.Source, "test.txt"), []byte("persistent"), 0o644)
			}
		}
		return 0
	}
	reader := func(ctx context.Context, s runtime.Spec, _ io.Reader) int {
		for _, b := range s.Binds {
			if b.Target == "/vol" {
				d, _ := os.ReadFile(filepath.Join(b.Source, "test.txt"))
				s.Stdout.Write(d)
			}
		}
		return 0
	}
	f.rt.Program("/bin/sh", writer)
	a, _, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image}, HostConfig: volumeHC("goldenvol:/vol")}, "writer", "")
	if err != nil {
		t.Fatal(err)
	}
	if !f.e.Volumes.Has("goldenvol") {
		t.Fatal("volume not created on first use")
	}
	f.e.Start(a)
	waitFor(t, f.e, a, "")
	// The mount point exists inside the container's own rootfs.
	if fi, err := os.Stat(filepath.Join(f.layout.Containers(), a, "rootfs/vol")); err != nil || !fi.IsDir() {
		t.Fatal("mount point not created in the container filesystem")
	}
	// In use while the (stopped) container exists.
	if err := f.e.Volumes.Remove("goldenvol", f.e.VolumeUsers); errdefs.KindOf(err) != errdefs.KindConflict {
		t.Fatalf("volume removed while referenced: %v", err)
	}
	// Remove the container and the image's data stays; a new container sees it.
	if err := f.e.Remove(a, false); err != nil {
		t.Fatal(err)
	}
	f.rt.Program("/bin/sh", reader)
	b, _, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image}, HostConfig: volumeHC("goldenvol:/vol")}, "reader", "")
	if err != nil {
		t.Fatal(err)
	}
	f.e.Start(b)
	waitFor(t, f.e, b, "")
	c, _ := f.e.Lookup(b)
	es, _, _ := c.Logger().Read(logsAll())
	if len(es) != 1 || es[0].Log != "persistent" {
		t.Fatalf("recreated container does not see the data: %+v", es)
	}
	// Auto-removed containers keep named volumes too.
	f.e.Remove(b, false)
	d, _, _ := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image}, HostConfig: &HostConfig{Binds: []string{"goldenvol:/vol"}, AutoRemove: true}}, "", "")
	f.e.Start(d)
	waitFor(t, f.e, d, "removed")
	if !f.e.Volumes.Has("goldenvol") {
		t.Fatal("--rm deleted a named volume")
	}
	if err := f.e.Volumes.Remove("goldenvol", f.e.VolumeUsers); err != nil {
		t.Fatalf("unused volume not removable: %v", err)
	}
	// An image removal never touches volumes (they are not image data).
	if _, err := os.Stat(f.e.Volumes.DataPath("goldenvol")); !os.IsNotExist(err) {
		t.Fatal("volume data left after removal")
	}
}

func TestMountsAPIAndHostileMounts(t *testing.T) {
	f := newFixture(t)
	mk := func(m string) (string, error) {
		id, _, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image}, HostConfig: &HostConfig{Mounts: []json.RawMessage{json.RawMessage(m)}}}, "", "")
		return id, err
	}
	if _, err := mk(`{"Type":"volume","Source":"viaMount","Target":"/m"}`); err != nil {
		t.Fatal(err)
	}
	if !f.e.Volumes.Has("viaMount") {
		t.Fatal("--mount type=volume did not create the volume")
	}
	if _, err := mk(`{"Type":"volume","Source":"","Target":"/anon"}`); err != nil {
		t.Fatalf("anonymous volume: %v", err)
	}
	for _, tc := range []struct {
		name, mount string
		kind        errdefs.Kind
	}{
		{"read-only", `{"Type":"volume","Source":"v1","Target":"/m","ReadOnly":true}`, errdefs.KindUnsupported},
		{"tmpfs", `{"Type":"tmpfs","Target":"/m"}`, errdefs.KindUnsupported},
		{"unknown type", `{"Type":"npipe","Source":"x","Target":"/m"}`, errdefs.KindInvalid},
		{"traversal name", `{"Type":"volume","Source":"../../etc","Target":"/m"}`, errdefs.KindInvalid},
		{"slash name", `{"Type":"volume","Source":"a/b","Target":"/m"}`, errdefs.KindInvalid},
		{"driver", `{"Type":"volume","Source":"v2","Target":"/m","VolumeOptions":{"DriverConfig":{"Name":"nfs"}}}`, errdefs.KindInvalid},
		{"driver opts", `{"Type":"volume","Source":"v3","Target":"/m","VolumeOptions":{"DriverConfig":{"Options":{"type":"tmpfs"}}}}`, errdefs.KindUnsupported},
		{"relative target", `{"Type":"volume","Source":"v4","Target":"m"}`, errdefs.KindInvalid},
		{"root target", `{"Type":"volume","Source":"v5","Target":"/"}`, errdefs.KindInvalid},
		{"proc target", `{"Type":"volume","Source":"v6","Target":"/proc/self"}`, errdefs.KindInvalid},
		{"managed file", `{"Type":"volume","Source":"v7","Target":"/etc/hosts"}`, errdefs.KindInvalid},
		{"relative bind", `{"Type":"bind","Source":"rel/path","Target":"/m"}`, errdefs.KindInvalid},
		{"host bind outside policy", `{"Type":"bind","Source":"/tmp","Target":"/m"}`, errdefs.KindForbidden},
		{"file in the image", `{"Type":"volume","Source":"v8","Target":"/data"}`, errdefs.KindInvalid},
		{"malformed", `{"Type":`, errdefs.KindInvalid},
	} {
		if _, err := mk(tc.mount); errdefs.KindOf(err) != tc.kind {
			t.Errorf("%s: kind %v err %v", tc.name, errdefs.KindOf(err), err)
		}
	}
	// Refused creates leave no volume behind that a traversal name could name.
	for _, v := range f.e.Volumes.List() {
		if strings.ContainsAny(v.Name, "/.") && v.Name != "viaMount" && len(v.Name) != 64 {
			t.Errorf("unexpected volume %q", v.Name)
		}
	}
	// Duplicate targets and the old -v syntax.
	if _, _, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image}, HostConfig: volumeHC("d1:/same", "d2:/same")}, "", ""); errdefs.KindOf(err) != errdefs.KindInvalid {
		t.Fatalf("duplicate mount point: %v", err)
	}
	if _, _, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image}, HostConfig: volumeHC("../x:/m")}, "", ""); errdefs.KindOf(err) != errdefs.KindForbidden && errdefs.KindOf(err) != errdefs.KindInvalid {
		t.Fatalf("-v ../x: %v", err)
	}
	if _, _, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image}, HostConfig: volumeHC("onlyone")}, "", ""); errdefs.KindOf(err) != errdefs.KindInvalid {
		t.Fatalf("bad spec: %v", err)
	}
}

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// serveHTTPish runs a tiny server on the "container's" network (the device's).
func serveGreeting(cp int) func(ctx context.Context, s runtime.Spec, _ io.Reader) int {
	return func(ctx context.Context, s runtime.Spec, _ io.Reader) int {
		ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(cp)))
		if err != nil {
			return 1
		}
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				c.Write([]byte("hello from the container\n"))
				c.Close()
			}
		}()
		<-ctx.Done()
		ln.Close()
		return 0
	}
}

func dialGreeting(addr string) (string, error) {
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return "", err
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	b, err := io.ReadAll(c)
	return string(b), err
}

func TestPublishedPortLifecycle(t *testing.T) {
	f := newFixture(t)
	cp := freePort(t)
	f.rt.Program("/bin/sh", serveGreeting(cp))
	key := strconv.Itoa(cp) + "/tcp"
	id, warnings, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image},
		HostConfig: &HostConfig{PortBindings: map[string][]PortBinding{key: {{HostPort: ""}}}}}, "web1", "")
	if err != nil || len(warnings) > 1 {
		t.Fatalf("%v %v", warnings, err)
	}
	// Nothing listens before start.
	if err := f.e.Start(id); err != nil {
		t.Fatal(err)
	}
	c, _ := f.e.Lookup(id)
	ports := c.Snapshot().Ports
	if len(ports) != 1 || ports[0].HostIP != "127.0.0.1" || ports[0].ContainerPort != cp || ports[0].HostPort == 0 || ports[0].HostPort == cp {
		t.Fatalf("assignments %+v", ports)
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(ports[0].HostPort))
	var got string
	for i := 0; i < 50; i++ { // the server inside needs a moment to listen
		if got, err = dialGreeting(addr); err == nil && got != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got != "hello from the container\n" {
		t.Fatalf("through the published port: %q %v", got, err)
	}
	// Stop: the listener is gone and the port is free again.
	zero := 0
	f.e.Stop(id, &zero)
	waitFor(t, f.e, id, "")
	if _, err := dialGreeting(addr); err == nil {
		t.Fatal("listener survived the container")
	}
	if p := c.Snapshot().Ports; len(p) != 0 {
		t.Fatalf("assignments kept after exit: %+v", p)
	}
	// Start again: published again (a new ephemeral port is fine).
	if err := f.e.Start(id); err != nil {
		t.Fatal(err)
	}
	p2 := c.Snapshot().Ports
	if len(p2) != 1 {
		t.Fatalf("%+v", p2)
	}
	addr2 := net.JoinHostPort("127.0.0.1", strconv.Itoa(p2[0].HostPort))
	// Remove (force): no listener, no process.
	if err := f.e.Remove(id, true); err != nil {
		t.Fatal(err)
	}
	if conn, err := net.DialTimeout("tcp", addr2, 500*time.Millisecond); err == nil {
		conn.Close()
		t.Fatal("orphan listener after remove")
	}
}

func TestPublishedPortCollisionAndPolicy(t *testing.T) {
	f := newFixture(t)
	f.rt.Program("/bin/sh", func(ctx context.Context, s runtime.Spec, _ io.Reader) int { <-ctx.Done(); return 0 })
	// A host port something else owns: start fails cleanly, container stays created.
	busy, _ := net.Listen("tcp", "127.0.0.1:0")
	defer busy.Close()
	bp := strconv.Itoa(busy.Addr().(*net.TCPAddr).Port)
	cp := freePort(t)
	id, _, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image},
		HostConfig: &HostConfig{PortBindings: map[string][]PortBinding{strconv.Itoa(cp) + "/tcp": {{HostPort: bp}}}}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	err = f.e.Start(id)
	if errdefs.KindOf(err) != errdefs.KindConflict || !strings.Contains(err.Error(), "port is already allocated") {
		t.Fatalf("collision: %v", err)
	}
	if r := f.e.List()[0]; r.State.Status != StatusCreated || r.State.Pid != 0 {
		t.Fatalf("%+v", r.State)
	}
	// Two mappings, the second collides: the first listener is released again.
	free := freePort(t)
	id2, _, _ := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image},
		HostConfig: &HostConfig{PortBindings: map[string][]PortBinding{
			"7001/tcp": {{HostPort: strconv.Itoa(free)}}, "7002/tcp": {{HostPort: bp}}}}}, "", "")
	if err := f.e.Start(id2); err == nil {
		t.Fatal("started despite a collision")
	}
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(free)))
	if err != nil {
		t.Fatalf("a failed start leaked a listener: %v", err)
	}
	l.Close()

	mk := func(hc HostConfig) error {
		_, _, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image}, HostConfig: &hc}, "", "")
		return err
	}
	pb := func(k, ip, port string) HostConfig {
		return HostConfig{PortBindings: map[string][]PortBinding{k: {{HostIP: ip, HostPort: port}}}}
	}
	for name, tc := range map[string]struct {
		hc   HostConfig
		kind errdefs.Kind
	}{
		"udp":              {pb("53/udp", "", "5353"), errdefs.KindUnsupported},
		"sctp":             {pb("80/sctp", "", "8080"), errdefs.KindUnsupported},
		"all interfaces":   {pb("80/tcp", "0.0.0.0", "8080"), errdefs.KindForbidden},
		"lan address":      {pb("80/tcp", "192.168.1.5", "8080"), errdefs.KindForbidden},
		"bad ip":           {pb("80/tcp", "not-an-ip", "8080"), errdefs.KindInvalid},
		"host port zero":   {pb("80/tcp", "", "0"), errdefs.KindInvalid},
		"host port huge":   {pb("80/tcp", "", "70000"), errdefs.KindInvalid},
		"host port text":   {pb("80/tcp", "", "http"), errdefs.KindInvalid},
		"container port 0": {pb("0/tcp", "", "8080"), errdefs.KindInvalid},
		"container text":   {pb("web/tcp", "", "8080"), errdefs.KindInvalid},
		"host range":       {pb("80/tcp", "", "8000-8010"), errdefs.KindInvalid},
	} {
		if err := mk(tc.hc); errdefs.KindOf(err) != tc.kind {
			t.Errorf("%s: kind %v err %v", name, errdefs.KindOf(err), err)
		}
	}
	// Loopback spellings are accepted; the same host port twice is not.
	if err := mk(pb("80/tcp", "127.0.0.1", "18080")); err != nil {
		t.Errorf("loopback: %v", err)
	}
	if err := mk(HostConfig{PortBindings: map[string][]PortBinding{"80/tcp": {{HostPort: "18081"}}, "81/tcp": {{HostPort: "18081"}}}}); errdefs.KindOf(err) != errdefs.KindInvalid {
		t.Errorf("duplicate host port: %v", err)
	}
	// The operator may allow non-loopback publishing explicitly.
	f.cfg.AllowNonLoopbackPublish = true
	f.open()
	if err := mk(pb("80/tcp", "0.0.0.0", "8080")); err != nil {
		t.Errorf("explicitly allowed non-loopback: %v", err)
	}
}

func TestPublishAllAndPassthroughWarning(t *testing.T) {
	f := newFixture(t)
	cp := freePort(t)
	f.rt.Program("/bin/sh", serveGreeting(cp))
	id, _, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image, ExposedPorts: map[string]struct{}{strconv.Itoa(cp) + "/tcp": {}, "53/udp": {}}},
		HostConfig: &HostConfig{PublishAllPorts: true}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.e.Start(id); err != nil {
		t.Fatal(err)
	}
	c, _ := f.e.Lookup(id)
	p := c.Snapshot().Ports
	if len(p) != 1 || p[0].ContainerPort != cp || p[0].HostIP != "127.0.0.1" {
		t.Fatalf("-P published %+v", p)
	}
	f.e.Remove(id, true)
	// host == container port: no forwarder, and the user is told.
	_, w, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image},
		HostConfig: &HostConfig{PortBindings: map[string][]PortBinding{"8080/tcp": {{HostPort: "8080"}}}}}, "", "")
	if err != nil || len(w) == 0 || !strings.Contains(strings.Join(w, " "), "equals container port") {
		t.Fatalf("%v %v", w, err)
	}
}
