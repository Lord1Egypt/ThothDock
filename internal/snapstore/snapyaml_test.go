package snapstore

import (
	"os"
	"reflect"
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
