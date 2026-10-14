package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
)

// A task's holds: the one the relaunch bound raises, the one a relaunch
// raises where the platform has no process sweep, and the answer to any
// hold a task raised with no run behind it.
//
// PRD section 10's relaunch_attempts row says that past the bound "the task
// holds for you, and you decide whether it is retried: nothing retries a
// task automatically without bound", and section 9 has a hold a task raised
// answered the way a run's is, given the task. This file is both halves.
//
// # The bound's hold
//
// When decideReconciliation finds a task past the bound, the pass registers
// a hold naming the task, keyed by the attempt that reached the bound,
// relaunchBoundKey. Registration is internal/store's RegisterHold, so the
// key is the identity: the same attempt found past the bound at every later
// start is the same hold, open or answered, and a later attempt that fails
// past the bound is a new key and so a new hold. Nothing here writes a wake:
// the supervisor loop surfaces an open hold naming a task as a hold-opened
// wake, a decision-class row, on its own terms, which is the one owner of
// the queue (reconcile.go says why this pass writes no row).
//
// # The answer is the record, and the record is the allowance
//
// An answer to the bound's hold is a resolution of that hold, recorded with
// the resolver internal/ipc's Request.HoldResolver derives from the surface.
// "retry" is a person's decision to relaunch once more, and it is read back
// by decideReconciliation, through boundAnswerOf, from the hold keyed by the
// latest attempt: a relaunch past the bound proceeds only when that hold is
// resolved "retry". The relaunch itself is relaunch.go's, the one anchored
// relaunch, unchanged: it begins the next attempt with
// store.BeginTaskAttempt anchored to the latest attempt it read. That anchor
// is what makes the allowance good for one attempt and no more (P6). Once
// the attempt begins, the latest attempt is the new one, whose key names a
// hold that does not exist, so a second relauncher that read the same
// allowance loses at the anchor with ErrTaskMoved and a later failure past
// the bound holds again. The bypass is of the bound only: every other
// refusal relaunchTask makes - the isolation-refused surface, a live or
// unobserved terminal, a branch a live task holds, the copy's sweep and its
// assertion - applies to a retry exactly as to any relaunch.
//
// "stop" is the decision not to: decideReconciliation reads it and does
// nothing, so the task is never relaunched or held again for that attempt,
// and stays where its records put it. Nothing reads who resolved to decide
// anything: the resolution's text is the decision, and internal/store's
// Resolver is recorded and not consulted, as that package requires.
//
// The resolution is written before the relaunch is asked, so a relaunch the
// program makes past the bound always has a recorded decision behind it. A
// relaunch refused at that moment leaves the decision standing, and the
// next reconciliation, which reads the same hold, relaunches on it; the
// answer says so rather than reporting a relaunch that did not happen.
//
// # The no-sweep hold
//
// Where agents.Sweeper answers agents.ErrSweepUnsupported nothing proves the
// copy unowned, and P6 refuses rather than relaunching on the dead reading
// alone (#95 line O). relaunch.go registers a hold naming the task, keyed by
// the attempt whose terminal it found gone, relaunchUnsweptKey, and refuses
// with *relaunchUnswept. It is the bound's mechanism with a key space of its
// own: the key is the identity, so the same attempt refused at every later
// start, or by the coordinator's task.relaunch, is the same hold, and the
// two holds of one attempt are two records, neither answering the other. It
// is answered the same way, retry or stop, and read back by
// relaunchAnswersOf. A retry is the one thing that lets relaunch.go past the
// refusal, and only for the attempt it names, so the anchor spends it on one
// attempt exactly as it spends the bound's: the attempt the retry begins is
// the latest, a hold keyed by it does not exist, and its own terminal gone
// is refused and held again. A stop is read by decideReconciliation, which
// leaves the task alone for that attempt. Every other refusal is unchanged,
// and the hold is asked only after them: a branch held, a live or
// unobserved terminal, and the re-probe refuse before the sweep is asked,
// and the assertion of the copy still runs after the claim. A retry on the
// bound's hold that reaches this refusal begins nothing and reports
// machine.AnswerRetryHeld, naming the hold the person answers next.
//
// # Any other hold a task raised
//
// A hold naming a task and no run, an investigation's report's among them,
// PRD section 11's "Recovering an investigation", is closed by the same
// answer: the resolution is recorded as given, on the same terms, and acts
// on nothing further here. Which hold an answer closes is the oldest one
// open naming the task and no run, the order internal/store lists open
// holds in, and the answer names it. A hold naming a run belongs to that
// run and is answered through the run.
//
// # Residual gaps
//
//   - The record understates who decided. Every answer arriving over the
//     local protocol is recorded as store.ResolvedByMachineInterface, the
//     only value Request.HoldResolver produces, because nothing on that
//     socket witnesses a person; store.ResolvedByPerson has no producer here
//     on purpose. A retry the coordinator gives under PRD section 9's
//     authority and one a person gives through the command are recorded
//     alike.
//   - While any of these holds is open the task resolves Held from
//     internal/taskstate's hold tier, PRD section 6's second tier, above
//     whatever its run, terminal or history says; once it is answered the
//     hold is no longer read and the task resolves from those again, so a
//     stopped task reads Failed or Unknown as it did before it held, and a
//     retried one reads what its new attempt gives it. That tier reads any
//     open hold naming the task, while an answer here closes only one naming
//     the task and no run; nothing in this build registers one naming both.
//   - The bound's hold is registered by reconciliation, which runs at service
//     start, so a task past the bound holds from the next start after its
//     terminal is found gone. The coordinator's task.relaunch goes through
//     the same relaunchTask, so before then it is refused past the bound as
//     not due, and after a retry answer it relaunches on that answer exactly
//     as the answer's own relaunch would; the anchor keeps the two to one
//     attempt between them.
//   - A retry on the no-sweep hold lets a worker start in a copy nothing
//     looked in: whatever the earlier attempt left running there is neither
//     found nor ended, and the relaunch's note and log say so. That is the
//     decision the hold asks a person for, not a proof.
//   - A retry on the bound's hold given after the no-sweep hold for the
//     same attempt was answered stop relaunches nothing, because the stop
//     stands, and reports machine.AnswerRetryDeferred; only a
//     relaunch_attempts lowered across a restart reaches it.
//   - An answer to a hold that is neither relaunch hold has no closed option set,
//     because nothing records one for it; what was answered is recorded and
//     nothing here checks it against what the hold offered.

