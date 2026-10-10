package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/Lord1Egypt/ThothDock/internal/engine"
	"github.com/Lord1Egypt/ThothDock/internal/errdefs"
	"github.com/Lord1Egypt/ThothDock/internal/network"
)

// networkJSON is the network object of the Engine API. Built-in networks
// report no IPAM configuration: they are the device network.
func (s *Server) networkJSON(n network.Network, withContainers bool) map[string]any {
	ipam := map[string]any{"Driver": "default", "Options": map[string]string{}, "Config": []map[string]string{}}
	if !n.Builtin {
		ipam["Config"] = []map[string]string{{"Subnet": network.Subnet, "Gateway": network.Gateway}}
	}
	info := network.Describe(n)
	members := s.Engine.NetworkMembers(n)
	containers := map[string]any{}
	if withContainers {
		for _, r := range members {
			if n.Builtin {
				// Device-network containers have no address of their own to report.
				containers[r.ID] = map[string]string{"Name": r.Name, "EndpointID": "", "MacAddress": "", "IPv4Address": "", "IPv6Address": ""}
				continue
			}
			ep := r.Networks[n.ID]
			containers[r.ID] = map[string]string{"Name": r.Name, "EndpointID": ep.EndpointID, "MacAddress": "",
				"IPv4Address": r.NetIP + "/16", "IPv6Address": ""}
		}
	}
	labels := nonNilMap(n.Labels)
	for k, v := range info.Labels() {
		labels[k] = v
	}
	created := n.Created
	if created.IsZero() {
		created = s.Started
	}
	return map[string]any{
		"Name": n.Name, "Id": n.ID, "Created": created.Format(time.RFC3339Nano), "Scope": "local", "Driver": n.Driver,
		"EnableIPv6": false, "IPAM": ipam, "Internal": false, "Attachable": n.Attachable, "Ingress": false,
		"ConfigFrom": map[string]string{"Network": ""}, "ConfigOnly": false, "Containers": containers,
		"Options": nonNilMap(n.Options), "Labels": labels,
		// ThothDock's own, truthful description; stock clients ignore it.
		"ThothDock": map[string]any{"Kind": info.Kind, "Builtin": info.Builtin, "Supported": info.Supported,
			"Summary": info.Summary, "Isolation": info.Isolation, "Subnet": info.Subnet,
			"Attached": len(members), "Operations": info.Operations},
	}
}

func matchNetwork(n network.Network, f map[string][]string, dangling func(network.Network) bool) bool {
	anyOf := func(key string, ok func(string) bool) bool {
		if len(f[key]) == 0 {
			return true
		}
		for _, v := range f[key] {
			if ok(v) {
				return true
			}
		}
		return false
	}
	return anyOf("name", func(v string) bool { return strings.Contains(n.Name, v) }) &&
		anyOf("id", func(v string) bool { return strings.HasPrefix(n.ID, v) }) &&
		anyOf("driver", func(v string) bool { return n.Driver == v }) &&
		anyOf("scope", func(v string) bool { return v == "local" }) &&
		anyOf("type", func(v string) bool { return v == "custom" && !n.Builtin || v == "builtin" && n.Builtin }) &&
		anyOf("dangling", func(v string) bool { return (v == "true" || v == "1") == dangling(n) }) &&
		allLabels(n.Labels, f["label"])
}

func (s *Server) dangling(n network.Network) bool {
	return !n.Builtin && len(s.Engine.NetworkUsers(n.ID)) == 0
}

