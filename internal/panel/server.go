// Package panel is the ThothDock Web Panel (docs/nextgen/adr/ADR-0005): an
// opt-in HTTPS service, run as its own process, that pairs a browser with a
// one-time code and offers a fixed set of engine operations through the
// engine's Unix socket. It is never a proxy for the Docker API.
package panel

import (
	"context"
	"crypto/tls"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed assets
var assets embed.FS

const (
	cookieName = "td_session"
	csrfHeader = "X-ThothDock-CSRF"
	maxBody    = 64 << 10
	logLimit   = 1 << 20
)

// Config is how the panel is started.
type Config struct {
	Socket   string // the engine's API socket
	StateDir string // certificate, key and sessions (owner-only)
	DataRoot string // the engine's data root, for the storage figures
	Listen   string // host:port of the HTTPS listener
	Version  string
}

// Server is the Web Panel.
type Server struct {
	cfg         Config
	eng         *engineClient
	auth        *auth
	cert        tls.Certificate
	fingerprint string
	log         *slog.Logger
	hosts       map[string]bool // names the Host header may carry
	// CodeChanged is called after a pairing code is used up or exhausted, so
	// whoever shows the code (the Android app) can stop showing a dead one.
	CodeChanged func()
}

// New prepares the state directory, the certificate and the sessions.
func New(cfg Config, log *slog.Logger) (*Server, error) {
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(cfg.StateDir, 0o700); err != nil {
		return nil, err
	}
	host, _, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return nil, err
	}
	cert, fp, err := certificate(cfg.StateDir, certHosts(host))
	if err != nil {
		return nil, err
	}
	a, err := openAuth(filepath.Join(cfg.StateDir, "sessions.json"))
	if err != nil {
		return nil, err
	}
	hosts := map[string]bool{"127.0.0.1": true, "localhost": true, "::1": true}
	for _, h := range append([]string{host}, certHosts(host)...) {
		if ip := net.ParseIP(h); ip == nil || !ip.IsUnspecified() {
			hosts[strings.ToLower(h)] = true
		}
	}
	return &Server{cfg: cfg, eng: newEngineClient(cfg.Socket), auth: a, cert: cert, fingerprint: fp, log: log, hosts: hosts}, nil
}

// hostAllowed: the Host header must name this panel -- its own address or
// loopback -- whatever the port (adb or SSH forwards change it). A page on
// another name that DNS-rebinds to the phone gets nothing, even before the
// certificate and the host-scoped session cookie stop it.
func (s *Server) hostAllowed(r *http.Request) bool {
	h := r.Host
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	return s.hosts[strings.ToLower(strings.Trim(h, "[]"))]
}

// certHosts are the names a new certificate covers: the listen address,
// or every interface address for a wildcard listener (where the platform
// lets an app read them).
func certHosts(host string) []string {
	if ip := net.ParseIP(host); ip != nil && !ip.IsUnspecified() {
		return []string{host}
	}
	var out []string
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && !n.IP.IsLinkLocalUnicast() {
			out = append(out, n.IP.String())
		}
	}
	return out
}

// Fingerprint is the certificate's SHA-256 fingerprint.
func (s *Server) Fingerprint() string { return s.fingerprint }

// NewCode starts a new pairing window.
func (s *Server) NewCode() (string, time.Time, error) { return s.auth.newCode() }

// TLSConfig serves the panel's certificate, TLS 1.2 or newer.
func (s *Server) TLSConfig() *tls.Config {
	return &tls.Config{Certificates: []tls.Certificate{s.cert}, MinVersion: tls.VersionTLS12}
}

// Run serves on cfg.Listen until ctx ends, then closes every connection.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return err
	}
	return s.Serve(ctx, ln)
}

// Serve serves HTTPS on ln until ctx ends. A client that speaks plain HTTP is
// told to use https:// (sniff.go); the panel itself is never served in clear.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	ln = newSniffListener(ln)
	srv := &http.Server{
		Handler:           s.Handler(),
		TLSConfig:         s.TLSConfig(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    32 << 10,
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelDebug),
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ServeTLS(ln, "", "") }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if srv.Shutdown(sctx) != nil {
		srv.Close()
	}
	return nil
}

