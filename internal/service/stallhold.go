package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// The stall hold: PRD section 6's "a stalled worker holds for you".
//
// # What holds
//
// A stall is the count supervise.go already keeps: consecutive surfaces of
// one unchanged screen. supervisor.Classify raises the surface that reaches
// stall_inspection_count plus stall_hold_count to
// supervisor.SurfaceInspectAndHold, and only for a task that resolves
// Running whose terminal was captured live this pass, so a declared wait, a
// gate run being advanced, a task already held and a terminal read gone or
// Unobserved never hold. That surface registers the hold in its own wake's
// transaction, through store.WakeRequest.Registers, so it is fenced by the
// incarnation like every write of the loop, and the hold-opened wake about
// it is detected in the same pass. Its key is supervisor.StallHold's: the
// attempt, and the surface that reached the count, so a replay finds the
// same hold. Nothing is ended and the copy is not touched: the hold waits on
// a person, it is not a park, and nothing here is a loop bound.
//
// While the hold is open the task resolves Held from internal/taskstate's
// hold tier and Running no longer, so no second hold is registered for the
// same stall. It is in flight whatever the queue holds, by supervisor.
// StallHeld, which the loop's watched set, the stop guard through feed, and
// the catch-up read all count, so its terminal is read at every pass and its
// unchanged screen goes on surfacing once per escalation_window.
//
// # The program closes it when the stall ends by itself
//
// Before the decision, a pass over a task with an open stall hold closes it
// when its question no longer stands, recording the program as the resolver,
// supervisor's closure word as the resolution and what it read as the note,
// fenced by the loop's incarnation like every other write the loop makes:
// the probe of the hold's attempt read the worker dead, or the screen read
// changed since the surface that reached the count, by its digest or its
// change epoch, so a change the pass saw while it could not close is still
// read as one at the next. A changed screen restarts the count, which the
// markers already do, and the closure surfaces once as
// supervisor.KindStallClosed, keyed by the hold and detected from the hold
// record, so a failed write is detected again. A dead worker raises no wake
// of its own: it surfaces as terminal gone, as any does. A terminal read
// Unobserved, or a live one whose screen did not change, closes nothing.
// task.stop closes the task's open stall holds the same way once its probe
// reads the worker dead, since that is a probe the program takes reading the
// worker dead.
//
// The closure and an answer are each one write to the hold, and the first
// to land stands: the pass closes only while it holds the task's slot,
// claimSteer's, and an answer that ends a worker reads the hold again under
// the same slot before it ends anything, so an answer is wholly before a
// closure or wholly after it, and one after it is refused before it ends
// anything. A pass that cannot take the slot closes nothing that pass.
//
// # The answers
//
// The answer set is closed, and any other word is refused with the hold left
// open. approve records the answer and leaves the worker running; the loop
// reads it off the record, approvedStallSurface, and starts the count of the
// screen again from zero through the stall base marker, so another hold comes
// only after another full count. stop is task.stop's stop on its terms, the
// copy untouched and the end proven by the probe reading dead, and registers
// the stop's own hold, which carries whether the work is begun again; a stop
// that cannot be proven is refused and the stall hold stays open. retry is
// that stop and then the answer retry to the stop's hold, in one answer:
// answerStopRetry's anchored relaunch, counted by relaunch_attempts, which
// past the bound registers the bound's hold and starts nothing.
//
// # Residual gaps
//
//   - The closure rests on the program's resolver, store.ResolvedByProgram,
//     which PRD section 8's hold row gives the service for the checks
//     stage's hold alone; section 6's stalled worker gives it this one too,
//     and section 8 was not amended to say so.
//   - A pass reads the screen of the latest attempt only. A stall hold whose
//     attempt is no longer the latest is closed only when a probe of that
//     attempt's own session reads it dead, which the pass takes for it.
//   - Detection is at a pass, so a closure lands up to one interval after
//     the screen changed or the worker died, and a pass that cannot take the
//     task's slot defers it one interval more.
//   - A housekeeping pass's retirement is not built, so its probe closes
//     nothing here.

