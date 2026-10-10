// Package guard inspects a Debian/Ubuntu root filesystem for the Engine Guard
// (engine-guard/): the placeholders that keep a stock Docker Engine out.
package guard

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Protected are the engine package names the guard covers.
var Protected = []string{"docker.io", "docker-ce", "docker-engine", "moby-engine", "containerd", "containerd.io", "runc"}

// EngineBinaries must not exist in a ThothDock guest.
var EngineBinaries = []string{"dockerd", "containerd", "runc", "docker-proxy", "docker-init", "containerd-shim-runc-v2"}

var binDirs = []string{"usr/bin", "usr/sbin", "usr/local/bin", "usr/local/sbin", "bin", "sbin", "usr/libexec/docker", "usr/lib/docker"}

// Level orders findings.
type Level int

const (
	OK Level = iota
	Warn
	Fail
)

func (l Level) String() string { return [...]string{"PASS", "WARN", "FAIL"}[l] }

// Finding is one line of the report.
type Finding struct {
	Level  Level
	What   string
	Detail string
}

// Package is one dpkg status record.
type Package struct{ Version, Status string }

// ReadStatus parses <root>/var/lib/dpkg/status. ok is false when the root has
// no dpkg database (not a Debian-family system).
func ReadStatus(root string) (pkgs map[string]Package, ok bool) {
	f, err := os.Open(filepath.Join(root, "var/lib/dpkg/status"))
	if err != nil {
		return nil, false
	}
	defer f.Close()
	pkgs = map[string]Package{}
	var name string
	var cur Package
	flush := func() {
		if name != "" {
			pkgs[name] = cur
		}
		name, cur = "", Package{}
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "Package: "):
			name = strings.TrimPrefix(line, "Package: ")
		case strings.HasPrefix(line, "Version: "):
			cur.Version = strings.TrimPrefix(line, "Version: ")
		case strings.HasPrefix(line, "Status: "):
			cur.Status = strings.TrimPrefix(line, "Status: ")
		}
	}
	flush()
	return pkgs, true
}

func installed(p Package) bool { return strings.HasSuffix(p.Status, " installed") }

// Check reports the guard's state for the root filesystem at root.
func Check(root string) []Finding {
	pkgs, ok := ReadStatus(root)
	if !ok {
		return []Finding{{OK, "Engine Guard", "not applicable: no dpkg database under " + root}}
	}
	var out []Finding
	for _, name := range Protected {
		p, have := pkgs[name]
		switch {
		case have && installed(p) && strings.HasPrefix(p.Version, "9999:"):
			out = append(out, Finding{OK, name, "protected (" + p.Version + ")"})
		case have && installed(p):
			out = append(out, Finding{Fail, name, "REAL ENGINE PACKAGE installed (" + p.Version + "); remove it and reinstall the guard"})
		default:
			out = append(out, Finding{Warn, name, "unprotected: the guard placeholder is not installed"})
		}
	}
	if g, have := pkgs["thothdock-engine-guard"]; have && installed(g) {
		out = append(out, Finding{OK, "thothdock-engine-guard", g.Version})
	} else {
		out = append(out, Finding{Warn, "thothdock-engine-guard", "not installed: no apt pin or hook"})
	}
	out = append(out, enforcement(root)...)
	for _, b := range EngineBinaries {
		var found string
		for _, d := range binDirs {
			if fi, err := os.Lstat(filepath.Join(root, d, b)); err == nil && !fi.IsDir() {
				found = "/" + d + "/" + b
				break
			}
		}
		if found != "" {
			out = append(out, Finding{Fail, b, "PRESENT at " + found + " (a stock engine binary; ThothDock is the engine)"})
		} else {
			out = append(out, Finding{OK, b, "absent"})
		}
	}
	return out
}

// enforcementFiles are what makes apt refuse a stock engine: the hook, the apt
// configuration that runs it, and the pin. The app rewrites them every time a
// terminal window opens, so a missing one is a Warn, not a Fail.
var enforcementFiles = []struct {
	path, what string
	exec       bool
}{
	{"usr/lib/thothdock/engine-guard-hook", "apt hook", true},
	{"etc/apt/apt.conf.d/99thothdock-engine-guard", "apt hook configuration", false},
	{"etc/apt/preferences.d/thothdock-engine-guard", "apt pin", false},
}

func enforcement(root string) []Finding {
	var out []Finding
	for _, f := range enforcementFiles {
		fi, err := os.Stat(filepath.Join(root, f.path))
		switch {
		case err != nil || !fi.Mode().IsRegular():
			out = append(out, Finding{Warn, f.what, "missing: /" + f.path + " (restored when a terminal window opens)"})
		case f.exec && fi.Mode().Perm()&0o111 == 0:
			out = append(out, Finding{Fail, f.what, "/" + f.path + " is not executable: apt would fail closed"})
		default:
			out = append(out, Finding{OK, f.what, "/" + f.path})
		}
	}
	return out
}

// Worst is the highest level among findings.
func Worst(fs []Finding) Level {
	w := OK
	for _, f := range fs {
		if f.Level > w {
			w = f.Level
		}
	}
	return w
}