// errNoTaskHold reports an answer given a task with no open hold naming it
// and no run. Nothing is recorded.
var errNoTaskHold = errors.New("service: the task has no open hold to answer")

// relaunchBoundPrefix is the key space of the bound's holds.
const relaunchBoundPrefix = "/relaunch-bound/"

// relaunchBoundKey is the key of the hold the relaunch bound raises for a
// task's attempt.
func relaunchBoundKey(task string, attempt int64) string {
	return "task/" + task + relaunchBoundPrefix + strconv.FormatInt(attempt, 10)
}

// isRelaunchBound reports whether key is a bound hold of the task.
func isRelaunchBound(key, task string) bool {
	return strings.HasPrefix(key, "task/"+task+relaunchBoundPrefix)
}

// relaunchUnsweptPrefix is the key space of the holds a relaunch with no
// sweep raises.
const relaunchUnsweptPrefix = "/relaunch-unswept/"

// relaunchUnsweptKey is the key of the hold a relaunch raises when the sweep
// of the task's copy answered agents.ErrSweepUnsupported, keyed by the
// attempt whose terminal was found gone. It is a key space of its own beside
// relaunchBoundKey's, so the two holds of one attempt are two records and
// answering one never answers the other.
func relaunchUnsweptKey(task string, attempt int64) string {
	return "task/" + task + relaunchUnsweptPrefix + strconv.FormatInt(attempt, 10)
}

// isRelaunchUnswept reports whether key is a no-sweep hold of the task.
func isRelaunchUnswept(key, task string) bool {
	return strings.HasPrefix(key, "task/"+task+relaunchUnsweptPrefix)
}

// isRelaunchHold reports whether key is one of the two holds of the task
// whose answer is retry or stop and whose retry asks relaunchTask.
func isRelaunchHold(key, task string) bool {
	return isRelaunchBound(key, task) || isRelaunchUnswept(key, task)
}

// boundAnswer is what a relaunch hold for a task's latest attempt says: the
// bound's, read by boundAnswerOf, or the no-sweep one, read by
// unsweptAnswerOf.
type boundAnswer int

const (
	// boundUnanswered is no hold, or one still open.
	boundUnanswered boundAnswer = iota
	// boundRetry is a hold resolved "retry".
	boundRetry
	// boundStop is a hold resolved any other way.
	boundStop
)

