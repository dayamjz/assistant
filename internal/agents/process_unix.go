//go:build unix

package agents

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// pollInterval is how often termination re-checks whether a process group has
// emptied. It is short enough that the ordinary case, where nothing survived
// the leader, costs no measurable time.
const pollInterval = 10 * time.Millisecond

// setProcessGroup puts the agent in a new process group of its own, with the
// agent as the group leader. Everything the agent starts inherits that group
// unless it deliberately leaves it, which is what makes one signal reach the
// whole tree.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminateTree ends the process group the invocation started, escalating from
// SIGTERM to SIGKILL after grace.
//
// It signals nothing when the group is already empty, which is both the common
// case and what keeps a signal from reaching an unrelated process that was
// later given the same group identifier. The residual race is worth naming
// rather than implying away: a group that held a process when it was checked
// and emptied before the signal was sent could in principle be reused between
// the two, and nothing here closes that window.
//
// leaderRunning and stopped are unused here: emptiness of the group is a
// better answer than either, and it is available on this platform.
func terminateTree(proc *os.Process, grace time.Duration, leaderRunning bool, stopped <-chan struct{}) {
	_, _ = leaderRunning, stopped
	if proc == nil {
		return
	}
	pgid := proc.Pid
	if groupEmpty(pgid) {
		return
	}
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	if waitForEmpty(pgid, grace) {
		return
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	// SIGKILL cannot be caught, so what remains after it is a process already
	// gone that has not been reaped yet, or one this process may not signal.
	// Waiting a little keeps the ordinary case honest without pretending the
	// second is fixable here.
	waitForEmpty(pgid, grace)
}

// waitForEmpty polls until the group holds no process or the deadline passes,
// and reports whether it emptied.
func waitForEmpty(pgid int, grace time.Duration) bool {
	deadline := time.Now().Add(grace)
	for {
		if groupEmpty(pgid) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(pollInterval)
	}
}

// groupEmpty reports whether the process group holds no process this process
// could signal. Signal zero performs the permission and existence checks
// without delivering anything, so ESRCH is the answer to "is the group gone".
//
// EPERM is treated as not empty: something is there and this process may not
// signal it, and reporting that as gone would be the false confidence a guard
// exists to avoid.
func groupEmpty(pgid int) bool {
	err := syscall.Kill(-pgid, 0)
	return err == syscall.ESRCH
}
