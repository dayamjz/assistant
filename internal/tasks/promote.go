package tasks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
	"github.com/dayamjz/assistant/internal/vcs"
)

// PRD section 5: a report is evidence, not authorization, and promotion is
// the explicit mechanism that flips an investigation task's contract in
// place, in the same terminal with the same loaded context, so the
// protections that apply to delivery work start applying. This file is that
// mechanism as a library entry. Choosing to promote is the coordinator's
// judgment (PRD section 7); everything the program decides from the request
// is Promote's, and every state change it makes is the program's.
//
// # The flip comes first, and it is one anchored write
//
// The flip also answers the copy's open kept work entries, if any, as
// carry on, naming the promoted task as the task carrying them on: PRD
// section 6 has a later promotion relaunch a worker in the kept copy with
// the entry's answer recorded as carry on, and the one transaction is what
// keeps the entry from closing without the promotion or the promotion from
// leaving it open. store.TransitionTaskShape owns the write.
//
// store.TransitionTaskShape writes the shape, the repository, the branch,
// the delivery posture and the delivery intent on the task row together with
// the "promoted" entry, in one transaction,
// anchored to the event sequence the resolution this promotion decided from
// had read. Delivery protection therefore starts before the copy changes:
// from the flip on, a cleanup decides over a delivery copy or refuses with
// store.ErrTaskShapeMoved, and a cleanup that claimed the copy first refuses
// the flip with store.ErrCleanupRecorded, because the two writes read each
// other's row under the store's one writer connection. A task's state is
// resolved rather than stored, so the anchor is what closes the gap between
// reading that state and acting on it: a history that moved since, a worker
// entry or a refusal, is a *store.TaskMovedError the caller reads again on.
//
// # The posture is read the way intake reads it
//
// A promoted task is a delivery task, and PRD section 6 has the record of
// its posture, not a later read of the key, decide its path. So promotion
// records one, on intake's terms (#187): the project's posture is read
// through the same ProjectPosture, from the trusted copy at a fresh fetch of
// the default branch, before the branch's exclusion is taken and before
// anything is written. A copy that cannot be read or parsed refuses the
// promotion with ErrPostureUnreadable and the task stays an investigation,
// with no posture assumed in its place, not even gated. The coordinator may
// ask for a stricter posture than the project's and never a lighter one,
// which is a *PostureError. The posture is then written in the flip itself,
// so no reader sees a promoted task without one, and the schema refuses
// every later write of it.
//
// What that leaves: the posture is read before the exclusion and the anchor,
// so a trusted copy that changes between the read and the flip is not seen,
// which is intake's window too; and a task promoted before this build
// carries no posture and is still followed as the gate, because nothing
// writes one onto a delivery task after its promotion.
//
// # The copy is written to after the flip, and asserted on both sides of it
//
// The copy is the clone the task's dispatch cut and proved: it is given the
// assistant remote at this home's gate and the task's branch at its current
// HEAD, which is the shape PRD section 5 gives a delivery copy, and
// AssertIsolated is then run over it on the delivery expectation, since
// there is no launch to assert at. A refusal is recorded as the task's
// "isolation-refused" event, anchored to the promoted entry, and returned
// as a *PromotionRefusal: the task is a delivery task whose copy fails the
// assertion, which is what its history then says, and the copy stands as it
// was found (P6).
//
// The write into the copy is preceded by one assertion more than the issue
// that specified this asked for, and the reason is PRD principle P1. A copy
// that is, or reaches, the person's primary checkout would have the remote
// written into the primary's own configuration, so before anything is
// written the copy is asserted on the delivery expectation and only the two
// comparisons the shaping is about to change, the remote set and HEAD, may
// fail; any other refusal is recorded without a byte written into the
// directory. That is not the strict stance relaxed in one path: the
// assertion that decides is the one after the write, over the copy as it
// then stands, and the one before it decides only whether the directory
// may be written to at all.
//
// # What a crash leaves
//
// A crash between the flip and the remote leaves a delivery task whose copy
// has no remote and whose HEAD is detached. That is safe: a delivery copy
// is protected, so a cleanup holds it to P12's three proofs and nothing is
// scratch, and the copy pushes nowhere. Nothing here repeats the copy side
// for it, because a second Promote of the task refuses on its shape; the
// residual gap is doc.go's.

