// Package api serves the Docker Engine API (v1.41 subset) over ThothDock's
// engine. Paths may carry a /v<major>.<minor> prefix; requests for a newer
// API version than ThothDock implements are refused, as dockerd does.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Lord1Egypt/ThothDock/internal/engine"
	"github.com/Lord1Egypt/ThothDock/internal/errdefs"
	"github.com/Lord1Egypt/ThothDock/internal/version"
)

// maxBody bounds JSON request bodies.
const maxBody = 1 << 20

// Server is the HTTP API.
type Server struct {
	Engine   *engine.Engine
	Log      *slog.Logger
	DaemonID string
	// RuntimeInfo describes the runtime for /version and /info.
	RuntimeName, RuntimePath, RuntimeVersion string
	Started                                  time.Time
	mux                                      *http.ServeMux
}

// Handler builds the router.
func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /_ping", s.ping)
	m.HandleFunc("HEAD /_ping", s.ping)
	m.HandleFunc("GET /version", s.version)
	m.HandleFunc("GET /info", s.info)

	m.HandleFunc("GET /containers/json", s.listContainers)
	m.HandleFunc("POST /containers/create", s.createContainer)
	m.HandleFunc("POST /containers/prune", s.pruneContainers)
	m.HandleFunc("GET /containers/{id}/json", s.inspectContainer)
	m.HandleFunc("POST /containers/{id}/start", s.startContainer)
	m.HandleFunc("POST /containers/{id}/stop", s.stopContainer)
	m.HandleFunc("POST /containers/{id}/restart", s.restartContainer)
	m.HandleFunc("POST /containers/{id}/kill", s.killContainer)
	m.HandleFunc("POST /containers/{id}/wait", s.waitContainer)
	m.HandleFunc("POST /containers/{id}/attach", s.attachContainer)
	m.HandleFunc("POST /containers/{id}/resize", s.resizeContainer)
	m.HandleFunc("GET /containers/{id}/logs", s.containerLogs)
	m.HandleFunc("DELETE /containers/{id}", s.removeContainer)
	m.HandleFunc("POST /containers/{id}/update", s.updateContainer)
	m.HandleFunc("GET /events", s.streamEvents)

	m.HandleFunc("GET /networks", s.listNetworks)
	m.HandleFunc("POST /networks/create", s.createNetwork)
	m.HandleFunc("POST /networks/prune", s.pruneNetworks)
	m.HandleFunc("GET /networks/{id}", s.inspectNetwork)
	m.HandleFunc("DELETE /networks/{id}", s.removeNetwork)
	m.HandleFunc("POST /networks/{id}/connect", s.connectNetwork)
	m.HandleFunc("POST /networks/{id}/disconnect", s.disconnectNetwork)

	m.HandleFunc("POST /containers/{id}/exec", s.execCreate)
	m.HandleFunc("POST /exec/{id}/start", s.execStart)
	m.HandleFunc("POST /exec/{id}/resize", s.execResize)
	m.HandleFunc("GET /exec/{id}/json", s.execInspect)

	m.HandleFunc("GET /volumes", s.listVolumes)
	m.HandleFunc("POST /volumes/create", s.createVolume)
	m.HandleFunc("POST /volumes/prune", s.pruneVolumes)
	m.HandleFunc("GET /volumes/{name}", s.inspectVolume)
	m.HandleFunc("DELETE /volumes/{name}", s.removeVolume)

	m.HandleFunc("GET /images/json", s.listImages)
	m.HandleFunc("POST /images/create", s.pullImage)
	m.HandleFunc("GET /images/{rest...}", s.imageGet)
	m.HandleFunc("POST /images/{rest...}", s.imagePost)
	m.HandleFunc("DELETE /images/{rest...}", s.deleteImage)

	for _, p := range []struct{ prefix, what string }{
		{"/containers/{id}/pause", "pause/unpause (PRoot has no freezer cgroup)"},
		{"/containers/{id}/unpause", "pause/unpause (PRoot has no freezer cgroup)"},
		{"/containers/{id}/stats", "container stats (there are no cgroups to read; planned from /proc)"},
		{"/containers/{id}/top", "docker top (planned)"},
		{"/containers/{id}/archive", "docker cp (planned)"},
		{"/containers/{id}/export", "docker export (planned)"},
		{"/containers/{id}/changes", "docker diff (planned)"},
		{"/containers/{id}/rename", "docker rename (planned)"},
		{"/build", "docker build (planned)"},
		{"/commit", "docker commit (planned)"},
		{"/system/df", "disk usage reporting (planned)"},
		{"/swarm", "swarm mode"},
		{"/nodes", "swarm mode"},
		{"/services", "swarm mode"},
		{"/tasks", "swarm mode"},
		{"/secrets", "swarm mode"},
		{"/configs", "swarm mode"},
		{"/plugins", "plugins"},
		{"/session", "BuildKit sessions"},
		{"/auth", "registry login checks (credentials are passed per pull)"},
		{"/distribution/", "distribution inspection (planned)"},
	} {
		what := p.what
		pattern := p.prefix
		if strings.HasSuffix(pattern, "/") {
			pattern += "{rest...}"
		} else {
			m.HandleFunc(pattern+"/{rest...}", notImplemented(what))
		}
		m.HandleFunc(pattern, notImplemented(what))
	}
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "page not found"})
	})
	s.mux = m
	return s.middleware(m)
}

