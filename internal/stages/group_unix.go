//go:build unix

package stages

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// endGroupOnCancel starts cmd as the leader of a process group of its own
// and has cancelling its context end that whole group with SIGKILL, rather
// than the interpreter alone. A descendant that stayed in the group, such as
// the child an interpreter forks for each command of a line, is ended with
// it, so it cannot go on holding the output pipe once the command was ended.
//
// The signal is sent only while os/exec is still waiting on the leader, so
// the leader is not yet reaped and its identifier still names this group;
// nothing is signalled after the command exited on its own. A descendant
// that left the group, by setsid or setpgid of its own, is not reached, and
// one that exits normally leaves nothing to end. It is mechanism for the
// cancelled case only: a command that ends on its own keeps the descendants
// it left, on the terms commandGrace states.
func endGroupOnCancel(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