// ErrNotPromotable is the class every *NotPromotableError belongs to: the
// task is not an investigation task, or its state is not one the closed
// table Promotable allows a promotion from.
var ErrNotPromotable = errors.New("tasks: the task cannot be promoted from where it stands")

// NotPromotableError reports a refused promotion and names what the task
// was found to be, so the coordinator sees which fact refused it.
type NotPromotableError struct {
	// Task is the task.
	Task string
	// Shape is the task's shape as resolved.
	Shape store.TaskShape
	// State is the task's state as resolved, and Source the tier that
	// decided it.
	State  taskstate.State
	Source taskstate.Source
}

// Error names the task and what refused it.
func (e *NotPromotableError) Error() string {
	if e.Shape != store.TaskInvestigation {
		return fmt.Sprintf("tasks: task %s is a %s task, and only an investigation task is promoted", e.Task, e.Shape)
	}
	return fmt.Sprintf("tasks: task %s resolves %s by its %s, which is not a state a promotion is made from",
		e.Task, e.State, e.Source)
}

// Unwrap makes every refused promotion match ErrNotPromotable.
func (e *NotPromotableError) Unwrap() error { return ErrNotPromotable }

// ErrBranchHeld is the class every *BranchHeldError belongs to: a live task
// already holds the repository and branch the promotion asked for, which is
// the floor Create applies, applied to a promotion.
var ErrBranchHeld = errors.New("tasks: a live task holds the branch")

// BranchHeldError reports a promotion the floor refused and names the task
// that holds the branch, so the person is told what they are waiting on.
// Nothing was written: the task stays an investigation task, and the
// delivery work on that branch is a delivery task queued behind the holder
// through Create, with the holder as its claimed overlap, rather than a
// promotion in place. This package does not queue the promoted task
// itself, because Task.BlockerTask is written at creation and never moves,
// and a second blocker write would be a second owner of that fact.
type BranchHeldError struct {
	// Repository and Branch are what was asked for.
	Repository string
	Branch     string
	// Holder is the newest live task on them, and HolderState its state.
	Holder      string
	HolderState taskstate.State
}

// Error names the branch and its holder.
func (e *BranchHeldError) Error() string {
	return fmt.Sprintf("tasks: branch %s of %s is held by task %s (%s), so the promotion onto it waits: "+
		"queue a delivery task behind that one instead", e.Branch, e.Repository, e.Holder, e.HolderState)
}

// Unwrap makes every held branch match ErrBranchHeld.
func (e *BranchHeldError) Unwrap() error { return ErrBranchHeld }

// ErrPromotionRefused is the class every *PromotionRefusal belongs to: the
// shape was flipped and the copy then failed the isolation assertion, which
// is recorded as the task's "isolation-refused" event.
var ErrPromotionRefused = errors.New("tasks: the task was promoted and its copy was refused")

// PromotionRefusal reports a promotion whose flip stands and whose copy did
// not pass: the task is a delivery task, the refusal is in its history, and
// the copy is where and as it was found.
type PromotionRefusal struct {
	// Task names the task.
	Task string
	// Reason is the recorded token.
	Reason taskstate.IsolationRefusal
	// Event is the entry recorded.
	Event store.TaskEvent
	// Written says whether the remote and the branch were written into the
	// copy before the refusal, which the assertion before the write
	// refuses without doing.
	Written bool
	// Cause is the assertion's own refusal, or what kept the copy from
	// being observed as a repository.
	Cause error
}

// Error names the task, the token and the cause.
func (e *PromotionRefusal) Error() string {
	return fmt.Sprintf("tasks: task %s was promoted and its copy refused (%s): %v", e.Task, e.Reason, e.Cause)
}

// Unwrap makes every refusal match ErrPromotionRefused, and the cause match
// through Is and As, so a caller asking for ErrNotIsolated still finds it.
func (e *PromotionRefusal) Unwrap() []error {
	if e.Cause == nil {
		return []error{ErrPromotionRefused}
	}
	return []error{ErrPromotionRefused, e.Cause}
}