// boundAnswerOf reads the bound's hold for the latest of attempts. A task
// with no attempt has none.
func (s *Service) boundAnswerOf(ctx context.Context, id string, attempts []store.TaskAttempt) (boundAnswer, error) {
	if len(attempts) == 0 {
		return boundUnanswered, nil
	}
	return s.relaunchAnswerOf(ctx, relaunchBoundKey(id, attempts[len(attempts)-1].Sequence))
}

// relaunchAnswers is what the two relaunch holds for a task's latest attempt
// say, the input decideReconciliation and relaunch.go read them through.
type relaunchAnswers struct {
	// bound is the answer to the relaunch bound's hold.
	bound boundAnswer
	// unswept is the answer to the hold a relaunch with no sweep raised.
	unswept boundAnswer
}

// relaunchAnswersOf reads both relaunch holds for the latest of attempts. A
// task with no attempt has neither.
func (s *Service) relaunchAnswersOf(ctx context.Context, id string, attempts []store.TaskAttempt) (relaunchAnswers, error) {
	if len(attempts) == 0 {
		return relaunchAnswers{}, nil
	}
	bound, err := s.boundAnswerOf(ctx, id, attempts)
	if err != nil {
		return relaunchAnswers{}, err
	}
	unswept, err := s.relaunchAnswerOf(ctx, relaunchUnsweptKey(id, attempts[len(attempts)-1].Sequence))
	if err != nil {
		return relaunchAnswers{}, err
	}
	return relaunchAnswers{bound: bound, unswept: unswept}, nil
}

// relaunchAnswerOf reads the relaunch hold keyed key: retry only when it is
// resolved exactly machine.AnswerRetry.
func (s *Service) relaunchAnswerOf(ctx context.Context, key string) (boundAnswer, error) {
	h, err := s.store.Hold(ctx, key)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return boundUnanswered, nil
	case err != nil:
		return boundUnanswered, err
	case h.Open():
		return boundUnanswered, nil
	case h.Resolution.Or("") == machine.AnswerRetry:
		return boundRetry, nil
	default:
		return boundStop, nil
	}
}

// holdUnswept registers the no-sweep hold for the attempt whose terminal
// the relaunch found gone and publishes it as it stands. Registering a key
// already there returns that hold unchanged, open or answered, which is
// what keeps a refusal asked again, at this start or a later one, from
// raising a second hold for the same attempt.
func (s *Service) holdUnswept(ctx context.Context, id string, attempt int64, ref, words string) (store.Hold, error) {
	h, err := s.store.RegisterHold(ctx, store.Hold{
		Key:     relaunchUnsweptKey(id, attempt),
		TaskID:  store.Known(id),
		Subject: "whether to begin this work again when nothing could check that the earlier attempt left no process behind",
		Detail: fmt.Sprintf("task %s: attempt %d's terminal (session %s) is gone with the work not landed, and %s. "+
			"Answer %q to relaunch it once in the same copy anyway, or %q to leave it where it stopped.",
			id, attempt, ref, words, machine.AnswerRetry, machine.AnswerStop),
	})
	if err != nil {
		return store.Hold{}, err
	}
	s.publishHoldState(h)
	return h, nil
}

// holdAtBound registers the bound's hold for the latest of attempts and
// publishes it as it stands. Registering a key already there returns that
// hold unchanged, which is what keeps a restart from raising a second one.
func (s *Service) holdAtBound(ctx context.Context, id string, attempts []store.TaskAttempt, d reconcileDecision) (store.Hold, error) {
	if len(attempts) == 0 {
		return store.Hold{}, fmt.Errorf("service: task %s has no attempt to hold at its relaunch bound", id)
	}
	latest := attempts[len(attempts)-1]
	h, err := s.store.RegisterHold(ctx, store.Hold{
		Key:     relaunchBoundKey(id, latest.Sequence),
		TaskID:  store.Known(id),
		Subject: "whether to begin this work again after it ended as many times as the program begins it on its own",
		Detail: fmt.Sprintf("task %s: attempt %d's terminal is gone with the work not landed, and %s. "+
			"Answer %q to relaunch it once more in the same copy, or %q to leave it where it stopped.",
			id, latest.Sequence, d.reason, machine.AnswerRetry, machine.AnswerStop),
	})
	if err != nil {
		return store.Hold{}, err
	}
	s.publishHoldState(h)
	return h, nil
}

