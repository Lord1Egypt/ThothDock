// Command thothdock-snap is the `snap` command inside a ThothTerm guest.
// snapd needs systemd, a setuid snap-confine, squashfs mounts and AppArmor,
// none of which a PRoot guest has (docs/APP_STORES.md). This command installs
// snaps from the Snap Store itself (download checked against the store's
// SHA3-384, extracted with unsquashfs into /snap/<name>/<revision>) and runs
// their apps through the ThothDock engine, like Flatpak sandboxes: the base
// snap's directories as the system, the guest's home and /tmp, and the snap
// at $SNAP. There is no AppArmor or seccomp: confinement is not enforced.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/Lord1Egypt/ThothDock/internal/sandbox"
	"github.com/Lord1Egypt/ThothDock/internal/snapstore"
	"github.com/Lord1Egypt/ThothDock/internal/termutil"
)

// Guest paths, as snapd lays them out.
const (
	snapRoot  = "/snap"
	snapBin   = "/snap/bin"
	dataRoot  = "/var/snap"
	stateDir  = "/var/lib/thothdock-snaps"
	stateFile = stateDir + "/state.json"
	version   = "thothdock-snap 0.1 (ThothDock)"
)

func main() { os.Exit(run(os.Args[1:])) }

func fail(format string, args ...any) int {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	return 1
}

const usage = `Usage: snap <command> [options]

  find <query>                 search the Snap Store
  info <snap>                  show a snap's channels and what would install
  install [--channel=C] [--classic] <snap>...
  remove <snap>...
  list                         installed snaps
  run <snap>[.<app>] [args]    run an app (also /snap/bin/<snap>)
  run --hook=configure <snap>  run a snap's install or configure hook again
  start|stop <snap>[.<app>]    run or stop a service (a daemon in snap.yaml)
  services                     services and whether they run
  logs <snap>[.<app>]          a service's output
  version

ThothDock's snap: installs from the Snap Store and runs apps through the
ThothDock engine. Confinement is not enforced (no AppArmor or seccomp).`

func run(args []string) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Println(usage)
		return 0
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "version", "--version":
		fmt.Println(version)
		return 0
	case "find":
		return cmdFind(rest)
	case "info":
		return cmdInfo(rest)
	case "install":
		return asRoot(args, func() int { return cmdInstall(rest) })
	case "remove":
		return asRoot(args, func() int { return cmdRemove(rest) })
	case "list":
		return cmdList()
	case "run":
		if len(rest) == 2 && strings.HasPrefix(rest[0], "--hook=") {
			return asRoot(args, func() int { return cmdHook(strings.TrimPrefix(rest[0], "--hook="), rest[1]) })
		}
		return cmdRun(rest, false)
	case "start":
		return asRoot(args, func() int { return cmdRun(rest, true) })
	case "stop":
		return asRoot(args, func() int { return cmdStop(rest) })
	case "services":
		return cmdServices()
	case "logs":
		return cmdLogs(rest)
	}
	return fail("unknown command %q, see 'snap help'", cmd)
}

// asRoot runs f as root, re-running the command through sudo when needed
// (snapd's install, remove and service commands need root too).
func asRoot(args []string, f func() int) int {
	if os.Geteuid() == 0 {
		return f()
	}
	self, err := os.Executable()
	if err != nil {
		return fail("%v", err)
	}
	c := exec.Command("sudo", append([]string{self}, args...)...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		return fail("this command needs root: sudo snap %s (%v)", strings.Join(args, " "), err)
	}
	return 0
}

// ---- state -------------------------------------------------------------------

type installed struct {
	Revision    int      `json:"revision"`
	Version     string   `json:"version"`
	Channel     string   `json:"channel"`
	Type        string   `json:"type"`
	Base        string   `json:"base"`
	Confinement string   `json:"confinement"`
	Apps        []string `json:"apps"`
	Daemons     []string `json:"daemons"`
	Aliases     []string `json:"aliases"`
}

type state struct {
	Snaps map[string]*installed `json:"snaps"`
}

func loadState() (*state, error) {
	st := &state{Snaps: map[string]*installed{}}
	b, err := os.ReadFile(stateFile)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, st); err != nil {
		return nil, fmt.Errorf("%s: %v", stateFile, err)
	}
	if st.Snaps == nil {
		st.Snaps = map[string]*installed{}
	}
	return st, nil
}

func (st *state) save() error {
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	tmp := stateFile + ".new"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, stateFile)
}

// ---- store commands ----------------------------------------------------------

