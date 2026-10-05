package api

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Lord1Egypt/ThothDock/internal/engine"
	"github.com/Lord1Egypt/ThothDock/internal/errdefs"
)

// apiStatus maps ThothDock's explicit states onto Docker's vocabulary.
func apiStatus(st engine.State) string {
	switch st.Status {
	case engine.StatusStarting:
		return "created"
	case engine.StatusFailed:
		return "dead"
	}
	return st.Status
}

func statusText(st engine.State) string {
	switch st.Status {
	case engine.StatusRunning, engine.StatusStarting:
		return "Up " + humanDuration(time.Since(st.StartedAt))
	case engine.StatusExited:
		return fmt.Sprintf("Exited (%d) %s ago", st.ExitCode, humanDuration(time.Since(st.FinishedAt)))
	case engine.StatusRemoving:
		return "Removal In Progress"
	case engine.StatusFailed:
		return "Dead"
	}
	return "Created"
}

func command(r engine.Record) string {
	return strings.Join(append([]string{r.Path}, r.Args...), " ")
}

func mounts(r engine.Record) []map[string]any {
	out := []map[string]any{}
	for _, b := range r.Binds {
		if b.Volume != "" {
			out = append(out, map[string]any{"Type": "volume", "Name": b.Volume, "Source": b.Source, "Destination": b.Target, "Driver": "local", "Mode": "z", "RW": true, "Propagation": ""})
			continue
		}
		out = append(out, map[string]any{"Type": "bind", "Source": b.Source, "Destination": b.Target, "Mode": "", "RW": true, "Propagation": "rprivate"})
	}
	return out
}