// promotable is the closed table of states a promotion is made from: a
// worker in its terminal, running, paused on something external, holding
// on a decision, or done and reported. Queued and Dispatched have no
// terminal with context to keep; Validating, Ready and Landed are a run's
// states and an investigation has no run; Failed is a copy the assertion
// refused or a worker that gave up; Complete is gone; and Unknown is the
// resolver declining, which a promotion does not read as any of the above.
// A worker whose newest entry is a note resolves Unknown, so it is promoted
// after its next state entry and not before; doc.go names that.
var promotable = []taskstate.State{taskstate.Running, taskstate.Paused, taskstate.Held, taskstate.Reported}

// Promotable reports whether a task in this state may be promoted. The
// set is the closed table above and nothing else.
func Promotable(s taskstate.State) bool { return slices.Contains(promotable, s) }

// PromotableStates returns the closed table, in its order. The result is a
// copy.
func PromotableStates() []taskstate.State { return slices.Clone(promotable) }

// Promotion is what the coordinator asks for. The branch and the repository
// are the request's and never the report's, and so is the intent: the
// report is untrusted worker text that the stored row carries as evidence,
// and the intent stage reads supplied intent.
type Promotion struct {
	// Task is the investigation task to promote.
	Task string
	// Repository names the repository the delivery works in. It must be
	// the one the task's copy was cut from.
	Repository string
	// Branch is the branch the delivery works on, created in the copy at
	// its current HEAD. It is held to intake's rules: the name, a branch
	// that exists in the gate or has a run, and the same-branch floor.
	Branch string
	// Intent is what the delivery is to do. It is recorded on the task row
	// in the flip, as store.Task.DeliveryIntent, which is what the run a
	// worker's push of the branch starts reads, and written under the
	// task's instructions for the worker. It is refused empty: a delivery
	// with nothing to do is not one, and the report is not it.
	Intent string
	// Delivery is a delivery posture the coordinator asks to record in place
	// of the project's, as text decoded by config.ParseDelivery, or empty to
	// record the project's own, on Request.Delivery's terms: it may be
	// stricter than the project's and never lighter, which is a
	// *PostureError.
	Delivery string
	// By is who promotes, which the copy's open kept work entries, if any,
	// are answered carry-on as in the flip, on store.TransitionTaskShape's
	// terms: internal/service passes what internal/ipc derives from the
	// surface. The zero Resolver is refused.
	By store.Resolver
	// Environment is the environment the worker runs under, as
	// agents.WorkerEnvironment computes it for a launch; the assertion
	// refuses one vcs.StripRedirecting would change. The worker was
	// launched before this call and nothing here can read its environment
	// back, so the assertion is of what the caller hands in.
	Environment []string
}

// ReportPointer names the stored report a promotion carries as evidence:
// the task_report row, which store.TaskReport reads by the task. The text
// is not carried, on PRD principle P13's terms.
type ReportPointer struct {
	// Task is the row's key.
	Task string
	// RecordedAt is when the row was written.
	RecordedAt time.Time
}

// Promoted is what a promotion did.
type Promoted struct {
	// Task is the record after the flip.
	Task store.Task
	// Event is the promoted entry, which is the note the worker's history
	// carries that its contract changed.
	Event store.TaskEvent
	// Head is the commit the task's branch was created at: the copy's HEAD
	// at promotion, whatever the investigation committed on top of its
	// start.
	Head string
	// Report points at the stored report when one was stored, and is
	// unknown otherwise; a promotion does not require one.
	Report store.Optional[ReportPointer]
	// Instructions is the file the intent and the second check were
	// written to.
	Instructions string
	// Brief is the text appended to that file, PromotionBrief's, which a
	// caller holding a live worker sends it too.
	Brief string
	// Isolation is the proof the copy passed after it was shaped.
	Isolation Isolation
}

