// Package layer applies OCI/Docker image layers (tar streams) to a root
// filesystem directory and copies assembled root filesystems.
//
// Layers are untrusted. Every path goes through securefs, so it resolves
// inside the target root with chroot semantics, and the final component is
// never followed. Unsafe entries fail the whole layer: a pull with a
// malicious layer fails instead of producing a partially trusted image.
package layer

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Lord1Egypt/ThothDock/internal/securefs"
)

const (
	whiteoutPrefix = ".wh."
	whiteoutMeta   = ".wh..wh."
	opaqueMarker   = ".wh..wh..opq"
	maxNameLen     = 4096
	maxLinkLen     = 4095
)

// Stats counts what a layer did.
type Stats struct {
	Entries        int64
	Files          int64
	Dirs           int64
	Symlinks       int64
	Hardlinks      int64
	HardlinkCopies int64 // hard links materialized as copies (Android SELinux)
	Whiteouts      int64
	OpaqueDirs     int64
	SkippedSpecial int64 // device nodes and FIFOs, which an app cannot create
	Bytes          int64
}

// UnsafeEntryError is returned for an entry the extractor refuses.
type UnsafeEntryError struct {
	Name   string
	Reason string
}

func (e *UnsafeEntryError) Error() string {
	return fmt.Sprintf("unsafe layer entry %q: %s", e.Name, e.Reason)
}

func unsafeEntry(name, format string, args ...any) error {
	return &UnsafeEntryError{Name: name, Reason: fmt.Sprintf(format, args...)}
}

type pendingDir struct {
	path  string
	mode  uint32
	mtime time.Time
}

type applier struct {
	root    *securefs.Root
	stats   Stats
	written map[string]bool     // exact paths this layer created
	keep    map[string]struct{} // those paths and every ancestor
	opaque  []string
	dirs    []pendingDir
	lim     Limits
}

// Apply extracts the uncompressed layer tar r onto the directory rootDir.
func Apply(rootDir string, r io.Reader) (Stats, error) {
	return ApplyLimited(rootDir, r, Limits{})
}

// ApplyLimited is Apply with resource ceilings; see Limits.
func ApplyLimited(rootDir string, r io.Reader, lim Limits) (Stats, error) {
	root, err := securefs.OpenRoot(rootDir)
	if err != nil {
		return Stats{}, err
	}
	defer root.Close()
	a := &applier{root: root, written: map[string]bool{}, keep: map[string]struct{}{}, lim: lim}
	if lim.MaxStreamBytes > 0 {
		r = &streamLimiter{r: r, max: lim.MaxStreamBytes}
	}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return a.stats, fmt.Errorf("reading layer: %w", err)
		}
		if err := a.entry(hdr, tr); err != nil {
			return a.stats, err
		}
	}
	// Drain trailing padding so a digest computed over the stream covers it.
	if _, err := io.Copy(io.Discard, r); err != nil {
		return a.stats, err
	}
	for _, d := range a.opaque {
		if err := a.applyOpaque(d); err != nil {
			return a.stats, err
		}
	}
	return a.stats, a.finishDirs()
}

// cleanName validates an archive name: relative, no "..", no NUL. The root
// itself yields no components.
func cleanName(name string) ([]string, error) {
	if name == "" {
		return nil, unsafeEntry(name, "empty name")
	}
	if len(name) > maxNameLen {
		return nil, unsafeEntry(name[:64]+"...", "name too long")
	}
	if strings.IndexByte(name, 0) >= 0 {
		return nil, unsafeEntry(name, "name contains NUL")
	}
	if strings.HasPrefix(name, "/") {
		return nil, unsafeEntry(name, "absolute path")
	}
	var out []string
	for _, c := range strings.Split(name, "/") {
		switch c {
		case "", ".":
			continue
		case "..":
			return nil, unsafeEntry(name, "path traversal (..)")
		}
		if len(c) > 255 {
			return nil, unsafeEntry(name, "component too long")
		}
		out = append(out, c)
	}
	return out, nil
}

func (a *applier) markWritten(comps []string) {
	p := strings.Join(comps, "/")
	a.written[p] = true
	for i := len(comps); i > 0; i-- {
		a.keep[strings.Join(comps[:i], "/")] = struct{}{}
	}
}

