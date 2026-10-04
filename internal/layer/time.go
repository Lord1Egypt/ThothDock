package layer

import (
	"time"

	"golang.org/x/sys/unix"
)

func timeOf(ts unix.Timespec) time.Time { return time.Unix(ts.Unix()) }
