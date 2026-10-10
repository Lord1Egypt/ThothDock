package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Lord1Egypt/ThothDock/internal/errdefs"
	"github.com/Lord1Egypt/ThothDock/internal/network"
)

func (f *fixture) onNetwork(name, netName string, aliases ...string) string {
	f.t.Helper()
	nc, _ := json.Marshal(map[string]any{"EndpointsConfig": map[string]any{netName: map[string]any{"Aliases": aliases}}})
	id, _, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image, Entrypoint: StrSlice{"sh"}, Cmd: []string{"run"}},
		HostConfig: &HostConfig{NetworkMode: netName}, NetworkingConfig: nc}, name, "")
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *fixture) hosts(id string) string {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.layout.Containers(), id, "hosts"))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(b)
}

func (f *fixture) rec(id string) Record {
	c, err := f.e.Lookup(id)
	if err != nil {
		f.t.Fatal(err)
	}
	return c.Snapshot()
}

func hasLine(hosts, line string) bool {
	for _, l := range strings.Split(hosts, "\n") {
		if l == line {
			return true
		}
	}
	return false
}

func TestNetworkMembersGetAddressesAndNames(t *testing.T) {
	f := newFixture(t)
	f.program()
	n, err := f.e.NetworkCreate(network.CreateRequest{Name: "demo_default"})
	if err != nil {
		t.Fatal(err)
	}
	api := f.onNetwork("demo-api-1", "demo_default", "api")
	web := f.onNetwork("demo-web-1", "demo_default", "web")
	ra, rw := f.rec(api), f.rec(web)
	if ra.NetIP != "127.77.0.2" || rw.NetIP != "127.77.0.3" {
		t.Fatalf("addresses %s %s", ra.NetIP, rw.NetIP)
	}
	if ep := rw.Networks[n.ID]; ep.Name != "demo_default" || len(ep.Aliases) != 1 || ep.Aliases[0] != "web" {
		t.Fatalf("endpoint %+v", ep)
	}
	// The first container's hosts file learnt about the second one.
	h := f.hosts(api)
	for _, want := range []string{
		"127.77.0.2\t" + api[:12] + " demo-api-1 api",
		"127.77.0.1\thost.docker.internal gateway.docker.internal",
		"127.77.0.3\tdemo-web-1 web " + web[:12],
	} {
		if !hasLine(h, want) {
			t.Fatalf("api hosts lacks %q:\n%s", want, h)
		}
	}
	if !hasLine(f.hosts(web), "127.77.0.2\tdemo-api-1 api "+api[:12]) {
		t.Fatalf("web hosts:\n%s", f.hosts(web))
	}
	// Both processes get their own address.
	f.e.Start(api)
	f.e.Start(web)
	if f.rt.Started[0].NetIP != "127.77.0.2" || f.rt.Started[1].NetIP != "127.77.0.3" {
		t.Fatalf("specs %q %q", f.rt.Started[0].NetIP, f.rt.Started[1].NetIP)
	}
	// In use: the network cannot go away.
	if err := f.e.NetworkRemove("demo_default"); errdefs.KindOf(err) != errdefs.KindForbidden {
		t.Fatalf("rm in use: %v", err)
	}
	// Removing a member frees its address and its peers forget it.
	f.e.Remove(web, true)
	if strings.Contains(f.hosts(api), "demo-web-1") {
		t.Fatalf("removed peer still listed:\n%s", f.hosts(api))
	}
	again := f.onNetwork("demo-web-2", "demo_default", "web")
	if f.rec(again).NetIP != "127.77.0.3" {
		t.Fatalf("freed address not reused: %s", f.rec(again).NetIP)
	}
	f.e.Shutdown(1)
}

func TestAddressesSurviveADaemonRestart(t *testing.T) {
	f := newFixture(t)
	f.e.NetworkCreate(network.CreateRequest{Name: "n1"})
	a := f.onNetwork("ca", "n1")
	f.open()
	b := f.onNetwork("cb", "n1")
	if f.rec(a).NetIP != "127.77.0.2" || f.rec(b).NetIP != "127.77.0.3" {
		t.Fatalf("after restart: a=%s b=%s", f.rec(a).NetIP, f.rec(b).NetIP)
	}
	if _, err := f.e.Networks.Lookup("n1"); err != nil {
		t.Fatalf("network not persisted: %v", err)
	}
}

func TestPublishedPortOfAnAddressedContainerTargetsItsAddress(t *testing.T) {
	f := newFixture(t)
	f.program()
	f.e.NetworkCreate(network.CreateRequest{Name: "n1"})
	free := freePort(t)
	nc, _ := json.Marshal(map[string]any{"EndpointsConfig": map[string]any{"n1": map[string]any{}}})
	// host port == container port is fine here: the addresses differ.
	id, warnings, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image, Entrypoint: StrSlice{"sh"}, Cmd: []string{"run"}},
		HostConfig: &HostConfig{NetworkMode: "n1", PortBindings: map[string][]PortBinding{
			"80/tcp": {{HostPort: itoa(free)}}, itoa(free) + "/tcp": {{HostPort: ""}}}},
		NetworkingConfig: nc}, "pub", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range warnings {
		if strings.Contains(w, "no forwarder") {
			t.Fatalf("passthrough warning for an addressed container: %v", warnings)
		}
	}
	if err := f.e.Start(id); err != nil {
		t.Fatal(err)
	}
	c, _ := f.e.Lookup(id)
	c.mu.Lock()
	targets := map[string]bool{}
	for _, fw := range c.forwarders {
		targets[fw.Binding.Target] = true
	}
	c.mu.Unlock()
	if !targets["127.77.0.2:30080"] || !targets["127.77.0.2:"+itoa(free)] {
		t.Fatalf("forwarder targets %v", targets)
	}
	f.e.Shutdown(1)
}