// Promote flips an investigation task to a delivery task in place, on the
// terms this file's opening comment states, and returns what it did.
//
// It decides from one read of every task, under the branch's exclusion: the
// task must be an investigation task in a state Promotable allows; the
// request's repository must be the one the task's record names, because
// that is what its copy was cut from; and the branch is held to intake's
// rules, the name, a branch no task has worked on that exists in the gate
// or has a run (*BranchTakenError), and the floor, a live task on the
// branch (*BranchHeldError), under which nothing is written. The flip is
// store.TransitionTaskShape anchored to that read, and it records the
// posture read before it: the project's, from its trusted copy through the
// queue's ProjectPosture, or the request's when that is no lighter. A copy
// that cannot be read refuses with ErrPostureUnreadable and a lighter request
// with a *PostureError, and under either nothing is written. Its refusals,
// *store.TaskMovedError, store.ErrTaskShapeMoved and
// store.ErrCleanupRecorded, come back as they are. The exclusion is given
// back after the flip, before the copy is touched.
//
// After the flip the copy is asserted, shaped, asserted again, and the
// intent and the delivery second check are appended to the instructions
// file; a copy that fails either assertion is a *PromotionRefusal with the
// refusal in the task's history, and a copy that cannot be opened as a
// repository is refused the same way, as RefusedCopyMissing when nothing
// stands at its path and RefusedNotIsolated otherwise. An error that is no
// refusal is a record that could not be read or written, a repository that
// could not be written to, or an instructions file that could not be
// appended to; the flip stands through every one of those, and doc.go
// states what that leaves.
func (q *Queue) Promote(ctx context.Context, req Promotion) (Promoted, error) {
	if strings.TrimSpace(req.Task) == "" {
		return Promoted{}, fmt.Errorf("%w: no task", ErrRequest)
	}
	if strings.TrimSpace(req.Repository) == "" {
		return Promoted{}, fmt.Errorf("%w: a delivery task names a repository", ErrRequest)
	}
	branch := strings.TrimSpace(req.Branch)
	if branch == "" {
		return Promoted{}, fmt.Errorf("%w: a delivery task names a branch", ErrRequest)
	}
	if reason := branchNameRefusal(branch); reason != "" {
		return Promoted{}, fmt.Errorf("%w: branch %q %s", ErrRequest, branch, reason)
	}
	if strings.TrimSpace(req.Intent) == "" {
		return Promoted{}, fmt.Errorf("%w: no intent", ErrRequest)
	}
	if req.By == (store.Resolver{}) {
		return Promoted{}, fmt.Errorf("%w: a promotion names who promotes", ErrRequest)
	}
	var requested config.Delivery
	if text := strings.TrimSpace(req.Delivery); text != "" {
		var err error
		if requested, err = config.ParseDelivery(text); err != nil {
			return Promoted{}, fmt.Errorf("%w: %w", ErrRequest, err)
		}
	}
	repository, err := q.store.Repository(ctx, req.Repository)
	if err != nil {
		return Promoted{}, fmt.Errorf("tasks: the repository the delivery works in: %w", err)
	}
	posture, err := q.recordedPosture(ctx, repository, req.Task, requested)
	if err != nil {
		return Promoted{}, err
	}
	gatePath, err := q.gatePath(ctx, repository)
	if err != nil {
		return Promoted{}, err
	}
	copyPath, err := q.home.TaskCopy(req.Task)
	if err != nil {
		return Promoted{}, err
	}
	instructions, err := q.home.TaskInstructions(req.Task)
	if err != nil {
		return Promoted{}, err
	}

	var out Promoted
	flip := func() error {
		resolved, err := q.reader.All(ctx)
		if err != nil {
			return err
		}
		r, err := q.promotable(ctx, req.Task, repository, branch, resolved)
		if err != nil {
			return err
		}
		detail := fmt.Sprintf("promoted to delivery on %s %s under %s", repository.ID, branch, posture)
		switch report, err := q.store.TaskReport(ctx, req.Task); {
		case err == nil:
			out.Report = store.Known(ReportPointer{Task: report.TaskID, RecordedAt: report.RecordedAt})
			detail += "; the report stored at " + report.RecordedAt.UTC().Format(time.RFC3339) + " is evidence"
		case !errors.Is(err, store.ErrNotFound):
			return err
		default:
			detail += "; no report stored"
		}
		if q.betweenReadAndWrite != nil {
			q.betweenReadAndWrite()
		}
		out.Task, out.Event, err = q.store.TransitionTaskShape(ctx, req.Task, store.TaskInvestigation, store.TaskDelivery,
			repository.ID, branch, posture, strings.TrimSpace(req.Intent), r.State.Basis.EventSequence, detail, req.By)
		return err
	}
	release, err := q.hold(ctx, repository.ID, branch)
	if err != nil {
		return Promoted{}, err
	}
	err = flip()
	release()
	if err != nil {
		return Promoted{}, err
	}
	if q.betweenFlipAndCopy != nil {
		q.betweenFlipAndCopy()
	}

	want, secondCheck, err := Expect(q.home, req.Task, store.TaskDelivery, gatePath, repository.WorkingPath, branch)
	if err != nil {
		return Promoted{}, err
	}
	refuse := func(reason taskstate.IsolationRefusal, written bool, cause error) (Promoted, error) {
		event, err := q.refuseIsolation(ctx, req.Task, out.Event.Sequence, reason)
		if err != nil {
			return Promoted{}, err
		}
		return Promoted{}, &PromotionRefusal{Task: req.Task, Reason: reason, Event: event, Written: written, Cause: cause}
	}
	obs, err := vcs.Observe(ctx, copyPath, q.git...)
	if err != nil {
		if !errors.Is(err, vcs.ErrNotARepository) {
			return Promoted{}, fmt.Errorf("tasks: observing the copy of task %s: %w", req.Task, err)
		}
		if _, statErr := os.Lstat(copyPath); errors.Is(statErr, os.ErrNotExist) {
			return refuse(taskstate.RefusedCopyMissing, false, err)
		}
		return refuse(taskstate.RefusedNotIsolated, false, err)
	}
	if _, err := AssertIsolated(obs, req.Environment, want); err != nil {
		var refused *IsolationError
		if !errors.As(err, &refused) {
			return Promoted{}, fmt.Errorf("tasks: asserting the copy of task %s before shaping it: %w", req.Task, err)
		}
		if refused.Comparison != ComparisonRemotes && refused.Comparison != ComparisonHead {
			reason, _ := Refusal(err)
			return refuse(reason, false, err)
		}
	}

	copied, err := vcs.OpenWorktree(ctx, copyPath, q.git...)
	if err != nil {
		return Promoted{}, fmt.Errorf("tasks: opening the copy of task %s: %w", req.Task, err)
	}
	if err := copied.SetRemote(ctx, gate.RemoteName, gatePath); err != nil {
		return Promoted{}, fmt.Errorf("tasks: pointing %s of the copy of task %s at the gate: %w", gate.RemoteName, req.Task, err)
	}
	head, err := copied.ResolveCommit(ctx, "HEAD")
	if err != nil {
		return Promoted{}, fmt.Errorf("tasks: reading HEAD of the copy of task %s: %w", req.Task, err)
	}
	if err := copied.SwitchToNewBranch(ctx, branch, head); err != nil {
		return Promoted{}, fmt.Errorf("tasks: creating the branch %s at %s in the copy of task %s: %w", branch, head, req.Task, err)
	}
	out.Head = head

	obs, err = vcs.Observe(ctx, copyPath, q.git...)
	if err != nil {
		return Promoted{}, fmt.Errorf("tasks: observing the shaped copy of task %s: %w", req.Task, err)
	}
	proof, err := AssertIsolated(obs, req.Environment, want)
	if err != nil {
		reason, refused := Refusal(err)
		if !refused {
			return Promoted{}, fmt.Errorf("tasks: asserting the shaped copy of task %s: %w", req.Task, err)
		}
		return refuse(reason, true, err)
	}
	out.Isolation = proof

	brief := PromotionBrief(branch, req.Intent, secondCheck)
	if err := appendPromotion(instructions, brief); err != nil {
		return Promoted{}, fmt.Errorf("tasks: writing the intent into the instructions of task %s: %w", req.Task, err)
	}
	out.Instructions = instructions
	out.Brief = brief
	return out, nil
}