func cmdFind(args []string) int {
	if len(args) == 0 {
		return fail("usage: snap find <query>")
	}
	found, err := snapstore.New().Find(strings.Join(args, " "))
	if err != nil {
		return fail("%v", err)
	}
	if len(found) == 0 {
		fmt.Println("No matching snaps.")
		return 0
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "Name\tVersion\tPublisher\tNotes\tSummary")
	for _, f := range found {
		notes := "-"
		if f.Confinement == "classic" {
			notes = "classic"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", f.Name, f.Version, f.Publisher, notes, f.Summary)
	}
	tw.Flush()
	return 0
}

func parseFlags(args []string) (channel string, classic bool, names []string, err error) {
	channel = "stable"
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--channel="):
			channel = strings.TrimPrefix(a, "--channel=")
		case a == "--classic":
			classic = true
		case a == "--stable" || a == "--candidate" || a == "--beta" || a == "--edge":
			channel = strings.TrimPrefix(a, "--")
		case strings.HasPrefix(a, "-"):
			return "", false, nil, fmt.Errorf("unknown option %s", a)
		default:
			names = append(names, a)
		}
	}
	return channel, classic, names, nil
}

func cmdInfo(args []string) int {
	channel, _, names, err := parseFlags(args)
	if err != nil || len(names) != 1 {
		return fail("usage: snap info [--channel=C] <snap>")
	}
	rel, err := snapstore.New().Info(names[0], channel)
	if err != nil {
		return fail("%v", err)
	}
	fmt.Printf("name:        %s\nchannel:     %s\nversion:     %s\nrevision:    %d\ntype:        %s\nbase:        %s\nconfinement: %s\nsize:        %.1f MB\n",
		rel.Name, rel.Channel, rel.Version, rel.Revision, rel.Type, rel.Base, rel.Confinement, float64(rel.Size)/(1<<20))
	return 0
}

// ---- install / remove --------------------------------------------------------

func cmdInstall(args []string) int {
	channel, classic, names, err := parseFlags(args)
	if err != nil || len(names) == 0 {
		return fail("usage: snap install [--channel=C] [--classic] <snap>...")
	}
	if err := ensureUnsquashfs(); err != nil {
		return fail("%v", err)
	}
	_, err = os.Stat(snapBin)
	firstSnap := errors.Is(err, os.ErrNotExist)
	st, err := loadState()
	if err != nil {
		return fail("%v", err)
	}
	store := snapstore.New()
	for _, n := range names {
		if err := install(store, st, n, channel, classic, map[string]bool{}); err != nil {
			return fail("%v", err)
		}
	}
	if _, err := os.Stat(snapBin); firstSnap && err == nil {
		fmt.Println("Open a new window to use these commands: a window opened before the first snap has no /snap/bin in its PATH.")
	}
	return 0
}

// ensureUnsquashfs installs squashfs-tools from the guest's own archive when
// unsquashfs is missing. snap install already runs as root and needs the
// network, so this asks nothing more of the user than the install itself.
func ensureUnsquashfs() error {
	if _, err := exec.LookPath("unsquashfs"); err == nil {
		return nil
	}
	if _, err := exec.LookPath("apt-get"); err != nil {
		return errors.New("unsquashfs is needed to unpack snaps: install squashfs-tools")
	}
	fmt.Println("Installing squashfs-tools, which unpacks snaps...")
	apt := func(args ...string) error {
		c := exec.Command("apt-get", args...)
		c.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		return c.Run()
	}
	pkg := []string{"install", "-y", "--no-install-recommends", "squashfs-tools"}
	if apt(pkg...) != nil {
		// The package lists may predate the package or be missing.
		if err := apt("update"); err != nil {
			return fmt.Errorf("apt-get update: %v", err)
		}
		if err := apt(pkg...); err != nil {
			return fmt.Errorf("installing squashfs-tools: %v", err)
		}
	}
	if _, err := exec.LookPath("unsquashfs"); err != nil {
		return errors.New("squashfs-tools is installed but unsquashfs is not on PATH")
	}
	return nil
}

