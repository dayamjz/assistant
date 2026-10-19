package standin

import "golang.org/x/sys/unix"

// groupOf is the process group of pid, a child of this process it has not
// yet waited for, read from the operating system's process table, or zero
// when it could not be read.
//
// The entry is read through the kern.proc.pid sysctl rather than getpgid
// because of what #360 observed: a command that finished before runCommand
// read its group made getpgid answer "no such process" on macOS, both for a
// child that had begun exiting and for one that had exited and was not yet
// reaped, and the stand-in reported group 0. The process table entry carried
// the group in every read made of those two states. runCommand reads it
// before it waits for the child, so the identifier still names that child
// and no other process. TestGroupOfReadsAnExitingChildBeforeItIsReaped holds
// this read to that window.
func groupOf(pid int) int {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil || len(procs) == 0 {
		return 0
	}
	return int(procs[0].Eproc.Pgid)
}
