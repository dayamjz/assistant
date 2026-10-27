package raw

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// Windows is a start only the Windows build holds.
func Windows() *exec.Cmd { return exec.Command("cmd") }

// Syscalls names the syscall and x/sys starters the Windows build has.
// Referencing them is the use; calling them is not what the guard reads.
func Syscalls() {
	_ = syscall.StartProcess
	_ = syscall.Exec
	_ = syscall.CreateProcess
	_ = syscall.CreateProcessAsUser
	_ = windows.CreateProcess
	_ = windows.CreateProcessAsUser
}