func install(store *snapstore.Client, st *state, name, channel string, classic bool, seen map[string]bool) error {
	if seen[name] {
		return fmt.Errorf("base loop at %s", name)
	}
	seen[name] = true
	rel, err := store.Info(name, channel)
	if err != nil {
		return err
	}
	if rel.Confinement == "classic" && !classic {
		return fmt.Errorf("%s uses classic confinement: install it with --classic (it then sees the whole system)", name)
	}
	if b := rel.Base; b != "" && b != "bare" && b != "none" {
		if _, ok := st.Snaps[b]; !ok {
			if err := install(store, st, b, "stable", false, seen); err != nil {
				return fmt.Errorf("base %s for %s: %w", b, name, err)
			}
		}
	}
	if cur, ok := st.Snaps[name]; ok && cur.Revision == rel.Revision {
		fmt.Printf("snap %q is already installed (%s, revision %d)\n", name, cur.Version, cur.Revision)
		if cur.Aliases == nil && cur.Type == "app" {
			// Installed before aliases were set up: add them now.
			setAliases(store, name, rel.SnapID, cur)
			return st.save()
		}
		return nil
	}
	fmt.Printf("Downloading %s %s (revision %d, %.1f MB) from the Snap Store...\n", name, rel.Version, rel.Revision, float64(rel.Size)/(1<<20))
	cache := filepath.Join(stateDir, "cache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return err
	}
	file := filepath.Join(cache, fmt.Sprintf("%s_%d.snap", name, rel.Revision))
	// A partial download of another revision can never be resumed.
	if old, _ := filepath.Glob(filepath.Join(cache, name+"_*.snap.partial")); len(old) > 0 {
		for _, p := range old {
			if p != file+".partial" {
				os.Remove(p)
			}
		}
	}
	p := newProgress(rel.Size)
	err = store.Fetch(rel, file, p.update)
	p.done()
	if err != nil {
		if _, perr := os.Stat(file + ".partial"); perr == nil {
			return fmt.Errorf("downloading %s: %v (run the same command again to continue the download)", name, err)
		}
		return fmt.Errorf("downloading %s: %v", name, err)
	}
	defer os.Remove(file)
	dir := filepath.Join(snapRoot, name)
	revDir := filepath.Join(dir, strconv.Itoa(rel.Revision))
	partial := revDir + ".partial"
	os.RemoveAll(partial)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// -no-xattrs: a PRoot guest cannot set them. Device nodes, if any, fail
	// individually; unsquashfs exits non-zero only on real errors.
	out, err := exec.Command("unsquashfs", "-n", "-no-xattrs", "-d", partial, file).CombinedOutput()
	if err != nil && !onlyDeviceNodeErrors(err, out, partial) {
		os.RemoveAll(partial)
		return fmt.Errorf("unpacking %s: %v: %s", name, err, firstErrors(out, 4))
	}
	meta, err := os.ReadFile(filepath.Join(partial, "meta/snap.yaml"))
	if err != nil {
		os.RemoveAll(partial)
		return fmt.Errorf("%s has no meta/snap.yaml: %v", name, err)
	}
	y, err := snapstore.ParseSnapYAML(meta)
	if err != nil || y.Name != name {
		os.RemoveAll(partial)
		return fmt.Errorf("%s: bad meta/snap.yaml: %v", name, err)
	}
	os.RemoveAll(revDir)
	if err := os.Rename(partial, revDir); err != nil {
		return err
	}
	cur := filepath.Join(dir, "current")
	os.Remove(cur + ".new")
	if err := os.Symlink(strconv.Itoa(rel.Revision), cur+".new"); err != nil {
		return err
	}
	if err := os.Rename(cur+".new", cur); err != nil {
		return err
	}
	if old, ok := st.Snaps[name]; ok {
		removeAliases(name, old.Aliases)
		if old.Revision != rel.Revision {
			os.RemoveAll(filepath.Join(dir, strconv.Itoa(old.Revision)))
		}
	}
	rec := &installed{Revision: rel.Revision, Version: rel.Version, Channel: rel.Channel, Type: y.Type,
		Base: rel.Base, Confinement: rel.Confinement}
	for app, a := range y.Apps {
		if a.Daemon != "" {
			rec.Daemons = append(rec.Daemons, app)
		} else {
			rec.Apps = append(rec.Apps, app)
		}
	}
	sort.Strings(rec.Apps)
	sort.Strings(rec.Daemons)
	for _, d := range []string{filepath.Join(dataRoot, name, strconv.Itoa(rel.Revision)), filepath.Join(dataRoot, name, "common")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	if err := writeLaunchers(name, rec.Apps); err != nil {
		return err
	}
	if rec.Type == "app" {
		setAliases(store, name, rel.SnapID, rec)
	}
	st.Snaps[name] = rec
	if err := st.save(); err != nil {
		return err
	}
	runHooks(name, rec, y, revDir)
	fmt.Printf("%s %s from the Snap Store installed\n", name, rel.Version)
	return nil
}

// progress draws one updating line while a snap downloads, on a terminal.
type progress struct {
	total      int64
	tty        bool
	start      time.Time
	last       time.Time
	first, now int64
	drawn      bool
}

func newProgress(total int64) *progress {
	return &progress{total: total, tty: termutil.IsTerminal(1), start: time.Now(), first: -1}
}

func (p *progress) update(done int64) {
	if p.first < 0 {
		p.first = done // bytes kept from an earlier, interrupted download
	}
	p.now = done
	if !p.tty || (time.Since(p.last) < 250*time.Millisecond && done < p.total) {
		return
	}
	p.last = time.Now()
	mb := float64(1 << 20)
	speed := ""
	if secs := time.Since(p.start).Seconds(); secs > 0.5 {
		speed = fmt.Sprintf("  %.1f MB/s", float64(done-p.first)/mb/secs)
	}
	fmt.Printf("\r  %.1f / %.1f MB  %3d%%%s   ", float64(done)/mb, float64(p.total)/mb, done*100/p.total, speed)
	p.drawn = true
}

func (p *progress) done() {
	if p.drawn {
		fmt.Println()
	}
}

// launcherBody runs <name>.<app> through snap run.
func launcherBody(name, app string) []byte {
	return []byte("#!/bin/sh\nexec /usr/local/bin/snap run " + name + "." + app + " \"$@\"\n")
}

// setAliases makes /snap/bin/<alias> for each command alias the store grants
// the snap ("gcloud" for google-cloud-cli.gcloud), as snapd does on install.
// An alias whose path already exists is left alone and reported. Failing to
// reach the store only costs the aliases, never the install.
func setAliases(store *snapstore.Client, name, snapID string, rec *installed) {
	rec.Aliases = []string{}
	granted, err := store.Aliases(snapID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %s's command aliases are not set up: %v\n", name, err)
		return
	}
	apps := map[string]bool{}
	for _, a := range rec.Apps {
		apps[a] = true
	}
	for _, alias := range snapstore.SortedAliases(granted) {
		app := granted[alias]
		if !apps[app] {
			continue
		}
		p := filepath.Join(snapBin, alias)
		if _, err := os.Lstat(p); err == nil {
			fmt.Fprintf(os.Stderr, "warning: alias %s for %s.%s not set up: %s already exists\n", alias, name, app, p)
			continue
		}
		if err := os.WriteFile(p, launcherBody(name, app), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "warning: alias %s: %v\n", alias, err)
			continue
		}
		rec.Aliases = append(rec.Aliases, alias)
	}
	if len(rec.Aliases) > 0 {
		fmt.Printf("Commands: %s\n", strings.Join(rec.Aliases, ", "))
	}
}

