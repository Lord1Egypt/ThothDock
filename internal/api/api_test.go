package api

import (
	"archive/tar"
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Lord1Egypt/ThothDock/internal/engine"
	"github.com/Lord1Egypt/ThothDock/internal/image"
	"github.com/Lord1Egypt/ThothDock/internal/oci"
	"github.com/Lord1Egypt/ThothDock/internal/platform"
	"github.com/Lord1Egypt/ThothDock/internal/registry"
	"github.com/Lord1Egypt/ThothDock/internal/registry/registrytest"
	"github.com/Lord1Egypt/ThothDock/internal/runtime"
	"github.com/Lord1Egypt/ThothDock/internal/store"
)

type apiFixture struct {
	srv   *httptest.Server
	rt    *runtime.FakeRuntime
	image string
}

func newAPI(t *testing.T) *apiFixture {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	l := platform.Layout{Root: t.TempDir()}
	l.Ensure()
	reg := registrytest.New(t)
	reg.Image(t, "library/tiny", "1", oci.HostPlatform(), oci.ContainerConfig{Cmd: []string{"/bin/sh"}}, true,
		[]registrytest.File{{Name: "bin/", Type: tar.TypeDir}, {Name: "bin/sh", Body: "#!", Mode: 0o755}, {Name: "bin/echo", Body: "#!", Mode: 0o755}})
	blobs := store.NewBlobs(l.Blobs(), l.Tmp())
	is, err := image.Open(l.Images(), filepath.Join(l.Root, "refs.json"), l.Tmp(), blobs, quiet)
	if err != nil {
		t.Fatal(err)
	}
	p := &image.Puller{Store: is, Client: registry.NewClient(reg.Server.Client(), "t")}
	rt := runtime.NewFake()
	e, err := engine.New(l, is, p, rt, engine.Config{}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Engine: e, Log: quiet, DaemonID: "test", RuntimeName: "fake", RuntimeVersion: "0"}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return &apiFixture{srv: ts, rt: rt, image: reg.Host() + "/library/tiny:1"}
}

