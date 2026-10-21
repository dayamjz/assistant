package procgroup

import (
	"errors"

	"golang.org/x/sys/unix"
)

// AwaitExit blocks until pid, a child of this process that nothing has
// reaped, has exited, and returns without reaping it, so its identifier stays
// its own until the caller reaps it. It is waitid on the one process with
// WNOWAIT, which leaves the child waitable. An error means the exit could not
// be observed, and says nothing about whether it happened.
func AwaitExit(pid int) error {
	for {
		var info unix.Siginfo
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		return err
	}
}