// removeAliases deletes the snap's aliases, but only those that still run
// this snap: another snap may have taken the name since.
func removeAliases(name string, aliases []string) {
	for _, alias := range aliases {
		p := filepath.Join(snapBin, alias)
		if b, err := os.ReadFile(p); err == nil && strings.Contains(string(b), " run "+name+".") {
			os.Remove(p)
		}
	}
}

// cmdHook is snap run --hook=<install|configure> <snap>, as in snapd.
func cmdHook(hook, name string) int {
	if hook != "install" && hook != "configure" {
		return fail("only the install and configure hooks can be run")
	}
	if !snapstore.ValidName(name) {
		return fail("%q is not a valid snap name", name)
	}
	st, err := loadState()
	if err != nil {
		return fail("%v", err)
	}
	rec, ok := st.Snaps[name]
	if !ok {
		return fail("snap %q is not installed", name)
	}
	revDir := filepath.Join(snapRoot, name, strconv.Itoa(rec.Revision))
	meta, err := os.ReadFile(filepath.Join(revDir, "meta/snap.yaml"))
	if err != nil {
		return fail("%v", err)
	}
	y, err := snapstore.ParseSnapYAML(meta)
	if err != nil {
		return fail("%v", err)
	}
	if !runHook(name, hook, rec, y, revDir) {
		return 1
	}
	return 0
}

// runHooks runs the snap's install and configure hooks, as snapd does after
// unpacking (redis's configure hook writes $SNAP_COMMON/etc/redis/redis.conf),
// in the snap's sandbox as root. snapctl is not available; a failing hook is
// reported and the snap stays installed.
func runHooks(name string, rec *installed, y *snapstore.SnapYAML, revDir string) {
	for _, hook := range []string{"install", "configure"} {
		runHook(name, hook, rec, y, revDir)
	}
}

// runHook runs one hook if the snap has it; false when it exists and failed.
func runHook(name, hook string, rec *installed, y *snapstore.SnapYAML, revDir string) bool {
	if fi, err := os.Stat(filepath.Join(revDir, "meta/hooks", hook)); err != nil || !fi.Mode().IsRegular() {
		return true
	}
	app := snapstore.App{Command: "meta/hooks/" + hook}
	req, err := buildRequest(name, "hook."+hook, rec, y, app, revDir, nil, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %s hook of %s: %v\n", hook, name, err)
		return false
	}
	req.Tty = false
	conn, err := net.Dial("unix", engineSocket())
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %s hook of %s not run: cannot reach the ThothDock engine: %v\n", hook, name, err)
		return false
	}
	defer conn.Close()
	code, err := sandbox.Run(conn, req, nil, os.Stdout, os.Stderr, nil)
	if err != nil || code != 0 {
		fmt.Fprintf(os.Stderr, "warning: the %s hook of %s failed (exit %d, %v)\n", hook, name, code, err)
		return false
	}
	return true
}

// writeLaunchers makes /snap/bin/<snap> (the app named like the snap) and
// /snap/bin/<snap>.<app> for the others, as snapd does.
func writeLaunchers(name string, apps []string) error {
	if err := os.MkdirAll(snapBin, 0o755); err != nil {
		return err
	}
	removeLaunchers(name)
	for _, app := range apps {
		cmd := name + "." + app
		if app == name {
			cmd = name
		}
		if err := os.WriteFile(filepath.Join(snapBin, cmd), launcherBody(name, app), 0o755); err != nil {
			return err
		}
	}
	return nil
}

