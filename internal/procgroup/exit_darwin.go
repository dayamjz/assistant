package procgroup

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// szomb is SZOMB from the platform's sys/proc.h, the process state of one
// that has exited and has not been reaped.
const szomb = 5

// AwaitExit blocks until pid, a child of this process that nothing has
// reaped, has exited, and returns without reaping it, so its identifier stays
// its own until the caller reaps it. It waits on a kqueue for the process's
// NOTE_EXIT, which reports an exit and reaps nothing, and then reads the
// process table, so an exit before the registration is not waited on for
// ever: a child that is a zombie, or that the registration could not find,
// has exited, since nothing but the caller reaps it. An error means the exit
// could not be observed, and says nothing about whether it happened.
func AwaitExit(pid int) error {
	kq, err := unix.Kqueue()
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(kq) }()
	var change unix.Kevent_t
	unix.SetKevent(&change, pid, unix.EVFILT_PROC, unix.EV_ADD|unix.EV_ONESHOT)
	change.Fflags = unix.NOTE_EXIT
	// The registration is made without blocking, so the process table can
	// be read between it and the wait.
	changes, timeout := []unix.Kevent_t{change}, &unix.Timespec{}
	events := make([]unix.Kevent_t, 1)
	for {
		n, err := unix.Kevent(kq, changes, events, timeout)
		switch {
		case errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.ESRCH):
			return nil
		case err != nil:
			return err
		case n == 1 && events[0].Flags&unix.EV_ERROR != 0 && events[0].Data == int64(unix.ESRCH):
			return nil
		case n == 1 && events[0].Flags&unix.EV_ERROR != 0:
			return unix.Errno(events[0].Data)
		case n == 1 && events[0].Fflags&unix.NOTE_EXIT != 0:
			return nil
		}
		if changes != nil {
			changes, timeout = nil, nil
			if running, err := running(pid); err != nil {
				return err
			} else if !running {
				return nil
			}
		}
	}
}

// running reads pid's entry in the kernel's process table and reports
// whether it names a process that has not exited: an absent entry and a
// zombie's both answer false, and an entry naming another process is an
// error rather than an answer.
func running(pid int) (bool, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return false, err
	}
	if len(procs) == 0 || procs[0].Proc.P_stat == szomb {
		return false, nil
	}
	if got := int(procs[0].Proc.P_pid); got != pid {
		return false, fmt.Errorf("procgroup: the process table entry asked for %d names %d", pid, got)
	}
	return true, nil
}