// Handler is the panel's HTTP surface.
func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	for path, file := range map[string]string{"/{$}": "index.html", "/app.js": "app.js", "/app.css": "app.css", "/mark.png": "mark.png"} {
		m.HandleFunc("GET "+path, s.static(file))
	}
	m.HandleFunc("POST /pair", s.pair)
	m.HandleFunc("GET /api/session", s.authed(s.sessionInfo))
	m.HandleFunc("POST /api/logout", s.authed(s.logout))
	m.HandleFunc("GET /api/summary", s.authed(s.summary))
	m.HandleFunc("GET /api/containers", s.authed(s.containers))
	m.HandleFunc("POST /api/containers/{id}/{action}", s.authed(s.containerAction))
	m.HandleFunc("DELETE /api/containers/{id}", s.authed(s.containerRemove))
	m.HandleFunc("GET /api/containers/{id}/logs", s.authed(s.containerLogs))
	m.HandleFunc("GET /api/images", s.authed(s.images))
	m.HandleFunc("GET /api/volumes", s.authed(s.volumes))
	m.HandleFunc("GET /api/networks", s.authed(s.networks))
	m.HandleFunc("GET /api/stacks", s.authed(s.stacks))
	m.HandleFunc("POST /api/stacks/{project}/{action}", s.authed(s.stackAction))
	m.HandleFunc("GET /api/events", s.authed(s.events))
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { jsonError(w, http.StatusNotFound, "not found") })
	return s.headers(m)
}

func (s *Server) headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Strict-Transport-Security", "max-age=31536000")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/pair" {
			h.Set("Cache-Control", "no-store")
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		}
		if !s.hostAllowed(r) {
			jsonError(w, http.StatusMisdirectedRequest, "this panel answers only on its own address")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) static(file string) http.HandlerFunc {
	types := map[string]string{".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8",
		".css": "text/css; charset=utf-8", ".png": "image/png"}
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := assets.ReadFile("assets/" + file)
		if err != nil {
			jsonError(w, http.StatusNotFound, "not found")
			return
		}
		w.Header().Set("Content-Type", types[filepath.Ext(file)])
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(b)
	}
}

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func engineFail(w http.ResponseWriter, err error) {
	var ee *engineError
	if errors.As(err, &ee) {
		status := ee.Status
		if status < 400 {
			status = http.StatusBadGateway
		}
		jsonError(w, status, ee.Message)
		return
	}
	jsonError(w, http.StatusBadGateway, err.Error())
}

// sameOrigin: a state-changing request must come from the panel's own
// page. Browsers always send Origin on POST and DELETE.
func sameOrigin(r *http.Request) bool {
	return r.Header.Get("Origin") == "https://"+r.Host
}

type authedHandler func(w http.ResponseWriter, r *http.Request, token string, sess session)

// authed requires a session; any method but GET also needs the panel's
// Origin and the session's CSRF token.
func (s *Server) authed(h authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		var token string
		if err == nil {
			token = c.Value
		}
		sess, ok := s.auth.check(token)
		if !ok {
			jsonError(w, http.StatusUnauthorized, "pairing required")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if !sameOrigin(r) || !csrfOK(sess, r.Header.Get(csrfHeader)) {
				jsonError(w, http.StatusForbidden, "cross-site request refused")
				return
			}
		}
		h(w, r, token, sess)
	}
}

func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) pair(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		jsonError(w, http.StatusForbidden, "cross-site request refused")
		return
	}
	var req struct{ Code string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request")
		return
	}
	code := strings.NewReplacer(" ", "", "-", "").Replace(req.Code)
	token, csrf, err := s.auth.pair(code, remoteHost(r))
	if err != nil {
		status := http.StatusUnauthorized
		if errors.Is(err, errTooFast) {
			status = http.StatusTooManyRequests
		}
		s.log.Warn("panel pairing refused", "remote", remoteHost(r), "reason", err)
		if s.CodeChanged != nil && !s.auth.codeActive() {
			s.CodeChanged() // five wrong tries ended the code
		}
		jsonError(w, status, err.Error())
		return
	}
	s.log.Info("panel paired a browser", "remote", remoteHost(r))
	if s.CodeChanged != nil {
		s.CodeChanged() // the code is single use
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", HttpOnly: true, Secure: true,
		SameSite: http.SameSiteStrictMode, MaxAge: int(sessionIdle / time.Second)})
	jsonOK(w, map[string]string{"csrf": csrf})
}

