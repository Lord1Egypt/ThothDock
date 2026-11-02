package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Lord1Egypt/ThothDock/internal/snapstore"
)

// ctl runs snapctl for the snap "nc" and returns what it printed and its exit status.
func ctl(t *testing.T, args ...string) (string, int) {
	t.Helper()
	t.Setenv("SNAP_INSTANCE_NAME", "nc")
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	code := snapctlMain(args)
	w.Close()
	os.Stdout = old
	b, _ := io.ReadAll(r)
	return strings.TrimRight(string(b), "\n"), code
}

func withDataRoot(t *testing.T) {
	t.Helper()
	old := dataRoot
	dataRoot = t.TempDir()
	t.Cleanup(func() { dataRoot = old })
}

// The calls Nextcloud's utilities make (snap/hooks/configure, apache-utilities).
func TestSnapctlNextcloudSequence(t *testing.T) {
	withDataRoot(t)
	if out, code := ctl(t, "get", "ports.http"); out != "" || code != 0 {
		t.Fatalf("unset key: %q %d (scripts test for empty output)", out, code)
	}
	if _, code := ctl(t, "set", "ports.http=80"); code != 0 {
		t.Fatal("set failed")
	}
	if _, code := ctl(t, "set", "private.ports.http=80", "http.compression=true", "php.memory-limit=512M"); code != 0 {
		t.Fatal("multi set failed")
	}
	for key, want := range map[string]string{"ports.http": "80", "private.ports.http": "80",
		"http.compression": "true", "php.memory-limit": "512M"} {
		if out, _ := ctl(t, "get", key); out != want {
			t.Errorf("get %s = %q, want %q", key, out, want)
		}
	}
	if out, _ := ctl(t, "get", "-d", "ports"); out != "{\n\t\"ports\": {\n\t\t\"http\": 80\n\t}\n}" {
		t.Errorf("get -d ports = %q", out)
	}
	if out, _ := ctl(t, "set", "-s", "mode=123"); out != "" {
		t.Errorf("set printed %q", out)
	}
	if out, _ := ctl(t, "get", "-t", "mode"); out != `"123"` {
		t.Errorf("-s keeps text: %q", out)
	}
	if _, code := ctl(t, "unset", "ports.http"); code != 0 {
		t.Fatal("unset failed")
	}
	if out, _ := ctl(t, "get", "ports.http"); out != "" {
		t.Errorf("unset key still set: %q", out)
	}
	if out, _ := ctl(t, "get", "private.ports.http"); out != "80" {
		t.Errorf("unset removed a sibling: %q", out)
	}
}

func TestSnapctlRejectsBadInput(t *testing.T) {
	withDataRoot(t)
	for _, args := range [][]string{{"set", "noequals"}, {"set", "Bad_Key=1"}, {"set", "a..b=1"}, {"get", "../x"},
		{"set"}, {"unset"}, {"nonsense"}, {"restart", "nc.apache"}, {"restart", "other.apache"}} {
		if _, code := ctl(t, args...); code == 0 {
			t.Errorf("snapctl %v accepted", args)
		}
	}
	t.Setenv("SNAP_INSTANCE_NAME", "")
	t.Setenv("SNAP_NAME", "")
	if code := snapctlMain([]string{"get", "x"}); code == 0 {
		t.Error("snapctl worked outside a snap")
	}
}

