package panel

import (
	"archive/tar"
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Lord1Egypt/ThothDock/internal/api"
	"github.com/Lord1Egypt/ThothDock/internal/engine"
	"github.com/Lord1Egypt/ThothDock/internal/image"
	"github.com/Lord1Egypt/ThothDock/internal/oci"
	"github.com/Lord1Egypt/ThothDock/internal/platform"
	"github.com/Lord1Egypt/ThothDock/internal/registry"
	"github.com/Lord1Egypt/ThothDock/internal/registry/registrytest"
	"github.com/Lord1Egypt/ThothDock/internal/runtime"
	"github.com/Lord1Egypt/ThothDock/internal/store"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type fixture struct {
	t      *testing.T
	eng    *engine.Engine
	rt     *runtime.FakeRuntime
	image  string
	socket string
	state  string
	root   string
	panel  *Server
	srv    *httptest.Server
	client *http.Client
	csrf   string
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{t: t, root: t.TempDir(), rt: runtime.NewFake()}
	l := platform.Layout{Root: f.root}
	l.Ensure()
	reg := registrytest.New(t)
	reg.Image(t, "library/tiny", "1", oci.HostPlatform(), oci.ContainerConfig{Cmd: []string{"/bin/sh"}}, true,
		[]registrytest.File{{Name: "bin/", Type: tar.TypeDir}, {Name: "bin/sh", Body: "#!", Mode: 0o755}})
	is, err := image.Open(l.Images(), filepath.Join(l.Root, "refs.json"), l.Tmp(), store.NewBlobs(l.Blobs(), l.Tmp()), quiet)
	if err != nil {
		t.Fatal(err)
	}
	p := &image.Puller{Store: is, Client: registry.NewClient(reg.Server.Client(), "t")}
	if f.eng, err = engine.New(l, is, p, f.rt, engine.Config{}, quiet); err != nil {
		t.Fatal(err)
	}
	f.image = reg.Host() + "/library/tiny:1"
	if _, _, err := p.Pull(context.Background(), f.image, image.PullOptions{Platform: oci.HostPlatform()}); err != nil {
		t.Fatal(err)
	}
	// The engine API on a Unix socket, as on the phone.
	f.socket = filepath.Join(t.TempDir(), "e.sock")
	ln, err := net.Listen("unix", f.socket)
	if err != nil {
		t.Fatal(err)
	}
	apiSrv := &http.Server{Handler: (&api.Server{Engine: f.eng, Log: quiet, DaemonID: "t", RuntimeName: "fake"}).Handler()}
	go apiSrv.Serve(ln)
	t.Cleanup(func() { apiSrv.Close() })
	f.state = filepath.Join(t.TempDir(), "panel")
	f.start()
	return f
}

// start (re)creates the panel on the same state directory.
func (f *fixture) start() {
	var err error
	f.panel, err = New(Config{Socket: f.socket, StateDir: f.state, DataRoot: f.root, Listen: "127.0.0.1:0", Version: "test"}, quiet)
	if err != nil {
		f.t.Fatal(err)
	}
	f.srv = httptest.NewUnstartedServer(f.panel.Handler())
	f.srv.TLS = f.panel.TLSConfig()
	f.srv.StartTLS()
	f.t.Cleanup(f.srv.Close)
	jar, _ := cookiejar.New(nil)
	f.client = &http.Client{Jar: jar, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
}

func (f *fixture) req(method, path, body string, hdr map[string]string) (*http.Response, map[string]any) {
	f.t.Helper()
	r, _ := http.NewRequest(method, f.srv.URL+path, strings.NewReader(body))
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	resp, err := f.client.Do(r)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	b, _ := io.ReadAll(resp.Body)
	json.Unmarshal(b, &out)
	return resp, out
}

func (f *fixture) origin() map[string]string {
	return map[string]string{"Origin": f.srv.URL, "Content-Type": "application/json"}
}

func (f *fixture) authed() map[string]string {
	h := f.origin()
	h[csrfHeader] = f.csrf
	return h
}

func (f *fixture) pair() {
	f.t.Helper()
	code, _, err := f.panel.NewCode()
	if err != nil {
		f.t.Fatal(err)
	}
	resp, body := f.req("POST", "/pair", `{"code":"`+code[:4]+" "+code[4:]+`"}`, f.origin())
	if resp.StatusCode != 200 {
		f.t.Fatalf("pair: %d %v", resp.StatusCode, body)
	}
	f.csrf = body["csrf"].(string)
}

func TestStaticAndUnauthenticated(t *testing.T) {
	f := newFixture(t)
	resp, _ := f.req("GET", "/", "", nil)
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("index: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	csp := resp.Header.Get("Content-Security-Policy")
	for _, want := range []string{"script-src 'self'", "frame-ancestors 'none'", "default-src 'self'"} {
		if !strings.Contains(csp, want) {
			t.Fatalf("CSP %q lacks %q", csp, want)
		}
	}
	if resp.Header.Get("Strict-Transport-Security") == "" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("security headers missing")
	}
	for _, p := range []string{"/api/containers", "/api/summary", "/api/session", "/api/events"} {
		if resp, _ := f.req("GET", p, "", nil); resp.StatusCode != 401 {
			t.Fatalf("%s without a session: %d", p, resp.StatusCode)
		}
	}
	// Never a generic proxy of the Docker API.
	for _, p := range []string{"/v1.41/containers/json", "/containers/json", "/api/../v1.41/info", "/api/docker/info"} {
		if resp, _ := f.req("GET", p, "", nil); resp.StatusCode != 404 && resp.StatusCode != 401 {
			t.Fatalf("%s: %d", p, resp.StatusCode)
		}
	}
}

func TestPairingRules(t *testing.T) {
	f := newFixture(t)
	clock := time.Unix(1_700_000_000, 0)
	f.panel.auth.now = func() time.Time { return clock }
	code, _, _ := f.panel.NewCode()
	if len(code) != 8 {
		t.Fatalf("code %q", code)
	}
	// Without the panel's Origin (a cross-site form post), nothing is tried.
	if resp, _ := f.req("POST", "/pair", `{"code":"`+code+`"}`, map[string]string{"Origin": "https://evil.example"}); resp.StatusCode != 403 {
		t.Fatalf("foreign origin: %d", resp.StatusCode)
	}
	if resp, _ := f.req("POST", "/pair", `{"code":"`+code+`"}`, nil); resp.StatusCode != 403 {
		t.Fatalf("no origin: %d", resp.StatusCode)
	}
	// One attempt per second per address.
	f.req("POST", "/pair", `{"code":"00000000"}`, f.origin())
	if resp, _ := f.req("POST", "/pair", `{"code":"00000001"}`, f.origin()); resp.StatusCode != 429 {
		t.Fatalf("rate limit: %d", resp.StatusCode)
	}
	// Five wrong codes end the code; the right one then fails too.
	for i := 0; i < 4; i++ {
		clock = clock.Add(2 * time.Second)
		if resp, _ := f.req("POST", "/pair", `{"code":"1111111`+fmt.Sprint(i)+`"}`, f.origin()); resp.StatusCode != 401 {
			t.Fatalf("wrong code: %d", resp.StatusCode)
		}
	}
	clock = clock.Add(2 * time.Second)
	if resp, body := f.req("POST", "/pair", `{"code":"`+code+`"}`, f.origin()); resp.StatusCode != 401 || !strings.Contains(fmt.Sprint(body), "no pairing code") {
		t.Fatalf("code survived 5 failures: %d %v", resp.StatusCode, body)
	}
	// Expiry.
	code, _, _ = f.panel.NewCode()
	clock = clock.Add(codeLifetime + time.Second)
	if resp, body := f.req("POST", "/pair", `{"code":"`+code+`"}`, f.origin()); resp.StatusCode != 401 || !strings.Contains(fmt.Sprint(body), "expired") {
		t.Fatalf("expired code accepted: %d %v", resp.StatusCode, body)
	}
	// Success: cookie flags, single use.
	code, _, _ = f.panel.NewCode()
	clock = clock.Add(2 * time.Second)
	resp, body := f.req("POST", "/pair", `{"code":"`+code+`"}`, f.origin())
	if resp.StatusCode != 200 || body["csrf"] == "" {
		t.Fatalf("pair: %d %v", resp.StatusCode, body)
	}
	set := resp.Header.Get("Set-Cookie")
	for _, want := range []string{cookieName + "=", "HttpOnly", "Secure", "SameSite=Strict", "Path=/"} {
		if !strings.Contains(set, want) {
			t.Fatalf("cookie %q lacks %q", set, want)
		}
	}
	clock = clock.Add(2 * time.Second)
	if resp, _ := f.req("POST", "/pair", `{"code":"`+code+`"}`, f.origin()); resp.StatusCode != 401 {
		t.Fatalf("code reused: %d", resp.StatusCode)
	}
}

func TestSessionsAreHashedPersistedAndRevocable(t *testing.T) {
	f := newFixture(t)
	f.pair()
	var token string
	for _, c := range f.client.Jar.Cookies(mustURL(f.srv.URL)) {
		if c.Name == cookieName {
			token = c.Value
		}
	}
	b, err := os.ReadFile(filepath.Join(f.state, "sessions.json"))
	if err != nil || token == "" || strings.Contains(string(b), token) {
		t.Fatalf("session file holds the raw token (or none): %v", err)
	}
	if fi, _ := os.Stat(filepath.Join(f.state, "sessions.json")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("sessions.json mode %v", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(filepath.Join(f.state, "key.pem")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("key.pem mode %v", fi.Mode().Perm())
	}
	fp := f.panel.Fingerprint()
	// A restarted panel keeps the certificate and the session.
	jar := f.client.Jar
	f.start()
	f.client.Jar = jar
	if f.panel.Fingerprint() != fp {
		t.Fatal("certificate changed across a restart")
	}
	if resp, _ := f.req("GET", "/api/session", "", nil); resp.StatusCode != 200 {
		t.Fatalf("session lost across a restart: %d", resp.StatusCode)
	}
	if err := RevokeAll(filepath.Join(f.state, "sessions.json")); err != nil {
		t.Fatal(err)
	}
	f.start()
	f.client.Jar = jar
	if resp, _ := f.req("GET", "/api/session", "", nil); resp.StatusCode != 401 {
		t.Fatalf("revoked session still works: %d", resp.StatusCode)
	}
}

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

func TestStateChangesNeedOriginAndCSRF(t *testing.T) {
	f := newFixture(t)
	f.rt.Program("/bin/sh", func(ctx context.Context, s runtime.Spec, _ io.Reader) int {
		fmt.Fprintln(s.Stdout, "hello from the container")
		<-ctx.Done()
		return 0
	})
	id, _, err := f.eng.Create(engine.CreateRequest{ContainerConfig: engine.ContainerConfig{Image: f.image,
		Labels: map[string]string{labelProject: "demo", labelService: "web"}}}, "demo-web-1", "")
	if err != nil {
		t.Fatal(err)
	}
	f.pair()
	noCSRF := f.origin()
	if resp, _ := f.req("POST", "/api/containers/"+id+"/start", "", noCSRF); resp.StatusCode != 403 {
		t.Fatalf("start without CSRF: %d", resp.StatusCode)
	}
	bad := f.authed()
	bad["Origin"] = "https://evil.example"
	if resp, _ := f.req("POST", "/api/containers/"+id+"/start", "", bad); resp.StatusCode != 403 {
		t.Fatalf("start from another origin: %d", resp.StatusCode)
	}
	wrong := f.authed()
	wrong[csrfHeader] = "x" + f.csrf
	if resp, _ := f.req("POST", "/api/containers/"+id+"/start", "", wrong); resp.StatusCode != 403 {
		t.Fatalf("start with a wrong CSRF token: %d", resp.StatusCode)
	}
	if resp, body := f.req("POST", "/api/containers/"+id+"/start", "", f.authed()); resp.StatusCode != 200 {
		t.Fatalf("start: %d %v", resp.StatusCode, body)
	}
	c, _ := f.eng.Lookup(id)
	if c.Snapshot().State.Status != engine.StatusRunning {
		t.Fatal("the engine did not start the container")
	}
	// Logs, stacks, summary.
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, body := f.req("GET", "/api/containers/demo-web-1/logs?tail=10", "", nil)
		if strings.Contains(fmt.Sprint(body["text"]), "hello from the container") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("logs: %v", body)
		}
		time.Sleep(50 * time.Millisecond)
	}
	resp, err := f.client.Get(f.srv.URL + "/api/stacks")
	if err != nil {
		t.Fatal(err)
	}
	var stacks []stackView
	json.NewDecoder(resp.Body).Decode(&stacks)
	resp.Body.Close()
	if len(stacks) != 1 || stacks[0].Name != "demo" || stacks[0].Running != 1 || stacks[0].Services[0].Service != "web" {
		t.Fatalf("stacks %+v", stacks)
	}
	if resp, body := f.req("POST", "/api/stacks/demo/stop", "", f.authed()); resp.StatusCode != 200 || body["containers"] != float64(1) {
		t.Fatalf("stack stop: %d %v", resp.StatusCode, body)
	}
	if resp, _ := f.req("POST", "/api/stacks/Bad..Name/stop", "", f.authed()); resp.StatusCode != 400 {
		t.Fatalf("bad project name: %d", resp.StatusCode)
	}
	if resp, _ := f.req("POST", "/api/containers/a%20b/start", "", f.authed()); resp.StatusCode != 400 {
		t.Fatalf("bad reference: %d", resp.StatusCode)
	}
	if resp, _ := f.req("POST", "/api/containers/"+id+"/kill", "", f.authed()); resp.StatusCode != 404 {
		t.Fatalf("unlisted action: %d", resp.StatusCode)
	}
	_, sum := f.req("GET", "/api/summary", "", nil)
	if sum["stacks"] != float64(1) || sum["host"] == nil {
		t.Fatalf("summary %v", sum)
	}
	// Delete, then logout ends the session.
	if resp, _ := f.req("DELETE", "/api/containers/"+id, "", f.authed()); resp.StatusCode != 200 {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	if _, err := f.eng.Lookup(id); err == nil {
		t.Fatal("container still exists")
	}
	if resp, _ := f.req("POST", "/api/logout", "", f.authed()); resp.StatusCode != 200 {
		t.Fatalf("logout: %d", resp.StatusCode)
	}
	if resp, _ := f.req("GET", "/api/session", "", nil); resp.StatusCode != 401 {
		t.Fatalf("session after logout: %d", resp.StatusCode)
	}
}

func TestEventStream(t *testing.T) {
	f := newFixture(t)
	f.pair()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", f.srv.URL+"/api/events", nil)
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content type %q", resp.Header.Get("Content-Type"))
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Scan() // ": connected"
	if _, _, err := f.eng.Create(engine.CreateRequest{ContainerConfig: engine.ContainerConfig{Image: f.image}}, "evented", ""); err != nil {
		t.Fatal(err)
	}
	for sc.Scan() {
		if line := sc.Text(); strings.HasPrefix(line, "data: ") {
			if !strings.Contains(line, `"action":"create"`) || !strings.Contains(line, `"name":"evented"`) {
				t.Fatalf("event %s", line)
			}
			return
		}
	}
	t.Fatal("no event")
}

func TestDemux(t *testing.T) {
	frame := func(stream byte, s string) []byte {
		h := []byte{stream, 0, 0, 0, 0, 0, 0, byte(len(s))}
		return append(h, s...)
	}
	in := append(frame(1, "out\n"), frame(2, "err\n")...)
	got, err := demux(strings.NewReader(string(in)), 1<<20)
	if err != nil || got != "out\nerr\n" {
		t.Fatalf("%q %v", got, err)
	}
	if got, _ := demux(strings.NewReader(string(in)), 5); got != "out\ne" {
		t.Fatalf("limit: %q", got)
	}
}

// ---- plain HTTP on the HTTPS port (sniff.go)

func startServing(t *testing.T) (*fixture, string, context.CancelFunc) {
	f := newFixture(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.panel.Serve(ctx, ln); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return f, ln.Addr().String(), cancel
}

func rawHTTP(t *testing.T, addr, request string) string {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(c, request)
	b, _ := io.ReadAll(c)
	return string(b)
}

func TestPlainHTTPGetsAFriendlyRedirectNeverContent(t *testing.T) {
	f, addr, _ := startServing(t)
	f.pair()
	reply := rawHTTP(t, addr, "GET /api/containers HTTP/1.1\r\nHost: "+addr+"\r\n\r\n")
	if !strings.HasPrefix(reply, "HTTP/1.1 307 ") || !strings.Contains(reply, "Location: https://"+addr+"/\r\n") {
		t.Fatalf("no redirect to the https address:\n%s", reply)
	}
	if strings.Contains(reply, "\"id\"") || strings.Contains(reply, "Set-Cookie") {
		t.Fatalf("plain HTTP leaked panel data:\n%s", reply)
	}
	if !strings.Contains(reply, "uses HTTPS") || !strings.Contains(reply, "https://"+addr+"/") {
		t.Fatalf("no explanation:\n%s", reply)
	}
	// The redirect target never carries the requested path (no parameter smuggling).
	if strings.Contains(rawHTTP(t, addr, "GET /api/containers?x=1 HTTP/1.1\r\nHost: "+addr+"\r\n\r\n"), "x=1") {
		t.Fatal("redirect echoed the request path")
	}
}

func TestPlainHTTPWithAForeignHostIsNotRedirected(t *testing.T) {
	_, addr, _ := startServing(t)
	_, port, _ := net.SplitHostPort(addr)
	for _, host := range []string{"evil.example:" + port, "evil.example", addr + "@evil.example", "127.0.0.1:1", "[::1]:" + port + "x"} {
		reply := rawHTTP(t, addr, "GET / HTTP/1.1\r\nHost: "+host+"\r\n\r\n")
		if !strings.HasPrefix(reply, "HTTP/1.1 400 ") || strings.Contains(reply, "Location:") {
			t.Fatalf("Host %q: expected a plain explanation, got:\n%s", host, reply)
		}
		if !strings.Contains(reply, "https://") {
			t.Fatalf("Host %q: no https instructions:\n%s", host, reply)
		}
	}
	if reply := rawHTTP(t, addr, "garbage garbage\r\n\r\n"); !strings.HasPrefix(reply, "HTTP/1.1 400 ") {
		t.Fatalf("garbage: %s", reply)
	}
	if reply := rawHTTP(t, addr, "HEAD / HTTP/1.1\r\nHost: localhost:"+port+"\r\n\r\n"); !strings.HasPrefix(reply, "HTTP/1.1 307 ") || strings.Contains(reply, "<html") {
		t.Fatalf("HEAD: %s", reply)
	}
}

func TestTLSStillWorksAndSlowClientsDoNotBlockIt(t *testing.T) {
	f, addr, _ := startServing(t)
	// Ten connections that never send a byte must not stop a real TLS client.
	for i := 0; i < 10; i++ {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	resp, err := client.Get("https://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Security-Policy") == "" {
		t.Fatalf("https: %d", resp.StatusCode)
	}
	_ = f
}

func TestCodeChangedFiresWhenTheCodeIsUsedOrExhausted(t *testing.T) {
	f := newFixture(t)
	calls := 0
	f.panel.CodeChanged = func() { calls++ }
	f.pair() // the code is used
	if calls != 1 {
		t.Fatalf("after a successful pairing: %d calls", calls)
	}
	clock := time.Now()
	f.panel.auth.now = func() time.Time { return clock }
	f.panel.NewCode()
	for i := 0; i < codeAttempts; i++ {
		clock = clock.Add(2 * time.Second)
		f.req("POST", "/pair", `{"code":"1111111`+fmt.Sprint(i)+`"}`, f.origin())
	}
	if calls != 2 {
		t.Fatalf("after five wrong tries: %d calls, want 2 (only the exhausting try)", calls)
	}
}
