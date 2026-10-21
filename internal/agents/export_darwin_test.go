package agents

import (
	"context"
	"maps"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// Refusal is a real unreadable working directory on this platform: the
// identifier of a running process the real lsof reading left out of what it
// reported, which is the whole of what that reading produces for a process
// it could not read.
type Refusal struct{ PID int }

// RealRefusal runs the real reading and returns a process in the process
// table, other than this one, that is running and that the reading left
// out. ok is false where there is none, or the reading failed.
func RealRefusal(ctx context.Context) (Refusal, bool) {
	cwds, err := systemLsof.readCwds(ctx)
	if err != nil {
		return Refusal{}, false
	}
	pids, err := ProcessIDs()
	if err != nil {
		return Refusal{}, false
	}
	for _, pid := range pids {
		if _, reported := cwds[pid]; reported || pid <= 0 || pid == os.Getpid() {
			continue
		}
		if _, present, err := Identify(pid); err == nil && present {
			return Refusal{PID: pid}, true
		}
	}
	return Refusal{}, false
}

// SweepRefusing is Sweeper.Sweep with every read the listing makes real
// except one: the real lsof reading's answer has the working directory of
// every process refuse names removed from it, which is the answer that
// reading gives for a process it could not read, as the Refusal a test
// obtains first shows it giving for a real one. lsof does not leave out a
// same-user process's working directory on demand, and this is the one
// read a test cannot make it refuse.
func SweepRefusing(ctx context.Context, s *Sweeper, dir string, refuse func(pid int) bool, _ Refusal) (SweepResult, error) {
	return s.sweep(ctx, dir, func(ctx context.Context, d string) (listing, error) {
		return listUnderReading(ctx, d, func(ctx context.Context) (map[int]string, error) {
			cwds, err := systemLsof.readCwds(ctx)
			if err != nil {
				return nil, err
			}
			maps.DeleteFunc(cwds, func(pid int, _ string) bool { return refuse(pid) })
			return cwds, nil
		})
	})
}

// ProcessIDs lists the identifiers in the kernel's process table.
func ProcessIDs() ([]int, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	pids := make([]int, 0, len(procs))
	for _, proc := range procs {
		pids = append(pids, int(proc.Proc.P_pid))
	}
	return pids, nil
}

// ParseLsof exposes the parse of lsof's field output.
var ParseLsof = parseLsof

// LsofOutput is the real output of the lsof run readCwds makes, so a test
// can derive the shapes the parse refuses from what it is really given.
var LsofOutput = systemLsof.run

// LsofDeadline is the deadline the listing's lsof run carries.
const LsofDeadline = lsofDeadline

// LsofGrace is the grace an lsof run under deadline gives procgroup.Run,
// which is how long output held open past lsof's exit is waited on.
var LsofGrace = lsofGrace

// ReadCwdsWithLsof is the listing's read of the working directories, made
// through the same run with program in place of /usr/sbin/lsof and
// deadline in place of LsofDeadline, so a test can cancel ctx partway.
func ReadCwdsWithLsof(ctx context.Context, program string, deadline time.Duration) (map[int]string, error) {
	return lsofRun{program: program, deadline: deadline}.readCwds(ctx)
}

// SweepWithLsof is Sweeper.Sweep with every read the listing makes real and
// lsof's run made through the same code with program in place of
// /usr/sbin/lsof and deadline in place of LsofDeadline, so a test can stand
// a program that never exits where lsof is.
func SweepWithLsof(ctx context.Context, s *Sweeper, dir, program string, deadline time.Duration) (SweepResult, error) {
	run := lsofRun{program: program, deadline: deadline}
	return s.sweep(ctx, dir, func(ctx context.Context, d string) (listing, error) {
		return listUnderReading(ctx, d, run.readCwds)
	})
}
