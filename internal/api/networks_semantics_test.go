package api

import (
	"encoding/json"
	"strings"
	"testing"
)

type thothNet struct {
	Name       string
	Labels     map[string]string
	Driver     string
	Containers map[string]struct{ Name, IPv4Address string }
	IPAM       struct{ Config []struct{ Subnet string } }
	ThothDock  struct {
		Kind       string
		Builtin    bool
		Supported  bool
		Summary    string
		Isolation  string
		Subnet     string
		Attached   int
		Operations []string
	}
}

func (f *apiFixture) net(t *testing.T, name string) thothNet {
	t.Helper()
	resp, body := f.do(t, "GET", "/networks/"+name, "")
	expect(t, resp, body, 200, `"Name":"`+name+`"`)
	var n thothNet
	if err := json.Unmarshal([]byte(body), &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f *apiFixture) run(t *testing.T, name, mode string) {
	t.Helper()
	resp, body := f.do(t, "POST", "/containers/create?name="+name, `{"Image":"`+f.image+`","HostConfig":{"NetworkMode":"`+mode+`"}`+
		map[bool]string{true: `,"NetworkingConfig":{"EndpointsConfig":{"` + mode + `":{"Aliases":["al-` + name + `"]}}}`, false: ""}[mode == "demo"]+`}`)
	expect(t, resp, body, 201, "Id")
}

// The built-in networks tell the truth, and the user network says what it is.
func TestNetworkSemanticsAreReportedByTheBackend(t *testing.T) {
	f := newAPI(t)
	f.pull(t)
	resp, body := f.do(t, "POST", "/networks/create", `{"Name":"demo"}`)
	expect(t, resp, body, 201, "Id")
	f.run(t, "plain", "")
	f.run(t, "viabridge", "bridge")
	f.run(t, "viahost", "host")
	f.run(t, "member", "demo")

	bridge, host, none, demo := f.net(t, "bridge"), f.net(t, "host"), f.net(t, "none"), f.net(t, "demo")
	for _, c := range []struct {
		n         thothNet
		kind      string
		supported bool
		attached  int
		ops       string
	}{
		{bridge, "device-bridge", true, 2, "inspect"},
		{host, "device-host", true, 1, "inspect"},
		{none, "unsupported", false, 0, "inspect"},
		{demo, "user-defined", true, 1, "inspect,connect,disconnect,remove"},
	} {
		td := c.n.ThothDock
		if td.Kind != c.kind || td.Supported != c.supported || td.Attached != c.attached || strings.Join(td.Operations, ",") != c.ops {
			t.Fatalf("%s: %+v", c.n.Name, td)
		}
		if td.Summary == "" {
			t.Fatalf("%s has no summary", c.n.Name)
		}
	}
	if len(bridge.IPAM.Config) != 0 || len(host.IPAM.Config) != 0 || len(none.IPAM.Config) != 0 {
		t.Fatal("a built-in network reports an address range it does not have")
	}
	if len(demo.IPAM.Config) != 1 || demo.IPAM.Config[0].Subnet != "127.77.0.0/16" || demo.ThothDock.Subnet != "127.77.0.0/16" {
		t.Fatalf("user network range: %+v", demo.IPAM.Config)
	}
	if !strings.Contains(demo.ThothDock.Isolation, "not enforced isolation") {
		t.Fatalf("a user network must disclaim kernel isolation: %q", demo.ThothDock.Isolation)
	}
	if !strings.Contains(strings.ToLower(bridge.ThothDock.Summary), "not a linux docker bridge") {
		t.Fatalf("bridge summary: %q", bridge.ThothDock.Summary)
	}
	// docker network inspect (the stock CLI) keeps labels, so they carry the truth too.
	if none.Labels["io.thothdock.network.supported"] != "false" || bridge.Labels["io.thothdock.network.kind"] != "device-bridge" {
		t.Fatalf("labels: %v / %v", none.Labels, bridge.Labels)
	}
	if len(none.Containers) != 0 || len(demo.Containers) != 1 || len(bridge.Containers) != 2 {
		t.Fatalf("members: none=%d demo=%d bridge=%d", len(none.Containers), len(demo.Containers), len(bridge.Containers))
	}
	for _, c := range bridge.Containers {
		if c.IPv4Address != "" {
			t.Fatalf("a device-network container has no address of its own: %q", c.IPv4Address)
		}
	}
	// A user network's labels are the user's own.
	if _, has := demo.Labels["io.thothdock.network.kind"]; has {
		t.Fatal("ThothDock labels leaked onto a user network")
	}

	// Container inspect names the built-in network the container asked for.
	_, body = f.do(t, "GET", "/containers/viahost/json", "")
	if !strings.Contains(body, `"NetworkMode":"host"`) || !strings.Contains(body, `"host":{`) {
		t.Fatalf("host container: %s", body)
	}
	_, body = f.do(t, "GET", "/containers/plain/json", "")
	if !strings.Contains(body, `"NetworkMode":"default"`) || !strings.Contains(body, `"bridge":{`) {
		t.Fatalf("default container: %s", body)
	}
}

// Unsupported network configurations fail loudly and never fall back to the device network.
func TestUnsupportedNetworkModesFailLoudly(t *testing.T) {
	f := newAPI(t)
	f.pull(t)
	for _, mode := range []string{"none", "container:abc", "overlay", "ns:/proc/1/ns/net"} {
		resp, body := f.do(t, "POST", "/containers/create?name=xx", `{"Image":"`+f.image+`","HostConfig":{"NetworkMode":"`+mode+`"}}`)
		if resp.StatusCode < 400 {
			t.Fatalf("--network %s was accepted: %d %s", mode, resp.StatusCode, body)
		}
	}
	resp, body := f.do(t, "POST", "/containers/create?name=xx", `{"Image":"`+f.image+`","HostConfig":{"NetworkMode":"none"}}`)
	expect(t, resp, body, 501, "--network none")
	_, body = f.do(t, "GET", "/containers/json?all=1", "")
	if strings.TrimSpace(body) != "[]" {
		t.Fatalf("a refused create left a container: %s", body)
	}
	f.do(t, "POST", "/networks/create", `{"Name":"demo"}`)
	f.run(t, "c1", "")
	for _, c := range []struct {
		net, verb string
		status    int
		contains  string
	}{
		{"none", "connect", 501, "network-disabled mode is not implemented"},
		{"none", "disconnect", 501, "network-disabled mode is not implemented"},
		{"bridge", "connect", 403, "Android device network"},
		{"host", "connect", 403, "Android device network"},
		{"host", "disconnect", 403, "Android device network"},
		{"nosuch", "connect", 404, "network nosuch not found"},
		{"demo", "connect", 404, "No such container: ghost"},
	} {
		ctr := "c1"
		if c.contains == "No such container: ghost" {
			ctr = "ghost"
		}
		resp, body := f.do(t, "POST", "/networks/"+c.net+"/"+c.verb, `{"Container":"`+ctr+`"}`)
		expect(t, resp, body, c.status, c.contains)
	}
	// Joining twice and leaving a network never joined are refused.
	resp, body = f.do(t, "POST", "/networks/demo/disconnect", `{"Container":"c1"}`)
	expect(t, resp, body, 403, "is not connected to network demo")
	resp, body = f.do(t, "POST", "/networks/create", `{"Name":"none"}`)
	expect(t, resp, body, 403, "pre-defined network")
	resp, body = f.do(t, "DELETE", "/networks/none", "")
	expect(t, resp, body, 403, "pre-defined network")
}
