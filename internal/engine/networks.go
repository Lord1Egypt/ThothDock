package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Lord1Egypt/ThothDock/internal/errdefs"
	"github.com/Lord1Egypt/ThothDock/internal/events"
	"github.com/Lord1Egypt/ThothDock/internal/network"
	"github.com/Lord1Egypt/ThothDock/internal/store"
)

// EndpointRecord is a container's membership of a user-defined network.
type EndpointRecord struct {
	NetworkID  string   `json:"networkId"`
	Name       string   `json:"name"`
	EndpointID string   `json:"endpointId"`
	Aliases    []string `json:"aliases,omitempty"`
}

// EndpointRequest is one entry of NetworkingConfig.EndpointsConfig, and the
// EndpointConfig of POST /networks/{id}/connect.
type EndpointRequest struct {
	Aliases    []string `json:"Aliases"`
	Links      []string `json:"Links"`
	IPAMConfig *struct {
		IPv4Address  string   `json:"IPv4Address"`
		IPv6Address  string   `json:"IPv6Address"`
		LinkLocalIPs []string `json:"LinkLocalIPs"`
	} `json:"IPAMConfig"`
}

type networkingConfig struct {
	EndpointsConfig map[string]*EndpointRequest `json:"EndpointsConfig"`
}

// netIPRuntime is a runtime that can give a process its own loopback address.
type netIPRuntime interface{ SupportsNetIP() bool }

func (e *Engine) supportsNetIP() bool {
	r, ok := e.Runtime.(netIPRuntime)
	return ok && r.SupportsNetIP()
}

func (e *Engine) endpointFor(ref string, ep *EndpointRequest) (network.Network, EndpointRecord, error) {
	n, err := e.Networks.Lookup(ref)
	if err != nil {
		return n, EndpointRecord{}, err
	}
	if n.Builtin {
		return n, EndpointRecord{}, nil
	}
	rec := EndpointRecord{NetworkID: n.ID, Name: n.Name, EndpointID: newID()}
	if ep != nil {
		if ep.IPAMConfig != nil && (ep.IPAMConfig.IPv4Address != "" || ep.IPAMConfig.IPv6Address != "" || len(ep.IPAMConfig.LinkLocalIPs) > 0) {
			return n, rec, unsupported("static container addresses", "every container gets the next free address in "+network.Subnet)
		}
		if len(ep.Links) > 0 {
			return n, rec, unsupported("container links", "use a user-defined network and names instead")
		}
		for _, a := range ep.Aliases {
			if !validHostname(a) {
				return n, rec, errdefs.Invalid("invalid network alias %q", a)
			}
		}
		rec.Aliases = append([]string(nil), ep.Aliases...)
	}
	return n, rec, nil
}

// resolveNetworks reads HostConfig.NetworkMode and NetworkingConfig at
// create: the user-defined networks the container joins, by network ID.
func (e *Engine) resolveNetworks(mode string, raw json.RawMessage) (map[string]EndpointRecord, error) {
	var nc networkingConfig
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &nc); err != nil {
			return nil, errdefs.Invalid("invalid NetworkingConfig: %v", err)
		}
	}
	out := map[string]EndpointRecord{}
	add := func(ref string) error {
		n, rec, err := e.endpointFor(ref, nc.EndpointsConfig[ref])
		if err != nil || n.Builtin {
			return err
		}
		out[n.ID] = rec
		return nil
	}
	if strings.HasPrefix(mode, "container:") {
		return nil, unsupported("--network container:<name>", "planned; containers cannot share another container's address yet")
	}
	if !network.IsDeviceMode(mode) && mode != "none" {
		if err := add(mode); err != nil {
			return nil, err
		}
	}
	for name := range nc.EndpointsConfig {
		if name != mode {
			if err := add(name); err != nil {
				return nil, err
			}
		}
	}
	if len(out) > 0 && !e.supportsNetIP() {
		return nil, unsupported("user-defined networks", "this PRoot has no --net-ip option (Garden patch 0009)")
	}
	return out, nil
}

// hostEntry is one container as other containers' /etc/hosts list it.
type hostEntry struct {
	ip    string
	names []string
}

func shortID(id string) string { return id[:12] }

