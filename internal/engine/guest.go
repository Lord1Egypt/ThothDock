package engine

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Lord1Egypt/ThothDock/internal/errdefs"
	"github.com/Lord1Egypt/ThothDock/internal/securefs"
)

const defaultPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// guestUser is the resolved Config.User.
type guestUser struct {
	UID, GID int
	Home     string
}

// resolveUser reads the container's own /etc/passwd and /etc/group (through
// securefs, so a symlinked /etc/passwd cannot point outside the rootfs).
func resolveUser(root *securefs.Root, spec string) (guestUser, error) {
	if spec == "" {
		spec = "0"
	}
	userPart, groupPart, hasGroup := strings.Cut(spec, ":")
	passwd, _ := root.ReadFile("/etc/passwd", 4<<20)
	u := guestUser{UID: -1, GID: -1}
	for _, line := range strings.Split(string(passwd), "\n") {
		f := strings.Split(line, ":")
		if len(f) < 7 {
			continue
		}
		uid, err1 := strconv.Atoi(f[2])
		gid, err2 := strconv.Atoi(f[3])
		if err1 != nil || err2 != nil {
			continue
		}
		if f[0] == userPart || strconv.Itoa(uid) == userPart {
			u = guestUser{UID: uid, GID: gid, Home: f[5]}
			break
		}
	}
	if u.UID < 0 {
		n, err := strconv.Atoi(userPart)
		if err != nil || n < 0 {
			return u, errdefs.Invalid("unable to find user %s: no matching entries in passwd file", userPart)
		}
		u = guestUser{UID: n, GID: n, Home: "/"}
		if n == 0 {
			u.Home = "/root"
		}
	}
	if hasGroup {
		gid, err := strconv.Atoi(groupPart)
		if err != nil {
			gid = -1
			group, _ := root.ReadFile("/etc/group", 4<<20)
			for _, line := range strings.Split(string(group), "\n") {
				f := strings.Split(line, ":")
				if len(f) >= 3 && f[0] == groupPart {
					gid, _ = strconv.Atoi(f[2])
					break
				}
			}
			if gid < 0 {
				return u, errdefs.Invalid("unable to find group %s: no matching entries in group file", groupPart)
			}
		}
		u.GID = gid
	}
	return u, nil
}

// lookPath finds the executable the way runc does: a name with a slash is
// taken as a path (relative to cwd), otherwise PATH is searched. Error
// texts follow Docker's, which the CLI turns into exit codes 127/126.
// mounts are the container's binds and volumes: a path under one of them is
// looked up in its source, as the process will see it, not in the image.
func lookPath(root *securefs.Root, mounts []BindRecord, name, cwd string, env []string) (string, error) {
	if name == "" {
		return "", errdefs.Invalid("no command specified")
	}
	if strings.Contains(name, "/") {
		p := name
		if !strings.HasPrefix(p, "/") {
			p = path.Join(cwd, p)
		}
		if err := checkExec(root, mounts, p); err != nil {
			return "", errdefs.Invalid("exec: %q: %v", name, err)
		}
		return path.Clean(p), nil
	}
	pathVar := defaultPath
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			pathVar = v
		}
	}
	for _, dir := range strings.Split(pathVar, ":") {
		if dir == "" {
			dir = "."
		}
		if !strings.HasPrefix(dir, "/") {
			dir = path.Join(cwd, dir)
		}
		p := path.Join(dir, name)
		if checkExec(root, mounts, p) == nil {
			return p, nil
		}
	}
	return "", errdefs.Invalid("exec: %q: executable file not found in $PATH", name)
}

func checkExec(root *securefs.Root, mounts []BindRecord, p string) error {
	var fi os.FileInfo
	if src, ok := mountedPath(mounts, p); ok {
		// The source is a daemon-approved host path (or a volume) the
		// container sees anyway; this only checks that the file exists.
		st, err := os.Stat(src)
		if err != nil {
			return fmt.Errorf("stat %s: no such file or directory", p)
		}
		fi = st
	} else {
		f, err := root.OpenFile(p)
		if err != nil {
			return fmt.Errorf("stat %s: no such file or directory", p)
		}
		defer f.Close()
		if fi, err = f.Stat(); err != nil {
			return err
		}
	}
	if fi.IsDir() {
		return fmt.Errorf("permission denied")
	}
	if fi.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("permission denied")
	}
	return nil
}

