package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Lord1Egypt/ThothDock/internal/platform"
	"github.com/Lord1Egypt/ThothDock/internal/runtime"
)

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// options shared by serve and doctor.
type options struct {
	root          string
	socket        string
	proot         string
	prootLoader   string
	prootLibDir   string
	link2symlink  string
	kernelRelease string
	resolvConf    string
	allowBind     multiFlag
	allowPublish  bool
	devTCP        string
	debug         bool
}

func (o *options) register(fs *flag.FlagSet) {
	def, _ := platform.DefaultRoot()
	fs.StringVar(&o.root, "root", def, "data root (env THOTHDOCK_ROOT)")
	fs.StringVar(&o.socket, "socket", "", "Unix socket path (default <root>/run/thothdock.sock); \"none\" only with --dev-tcp")
	fs.StringVar(&o.proot, "proot", os.Getenv("THOTHDOCK_PROOT"), "PRoot executable (env THOTHDOCK_PROOT; on Android the edition's libproot.so)")
	fs.StringVar(&o.prootLoader, "proot-loader", os.Getenv("PROOT_LOADER"), "PRoot loader (env PROOT_LOADER; on Android libproot_loader.so)")
	fs.StringVar(&o.prootLibDir, "proot-lib-dir", "", "directory with PRoot's shared libraries (libtalloc.so.2), prepended to LD_LIBRARY_PATH")
	fs.StringVar(&o.link2symlink, "link2symlink", "auto", "emulate hard links inside containers: auto (when the data root refuses hard links), on, off")
	fs.StringVar(&o.kernelRelease, "kernel-release", "", "kernel release reported inside containers (default: the real one)")
	fs.StringVar(&o.resolvConf, "resolv-conf", "/etc/resolv.conf", "resolver configuration for the daemon and containers")
	fs.Var(&o.allowBind, "allow-bind", "host directory containers may bind-mount from (repeatable; default none)")
	fs.BoolVar(&o.allowPublish, "allow-publish-nonlocal", false, "let -p name a host address other than loopback (default: published ports are reachable from this device only)")
	fs.StringVar(&o.devTCP, "dev-tcp", "", "DEVELOPMENT ONLY: also listen on this loopback TCP address (127.0.0.1:PORT), unauthenticated")
	fs.BoolVar(&o.debug, "debug", false, "log every request")
}

func (o *options) layout() (platform.Layout, error) {
	if o.root == "" {
		return platform.Layout{}, errors.New("no data root: pass --root or set THOTHDOCK_ROOT")
	}
	abs, err := filepath.Abs(o.root)
	if err != nil {
		return platform.Layout{}, err
	}
	return platform.Layout{Root: abs}, nil
}

func (o *options) socketPath(l platform.Layout) string {
	if o.socket != "" {
		return o.socket
	}
	return l.Socket()
}

func findPRoot(path string) (string, error) {
	if path == "" {
		p, err := exec.LookPath("proot")
		if err != nil {
			return "", errors.New("no PRoot: pass --proot or set THOTHDOCK_PROOT (on Android: the Garden edition's libproot.so)")
		}
		path = p
	}
	return filepath.Abs(path)
}

// hardlinksWork tests whether the data root's filesystem lets this process
// create hard links (Android's SELinux policy forbids it for app data).
func hardlinksWork(dir string) bool {
	f, err := os.CreateTemp(dir, "linktest-")
	if err != nil {
		return false
	}
	f.Close()
	defer os.Remove(f.Name())
	if err := os.Link(f.Name(), f.Name()+".l"); err != nil {
		return false
	}
	os.Remove(f.Name() + ".l")
	return true
}

func (o *options) runtimeConfig(l platform.Layout) (runtime.PRootConfig, error) {
	p, err := findPRoot(o.proot)
	if err != nil {
		return runtime.PRootConfig{}, err
	}
	cfg := runtime.PRootConfig{Path: p, Loader: o.prootLoader, LibDir: o.prootLibDir,
		TmpDir: filepath.Join(l.Tmp(), "proot"), KernelRelease: o.kernelRelease}
	switch o.link2symlink {
	case "on":
		cfg.LinkToSymlink = true
	case "off":
	case "auto":
		cfg.LinkToSymlink = !hardlinksWork(l.Tmp())
	default:
		return cfg, fmt.Errorf("--link2symlink must be auto, on or off")
	}
	return cfg, nil
}

// prootVersion asks proot for its version. "--version" prints an ASCII-art
// banner whose last art line ends with the version: "5.1.0" upstream, a
// git description such as "trixie-v0.2.0-71-g54a965fc" in Garden builds.
func prootVersion(rt *runtime.PRootRuntime) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, rt.Config().Path, "--version")
	cmd.Env = rt.Env(runtime.Spec{})
	out, _ := cmd.CombinedOutput()
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "|__|") {
			if f := strings.Fields(line); len(f) > 0 {
				return "PRoot " + f[len(f)-1]
			}
		}
	}
	return "unknown"
}

// nameservers reads "nameserver" lines.
func nameservers(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "nameserver" && net.ParseIP(fields[1]) != nil {
			out = append(out, fields[1])
		}
	}
	return out
}

// useResolver points Go's resolver at path's nameservers when the system
// has no /etc/resolv.conf (Android), so the daemon can reach registries.
func useResolver(path string) {
	if _, err := os.Stat("/etc/resolv.conf"); err == nil && path == "/etc/resolv.conf" {
		return
	}
	if len(nameservers(path)) == 0 {
		return
	}
	// The file is read on every lookup: Android rewrites it when the
	// network changes.
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			lastErr := errors.New("no nameservers in " + path)
			for _, s := range nameservers(path) {
				c, err := d.DialContext(ctx, network, net.JoinHostPort(s, "53"))
				if err == nil {
					return c, nil
				}
				lastErr = err
			}
			return nil, lastErr
		},
	}
}

// lockRoot takes the data root's exclusive lock, so two daemons (or a
// daemon and gc) never share a store.
func lockRoot(l platform.Layout) (*os.File, error) {
	if err := os.MkdirAll(l.Run(), 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(l.Run(), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(l.LockFile(), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another thothdock process holds %s", l.LockFile())
	}
	return f, nil
}
