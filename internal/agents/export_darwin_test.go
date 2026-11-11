package agents

import (
	"context"
	"fmt"
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
func SweepRefusing(ctx context.Context, s *Sweeper, dir string, refuse func(pid int) bool, refusal Refusal) (SweepResult, error) {
	r, _, err := SweepRefusingAfter(ctx, s, dir, refuse, nil, refusal)
	return r, err
}

// SweepRefusingAfter is SweepRefusing with afterFirst called after the first
// listing returns, before any re-list Sweeper makes when that listing's only
// blockers are unreadable processes. listings is how many times the listing
// ran. A nil afterFirst is SweepRefusing.
func SweepRefusingAfter(ctx context.Context, s *Sweeper, dir string, refuse func(pid int) bool, afterFirst func(), refusal Refusal) (SweepResult, int, error) {
	return sweepRefusingAfter(ctx, s, dir, refuse, afterFirst, 0, refusal)
}

// SweepRefusingFailingOn is SweepRefusingAfter whose listing answers a planted
// error on the failOn-th call (1-based), so a test can hold the re-list
// failure path. failOn 0 never plants a failure.
func SweepRefusingFailingOn(ctx context.Context, s *Sweeper, dir string, refuse func(pid int) bool, failOn int, refusal Refusal) (SweepResult, int, error) {
	return sweepRefusingAfter(ctx, s, dir, refuse, nil, failOn, refusal)
}

func sweepRefusingAfter(ctx context.Context, s *Sweeper, dir string, refuse func(pid int) bool, afterFirst func(), failOn int, _ Refusal) (SweepResult, int, error) {
	var listings int
	r, err := s.sweep(ctx, dir, func(ctx context.Context, d string) (listing, error) {
		listings++
		if failOn > 0 && listings == failOn {
			return listing{}, fmt.Errorf("agents: planted listing failure on call %d", listings)
		}
		found, listErr := listUnderReading(ctx, d, func(ctx context.Context) (map[int]string, error) {
			cwds, err := systemLsof.readCwds(ctx)
			if err != nil {
				return nil, err
			}
			maps.DeleteFunc(cwds, func(pid int, _ string) bool { return refuse(pid) })
			return cwds, nil
		})
		if listings == 1 && afterFirst != nil && listErr == nil {
			afterFirst()
		}
		return found, listErr
	})
	return r, listings, err
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

// ErrLsofNoSelf is the listing's refusal of lsof output that does not report
// this process's own working directory.
var ErrLsofNoSelf = errLsofNoSelf

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
//
// Beside the sweep's answer it returns how long lsof's run took, from the
// listing starting it to the listing having its answer, which is the
// interval deadline bounds. The reads of the process table the listing
// makes before that run are not under deadline, so a time taken around the
// whole sweep is not one deadline bounds. It is zero when the listing
// answered without starting the run.
func SweepWithLsof(ctx context.Context, s *Sweeper, dir, program string, deadline time.Duration) (SweepResult, time.Duration, error) {
	run := lsofRun{program: program, deadline: deadline}
	var took time.Duration
	r, err := s.sweep(ctx, dir, func(ctx context.Context, d string) (listing, error) {
		return listUnderReading(ctx, d, func(ctx context.Context) (map[int]string, error) {
			started := time.Now()
			defer func() { took = time.Since(started) }()
			return run.readCwds(ctx)
		})
	})
	return r, took, err
}

// LsofShare is the part of a run's deadline lsof itself is given before its
// group is ended. A run whose lsof is still running when it passes answers
// no earlier than this past its start; one whose lsof exited sooner, with
// its output still held open a grace later, answers ErrSweepTimedOut then.
var LsofShare = lsofShare