// approvedStallSurface is the surface number of the latest stall hold of the
// task's current attempt and current screen that was answered approve, or
// zero when there is none: the count's restart the loop has not yet taken
// when it is above the stall base marker. The screen is the markers' change
// epoch, so an approve given about a screen that has since changed restarts
// nothing, the change having restarted the count already.
func approvedStallSurface(o observation, attempt int64) int64 {
	var surface int64
	for _, h := range o.observed.Snapshot.Holds {
		sh, ok := supervisor.ParseStallHold(h.Key)
		if !ok || sh.Task != o.observed.Snapshot.Task.ID || sh.Attempt != attempt || sh.Epoch != o.markers.ChangeEpoch {
			continue
		}
		if h.Resolution.Or("") == machine.AnswerApprove && sh.Surface > surface {
			surface = sh.Surface
		}
	}
	return surface
}

// latestAttemptOf is the task's latest attempt's sequence, and whether that
// attempt recorded a session, which a stall hold is keyed by and a capture
// is of.
func latestAttemptOf(o observation) (int64, bool) {
	attempts := o.observed.Snapshot.Attempts
	if len(attempts) == 0 {
		return 0, false
	}
	latest := attempts[len(attempts)-1]
	return latest.Sequence, latest.SessionRef.IsKnown()
}

// stallHold is the hold a surface at supervisor.SurfaceInspectAndHold
// registers, worded for a person on machine.HoldPhrase's terms like every
// other hold, and naming the three answers.
func stallHold(o observation, sh supervisor.StallHold, stall supervisor.Stall) store.Hold {
	return store.Hold{
		Key:     sh.Key(),
		TaskID:  store.Known(sh.Task),
		Subject: "whether to end a running worker whose screen has not changed through repeated inspections",
		Detail: fmt.Sprintf("task %s: attempt %d's worker is still running, and its screen has now surfaced unchanged %d times in a row, "+
			"the last %d of them asking the coordinator for a deeper inspection. Nothing has been ended and its copy is untouched. "+
			"Answer %q to let it keep running and start the count again, %q to end it and keep its copy, after which its stop asks "+
			"whether to begin the work again, or %q to end it and begin the work again once in the same copy.",
			sh.Task, sh.Attempt, stall.Repetitions+1, stall.Repetitions+1-max(stall.InspectAt, 0),
			machine.AnswerApprove, machine.AnswerStop, machine.AnswerRetry),
	}
}

// stallClosedMarker composes a stall closure's wake marker on
// messageUnacknowledgedMarker's terms: the hold's key, so each closure
// surfaces once.
func stallClosedMarker(key string) string { return "stall-closed:" + key }

// stallClosuresOf is every stall hold of the task the program closed on a
// changed screen, as the snapshot holds them and as this pass closed them,
// each once: the closures supervisor.KindStallClosed surfaces. The
// resolution alone says it is one, because the answer set refuses that word
// to everyone but the program.
func stallClosuresOf(o observation) []store.Hold {
	var out []store.Hold
	seen := make(map[string]bool)
	for _, h := range append(o.observed.Snapshot.Holds, o.closed...) {
		sh, ok := supervisor.ParseStallHold(h.Key)
		if !ok || sh.Task != o.observed.Snapshot.Task.ID || seen[h.Key] ||
			h.Resolution.Or("") != supervisor.StallClosedScreenChanged {
			continue
		}
		seen[h.Key] = true
		out = append(out, h)
	}
	return out
}