func notImplemented(what string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeError(w, errdefs.Unsupported("ThothDock does not implement %s", what))
	}
}

var versionPrefix = regexp.MustCompile(`^/v([0-9]+)\.([0-9]+)(/.*)$`)

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Api-Version", version.APIVersion)
		h.Set("Docker-Experimental", "false")
		h.Set("Ostype", "linux")
		h.Set("Server", "ThothDock/"+version.Version+" (linux)")
		if m := versionPrefix.FindStringSubmatch(r.URL.Path); m != nil {
			v := m[1] + "." + m[2]
			if compareVersions(v, version.APIVersion) > 0 {
				writeError(w, errdefs.Invalid("client version %s is too new. Maximum supported API version is %s", v, version.APIVersion))
				return
			}
			if compareVersions(v, version.MinAPIVersion) < 0 {
				writeError(w, errdefs.Invalid("client version %s is too old. Minimum supported API version is %s, please upgrade your client to a newer version", v, version.MinAPIVersion))
				return
			}
			r.URL.Path = m[3]
			r.URL.RawPath = ""
		}
		start := time.Now()
		next.ServeHTTP(w, r)
		s.Log.Debug("request", "method", r.Method, "path", r.URL.Path, "took", time.Since(start))
	})
}

func compareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 2; i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func statusOf(err error) int {
	switch errdefs.KindOf(err) {
	case errdefs.KindNotFound:
		return http.StatusNotFound
	case errdefs.KindInvalid:
		return http.StatusBadRequest
	case errdefs.KindConflict:
		return http.StatusConflict
	case errdefs.KindNotModified:
		return http.StatusNotModified
	case errdefs.KindUnsupported:
		return http.StatusNotImplemented
	case errdefs.KindForbidden:
		return http.StatusForbidden
	}
	return http.StatusInternalServerError
}

func writeError(w http.ResponseWriter, err error) {
	status := statusOf(err)
	if status == http.StatusNotModified {
		w.WriteHeader(status)
		return
	}
	writeJSON(w, status, map[string]string{"message": err.Error()})
}

// bodyReadTimeout bounds how long a client may take to deliver a JSON
// request body. It is a variable so tests can shorten it.
var bodyReadTimeout = func() *atomic.Int64 { var a atomic.Int64; a.Store(int64(30 * time.Second)); return &a }()

// decodeBody reads a bounded JSON body within bodyReadTimeout. An empty body
// leaves v unchanged. The deadline is set only for the read and cleared after:
// an expired read deadline would also cancel the request's context, so it must
// never outlive the body (long-running requests like wait and pull follow).
func decodeBody(w http.ResponseWriter, r *http.Request, v any) error {
	rc := http.NewResponseController(w)
	if rc.SetReadDeadline(time.Now().Add(time.Duration(bodyReadTimeout.Load()))) == nil {
		defer rc.SetReadDeadline(time.Time{})
	}
	body := http.MaxBytesReader(w, r.Body, maxBody)
	dec := json.NewDecoder(body)
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return errdefs.Invalid("request body exceeds %d bytes", maxBody)
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return errdefs.Invalid("timed out reading the request body")
		}
		return errdefs.Invalid("invalid JSON body: %v", err)
	}
	return nil
}

func boolParam(r *http.Request, name string) bool {
	switch strings.ToLower(r.URL.Query().Get(name)) {
	case "1", "true", "yes":
		return true
	}
	return false
}

func intParam(r *http.Request, name string) (*int, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return nil, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return nil, errdefs.Invalid("invalid %s parameter %q", name, v)
	}
	return &n, nil
}

// parseFilters accepts both the current {"key":["v"]} and the legacy
// {"key":{"v":true}} encodings.
func parseFilters(raw string) (map[string][]string, error) {
	out := map[string][]string{}
	if raw == "" {
		return out, nil
	}
	var any map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &any); err != nil {
		return nil, errdefs.Invalid("invalid filters: %v", err)
	}
	for k, v := range any {
		var list []string
		if json.Unmarshal(v, &list) == nil {
			out[k] = list
			continue
		}
		var set map[string]bool
		if err := json.Unmarshal(v, &set); err != nil {
			return nil, errdefs.Invalid("invalid filter %q", k)
		}
		for x, on := range set {
			if on {
				out[k] = append(out[k], x)
			}
		}
	}
	return out, nil
}

// humanDuration matches the Docker CLI's "Up 5 minutes" wording.
func humanDuration(d time.Duration) string {
	seconds := int(d.Seconds())
	switch {
	case seconds < 1:
		return "Less than a second"
	case seconds == 1:
		return "1 second"
	case seconds < 60:
		return fmt.Sprintf("%d seconds", seconds)
	}
	minutes := int(d.Minutes())
	switch {
	case minutes == 1:
		return "About a minute"
	case minutes < 60:
		return fmt.Sprintf("%d minutes", minutes)
	}
	hours := int(d.Hours() + 0.5)
	switch {
	case hours == 1:
		return "About an hour"
	case hours < 48:
		return fmt.Sprintf("%d hours", hours)
	case hours < 24*7*2:
		return fmt.Sprintf("%d days", hours/24)
	case hours < 24*30*2:
		return fmt.Sprintf("%d weeks", hours/24/7)
	case hours < 24*365*2:
		return fmt.Sprintf("%d months", hours/24/30)
	}
	return fmt.Sprintf("%d years", int(d.Hours())/24/365)
}