func (s *Server) listContainers(w http.ResponseWriter, r *http.Request) {
	filters, err := parseFilters(r.URL.Query().Get("filters"))
	if err != nil {
		writeError(w, err)
		return
	}
	for k := range filters {
		switch k {
		case "id", "name", "status", "label", "exited", "ancestor":
		default:
			writeError(w, errdefs.Invalid("invalid filter '%s'", k))
			return
		}
	}
	all := boolParam(r, "all")
	limit, err := intParam(r, "limit")
	if err != nil {
		writeError(w, err)
		return
	}
	out := []map[string]any{}
	for _, c := range s.Engine.List() {
		running := engine.IsRunning(c.State.Status)
		if !all && !running && len(filters["status"]) == 0 && len(filters["exited"]) == 0 {
			continue
		}
		if !matchContainer(c, filters) {
			continue
		}
		if limit != nil && *limit > 0 && len(out) >= *limit {
			break
		}
		out = append(out, map[string]any{
			"Id": c.ID, "Names": []string{"/" + c.Name}, "Image": c.ImageRef, "ImageID": string(c.Image),
			"Command": command(c), "Created": c.Created.Unix(), "Ports": portsList(c), "Labels": nonNilMap(c.Config.Labels),
			"State": apiStatus(c.State), "Status": statusText(c.State),
			"HostConfig":      map[string]string{"NetworkMode": "host"},
			"NetworkSettings": map[string]any{"Networks": map[string]any{"host": map[string]any{}}},
			"Mounts":          mounts(c),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func nonNilMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func matchContainer(c engine.Record, f map[string][]string) bool {
	anyOf := func(key string, ok func(string) bool) bool {
		vals := f[key]
		if len(vals) == 0 {
			return true
		}
		for _, v := range vals {
			if ok(v) {
				return true
			}
		}
		return false
	}
	return anyOf("id", func(v string) bool { return strings.HasPrefix(c.ID, v) }) &&
		anyOf("name", func(v string) bool { return strings.Contains(c.Name, strings.TrimPrefix(v, "/")) }) &&
		anyOf("status", func(v string) bool { return apiStatus(c.State) == v }) &&
		anyOf("exited", func(v string) bool {
			return c.State.Status == engine.StatusExited && strconv.Itoa(c.State.ExitCode) == v
		}) &&
		anyOf("ancestor", func(v string) bool {
			return c.ImageRef == v || strings.HasPrefix(string(c.Image), v) || strings.HasPrefix(c.Image.Hex(), v)
		}) &&
		allLabels(c.Config.Labels, f["label"])
}

func allLabels(labels map[string]string, want []string) bool {
	for _, l := range want {
		k, v, hasV := strings.Cut(l, "=")
		got, ok := labels[k]
		if !ok || hasV && got != v {
			return false
		}
	}
	return true
}

func (s *Server) createContainer(w http.ResponseWriter, r *http.Request) {
	var req engine.CreateRequest
	if err := decodeBody(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	q := r.URL.Query()
	id, warnings, err := s.Engine.Create(req, q.Get("name"), q.Get("platform"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"Id": id, "Warnings": warnings})
}

func (s *Server) inspectContainer(w http.ResponseWriter, r *http.Request) {
	c, err := s.Engine.Lookup(r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	rec := c.Snapshot()
	st := rec.State
	cfg := rec.Config
	dir := c.Dir()
	writeJSON(w, http.StatusOK, map[string]any{
		"Id": rec.ID, "Created": rec.Created.Format(time.RFC3339Nano), "Path": rec.Path, "Args": nonNilSlice(rec.Args),
		"State": map[string]any{
			"Status": apiStatus(st), "Running": engine.IsRunning(st.Status), "Paused": false, "Restarting": false,
			"OOMKilled": false, "Dead": st.Status == engine.StatusFailed, "Pid": st.Pid, "ExitCode": st.ExitCode,
			"Error": st.Error, "StartedAt": st.StartedAt.Format(time.RFC3339Nano), "FinishedAt": st.FinishedAt.Format(time.RFC3339Nano),
		},
		"Image": string(rec.Image), "ResolvConfPath": filepath.Join(dir, "resolv.conf"),
		"HostnamePath": filepath.Join(dir, "hostname"), "HostsPath": filepath.Join(dir, "hosts"),
		"LogPath": c.Logger().Path(), "Name": "/" + rec.Name, "RestartCount": rec.RestartCount,
		"Driver": "thothdock-copy", "Platform": "linux", "MountLabel": "", "ProcessLabel": "", "AppArmorProfile": "",
		"ExecIDs": nil, "HostConfig": rec.HostConfig,
		"GraphDriver": map[string]any{"Name": "thothdock-copy", "Data": map[string]string{"RootDir": filepath.Join(dir, "rootfs")}},
		"Mounts":      mounts(rec), "Config": cfg,
		"NetworkSettings": map[string]any{
			"Bridge": "", "SandboxID": "", "HairpinMode": false, "Ports": portsMap(rec),
			"SandboxKey": "", "Networks": map[string]any{"host": map[string]any{"NetworkID": "host"}},
		},
	})
}

func nonNilSlice(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (s *Server) startContainer(w http.ResponseWriter, r *http.Request) {
	if err := s.Engine.Start(r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) stopContainer(w http.ResponseWriter, r *http.Request) {
	t, err := intParam(r, "t")
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.Engine.Stop(r.PathValue("id"), t); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) restartContainer(w http.ResponseWriter, r *http.Request) {
	t, err := intParam(r, "t")
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.Engine.Restart(r.PathValue("id"), t); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) killContainer(w http.ResponseWriter, r *http.Request) {
	if err := s.Engine.Kill(r.PathValue("id"), r.URL.Query().Get("signal")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) resizeContainer(w http.ResponseWriter, r *http.Request) {
	h, err1 := intParam(r, "h")
	wd, err2 := intParam(r, "w")
	if err1 != nil || err2 != nil || h == nil || wd == nil {
		writeError(w, errdefs.Invalid("resize needs integer h and w"))
		return
	}
	if err := s.Engine.Resize(r.PathValue("id"), *h, *wd); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// waitContainer registers the waiter, then flushes the headers so the
// client knows it is registered before it starts the container (docker run
// relies on this to never miss a fast exit).
func (s *Server) waitContainer(w http.ResponseWriter, r *http.Request) {
	ch, cancel, err := s.Engine.Wait(r.PathValue("id"), r.URL.Query().Get("condition"))
	if err != nil {
		writeError(w, err)
		return
	}
	defer cancel()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	select {
	case res := <-ch:
		body := map[string]any{"StatusCode": res.StatusCode}
		if res.Error != "" {
			body["Error"] = map[string]string{"Message": res.Error}
		}
		writeBody(w, body)
	case <-r.Context().Done():
	}
}

func (s *Server) removeContainer(w http.ResponseWriter, r *http.Request) {
	if boolParam(r, "link") {
		writeError(w, errdefs.Unsupported("ThothDock does not implement container links"))
		return
	}
	if err := s.Engine.Remove(r.PathValue("id"), boolParam(r, "force")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) pruneContainers(w http.ResponseWriter, r *http.Request) {
	filters, err := parseFilters(r.URL.Query().Get("filters"))
	if err != nil {
		writeError(w, err)
		return
	}
	for k := range filters {
		if k != "label" {
			writeError(w, errdefs.Invalid("invalid filter '%s'", k))
			return
		}
	}
	deleted := []string{}
	for _, c := range s.Engine.List() {
		if engine.IsRunning(c.State.Status) || !allLabels(c.Config.Labels, filters["label"]) {
			continue
		}
		if err := s.Engine.Remove(c.ID, false); err == nil {
			deleted = append(deleted, c.ID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ContainersDeleted": deleted, "SpaceReclaimed": 0})
}

// portsList is the "Ports" array of GET /containers/json. A passthrough
// mapping (host port == container port, no forwarder) is listed as exposed
// only, because ThothDock does not control where that service listens.
func portsList(r engine.Record) []map[string]any {
	out := []map[string]any{}
	for _, p := range r.Ports {
		if p.Passthrough {
			out = append(out, map[string]any{"PrivatePort": p.ContainerPort, "Type": "tcp"})
			continue
		}
		out = append(out, map[string]any{"IP": p.HostIP, "PrivatePort": p.ContainerPort, "PublicPort": p.HostPort, "Type": "tcp"})
	}
	return out
}

// portsMap is NetworkSettings.Ports: published ports map to their host
// bindings, exposed-but-unpublished ones to null, as Docker reports them.
func portsMap(r engine.Record) map[string]any {
	out := map[string]any{}
	for k := range r.Config.ExposedPorts {
		out[k] = nil
	}
	for _, p := range r.Ports {
		k := strconv.Itoa(p.ContainerPort) + "/tcp"
		if p.Passthrough {
			if _, ok := out[k]; !ok {
				out[k] = nil
			}
			continue
		}
		list, _ := out[k].([]map[string]string)
		out[k] = append(list, map[string]string{"HostIp": p.HostIP, "HostPort": strconv.Itoa(p.HostPort)})
	}
	return out
}
