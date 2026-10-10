package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/Lord1Egypt/ThothDock/internal/errdefs"
	"github.com/Lord1Egypt/ThothDock/internal/events"
	"github.com/Lord1Egypt/ThothDock/internal/image"
	"github.com/Lord1Egypt/ThothDock/internal/oci"
	"github.com/Lord1Egypt/ThothDock/internal/registry"
)

func created(img *image.Image) time.Time {
	if img.Config.Created != nil {
		return *img.Config.Created
	}
	return time.Unix(0, 0)
}

func (s *Server) listImages(w http.ResponseWriter, r *http.Request) {
	filters, err := parseFilters(r.URL.Query().Get("filters"))
	if err != nil {
		writeError(w, err)
		return
	}
	for k := range filters {
		if k != "reference" && k != "dangling" && k != "label" {
			writeError(w, errdefs.Invalid("invalid filter '%s'", k))
			return
		}
	}
	out := []map[string]any{}
	for _, img := range s.Engine.Images.List() {
		if !matchImage(img, filters) {
			continue
		}
		tags, digests := img.RepoTags, img.RepoDigests
		if len(tags) == 0 {
			tags = []string{"<none>:<none>"}
		}
		if len(digests) == 0 {
			digests = []string{"<none>@<none>"}
		}
		out = append(out, map[string]any{
			"Id": string(img.ID), "ParentId": "", "RepoTags": tags, "RepoDigests": digests,
			"Created": created(img.Image).Unix(), "Size": img.Size, "SharedSize": -1, "VirtualSize": img.Size,
			"Labels": nonNilMap(img.Config.Config.Labels), "Containers": -1,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func matchImage(img image.Summary, f map[string][]string) bool {
	if refs := f["reference"]; len(refs) > 0 {
		found := false
		for _, want := range refs {
			for _, t := range img.RepoTags {
				name, _, _ := strings.Cut(t, ":")
				if t == want || name == want || strings.HasPrefix(t, strings.TrimSuffix(want, "*")) && strings.HasSuffix(want, "*") {
					found = true
				}
			}
		}
		if !found {
			return false
		}
	}
	for _, d := range f["dangling"] {
		if (d == "true" || d == "1") != (len(img.RepoTags) == 0) {
			return false
		}
	}
	return allLabels(img.Config.Config.Labels, f["label"])
}

// registryCreds decodes the CLI's X-Registry-Auth header (base64url JSON).
func registryCreds(r *http.Request) registry.Credentials {
	h := r.Header.Get("X-Registry-Auth")
	if h == "" {
		return registry.Credentials{}
	}
	var raw []byte
	var err error
	for _, enc := range []*base64.Encoding{base64.URLEncoding, base64.RawURLEncoding, base64.StdEncoding} {
		if raw, err = enc.DecodeString(h); err == nil {
			break
		}
	}
	var a struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err != nil || json.Unmarshal(raw, &a) != nil {
		return registry.Credentials{}
	}
	return registry.Credentials{Username: a.Username, Password: a.Password}
}

type jsonProgress struct {
	Current int64 `json:"current,omitempty"`
	Total   int64 `json:"total,omitempty"`
}

type jsonMessage struct {
	Status         string        `json:"status,omitempty"`
	ID             string        `json:"id,omitempty"`
	ProgressDetail *jsonProgress `json:"progressDetail,omitempty"`
	Error          string        `json:"error,omitempty"`
	ErrorDetail    *struct {
		Message string `json:"message"`
	} `json:"errorDetail,omitempty"`
}

func (s *Server) pullImage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("fromSrc") != "" {
		writeError(w, errdefs.Unsupported("ThothDock does not implement docker import (planned)"))
		return
	}
	name := q.Get("fromImage")
	if name == "" {
		writeError(w, errdefs.Invalid("fromImage is required"))
		return
	}
	if tag := q.Get("tag"); tag != "" {
		if strings.HasPrefix(tag, "sha256:") {
			name += "@" + tag
		} else {
			name += ":" + tag
		}
	}
	plat := oci.HostPlatform()
	if p := q.Get("platform"); p != "" {
		want, err := oci.ParsePlatform(p)
		if err != nil {
			writeError(w, errdefs.Invalid("%v", err))
			return
		}
		if !oci.Matches(plat, want) {
			writeError(w, errdefs.Invalid("platform %s cannot run on this %s device (no emulation)", want, plat))
			return
		}
		plat = want
	}
	started := false
	enc := json.NewEncoder(w)
	start := func() {
		if !started {
			started = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
		}
	}
	send := func(m jsonMessage) {
		start()
		enc.Encode(m)
		flush(w)
	}
	progress := func(p image.Progress) {
		m := jsonMessage{Status: p.Status, ID: p.ID}
		if p.ID != "" && !strings.HasPrefix(p.Status, "Pulling from ") {
			m.ProgressDetail = &jsonProgress{Current: p.Current, Total: p.Total}
		}
		send(m)
	}
	_, status, err := s.Engine.Puller.Pull(r.Context(), name, image.PullOptions{Platform: plat, Creds: registryCreds(r), Progress: progress})
	if err != nil {
		s.Log.Warn("pull failed", "image", name, "err", err)
		if !started {
			writeError(w, err)
			return
		}
		m := jsonMessage{Error: err.Error()}
		m.ErrorDetail = &struct {
			Message string `json:"message"`
		}{err.Error()}
		send(m)
		return
	}
	s.Engine.Events.Publish(events.Event{Type: "image", Action: "pull", ID: name, Attrs: map[string]string{"name": name}})
	send(jsonMessage{Status: "Status: " + status})
}

// imagePath splits /images/{name}/{action}; names may contain slashes.
func imagePath(rest string, actions ...string) (name, action string) {
	for _, a := range actions {
		if n, ok := strings.CutSuffix(rest, "/"+a); ok {
			return n, a
		}
	}
	return rest, ""
}

func (s *Server) imageGet(w http.ResponseWriter, r *http.Request) {
	name, action := imagePath(r.PathValue("rest"), "json", "history")
	switch action {
	case "json":
		s.inspectImage(w, name)
	case "history":
		s.imageHistory(w, name)
	default:
		writeError(w, errdefs.Unsupported("ThothDock does not implement GET /images/%s", r.PathValue("rest")))
	}
}

func (s *Server) inspectImage(w http.ResponseWriter, name string) {
	img, err := s.Engine.Images.Get(name)
	if err != nil {
		writeError(w, err)
		return
	}
	c := img.Config
	tags, digests := img.RepoTags, img.RepoDigests
	if tags == nil {
		tags = []string{}
	}
	if digests == nil {
		digests = []string{}
	}
	layers := make([]string, 0, len(c.RootFS.DiffIDs))
	for _, d := range c.RootFS.DiffIDs {
		layers = append(layers, string(d))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"Id": string(img.ID), "RepoTags": tags, "RepoDigests": digests, "Parent": "", "Comment": "",
		"Created": created(img.Image).Format(time.RFC3339Nano), "Container": "", "ContainerConfig": map[string]any{},
		"DockerVersion": "", "Author": c.Author, "Config": c.Config,
		"Architecture": img.Platform.Architecture, "Variant": img.Platform.Variant, "Os": img.Platform.OS,
		"Size": img.Size, "VirtualSize": img.Size,
		"GraphDriver": map[string]any{"Name": "thothdock-copy", "Data": map[string]string{"RootDir": s.Engine.Images.RootfsPath(img.ID)}},
		"RootFS":      map[string]any{"Type": "layers", "Layers": layers},
		"Metadata":    map[string]any{"LastTagTime": img.Pulled.Format(time.RFC3339Nano)},
	})
}

func (s *Server) imageHistory(w http.ResponseWriter, name string) {
	img, err := s.Engine.Images.Get(name)
	if err != nil {
		writeError(w, err)
		return
	}
	out := []map[string]any{}
	hist := img.Config.History
	layer := len(img.Layers) - 1
	for i := len(hist) - 1; i >= 0; i-- {
		h := hist[i]
		var size int64
		if !h.EmptyLayer && layer >= 0 {
			size = img.Layers[layer].Size
			layer--
		}
		var t int64
		if h.Created != nil {
			t = h.Created.Unix()
		}
		id := "<missing>"
		if i == len(hist)-1 {
			id = string(img.ID)
		}
		out = append(out, map[string]any{"Id": id, "Created": t, "CreatedBy": h.CreatedBy, "Tags": nil, "Size": size, "Comment": h.Comment})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) imagePost(w http.ResponseWriter, r *http.Request) {
	name, action := imagePath(r.PathValue("rest"), "tag", "push")
	switch action {
	case "tag":
		repo, tag := r.URL.Query().Get("repo"), r.URL.Query().Get("tag")
		if repo == "" {
			writeError(w, errdefs.Invalid("repo is required"))
			return
		}
		target := repo
		if tag != "" {
			target += ":" + tag
		}
		if err := s.Engine.Images.Tag(name, target); err != nil {
			writeError(w, err)
			return
		}
		s.Engine.Events.Publish(events.Event{Type: "image", Action: "tag", ID: name, Attrs: map[string]string{"name": target}})
		w.WriteHeader(http.StatusCreated)
	case "push":
		writeError(w, errdefs.Unsupported("ThothDock does not implement docker push (planned)"))
	default:
		writeError(w, errdefs.Unsupported("ThothDock does not implement POST /images/%s", r.PathValue("rest")))
	}
}

func (s *Server) deleteImage(w http.ResponseWriter, r *http.Request) {
	items, err := s.Engine.Images.Delete(r.PathValue("rest"), boolParam(r, "force"), s.Engine.ImageUsage)
	if err != nil {
		writeError(w, err)
		return
	}
	for _, it := range items {
		if it.Untagged != "" {
			s.Engine.Events.Publish(events.Event{Type: "image", Action: "untag", ID: it.Untagged, Attrs: map[string]string{"name": it.Untagged}})
		}
		if it.Deleted != "" {
			s.Engine.Events.Publish(events.Event{Type: "image", Action: "delete", ID: it.Deleted, Attrs: map[string]string{"name": it.Deleted}})
		}
	}
	writeJSON(w, http.StatusOK, items)
}
