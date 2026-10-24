package service

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/home"
)

// The sweep asked of a copy that already stands before a worker starts in it.
// A run's copy is swept at another moment, once the run is final, and
// runsweep.go owns that one.
//
// PRD section 11's "terminal gone, work not landed" preserves the copy,
// proves no live process owns it, and relaunches in it. A dead terminal
// proves the first half of that and not the second: a descendant of the
// previous worker that left its process group can still be writing in the
// copy. And a dispatch retried after a crash finds a copy standing whose
// worker may have started before its session reference was recorded, the
// window launch.go names, which nothing recorded can find. Both therefore
// ask agents.Sweeper of the standing copy before they claim an attempt:
// relaunch.go after its re-probe, dispatch.go when a retry finds the copy
// path already there. A dispatch that is about to cut the copy has nothing
// to sweep, and a first claim sweeps nothing: no attempt has cut a copy, so
// what stands there is a concurrent winner's or nobody's attempt's, and
// dispatch.go refuses the second after its claim (#562). This
// file is how the answer is read, and there are four readings; the one rule
// between them is that a sweep that could not be made is never read as one
// that found nothing.
//
//   - Clear: the sweep listed, everything it found under the copy was ended,
//     nothing it could read was left undecided, and every process whose
//     working directory it could not read fell under one of
//     agents.Sweeper's two carve-outs. The worker is started,
//     and the relaunch's note and the log say what was ended. That includes
//     anything a person had running there, a shell among it, because the
//     sweep's scope is the directory and nothing else, and the words say the
//     copy was reclaimed from whatever was in it.
//   - Unsupported: agents.ErrSweepUnsupported, which is every platform but
//     Linux and macOS. Nothing was looked for and nothing is proven, and
//     both callers refuse (P6, #95 line O, which the owner extended from a
//     relaunch to a retried dispatch): each registers a hold for a person
//     before its claim, keyed by the attempt it read and in a key space of
//     its own, relaunch.go's *relaunchUnswept and dispatch.go's
//     *dispatchUnswept, and goes ahead only on a retry answer to that hold,
//     which the attempt anchor spends on one attempt, with the log, and a
//     relaunch's note, saying nothing was proven; what still runs in the
//     copy then is not looked for or ended, which is the person's decision
//     rather than a proof. taskholds.go owns both holds and their answers.
//   - Not a copy: the copy path does not stand as the copy itself, because it
//     is a symbolic link or vanished, so home.CopyAt refuses it and nothing
//     was swept. The caller goes on to the claim so the isolation assertion
//     records its own refusal, a missing copy or a link into the primary
//     checkout among them, and a copy the assertion would pass is refused
//     after it as not isolated, recorded on the new attempt, rather than
//     given a worker unswept.
//   - Anything else: the listing failed, lsof missing or refused on macOS
//     among it, something under the copy survived the forceful step or
//     could not be decided, or a process whose working directory could not
//     be read falls under neither carve-out, a process of this service's own
//     user among them, and so could be standing in the copy. The caller
//     refuses with *copyUnswept before the claim, nothing is recorded, and
//     the next pass asks again: the next start for a relaunch, the next
//     dispatch pass for a dispatch. When the sweep completed, the refusal's
//     text is its result as agents.SweepResult.String renders it, which
//     names each process that kept the sweep from clearing for its
//     unreadable working directory by identifier, user identifiers and
//     command name, up to agents.BlockersShown of them and a count of the
//     rest. The log lines for a relaunch and a dispatch not made, in
//     reconcile.go and dispatch.go, carry that text; no relaunch note is
//     written for a refused sweep. A host with such a process anywhere off
//     this service's parent chain refuses every relaunch and every retried
//     dispatch while it runs, wherever it stands, which is the strict rule
//     paying for the proof rather than a gap in it.
//
// What Clear does not cover is agents.Sweeper's to state: an unreadable
// process one of its carve-outs sets aside, an ancestor of this service or another
// user's process this service may not signal, is counted and not looked at,
// and a process that left the copy before the listing is not seen.
//
// A sweep is not under reconcile.go's probe bound. It costs a read of the
// process table and, when it signals something, waits of up to reapGrace
// after each of the two signals, and at a start it runs inside recover,
// before anything is served.