func (f *apiFixture) do(t *testing.T, method, path, body string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, f.srv.URL+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func expect(t *testing.T, resp *http.Response, body string, status int, contains string) {
	t.Helper()
	if resp.StatusCode != status || !strings.Contains(body, contains) {
		t.Fatalf("%s %s: got %d %q, want %d containing %q", resp.Request.Method, resp.Request.URL.Path, resp.StatusCode, body, status, contains)
	}
}

func TestRoutesVersionsAndErrors(t *testing.T) {
	f := newAPI(t)
	for _, p := range []string{"/_ping", "/v1.41/_ping", "/v1.24/_ping"} {
		resp, body := f.do(t, "GET", p, "")
		expect(t, resp, body, 200, "OK")
		if resp.Header.Get("Api-Version") != "1.41" {
			t.Fatal("missing Api-Version header")
		}
	}
	resp, _ := f.do(t, "HEAD", "/_ping", "")
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	resp, body := f.do(t, "GET", "/v1.52/version", "")
	expect(t, resp, body, 400, "client version 1.52 is too new. Maximum supported API version is 1.41")
	resp, body = f.do(t, "GET", "/v1.12/version", "")
	expect(t, resp, body, 400, "too old")
	resp, body = f.do(t, "GET", "/v1.41/version", "")
	expect(t, resp, body, 200, `"ApiVersion":"1.41"`)
	resp, body = f.do(t, "GET", "/info", "")
	expect(t, resp, body, 200, `"CgroupDriver":"none"`)
	resp, body = f.do(t, "GET", "/containers/json", "")
	expect(t, resp, body, 200, "[]")
	resp, body = f.do(t, "GET", "/images/json", "")
	expect(t, resp, body, 200, "[]")
	resp, body = f.do(t, "GET", "/containers/doesnotexist/json", "")
	expect(t, resp, body, 404, `"message":"No such container: doesnotexist"`)
	resp, body = f.do(t, "GET", "/containers/json?filters=%7Bbad", "")
	expect(t, resp, body, 400, "invalid filters")
	resp, body = f.do(t, "GET", "/containers/json?filters=%7B%22volume%22%3A%5B%22x%22%5D%7D", "")
	expect(t, resp, body, 400, "invalid filter 'volume'")
	resp, body = f.do(t, "POST", "/containers/create", "{not json")
	expect(t, resp, body, 400, "invalid JSON")
	resp, body = f.do(t, "POST", "/containers/create", `{"Image":"`+strings.Repeat("a", 2<<20)+`"}`)
	expect(t, resp, body, 400, "exceeds")
	resp, body = f.do(t, "POST", "/containers/create", `{"Image":"ghost:1"}`)
	expect(t, resp, body, 404, "No such image: ghost:1")
	resp, body = f.do(t, "POST", "/networks/create", `{}`)
	expect(t, resp, body, 501, "networks")
	resp, body = f.do(t, "POST", "/containers/abc/exec", `{"Cmd":["ls"]}`)
	expect(t, resp, body, 404, "No such container")
	resp, body = f.do(t, "POST", "/exec/deadbeef/start", `{}`)
	expect(t, resp, body, 404, "No such exec instance")
	resp, body = f.do(t, "GET", "/nowhere", "")
	expect(t, resp, body, 404, "page not found")
	resp, body = f.do(t, "POST", "/containers/x/kill?signal=NOPE", "")
	expect(t, resp, body, 400, "invalid signal")
	resp, body = f.do(t, "POST", "/containers/x/wait?condition=sometime", "")
	expect(t, resp, body, 400, "invalid condition")
}

// TestRunFlowOverHTTP mirrors docker run: pull, create, attach (hijacked,
// multiplexed), wait, start, logs, delete.
func TestRunFlowOverHTTP(t *testing.T) {
	f := newAPI(t)
	f.rt.Program("/bin/sh", func(ctx context.Context, s runtime.Spec, _ io.Reader) int {
		fmt.Fprint(s.Stdout, "hello\n")
		fmt.Fprint(s.Stderr, "oops\n")
		return 7
	})
	host := strings.TrimSuffix(f.image, ":1")
	resp, body := f.do(t, "POST", "/v1.41/images/create?fromImage="+host+"&tag=1", "")
	expect(t, resp, body, 200, "Status: Downloaded newer image")
	resp, body = f.do(t, "POST", "/v1.41/containers/create?name=web", `{"Image":"`+f.image+`","HostConfig":{"Privileged":true}}`)
	expect(t, resp, body, 501, "privileged")
	resp, body = f.do(t, "POST", "/v1.41/containers/create?name=web", `{"Image":"`+f.image+`","Cmd":"/bin/sh","AttachStdout":true,"AttachStderr":true}`)
	expect(t, resp, body, 201, `"Id"`)
	var created struct{ Id string }
	json.Unmarshal([]byte(body), &created)

	// Hijack an attach exactly like the Docker CLI.
	conn, err := net.Dial("tcp", strings.TrimPrefix(f.srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "POST /v1.41/containers/web/attach?stream=1&stdout=1&stderr=1 HTTP/1.1\r\nHost: x\r\nUpgrade: tcp\r\nConnection: Upgrade\r\n\r\n")
	br := bufio.NewReader(conn)
	status, _ := br.ReadString('\n')
	if !strings.HasPrefix(status, "HTTP/1.1 101") {
		t.Fatalf("attach status %q", status)
	}
	for {
		line, _ := br.ReadString('\n')
		if line == "\r\n" {
			break
		}
	}
	waitDone := make(chan string, 1)
	go func() {
		_, b := f.do(t, "POST", "/v1.41/containers/web/wait?condition=next-exit", "")
		waitDone <- b
	}()
	time.Sleep(100 * time.Millisecond)
	resp, body = f.do(t, "POST", "/v1.41/containers/web/start", "")
	expect(t, resp, body, 204, "")
	got := map[byte]string{}
	for {
		var hdr [8]byte
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			break
		}
		n := binary.BigEndian.Uint32(hdr[4:])
		payload := make([]byte, n)
		io.ReadFull(br, payload)
		got[hdr[0]] += string(payload)
	}
	if got[1] != "hello\n" || got[2] != "oops\n" {
		t.Fatalf("attach frames %q", got)
	}
	select {
	case b := <-waitDone:
		if !strings.Contains(b, `"StatusCode":7`) {
			t.Fatalf("wait %q", b)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not return")
	}
	resp, body = f.do(t, "GET", "/v1.41/containers/web/logs?stdout=1", "")
	if resp.StatusCode != 200 || !strings.HasSuffix(body, "hello\n") || strings.Contains(body, "oops") || body[0] != 1 {
		t.Fatalf("logs %d %q", resp.StatusCode, body)
	}
	resp, body = f.do(t, "GET", "/v1.41/containers/web/logs", "")
	expect(t, resp, body, 400, "at least one stream")
	resp, body = f.do(t, "GET", "/v1.41/containers/json?all=1", "")
	expect(t, resp, body, 200, `"Status":"Exited (7)`)
	resp, body = f.do(t, "GET", "/v1.41/containers/json", "")
	expect(t, resp, body, 200, "[]")
	resp, body = f.do(t, "GET", "/v1.41/containers/"+created.Id[:6]+"/json", "")
	expect(t, resp, body, 200, `"ExitCode":7`)
	resp, body = f.do(t, "GET", "/v1.41/images/"+f.image+"/json", "")
	expect(t, resp, body, 200, `"RootFS"`)
	var img struct{ Id string }
	json.Unmarshal([]byte(body), &img)
	// By tag with another reference left: only untagged, as in Docker.
	resp, body = f.do(t, "DELETE", "/v1.41/images/"+f.image, "")
	expect(t, resp, body, 200, `"Untagged"`)
	resp, body = f.do(t, "DELETE", "/v1.41/images/"+img.Id, "")
	expect(t, resp, body, 409, "must be forced")
	resp, body = f.do(t, "DELETE", "/v1.41/containers/web", "")
	expect(t, resp, body, 204, "")
	resp, body = f.do(t, "DELETE", "/v1.41/containers/web", "")
	expect(t, resp, body, 404, "No such container")
}

func TestExecOverHTTP(t *testing.T) {
	f := newAPI(t)
	f.rt.Program("/bin/sh", func(ctx context.Context, s runtime.Spec, _ io.Reader) int { <-ctx.Done(); return 0 })
	f.rt.Program("/bin/echo", func(ctx context.Context, s runtime.Spec, in io.Reader) int {
		fmt.Fprint(s.Stdout, "out-data")
		fmt.Fprint(s.Stderr, "err-data")
		return 9
	})
	host := strings.TrimSuffix(f.image, ":1")
	f.do(t, "POST", "/v1.41/images/create?fromImage="+host+"&tag=1", "")
	resp, body := f.do(t, "POST", "/v1.41/containers/create?name=xx", `{"Image":"`+f.image+`","Cmd":["/bin/sh"]}`)
	expect(t, resp, body, 201, "Id")
	resp, body = f.do(t, "POST", "/v1.41/containers/xx/exec", `{"Cmd":["echo"],"AttachStdout":true}`)
	expect(t, resp, body, 409, "is not running")
	f.do(t, "POST", "/v1.41/containers/xx/start", "")
	resp, body = f.do(t, "POST", "/v1.41/containers/xx/exec", `{"Cmd":["echo"],"AttachStdout":true,"AttachStderr":true,"Privileged":true}`)
	expect(t, resp, body, 501, "privileged")
	resp, body = f.do(t, "POST", "/v1.41/containers/xx/exec", `{"Cmd":["echo"],"AttachStdout":true,"AttachStderr":true}`)
	expect(t, resp, body, 201, "Id")
	var created struct{ Id string }
	json.Unmarshal([]byte(body), &created)
	resp, body = f.do(t, "GET", "/v1.41/exec/"+created.Id+"/json", "")
	expect(t, resp, body, 200, `"ExitCode":null`)

	conn, err := net.Dial("tcp", strings.TrimPrefix(f.srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	payload := `{"Detach":false,"Tty":false}`
	fmt.Fprintf(conn, "POST /v1.41/exec/%s/start HTTP/1.1\r\nHost: x\r\nUpgrade: tcp\r\nConnection: Upgrade\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", created.Id, len(payload), payload)
	br := bufio.NewReader(conn)
	status, _ := br.ReadString('\n')
	if !strings.HasPrefix(status, "HTTP/1.1 101") {
		t.Fatalf("status %q", status)
	}
	for {
		l, _ := br.ReadString('\n')
		if l == "\r\n" {
			break
		}
	}
	got := map[byte]string{}
	for {
		var hdr [8]byte
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			break
		}
		n := binary.BigEndian.Uint32(hdr[4:])
		p := make([]byte, n)
		io.ReadFull(br, p)
		got[hdr[0]] += string(p)
	}
	if got[1] != "out-data" || got[2] != "err-data" {
		t.Fatalf("frames %q", got)
	}
	// The exit code is ready the moment the stream ends.
	resp, body = f.do(t, "GET", "/v1.41/exec/"+created.Id+"/json", "")
	expect(t, resp, body, 200, `"ExitCode":9`)
	expect(t, resp, body, 200, `"Running":false`)
	resp, body = f.do(t, "POST", "/v1.41/exec/"+created.Id+"/start", `{"Detach":true}`)
	expect(t, resp, body, 409, "already been started")
	resp, body = f.do(t, "POST", "/v1.41/exec/"+created.Id+"/resize?h=0&w=5", "")
	expect(t, resp, body, 400, "invalid terminal size")
	f.do(t, "DELETE", "/v1.41/containers/xx?force=1", "")
}

func TestVolumesOverHTTP(t *testing.T) {
	f := newAPI(t)
	resp, body := f.do(t, "GET", "/v1.41/volumes", "")
	expect(t, resp, body, 200, `"Volumes":[]`)
	resp, body = f.do(t, "POST", "/v1.41/volumes/create", `{"Name":"goldenvol","Labels":{"team":"x"}}`)
	expect(t, resp, body, 201, `"Name":"goldenvol"`)
	expect(t, resp, body, 201, `"Driver":"local"`)
	expect(t, resp, body, 201, `"Scope":"local"`)
	resp, body = f.do(t, "POST", "/v1.41/volumes/create", `{"Name":"goldenvol"}`)
	expect(t, resp, body, 201, `"team":"x"`) // idempotent: the existing volume
	resp, body = f.do(t, "GET", "/v1.41/volumes/goldenvol", "")
	expect(t, resp, body, 200, `/volumes/goldenvol/_data`)
	resp, body = f.do(t, "GET", "/v1.41/volumes/nope1", "")
	expect(t, resp, body, 404, "no such volume")
	resp, body = f.do(t, "GET", `/v1.41/volumes?filters={"label":["team=x"]}`, "")
	expect(t, resp, body, 200, `goldenvol`)
	resp, body = f.do(t, "GET", `/v1.41/volumes?filters={"label":["team=y"]}`, "")
	expect(t, resp, body, 200, `"Volumes":[]`)
	resp, body = f.do(t, "GET", `/v1.41/volumes?filters={"bogus":["x"]}`, "")
	expect(t, resp, body, 400, "invalid filter")

	// Hostile names never reach the file system.
	for _, name := range []string{"../escape", "a/b", "..", "x\\u0000y", "-lead", "a b", strings.Repeat("a", 300)} {
		resp, body = f.do(t, "POST", "/v1.41/volumes/create", `{"Name":"`+name+`"}`)
		if resp.StatusCode != 400 {
			t.Errorf("name %q: %d %s", name, resp.StatusCode, body)
		}
	}
	for _, p := range []string{"/v1.41/volumes/..%2f..%2fetc", "/v1.41/volumes/a%2fb", "/v1.41/volumes/%2e%2e"} {
		resp, _ = f.do(t, "DELETE", p, "")
		if resp.StatusCode < 400 {
			t.Errorf("DELETE %s: %d", p, resp.StatusCode)
		}
	}
	resp, body = f.do(t, "POST", "/v1.41/volumes/create", `{"Name":"nfsvol","Driver":"nfs"}`)
	expect(t, resp, body, 400, "not available")
	resp, body = f.do(t, "POST", "/v1.41/volumes/create", `{"Name":"optvol","DriverOpts":{"type":"tmpfs"}}`)
	expect(t, resp, body, 501, "driver options")
	resp, body = f.do(t, "POST", "/v1.41/volumes/create", `{"Name":`)
	expect(t, resp, body, 400, "invalid JSON")

	// In use by a container -> 409; removed after the container is gone.
	host := strings.TrimSuffix(f.image, ":1")
	f.do(t, "POST", "/v1.41/images/create?fromImage="+host+"&tag=1", "")
	resp, body = f.do(t, "POST", "/v1.41/containers/create?name=holder", `{"Image":"`+f.image+`","HostConfig":{"Binds":["goldenvol:/v"]}}`)
	expect(t, resp, body, 201, "Id")
	resp, body = f.do(t, "DELETE", "/v1.41/volumes/goldenvol", "")
	expect(t, resp, body, 409, "volume is in use")
	resp, body = f.do(t, "DELETE", "/v1.41/volumes/goldenvol?force=1", "")
	expect(t, resp, body, 409, "volume is in use")
	resp, body = f.do(t, "GET", "/v1.41/containers/holder/json", "")
	expect(t, resp, body, 200, `"Type":"volume"`)
	expect(t, resp, body, 200, `"Name":"goldenvol"`)
	resp, body = f.do(t, "POST", "/v1.41/volumes/prune", "")
	expect(t, resp, body, 200, `"VolumesDeleted":[]`)
	f.do(t, "DELETE", "/v1.41/containers/holder", "")
	resp, body = f.do(t, "POST", "/v1.41/volumes/prune", "")
	expect(t, resp, body, 200, `goldenvol`)
	resp, body = f.do(t, "DELETE", "/v1.41/volumes/goldenvol?force=1", "")
	expect(t, resp, body, 204, "") // force on a missing volume succeeds, as in Docker
	resp, body = f.do(t, "DELETE", "/v1.41/volumes/goldenvol", "")
	expect(t, resp, body, 404, "no such volume")
}