func removeLaunchers(name string) {
	entries, _ := os.ReadDir(snapBin)
	for _, e := range entries {
		if e.Name() == name || strings.HasPrefix(e.Name(), name+".") {
			os.Remove(filepath.Join(snapBin, e.Name()))
		}
	}
}

func cmdRemove(args []string) int {
	if len(args) == 0 {
		return fail("usage: snap remove <snap>...")
	}
	st, err := loadState()
	if err != nil {
		return fail("%v", err)
	}
	for _, name := range args {
		if !snapstore.ValidName(name) {
			return fail("%q is not a valid snap name", name)
		}
		if _, ok := st.Snaps[name]; !ok {
			return fail("snap %q is not installed", name)
		}
		for other, s := range st.Snaps {
			if s.Base == name {
				return fail("cannot remove %s: %s uses it as its base", name, other)
			}
		}
		stopAll(name)
		removeAliases(name, st.Snaps[name].Aliases)
		removeLaunchers(name)
		os.RemoveAll(filepath.Join(snapRoot, name))
		os.RemoveAll(filepath.Join(dataRoot, name))
		delete(st.Snaps, name)
		if err := st.save(); err != nil {
			return fail("%v", err)
		}
		fmt.Printf("%s removed (your data in ~/snap/%s stays)\n", name, name)
	}
	return 0
}

func cmdList() int {
	st, err := loadState()
	if err != nil {
		return fail("%v", err)
	}
	if len(st.Snaps) == 0 {
		fmt.Println("No snaps are installed yet. Try 'snap install hello-world'.")
		return 0
	}
	names := make([]string, 0, len(st.Snaps))
	for n := range st.Snaps {
		names = append(names, n)
	}
	sort.Strings(names)
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "Name\tVersion\tRev\tTracking\tNotes")
	for _, n := range names {
		s := st.Snaps[n]
		notes := s.Type
		if s.Type == "app" {
			notes = s.Confinement
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\n", n, s.Version, s.Revision, s.Channel, notes)
	}
	tw.Flush()
	return 0
}

// ---- run ---------------------------------------------------------------------

// skipTop are base-snap directories the sandbox gets from elsewhere.
var skipTop = map[string]bool{"proc": true, "dev": true, "sys": true, "run": true, "tmp": true, "home": true,
	"snap": true, "var": true, "root": true, "media": true, "mnt": true, "lost+found": true, "writable": true, "boot": true}

func splitApp(ref string) (snap, app string, err error) {
	snap, app, _ = strings.Cut(ref, ".")
	if app == "" {
		app = snap
	}
	if !snapstore.ValidName(snap) {
		return "", "", fmt.Errorf("%q is not a valid snap name", snap)
	}
	return snap, app, nil
}

func cmdRun(args []string, service bool) int {
	if len(args) == 0 {
		return fail("usage: snap %s <snap>[.<app>] [args]", map[bool]string{true: "start", false: "run"}[service])
	}
	name, appName, err := splitApp(args[0])
	if err != nil {
		return fail("%v", err)
	}
	st, err := loadState()
	if err != nil {
		return fail("%v", err)
	}
	s, ok := st.Snaps[name]
	if !ok {
		return fail("snap %q is not installed (try: sudo snap install %s)", name, name)
	}
	snapDir := path.Join(snapRoot, name, strconv.Itoa(s.Revision))
	meta, err := os.ReadFile(path.Join(snapDir, "meta/snap.yaml"))
	if err != nil {
		return fail("%v", err)
	}
	y, err := snapstore.ParseSnapYAML(meta)
	if err != nil {
		return fail("%v", err)
	}
	app, ok := y.Apps[appName]
	if !ok {
		return fail("snap %q has no app %q", name, appName)
	}
	if service != (app.Daemon != "") {
		if service {
			return fail("%s.%s is not a service; run it with: snap run %s.%s", name, appName, name, appName)
		}
		return fail("%s.%s is a service; start it with: sudo snap start %s.%s", name, appName, name, appName)
	}
	if service {
		// One instance per service, as under snapd.
		if list, err := engineSandboxes(); err == nil {
			for _, sb := range list {
				if sb.AppID == "snap:"+name+"."+appName {
					fmt.Printf("%s.%s is already running (pid %d)\n", name, appName, sb.Pid)
					return 0
				}
			}
		}
	}
	req, err := buildRequest(name, appName, s, y, app, snapDir, args[1:], service)
	if err != nil {
		return fail("%v", err)
	}
	conn, err := net.Dial("unix", engineSocket())
	if err != nil {
		return fail("cannot reach the ThothDock engine: %v", err)
	}
	defer conn.Close()
	if service {
		started := make(chan int, 1)
		go sandbox.Run(conn, req, nil, io.Discard, io.Discard, func(s sandbox.Started) { started <- s.Pid })
		select {
		case pid := <-started:
			fmt.Printf("Started %s.%s (pid %d); output: %s\n", name, appName, pid, logPath(name, appName))
			return 0
		case <-time.After(30 * time.Second):
			return fail("%s.%s did not start", name, appName)
		}
	}
	var resizes <-chan [2]uint16
	if req.Tty {
		if restore, err := termutil.MakeRaw(0); err == nil {
			defer restore()
		}
		req.Width, req.Height = termutil.Size(1)
		resizes = termutil.Resizes(1)
	}
	var stopped atomic.Int32
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		sig := (<-sigs).(syscall.Signal)
		stopped.Store(int32(sig))
		conn.Close()
	}()
	code, err := sandbox.RunWithResize(conn, req, os.Stdin, os.Stdout, os.Stderr, nil, resizes)
	if err != nil {
		if sig := stopped.Load(); sig != 0 {
			return 128 + int(sig)
		}
		return fail("%v", err)
	}
	return code
}

