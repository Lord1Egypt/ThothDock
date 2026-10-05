package engine

import "golang.org/x/sys/unix"

// unixOpenatCreate creates an empty regular file (a mount point for a file
// bind) without following a symlink in the final component, leaving an
// existing file alone.
func unixOpenatCreate(dirfd int, name string) (int, error) {
	fd, err := unix.Openat(dirfd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o644)
	if err != nil {
		return -1, err
	}
	return fd, unix.Close(fd)
}