func (s *Server) listNetworks(w http.ResponseWriter, r *http.Request) {
	filters, err := parseFilters(r.URL.Query().Get("filters"))
	if err != nil {
		writeError(w, err)
		return
	}
	for k := range filters {
		switch k {
		case "name", "id", "driver", "scope", "type", "dangling", "label":
		default:
			writeError(w, errdefs.Invalid("invalid filter '%s'", k))
			return
		}
	}
	out := []map[string]any{}
	for _, n := range s.Engine.Networks.List() {
		if matchNetwork(n, filters, s.dangling) {
			out = append(out, s.networkJSON(n, false))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) inspectNetwork(w http.ResponseWriter, r *http.Request) {
	n, err := s.Engine.Networks.Lookup(r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.networkJSON(n, true))
}

func (s *Server) createNetwork(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name           string
		CheckDuplicate bool
		Driver         string
		Internal       bool
		Attachable     bool
		Ingress        bool
		EnableIPv6     bool
		ConfigOnly     bool
		ConfigFrom     *struct{ Network string }
		IPAM           *struct {
			Driver string
			Config []map[string]any
		}
		Options map[string]string
		Labels  map[string]string
	}
	if err := decodeBody(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	switch {
	case req.Internal:
		err := errdefs.Unsupported("ThothDock does not support internal networks: blocking a container's outside traffic needs packet filtering it does not have, so the request is refused rather than half-honoured")
		writeError(w, err)
		return
	case req.EnableIPv6:
		writeError(w, errdefs.Unsupported("ThothDock does not support IPv6 networks: container addresses are IPv4 loopback addresses"))
		return
	case req.Ingress || req.ConfigOnly || req.ConfigFrom != nil && req.ConfigFrom.Network != "":
		writeError(w, errdefs.Unsupported("ThothDock does not support swarm network features (ingress, config-only networks)"))
		return
	case req.IPAM != nil && (len(req.IPAM.Config) > 0 || req.IPAM.Driver != "" && req.IPAM.Driver != "default"):
		writeError(w, errdefs.Unsupported("ThothDock does not support custom IPAM: every network shares the container address pool %s", network.Subnet))
		return
	}
	n, err := s.Engine.NetworkCreate(network.CreateRequest{Name: req.Name, Driver: req.Driver, Labels: req.Labels,
		Options: req.Options, Attachable: req.Attachable})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"Id": n.ID, "Warning": ""})
}

func (s *Server) removeNetwork(w http.ResponseWriter, r *http.Request) {
	if err := s.Engine.NetworkRemove(r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) connectNetwork(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Container      string
		EndpointConfig *engine.EndpointRequest
	}
	if err := decodeBody(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	if err := s.Engine.NetworkConnect(r.PathValue("id"), req.Container, req.EndpointConfig); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) disconnectNetwork(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Container string
		Force     bool
	}
	if err := decodeBody(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	if err := s.Engine.NetworkDisconnect(r.PathValue("id"), req.Container); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) pruneNetworks(w http.ResponseWriter, r *http.Request) {
	filters, err := parseFilters(r.URL.Query().Get("filters"))
	if err != nil {
		writeError(w, err)
		return
	}
	var until time.Time
	for k, v := range filters {
		switch k {
		case "label":
		case "until":
			if len(v) > 0 {
				if until, err = parseEventTime(v[0], time.Now()); err != nil {
					writeError(w, err)
					return
				}
			}
		default:
			writeError(w, errdefs.Invalid("invalid filter '%s'", k))
			return
		}
	}
	gone := s.Engine.NetworkPrune(func(n network.Network) bool {
		return allLabels(n.Labels, filters["label"]) && (until.IsZero() || n.Created.Before(until))
	})
	if gone == nil {
		gone = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"NetworksDeleted": gone})
}

// containerNetworks is NetworkSettings.Networks (and the network part of
// GET /containers/json): the user networks by name, or "host" for a
// container on the device network.
func containerNetworks(r engine.Record) map[string]any {
	if r.NetIP == "" || len(r.Networks) == 0 {
		// The device network, under the built-in name the container asked for.
		name := engine.DeviceMode(r)
		return map[string]any{name: map[string]any{
			"IPAMConfig": nil, "Links": nil, "Aliases": nil, "NetworkID": network.BuiltinID(name), "EndpointID": "",
			"Gateway": "", "IPAddress": "", "IPPrefixLen": 0, "IPv6Gateway": "", "GlobalIPv6Address": "",
			"GlobalIPv6PrefixLen": 0, "MacAddress": "", "DriverOpts": nil}}
	}
	out := map[string]any{}
	for id, ep := range r.Networks {
		aliases := ep.Aliases
		if aliases == nil {
			aliases = []string{}
		}
		out[ep.Name] = map[string]any{
			"IPAMConfig": nil, "Links": nil, "Aliases": aliases, "NetworkID": id, "EndpointID": ep.EndpointID,
			"Gateway": network.Gateway, "IPAddress": r.NetIP, "IPPrefixLen": network.PrefixLen,
			"IPv6Gateway": "", "GlobalIPv6Address": "", "GlobalIPv6PrefixLen": 0, "MacAddress": "", "DriverOpts": nil,
		}
	}
	return out
}

func networkMode(r engine.Record) string {
	if r.NetIP == "" || len(r.Networks) == 0 {
		if r.HostConfig.NetworkMode == "" {
			return "default"
		}
		return r.HostConfig.NetworkMode
	}
	return r.HostConfig.NetworkMode
}
