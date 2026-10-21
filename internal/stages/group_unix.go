//go:build unix

package stages

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// inGroup starts cmd as the leader of a process group of its own, has
// cancelling its context end that whole group with SIGKILL rather than the
// interpreter alone, and returns what ends the group once cmd.Wait has
// returned. runCommand calls the returned function on every path that
// started the command, so a descendant still in the group is ended with the
// command whether it exited on its own, was ended at its deadline, or was
// cancelled, and it cannot go on holding the output pipe or the host's
// cores once the call returned. That is PRD section 8's "a process group
// the invoker ends when the call returns or times out".
//
// The cancelling signal is sent while os/exec is still waiting on the
// leader, so the leader is not yet reaped and its identifier still names
// this group. The signal sent after Wait is not: the leader has been reaped
// by then, so the identifier names this group only while some member of it
// is still running, and a group whose last member exited between Wait
// returning and the signal leaves an identifier the system may reuse. The
// window is that gap alone, and nothing here closes it; internal/agents
// names the same window for its own groups.
//
// A descendant that left the group, by setsid or setpgid of its own, is
// reached by neither signal. awaitOutput still bounds how long such a
// descendant can hold the output pipe, and nothing ends it.
func inGroup(cmd *exec.Cmd) (end func()) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}
}
