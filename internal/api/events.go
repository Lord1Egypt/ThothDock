package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Lord1Egypt/ThothDock/internal/errdefs"
	"github.com/Lord1Egypt/ThothDock/internal/events"
)

// eventJSON is dockerd's event message, including the legacy status/id/from
// fields older clients read for container and image events.
func eventJSON(e events.Event) map[string]any {
	m := map[string]any{
		"Type": e.Type, "Action": e.Action, "scope": "local",
		"Actor": map[string]any{"ID": e.ID, "Attributes": nonNilMap(e.Attrs)},
		"time":  e.Time.Unix(), "timeNano": e.Time.UnixNano(),
	}
	if e.Type == "container" || e.Type == "image" {
		m["status"], m["id"] = e.Action, e.ID
		if from := e.Attrs["image"]; from != "" {
			m["from"] = from
		}
	}
	return m
}

// parseEventTime reads the CLI's "seconds[.nanoseconds]" timestamps and,
// for convenience, Go durations meaning "that long ago".
func parseEventTime(v string, now time.Time) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	if d, err := time.ParseDuration(v); err == nil {
		return now.Add(-d), nil
	}
	sec, frac, _ := strings.Cut(v, ".")
	s, err := strconv.ParseInt(sec, 10, 64)
	if err != nil {
		return time.Time{}, errdefs.Invalid("invalid timestamp %q", v)
	}
	var ns int64
	if frac != "" {
		if len(frac) > 9 {
			frac = frac[:9]
		}
		frac += strings.Repeat("0", 9-len(frac))
		if ns, err = strconv.ParseInt(frac, 10, 64); err != nil {
			return time.Time{}, errdefs.Invalid("invalid timestamp %q", v)
		}
	}
	return time.Unix(s, ns), nil
}

type eventFilter map[string][]string

func (f eventFilter) any(key string, ok func(string) bool) bool {
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

func prefixOrName(e events.Event) func(string) bool {
	return func(v string) bool { return strings.HasPrefix(e.ID, v) || e.Attrs["name"] == v }
}

func (f eventFilter) match(e events.Event) bool {
	return f.any("type", func(v string) bool { return e.Type == v }) &&
		f.any("event", func(v string) bool { return e.Action == v || strings.HasPrefix(e.Action, v+":") }) &&
		(len(f["container"]) == 0 || e.Type == "container" && f.any("container", prefixOrName(e))) &&
		(len(f["network"]) == 0 || e.Type == "network" && f.any("network", prefixOrName(e))) &&
		(len(f["volume"]) == 0 || e.Type == "volume" && f.any("volume", func(v string) bool { return e.ID == v })) &&
		f.any("image", func(v string) bool { return e.Attrs["image"] == v || e.Type == "image" && e.ID == v }) &&
		allLabels(e.Attrs, f["label"])
}

func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	raw, err := parseFilters(q.Get("filters"))
	if err != nil {
		writeError(w, err)
		return
	}
	for k := range raw {
		switch k {
		case "type", "event", "container", "image", "label", "network", "volume", "daemon", "scope":
		default:
			writeError(w, errdefs.Invalid("invalid filter '%s'", k))
			return
		}
	}
	filter := eventFilter(raw)
	now := time.Now()
	since, err := parseEventTime(q.Get("since"), now)
	if err != nil {
		writeError(w, err)
		return
	}
	until, err := parseEventTime(q.Get("until"), now)
	if err != nil {
		writeError(w, err)
		return
	}
	past, sub := s.Engine.Events.Subscribe(since)
	defer sub.Close()
	if since.IsZero() && until.IsZero() {
		// As dockerd: history only when asked for. Compose subscribes
		// without since and would read an old "die" as a fresh exit.
		past = nil
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	send := func(e events.Event) bool {
		if !until.IsZero() && e.Time.After(until) {
			return false
		}
		if filter.match(e) {
			if enc.Encode(eventJSON(e)) != nil {
				return false
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		return true
	}
	if flusher != nil {
		flusher.Flush() // the client knows it is subscribed
	}
	for _, e := range past {
		if !send(e) {
			return
		}
	}
	var deadline <-chan time.Time
	if !until.IsZero() {
		if !until.After(time.Now()) {
			return
		}
		t := time.NewTimer(time.Until(until))
		defer t.Stop()
		deadline = t.C
	}
	for {
		select {
		case e, ok := <-sub.C:
			if !ok || !send(e) {
				return // the bus dropped a slow consumer, or until passed
			}
		case <-deadline:
			return
		case <-r.Context().Done():
			return
		}
	}
}
