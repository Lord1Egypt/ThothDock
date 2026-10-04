package runtime

import (
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// openPTY allocates a pseudo-terminal pair through /dev/ptmx, which
// unprivileged Android apps may use (it is how every terminal app works).
func openPTY() (master, slave *os.File, err error) {
	mfd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	if err := unix.IoctlSetPointerInt(mfd, unix.TIOCSPTLCK, 0); err != nil {
		unix.Close(mfd)
		return nil, nil, err
	}
	n, err := unix.IoctlGetUint32(mfd, unix.TIOCGPTN)
	if err != nil {
		unix.Close(mfd)
		return nil, nil, err
	}
	name := "/dev/pts/" + strconv.FormatUint(uint64(n), 10)
	sfd, err := unix.Open(name, unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		unix.Close(mfd)
		return nil, nil, err
	}
	return os.NewFile(uintptr(mfd), "/dev/ptmx"), os.NewFile(uintptr(sfd), name), nil
}