func (s *Server) sessionInfo(w http.ResponseWriter, r *http.Request, _ string, sess session) {
	jsonOK(w, map[string]string{"csrf": sess.CSRF, "fingerprint": s.fingerprint, "version": s.cfg.Version})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, token string, _ session) {
	if err := s.auth.logout(token); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	jsonOK(w, map[string]bool{"ok": true})
}

// ---------------------------------------------------------------- engine views

var (
	refRe     = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
	projectRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
)

const (
	labelProject = "com.docker.compose.project"
	labelService = "com.docker.compose.service"
)

type apiPort struct {
	IP          string
	PrivatePort int
	PublicPort  int
	Type        string
}

type apiContainer struct {
	Id      string
	Names   []string
	Image   string
	State   string
	Status  string
	Created int64
	Ports   []apiPort
	Labels  map[string]string
}

type containerView struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Image   string   `json:"image"`
	State   string   `json:"state"`
	Status  string   `json:"status"`
	Created int64    `json:"created"`
	Ports   []string `json:"ports"`
	Project string   `json:"project,omitempty"`
	Service string   `json:"service,omitempty"`
}

func (s *Server) listContainers(ctx context.Context) ([]containerView, error) {
	var raw []apiContainer
	if err := s.eng.getJSON(ctx, "/containers/json?all=1", &raw); err != nil {
		return nil, err
	}
	out := make([]containerView, 0, len(raw))
	for _, c := range raw {
		v := containerView{ID: c.Id, Image: c.Image, State: c.State, Status: c.Status, Created: c.Created, Ports: []string{},
			Project: c.Labels[labelProject], Service: c.Labels[labelService]}
		if len(c.Names) > 0 {
			v.Name = strings.TrimPrefix(c.Names[0], "/")
		}
		for _, p := range c.Ports {
			if p.PublicPort != 0 {
				v.Ports = append(v.Ports, p.IP+":"+strconv.Itoa(p.PublicPort)+"→"+strconv.Itoa(p.PrivatePort)+"/"+p.Type)
			}
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Server) containers(w http.ResponseWriter, r *http.Request, _ string, _ session) {
	cs, err := s.listContainers(r.Context())
	if err != nil {
		engineFail(w, err)
		return
	}
	jsonOK(w, cs)
}

func (s *Server) containerAction(w http.ResponseWriter, r *http.Request, _ string, _ session) {
	id, action := r.PathValue("id"), r.PathValue("action")
	if !refRe.MatchString(id) {
		jsonError(w, http.StatusBadRequest, "invalid container reference")
		return
	}
	switch action {
	case "start", "stop", "restart":
	default:
		jsonError(w, http.StatusNotFound, "unknown action")
		return
	}
	path := "/containers/" + url.PathEscape(id) + "/" + action
	if action != "start" {
		path += "?t=10"
	}
	if err := s.eng.call(r.Context(), http.MethodPost, path); err != nil {
		engineFail(w, err)
		return
	}
	jsonOK(w, map[string]bool{"ok": true})
}

func (s *Server) containerRemove(w http.ResponseWriter, r *http.Request, _ string, _ session) {
	id := r.PathValue("id")
	if !refRe.MatchString(id) {
		jsonError(w, http.StatusBadRequest, "invalid container reference")
		return
	}
	if err := s.eng.call(r.Context(), http.MethodDelete, "/containers/"+url.PathEscape(id)+"?force=1"); err != nil {
		engineFail(w, err)
		return
	}
	jsonOK(w, map[string]bool{"ok": true})
}

func (s *Server) containerLogs(w http.ResponseWriter, r *http.Request, _ string, _ session) {
	id := r.PathValue("id")
	if !refRe.MatchString(id) {
		jsonError(w, http.StatusBadRequest, "invalid container reference")
		return
	}
	tail := 200
	if n, err := strconv.Atoi(r.URL.Query().Get("tail")); err == nil && n > 0 && n <= 5000 {
		tail = n
	}
	var inspect struct{ Config struct{ Tty bool } }
	if err := s.eng.getJSON(r.Context(), "/containers/"+url.PathEscape(id)+"/json", &inspect); err != nil {
		engineFail(w, err)
		return
	}
	resp, err := s.eng.do(r.Context(), http.MethodGet, "/containers/"+url.PathEscape(id)+"/logs?stdout=1&stderr=1&tail="+strconv.Itoa(tail), nil)
	if err != nil {
		engineFail(w, err)
		return
	}
	defer resp.Body.Close()
	var text string
	if inspect.Config.Tty {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, logLimit))
		text = strings.ToValidUTF8(string(b), "�")
	} else if text, err = demux(resp.Body, logLimit); err != nil {
		engineFail(w, err)
		return
	}
	jsonOK(w, map[string]string{"text": text})
}

