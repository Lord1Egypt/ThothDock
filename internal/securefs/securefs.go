// Package securefs works on paths inside a root directory with chroot
// semantics. Every component is opened relative to a directory file
// descriptor with O_NOFOLLOW; a symlink met on the way is read and resolved
// as if the root were "/", and ".." never climbs above the root. Whatever
// symlinks a tree contains -- absolute, relative, dangling, chained -- no
// operation can reach outside the root, and no check-then-use race exists
// because each step starts from a descriptor already held.
//
// Image layers and container root filesystems are untrusted input; every
// ThothDock read or write inside them goes through this package.
package securefs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// MaxSymlinks bounds symlink expansion during one resolution, like the
// kernel's ELOOP limit.
const MaxSymlinks = 40

// Root is an open root directory.
type Root struct {
	fd   int
	path string
}

// OpenRoot opens dir as a root. dir itself is trusted (it is ThothDock's own
// storage); only what lies below it is not.
func OpenRoot(dir string) (*Root, error) {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: dir, Err: err}
	}
	return &Root{fd: fd, path: dir}, nil
}

// Path is the host path of the root.
func (r *Root) Path() string { return r.path }

// FD is the root's directory descriptor, owned by r.
func (r *Root) FD() int { return r.fd }

// Close releases the root.
func (r *Root) Close() error { return unix.Close(r.fd) }

// Split turns a guest path into components. Absolute and relative paths are
// both taken relative to the root. NUL is rejected.
func Split(p string) ([]string, error) {
	if strings.IndexByte(p, 0) >= 0 {
		return nil, fmt.Errorf("path contains NUL")
	}
	var out []string
	for _, c := range strings.Split(p, "/") {
		if c != "" && c != "." {
			out = append(out, c)
		}
	}
	return out, nil
}

// walker resolves directories, keeping a stack of open descriptors whose
// names are the resolved (symlink-free) path from the root.
type walker struct {
	root  *Root
	fds   []int
	names []string
	links int
}

func (r *Root) newWalker() (*walker, error) {
	fd, err := unix.Dup(r.fd)
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(fd)
	return &walker{root: r, fds: []int{fd}}, nil
}

func (w *walker) top() int { return w.fds[len(w.fds)-1] }

func (w *walker) pop() {
	unix.Close(w.top())
	w.fds = w.fds[:len(w.fds)-1]
	w.names = w.names[:len(w.names)-1]
}

func (w *walker) toRoot() {
	for len(w.fds) > 1 {
		w.pop()
	}
}

func (w *walker) close() {
	for _, fd := range w.fds {
		unix.Close(fd)
	}
	w.fds = nil
}

// release hands the top descriptor to the caller and closes the rest.
func (w *walker) release() int {
	fd := w.top()
	for _, f := range w.fds[:len(w.fds)-1] {
		unix.Close(f)
	}
	w.fds = nil
	return fd
}

func (w *walker) resolved() string { return "/" + strings.Join(w.names, "/") }

// expand queues the components of a symlink's target in place of the link.
func (w *walker) expand(target string, rest []string) ([]string, error) {
	w.links++
	if w.links > MaxSymlinks {
		return nil, unix.ELOOP
	}
	if strings.HasPrefix(target, "/") {
		w.toRoot()
	}
	tc, err := Split(target)
	if err != nil {
		return nil, err
	}
	return append(tc, rest...), nil
}