// hostsFor is /etc/hosts for a container on user-defined networks: itself,
// the gateway, then every peer that shares a network with it. The first
// line that names a host wins, so a name two networks share resolves in
// network-name order.
func hostsFor(self Record, all []Record) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("127.0.0.1\tlocalhost\n::1\tlocalhost ip6-localhost ip6-loopback\nfe00::0\tip6-localnet\nff00::0\tip6-mcastprefix\nff02::1\tip6-allnodes\nff02::2\tip6-allrouters\n")
	seen := map[string]bool{"localhost": true}
	line := func(ip string, names ...string) {
		var keep []string
		for _, n := range names {
			if n != "" && !seen[n] {
				seen[n] = true
				keep = append(keep, n)
			}
		}
		if len(keep) > 0 {
			fmt.Fprintf(&b, "%s\t%s\n", ip, strings.Join(keep, " "))
		}
	}
	selfNames := []string{self.Config.Hostname, self.Name, shortID(self.ID)}
	nets := make([]EndpointRecord, 0, len(self.Networks))
	for _, ep := range self.Networks {
		nets = append(nets, ep)
	}
	sort.Slice(nets, func(i, j int) bool { return nets[i].Name < nets[j].Name })
	for _, ep := range nets {
		selfNames = append(selfNames, ep.Aliases...)
	}
	line(self.NetIP, selfNames...)
	line(network.Gateway, "host.docker.internal", "gateway.docker.internal")
	for _, ep := range nets {
		var peers []Record
		for _, p := range all {
			if _, member := p.Networks[ep.NetworkID]; member && p.ID != self.ID && p.NetIP != "" {
				peers = append(peers, p)
			}
		}
		sort.Slice(peers, func(i, j int) bool { return peers[i].Name < peers[j].Name })
		for _, p := range peers {
			names := append([]string{p.Name}, p.Networks[ep.NetworkID].Aliases...)
			line(p.NetIP, append(names, shortID(p.ID), p.Config.Hostname)...)
		}
	}
	for _, h := range self.HostConfig.ExtraHosts {
		name, ip, ok := strings.Cut(h, ":")
		if !ok {
			return nil, errdefs.Invalid("invalid extra host %q: want host:ip", h)
		}
		if ip == "host-gateway" {
			ip = network.Gateway
		}
		if net.ParseIP(ip) == nil || !validHostname(name) {
			return nil, errdefs.Invalid("invalid extra host %q", h)
		}
		fmt.Fprintf(&b, "%s\t%s\n", ip, name)
	}
	return b.Bytes(), nil
}

// syncHosts rewrites /etc/hosts of every container on the given networks
// (nil: on any user network), and of the containers named in also (one that
// just left a network). A container without an address gets the device
// network's hosts file back. Each file is replaced atomically; PRoot opens
// the path afresh on every lookup, so running containers see the change.
func (e *Engine) syncHosts(netIDs map[string]bool, also ...string) {
	e.hostsMu.Lock()
	defer e.hostsMu.Unlock()
	all := e.List()
	for _, r := range all {
		affected := netIDs == nil && r.NetIP != ""
		for id := range r.Networks {
			affected = affected || netIDs[id]
		}
		for _, id := range also {
			affected = affected || r.ID == id
		}
		if !affected {
			continue
		}
		var data []byte
		var err error
		if r.NetIP != "" {
			data, err = hostsFor(r, all)
		} else {
			data, err = hostsFile(r.Config.Hostname, r.HostConfig.ExtraHosts)
		}
		if err == nil {
			err = store.WriteFileAtomic(filepath.Join(e.Layout.Containers(), r.ID, "hosts"), data, 0o644)
		}
		if err != nil {
			e.log.Warn("rewriting a container's hosts file", "id", shortID(r.ID), "err", err)
		}
	}
}

func netSet(eps map[string]EndpointRecord) map[string]bool {
	out := map[string]bool{}
	for id := range eps {
		out[id] = true
	}
	return out
}

func (e *Engine) networkEvent(n network.Network, action string, extra ...string) {
	attrs := map[string]string{"name": n.Name, "type": n.Driver}
	for i := 0; i+1 < len(extra); i += 2 {
		attrs[extra[i]] = extra[i+1]
	}
	e.Events.Publish(events.Event{Type: "network", Action: action, ID: n.ID, Attrs: attrs})
}

// NetworkCreate adds a user-defined network.
func (e *Engine) NetworkCreate(req network.CreateRequest) (network.Network, error) {
	if !e.supportsNetIP() {
		return network.Network{}, unsupported("user-defined networks", "this PRoot has no --net-ip option (Garden patch 0009)")
	}
	n, err := e.Networks.Create(req)
	if err != nil {
		return n, err
	}
	e.networkEvent(n, "create")
	return n, nil
}

