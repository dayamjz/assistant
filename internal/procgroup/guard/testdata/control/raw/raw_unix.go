//go:build darwin || linux

package raw

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// Unix names the syscall and x/sys starters the unix builds have. The names
// are referenced and not called: the guard asks the type checker, and a
// string in Starters that is not that checker's name for the function is a
// use this file holds and the test does not see.
func Unix() {
	_ = syscall.ForkExec
	_ = syscall.StartProcess
	_ = syscall.Exec
	_ = unix.Exec
}