type imageView struct {
	ID      string   `json:"id"`
	Tags    []string `json:"tags"`
	Size    int64    `json:"size"`
	Created int64    `json:"created"`
}

func (s *Server) images(w http.ResponseWriter, r *http.Request, _ string, _ session) {
	var raw []struct {
		Id       string
		RepoTags []string
		Size     int64
		Created  int64
	}
	if err := s.eng.getJSON(r.Context(), "/images/json", &raw); err != nil {
		engineFail(w, err)
		return
	}
	out := make([]imageView, 0, len(raw))
	for _, i := range raw {
		tags := i.RepoTags
		if tags == nil {
			tags = []string{}
		}
		out = append(out, imageView{ID: strings.TrimPrefix(i.Id, "sha256:"), Tags: tags, Size: i.Size, Created: i.Created})
	}
	jsonOK(w, out)
}

func (s *Server) volumes(w http.ResponseWriter, r *http.Request, _ string, _ session) {
	var raw struct {
		Volumes []struct {
			Name      string
			Driver    string
			CreatedAt string
			Labels    map[string]string
		}
	}
	if err := s.eng.getJSON(r.Context(), "/volumes", &raw); err != nil {
		engineFail(w, err)
		return
	}
	type view struct {
		Name    string `json:"name"`
		Driver  string `json:"driver"`
		Created string `json:"created"`
		Project string `json:"project,omitempty"`
	}
	out := make([]view, 0, len(raw.Volumes))
	for _, v := range raw.Volumes {
		out = append(out, view{Name: v.Name, Driver: v.Driver, Created: v.CreatedAt, Project: v.Labels[labelProject]})
	}
	jsonOK(w, out)
}

// networkView is one network as the panel shows it. Everything about what a
// network is comes from the engine (network.Describe); the panel guesses nothing.
type networkView struct {
	Name       string   `json:"name"`
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	Builtin    bool     `json:"builtin"`
	Supported  bool     `json:"supported"`
	Summary    string   `json:"summary"`
	Isolation  string   `json:"isolation"`
	Subnet     string   `json:"subnet"`
	Attached   int      `json:"attached"`
	Operations []string `json:"operations"`
	Project    string   `json:"project,omitempty"`
}

const kindUserDefined = "user-defined"

func (s *Server) listNetworks(ctx context.Context) ([]networkView, error) {
	var raw []struct {
		Name, Id  string
		Labels    map[string]string
		ThothDock struct {
			Kind                       string
			Builtin, Supported         bool
			Summary, Isolation, Subnet string
			Attached                   int
			Operations                 []string
		}
	}
	if err := s.eng.getJSON(ctx, "/networks", &raw); err != nil {
		return nil, err
	}
	out := make([]networkView, 0, len(raw))
	for _, n := range raw {
		t := n.ThothDock
		ops := t.Operations
		if ops == nil {
			ops = []string{}
		}
		out = append(out, networkView{Name: n.Name, ID: n.Id, Kind: t.Kind, Builtin: t.Builtin, Supported: t.Supported,
			Summary: t.Summary, Isolation: t.Isolation, Subnet: t.Subnet, Attached: t.Attached, Operations: ops,
			Project: n.Labels[labelProject]})
	}
	return out, nil
}

func (s *Server) networks(w http.ResponseWriter, r *http.Request, _ string, _ session) {
	out, err := s.listNetworks(r.Context())
	if err != nil {
		engineFail(w, err)
		return
	}
	jsonOK(w, out)
}

type stackView struct {
	Name     string          `json:"name"`
	Running  int             `json:"running"`
	Services []containerView `json:"services"`
}

