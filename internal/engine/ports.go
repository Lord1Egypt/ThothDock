package engine

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/Lord1Egypt/ThothDock/internal/errdefs"
	"github.com/Lord1Egypt/ThothDock/internal/portmap"
)

// PortAssign is a published port while its container runs.
type PortAssign struct {
	HostIP        string
	HostPort      int
	ContainerPort int
	// Passthrough: the host port equals the container port, so no forwarder
	// is possible (it would collide with the service itself). The service is
	// reachable at its own bind address.
	Passthrough bool
}

type portPlan struct {
	hostIP        string
	hostPort      int // 0: ephemeral
	containerPort int
}

func splitPortKey(key string) (int, string, error) {
	num, proto, found := strings.Cut(key, "/")
	if !found {
		proto = "tcp"
	}
	n, err := strconv.Atoi(num)
	if err != nil || n < 1 || n > 65535 {
		return 0, "", errdefs.Invalid("invalid container port %q", key)
	}
	return n, strings.ToLower(proto), nil
}

// planPorts validates -p/-P. It returns what to publish and warnings.
func (e *Engine) planPorts(hc *HostConfig, exposed map[string]struct{}) ([]portPlan, []string, error) {
	var plans []portPlan
	var warnings []string
	explicit := map[int]bool{}
	taken := map[string]bool{}
	for key, bindings := range hc.PortBindings {
		cp, proto, err := splitPortKey(key)
		if err != nil {
			return nil, nil, err
		}
		if proto != "tcp" {
			return nil, nil, unsupported(strings.ToUpper(proto)+" port publishing", "only TCP is implemented; "+key+" was refused rather than silently ignored")
		}
		for _, b := range bindings {
			ip := b.HostIP
			switch {
			case ip == "":
				ip = "127.0.0.1"
			case net.ParseIP(ip) == nil:
				return nil, nil, errdefs.Invalid("invalid host IP address %q for %s", b.HostIP, key)
			case !net.ParseIP(ip).IsLoopback() && !e.cfg.AllowNonLoopbackPublish:
				return nil, nil, errdefs.Forbidden("publishing to %s is disabled: ports are published on 127.0.0.1 only unless the daemon is started with --allow-publish-nonlocal", ip)
			}
			hp := 0
			if b.HostPort != "" {
				hp, err = strconv.Atoi(b.HostPort)
				if err != nil || hp < 1 || hp > 65535 {
					return nil, nil, errdefs.Invalid("invalid host port %q for %s", b.HostPort, key)
				}
				if k := net.JoinHostPort(ip, b.HostPort); taken[k] {
					return nil, nil, errdefs.Invalid("host port %s is requested twice", k)
				} else {
					taken[k] = true
				}
			}
			explicit[cp] = true
			plans = append(plans, portPlan{hostIP: ip, hostPort: hp, containerPort: cp})
		}
	}
	if hc.PublishAllPorts {
		for key := range exposed {
			cp, proto, err := splitPortKey(key)
			if err != nil {
				return nil, nil, err
			}
			if proto != "tcp" {
				warnings = append(warnings, fmt.Sprintf("%s is not published: only TCP is implemented", key))
				continue
			}
			if !explicit[cp] {
				plans = append(plans, portPlan{hostIP: "127.0.0.1", containerPort: cp})
			}
		}
	}
	for _, p := range plans {
		if p.hostPort != 0 && p.hostPort == p.containerPort {
			warnings = append(warnings, fmt.Sprintf("host port %d equals container port %d: containers share the device network, so the service is already reachable at its own address and no forwarder is started (its bind address is up to the service)", p.hostPort, p.containerPort))
		}
	}
	return plans, warnings, nil
}

// openPorts binds the host side of every published port before the process
// starts, so a taken port fails the start cleanly. Called with c.mu held.
func (e *Engine) openPorts(c *Container) ([]*portmap.Forwarder, []PortAssign, error) {
	plans, _, err := e.planPorts(&c.rec.HostConfig, c.rec.Config.ExposedPorts)
	if err != nil {
		return nil, nil, err
	}
	var fws []*portmap.Forwarder
	var assigns []PortAssign
	closeAll := func() {
		for _, f := range fws {
			f.Close()
		}
	}
	for _, p := range plans {
		if p.hostPort != 0 && p.hostPort == p.containerPort {
			assigns = append(assigns, PortAssign{HostIP: p.hostIP, HostPort: p.hostPort, ContainerPort: p.containerPort, Passthrough: true})
			continue
		}
		f, err := portmap.Start(portmap.Binding{HostIP: p.hostIP, HostPort: p.hostPort, ContainerPort: p.containerPort})
		if err != nil {
			closeAll()
			var ae *portmap.AllocatedError
			if errors.As(err, &ae) {
				return nil, nil, errdefs.Conflict("driver failed programming external connectivity on endpoint %s: Bind for %s failed: port is already allocated", c.rec.Name, ae.Addr)
			}
			return nil, nil, err
		}
		fws = append(fws, f)
		assigns = append(assigns, PortAssign{HostIP: p.hostIP, HostPort: f.Binding.HostPort, ContainerPort: p.containerPort})
	}
	return fws, assigns, nil
}

// closePortsLocked releases the container's listeners; c.mu is held.
func (c *Container) closePortsLocked() {
	for _, f := range c.forwarders {
		f.Close()
	}
	c.forwarders, c.ports = nil, nil
}
