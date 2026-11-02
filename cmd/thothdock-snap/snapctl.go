package main

// snapctl and snap get/set/unset: a snap's configuration, as snapd keeps it.
//
// The configuration of a snap is one JSON document, /var/snap/<snap>/config.json,
// shared by every revision. snapctl is this same binary under another name: the
// sandbox of a non-classic snap gets it bound at /usr/lib/snapd/snapctl (where
// the base snaps' /usr/bin/snapctl symlink points); a classic snap finds it
// as /usr/local/bin/snapctl. Hooks may also start, stop and restart the snap's
// services: snapctl writes the request to a spool file the hook was given and
// the snap command applies it once the hook has ended, as snapd does after a
// hook. The sandbox has no way to reach the engine itself.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/Lord1Egypt/ThothDock/internal/snapstore"
)

const spoolEnv = "THOTHDOCK_SNAPCTL_SPOOL"

// keyPart is one dot-separated part of a configuration key, as snapd allows.
var keyPart = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)

func configPath(name string) string { return path.Join(dataRoot, name, "config.json") }

func splitKey(k string) ([]string, error) {
	parts := strings.Split(k, ".")
	for _, p := range parts {
		if !keyPart.MatchString(p) {
			return nil, fmt.Errorf("invalid option name: %q", k)
		}
	}
	return parts, nil
}

func readConfig(name string) (map[string]any, error) {
	b, err := os.ReadFile(configPath(name))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	cfg := map[string]any{}
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("%s: %v", configPath(name), err)
	}
	return cfg, nil
}

// updateConfig reads the configuration under a lock, lets f change it and
// writes it back whole: two services setting their defaults at once (Nextcloud's
// apache and php-fpm do) must not lose each other's keys.
func updateConfig(name string, f func(cfg map[string]any) error) error {
	dir := path.Join(dataRoot, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	lock, err := os.OpenFile(path.Join(dir, ".config.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	cfg, err := readConfig(name)
	if err != nil {
		return err
	}
	if err := f(cfg); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := configPath(name) + ".new"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, configPath(name))
}

func getValue(cfg map[string]any, parts []string) (any, bool) {
	var cur any = cfg
	for _, p := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[p]; !ok {
			return nil, false
		}
	}
	return cur, true
}

func setValue(cfg map[string]any, parts []string, v any) {
	cur := cfg
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[p] = next
		}
		cur = next
	}
	cur[parts[len(parts)-1]] = v
}

// unsetValue removes the key and the maps it leaves empty.
func unsetValue(cfg map[string]any, parts []string) {
	m, ok := cfg, true
	chain := []map[string]any{cfg}
	for _, p := range parts[:len(parts)-1] {
		if m, ok = m[p].(map[string]any); !ok {
			return
		}
		chain = append(chain, m)
	}
	delete(m, parts[len(parts)-1])
	for i := len(chain) - 1; i > 0 && len(chain[i]) == 0; i-- {
		delete(chain[i-1], parts[i-1])
	}
}

// parseValue reads a value as JSON ("80" is a number, "true" a boolean,
// {"a":1} an object) and falls back to the text, as snapd does.
func parseValue(s string, asString bool) any {
	if asString {
		return s
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil || dec.More() {
		return s
	}
	if _, err := dec.Token(); err != io.EOF {
		return s
	}
	return v
}

// formatValue prints a value: text as it is, anything else as JSON.
func formatValue(v any, typed bool) string {
	if s, ok := v.(string); ok && !typed {
		return s
	}
	var b []byte
	switch v.(type) {
	case map[string]any, []any:
		b, _ = json.MarshalIndent(v, "", "\t")
	default:
		b, _ = json.Marshal(v)
	}
	return string(b)
}

// getKeys reads the keys (the whole configuration for none) as an object
// holding only what is set.
func getKeys(cfg map[string]any, keys []string) (map[string]any, error) {
	if len(keys) == 0 {
		return cfg, nil
	}
	out := map[string]any{}
	for _, k := range keys {
		parts, err := splitKey(k)
		if err != nil {
			return nil, err
		}
		if v, ok := getValue(cfg, parts); ok {
			out[k] = v
		}
	}
	return out, nil
}

// applySets changes the configuration for key=value pairs, or removes keys.
func applySets(cfg map[string]any, pairs []string, asString bool) error {
	for _, kv := range pairs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return fmt.Errorf("invalid configuration: %q (want key=value)", kv)
		}
		parts, err := splitKey(k)
		if err != nil {
			return err
		}
		setValue(cfg, parts, parseValue(v, asString))
	}
	return nil
}

