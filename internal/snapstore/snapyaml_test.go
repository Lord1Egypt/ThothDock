package snapstore

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// The meta/snap.yaml of the real hello-world (base core, 1-space indents,
// flow list, block scalar) and hello (core20, command-chain, environment).
func TestRealSnapYAML(t *testing.T) {
	b, _ := os.ReadFile("testdata/hello-world.yaml")
	y, err := ParseSnapYAML(b)
	if err != nil {
		t.Fatal(err)
	}
	if y.Name != "hello-world" || y.Version != "6.4" || y.Base != "" || y.Confinement != "strict" || y.Type != "app" {
		t.Fatalf("%+v", y)
	}
	if len(y.Apps) != 4 || y.Apps["hello-world"].Command != "bin/echo" || y.Apps["sh"].Command != "bin/sh" {
		t.Fatalf("apps %+v", y.Apps)
	}
	b, _ = os.ReadFile("testdata/hello.yaml")
	y, err = ParseSnapYAML(b)
	if err != nil {
		t.Fatal(err)
	}
	if y.Name != "hello" || y.Version != "2.10" || y.Base != "core20" {
		t.Fatalf("%+v", y)
	}
	u := y.Apps["universe"]
	if u.Command != "bin/hello -g $SAY" || !reflect.DeepEqual(u.CommandChain, []string{"snap/command-chain/snapcraft-runner"}) ||
		u.Environment["SAY"] != "Hello, universe!" {
		t.Fatalf("universe %+v", u)
	}
}

// core20's own snap.yaml: a single-quoted description over three lines.
func TestBaseSnapYAML(t *testing.T) {
	b, _ := os.ReadFile("testdata/core20.yaml")
	y, err := ParseSnapYAML(b)
	if err != nil {
		t.Fatal(err)
	}
	if y.Name != "core20" || y.Type != "base" || y.Version != "20260901" || len(y.Apps) != 0 {
		t.Fatalf("%+v", y)
	}
}

// redis (a plain command over three lines, a daemon) and mosquitto (a plain
// description over five lines, shell-style environment).
func TestServerSnapYAML(t *testing.T) {
	b, _ := os.ReadFile("testdata/redis.yaml")
	y, err := ParseSnapYAML(b)
	if err != nil {
		t.Fatal(err)
	}
	srv := y.Apps["server"]
	if y.Base != "core22" || srv.Daemon != "simple" || srv.Environment["LANG"] != "C.UTF-8" ||
		srv.Command != "usr/bin/redis-server $SNAP_COMMON/etc/redis/redis.conf --loadmodule $SNAP/usr/lib/redis/modules/redisbloom.so --loadmodule $SNAP/usr/lib/redis/modules/rejson.so --loadmodule $SNAP/usr/lib/redis/modules/redistimeseries.so --loadmodule $SNAP/usr/lib/redis/modules/redisearch.so" {
		t.Fatalf("server %+v", srv)
	}
	if y.Apps["cli"].Command != "usr/bin/redis-cli" || y.Apps["cli"].Daemon != "" {
		t.Fatalf("cli %+v", y.Apps["cli"])
	}
	b, _ = os.ReadFile("testdata/mosquitto.yaml")
	if y, err = ParseSnapYAML(b); err != nil {
		t.Fatal(err)
	}
	if y.Base != "core24" || y.Apps["mosquitto"].Daemon != "simple" || y.Apps["mosquitto"].Command != "launcher.sh" ||
		y.Environment["PATH"] != "$SNAP/usr/sbin:$SNAP/usr/bin:$SNAP/sbin:$SNAP/bin:$PATH" {
		t.Fatalf("mosquitto %+v", y)
	}
}

func TestSnapYAMLShapes(t *testing.T) {
	y, err := ParseSnapYAML([]byte(`name: srv # a comment
version: "1.0"
type: app
environment:
  LANG: C.UTF-8
apps:
  web:
    command: bin/serve --port 8080
    daemon: simple
    plugs: [network, network-bind]
`))
	if err != nil {
		t.Fatal(err)
	}
	if y.Name != "srv" || y.Version != "1.0" || y.Environment["LANG"] != "C.UTF-8" || y.Apps["web"].Daemon != "simple" {
		t.Fatalf("%+v", y)
	}
	for _, bad := range []string{"name: x\n  bad: indent\n", "- a\n", "version: 1\n", "name: 'never closed\n  more\n"} {
		if _, err := ParseSnapYAML([]byte(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestSnapYAMLTimersAndPlugs(t *testing.T) {
	y, err := ParseSnapYAML([]byte(`name: nc
version: 1
apps:
  apache:
    command: bin/run
    daemon: simple
    plugs:
    - network
    - network-bind
  logrotate:
    command: bin/rotate
    timer: 00:00
    daemon: simple
    plugs: [network]
hooks:
  configure:
    plugs:
    - removable-media
    - network
`))
	if err != nil {
		t.Fatal(err)
	}
	if y.Apps["logrotate"].Timer != "00:00" || y.Apps["apache"].Timer != "" {
		t.Fatalf("timers: %+v", y.Apps)
	}
	if got := strings.Join(y.Plugs, ","); got != "network,network-bind,removable-media" {
		t.Fatalf("plugs %q", got)
	}
}