// mountedPath maps a container path to its host source when it lies under a
// bind or volume target; the deepest target wins, as it does in the container.
func mountedPath(mounts []BindRecord, p string) (string, bool) {
	p = path.Clean(p)
	best := -1
	var rel string
	for i, m := range mounts {
		t := path.Clean(m.Target)
		var r string
		switch {
		case p == t:
			r = ""
		case strings.HasPrefix(p, strings.TrimSuffix(t, "/")+"/"):
			r = strings.TrimPrefix(p, strings.TrimSuffix(t, "/")+"/")
		default:
			continue
		}
		if best < 0 || len(t) > len(path.Clean(mounts[best].Target)) {
			best, rel = i, r
		}
	}
	if best < 0 {
		return "", false
	}
	return filepath.Join(mounts[best].Source, filepath.FromSlash(rel)), true
}

// mergeEnv overlays override on base by variable name.
func mergeEnv(base, override []string) []string {
	idx := map[string]int{}
	out := []string{}
	for _, list := range [][]string{base, override} {
		for _, kv := range list {
			k, _, _ := strings.Cut(kv, "=")
			if i, ok := idx[k]; ok {
				out[i] = kv
				continue
			}
			idx[k] = len(out)
			out = append(out, kv)
		}
	}
	return out
}

func hasEnv(env []string, key string) bool {
	for _, kv := range env {
		if strings.HasPrefix(kv, key+"=") {
			return true
		}
	}
	return false
}

func validHostname(h string) bool { return validName(h, false) }

// validHostsName is a name ThothDock writes into a container's /etc/hosts:
// a network alias (Compose uses each service name) or an --add-host name.
// Docker accepts underscores there, and resolvers read them from the hosts
// file; nothing that could start a new line or field is ever allowed.
func validHostsName(h string) bool { return validName(h, true) }

func validName(h string, underscore bool) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || underscore && c == '_') {
				return false
			}
		}
	}
	return true
}

// hostsFile is /etc/hosts for a container that shares the device network.
func hostsFile(hostname string, extra []string) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("127.0.0.1\tlocalhost\n::1\tlocalhost ip6-localhost ip6-loopback\nfe00::0\tip6-localnet\nff00::0\tip6-mcastprefix\nff02::1\tip6-allnodes\nff02::2\tip6-allrouters\n")
	fmt.Fprintf(&b, "127.0.1.1\t%s\n", hostname)
	for _, h := range extra {
		name, ip, ok := strings.Cut(h, ":")
		if !ok {
			return nil, errdefs.Invalid("invalid extra host %q: want host:ip", h)
		}
		if ip == "host-gateway" {
			ip = "127.0.0.1"
		}
		if net.ParseIP(ip) == nil || !validHostsName(name) {
			return nil, errdefs.Invalid("invalid extra host %q", h)
		}
		fmt.Fprintf(&b, "%s\t%s\n", ip, name)
	}
	return b.Bytes(), nil
}

// resolvFile is /etc/resolv.conf: explicit --dns/--dns-search, else the
// configured host file, else Docker's documented fallback (8.8.8.8, 8.8.4.4).
func resolvFile(hostFile string, dns, search, options []string) ([]byte, []string, error) {
	var warnings []string
	var b bytes.Buffer
	if len(dns) > 0 || len(search) > 0 || len(options) > 0 {
		for _, s := range dns {
			if net.ParseIP(s) == nil {
				return nil, nil, errdefs.Invalid("invalid DNS server %q", s)
			}
			fmt.Fprintf(&b, "nameserver %s\n", s)
		}
		for _, s := range search {
			if !validHostname(s) {
				return nil, nil, errdefs.Invalid("invalid DNS search domain %q", s)
			}
		}
		if len(search) > 0 {
			fmt.Fprintf(&b, "search %s\n", strings.Join(search, " "))
		}
		for _, o := range options {
			if strings.ContainsAny(o, "\n\r") {
				return nil, nil, errdefs.Invalid("invalid DNS option %q", o)
			}
		}
		if len(options) > 0 {
			fmt.Fprintf(&b, "options %s\n", strings.Join(options, " "))
		}
		if len(dns) > 0 {
			return b.Bytes(), nil, nil
		}
	}
	if hostFile != "" {
		if data, err := os.ReadFile(hostFile); err == nil {
			var keep bytes.Buffer
			sc := bufio.NewScanner(bytes.NewReader(data))
			found := false
			for sc.Scan() {
				line := sc.Text()
				if strings.HasPrefix(strings.TrimSpace(line), "nameserver") {
					found = true
				}
				keep.WriteString(line + "\n")
			}
			if found {
				keep.Write(b.Bytes())
				return keep.Bytes(), nil, nil
			}
		}
	}
	warnings = append(warnings, "No usable DNS configuration found; containers use Docker's default nameservers 8.8.8.8 and 8.8.4.4")
	b.WriteString("nameserver 8.8.8.8\nnameserver 8.8.4.4\n")
	return b.Bytes(), warnings, nil
}
