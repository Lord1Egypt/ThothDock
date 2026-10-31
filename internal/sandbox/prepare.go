package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/Lord1Egypt/ThothDock/internal/securefs"
)

// Request is what the guest's bwrap shim sends: the parsed command line, the
// environment bwrap would have started from, the identity to run as, and the
// contents of every descriptor a --file, --bind-data or --ro-bind-data named
// (the descriptors themselves cannot cross the socket).
type Request struct {
	Ops     []Op
	Env     []EnvOp
	BaseEnv []string // the shim's own environment, before Env applies
	Chdir   string
	Argv    []string
	UID     int
	GID     int
	Tty     bool
	Files   map[int][]byte
	// DieWithParent ends the sandbox when the shim's connection ends.
	DieWithParent bool
}

// Limits bound what one request may make the engine write.
const (
	MaxFileBytes  = 16 << 20 // all --file/--bind-data contents together
	MaxOps        = 4096
	maxLinkFollow = 40
)

// Bind is one host directory or file shown at Target inside the sandbox.
type Bind struct {
	Source string // host path, inside the guest root
	Target string // absolute path inside the sandbox
}

// Prepared is a sandbox ready to start: its root directory, the binds over
// it, and the process to run.
type Prepared struct {
	Root  string
	Binds []Bind
	Env   []string
	Cwd   string
	Argv  []string // Argv[0] is absolute, resolved through PATH
	// Warnings name what was asked for but cannot be honoured under PRoot.
	Warnings []string
}

// Prepare builds the sandbox root in dir (an empty directory the engine owns)
// and resolves every guest path the request names inside guest, the guest's
// root file system, with chroot semantics: no source can name a path outside
// it. dir is left for the caller to remove.
func Prepare(req *Request, guest *securefs.Root, dir string) (*Prepared, error) {
	if len(req.Ops) > MaxOps {
		return nil, fmt.Errorf("sandbox: %d operations, more than %d", len(req.Ops), MaxOps)
	}
	var total int
	for _, b := range req.Files {
		total += len(b)
	}
	if total > MaxFileBytes {
		return nil, fmt.Errorf("sandbox: %d bytes of file data, more than %d", total, MaxFileBytes)
	}
	if len(req.Argv) == 0 {
		return nil, ErrNoCommand
	}
	root, err := securefs.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	p := &Prepared{Root: dir}
	for _, op := range req.Ops {
		if err := p.apply(op, req, guest, root); err != nil {
			return nil, err
		}
	}
	p.Env = environment(req.BaseEnv, req.Env)
	p.Cwd = req.Chdir
	if p.Cwd == "" {
		p.Cwd = "/"
	}
	argv0, err := p.lookPath(req.Argv[0], guest, root)
	if err != nil {
		return nil, err
	}
	p.Argv = append([]string{argv0}, req.Argv[1:]...)
	return p, nil
}

func (p *Prepared) warn(format string, args ...any) {
	p.Warnings = append(p.Warnings, fmt.Sprintf(format, args...))
}

func cleanDest(d string) (string, error) {
	if !strings.HasPrefix(d, "/") || strings.ContainsRune(d, 0) {
		return "", fmt.Errorf("sandbox: destination %q is not an absolute path", d)
	}
	d = path.Clean(d)
	if d == "/" {
		return "", errors.New("sandbox: an operation cannot replace the sandbox root")
	}
	return d, nil
}