func groupStacks(cs []containerView) []stackView {
	byName := map[string]*stackView{}
	for _, c := range cs {
		if c.Project == "" {
			continue
		}
		st := byName[c.Project]
		if st == nil {
			st = &stackView{Name: c.Project}
			byName[c.Project] = st
		}
		st.Services = append(st.Services, c)
		if c.State == "running" {
			st.Running++
		}
	}
	out := make([]stackView, 0, len(byName))
	for _, st := range byName {
		sort.Slice(st.Services, func(i, j int) bool { return st.Services[i].Service < st.Services[j].Service })
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *Server) stacks(w http.ResponseWriter, r *http.Request, _ string, _ session) {
	cs, err := s.listContainers(r.Context())
	if err != nil {
		engineFail(w, err)
		return
	}
	jsonOK(w, groupStacks(cs))
}

// stackAction starts, stops or restarts every container of a Compose
// project. Creating or removing a stack stays with the Compose client.
func (s *Server) stackAction(w http.ResponseWriter, r *http.Request, _ string, _ session) {
	project, action := r.PathValue("project"), r.PathValue("action")
	if !projectRe.MatchString(project) {
		jsonError(w, http.StatusBadRequest, "invalid project name")
		return
	}
	switch action {
	case "start", "stop", "restart":
	default:
		jsonError(w, http.StatusNotFound, "unknown action")
		return
	}
	cs, err := s.listContainers(r.Context())
	if err != nil {
		engineFail(w, err)
		return
	}
	done := 0
	for _, c := range cs {
		if c.Project != project {
			continue
		}
		path := "/containers/" + c.ID + "/" + action
		if action != "start" {
			path += "?t=10"
		}
		if err := s.eng.call(r.Context(), http.MethodPost, path); err != nil {
			engineFail(w, err)
			return
		}
		done++
	}
	if done == 0 {
		jsonError(w, http.StatusNotFound, "no such stack")
		return
	}
	jsonOK(w, map[string]int{"containers": done})
}

func (s *Server) summary(w http.ResponseWriter, r *http.Request, _ string, _ session) {
	var info struct {
		Containers, ContainersRunning, ContainersStopped, Images, NCPU int
		MemTotal                                                       int64
		ServerVersion, OperatingSystem, KernelVersion, Architecture    string
	}
	if err := s.eng.getJSON(r.Context(), "/info", &info); err != nil {
		engineFail(w, err)
		return
	}
	cs, err := s.listContainers(r.Context())
	if err != nil {
		engineFail(w, err)
		return
	}
	var vols struct{ Volumes []json.RawMessage }
	s.eng.getJSON(r.Context(), "/volumes", &vols)
	// The tile counts user-defined networks only, and says so; the built-in
	// compatibility networks are listed on the Networks tab with what they are.
	userNets := 0
	if nets, err := s.listNetworks(r.Context()); err == nil {
		for _, n := range nets {
			if n.Kind == kindUserDefined {
				userNets++
			}
		}
	}
	jsonOK(w, map[string]any{
		"engine": map[string]any{"version": info.ServerVersion, "os": info.OperatingSystem, "kernel": info.KernelVersion,
			"arch": info.Architecture, "cpus": info.NCPU},
		"containers": map[string]int{"total": info.Containers, "running": info.ContainersRunning, "stopped": info.ContainersStopped},
		"images":     info.Images, "volumes": len(vols.Volumes), "userNetworks": userNets, "stacks": len(groupStacks(cs)),
		"host": hostFigures(s.cfg.DataRoot),
	})
}

// events streams engine events to the browser (server-sent events), so the
// page refreshes on change instead of polling.
func (s *Server) events(w http.ResponseWriter, r *http.Request, _ string, _ session) {
	filters := url.QueryEscape(`{"type":["container","image","network","volume"]}`)
	resp, err := s.eng.do(r.Context(), http.MethodGet, "/events?filters="+filters, nil)
	if err != nil {
		engineFail(w, err)
		return
	}
	defer resp.Body.Close()
	flusher, ok := w.(http.Flusher)
	if !ok {
		jsonError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	rc := http.NewResponseController(w)
	rc.SetReadDeadline(time.Time{})
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, ": connected\n\n")
	flusher.Flush()
	dec := json.NewDecoder(resp.Body)
	for {
		var ev struct {
			Type, Action string
			Actor        struct {
				ID         string
				Attributes map[string]string
			}
			Time int64 `json:"time"`
		}
		if err := dec.Decode(&ev); err != nil {
			return
		}
		b, _ := json.Marshal(map[string]any{"type": ev.Type, "action": ev.Action, "id": ev.Actor.ID,
			"name": ev.Actor.Attributes["name"], "time": ev.Time})
		if _, err := io.WriteString(w, "data: "+string(b)+"\n\n"); err != nil {
			return
		}
		flusher.Flush()
	}
}