func logPath(name, app string) string { return path.Join(dataRoot, name, "common", app+".log") }

// buildRequest lays out the sandbox snapd would give the app.
func buildRequest(name, appName string, s *installed, y *snapstore.SnapYAML, app snapstore.App, snapDir string, args []string, service bool) (*sandbox.Request, error) {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "/root"
	}
	rev := strconv.Itoa(s.Revision)
	vars := map[string]string{
		"SNAP": snapDir, "SNAP_NAME": name, "SNAP_INSTANCE_NAME": name, "SNAP_REVISION": rev, "SNAP_VERSION": s.Version,
		"SNAP_ARCH": storeArch(), "SNAP_DATA": path.Join(dataRoot, name, rev), "SNAP_COMMON": path.Join(dataRoot, name, "common"),
		"SNAP_USER_DATA": path.Join(home, "snap", name, rev), "SNAP_USER_COMMON": path.Join(home, "snap", name, "common"),
		"SNAP_REAL_HOME": home, "SNAP_COOKIE": "", "SNAP_CONTEXT": "",
		"SNAP_LIBRARY_PATH": "/var/lib/snapd/lib/gl:/var/lib/snapd/lib/gl32:/var/lib/snapd/void",
	}
	for _, d := range []string{vars["SNAP_USER_DATA"], vars["SNAP_USER_COMMON"]} {
		os.MkdirAll(d, 0o755)
	}
	env := map[string]string{}
	var order []string
	set := func(k, v string) {
		if _, ok := env[k]; !ok {
			order = append(order, k)
		}
		env[k] = v
	}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			set(k, v)
		}
	}
	for k, v := range vars {
		set(k, v)
	}
	set("PATH", snapDir+"/usr/sbin:"+snapDir+"/usr/bin:"+snapDir+"/sbin:"+snapDir+"/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/games:/usr/local/games")
	if s.Confinement != "classic" {
		set("HOME", vars["SNAP_USER_DATA"])
	}
	set("THOTHDOCK_SNAP_APP", name+"."+appName) // the engine's Apps view groups sandboxes by it
	expand := func(v string) string { return expandVars(v, func(k string) string { return env[k] }) }
	for _, m := range []map[string]string{y.Environment, app.Environment} {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			set(k, expand(m[k]))
		}
	}
	var argv []string
	for _, c := range app.CommandChain {
		argv = append(argv, inSnap(snapDir, expand(c)))
	}
	words := strings.Fields(app.Command)
	if len(words) == 0 {
		return nil, fmt.Errorf("%s.%s has no command", name, appName)
	}
	for i, w := range words {
		w = expand(w)
		if i == 0 {
			w = inSnap(snapDir, w)
		}
		argv = append(argv, w)
	}
	argv = append(argv, args...)
	if service {
		// A detached service writes to a log the user can read with snap logs.
		argv = append([]string{"/bin/sh", "-c", `exec "$@" >>"$THOTHDOCK_SNAP_LOG" 2>&1`, "sh"}, argv...)
		set("THOTHDOCK_SNAP_LOG", logPath(name, appName))
	}
	baseEnv := make([]string, 0, len(order))
	for _, k := range order {
		baseEnv = append(baseEnv, k+"="+env[k])
	}
	ops, err := sandboxOps(s)
	if err != nil {
		return nil, err
	}
	cwd, _ := os.Getwd()
	if cwd == "" || !visibleInSandbox(cwd, s) {
		cwd = env["HOME"]
	}
	return &sandbox.Request{Ops: ops, BaseEnv: baseEnv, Chdir: cwd, Argv: argv, UID: os.Getuid(), GID: os.Getgid(),
		Tty: !service && termutil.IsTerminal(0) && termutil.IsTerminal(1), DieWithParent: !service}, nil
}

func inSnap(snapDir, p string) string {
	if strings.HasPrefix(p, "/") {
		return p
	}
	return path.Join(snapDir, p)
}

