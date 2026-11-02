package engine

import (
	"path/filepath"
	"testing"

	"github.com/Lord1Egypt/ThothDock/internal/network"
)

func TestSnapAddressIsOnePerSnapAndStable(t *testing.T) {
	file := filepath.Join(t.TempDir(), "snap-addrs.json")
	pool := network.NewPool()
	a := newSnapAddrs(file, pool)
	redis1, err := a.address("redis")
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := a.address("redis"); again != redis1 {
		t.Fatalf("a snap's second sandbox got %s, not %s", again, redis1)
	}
	nc, _ := a.address("nextcloud")
	if nc == redis1 {
		t.Fatalf("two snaps share %s", nc)
	}
	if other, _ := pool.Allocate("container"); other == redis1 || other == nc {
		t.Fatalf("a container was given the snap address %s", other)
	}

	// The engine restarts: the same snaps get the same addresses back.
	pool2 := network.NewPool()
	b := newSnapAddrs(file, pool2)
	if got, _ := b.address("nextcloud"); got != nc {
		t.Errorf("after a restart nextcloud is %s, was %s", got, nc)
	}
	if got, _ := b.address("redis"); got != redis1 {
		t.Errorf("after a restart redis is %s, was %s", got, redis1)
	}

	// Removing the snap frees its address for the next one.
	b.forget("redis")
	if c, _ := b.address("mosquitto"); c != redis1 {
		t.Errorf("a removed snap's address %s was not reused (got %s)", redis1, c)
	}
}

// A container that already holds an address saved for a snap keeps it.
func TestSnapAddressYieldsToAContainer(t *testing.T) {
	file := filepath.Join(t.TempDir(), "snap-addrs.json")
	first := newSnapAddrs(file, network.NewPool())
	ip, _ := first.address("redis")
	pool := network.NewPool()
	if err := pool.Reserve(ip, "container-1"); err != nil {
		t.Fatal(err)
	}
	got, err := newSnapAddrs(file, pool).address("redis")
	if err != nil || got == ip {
		t.Fatalf("the snap took the container's address %s (%s, %v)", ip, got, err)
	}
}

func TestSnapOf(t *testing.T) {
	for in, want := range map[string]string{"snap:redis.server": "redis", "snap:nextcloud.occ": "nextcloud",
		"snap:x": "x", "org.gnome.Calc": "", "": "", "snap:": ""} {
		if got := snapOf(in); got != want {
			t.Errorf("snapOf(%q) = %q, want %q", in, got, want)
		}
	}
}
