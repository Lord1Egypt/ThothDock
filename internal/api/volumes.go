package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/Lord1Egypt/ThothDock/internal/errdefs"
	"github.com/Lord1Egypt/ThothDock/internal/volume"
)

func (s *Server) volumeJSON(v volume.Volume) map[string]any {
	labels := v.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	return map[string]any{
		"Name": v.Name, "Driver": "local", "Mountpoint": s.Engine.Volumes.DataPath(v.Name),
		"CreatedAt": v.CreatedAt.Format(time.RFC3339), "Status": map[string]any{},
		"Labels": labels, "Scope": "local", "Options": map[string]string{},
	}
}

func (s *Server) listVolumes(w http.ResponseWriter, r *http.Request) {
	filters, err := parseFilters(r.URL.Query().Get("filters"))
	if err != nil {
		writeError(w, err)
		return
	}
	for k := range filters {
		switch k {
		case "name", "label", "dangling", "driver":
		default:
			writeError(w, errdefs.Invalid("invalid filter '%s'", k))
			return
		}
	}
	out := []map[string]any{}
	for _, v := range s.Engine.Volumes.List() {
		if !matchVolume(v, filters, len(s.Engine.VolumeUsers(v.Name)) == 0) {
			continue
		}
		out = append(out, s.volumeJSON(v))
	}
	writeJSON(w, http.StatusOK, map[string]any{"Volumes": out, "Warnings": nil})
}

func matchVolume(v volume.Volume, f map[string][]string, unused bool) bool {
	if names := f["name"]; len(names) > 0 {
		ok := false
		for _, n := range names {
			ok = ok || strings.Contains(v.Name, n)
		}
		if !ok {
			return false
		}
	}
	for _, d := range f["driver"] {
		if d != "local" {
			return false
		}
	}
	for _, d := range f["dangling"] {
		if (d == "true" || d == "1") != unused {
			return false
		}
	}
	return allLabels(v.Labels, f["label"])
}

func (s *Server) createVolume(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string
		Driver     string
		DriverOpts map[string]string
		Labels     map[string]string
	}
	if err := decodeBody(r, &req); err != nil {
		writeError(w, err)
		return
	}
	v, err := s.Engine.Volumes.Create(req.Name, req.Driver, req.Labels, req.DriverOpts)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.volumeJSON(v))
}

func (s *Server) inspectVolume(w http.ResponseWriter, r *http.Request) {
	v, err := s.Engine.Volumes.Get(r.PathValue("name"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.volumeJSON(v))
}

func (s *Server) removeVolume(w http.ResponseWriter, r *http.Request) {
	// force only turns "no such volume" into success in Docker; it never
	// removes a volume that is in use.
	err := s.Engine.Volumes.Remove(r.PathValue("name"), s.Engine.VolumeUsers)
	if err != nil && boolParam(r, "force") && errdefs.KindOf(err) == errdefs.KindNotFound {
		err = nil
	}
	if err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) pruneVolumes(w http.ResponseWriter, r *http.Request) {
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
	gone, err := s.Engine.Volumes.Prune(s.Engine.VolumeUsers, func(v volume.Volume) bool { return allLabels(v.Labels, filters["label"]) })
	if err != nil {
		writeError(w, err)
		return
	}
	if gone == nil {
		gone = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"VolumesDeleted": gone, "SpaceReclaimed": 0})
}