func (a *applier) entry(hdr *tar.Header, tr io.Reader) error {
	a.stats.Entries++
	if a.lim.MaxEntries > 0 && a.stats.Entries > a.lim.MaxEntries {
		return &LimitError{What: "entry count", Limit: a.lim.MaxEntries}
	}
	comps, err := cleanName(hdr.Name)
	if err != nil {
		return err
	}
	if hdr.Typeflag == tar.TypeXGlobalHeader {
		return nil
	}
	if len(comps) == 0 {
		if hdr.Typeflag == tar.TypeDir {
			return nil // "./": the root keeps ThothDock's own mode
		}
		return unsafeEntry(hdr.Name, "non-directory entry for the root")
	}
	rel := strings.Join(comps, "/")
	base := comps[len(comps)-1]

	if strings.HasPrefix(base, whiteoutPrefix) {
		return a.whiteout(hdr.Name, comps)
	}

	switch hdr.Typeflag {
	case tar.TypeDir, tar.TypeReg, tar.TypeRegA, tar.TypeGNUSparse, tar.TypeSymlink, tar.TypeLink:
	case tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
		a.stats.SkippedSpecial++
		return nil
	default:
		return unsafeEntry(hdr.Name, "unsupported entry type %q", hdr.Typeflag)
	}

	par, err := a.root.OpenParent(rel, true, func(created string) {
		a.dirs = append(a.dirs, pendingDir{path: created, mode: 0o755})
	})
	if err != nil {
		return fmt.Errorf("layer entry %q: %w", hdr.Name, err)
	}
	defer par.Close()

	switch hdr.Typeflag {
	case tar.TypeDir:
		err = a.dir(par, hdr, rel)
	case tar.TypeSymlink:
		err = a.symlink(par, hdr)
	case tar.TypeLink:
		err = a.hardlink(par, hdr, rel)
	default:
		err = a.file(par, hdr, tr)
	}
	if err != nil {
		return err
	}
	a.markWritten(comps)
	return nil
}

// replaceable clears the final component unless it is a directory that the
// entry keeps (a directory entry over a directory).
func replaceable(par *securefs.Parent, keepDir bool) (exists bool, err error) {
	var st unix.Stat_t
	if err := unix.Fstatat(par.FD, par.Name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if err == unix.ENOENT {
			return false, nil
		}
		return false, err
	}
	if keepDir && st.Mode&unix.S_IFMT == unix.S_IFDIR {
		return true, nil
	}
	return false, securefs.RemoveAt(par.FD, par.Name)
}

func (a *applier) dir(par *securefs.Parent, hdr *tar.Header, rel string) error {
	exists, err := replaceable(par, true)
	if err != nil {
		return fmt.Errorf("layer entry %q: %w", hdr.Name, err)
	}
	if !exists {
		if err := unix.Mkdirat(par.FD, par.Name, 0o700); err != nil {
			return fmt.Errorf("layer entry %q: mkdir: %w", hdr.Name, err)
		}
	}
	a.stats.Dirs++
	a.dirs = append(a.dirs, pendingDir{path: rel, mode: dirMode(hdr.Mode), mtime: hdr.ModTime})
	return nil
}

func (a *applier) file(par *securefs.Parent, hdr *tar.Header, tr io.Reader) error {
	if _, err := replaceable(par, false); err != nil {
		return fmt.Errorf("layer entry %q: %w", hdr.Name, err)
	}
	fd, err := unix.Openat(par.FD, par.Name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("layer entry %q: create: %w", hdr.Name, err)
	}
	f := os.NewFile(uintptr(fd), hdr.Name)
	var src io.Reader = tr
	budget := int64(-1)
	if a.lim.MaxFileBytes > 0 {
		// One byte past the budget, so exceeding it is seen as such and not
		// as a short file.
		budget = a.lim.MaxFileBytes - a.stats.Bytes
		src = io.LimitReader(tr, budget+1)
	}
	n, err := io.Copy(&spaceWriter{w: f, a: a}, src)
	if err == nil && budget >= 0 && n > budget {
		err = &LimitError{What: "extracted size", Limit: a.lim.MaxFileBytes}
	}
	if err == nil && n != hdr.Size {
		err = io.ErrUnexpectedEOF
	}
	if err == nil {
		err = f.Chmod(os.FileMode(fileMode(hdr.Mode)))
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("layer entry %q: %w", hdr.Name, err)
	}
	a.stats.Files++
	a.stats.Bytes += n
	setTimes(par.FD, par.Name, hdr.ModTime)
	return nil
}

