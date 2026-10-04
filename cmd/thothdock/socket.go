package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

// listenUnix creates the API socket, owner-only (0600). Its directory must
// be a real directory owned by this user: it is created 0700 when missing,
// tightened to 0700 when wider, and refused when it is a symlink or someone
// else's. Whatever occupies the socket path -- a stale socket left by a
// crash, a planted file or symlink -- is removed without being followed;
// callers hold the data root's lock, so no live daemon owns it.
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
	if err := os.Remove(sock); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, fmt.Errorf("removing stale %s: %w", sock, err)
	}
	// The umask makes the socket 0600 from the moment it exists.
	old := syscall.Umask(0o177)
	ln, err := net.Listen("unix", sock)
	syscall.Umask(old)
	if err != nil {
		return nil, nil, err
	}
	// Go would unlink the path on Close even if it were replaced meanwhile;
	// removeOwnSocket does that only for our own inode.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	sfi, err := os.Lstat(sock)
	if err != nil || sfi.Mode().Type() != os.ModeSocket || sfi.Mode().Perm() != 0o600 {
		ln.Close()
		return nil, nil, fmt.Errorf("socket %s was not created owner-only", sock)
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
