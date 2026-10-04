package layer

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/Lord1Egypt/ThothDock/internal/securefs"
)

// CopyStats counts a tree copy.
type CopyStats struct {
	Files, Dirs, Symlinks, Hardlinks, HardlinkCopies, Skipped, Bytes int64
}

type inode struct{ dev, ino uint64 }

type copier struct {
	dstRoot int
	stats   CopyStats
	seen    map[inode]string // first copy of a multiply-linked file, relative to dstRoot
}

// CopyTree copies the directory src to the new directory dst, which must
// not exist. Nothing is followed: symlinks are copied as links, files that
// share an inode in src share one in dst where the platform allows a hard
// link and are copied otherwise. Device nodes and FIFOs are skipped. This is
// how a container gets a private root filesystem from an image's shared,
// immutable one.
func CopyTree(src, dst string) (CopyStats, error) {
	srcfd, err := unix.Open(src, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return CopyStats{}, &os.PathError{Op: "open", Path: src, Err: err}
	}
	defer unix.Close(srcfd)
	var st unix.Stat_t
	if err := unix.Fstat(srcfd, &st); err != nil {
		return CopyStats{}, err
	}
	if err := unix.Mkdir(dst, 0o700); err != nil {
		return CopyStats{}, &os.PathError{Op: "mkdir", Path: dst, Err: err}
	}
	dstfd, err := unix.Open(dst, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return CopyStats{}, &os.PathError{Op: "open", Path: dst, Err: err}
	}
	defer unix.Close(dstfd)
	c := &copier{dstRoot: dstfd, seen: map[inode]string{}}
	if err := c.dir(srcfd, dstfd, ""); err != nil {
		return c.stats, err
	}
	if err := unix.Fchmod(dstfd, st.Mode&0o777|0o700); err != nil {
		return c.stats, err
	}
	return c.stats, nil
}

func (c *copier) dir(srcfd, dstfd int, rel string) error {
	names, err := securefs.ReadDirNames(srcfd)
	if err != nil {
		return err
	}
	for _, n := range names {
		p := n
		if rel != "" {
			p = rel + "/" + n
		}
		var st unix.Stat_t
		if err := unix.Fstatat(srcfd, n, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return fmt.Errorf("copy %s: %w", p, err)
		}
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			if err := c.subdir(srcfd, dstfd, n, p, &st); err != nil {
				return err
			}
		case unix.S_IFREG:
			if err := c.file(srcfd, dstfd, n, p, &st); err != nil {
				return err
			}
		case unix.S_IFLNK:
			t, err := securefs.Readlinkat(srcfd, n)
			if err != nil {
				return fmt.Errorf("copy %s: %w", p, err)
			}
			if err := unix.Symlinkat(t, dstfd, n); err != nil {
				return fmt.Errorf("copy %s: %w", p, err)
			}
			setTimes(dstfd, n, timeOf(st.Mtim))
			c.stats.Symlinks++
		default:
			c.stats.Skipped++
		}
	}
	return nil
}

func (c *copier) subdir(srcfd, dstfd int, n, p string, st *unix.Stat_t) error {
	if err := unix.Mkdirat(dstfd, n, 0o700); err != nil {
		return fmt.Errorf("copy %s: %w", p, err)
	}
	s, err := unix.Openat(srcfd, n, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("copy %s: %w", p, err)
	}
	defer unix.Close(s)
	d, err := unix.Openat(dstfd, n, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("copy %s: %w", p, err)
	}
	defer unix.Close(d)
	if err := c.dir(s, d, p); err != nil {
		return err
	}
	if err := unix.Fchmod(d, st.Mode&0o777|0o700); err != nil {
		return err
	}
	setTimes(dstfd, n, timeOf(st.Mtim))
	c.stats.Dirs++
	return nil
}

func (c *copier) file(srcfd, dstfd int, n, p string, st *unix.Stat_t) error {
	key := inode{uint64(st.Dev), uint64(st.Ino)}
	if st.Nlink > 1 {
		if first, ok := c.seen[key]; ok {
			par, err := openDstParent(c.dstRoot, first)
			if err != nil {
				return err
			}
			copied, err := LinkOrCopy(par, lastComp(first), dstfd, n)
			unix.Close(par)
			if err != nil {
				return fmt.Errorf("copy %s: %w", p, err)
			}
			c.stats.Hardlinks++
			if copied {
				c.stats.HardlinkCopies++
				c.stats.Bytes += st.Size
			}
			return nil
		}
		c.seen[key] = p
	}
	if err := CopyFileAt(srcfd, n, dstfd, n, st.Mode&0o7777); err != nil {
		return fmt.Errorf("copy %s: %w", p, err)
	}
	c.stats.Files++
	c.stats.Bytes += st.Size
	return nil
}

// openDstParent opens the parent of rel inside the destination tree. Every
// component is a directory this copy created, opened without following.
func openDstParent(root int, rel string) (int, error) {
	fd, err := unix.Dup(root)
	if err != nil {
		return -1, err
	}
	comps := strings.Split(rel, "/")
	for _, c := range comps[:len(comps)-1] {
		next, err := unix.Openat(fd, c, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if err != nil {
			return -1, err
		}
		fd = next
	}
	return fd, nil
}

func lastComp(rel string) string { return rel[strings.LastIndexByte(rel, '/')+1:] }