func (a *applier) symlink(par *securefs.Parent, hdr *tar.Header) error {
	t := hdr.Linkname
	if t == "" || len(t) > maxLinkLen || strings.IndexByte(t, 0) >= 0 {
		return unsafeEntry(hdr.Name, "invalid symlink target")
	}
	if _, err := replaceable(par, false); err != nil {
		return fmt.Errorf("layer entry %q: %w", hdr.Name, err)
	}
	// The target is stored verbatim: it is a guest path, resolved later by
	// PRoot inside the container, and never followed by ThothDock.
	if err := unix.Symlinkat(t, par.FD, par.Name); err != nil {
		return fmt.Errorf("layer entry %q: symlink: %w", hdr.Name, err)
	}
	a.stats.Symlinks++
	setTimes(par.FD, par.Name, hdr.ModTime)
	return nil
}

func (a *applier) hardlink(par *securefs.Parent, hdr *tar.Header, rel string) error {
	tcomps, err := cleanName(hdr.Linkname)
	if err != nil || len(tcomps) == 0 {
		return unsafeEntry(hdr.Name, "unsafe hard link target %q", hdr.Linkname)
	}
	trel := strings.Join(tcomps, "/")
	if trel == rel {
		return nil
	}
	tpar, err := a.root.OpenParent(trel, false, nil)
	if err != nil {
		return unsafeEntry(hdr.Name, "hard link target %q: %v", hdr.Linkname, err)
	}
	defer tpar.Close()
	var st unix.Stat_t
	if err := unix.Fstatat(tpar.FD, tpar.Name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return unsafeEntry(hdr.Name, "hard link target %q: %v", hdr.Linkname, err)
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFREG, unix.S_IFLNK:
	default:
		return unsafeEntry(hdr.Name, "hard link target %q is not a regular file or symlink", hdr.Linkname)
	}
	if _, err := replaceable(par, false); err != nil {
		return fmt.Errorf("layer entry %q: %w", hdr.Name, err)
	}
	if a.lim.CheckSpace != nil && st.Mode&unix.S_IFMT == unix.S_IFREG {
		if err := a.lim.CheckSpace(); err != nil {
			return err
		}
	}
	copied, err := LinkOrCopy(tpar.FD, tpar.Name, par.FD, par.Name)
	if err != nil {
		return fmt.Errorf("layer entry %q: %w", hdr.Name, err)
	}
	a.stats.Hardlinks++
	if copied {
		a.stats.HardlinkCopies++
		// A copy costs the file's size again: many links to one big file
		// must not get around the extracted-size ceiling.
		if st.Mode&unix.S_IFMT == unix.S_IFREG {
			a.stats.Bytes += st.Size
			if a.lim.MaxFileBytes > 0 && a.stats.Bytes > a.lim.MaxFileBytes {
				return &LimitError{What: "extracted size", Limit: a.lim.MaxFileBytes}
			}
		}
	}
	return nil
}

// LinkOrCopy hard-links (odir, oname) to (ndir, nname) without following
// either. Android forbids untrusted apps to create hard links (SELinux
// "neverallow all_untrusted_apps file_type:file link"), so a refused link
// becomes a copy: a regular file read through O_NOFOLLOW, a symlink by its
// target text. It reports whether a copy was made.
func LinkOrCopy(odir int, oname string, ndir int, nname string) (bool, error) {
	err := unix.Linkat(odir, oname, ndir, nname, 0)
	if err == nil {
		return false, nil
	}
	if err != unix.EPERM && err != unix.EACCES && err != unix.EXDEV && err != unix.EMLINK {
		return false, fmt.Errorf("link: %w", err)
	}
	var st unix.Stat_t
	if err := unix.Fstatat(odir, oname, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return false, err
	}
	if st.Mode&unix.S_IFMT == unix.S_IFLNK {
		t, err := securefs.Readlinkat(odir, oname)
		if err != nil {
			return false, err
		}
		return true, unix.Symlinkat(t, ndir, nname)
	}
	return true, CopyFileAt(odir, oname, ndir, nname, st.Mode&0o7777)
}

