package agents

import (
	"context"
	"errors"
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
	return s.sweep(ctx, dir, func(ctx context.Context, d string) (listing, error) {
		return listUnderReading(ctx, d, func(pid int) (string, error) {
			if refuse(pid) {
				return "", &os.PathError{Op: refusal.Op, Path: "/proc/" + strconv.Itoa(pid) + "/cwd", Err: refusal.Err}
			}
			return readCwdLink(pid)
		})
	})
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
