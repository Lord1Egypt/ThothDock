package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// listenUnix creates the API socket, owner-only (0600).
//
//   - The socket's directory must be a real directory owned by this user. It
//     is created 0700 when missing and tightened to 0700 when wider; a
//     symlink or someone else's directory is refused.
//   - A symlink at the socket path is refused (fail closed: nothing but an
//     attacker or a bug leaves one there). A stale regular file or socket --
//     what a crash leaves -- is replaced.
//   - The socket is bound under a private temporary name with umask 0177 (so
//     it is 0600 from the moment it exists) and renamed over the final name
//     atomically: a client sees either no socket or the new, fully
//     permissioned one, never a half-made one or a stale one.
//
// Callers hold the data root's lock, so no live daemon owns the path.
func listenUnix(sock string) (net.Listener, os.FileInfo, error) {
	if len(sock) > 107 {
		return nil, nil, fmt.Errorf("socket path %s is %d bytes; Unix sockets allow 107: use a shorter --root or --socket", sock, len(sock))
	}
	dir := filepath.Dir(sock)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return nil, nil, err
	}
	if !fi.IsDir() {
		return nil, nil, fmt.Errorf("socket directory %s is not a directory (%v)", dir, fi.Mode().Type())
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() {
		return nil, nil, fmt.Errorf("socket directory %s is not owned by this user", dir)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return nil, nil, err
		}
	}
	if old, err := os.Lstat(sock); err == nil {
		if old.Mode()&os.ModeSymlink != 0 {
			return nil, nil, fmt.Errorf("socket path %s is a symlink; refusing to use it", sock)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	tmp := sock + "." + strconv.Itoa(os.Getpid())
	os.Remove(tmp)
	old := syscall.Umask(0o177)
	ln, err := net.Listen("unix", tmp)
	syscall.Umask(old)
	if err != nil {
		return nil, nil, err
	}
	// Go would unlink the path on Close even if it were replaced meanwhile;
	// removeOwnSocket does that only for our own inode.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	fail := func(err error) (net.Listener, os.FileInfo, error) {
		ln.Close()
		os.Remove(tmp)
		return nil, nil, err
	}
	sfi, err := os.Lstat(tmp)
	if err != nil || sfi.Mode().Type() != os.ModeSocket || sfi.Mode().Perm() != 0o600 {
		return fail(fmt.Errorf("socket %s was not created owner-only", tmp))
	}
	if err := os.Rename(tmp, sock); err != nil {
		return fail(err)
	}
	sfi, err = os.Lstat(sock)
	if err != nil {
		return fail(err)
	}
	return ln, sfi, nil
}

// removeOwnSocket unlinks sock only if it is still the socket this daemon
// created. Inode numbers are reused, so the type is checked too.
func removeOwnSocket(sock string, created os.FileInfo) {
	if fi, err := os.Lstat(sock); err == nil && fi.Mode().Type() == os.ModeSocket && os.SameFile(fi, created) {
		os.Remove(sock)
	}
}