func visibleInSandbox(dir string, s *installed) bool {
	if s.Confinement == "classic" {
		return true
	}
	for _, p := range []string{"/home", "/tmp", "/root", "/snap", "/var/snap"} {
		if dir == p || strings.HasPrefix(dir, p+"/") {
			return true
		}
	}
	return false
}

func bind(src, dest string) sandbox.Op {
	return sandbox.Op{Kind: sandbox.OpBind, Src: src, Dest: dest, FD: -1, Perms: -1}
}

func roBind(src, dest string) sandbox.Op {
	return sandbox.Op{Kind: sandbox.OpROBind, Src: src, Dest: dest, FD: -1, Perms: -1}
}

func roBindTry(src, dest string) sandbox.Op {
	o := roBind(src, dest)
	o.Try = true
	return o
}

func dirOp(dest string) sandbox.Op {
	return sandbox.Op{Kind: sandbox.OpDir, Dest: dest, FD: -1, Perms: -1}
}

// sandboxOps: classic snaps see the guest; others get the base snap's
// system directories, with the guest's network and user files over /etc.
func sandboxOps(s *installed) ([]sandbox.Op, error) {
	proc := sandbox.Op{Kind: sandbox.OpProc, Dest: "/proc", FD: -1, Perms: -1}
	dev := sandbox.Op{Kind: sandbox.OpDev, Dest: "/dev", FD: -1, Perms: -1}
	if s.Confinement == "classic" || s.Base == "" || s.Base == "none" {
		return []sandbox.Op{roBind("/", "/"), proc, dev}, nil
	}
	ops := []sandbox.Op{}
	if s.Base != "bare" {
		baseDir := path.Join(snapRoot, s.Base, "current")
		if t, err := os.Readlink(baseDir); err == nil && !strings.Contains(t, "/") {
			baseDir = path.Join(snapRoot, s.Base, t)
		}
		entries, err := os.ReadDir(baseDir)
		if err != nil {
			return nil, fmt.Errorf("base %s is not installed: %v", s.Base, err)
		}
		for _, e := range entries {
			n := e.Name()
			if skipTop[n] {
				continue
			}
			full := path.Join(baseDir, n)
			if e.Type()&os.ModeSymlink != 0 {
				if t, err := os.Readlink(full); err == nil {
					ops = append(ops, sandbox.Op{Kind: sandbox.OpSymlink, Src: t, Dest: "/" + n, FD: -1, Perms: -1})
				}
			} else if e.IsDir() {
				ops = append(ops, roBind(full, "/"+n))
			}
		}
		for _, f := range []string{"/etc/resolv.conf", "/etc/hosts", "/etc/passwd", "/etc/group", "/etc/nsswitch.conf"} {
			ops = append(ops, roBindTry(f, f))
		}
	}
	ops = append(ops, dirOp("/var"), dirOp("/var/tmp"), dirOp("/run"), dirOp("/mnt"), dirOp("/media"),
		bind(snapRoot, snapRoot), bind(dataRoot, dataRoot), bind("/home", "/home"), bind("/tmp", "/tmp"), proc, dev)
	if os.Geteuid() == 0 {
		ops = append(ops, bind("/root", "/root"))
	} else {
		ops = append(ops, dirOp("/root"))
	}
	return ops, nil
}

func storeArch() string {
	if goruntime.GOARCH == "arm" {
		return "armhf"
	}
	return goruntime.GOARCH
}

func engineSocket() string {
	if p := os.Getenv("THOTHDOCK_SOCKET"); p != "" {
		return p
	}
	if h, ok := strings.CutPrefix(os.Getenv("DOCKER_HOST"), "unix://"); ok && h != "" {
		return h
	}
	return "/run/thothdock/thothdock.sock"
}

// ---- services ----------------------------------------------------------------

type sandboxInfo struct {
	ID, AppID string
	Pid       int
	NetIP     string
}

func engineSandboxes() ([]sandboxInfo, error) {
	c := http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		Dial: func(_, _ string) (net.Conn, error) { return net.Dial("unix", engineSocket()) }}}
	resp, err := c.Get("http://thothdock/thothdock/apps")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var v struct{ Sandboxes []sandboxInfo }
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return nil, err
	}
	return v.Sandboxes, nil
}

func killSandbox(id string) error {
	c := http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{
		Dial: func(_, _ string) (net.Conn, error) { return net.Dial("unix", engineSocket()) }}}
	resp, err := c.Post("http://thothdock/thothdock/sandboxes/"+id+"/kill", "application/json", bytes.NewReader(nil))
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("engine answered %s", resp.Status)
	}
	return nil
}

// stopAll stops every sandbox of a snap (used before removing it).
func stopAll(name string) {
	list, err := engineSandboxes()
	if err != nil {
		return
	}
	for _, sb := range list {
		if strings.HasPrefix(sb.AppID, "snap:"+name+".") {
			killSandbox(sb.ID)
		}
	}
}