func (p *Prepared) apply(op Op, req *Request, guest, root *securefs.Root) error {
	var dest string
	if op.Kind != OpChmod || op.Dest != "" {
		d, err := cleanDest(op.Dest)
		if err != nil {
			return err
		}
		dest = d
	}
	switch op.Kind {
	case OpBind, OpROBind, OpDevBind:
		if op.Kind == OpDevBind && path.Clean(op.Src) == "/dev" && dest == "/dev" {
			return nil // the runtime binds /dev itself
		}
		src, isDir, err := ResolveGuest(guest, op.Src)
		if err != nil {
			if op.Try && errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("sandbox: bind source %q: %w", op.Src, err)
		}
		if op.Kind == OpROBind {
			p.warn("%s is bound writable: PRoot has no read-only binds", dest)
		}
		if err := mountpoint(root, dest, isDir); err != nil {
			return err
		}
		p.Binds = append(p.Binds, Bind{Source: src, Target: dest})
	case OpProc, OpDev:
		if (op.Kind == OpProc && dest != "/proc") || (op.Kind == OpDev && dest != "/dev") {
			return fmt.Errorf("sandbox: --%s is only supported on /%s", op.Kind, op.Kind)
		}
	case OpTmpfs, OpDir:
		if err := root.MkdirAll(dest); err != nil {
			return fmt.Errorf("sandbox: %s %s: %w", op.Kind, dest, err)
		}
		if op.Perms >= 0 {
			if err := chmodIn(root, dest, uint32(op.Perms)); err != nil {
				return err
			}
		}
	case OpSymlink:
		par, err := root.OpenParent(dest, true, nil)
		if err != nil {
			return fmt.Errorf("sandbox: symlink %s: %w", dest, err)
		}
		err = unix.Symlinkat(op.Src, par.FD, par.Name)
		par.Close()
		if err != nil && !errors.Is(err, unix.EEXIST) {
			return fmt.Errorf("sandbox: symlink %s: %w", dest, err)
		}
	case OpFile, OpBindData, OpROBindData:
		data, ok := req.Files[op.FD]
		if !ok {
			return fmt.Errorf("sandbox: no contents for descriptor %d (%s)", op.FD, dest)
		}
		mode := uint32(0o600)
		if op.Kind == OpROBindData {
			mode = 0o400
		}
		if op.Perms >= 0 {
			mode = uint32(op.Perms)
		}
		if err := writeIn(root, dest, data, mode); err != nil {
			return err
		}
	case OpChmod:
		if err := chmodIn(root, dest, uint32(op.Mode)); err != nil {
			return err
		}
	case OpRemountRO:
		p.warn("%s stays writable: PRoot cannot remount read-only", dest)
	case OpLockFile, OpMqueue:
		p.warn("--%s %s is not supported and was skipped", op.Kind, dest)
	case OpBindFD, OpROBindFD:
		return fmt.Errorf("sandbox: --%s cannot be passed to the engine", op.Kind)
	default:
		return fmt.Errorf("sandbox: unknown operation %q", op.Kind)
	}
	return nil
}

// ResolveGuest resolves p inside guest, following every symlink with chroot
// semantics, and returns the host path and whether it is a directory. A
// symlink whose target is an absolute host path inside the guest root (how
// PRoot's link2symlink stores hard links) is followed as the guest path it
// stands for.
func ResolveGuest(guest *securefs.Root, p string) (string, bool, error) {
	cur := p
	for i := 0; i <= maxLinkFollow; i++ {
		if path.Clean("/"+cur) == "/" {
			return guest.Path(), true, nil
		}
		par, err := guest.OpenParent(cur, false, nil)
		if err != nil {
			return "", false, err
		}
		var st unix.Stat_t
		err = unix.Fstatat(par.FD, par.Name, &st, unix.AT_SYMLINK_NOFOLLOW)
		if err != nil {
			par.Close()
			if errors.Is(err, unix.ENOENT) {
				return "", false, os.ErrNotExist
			}
			return "", false, err
		}
		if st.Mode&unix.S_IFMT != unix.S_IFLNK {
			par.Close()
			host := filepath.Join(guest.Path(), par.Resolved, par.Name)
			return host, st.Mode&unix.S_IFMT == unix.S_IFDIR, nil
		}
		target, err := securefs.Readlinkat(par.FD, par.Name)
		par.Close()
		if err != nil {
			return "", false, err
		}
		if prefix := guest.Path() + "/"; strings.HasPrefix(target, prefix) {
			target = "/" + strings.TrimPrefix(target, prefix)
		} else if !strings.HasPrefix(target, "/") {
			target = path.Join(par.Resolved, target)
		}
		cur = target
	}
	return "", false, fmt.Errorf("too many levels of symbolic links in %q", p)
}

// mountpoint makes dest exist in the sandbox root as a directory or an empty
// file, for a bind of that kind to land on.
func mountpoint(root *securefs.Root, dest string, dir bool) error {
	if dir {
		return root.MkdirAll(dest)
	}
	par, err := root.OpenParent(dest, true, nil)
	if err != nil {
		return err
	}
	defer par.Close()
	fd, err := unix.Openat(par.FD, par.Name, unix.O_CREAT|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		if errors.Is(err, unix.EEXIST) || errors.Is(err, unix.EISDIR) {
			return nil
		}
		return fmt.Errorf("sandbox: mount point %s: %w", dest, err)
	}
	return unix.Close(fd)
}