// promotable finds the task in the read and decides whether the request may
// be made of it: the shape and the state, the repository the copy was cut
// from, and the branch under intake's rules and the floor. It returns the
// task's resolution, whose basis is what the flip is anchored to.
func (q *Queue) promotable(ctx context.Context, id string, repository store.Repository, branch string,
	resolved []taskstate.Resolved) (taskstate.Resolved, error) {
	var r *taskstate.Resolved
	for i := range resolved {
		if resolved[i].Record.ID == id {
			r = &resolved[i]
			break
		}
	}
	if r == nil {
		return taskstate.Resolved{}, fmt.Errorf("tasks: the task %q to promote: %w", id, store.ErrNotFound)
	}
	if r.Record.Shape != store.TaskInvestigation || !Promotable(r.State.State) {
		return taskstate.Resolved{}, &NotPromotableError{Task: id, Shape: r.Record.Shape, State: r.State.State, Source: r.State.Source}
	}
	switch cut, ok := r.Record.RepositoryID.Get(); {
	case !ok:
		return taskstate.Resolved{}, fmt.Errorf("%w: task %s names no repository, so its copy was cut from none", ErrRequest, id)
	case cut != repository.ID:
		return taskstate.Resolved{}, fmt.Errorf("%w: task %s works in %s, and its copy cannot deliver to %s",
			ErrRequest, id, cut, repository.ID)
	}
	onBranch := 0
	var holder *taskstate.Resolved
	for i := range resolved {
		other := &resolved[i]
		if other.Record.ID == id || other.Record.RepositoryID.Or("") != repository.ID || other.Record.Branch.Or("") != branch {
			continue
		}
		onBranch++
		if Live(other.State.State) && (holder == nil || other.Record.Row > holder.Record.Row) {
			holder = other
		}
	}
	if holder != nil {
		return taskstate.Resolved{}, &BranchHeldError{Repository: repository.ID, Branch: branch,
			Holder: holder.Record.ID, HolderState: holder.State.State}
	}
	if onBranch == 0 {
		if err := q.refuseAPersonsBranch(ctx, repository, branch); err != nil {
			return taskstate.Resolved{}, err
		}
	}
	return *r, nil
}

