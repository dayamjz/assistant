package agents

import "golang.org/x/sys/unix"

// exitedOnly asks the kernel's process table for the group's members and
// reports whether at least one was listed and every one listed carries the
// zombie state, which is a process that has exited and not been reaped. A
// failed query answers no, so an unreadable table never reads as an exited
// group.
func exitedOnly(pgid int) bool {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pgid)
	if err != nil || len(procs) == 0 {
		return false
	}
	for _, proc := range procs {
		if proc.Proc.P_stat != szomb {
			return false
		}
	}
	return true
}