func writeIn(root *securefs.Root, dest string, data []byte, mode uint32) error {
	par, err := root.OpenParent(dest, true, nil)
	if err != nil {
		return err
	}
	defer par.Close()
	fd, err := unix.Openat(par.FD, par.Name, unix.O_CREAT|unix.O_TRUNC|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("sandbox: file %s: %w", dest, err)
	}
	f := os.NewFile(uintptr(fd), dest)
	_, werr := f.Write(data)
	cerr := unix.Fchmod(fd, mode&0o7777)
	f.Close()
	if werr != nil {
		return fmt.Errorf("sandbox: file %s: %w", dest, werr)
	}
	return cerr
}

func chmodIn(root *securefs.Root, dest string, mode uint32) error {
	par, err := root.OpenParent(dest, false, nil)
	if err != nil {
		return err
	}
	defer par.Close()
	if err := unix.Fchmodat(par.FD, par.Name, mode&0o7777, 0); err != nil {
		return fmt.Errorf("sandbox: chmod %s: %w", dest, err)
	}
	return nil
}

// environment applies bwrap's --clearenv/--setenv/--unsetenv, in order, to
// the environment bwrap started from.
func environment(base []string, ops []EnvOp) []string {
	var keys []string
	vals := map[string]string{}
	set := func(k, v string) {
		if _, ok := vals[k]; !ok {
			keys = append(keys, k)
		}
		vals[k] = v
	}
	for _, kv := range base {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			set(k, v)
		}
	}
	for _, op := range ops {
		switch {
		case op.Clear:
			keys, vals = nil, map[string]string{}
		case op.Unset:
			delete(vals, op.Name)
		default:
			set(op.Name, op.Value)
		}
	}
	out := make([]string, 0, len(vals))
	for _, k := range keys {
		if v, ok := vals[k]; ok {
			out = append(out, k+"="+v)
			delete(vals, k)
		}
	}
	return out
}

// lookPath resolves a command name through the sandbox's PATH, as bwrap's
// execvp does, looking through the binds and the sandbox root. Every
// candidate is resolved with chroot semantics, never through the host's view.
func (p *Prepared) lookPath(name string, guest, root *securefs.Root) (string, error) {
	if strings.Contains(name, "/") {
		if !strings.HasPrefix(name, "/") {
			return "", fmt.Errorf("sandbox: command %q must be absolute or a plain name", name)
		}
		return path.Clean(name), nil
	}
	pathVar := "/usr/local/bin:/usr/bin:/bin"
	for _, kv := range p.Env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			pathVar = v
		}
	}
	for _, dir := range strings.Split(pathVar, ":") {
		if !strings.HasPrefix(dir, "/") {
			continue
		}
		cand := path.Join(dir, name)
		host, isDir, err := p.resolveInSandbox(cand, guest, root)
		if err != nil || isDir {
			continue
		}
		if fi, err := os.Lstat(host); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			return cand, nil
		}
	}
	return "", fmt.Errorf("sandbox: %s: command not found in PATH %s", name, pathVar)
}

// resolveInSandbox resolves a path inside the sandbox to the host file that
// backs it: through the deepest bind that covers it (inside the guest root),
// else inside the sandbox root.
func (p *Prepared) resolveInSandbox(sp string, guest, root *securefs.Root) (string, bool, error) {
	best := -1
	for i, b := range p.Binds {
		if (sp == b.Target || strings.HasPrefix(sp, b.Target+"/")) && (best < 0 || len(b.Target) > len(p.Binds[best].Target)) {
			best = i
		}
	}
	if best < 0 {
		return ResolveGuest(root, sp)
	}
	b := p.Binds[best]
	rel, ok := strings.CutPrefix(b.Source, guest.Path())
	if !ok {
		return "", false, fmt.Errorf("bind source %s is outside the guest", b.Source)
	}
	return ResolveGuest(guest, path.Join("/", rel, strings.TrimPrefix(sp, b.Target)))
}
