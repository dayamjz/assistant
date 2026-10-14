package agents

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dayamjz/assistant/internal/home"
)

// ErrSweepUnsupported is Sweeper.Sweep's answer on a platform where this
// package has no verified way to read another process's working directory.
// It means the sweep could not prove anything, never that it found nothing:
// no process was looked for and none was signalled. Linux is the one
// platform with a listing; every other platform answers this.
var ErrSweepUnsupported = errors.New("agents: this platform has no verified listing of process working directories, so no sweep was made")

// SweepResult is what one sweep of one copy did. The zero value is not a
// clean sweep: Clear is false for it, and a result returned beside an error
// is always the zero value, so a caller that drops the error cannot read an
// unsupported or failed sweep as one that found nothing.
type SweepResult struct {
	// Dir is the copy swept, as home.CopyAt returned it.
	Dir string
	// Ended are processes whose working directory was under Dir, verified
	// present by identity, signalled, and then no longer running: absent,
	// or with their identifier given to another process, after the polite
	// step or the forceful one.
	Ended []Identity
	// Exited are processes the listing found under Dir that were no longer
	// running when their identity was verified before the first signal.
	// Nothing was signalled.
	Exited []Identity
	// Replaced are processes the listing found under Dir whose identifier
	// had been given to another process by the time it was verified.
	// Nothing was signalled: the running process is not the one listed.
	Replaced []Identity
	// Survived are processes still running with their listed identity after
	// the forceful step: ones this process may not signal, or ones not gone
	// when the grace ran out.
	Survived []Identity
	// Unverified are identifiers of processes this sweep could not decide
	// about although it was not refused permission to look: an identity
	// that could not be read, a working directory whose read failed for any
	// reason but permission, an identity that changed between the reads the
	// listing made, or a presence that could not be read before a signal.
	// None of them was signalled.
	Unverified []int
	// Unreadable counts processes whose working directory this process was
	// refused permission to read, whoever owns them. None was signalled, and
	// the sweep claims nothing about where they run.
	Unreadable int

	// listed is true only when the platform's listing completed, which is
	// what makes Clear possible at all.
	listed bool
}

// Clear reports whether the sweep proves that no process whose working
// directory this process may read still has it under Dir: the listing
// completed, nothing survived the forceful step, and nothing was left
// undecided. It is a statement about the processes the sweep was permitted
// to read and no others: Unreadable processes do not make it false, which
// is the sweep's stated limit and not a proof about them, and neither does a
// process that changed its working directory out of Dir before the listing,
// which the listing cannot see.
func (r SweepResult) Clear() bool {
	return r.listed && len(r.Survived) == 0 && len(r.Unverified) == 0
}

// String renders the result for a log line.
func (r SweepResult) String() string {
	if !r.listed {
		return "no sweep was made"
	}
	return fmt.Sprintf("swept %s: %d ended, %d exited before a signal, %d replaced, %d survived, %d unverified, %d not readable",
		r.Dir, len(r.Ended), len(r.Exited), len(r.Replaced), len(r.Survived), len(r.Unverified), r.Unreadable)
}

// Sweeper is the identity-based sweep PRD section 8 requires: it ends the
// processes whose working directory resolves under one of its home's
// isolated copies, and nothing else. It implements Sweep.
//
// What is in scope is decided by two things and nothing else. The directory
// must be a copy home.CopyAt accepts, so a sweep cannot be pointed at a
// person's checkout, at a copy of another home, or at a copy path that is a
// symbolic link to somewhere else. And a process is in scope when the
// working directory the platform reports for it, resolved through symbolic
// links when it can be, is that directory or below it; a working directory
// that cannot be resolved, such as one that was removed, is compared as the
// platform reported it. No command line, name, user or parent is read or
// matched.
//
// That scope is a policy, and it is stated here because it is broader than
// "what an agent left behind": anything running under the copy is ended,
// including a shell or an editor a person opened there. This process is
// never listed, whatever its working directory.
//
// Each process listed is ended by itself, by identifier, and only after
// Identity.Presence has verified the identity listed is still the one
// running: politely first, then forcefully after the grace for whatever is
// still present, re-verified before that signal too. Its process group is
// not signalled, because a member of that group may run outside the copy.
//
// Residual gaps, none of them closed here:
//
//   - Reuse between the presence check and the signal, and reuse within
//     the start time's resolution, which Identity names per platform.
//   - A process that changed its working directory out of the copy before
//     the listing is not found, whatever files it holds open in it.
//   - A process started under the copy after its entry was read is not
//     found; the sweep is a pass over a moment, not a lock on the copy.
//   - A process whose working directory this process may not read is
//     counted Unreadable and never ended, and Clear says nothing about it.
//     The sweep does not ask who owns such a process, so a refusal for a
//     process of this process's own user is counted the same way.
//   - Only Linux lists; elsewhere every sweep is ErrSweepUnsupported.
type Sweeper struct {
	home   *home.Home
	grace  time.Duration
	report func(dir string, r SweepResult, err error)
}