// stallEnded is why a stall hold's question no longer stands, on the file
// comment's terms, and what was read: the word and the note to close it
// with, or false when it still stands.
func (s *Service) stallEnded(c *incarnation, o observation, sh supervisor.StallHold, probes map[string]session.Observation) (string, string, bool) {
	ref := ""
	for _, a := range o.observed.Snapshot.Attempts {
		if a.Sequence == sh.Attempt {
			ref = a.SessionRef.Or("")
		}
	}
	if ref == "" {
		return "", "", false
	}
	probed, ok := probes[ref]
	if !ok {
		ctx, cancel := s.sup.bound(c.ctx)
		probed = s.sup.terminals.Probe(ctx, ref)
		cancel()
	}
	if probed.Liveness == taskstate.Dead {
		return supervisor.StallClosedWorkerDead, fmt.Sprintf("a supervision pass's probe read attempt %d's worker (session %s) dead: %s %s",
			sh.Attempt, ref, probed.Reason, probed.Detail), true
	}
	latest, _ := latestAttemptOf(o)
	screen, captured := o.screen.Get()
	if sh.Attempt != latest || !captured || !o.has {
		return "", "", false
	}
	if before := o.markers.CaptureDigest.Or(""); before != screen || o.markers.ChangeEpoch != sh.Epoch {
		return supervisor.StallClosedScreenChanged, fmt.Sprintf("a supervision pass read attempt %d's screen changed: digest %s at change %d, "+
			"where the hold was registered at change %d", sh.Attempt, screen, o.markers.ChangeEpoch, sh.Epoch), true
	}
	return "", "", false
}

// closeEndedStalls is the pass's closure of the task's open stall holds
// whose question no longer stands, under the task's slot, on the file
// comment's terms. It returns the holds it closed as the record holds them.
// A pass that cannot take the slot, or whose write fails, closes nothing and
// logs why; the next pass reads the same facts again.
func (s *Service) closeEndedStalls(c *incarnation, o observation, stalls []supervisor.StallHold, probes map[string]session.Observation) []store.Hold {
	type ending struct {
		hold       supervisor.StallHold
		word, note string
	}
	var due []ending
	for _, sh := range stalls {
		if word, note, ok := s.stallEnded(c, o, sh, probes); ok {
			due = append(due, ending{sh, word, note})
		}
	}
	if len(due) == 0 {
		return nil
	}
	release, err := s.claimSteer(o.observed.Snapshot.Task.ID)
	if err != nil {
		s.log.Printf("the stall hold of task %s is not closed this pass: %v", o.observed.Snapshot.Task.ID, err)
		return nil
	}
	defer release()
	var closed []store.Hold
	for _, e := range due {
		ctx, cancel := s.sup.bound(c.ctx)
		h, ok := s.closeStallHold(ctx, &c.inc, e.hold, e.word, e.note)
		cancel()
		if ok {
			closed = append(closed, h)
		}
	}
	return closed
}

// closeStallHold closes one stall hold as the program, with word as the
// resolution and note as what it read, publishes it, and reports it as the
// record holds it. A closure the loop makes passes its incarnation, which
// fences the write as every write of the loop is fenced; task.stop's passes
// nil. A hold already answered is left as it stands, the first write having
// landed, and reported false; any other failure, a replaced incarnation's
// among them, is logged and reported false.
func (s *Service) closeStallHold(ctx context.Context, inc *store.Incarnation, sh supervisor.StallHold, word, note string) (store.Hold, bool) {
	var h store.Hold
	var err error
	if inc != nil {
		h, err = s.store.ResolveHoldUnder(ctx, *inc, sh.Key(), word, note, store.ResolvedByProgram())
	} else {
		h, err = s.store.ResolveHoldNoted(ctx, sh.Key(), word, note, store.ResolvedByProgram())
	}
	switch {
	case errors.Is(err, store.ErrHoldResolved):
		return store.Hold{}, false
	case err != nil:
		s.log.Printf("the stall hold %s could not be closed on %s: %v", sh.Key(), word, err)
		return store.Hold{}, false
	}
	s.publishHoldState(h)
	s.log.Printf("the program closed the stall hold %s: %s (%s)", sh.Key(), word, note)
	return h, true
}

