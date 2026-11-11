package agents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
)

// Refusal is a real refused read of a working directory on this platform:
// the *os.PathError reading a process's cwd link returned when it was
// refused.
type Refusal = *os.PathError

// RealRefusal reads the cwd links /proc lists until one is refused, and
// returns that refusal. ok is false where none is.
func RealRefusal(context.Context) (Refusal, bool) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, false
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		_, err := os.Readlink("/proc/" + entry.Name() + "/cwd")
		var refusal *os.PathError
		if errors.Is(err, os.ErrPermission) && errors.As(err, &refusal) {
			return refusal, true
		}
	}
	return nil, false
}

// SweepRefusing is Sweeper.Sweep with every read the listing makes real
// except one: the cwd link of a process refuse names answers refusal, the
// *os.PathError a real refused read returned, with its path set to that
// process's link. The kernel does not refuse a read of a same-user process's
// working directory on demand, and this is the one read a test cannot make
// it refuse, so the shape is carried over from a real refusal rather than
// stated.
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

func sweepRefusingAfter(ctx context.Context, s *Sweeper, dir string, refuse func(pid int) bool, afterFirst func(), failOn int, refusal Refusal) (SweepResult, int, error) {
	var listings int
	r, err := s.sweep(ctx, dir, func(ctx context.Context, d string) (listing, error) {
		listings++
		if failOn > 0 && listings == failOn {
			return listing{}, fmt.Errorf("agents: planted listing failure on call %d", listings)
		}
		found, listErr := listUnderReading(ctx, d, func(pid int) (string, error) {
			if refuse(pid) {
				return "", &os.PathError{Op: refusal.Op, Path: "/proc/" + strconv.Itoa(pid) + "/cwd", Err: refusal.Err}
			}
			return readCwdLink(pid)
		})
		if listings == 1 && afterFirst != nil && listErr == nil {
			afterFirst()
		}
		return found, listErr
	})
	return r, listings, err
}

// ProcessIDs lists the identifiers of the numbered entries in /proc.
func ProcessIDs() ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, entry := range entries {
		if pid, err := strconv.Atoi(entry.Name()); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}