// NetworkUsers lists the containers attached to a network.
func (e *Engine) NetworkUsers(id string) []Record {
	var out []Record
	for _, r := range e.List() {
		if _, ok := r.Networks[id]; ok {
			out = append(out, r)
		}
	}
	return out
}

// NetworkRemove deletes a user-defined network nothing is attached to.
func (e *Engine) NetworkRemove(ref string) error {
	n, err := e.Networks.Lookup(ref)
	if err != nil {
		return err
	}
	if n.Builtin {
		return errdefs.Forbidden("%s is a pre-defined network and cannot be removed", n.Name)
	}
	if users := e.NetworkUsers(n.ID); len(users) > 0 {
		names := make([]string, len(users))
		for i, u := range users {
			names[i] = u.Name
		}
		return errdefs.Forbidden("error while removing network: network %s id %s has active endpoints (%s)", n.Name, n.ID, strings.Join(names, ", "))
	}
	if err := e.Networks.Remove(n.ID); err != nil {
		return err
	}
	e.networkEvent(n, "destroy")
	return nil
}

// NetworkConnect attaches a container. A running container that is still on
// the device network cannot get an address until it restarts, so that case
// is refused rather than half-applied.
func (e *Engine) NetworkConnect(netRef, ctrRef string, ep *EndpointRequest) error {
	n, rec, err := e.endpointFor(netRef, ep)
	if err != nil {
		return err
	}
	if n.Builtin {
		return errdefs.Forbidden("ThothDock cannot connect a container to the pre-defined %s network: it is the device network every container already shares", n.Name)
	}
	if !e.supportsNetIP() {
		return unsupported("user-defined networks", "this PRoot has no --net-ip option (Garden patch 0009)")
	}
	c, err := e.Lookup(ctrRef)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if _, ok := c.rec.Networks[n.ID]; ok {
		c.mu.Unlock()
		return errdefs.Forbidden("endpoint with name %s already exists in network %s", c.rec.Name, n.Name)
	}
	if c.rec.NetIP == "" {
		if IsRunning(c.rec.State.Status) {
			c.mu.Unlock()
			return errdefs.Conflict("container %s is running on the device network: stop it, connect it, then start it again (its address is applied at start)", c.rec.Name)
		}
		ip, err := e.addrs.Allocate(c.rec.ID)
		if err != nil {
			c.mu.Unlock()
			return err
		}
		c.rec.NetIP = ip
	}
	if c.rec.Networks == nil {
		c.rec.Networks = map[string]EndpointRecord{}
	}
	c.rec.Networks[n.ID] = rec
	err = e.persist(c)
	c.mu.Unlock()
	if err != nil {
		return err
	}
	e.syncHosts(map[string]bool{n.ID: true})
	e.networkEvent(n, "connect", "container", c.rec.ID)
	return nil
}

// NetworkDisconnect detaches a container. Its address stays while it is a
// member of any user-defined network, or while it runs.
func (e *Engine) NetworkDisconnect(netRef, ctrRef string) error {
	n, err := e.Networks.Lookup(netRef)
	if err != nil {
		return err
	}
	c, err := e.Lookup(ctrRef)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if _, ok := c.rec.Networks[n.ID]; !ok {
		c.mu.Unlock()
		return errdefs.Forbidden("container %s is not connected to network %s", c.rec.Name, n.Name)
	}
	delete(c.rec.Networks, n.ID)
	if len(c.rec.Networks) == 0 && !IsRunning(c.rec.State.Status) && c.rec.NetIP != "" {
		e.addrs.Release(c.rec.NetIP)
		c.rec.NetIP = ""
	}
	err = e.persist(c)
	c.mu.Unlock()
	if err != nil {
		return err
	}
	e.syncHosts(map[string]bool{n.ID: true}, c.rec.ID)
	e.networkEvent(n, "disconnect", "container", c.rec.ID)
	return nil
}

// NetworkPrune removes the user-defined networks nothing is attached to and
// that match.
func (e *Engine) NetworkPrune(match func(network.Network) bool) []string {
	var gone []string
	for _, n := range e.Networks.List() {
		if n.Builtin || !match(n) || len(e.NetworkUsers(n.ID)) > 0 {
			continue
		}
		if e.Networks.Remove(n.ID) == nil {
			e.networkEvent(n, "destroy")
			gone = append(gone, n.Name)
		}
	}
	return gone
}
