package engine

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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
		"pids": {PidsLimit: &one}, "caps": {CapAdd: []string{"NET_ADMIN"}}, "ports": {PortBindings: map[string][]PortBinding{"80/tcp": {{HostPort: "8080"}}}},
		"restart": {RestartPolicy: RestartPolicy{Name: "always"}}, "readonly": {ReadonlyRootfs: true},
		"network-none": {NetworkMode: "none"}, "init": {Init: &tr}, "named-volume": {Binds: []string{"data:/data"}},
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
	if err != nil || len(warnings) == 0 || !strings.Contains(warnings[0], "shares the device network") {
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