func applyUnsets(cfg map[string]any, keys []string) error {
	for _, k := range keys {
		parts, err := splitKey(k)
		if err != nil {
			return err
		}
		unsetValue(cfg, parts)
	}
	return nil
}

// ---- snapctl -----------------------------------------------------------------

const snapctlUsage = `Usage: snapctl <command>

  get [-d] [-t] [<key>...]        read configuration (-d: as a JSON object)
  set [-s] [-t] <key>=<value>...  change configuration
  unset <key>...                  remove configuration
  start|stop|restart <snap>[.<app>]...
                                  from a hook: applied when the hook ends
  services [<snap>[.<app>]...]    from a hook: which services are active
  is-connected <plug>             whether the snap declares the plug
  set-health <status> [<message>] accepted and ignored

ThothDock's snapctl: configuration and services of the snap that runs it.
There are no interface connections: every declared plug counts as connected.`

func snapctlMain(args []string) int {
	name := os.Getenv("SNAP_INSTANCE_NAME")
	if name == "" {
		name = os.Getenv("SNAP_NAME")
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Println(snapctlUsage)
		return 0
	}
	if !snapstore.ValidName(name) {
		return fail("snapctl can only be run from a snap's hook, service or app")
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "get":
		return snapctlGet(name, rest)
	case "set":
		return snapctlSet(name, rest)
	case "unset":
		if len(rest) == 0 {
			return fail("snapctl unset: no keys given")
		}
		if err := updateConfig(name, func(cfg map[string]any) error { return applyUnsets(cfg, rest) }); err != nil {
			return fail("%v", err)
		}
		return 0
	case "start", "stop", "restart":
		return snapctlService(name, cmd, rest)
	case "is-connected":
		var plug string
		for _, a := range rest {
			if !strings.HasPrefix(a, "-") {
				plug = a
			}
		}
		if plug == "" {
			return fail("snapctl is-connected: no plug given")
		}
		meta, err := os.ReadFile(path.Join(os.Getenv("SNAP"), "meta/snap.yaml"))
		if err != nil {
			return fail("%v", err)
		}
		y, err := snapstore.ParseSnapYAML(meta)
		if err != nil {
			return fail("%v", err)
		}
		if slices.Contains(y.Plugs, plug) {
			return 0
		}
		return 1
	case "services":
		return snapctlServices(name, rest)
	case "set-health":
		return 0
	}
	return fail("snapctl %s is not supported by ThothDock (see snapctl help)", cmd)
}

func snapctlGet(name string, args []string) int {
	var asDoc, typed bool
	var keys []string
	for _, a := range args {
		switch {
		case a == "-d" || a == "--document":
			asDoc = true
		case a == "-t" || a == "--typed":
			typed = true
		case a == "-dt" || a == "-td":
			asDoc, typed = true, true
		case strings.HasPrefix(a, "-"):
			return fail("snapctl get: unknown option %s", a)
		default:
			keys = append(keys, a)
		}
	}
	cfg, err := readConfig(name)
	if err != nil {
		return fail("%v", err)
	}
	got, err := getKeys(cfg, keys)
	if err != nil {
		return fail("%v", err)
	}
	if asDoc || len(keys) != 1 {
		if len(keys) == 0 && len(cfg) == 0 {
			fmt.Println("{}")
			return 0
		}
		b, _ := json.MarshalIndent(got, "", "\t")
		fmt.Println(string(b))
		return 0
	}
	// One key, as text: nothing is printed for a key that is not set, and
	// scripts test for the empty output.
	if v, ok := got[keys[0]]; ok {
		fmt.Println(formatValue(v, typed))
	}
	return 0
}

func snapctlSet(name string, args []string) int {
	var asString bool
	var pairs []string
	for _, a := range args {
		switch {
		case a == "-s" || a == "--string":
			asString = true
		case a == "-t" || a == "--typed":
		case strings.HasPrefix(a, "-") && !strings.Contains(a, "="):
			return fail("snapctl set: unknown option %s", a)
		default:
			pairs = append(pairs, a)
		}
	}
	if len(pairs) == 0 {
		return fail("snapctl set: no key=value given")
	}
	if err := updateConfig(name, func(cfg map[string]any) error { return applySets(cfg, pairs, asString) }); err != nil {
		return fail("%v", err)
	}
	return 0
}