// closeStallsOnDead closes every open stall hold of the task but the one
// named answering as the program, on a probe that read the worker of attempt
// dead after a stop: task.stop's half of the file comment's closure. The
// caller holds the task's slot.
func (s *Service) closeStallsOnDead(ctx context.Context, task string, attempt int64, ref, answering string, probed session.Observation) {
	open, err := s.store.OpenHolds(ctx)
	if err != nil {
		s.log.Printf("the stall holds of task %s could not be read after its stop, and are closed at the next pass: %v", task, err)
		return
	}
	for _, h := range open {
		sh, ok := supervisor.ParseStallHold(h.Key)
		if !ok || sh.Task != task || sh.Attempt != attempt || h.Key == answering {
			continue
		}
		s.closeStallHold(ctx, nil, sh, supervisor.StallClosedWorkerDead,
			fmt.Sprintf("a stop's probe read attempt %d's worker (session %s) dead: %s %s", attempt, ref, probed.Reason, probed.Detail))
	}
}

// answerStall answers a stall hold, on the file comment's terms. The caller
// has refused a subordinate and a worker and read the hold open; p is the
// caller's placement. An answer outside the set is ipc.ErrInvalidRequest and
// records nothing. A stop or retry that cannot take the task's slot, finds
// the hold answered or closed once it has, finds the stalled attempt is no
// longer the latest, or cannot prove the worker ended is ipc.ErrRefused,
// with the hold left as it was and nothing relaunched.
func (s *Service) answerStall(ctx context.Context, req ipc.Request, p placedPeer, r machine.TaskRespondRequest,
	hold store.Hold, sh supervisor.StallHold) (machine.TaskAnswered, error) {
	switch r.Answer {
	case machine.AnswerApprove:
		resolved, err := s.store.ResolveHoldNoted(ctx, hold.Key, r.Answer, r.Note, req.HoldResolver())
		if errors.Is(err, store.ErrHoldResolved) {
			return machine.TaskAnswered{}, fmt.Errorf("%w: %w", ipc.ErrRefused, err)
		}
		if err != nil {
			return machine.TaskAnswered{}, err
		}
		s.publishHoldState(resolved)
		s.log.Printf("task %s keeps running: its stall hold %s was answered %q", r.Task, hold.Key, r.Answer)
		return machine.TaskAnswered{Hold: resolved, Effect: machine.AnswerRecorded}, nil
	case machine.AnswerStop, machine.AnswerRetry:
	default:
		return machine.TaskAnswered{}, fmt.Errorf("%w: task %s holds on %s, which is answered %q, %q or %q, not %q",
			ipc.ErrInvalidRequest, r.Task, hold.Key, machine.AnswerApprove, machine.AnswerStop, machine.AnswerRetry, r.Answer)
	}
	if s.terminals == nil {
		return machine.TaskAnswered{}, fmt.Errorf("%w: %w", ipc.ErrUnavailable, errRelaunchUnprobed)
	}
	out, stopHold, err := s.stopStalled(ctx, req, p, r, hold, sh)
	if err != nil || r.Answer != machine.AnswerRetry || stopHold == nil {
		return out, err
	}
	// The retry to the stop's hold, in the same answer: the decision is
	// recorded on that hold first, as every retry is, and the relaunch is
	// answerStopRetry's, the one a retry on a stop's hold makes.
	retried, err := s.store.ResolveHoldNoted(ctx, stopHold.Key, machine.AnswerRetry, r.Note, req.HoldResolver())
	if err != nil {
		return machine.TaskAnswered{}, fmt.Errorf("service: the worker of task %s was stopped on the answer %q to %s, and the retry could not be recorded on its stop's hold %s: %w",
			r.Task, r.Answer, hold.Key, stopHold.Key, err)
	}
	s.publishHoldState(retried)
	out.Stopped = &retried
	s.answerStopRetry(ctx, r, retried, &out)
	return out, nil
}

