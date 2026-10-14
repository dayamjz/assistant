package agents

import (
	"context"
	"os"
	"strconv"
)

// SweepRefusing is Sweeper.Sweep with every read the listing makes real
// except one: the cwd link of a process refuse names answers refusal, the
// *os.PathError a real refused read returned, with its path set to that
// process's link. The kernel does not refuse a read of a same-user process's
// working directory on demand, and this is the one read a test cannot make
// it refuse, so the shape is carried over from a real refusal rather than
// stated.
func SweepRefusing(ctx context.Context, s *Sweeper, dir string, refuse func(pid int) bool, refusal *os.PathError) (SweepResult, error) {
	return s.sweep(ctx, dir, func(ctx context.Context, d string) (listing, error) {
		return listUnderReading(ctx, d, func(pid int) (string, error) {
			if refuse(pid) {
				return "", &os.PathError{Op: refusal.Op, Path: "/proc/" + strconv.Itoa(pid) + "/cwd", Err: refusal.Err}
			}
			return readCwdLink(pid)
		})
	})
}

// ClassifyRefused is the decision the listing makes about pid were its
// working directory refused, from pid's real identity and status entry and
// this process's real viewer, with signal standing for the zero signal's
// answer.
func ClassifyRefused(pid int, signal error) (bool, error) {
	id, _, err := Identify(pid)
	if err != nil {
		return false, err
	}
	uids, _, err := statusUids(pid)
	if err != nil {
		return false, err
	}
	return currentViewer().blocks(refused{id: id, uids: uids, signal: signal}), nil
}

// Ancestry exposes the parent chain the listing sets aside.
func Ancestry() []Identity { return ancestry(os.Getpid()) }