// dirs walks comps as directories. With mkdir, missing directories are
// created (mode 0700) and created(resolvedPath) is called for each.
func (w *walker) dirs(comps []string, mkdir bool, created func(string)) error {
	queue := append([]string(nil), comps...)
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		switch c {
		case "", ".":
			continue
		case "..":
			if len(w.fds) > 1 {
				w.pop()
			}
			continue
		}
		fd, err := unix.Openat(w.top(), c, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err == unix.ENOENT && mkdir {
			if merr := unix.Mkdirat(w.top(), c, 0o700); merr != nil && merr != unix.EEXIST {
				return &os.PathError{Op: "mkdir", Path: w.resolved() + "/" + c, Err: merr}
			} else if merr == nil && created != nil {
				created(strings.TrimPrefix(w.resolved()+"/"+c, "/"))
			}
			fd, err = unix.Openat(w.top(), c, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		if err == nil {
			w.fds = append(w.fds, fd)
			w.names = append(w.names, c)
			continue
		}
		if err == unix.ELOOP || err == unix.ENOTDIR {
			target, rerr := readlinkat(w.top(), c)
			if rerr != nil {
				return &os.PathError{Op: "walk", Path: w.resolved() + "/" + c, Err: unix.ENOTDIR}
			}
			if queue, err = w.expand(target, queue); err != nil {
				return &os.PathError{Op: "walk", Path: w.resolved() + "/" + c, Err: err}
			}
			continue
		}
		return &os.PathError{Op: "walk", Path: w.resolved() + "/" + c, Err: err}
	}
	return nil
}

// Parent is an open parent directory and the final component below it.
type Parent struct {
	FD       int
	Name     string
	Resolved string // resolved guest path of the parent directory
}

// Close releases the parent descriptor.
func (p *Parent) Close() error { return unix.Close(p.FD) }

// OpenParent resolves every component of p except the last, following
// symlinks inside the root. The last component is returned unresolved, so
// the caller decides whether to follow it. With mkdir, missing parents are
// created; created, if not nil, learns each one.
func (r *Root) OpenParent(p string, mkdir bool, created func(string)) (*Parent, error) {
	comps, err := Split(p)
	if err != nil {
		return nil, err
	}
	if len(comps) == 0 {
		return nil, fmt.Errorf("path %q names the root itself", p)
	}
	last := comps[len(comps)-1]
	if last == ".." {
		return nil, fmt.Errorf("path %q ends in ..", p)
	}
	w, err := r.newWalker()
	if err != nil {
		return nil, err
	}
	if err := w.dirs(comps[:len(comps)-1], mkdir, created); err != nil {
		w.close()
		return nil, err
	}
	res := w.resolved()
	return &Parent{FD: w.release(), Name: last, Resolved: res}, nil
}

// OpenDir resolves p (following symlinks inside the root) to a directory.
func (r *Root) OpenDir(p string, mkdir bool) (int, error) {
	comps, err := Split(p)
	if err != nil {
		return -1, err
	}
	w, err := r.newWalker()
	if err != nil {
		return -1, err
	}
	if err := w.dirs(comps, mkdir, nil); err != nil {
		w.close()
		return -1, err
	}
	return w.release(), nil
}

// MkdirAll creates p and its parents inside the root.
func (r *Root) MkdirAll(p string) error {
	fd, err := r.OpenDir(p, true)
	if err != nil {
		return err
	}
	return unix.Close(fd)
}

// OpenFile opens p read-only, following symlinks inside the root, including
// a symlink in the final component.
func (r *Root) OpenFile(p string) (*os.File, error) {
	cur := p
	links := 0
	for {
		par, err := r.OpenParent(cur, false, nil)
		if err != nil {
			return nil, err
		}
		fd, err := unix.Openat(par.FD, par.Name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err == unix.ELOOP {
			target, rerr := readlinkat(par.FD, par.Name)
			par.Close()
			if rerr != nil {
				return nil, &os.PathError{Op: "open", Path: p, Err: err}
			}
			links++
			if links > MaxSymlinks {
				return nil, &os.PathError{Op: "open", Path: p, Err: unix.ELOOP}
			}
			if strings.HasPrefix(target, "/") {
				cur = target
			} else {
				cur = par.Resolved + "/" + target
			}
			continue
		}
		par.Close()
		if err != nil {
			return nil, &os.PathError{Op: "open", Path: p, Err: err}
		}
		return os.NewFile(uintptr(fd), p), nil
	}
}

// ReadFile reads at most max bytes of p (see OpenFile).
func (r *Root) ReadFile(p string, max int64) ([]byte, error) {
	f, err := r.OpenFile(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", p)
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s exceeds %d bytes", p, max)
	}
	return data, nil
}

// RemoveAll removes p (never following its final component) and, if it is
// a directory, everything below it. A missing p is not an error.
func (r *Root) RemoveAll(p string) error {
	par, err := r.OpenParent(p, false, nil)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return err
	}
	defer par.Close()
	return RemoveAt(par.FD, par.Name)
}

// RemoveAt removes name inside directory dirfd without following it; a
// directory is emptied first. Directories without owner write or search
// permission are opened up so ThothDock can always clean what it created.
func RemoveAt(dirfd int, name string) error {
	var st unix.Stat_t
	if err := unix.Fstatat(dirfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if err == unix.ENOENT {
			return nil
		}
		return &os.PathError{Op: "lstat", Path: name, Err: err}
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		if err := unix.Unlinkat(dirfd, name, 0); err != nil && err != unix.ENOENT {
			return &os.PathError{Op: "unlink", Path: name, Err: err}
		}
		return nil
	}
	if st.Mode&0o700 != 0o700 {
		_ = unix.Fchmodat(dirfd, name, (st.Mode&0o7777)|0o700, 0)
	}
	fd, err := unix.Openat(dirfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return &os.PathError{Op: "open", Path: name, Err: err}
	}
	names, err := ReadDirNames(fd)
	if err != nil {
		unix.Close(fd)
		return err
	}
	for _, n := range names {
		if err := RemoveAt(fd, n); err != nil {
			unix.Close(fd)
			return err
		}
	}
	unix.Close(fd)
	if err := unix.Unlinkat(dirfd, name, unix.AT_REMOVEDIR); err != nil && err != unix.ENOENT {
		return &os.PathError{Op: "rmdir", Path: name, Err: err}
	}
	return nil
}

// RemoveTree removes a whole host directory tree that ThothDock owns, such
// as a container's root filesystem, without following any symlink in it.
func RemoveTree(path string) error {
	dir, base := splitHostPath(path)
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		if err == unix.ENOENT {
			return nil
		}
		return &os.PathError{Op: "open", Path: dir, Err: err}
	}
	defer unix.Close(fd)
	return RemoveAt(fd, base)
}

func splitHostPath(p string) (string, string) {
	p = strings.TrimRight(p, "/")
	i := strings.LastIndexByte(p, '/')
	if i <= 0 {
		return "/", strings.TrimPrefix(p, "/")
	}
	return p[:i], p[i+1:]
}

// ReadDirNames lists a directory descriptor without consuming it.
func ReadDirNames(fd int) ([]string, error) {
	dup, err := unix.Dup(fd)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(dup), "dir")
	defer f.Close()
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}
	return f.Readdirnames(-1)
}

func readlinkat(dirfd int, name string) (string, error) {
	buf := make([]byte, unix.PathMax)
	n, err := unix.Readlinkat(dirfd, name, buf)
	if err != nil {
		return "", err
	}
	if n >= len(buf) {
		return "", unix.ENAMETOOLONG
	}
	return string(buf[:n]), nil
}

// Readlinkat reads a symlink's target relative to dirfd.
func Readlinkat(dirfd int, name string) (string, error) { return readlinkat(dirfd, name) }
