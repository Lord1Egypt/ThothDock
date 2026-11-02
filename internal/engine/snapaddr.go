package engine

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Lord1Egypt/ThothDock/internal/network"
)

// snapAddrs gives every sandbox of one snap the same loopback address, and
// the same one every time. Under snapd a snap's services and commands share
// the host's network: redis.cli reaches the redis server on localhost. With
// one address per snap, "localhost" inside a snap's sandboxes is that
// address, so a snap's own processes find each other, a service keeps the
// address people put in a browser or a trusted-domain list, and two snaps
// never share a port.
type snapAddrs struct {
	file string
	pool *network.Pool

	mu     sync.Mutex
	loaded bool
	ips    map[string]string // snap name -> address
}

func newSnapAddrs(file string, pool *network.Pool) *snapAddrs {
	return &snapAddrs{file: file, pool: pool, ips: map[string]string{}}
}

// snapOf is the snap an app ID ("snap:<snap>.<app>") belongs to, or "".
func snapOf(appID string) string {
	rest, ok := strings.CutPrefix(appID, "snap:")
	if !ok {
		return ""
	}
	name, _, _ := strings.Cut(rest, ".")
	return name
}

// load reads the saved addresses once, after the containers have claimed
// theirs: a snap whose address a container holds gets a new one.
func (s *snapAddrs) load() {
	if s.loaded {
		return
	}
	s.loaded = true
	b, err := os.ReadFile(s.file)
	if err != nil {
		return
	}
	var saved map[string]string
	if json.Unmarshal(b, &saved) != nil {
		return
	}
	for name, ip := range saved {
		if err := s.pool.Reserve(ip, "snap:"+name); err == nil {
			s.ips[name] = ip
		}
	}
}

func (s *snapAddrs) save() error {
	b, err := json.MarshalIndent(s.ips, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.file), 0o700); err != nil {
		return err
	}
	tmp := s.file + ".new"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.file)
}

// address is the snap's address, chosen on first use and kept for good.
func (s *snapAddrs) address(snap string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	if ip, ok := s.ips[snap]; ok {
		return ip, nil
	}
	ip, err := s.pool.Allocate("snap:" + snap)
	if err != nil {
		return "", err
	}
	s.ips[snap] = ip
	if err := s.save(); err != nil {
		delete(s.ips, snap)
		s.pool.Release(ip)
		return "", errors.Join(errors.New("sandbox: cannot save the snap's address"), err)
	}
	return ip, nil
}

// forget gives a removed snap's address back.
func (s *snapAddrs) forget(snap string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	if ip, ok := s.ips[snap]; ok {
		delete(s.ips, snap)
		s.pool.Release(ip)
		s.save()
	}
}

// ForgetSnapAddress gives a removed snap's address back to the pool.
func (e *Engine) ForgetSnapAddress(snap string) {
	if snap != "" && !strings.ContainsAny(snap, "./") {
		e.snapIPs.forget(snap)
	}
}
