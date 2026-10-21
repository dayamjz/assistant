package agents_test

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

// waitExitedUnreaped blocks until pid, a child of this process, has exited,
// and leaves it unreaped: waitid with WNOWAIT returns on the exit and does
// not collect the status, so the child stays in the process table as a
// zombie until something waits on it again. It reports true, since this
// platform can plant one; any failure of the wait fails t.
func waitExitedUnreaped(t *testing.T, pid int) bool {
	t.Helper()
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			t.Fatalf("waiting on child %d's exit without reaping it: %v", pid, err)
		}
		return true
	}
}