// refuseIsolation records the refusal as the task's "isolation-refused"
// event, anchored to the promoted entry, on the terms internal/service's
// dispatch records its own: the token is checked against the kind's decoder
// before it is appended, because the store records any text and
// internal/taskstate's table is the one place the vocabulary is defined.
func (q *Queue) refuseIsolation(ctx context.Context, id string, anchor int64, reason taskstate.IsolationRefusal) (store.TaskEvent, error) {
	row, ok := taskstate.Lookup(isolationRefusedKind)
	if !ok || row.Detail == nil {
		return store.TaskEvent{}, fmt.Errorf("tasks: the %q kind has no decoder to check %q against", isolationRefusedKind, reason)
	}
	if err := row.Detail(string(reason)); err != nil {
		return store.TaskEvent{}, fmt.Errorf("tasks: task %s: %w", id, err)
	}
	event, err := q.store.AppendTaskEventAfter(ctx, id, anchor, isolationRefusedKind, string(reason))
	if err != nil {
		return store.TaskEvent{}, fmt.Errorf("tasks: recording the refusal of task %s: %w", id, err)
	}
	return event, nil
}

// isolationRefusedKind is the kind internal/taskstate gives the refusal,
// spelled here because that package exports the row and not the name;
// Lookup is what checks the spelling is one that package knows.
const isolationRefusedKind = "isolation-refused"

// PromotionHeading is the heading the intent and the delivery second check
// are appended to the instructions under. It is exported for one consumer,
// internal/service's task intent (intakeInstructions in workers.go), which
// reads the instructions file as the coordinator wrote it at intake and stops
// at the first section the program appended, this one among them. That
// consumer reads the file only for a task with no recorded delivery intent,
// so this section matters there only for a task promoted before the store
// recorded one.
const PromotionHeading = "## Promoted to delivery, written by the program at promotion"

// PromotionBrief is what a promoted task's worker is told its contract now
// is: the heading, the branch it delivers on and where it pushes, the intent
// as the coordinator gave it, and the second check, line, to run before
// committing anything further. The intent is the request's, and every other
// word is fixed here, so the instructions file and the message a live worker
// is sent at promotion carry one text.
func PromotionBrief(branch, intent, line string) string {
	return PromotionHeading + "\n\n" +
		"This task is now a delivery task. Its deliverable is a change on the branch " + branch + ", pushed only to the " +
		"assistant remote, which is the gate this task's delivery goes through, and the intent for it is:\n\n" +
		strings.TrimSpace(intent) + "\n\n" +
		"Before committing anything further, run this line in the working copy you were started in and stop if it " +
		"exits non-zero: it checks that you are in the isolated copy the program cut for this task, now with the " +
		"one remote a delivery copy has.\n\n    " + line + "\n"
}

// appendPromotion appends the brief to the instructions file, once and
// whole (AppendNote). The file is the one thing the worker was started
// with, and a worker told to re-read it, or a worker begun on the task
// later, is told there what the contract now is.
func appendPromotion(path, brief string) error {
	return AppendNote(path, PromotionHeading, brief)
}