// NewSweeper returns a sweep over h's copies, giving a polite signal grace
// to be obeyed before the forceful one. report receives what each
// SweepUnder did, which is the only way that method has to say it; Sweep
// returns its answer and does not call report. A nil report discards what
// SweepUnder did.
func NewSweeper(h *home.Home, grace time.Duration, report func(dir string, r SweepResult, err error)) *Sweeper {
	return &Sweeper{home: h, grace: grace, report: report}
}

// Sweep ends the processes running under dir, on Sweeper's terms, and
// reports what it did. An error is returned beside the zero SweepResult and
// means nothing was signalled: a dir home.CopyAt refuses, wrapping
// home.ErrNotACopy; ErrSweepUnsupported on a platform with no listing; and
// any failure of the listing itself, including ctx ending during it. A ctx
// that ends while signalled processes are being waited on cuts the waits
// short, and what is still running then is reported Survived.
func (s *Sweeper) Sweep(ctx context.Context, dir string) (SweepResult, error) {
	if s == nil || s.home == nil {
		return SweepResult{}, fmt.Errorf("agents: a sweep needs the home whose copies it may sweep")
	}
	copyDir, err := s.home.CopyAt(dir)
	if err != nil {
		return SweepResult{}, err
	}
	found, err := listUnder(ctx, copyDir)
	if err != nil {
		return SweepResult{}, fmt.Errorf("agents: listing processes under %s: %w", copyDir, err)
	}
	r := endListed(ctx, found.under, s.grace)
	r.Dir = copyDir
	r.Unverified = append(found.unverified, r.Unverified...)
	r.Unreadable = found.unreadable
	r.listed = true
	return r, nil
}

// SweepUnder is Sweep for the Sweep seam, which takes no answer: what the
// sweep did goes to the report NewSweeper was given.
func (s *Sweeper) SweepUnder(ctx context.Context, dir string) {
	r, err := s.Sweep(ctx, dir)
	if s != nil && s.report != nil {
		s.report(dir, r, err)
	}
}

// listing is what a platform's listUnder found: the identities of the
// processes under the directory, the identifiers it could not decide about,
// and how many processes' working directories it was refused.
type listing struct {
	under      []Identity
	unverified []int
	unreadable int
}

// endListed ends the listed processes on Sweeper's terms and returns what it
// did, leaving Dir, Unreadable and listed for Sweep to fill.
func endListed(ctx context.Context, ids []Identity, grace time.Duration) SweepResult {
	var r SweepResult
	var signalled []Identity
	for _, id := range ids {
		switch presence, err := id.Presence(); {
		case err != nil:
			r.Unverified = append(r.Unverified, id.PID)
		case presence == PresenceAbsent:
			r.Exited = append(r.Exited, id)
		case presence == PresenceReplaced:
			r.Replaced = append(r.Replaced, id)
		default:
			// A failed signal is not reported here: the presence read
			// after the wait is what decides whether the process went.
			_ = signalProcess(id.PID, false)
			signalled = append(signalled, id)
		}
	}
	if len(signalled) == 0 {
		return r
	}
	remaining := waitGone(ctx, signalled, grace)
	var forced []Identity
	for _, id := range remaining {
		if presence, err := id.Presence(); err == nil && presence == PresencePresent {
			_ = signalProcess(id.PID, true)
			forced = append(forced, id)
		}
	}
	if len(forced) > 0 {
		waitGone(ctx, forced, grace)
	}
	for _, id := range signalled {
		switch presence, err := id.Presence(); {
		case err != nil:
			r.Unverified = append(r.Unverified, id.PID)
		case presence == PresencePresent:
			r.Survived = append(r.Survived, id)
		default:
			r.Ended = append(r.Ended, id)
		}
	}
	return r
}

// waitGone polls until none of ids reads present, grace passes or ctx ends,
// and returns those still present, or whose presence could not be read, when
// it stopped.
func waitGone(ctx context.Context, ids []Identity, grace time.Duration) []Identity {
	deadline := time.Now().Add(grace)
	for {
		var left []Identity
		for _, id := range ids {
			if presence, err := id.Presence(); err != nil || presence == PresencePresent {
				left = append(left, id)
			}
		}
		if len(left) == 0 || !time.Now().Before(deadline) || ctx.Err() != nil {
			return left
		}
		ids = left
		time.Sleep(pollInterval)
	}
}