func cmdStop(args []string) int {
	if len(args) != 1 {
		return fail("usage: snap stop <snap>[.<app>]")
	}
	name, app, err := splitApp(args[0])
	if err != nil {
		return fail("%v", err)
	}
	list, err := engineSandboxes()
	if err != nil {
		return fail("cannot reach the ThothDock engine: %v", err)
	}
	want := "snap:" + name + "." + app
	if !strings.Contains(args[0], ".") {
		want = "snap:" + name + "."
	}
	n := 0
	for _, sb := range list {
		if sb.AppID == want || (strings.HasSuffix(want, ".") && strings.HasPrefix(sb.AppID, want)) {
			if err := killSandbox(sb.ID); err != nil {
				return fail("%v", err)
			}
			n++
		}
	}
	if n == 0 {
		fmt.Printf("%s is not running\n", args[0])
		return 0
	}
	fmt.Printf("Stopped %s (%d process%s)\n", args[0], n, map[bool]string{true: "", false: "es"}[n == 1])
	return 0
}

func cmdServices() int {
	st, err := loadState()
	if err != nil {
		return fail("%v", err)
	}
	running := map[string]sandboxInfo{}
	if list, err := engineSandboxes(); err == nil {
		for _, sb := range list {
			running[strings.TrimPrefix(sb.AppID, "snap:")] = sb
		}
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "Service\tCurrent\tAddress\tNotes")
	any := false
	names := make([]string, 0, len(st.Snaps))
	for n := range st.Snaps {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		for _, d := range st.Snaps[n].Daemons {
			any = true
			cur, addr := "inactive", "-"
			if sb, ok := running[n+"."+d]; ok {
				cur = "active"
				if sb.NetIP != "" {
					addr = sb.NetIP
				}
			}
			fmt.Fprintf(tw, "%s.%s\t%s\t%s\t%s\n", n, d, cur, addr, "start with: sudo snap start "+n+"."+d)
		}
	}
	tw.Flush()
	if !any {
		fmt.Println("No installed snap has services.")
	}
	return 0
}

func cmdLogs(args []string) int {
	if len(args) != 1 {
		return fail("usage: snap logs <snap>[.<app>]")
	}
	name, app, err := splitApp(args[0])
	if err != nil {
		return fail("%v", err)
	}
	b, err := os.ReadFile(logPath(name, app))
	if err != nil {
		return fail("no output recorded for %s.%s yet", name, app)
	}
	os.Stdout.Write(b)
	return 0
}

// onlyDeviceNodeErrors: base snaps ship /dev/null and friends, which a PRoot
// guest cannot create (unsquashfs exits 2). Sandboxes get /dev from the
// runtime, never from the snap, so that is the one failure accepted; any
// error about another path still fails the install.
func onlyDeviceNodeErrors(err error, out []byte, dir string) bool {
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 2 {
		return false
	}
	saw := false
	for _, l := range strings.Split(string(out), "\n") {
		if !strings.Contains(l, "failed") && !strings.Contains(l, "could not") && !strings.Contains(l, "rror") {
			continue
		}
		if !strings.Contains(l, " "+dir+"/dev/") {
			return false
		}
		saw = true
	}
	return saw
}

func firstErrors(b []byte, n int) string {
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, "failed") || strings.Contains(l, "could not") || strings.Contains(l, "rror") {
			out = append(out, strings.TrimSpace(l))
			if len(out) == n {
				break
			}
		}
	}
	if len(out) == 0 {
		return strings.TrimSpace(string(b))
	}
	return strings.Join(out, " | ")
}

// expandVars expands $VAR, ${VAR}, ${VAR:-word} and ${VAR:+word} (word
// expanded in turn), the forms snapcraft writes into snap.yaml environments,
// e.g. ${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}.
func expandVars(s string, get func(string) string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '$' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		if s[i+1] == '{' {
			depth, j := 1, i+2
			for ; j < len(s) && depth > 0; j++ {
				switch s[j] {
				case '{':
					depth++
				case '}':
					depth--
				}
			}
			if depth != 0 {
				b.WriteString(s[i:])
				break
			}
			inner := s[i+2 : j-1]
			name, op, word := inner, "", ""
			if k := strings.Index(inner, ":"); k >= 0 && k+1 < len(inner) {
				name, op, word = inner[:k], inner[k:k+2], inner[k+2:]
			}
			v := get(name)
			switch op {
			case ":-":
				if v == "" {
					v = expandVars(word, get)
				}
			case ":+":
				if v != "" {
					v = expandVars(word, get)
				}
			}
			b.WriteString(v)
			i = j - 1
			continue
		}
		j := i + 1
		for j < len(s) && (s[j] == '_' || s[j] >= 'A' && s[j] <= 'Z' || s[j] >= 'a' && s[j] <= 'z' || j > i+1 && s[j] >= '0' && s[j] <= '9') {
			j++
		}
		if j == i+1 {
			b.WriteByte('$')
			continue
		}
		b.WriteString(get(s[i+1 : j]))
		i = j - 1
	}
	return b.String()
}
