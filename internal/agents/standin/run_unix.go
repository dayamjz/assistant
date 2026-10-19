//go:build unix

package standin

import (
	"os/exec"
	"syscall"
)

// ownGroup starts cmd as the leader of a new process group, which is the
// shape runCommand models.
func ownGroup(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return nil
}