func snapctlService(name, verb string, targets []string) int {
	var refs []string
	for _, t := range targets {
		if strings.HasPrefix(t, "-") {
			// --reload restarts; --disable and --enable also change whether
			// the service starts with the snap.
			if (verb == "stop" && t == "--disable") || (verb != "stop" && t == "--enable") {
				verb += "-" + strings.TrimPrefix(t, "--")
			}
			continue
		}
		if t != name && !strings.HasPrefix(t, name+".") {
			return fail("snapctl %s: %q is not a service of this snap", verb, t)
		}
		refs = append(refs, t)
	}
	if len(refs) == 0 {
		return fail("snapctl %s: no services given", verb)
	}
	spool := os.Getenv(spoolEnv)
	if spool == "" {
		return fail("snapctl %s works only in a hook here; from a shell use: sudo snap %s %s", verb, verb, refs[0])
	}
	f, err := os.OpenFile(spool, os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fail("%v", err)
	}
	defer f.Close()
	for _, r := range refs {
		if _, err := fmt.Fprintf(f, "%s %s\n", verb, r); err != nil {
			return fail("%v", err)
		}
	}
	return 0
}

// ---- the snap command's side of services requested from a hook ---------------

// newSpool makes the file a hook's snapctl appends its service requests to.
func newSpool(name string) (string, error) {
	var raw [8]byte
	f, err := os.Open("/dev/urandom")
	if err != nil {
		return "", err
	}
	_, err = io.ReadFull(f, raw[:])
	f.Close()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(path.Join(dataRoot, name), 0o755); err != nil {
		return "", err
	}
	p := path.Join(dataRoot, name, fmt.Sprintf(".snapctl-%x", raw))
	if err := os.WriteFile(p, nil, 0o666); err != nil {
		return "", err
	}
	os.Chmod(p, 0o666) // a hook of a non-root run may append too
	return p, nil
}