// respondTask answers the oldest open hold naming the task and no run, on
// the terms this file's opening comment states. A task's worker is refused
// with ipc.ErrWorker, from the caller's credentials. A request naming no
// task or giving no answer, and an answer the bound's hold does not offer,
// are ipc.ErrInvalidRequest; a task with no such hold is errNoTaskHold and a
// hold another caller answered first is store.ErrHoldResolved, each wrapped
// in ipc.ErrRefused. None of those records anything. A retry's relaunch
// refused now is not an error: the decision is recorded, and the answer
// reports AnswerRetryDeferred with the reason.
func (s *Service) respondTask(ctx context.Context, req ipc.Request, r machine.TaskRespondRequest) (machine.TaskAnswered, error) {
	if err := s.refuseWorker(req); err != nil {
		return machine.TaskAnswered{}, err
	}
	switch {
	case strings.TrimSpace(r.Task) == "":
		return machine.TaskAnswered{}, fmt.Errorf("%w: an answer names the task whose hold it closes", ipc.ErrInvalidRequest)
	case strings.TrimSpace(r.Answer) == "":
		return machine.TaskAnswered{}, fmt.Errorf("%w: an answer to task %s's hold is empty", ipc.ErrInvalidRequest, r.Task)
	}
	if _, err := s.store.Task(ctx, r.Task); err != nil {
		return machine.TaskAnswered{}, err
	}
	hold, err := s.oldestTaskHold(ctx, r.Task)
	if err != nil {
		return machine.TaskAnswered{}, fmt.Errorf("%w: %w", ipc.ErrRefused, err)
	}
	relaunchHold := isRelaunchHold(hold.Key, r.Task)
	if relaunchHold && r.Answer != machine.AnswerRetry && r.Answer != machine.AnswerStop {
		return machine.TaskAnswered{}, fmt.Errorf("%w: task %s holds on %s, which is answered %q or %q, not %q",
			ipc.ErrInvalidRequest, r.Task, hold.Key, machine.AnswerRetry, machine.AnswerStop, r.Answer)
	}
	resolved, err := s.store.ResolveHold(ctx, hold.Key, r.Answer, req.HoldResolver())
	if errors.Is(err, store.ErrHoldResolved) {
		return machine.TaskAnswered{}, fmt.Errorf("%w: %w", ipc.ErrRefused, err)
	}
	if err != nil {
		return machine.TaskAnswered{}, err
	}
	s.publishHoldState(resolved)
	out := machine.TaskAnswered{Hold: resolved, Effect: machine.AnswerRecorded}
	switch {
	case !relaunchHold:
	case r.Answer == machine.AnswerStop:
		out.Effect = machine.AnswerStopped
		s.log.Printf("task %s is not relaunched: hold %s was answered %q", r.Task, hold.Key, r.Answer)
	default:
		relaunch, err := s.relaunchTask(ctx, r.Task)
		var unswept *relaunchUnswept
		if errors.As(err, &unswept) {
			out.Effect, out.NotRelaunched = machine.AnswerRetryHeld, err.Error()
			s.log.Printf("task %s was answered %q on hold %s and was not relaunched, because it now holds on %s: %v",
				r.Task, r.Answer, hold.Key, unswept.Hold.Key, err)
			break
		}
		if err != nil {
			out.Effect, out.NotRelaunched = machine.AnswerRetryDeferred, err.Error()
			s.log.Printf("task %s was answered %q on hold %s and was not relaunched now, so the next start relaunches it: %v",
				r.Task, r.Answer, hold.Key, err)
			break
		}
		out.Effect = machine.AnswerRelaunched
		out.Relaunch = &machine.RelaunchBegun{Attempt: relaunch.launch.attempt, Superseded: relaunch.superseded}
		s.log.Printf("task %s was relaunched as attempt %d on the answer %q to hold %s, superseding session %s; %s",
			r.Task, relaunch.launch.attempt, r.Answer, hold.Key, relaunch.superseded, relaunch.swept)
		s.launchInBackground(relaunch.launch)
	}
	o, err := s.tasks().ObserveTask(ctx, r.Task)
	if err != nil {
		return machine.TaskAnswered{}, err
	}
	out.Task = taskView(o)
	return out, nil
}

// oldestTaskHold is the oldest open hold naming the task and no run, in the
// order internal/store lists open holds, or errNoTaskHold.
func (s *Service) oldestTaskHold(ctx context.Context, id string) (store.Hold, error) {
	open, err := s.store.OpenHolds(ctx)
	if err != nil {
		return store.Hold{}, err
	}
	for _, h := range open {
		if task, ok := h.TaskID.Get(); ok && task == id && !h.RunID.IsKnown() {
			return h, nil
		}
	}
	return store.Hold{}, fmt.Errorf("%w: task %s", errNoTaskHold, id)
}
