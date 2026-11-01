package engine

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"syscall"

	"github.com/Lord1Egypt/ThothDock/internal/errdefs"
	"github.com/Lord1Egypt/ThothDock/internal/sandbox"
	"github.com/Lord1Egypt/ThothDock/internal/securefs"
	"github.com/Lord1Egypt/ThothDock/internal/snapstore"
)

// App is an installed Flatpak application found in the guest, with the
// sandboxes it runs in now.
type App struct {
	Kind         string // "flatpak"
	ID           string // e.g. org.example.Tool
	Name         string // from its .desktop file, else the ID
	Branch       string
	Arch         string
	Installation string // "user:<name>" or "system"
	Runtime      string // from metadata, e.g. org.freedesktop.Platform/aarch64/24.08
	SizeBytes    int64  // the files of the active deployment
	Running      []SandboxInfo
}

// AppsView is GET /thothdock/apps: installed apps, how many runtimes they
// share, and every running sandbox.
type AppsView struct {
	Apps      []App
	Runtimes  int
	Sandboxes []SandboxInfo
}

// flatpakInstallations are the guest directories flatpak installs into.
func flatpakInstallations(guest *securefs.Root) map[string]string {
	out := map[string]string{"system": "/var/lib/flatpak"}
	if host, isDir, err := sandbox.ResolveGuest(guest, "/home"); err == nil && isDir {
		if entries, err := os.ReadDir(host); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					out["user:"+e.Name()] = "/home/" + e.Name() + "/.local/share/flatpak"
				}
			}
		}
	}
	return out
}

// Apps lists the Flatpak apps installed in the guest. Sizes are cached per
// deployment (a commit is never modified), so polling stays cheap.
func (e *Engine) Apps() AppsView {
	view := AppsView{Apps: []App{}, Sandboxes: e.Sandboxes()}
	if e.cfg.GuestRoot == "" {
		return view
	}
	guest, err := securefs.OpenRoot(e.cfg.GuestRoot)
	if err != nil {
		return view
	}
	defer guest.Close()
	running := map[string][]SandboxInfo{}
	for _, s := range view.Sandboxes {
		if s.AppID != "" {
			running[s.AppID] = append(running[s.AppID], s)
		}
	}
	view.Apps = append(view.Apps, e.snapApps(guest, view.Sandboxes, &view.Runtimes)...)
	for inst, dir := range flatpakInstallations(guest) {
		view.Runtimes += len(deployments(guest, dir+"/runtime"))
		for _, d := range deployments(guest, dir+"/app") {
			app := App{Kind: "flatpak", ID: d.id, Name: d.id, Arch: d.arch, Branch: d.branch, Installation: inst}
			meta := keyFile(guest, filepath.Join(d.host, "metadata"))
			app.Runtime = meta["Application.runtime"]
			if n := keyFile(guest, filepath.Join(d.host, "files/share/applications", d.id+".desktop"))["Desktop Entry.Name"]; n != "" {
				app.Name = n
			}
			app.SizeBytes = e.deploymentSize(guest, d.host)
			app.Running = running[d.id]
			view.Apps = append(view.Apps, app)
		}
	}
	sort.Slice(view.Apps, func(i, j int) bool {
		if view.Apps[i].Name != view.Apps[j].Name {
			return strings.ToLower(view.Apps[i].Name) < strings.ToLower(view.Apps[j].Name)
		}
		return view.Apps[i].Installation < view.Apps[j].Installation
	})
	return view
}

type deployment struct {
	id, arch, branch string
	host             string // host path of the active deployment, inside the guest
}

// deployments lists <dir>/<id>/<arch>/<branch>/active of one installation.
func deployments(guest *securefs.Root, dir string) []deployment {
	base, isDir, err := sandbox.ResolveGuest(guest, dir)
	if err != nil || !isDir {
		return nil
	}
	var out []deployment
	ids, _ := os.ReadDir(base)
	for _, id := range ids {
		archs, _ := os.ReadDir(filepath.Join(base, id.Name()))
		for _, arch := range archs {
			branches, _ := os.ReadDir(filepath.Join(base, id.Name(), arch.Name()))
			for _, br := range branches {
				g := strings.Join([]string{dir, id.Name(), arch.Name(), br.Name(), "active"}, "/")
				host, isDir, err := sandbox.ResolveGuest(guest, g)
				if err == nil && isDir {
					out = append(out, deployment{id: id.Name(), arch: arch.Name(), branch: br.Name(), host: host})
				}
			}
		}
	}
	return out
}