// CopyFileAt copies a regular file without following symlinks at either end.
func CopyFileAt(odir int, oname string, ndir int, nname string, mode uint32) error {
	in, err := unix.Openat(odir, oname, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	inf := os.NewFile(uintptr(in), oname)
	defer inf.Close()
	fi, err := inf.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", oname)
	}
	out, err := unix.Openat(ndir, nname, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	outf := os.NewFile(uintptr(out), nname)
	_, err = io.Copy(outf, inf)
	if err == nil {
		err = outf.Chmod(os.FileMode(fileMode(int64(mode))))
	}
	if cerr := outf.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		setTimes(ndir, nname, fi.ModTime())
	}
	return err
}

func (a *applier) whiteout(name string, comps []string) error {
	base := comps[len(comps)-1]
	parentRel := strings.Join(comps[:len(comps)-1], "/")
	if base == opaqueMarker {
		a.stats.OpaqueDirs++
		a.opaque = append(a.opaque, parentRel)
		return nil
	}
	if strings.HasPrefix(base, whiteoutMeta) {
		return nil // other AUFS metadata (.wh..wh.plnk etc.) carries nothing
	}
	target := strings.TrimPrefix(base, whiteoutPrefix)
	if target == "" || target == "." || target == ".." {
		return unsafeEntry(name, "invalid whiteout target")
	}
	a.stats.Whiteouts++
	rel := target
	if parentRel != "" {
		rel = parentRel + "/" + target
	}
	// A whiteout hides lower layers only; never what this layer added.
	if a.written[rel] {
		return nil
	}
	if err := a.root.RemoveAll(rel); err != nil && !errors.Is(err, unix.ENOTDIR) {
		return fmt.Errorf("whiteout %q: %w", name, err)
	}
	return nil
}

// applyOpaque removes everything below dir that this layer did not write:
// an opaque directory hides all of its lower content.
func (a *applier) applyOpaque(dir string) error {
	fd, err := a.root.OpenDir(dir, false)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return fmt.Errorf("opaque directory %q: %w", dir, err)
	}
	defer unix.Close(fd)
	return a.pruneLower(fd, dir)
}

func (a *applier) pruneLower(fd int, dir string) error {
	names, err := securefs.ReadDirNames(fd)
	if err != nil {
		return err
	}
	for _, n := range names {
		p := n
		if dir != "" {
			p = dir + "/" + n
		}
		if _, ok := a.keep[p]; !ok {
			if err := securefs.RemoveAt(fd, n); err != nil {
				return err
			}
			continue
		}
		var st unix.Stat_t
		if err := unix.Fstatat(fd, n, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		if st.Mode&unix.S_IFMT != unix.S_IFDIR {
			continue
		}
		child, err := unix.Openat(fd, n, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		err = a.pruneLower(child, p)
		unix.Close(child)
		if err != nil {
			return err
		}
	}
	return nil
}

// finishDirs applies directory modes and times once every entry exists,
// deepest first so a parent's time is not disturbed by its children.
func (a *applier) finishDirs() error {
	sort.SliceStable(a.dirs, func(i, j int) bool {
		return strings.Count(a.dirs[i].path, "/") > strings.Count(a.dirs[j].path, "/")
	})
	for _, d := range a.dirs {
		par, err := a.root.OpenParent(d.path, false, nil)
		if err != nil {
			if errors.Is(err, unix.ENOENT) {
				continue // removed by a later whiteout
			}
			return err
		}
		var st unix.Stat_t
		if err := unix.Fstatat(par.FD, par.Name, &st, unix.AT_SYMLINK_NOFOLLOW); err == nil && st.Mode&unix.S_IFMT == unix.S_IFDIR {
			if err := unix.Fchmodat(par.FD, par.Name, d.mode, 0); err != nil {
				par.Close()
				return fmt.Errorf("chmod %q: %w", d.path, err)
			}
			if !d.mtime.IsZero() {
				setTimes(par.FD, par.Name, d.mtime)
			}
		}
		par.Close()
	}
	return nil
}

// File and directory modes keep the permission bits but never setuid,
// setgid or sticky (no privilege exists to honour them), and always leave
// the owner -- ThothDock -- able to read, copy and remove what it created.
func fileMode(m int64) uint32 { return uint32(m)&0o777 | 0o600 }
func dirMode(m int64) uint32  { return uint32(m)&0o777 | 0o700 }

func setTimes(dirfd int, name string, mtime time.Time) {
	if mtime.IsZero() {
		return
	}
	ts := unix.NsecToTimespec(mtime.UnixNano())
	_ = unix.UtimesNanoAt(dirfd, name, []unix.Timespec{ts, ts}, unix.AT_SYMLINK_NOFOLLOW)
}
