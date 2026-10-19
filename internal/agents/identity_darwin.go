package agents

import (
	"golang.org/x/sys/unix"
)

// szomb is SZOMB from the platform's sys/proc.h, the process state of one
// that has exited and not been reaped. It is spelled here because x/sys does
// not export it.
const szomb = 5

// startTime reads the process's entry in the kernel's process table and
// returns its start time in microseconds since the epoch. A query that
// returns no entry is a process that does not exist, which proves absence.
// A process that has exited and not been reaped still has an entry here,
// with the zombie flag set, and is counted as not running.
func startTime(pid int) (uint64, bool, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return 0, false, err
	}
	if len(procs) == 0 {
		return 0, false, nil
	}
	proc := procs[0].Proc
	if proc.P_stat == szomb {
		return 0, false, nil
	}
	return startOf(proc), true, nil
}

// startOf is the start time a process table entry carries, in microseconds
// since the epoch, as startTime reads it.
func startOf(proc unix.ExternProc) uint64 {
	t := proc.P_starttime
	return uint64(t.Sec)*1_000_000 + uint64(t.Usec)
}

// noTerminal is what the process table carries in Eproc.Tdev for a process
// with no controlling terminal: NODEV from the platform's sys/param.h, which
// is (dev_t)-1, spelled here because x/sys does not export it.
const noTerminal = -1

// lineage reads the parent, the process group and the controlling
// terminal's foreground group from the same process table entry startTime
// reads: Eproc.Ppid, Eproc.Pgid and Eproc.Tpgid, the last read as zero when
// Eproc.Tdev says the process has no controlling terminal.
func lineage(pid int) (Lineage, bool, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return Lineage{}, false, err
	}
	if len(procs) == 0 || procs[0].Proc.P_stat == szomb {
		return Lineage{}, false, nil
	}
	entry := procs[0]
	terminalGroup := 0
	if entry.Eproc.Tdev != noTerminal {
		terminalGroup = max(int(entry.Eproc.Tpgid), 0)
	}
	return Lineage{
		Identity:      Identity{PID: pid, Start: startOf(entry.Proc)},
		Parent:        int(entry.Eproc.Ppid),
		Group:         int(entry.Eproc.Pgid),
		TerminalGroup: terminalGroup,
	}, true, nil
}