// Two services set their defaults at once; both keys must survive.
func TestSnapctlConcurrentSets(t *testing.T) {
	withDataRoot(t)
	var wg sync.WaitGroup
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := updateConfig("nc", func(cfg map[string]any) error {
				setValue(cfg, []string{"k" + fmt.Sprint(i)}, true)
				return nil
			}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	cfg, err := readConfig("nc")
	if err != nil || len(cfg) != 25 {
		t.Fatalf("%d keys survived (%v)", len(cfg), err)
	}
}

func TestParseAndFormatValue(t *testing.T) {
	for in, want := range map[string]any{"80": "80", "true": "true", "512M": "512M", "": "", "1 2": "1 2", "{}x": "{}x"} {
		if got := formatValue(parseValue(in, false), false); got != want {
			t.Errorf("%q round-trips to %q", in, got)
		}
	}
	if _, ok := parseValue(`{"a":[1,2]}`, false).(map[string]any); !ok {
		t.Error("object not parsed")
	}
	if got := formatValue(parseValue("null", false), false); got != "null" {
		t.Errorf("null: %q", got)
	}
}

func TestUnsetPrunesEmptyParents(t *testing.T) {
	cfg := map[string]any{}
	setValue(cfg, []string{"a", "b", "c"}, 1)
	setValue(cfg, []string{"a", "x"}, 2)
	unsetValue(cfg, []string{"a", "b", "c"})
	if _, ok := cfg["a"].(map[string]any)["b"]; ok {
		t.Error("empty map a.b kept")
	}
	unsetValue(cfg, []string{"a", "x"})
	if len(cfg) != 0 {
		t.Errorf("empty a kept: %v", cfg)
	}
	unsetValue(cfg, []string{"never", "set"}) // no panic
}

func TestRequestsApplyOnlyToTheSnapsServices(t *testing.T) {
	withDataRoot(t)
	os.MkdirAll(filepath.Join(dataRoot, "nc"), 0o755)
	t.Setenv("SNAP_INSTANCE_NAME", "nc")
	t.Setenv(spoolEnv, "")
	if _, code := ctl(t, "restart", "nc.apache"); code == 0 {
		t.Fatal("restart outside a hook accepted")
	}
	spool, err := newSpool("nc")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(spoolEnv, spool)
	if _, code := ctl(t, "restart", "--reload", "nc.apache"); code != 0 {
		t.Fatal("restart in a hook refused")
	}
	b, _ := os.ReadFile(spool)
	if string(b) != "restart nc.apache\n" {
		t.Fatalf("spool %q", b)
	}
	if err := os.Symlink("/etc/passwd", spool+".link"); err != nil {
		t.Fatal(err)
	}
	t.Setenv(spoolEnv, spool+".link")
	if _, code := ctl(t, "restart", "nc.apache"); code == 0 {
		t.Error("a symlinked spool was followed")
	}
}

func TestDaemonsOfSkipsTimers(t *testing.T) {
	y := &snapstore.SnapYAML{Apps: map[string]snapstore.App{
		"apache": {Daemon: "simple"}, "mysql": {Daemon: "simple"}, "occ": {},
		"logrotate": {Daemon: "simple", Timer: "00:00"},
	}}
	if got := daemonsOf(y); !reflect.DeepEqual(got, []string{"apache", "mysql"}) {
		t.Fatalf("%v", got)
	}
}

// unsquashfs under PRoot makes a hard link as a symlink to the path it was
// unpacked at; older installs unpacked into <dir>.partial and renamed.
func TestFixPartialLinks(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "core18", "3085")
	bin := filepath.Join(dir, "usr", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(bin, ".l2s.python3.60001.0002"), []byte("python"), 0o755)
	stale := dir + ".partial"
	os.Symlink(filepath.Join(stale, "usr/bin/.l2s.python3.60001.0002"), filepath.Join(bin, "python3.6"))
	os.Symlink("python3.6", filepath.Join(bin, "python3")) // a real relative symlink stays
	os.Symlink("/etc/alternatives/x", filepath.Join(bin, "abs"))
	if _, err := os.ReadFile(filepath.Join(bin, "python3.6")); err == nil {
		t.Fatal("the stale link resolved before the repair")
	}
	n, err := fixPartialLinks(dir)
	if err != nil || n != 1 {
		t.Fatalf("fixed %d (%v)", n, err)
	}
	b, err := os.ReadFile(filepath.Join(bin, "python3"))
	if err != nil || string(b) != "python" {
		t.Fatalf("python3 -> %q (%v)", b, err)
	}
	if got, _ := os.Readlink(filepath.Join(bin, "python3.6")); got != ".l2s.python3.60001.0002" {
		t.Errorf("repaired link is %q, want one relative to its directory", got)
	}
	if got, _ := os.Readlink(filepath.Join(bin, "abs")); got != "/etc/alternatives/x" {
		t.Errorf("an unrelated absolute link changed to %q", got)
	}
	if n, _ := fixPartialLinks(dir); n != 0 {
		t.Errorf("second pass changed %d", n)
	}
}

func TestStopDisableAndStartEnableReachTheSpool(t *testing.T) {
	withDataRoot(t)
	t.Setenv("SNAP_INSTANCE_NAME", "caddy")
	spool, err := newSpool("caddy")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(spoolEnv, spool)
	if code := snapctlMain([]string{"stop", "--disable", "caddy.server"}); code != 0 {
		t.Fatal("stop --disable refused")
	}
	if code := snapctlMain([]string{"start", "--enable", "caddy.server"}); code != 0 {
		t.Fatal("start --enable refused")
	}
	if code := snapctlMain([]string{"restart", "--reload", "caddy.server"}); code != 0 {
		t.Fatal("restart --reload refused")
	}
	b, _ := os.ReadFile(spool)
	if want := "stop-disable caddy.server\nstart-enable caddy.server\nrestart caddy.server\n"; string(b) != want {
		t.Fatalf("spool %q, want %q", b, want)
	}
}