// stopStalled is a stop or retry's stop, under the task's slot: it reads the
// stall hold again, stops the worker on stopTask's terms, which registers the
// stop's hold, and records the answer on the stall hold. It returns the
// answer so far and the stop's hold, nil for a task in the terminal tier,
// whose stop registers none.
func (s *Service) stopStalled(ctx context.Context, req ipc.Request, p placedPeer, r machine.TaskRespondRequest,
	hold store.Hold, sh supervisor.StallHold) (machine.TaskAnswered, *store.Hold, error) {
	release, err := s.claimSteer(r.Task)
	if err != nil {
		return machine.TaskAnswered{}, nil, fmt.Errorf("%w: %w", ipc.ErrRefused, err)
	}
	defer release()
	current, err := s.store.Hold(ctx, hold.Key)
	if err != nil {
		return machine.TaskAnswered{}, nil, err
	}
	if !current.Open() {
		return machine.TaskAnswered{}, nil, fmt.Errorf("%w: %w: hold %s was closed with %q before this answer, and nothing was ended",
			ipc.ErrRefused, store.ErrHoldResolved, hold.Key, current.Resolution.Or(""))
	}
	latest, ok, err := s.latestAttempt(ctx, r.Task)
	if err != nil {
		return machine.TaskAnswered{}, nil, err
	}
	if !ok || latest.Sequence != sh.Attempt {
		return machine.TaskAnswered{}, nil, fmt.Errorf("%w: hold %s is about attempt %d, which is no longer the task's latest, and nothing was ended",
			ipc.ErrRefused, hold.Key, sh.Attempt)
	}
	stopped, err := s.stopClaimed(ctx, req, p, r.Task, latest, hold.Key)
	if err != nil {
		var unverified *stopUnverified
		var refused *stopRefused
		if errors.As(err, &unverified) || errors.As(err, &refused) || errors.Is(err, errNoWorker) {
			return machine.TaskAnswered{}, nil, fmt.Errorf("%w: %w", ipc.ErrRefused, err)
		}
		return machine.TaskAnswered{}, nil, err
	}
	out := machine.TaskAnswered{Effect: machine.AnswerWorkerStopped}
	resolved, err := s.store.ResolveHoldNoted(ctx, hold.Key, r.Answer, r.Note, req.HoldResolver())
	switch {
	case errors.Is(err, store.ErrHoldResolved):
		// PRD section 6's one exception: the hold was closed between the
		// read above and here, which the slot leaves no writer for in this
		// service, and the answer still reports the end it made.
		resolved, err = s.store.Hold(ctx, hold.Key)
		if err != nil {
			return machine.TaskAnswered{}, nil, err
		}
		s.log.Printf("task %s's stall hold %s was closed as %q before the answer %q could be recorded; the stop it made stands",
			r.Task, hold.Key, resolved.Resolution.Or(""), r.Answer)
	case err != nil:
		return machine.TaskAnswered{}, nil, fmt.Errorf("service: the worker of task %s was stopped on the answer %q and it could not be recorded on %s: %w",
			r.Task, r.Answer, hold.Key, err)
	default:
		s.publishHoldState(resolved)
	}
	out.Hold = resolved
	s.log.Printf("task %s's stalled worker (attempt %d, session %s) was stopped on the answer %q to %s: %s",
		r.Task, stopped.Attempt, stopped.Session, r.Answer, hold.Key, stopped.Ended)
	if stopped.Finished != "" {
		out.Effect = machine.AnswerStopped
		return out, nil, nil
	}
	out.Stopped = &stopped.Hold
	return out, &stopped.Hold, nil
}

// stallHoldOf parses key as a stall hold of task.
func stallHoldOf(key, task string) (supervisor.StallHold, bool) {
	sh, ok := supervisor.ParseStallHold(key)
	return sh, ok && sh.Task == task
}