// deploymentSize adds up the regular files of a deployment. Its files are
// link2symlink entries (OSTree hard links) whose target, a host path inside
// the guest root, holds the data; that target's size is what is counted.
func (e *Engine) deploymentSize(guest *securefs.Root, host string) int64 {
	e.mu.Lock()
	if n, ok := e.appSizes[host]; ok {
		e.mu.Unlock()
		return n
	}
	e.mu.Unlock()
	var total int64
	prefix := guest.Path() + "/"
	filepath.WalkDir(host, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		switch {
		case d.Type().IsRegular():
			if fi, err := d.Info(); err == nil {
				total += fi.Size()
			}
		case d.Type()&fs.ModeSymlink != 0:
			// The target is guest-written text: resolve it inside the guest.
			if t, err := os.Readlink(p); err == nil && strings.HasPrefix(t, prefix) && strings.Contains(t, "/.l2s.") {
				if real, isDir, err := sandbox.ResolveGuest(guest, t); err == nil && !isDir {
					if fi, err := os.Lstat(real); err == nil && fi.Mode().IsRegular() {
						total += fi.Size()
					}
				}
			}
		}
		return nil
	})
	e.mu.Lock()
	e.appSizes[host] = total
	e.mu.Unlock()
	return total
}

// keyFile reads a GKeyFile/desktop file (a host path inside the guest,
// resolved there with chroot semantics, link2symlink included) into
// "Group.key" -> value: first value wins, no locale variants, at most 1 MiB.
// Missing or unreadable: empty.
func keyFile(guest *securefs.Root, path string) map[string]string {
	out := map[string]string{}
	real, isDir, err := sandbox.ResolveGuest(guest, path)
	if err != nil || isDir {
		return out
	}
	f, err := os.OpenFile(real, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return out
	}
	defer f.Close()
	group := ""
	sc := bufio.NewScanner(io.LimitReader(f, 1<<20))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			group = line[1 : len(line)-1]
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok && !strings.Contains(k, "[") {
			key := group + "." + strings.TrimSpace(k)
			if _, seen := out[key]; !seen {
				out[key] = strings.TrimSpace(v)
			}
		}
	}
	return out
}

// snapApps lists the app snaps thothdock-snap installed in the guest
// (/var/lib/thothdock-snaps/state.json); base snaps count as runtimes.
func (e *Engine) snapApps(guest *securefs.Root, sandboxes []SandboxInfo, runtimes *int) []App {
	host, isDir, err := sandbox.ResolveGuest(guest, "/var/lib/thothdock-snaps/state.json")
	if err != nil || isDir {
		return nil
	}
	f, err := os.OpenFile(host, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil
	}
	defer f.Close()
	var st struct {
		Snaps map[string]struct {
			Revision         int
			Version, Channel string
			Type, Base       string
			Confinement      string
		}
	}
	if json.NewDecoder(io.LimitReader(f, 4<<20)).Decode(&st) != nil {
		return nil
	}
	var out []App
	for name, s := range st.Snaps {
		if !snapstore.ValidName(name) {
			continue
		}
		if s.Type != "app" {
			*runtimes++
			continue
		}
		app := App{Kind: "snap", ID: name, Name: name, Branch: s.Channel, Arch: goruntime.GOARCH,
			Installation: "system", Runtime: s.Base}
		if s.Confinement == "classic" {
			app.Runtime = "classic"
		}
		if dir, isDir, err := sandbox.ResolveGuest(guest, fmt.Sprintf("/snap/%s/%d", name, s.Revision)); err == nil && isDir {
			app.SizeBytes = e.deploymentSize(guest, dir)
		}
		for _, sb := range sandboxes {
			if strings.HasPrefix(sb.AppID, "snap:"+name+".") {
				app.Running = append(app.Running, sb)
			}
		}
		out = append(out, app)
	}
	return out
}

// KillSandbox ends one sandbox (the Apps view's Stop).
func (e *Engine) KillSandbox(id string) error {
	e.mu.Lock()
	s, ok := e.sandboxes[id]
	e.mu.Unlock()
	if !ok {
		return errdefs.NotFound("No such sandbox: %s", id)
	}
	return s.Kill()
}