// daemonsOf are a snap's services started with it: a daemon with a timer
// runs on its schedule, which only snapd's timer unit can keep.
func daemonsOf(y *snapstore.SnapYAML) []string {
	var out []string
	for n, a := range y.Apps {
		if a.Daemon != "" && a.Timer == "" {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// applyRequests carries out the start, stop and restart requests a hook made.
func applyRequests(name, spool string, y *snapstore.SnapYAML) {
	defer os.Remove(spool)
	f, err := os.OpenFile(spool, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(io.LimitReader(f, 1<<16))
	for sc.Scan() {
		verb, ref, _ := strings.Cut(sc.Text(), " ")
		persist := ""
		switch verb {
		case "start", "stop", "restart":
		case "stop-disable":
			verb, persist = "stop", "disable"
		case "start-enable", "restart-enable":
			verb, persist = "start", "enable"
		default:
			continue
		}
		var apps []string
		if ref == name {
			apps = daemonsOf(y)
		} else if app, ok := strings.CutPrefix(ref, name+"."); ok && y.Apps[app].Daemon != "" {
			apps = []string{app}
		} else {
			fmt.Fprintf(os.Stderr, "warning: %s of %q ignored: not a service of %s\n", verb, ref, name)
			continue
		}
		for _, app := range apps {
			if persist != "" {
				setDisabled(name, app, persist == "disable")
			}
			controlService(verb, name, app)
		}
	}
}

// controlService starts, stops or restarts one service. Restarting a service
// that is not running starts it, as systemctl does.
func controlService(verb, name, app string) {
	ref := name + "." + app
	if verb == "stop" || verb == "restart" {
		if sb, ok := runningService(name, app); ok {
			killSandbox(sb.ID)
			waitStopped(name, app)
		} else if verb == "stop" {
			return
		}
	}
	if verb != "stop" {
		if code := cmdRun([]string{ref}, true); code != 0 {
			fmt.Fprintf(os.Stderr, "warning: %s of %s failed\n", verb, ref)
		}
	}
}

func runningService(name, app string) (sandboxInfo, bool) {
	list, err := engineSandboxes()
	if err != nil {
		return sandboxInfo{}, false
	}
	for _, sb := range list {
		if sb.AppID == "snap:"+name+"."+app {
			return sb, true
		}
	}
	return sandboxInfo{}, false
}

// waitStopped waits until the engine no longer lists the service: starting
// it again before then would be taken for "already running".
func waitStopped(name, app string) {
	for i := 0; i < 100; i++ {
		if _, ok := runningService(name, app); !ok {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// ---- snap get / set / unset --------------------------------------------------

func cmdGet(args []string) int {
	var asDoc, typed bool
	var rest []string
	for _, a := range args {
		switch a {
		case "-d":
			asDoc = true
		case "-t":
			typed = true
		default:
			rest = append(rest, a)
		}
	}
	if len(rest) == 0 {
		return fail("usage: snap get [-d] [-t] <snap> [<key>...]")
	}
	name, keys := rest[0], rest[1:]
	st, err := loadState()
	if err != nil {
		return fail("%v", err)
	}
	if _, ok := st.Snaps[name]; !ok {
		return fail("snap %q is not installed", name)
	}
	cfg, err := readConfig(name)
	if err != nil {
		return fail("%v", err)
	}
	delete(cfg, "private") // keys a snap keeps for itself
	got, err := getKeys(cfg, keys)
	if err != nil {
		return fail("%v", err)
	}
	for _, k := range keys {
		if parts, _ := splitKey(k); len(parts) > 0 && parts[0] == "private" {
			delete(got, k)
		}
	}
	if len(keys) == 1 && !asDoc {
		v, ok := got[keys[0]]
		if !ok {
			return fail("snap %q has no %q configuration option", name, keys[0])
		}
		fmt.Println(formatValue(v, typed))
		return 0
	}
	if len(got) == 0 {
		fmt.Printf("snap %q has no configuration\n", name)
		return 0
	}
	b, _ := json.MarshalIndent(got, "", "\t")
	fmt.Println(string(b))
	return 0
}

// cmdSetUnset changes the configuration and runs the snap's configure hook,
// which validates it and restarts what depends on it. The old configuration
// comes back if the hook fails.
func cmdSetUnset(unset bool, args []string) int {
	var asString bool
	var rest []string
	for _, a := range args {
		if a == "-s" || a == "--string" {
			asString = true
		} else if a != "-t" {
			rest = append(rest, a)
		}
	}
	verb := "set"
	if unset {
		verb = "unset"
	}
	if len(rest) < 2 {
		return fail("usage: snap %s <snap> <key>%s...", verb, map[bool]string{true: "", false: "=<value>"}[unset])
	}
	name, items := rest[0], rest[1:]
	if !snapstore.ValidName(name) {
		return fail("%q is not a valid snap name", name)
	}
	rec, y, revDir, err := loadSnap(name)
	if err != nil {
		return fail("%v", err)
	}
	var before map[string]any
	err = updateConfig(name, func(cfg map[string]any) error {
		b, _ := json.Marshal(cfg)
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber()
		if err := dec.Decode(&before); err != nil {
			return err
		}
		if unset {
			return applyUnsets(cfg, items)
		}
		return applySets(cfg, items, asString)
	})
	if err != nil {
		return fail("%v", err)
	}
	if !runHook(name, "configure", rec, y, revDir) {
		updateConfig(name, func(cfg map[string]any) error {
			for k := range cfg {
				delete(cfg, k)
			}
			for k, v := range before {
				cfg[k] = v
			}
			return nil
		})
		return fail("cannot perform the following tasks: the configure hook of %s failed; the configuration is unchanged", name)
	}
	return 0
}

// loadSnap reads what running a snap's hook or app needs.
func loadSnap(name string) (*installed, *snapstore.SnapYAML, string, error) {
	st, err := loadState()
	if err != nil {
		return nil, nil, "", err
	}
	rec, ok := st.Snaps[name]
	if !ok {
		return nil, nil, "", fmt.Errorf("snap %q is not installed", name)
	}
	revDir := filepath.Join(snapRoot, name, strconv.Itoa(rec.Revision))
	meta, err := os.ReadFile(filepath.Join(revDir, "meta/snap.yaml"))
	if err != nil {
		return nil, nil, "", err
	}
	y, err := snapstore.ParseSnapYAML(meta)
	if err != nil {
		return nil, nil, "", err
	}
	return rec, y, revDir, nil
}

// ---- repair of links made by older installs ----------------------------------

// fixPartialLinks points the hard links of a snap unpacked by an older
// version, under "<dir>.partial", at <dir>. They are relative now, so the
// snap can be moved again. It returns how many it changed.
func fixPartialLinks(dir string) (int, error) {
	stale := dir + ".partial"
	n := 0
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.Type()&os.ModeSymlink == 0 {
			return nil
		}
		t, err := os.Readlink(p)
		if err != nil || !strings.HasPrefix(t, stale+"/") {
			return nil
		}
		rel, err := filepath.Rel(filepath.Dir(p), dir+strings.TrimPrefix(t, stale))
		if err != nil {
			return nil
		}
		tmp := p + ".fixing"
		os.Remove(tmp)
		if err := os.Symlink(rel, tmp); err != nil {
			return err
		}
		if err := os.Rename(tmp, p); err != nil {
			os.Remove(tmp)
			return err
		}
		n++
		return nil
	})
	return n, err
}

// repairLinks fixes the named snap and its bases once. A failure is
// reported and tried again next time.
func repairLinks(name string) {
	st, err := loadState()
	if err != nil {
		return
	}
	changed := false
	for seen := 0; name != "" && name != "bare" && name != "none" && seen < 4; seen++ {
		rec, ok := st.Snaps[name]
		if !ok {
			break
		}
		if !rec.LinksFixed {
			n, err := fixPartialLinks(filepath.Join(snapRoot, name, strconv.Itoa(rec.Revision)))
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: repairing the hard links of %s: %v\n", name, err)
				break
			}
			if n > 0 {
				fmt.Fprintf(os.Stderr, "Repaired %d hard links of %s.\n", n, name)
			}
			rec.LinksFixed, changed = true, true
		}
		name = rec.Base
	}
	if changed {
		st.save()
	}
}

// setDisabled records whether a service starts with its snap.
func setDisabled(name, app string, off bool) {
	st, err := loadState()
	if err != nil || st.Snaps[name] == nil {
		return
	}
	rec := st.Snaps[name]
	rec.Disabled = slices.DeleteFunc(rec.Disabled, func(a string) bool { return a == app })
	if off {
		rec.Disabled = append(rec.Disabled, app)
		sort.Strings(rec.Disabled)
	}
	if err := st.save(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}
}

// snapctlBinary is a copy of this binary inside the guest's own file system,
// which the sandboxes bind as snapctl. The snap command itself reaches the
// guest through a bind of the app's native library, which the engine, building
// a sandbox from outside, does not see: it would find an empty placeholder.
// Root makes the copy (install, refresh and autostart do, so it exists before
// any snap runs) and renews it when the binary changes; anyone else uses what
// is there, and without a copy snaps run without snapctl.
func snapctlBinary() string {
	dst := path.Join(stateDir, "bin", "snapctl")
	self, err := os.Executable()
	if err != nil || os.Geteuid() != 0 {
		if _, err := os.Stat(dst); err == nil {
			return dst
		}
		return ""
	}
	if sameContent(self, dst) {
		return dst
	}
	if err := copyExecutable(self, dst); err != nil {
		if _, serr := os.Stat(dst); serr == nil {
			return dst // an older copy still gives snaps a snapctl
		}
		fmt.Fprintf(os.Stderr, "warning: snapctl is not available to snaps: %v\n", err)
		return ""
	}
	return dst
}

func sameContent(a, b string) bool {
	fa, err := os.Open(a)
	if err != nil {
		return false
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return false
	}
	defer fb.Close()
	sa, _ := fa.Stat()
	sb, _ := fb.Stat()
	if sa == nil || sb == nil || sa.Size() != sb.Size() {
		return false
	}
	ba, bb := make([]byte, 1<<16), make([]byte, 1<<16)
	for {
		na, ea := io.ReadFull(fa, ba)
		nb, _ := io.ReadFull(fb, bb)
		if na != nb || !bytes.Equal(ba[:na], bb[:nb]) {
			return false
		}
		if ea != nil {
			return true
		}
	}
}

func copyExecutable(src, dst string) error {
	if err := os.MkdirAll(path.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// ---- snapctl services --------------------------------------------------------

const servicesEnv = "THOTHDOCK_SNAPCTL_SERVICES"

// writeServices describes the snap's services for a hook's "snapctl services":
// the sandbox cannot ask the engine, so the snap command says what is running.
func writeServices(name string, rec *installed, y *snapstore.SnapYAML, spool string) string {
	file := spool + ".services"
	var b strings.Builder
	for _, d := range rec.Daemons {
		startup, current := "enabled", "inactive"
		if slices.Contains(rec.Disabled, d) {
			startup = "disabled"
		}
		if _, ok := runningService(name, d); ok {
			current = "active"
		}
		fmt.Fprintf(&b, "%s.%s %s %s\n", name, d, startup, current)
	}
	if os.WriteFile(file, []byte(b.String()), 0o644) != nil {
		return ""
	}
	return file
}

func snapctlServices(name string, args []string) int {
	b, err := os.ReadFile(os.Getenv(servicesEnv))
	if err != nil {
		return fail("snapctl services works only in a hook here; from a shell use: snap services")
	}
	want := map[string]bool{}
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			if a != name && !strings.HasPrefix(a, name+".") {
				return fail("snapctl services: %q is not a service of this snap", a)
			}
			want[a] = true
		}
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "Service\tStartup\tCurrent\tNotes")
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		f := strings.Fields(l)
		if len(f) != 3 {
			continue
		}
		if len(want) > 0 && !want[f[0]] && !want[name] {
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t-\n", f[0], f[1], f[2])
	}
	tw.Flush()
	return 0
}