func TestConnectDisconnect(t *testing.T) {
	f := newFixture(t)
	f.program()
	f.e.NetworkCreate(network.CreateRequest{Name: "front"})
	f.e.NetworkCreate(network.CreateRequest{Name: "back"})
	db := f.onNetwork("db", "back", "database")
	app := f.onNetwork("app", "front")
	if strings.Contains(f.hosts(app), "database") {
		t.Fatal("app sees db before sharing a network")
	}
	if err := f.e.NetworkConnect("back", "app", &EndpointRequest{Aliases: []string{"api"}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.hosts(app), "database") || !strings.Contains(f.hosts(db), " api ") {
		t.Fatalf("after connect:\napp:\n%s\ndb:\n%s", f.hosts(app), f.hosts(db))
	}
	if f.rec(app).NetIP != "127.77.0.3" {
		t.Fatal("connect changed the address")
	}
	if err := f.e.NetworkConnect("back", "app", nil); errdefs.KindOf(err) != errdefs.KindForbidden {
		t.Fatalf("double connect: %v", err)
	}
	if err := f.e.NetworkDisconnect("back", "app"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.hosts(app), "database") || strings.Contains(f.hosts(db), "app") {
		t.Fatal("still resolvable after disconnect")
	}
	// A device-network container: connect works while stopped, and it gets an address.
	plain := f.create("sh")
	if err := f.e.NetworkConnect("front", plain, nil); err != nil {
		t.Fatal(err)
	}
	if ip := f.rec(plain).NetIP; ip == "" {
		t.Fatal("no address after connect")
	}
	// ... but not while it runs on the device network.
	running := f.createWith(RestartPolicy{}, "run")
	f.e.Start(running)
	if err := f.e.NetworkConnect("front", running, nil); errdefs.KindOf(err) != errdefs.KindConflict {
		t.Fatalf("connect a running device-network container: %v", err)
	}
	if err := f.e.NetworkConnect("host", plain, nil); errdefs.KindOf(err) != errdefs.KindForbidden {
		t.Fatalf("connect to host: %v", err)
	}
	// Disconnected from its last network, a stopped container returns its address.
	f.e.NetworkDisconnect("front", plain)
	if f.rec(plain).NetIP != "" {
		t.Fatal("address kept with no network")
	}
	f.e.Shutdown(1)
}

func TestNetworkCreateRefusals(t *testing.T) {
	f := newFixture(t)
	for _, mode := range []string{"container:abc"} {
		if _, _, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image}, HostConfig: &HostConfig{NetworkMode: mode}}, "", ""); errdefs.KindOf(err) != errdefs.KindUnsupported {
			t.Fatalf("%s: %v", mode, err)
		}
	}
	if _, _, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image}, HostConfig: &HostConfig{NetworkMode: "nosuch"}}, "", ""); errdefs.KindOf(err) != errdefs.KindNotFound {
		t.Fatalf("missing network: %v", err)
	}
	f.e.NetworkCreate(network.CreateRequest{Name: "n1"})
	nc, _ := json.Marshal(map[string]any{"EndpointsConfig": map[string]any{"n1": map[string]any{"IPAMConfig": map[string]any{"IPv4Address": "127.77.0.9"}}}})
	if _, _, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image}, HostConfig: &HostConfig{NetworkMode: "n1"}, NetworkingConfig: nc}, "", ""); errdefs.KindOf(err) != errdefs.KindUnsupported {
		t.Fatalf("static IP: %v", err)
	}
	if len(f.e.List()) != 0 {
		t.Fatal("refused creates left containers")
	}
	// A PRoot without --net-ip: user networks are refused, everything else works.
	f.rt.NoNetIP = true
	if _, err := f.e.NetworkCreate(network.CreateRequest{Name: "n2"}); errdefs.KindOf(err) != errdefs.KindUnsupported {
		t.Fatalf("network create without --net-ip: %v", err)
	}
	if _, _, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image}, HostConfig: &HostConfig{NetworkMode: "n1"}}, "", ""); errdefs.KindOf(err) != errdefs.KindUnsupported {
		t.Fatalf("create on a network without --net-ip: %v", err)
	}
	if _, _, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image}}, "", ""); err != nil {
		t.Fatalf("device-network create without --net-ip: %v", err)
	}
}

func TestExtraHostsHostGatewayOnANetwork(t *testing.T) {
	f := newFixture(t)
	f.e.NetworkCreate(network.CreateRequest{Name: "n1"})
	nc, _ := json.Marshal(map[string]any{"EndpointsConfig": map[string]any{"n1": map[string]any{}}})
	id, _, err := f.e.Create(CreateRequest{ContainerConfig: ContainerConfig{Image: f.image},
		HostConfig: &HostConfig{NetworkMode: "n1", ExtraHosts: []string{"db.local:host-gateway"}}, NetworkingConfig: nc}, "xx", "")
	if err != nil {
		t.Fatal(err)
	}
	if !hasLine(f.hosts(id), "127.77.0.1\tdb.local") {
		t.Fatalf("hosts:\n%s", f.hosts(id))
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
