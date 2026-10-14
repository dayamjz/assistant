//go:build unix

package journey_test

import (
	"os/exec"
	"syscall"
)

// joinGroup starts cmd inside the process group group leads rather than in a
// group of its own.
//
// It is how this harness acts as a task's worker. internal/service places a
// caller in a worker group from the caller's own credentials, by the process
// group the kernel reports for it, so a git started here in the group the
// service started the worker in is, to the product, a process of that worker.
// The stand-in worker itself prints and runs nothing, so what this stands in
// for is the git a real worker's tool call would run as its descendant.
func joinGroup(cmd *exec.Cmd, group int) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: group}
}
