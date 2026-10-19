//go:build unix && !darwin

package standin

import "syscall"

// groupOf is the process group of pid, a child of this process it has not
// yet waited for, read from the operating system, or zero when it could not
// be read. runCommand reads it before it waits for the child, so the
// identifier still names that child and no other process.
// TestGroupOfReadsAnExitingChildBeforeItIsReaped holds this read to the
// window #360 found on macOS, a child that is exiting and has not been
// reaped, on every platform that test runs on; group_darwin.go says why
// macOS reads otherwise.
func groupOf(pid int) int {
	group, err := syscall.Getpgid(pid)
	if err != nil {
		return 0
	}
	return group
}