// copyUnswept is a worker refused because the sweep of its standing copy did
// not clear it: Cause is the sweep's error, or nil when the sweep completed
// and left something running or undecided, which Result names, the
// processes whose unreadable working directory blocked it among them. The
// command names Result carries are for a person reading the refusal, and
// nothing here decides from them.
type copyUnswept struct {
	Task   string
	Result agents.SweepResult
	Cause  error
}

// Error names what the sweep could not do.
func (e *copyUnswept) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("service: task %s gets no worker because its standing copy could not be swept: %v", e.Task, e.Cause)
	}
	return fmt.Sprintf("service: task %s gets no worker because its standing copy was not cleared: %s", e.Task, e.Result)
}

// Unwrap is the sweep's error.
func (e *copyUnswept) Unwrap() error { return e.Cause }

// copySwept is what the sweep of a standing copy allows: the words the note
// and the log carry, and, when the copy path did not stand as the copy
// itself, why a copy the assertion passes must still be refused.
type copySwept struct {
	words string
	// unsupported is true when the sweep answered agents.ErrSweepUnsupported:
	// nothing was looked for, and words says so. relaunch.go and dispatch.go
	// both refuse on it unless a person answered retry on their own hold.
	unsupported bool
	// unswept is non-nil when nothing was swept because the copy path was
	// not the copy itself; the caller refuses after the assertion on it.
	unswept error
}

// sweepStandingCopy sweeps the task's standing copy and reads the answer on
// this file's terms.
func (s *Service) sweepStandingCopy(ctx context.Context, id, copyPath string) (copySwept, error) {
	sweep := s.sweepCopy
	if sweep == nil {
		sweep = agents.NewSweeper(s.home, reapGrace, nil).Sweep
	}
	r, err := sweep(ctx, copyPath)
	if errors.Is(err, home.ErrNotACopy) {
		if _, statErr := os.Lstat(copyPath); statErr == nil {
			return copySwept{unswept: err}, nil
		}
		return copySwept{unswept: err, words: "its copy is not standing, so nothing was swept"}, nil
	}
	return readSweep(id, r, err)
}

// readSweep is the reading of a sweep's answer other than the not-a-copy
// one, which needs the filesystem and is sweepStandingCopy's.
func readSweep(id string, r agents.SweepResult, err error) (copySwept, error) {
	switch {
	case errors.Is(err, agents.ErrSweepUnsupported):
		return copySwept{unsupported: true, words: "this platform has no process sweep, so nothing still running in the copy was " +
			"looked for or ended and none is proven absent"}, nil
	case err != nil:
		return copySwept{}, &copyUnswept{Task: id, Cause: err}
	case !r.Clear():
		return copySwept{}, &copyUnswept{Task: id, Result: r}
	case len(r.Ended) == 0:
		return copySwept{words: "the copy was swept and no process this service could read was running in it" + unreadable(r)}, nil
	default:
		return copySwept{words: fmt.Sprintf("the copy was reclaimed from whatever was running in it: %d process(es) "+
			"whose working directory was in it were ended, whatever they were%s", len(r.Ended), unreadable(r))}, nil
	}
}

// unreadable is the words for the processes a clear sweep could not read,
// and nothing when there were none. A clear sweep has set every one of them
// aside, so the words say on what terms rather than leaving the count bare.
func unreadable(r agents.SweepResult) string {
	if r.Unreadable == 0 {
		return ""
	}
	return fmt.Sprintf(" (%d process(es) whose working directory this service could not read were not looked at; "+
		"each is an ancestor of this service or another user's process this service may not signal)", r.Unreadable)
}
