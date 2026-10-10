// Package network holds ThothDock's user-defined networks and the loopback
// address pool their containers draw from (docs/nextgen/adr/ADR-0002).
//
// A network is a name and a membership list. A container attached to any
// user-defined network gets one address in 127.77.0.0/16, which PRoot's
// --net-ip makes its own: wildcard and loopback binds land there, and its
// localhost is private. Containers find each other through /etc/hosts.
package network

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io/fs"
	"net"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Lord1Egypt/ThothDock/internal/errdefs"
	"github.com/Lord1Egypt/ThothDock/internal/store"
)

// The address plan every user-defined network shares.
const (
	Subnet    = "127.77.0.0/16"
	Gateway   = "127.77.0.1"
	PrefixLen = 16
	// LowPortShift moves container ports 1-1023 to an unprivileged range on
	// container addresses (PRoot does the same on bind and connect).
	LowPortShift = 30000
)

// Shift is the real port behind a container port on a container address.
func Shift(port int) int {
	if port >= 1 && port <= 1023 {
		return port + LowPortShift
	}
	return port
}

// Network is a network as the API reports it.
type Network struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`
	Created    time.Time         `json:"created"`
	Labels     map[string]string `json:"labels,omitempty"`
	Options    map[string]string `json:"options,omitempty"`
	Attachable bool              `json:"attachable,omitempty"`
	// Builtin marks host, bridge and none, which always exist, are never
	// stored and keep the device-network behaviour.
	Builtin bool `json:"-"`
}

// BuiltinID is the fixed ID of the built-in network called name.
func BuiltinID(name string) string { return builtinID(name) }

func builtinID(name string) string {
	h := sha256.Sum256([]byte("thothdock-builtin-network:" + name))
	return hex.EncodeToString(h[:])
}

var builtins = []Network{
	{ID: builtinID("bridge"), Name: "bridge", Driver: "bridge", Builtin: true},
	{ID: builtinID("host"), Name: "host", Driver: "host", Builtin: true},
	{ID: builtinID("none"), Name: "none", Driver: "null", Builtin: true},
}

// IsDeviceMode reports whether a HostConfig.NetworkMode means the device
// network, as in ThothDock v0.1.1.
func IsDeviceMode(mode string) bool {
	switch mode {
	case "", "default", "bridge", "host":
		return true
	}
	return false
}

// Kind says what a network really is. The Engine API has no field for this,
// so ThothDock reports it itself (labels on the built-in networks, the
// "ThothDock" object in network JSON) and the Web Panel and app show it.
const (
	KindDeviceBridge = "device-bridge" // "bridge": the Android device network, not a Linux bridge
	KindDeviceHost   = "device-host"   // "host": the Android device network
	KindUnsupported  = "unsupported"   // "none": network-disabled mode is not implemented
	KindUserDefined  = "user-defined"  // per-container loopback addresses and names
)

// Info is the truthful description of one network.
type Info struct {
	Kind       string
	Builtin    bool
	Supported  bool
	Summary    string
	Isolation  string
	Subnet     string
	Operations []string
}

// IsolationNote is the limit every user-defined network shares.
const IsolationNote = "Functional address separation, not enforced isolation: a container that knows another container's 127.77.x.y address can reach it whatever network it is on."

// Describe reports what the network is and which operations it supports.
func Describe(n Network) Info {
	switch {
	case !n.Builtin:
		return Info{Kind: KindUserDefined, Supported: true, Subnet: Subnet,
			Summary:    "Userspace network: every container gets its own loopback address (" + Subnet + ") and the names and aliases of its members resolve through /etc/hosts.",
			Isolation:  IsolationNote,
			Operations: []string{"inspect", "connect", "disconnect", "remove"}}
	case n.Name == "bridge":
		return Info{Kind: KindDeviceBridge, Builtin: true, Supported: true,
			Summary:    "Compatibility mode: the shared Android device network. It is not a Linux Docker bridge and has no address range of its own.",
			Isolation:  "None: containers share the phone's network stack.",
			Operations: []string{"inspect"}}
	case n.Name == "host":
		return Info{Kind: KindDeviceHost, Builtin: true, Supported: true,
			Summary:    "The shared Android device network, as with --network host.",
			Isolation:  "None: containers share the phone's network stack.",
			Operations: []string{"inspect"}}
	default:
		return Info{Kind: KindUnsupported, Builtin: true, Supported: false,
			Summary:   "Not implemented: --network none is refused. There is no network namespace to disable networking in, so ThothDock does not pretend to.",
			Isolation: "Not available.", Operations: []string{"inspect"}}
	}
}

// Labels are the ThothDock labels the Engine API shows on a built-in
// network, so docker network inspect tells the truth too.
func (i Info) Labels() map[string]string {
	if !i.Builtin {
		return nil
	}
	return map[string]string{
		"io.thothdock.network.kind":      i.Kind,
		"io.thothdock.network.supported": map[bool]string{true: "true", false: "false"}[i.Supported],
		"io.thothdock.network.note":      i.Summary,
	}
}

var nameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// CreateRequest is what POST /networks/create may ask for.
type CreateRequest struct {
	Name       string
	Driver     string
	Labels     map[string]string
	Options    map[string]string
	Attachable bool
}

type file struct {
	Networks map[string]*Network `json:"networks"`
}

// Store persists user-defined networks in one JSON file.
type Store struct {
	mu   sync.Mutex
	path string
	nets map[string]*Network
}

// Open reads the store; a missing file is an empty store.
func Open(path string) (*Store, error) {
	s := &Store{path: path, nets: map[string]*Network{}}
	var f file
	if err := store.ReadJSON(path, &f); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for id, n := range f.Networks {
		if n == nil || n.ID != id {
			return nil, errdefs.Invalid("network store %s is inconsistent at %q", path, id)
		}
		s.nets[id] = n
	}
	return s, nil
}

func (s *Store) saveLocked() error {
	return store.WriteJSONAtomic(s.path, &file{Networks: s.nets})
}

// Create adds a user-defined network.
func (s *Store) Create(req CreateRequest) (Network, error) {
	if req.Name == "" {
		return Network{}, errdefs.Invalid("network name is required")
	}
	if len(req.Name) > 128 || !nameRe.MatchString(req.Name) {
		return Network{}, errdefs.Invalid("invalid network name %q: only [a-zA-Z0-9][a-zA-Z0-9_.-] are allowed", req.Name)
	}
	switch req.Driver {
	case "", "bridge":
	default:
		return Network{}, errdefs.Unsupported("ThothDock does not support the %q network driver: only bridge-like user networks exist, on loopback addresses (see docs/COMPATIBILITY.md)", req.Driver)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range builtins {
		if b.Name == req.Name {
			return Network{}, errdefs.Forbidden("%s is a pre-defined network and cannot be created", req.Name)
		}
	}
	for _, n := range s.nets {
		if n.Name == req.Name {
			return Network{}, errdefs.Conflict("network with name %s already exists", req.Name)
		}
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return Network{}, err
	}
	n := &Network{ID: hex.EncodeToString(b[:]), Name: req.Name, Driver: "bridge", Created: time.Now().UTC(),
		Labels: req.Labels, Options: req.Options, Attachable: req.Attachable}
	s.nets[n.ID] = n
	if err := s.saveLocked(); err != nil {
		delete(s.nets, n.ID)
		return Network{}, err
	}
	return *n, nil
}

// Lookup resolves a full ID, a name or a unique ID prefix; built-in
// networks included.
func (s *Store) Lookup(ref string) (Network, error) {
	if ref == "" {
		return Network{}, errdefs.Invalid("empty network reference")
	}
	for _, b := range builtins {
		if b.Name == ref || b.ID == ref {
			return b, nil
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if n, ok := s.nets[ref]; ok {
		return *n, nil
	}
	for _, n := range s.nets {
		if n.Name == ref {
			return *n, nil
		}
	}
	var found *Network
	for id, n := range s.nets {
		if strings.HasPrefix(id, ref) {
			if found != nil {
				return Network{}, errdefs.Invalid("network %s is ambiguous", ref)
			}
			found = n
		}
	}
	if found == nil {
		return Network{}, errdefs.NotFound("network %s not found", ref)
	}
	return *found, nil
}

// List returns the built-in networks, then user networks by name.
func (s *Store) List() []Network {
	s.mu.Lock()
	user := make([]Network, 0, len(s.nets))
	for _, n := range s.nets {
		user = append(user, *n)
	}
	s.mu.Unlock()
	sort.Slice(user, func(i, j int) bool { return user[i].Name < user[j].Name })
	return append(append([]Network{}, builtins...), user...)
}

// Remove deletes a user network. The caller checks that nothing uses it.
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.nets[id]
	if !ok {
		return errdefs.NotFound("network %s not found", id)
	}
	delete(s.nets, id)
	if err := s.saveLocked(); err != nil {
		s.nets[id] = n
		return err
	}
	return nil
}

// Pool hands out container addresses: 127.77.0.2 to 127.77.255.254, lowest
// free first. It is rebuilt from the container records at start.
type Pool struct {
	mu   sync.Mutex
	used map[uint32]string // address -> owner (container ID)
}

// NewPool returns an empty pool.
func NewPool() *Pool { return &Pool{used: map[uint32]string{}} }

const (
	poolFirst = 0x7f4d0002 // 127.77.0.2
	poolLast  = 0x7f4dfffe // 127.77.255.254
)

func toU32(ip string) (uint32, bool) {
	p := net.ParseIP(ip).To4()
	if p == nil {
		return 0, false
	}
	return binary.BigEndian.Uint32(p), true
}

func fromU32(a uint32) string {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], a)
	return net.IP(b[:]).String()
}

// Reserve records an address already owned (from a persisted record).
func (p *Pool) Reserve(ip, owner string) error {
	a, ok := toU32(ip)
	if !ok || a < poolFirst || a > poolLast {
		return errdefs.Invalid("address %s is outside the container pool %s", ip, Subnet)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if o, taken := p.used[a]; taken && o != owner {
		return errdefs.Conflict("address %s is used by container %s", ip, o)
	}
	p.used[a] = owner
	return nil
}

// Allocate returns the lowest free address for owner.
func (p *Pool) Allocate(owner string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for a := uint32(poolFirst); a <= poolLast; a++ {
		if _, taken := p.used[a]; !taken {
			p.used[a] = owner
			return fromU32(a), nil
		}
	}
	return "", errdefs.Conflict("no free container address left in %s", Subnet)
}

// Release frees an address.
func (p *Pool) Release(ip string) {
	if a, ok := toU32(ip); ok {
		p.mu.Lock()
		delete(p.used, a)
		p.mu.Unlock()
	}
}
