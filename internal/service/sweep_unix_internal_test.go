//go:build unix

package service

import (
	"os/exec"
	"syscall"
)

// detach starts cmd in a session of its own, so no process group this
// package or internal/agents signals reaches it.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
