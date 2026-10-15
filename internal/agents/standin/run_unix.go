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

// groupOf is the process group of a running process, read from the
// operating system, or zero when it could not be read.
func groupOf(pid int) int {
	group, err := syscall.Getpgid(pid)
	if err != nil {
		return 0
	}
	return group
}
