package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/custody"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/safety"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// The local delivery posture: PRD section 6's local mode, where a delivery
// task's work never leaves the machine.
//
// # A worker's push starts no run
//
// The worker pushes to the gate the way every delivery worker does, and
// admission holds the push to admit_workers.go's rule exactly as before. What
// changes is the notification: a branch update whose pusher the worker group
// set places in a task's worker group, for that task's own branch, on a task
// recorded under config.DeliveryLocal, starts no run. The posture read is the
// one intake or promotion recorded on the task, never a later read of the
// delivery key,
// and a task with no recorded posture is followed as the gate. Nothing else
// is decided from the pusher: a push from outside every worker group is a
// person's, and a person's push is consent to validation (P1), so it starts
// the full run whatever posture any task of that branch was recorded under.
// A pusher whose group cannot be read is followed as a person, which is the
// stricter of the two paths.
//
// In place of the run, the change is offered for approval: the person's
// local default branch is observed in their working copy through
// internal/safety, and a hold naming the task is registered whose key carries
// the commit pushed and the tip that observation found. The hold is what the
// task resolves Held from, so the task waits on the person, and it is
// answered through task.respond like every hold a task raises, with one
// rule more.
//
// # The approval is answered only from the foreground of a terminal
//
// PRD section 6 gives the local landing to the person: "You approve". Every
// other hold a task raises is the coordinator's to answer, and the
// coordinator reaches task.respond through the same binary and the same
// socket a person does, so for this hold respondTask asks foreground.go's
// relation first: the peer the kernel named must be in the foreground
// process group of its controlling terminal, as its lineage reads, which a
// shell gives a person's command and a host agent's tool does not give the
// command it runs. Anything else is refused with ipc.ErrNotAtTerminal, a
// lineage that cannot be read with ipc.ErrUnavailable, and in both nothing
// is read of the answer, nothing is recorded, and the hold stays open.
// Approve and stop are both held to it, so the offer is closed by nobody
// but a peer at a terminal. A worker is refused as before, first.
//
// What that establishes is a terminal's foreground, not a person: a program
// that allocates a terminal of its own can run the command in its
// foreground, which foreground.go names as the gap. So the resolver
// recorded on the approval stays the one ipc.Request.HoldResolver derives,
// ResolvedByMachineInterface, which understates who decided; a producer of
// store.ResolvedByPerson needs a surface that holds the terminal itself, and
// this build has none.
//
// # Approving fast-forwards, anchored to what was offered
//
// Answering approve is the landing. The anchor is the tip read when the
// change was offered, restored from the hold's key, and never a read made
// when the answer arrives, which P6 names as the anchor that protects
// nothing: a default branch the person moved in between is refused as moved.
// The commit is fetched from the gate into the working copy by identifier,
// internal/safety's DecideFastForward refuses anything but a fast-forward
// from the anchor and anything section 11's custody classification would
// block, and internal/vcs' MoveBranch moves the branch as a
// compare-and-swap on the anchor, with the tree when the branch is checked out
// there. The decision and the move are internal/custody's FastForward, the
// one fast-forward of a person's branch this build makes, which section 11's
// behind row moves through as well. Then the local landing record is written once, in the transaction
// that records the approval on the hold, and the task resolves Landed from it
// in internal/taskstate's terminal tier. A refused approval records nothing:
// the answer is refused with the reason, and the hold stays open for an
// answer that can be acted on, stop among them.
//
// Restoring the anchor goes through safety.RestoreObservedFromCheckpoint,
// whose provenance rests on the record it is handed; the hold key is this
// home's store, the same trust boundary a checkpoint is inside, and nothing
// outside the service writes one.
//
// # Residual gaps
//
//   - A delivery task with no recorded posture is followed as the gate: one
//     recorded before the column existed, or promoted before promotion
//     recorded a posture. Nothing writes one onto such a task afterwards.
//   - Each push of a local task's branch offers its commit on a hold of its
//     own. An answer that names one closes that offer. An answer that names
//     none closes the offer when it is the only one open, and is refused
//     while more than one is open, listing them.
//   - Between the landing's move and its record a crash leaves the branch
//     moved with no record; the hold stays open, and an approve answered
//     again is refused as moved, because the anchor no longer describes the
//     branch. The branch is not put back.
//   - The copy is cleaned up after the landing only when the worker's
//     terminal is already gone; a worker still running keeps its copy, on
//     cleanup.go's terms for a landed delivery task, and nothing asks again.
//   - The foreground relation's own gaps, which foreground.go states: a
//     command in the foreground of a terminal a program allocated for it is
//     served, a person's command run in the background or with no terminal
//     is refused, and the peer's lineage is read at the request rather than
//     at the connection.

// localApprovalPrefix is the key space of the holds a local task's push
// raises.
const localApprovalPrefix = "/local-approval/"

// localApprovalKey is the key of the hold offering commit for approval, with
// tip the local default branch's tip observed when it was offered.
func localApprovalKey(task, commit, tip string) string {
	return "task/" + task + localApprovalPrefix + commit + "/" + tip
}

// localApprovalOf reads a local approval key of the task back into the
// commit offered and the tip observed, and reports whether key is one.
func localApprovalOf(key, task string) (commit, tip string, ok bool) {
	rest, found := strings.CutPrefix(key, "task/"+task+localApprovalPrefix)
	if !found {
		return "", "", false
	}
	commit, tip, found = strings.Cut(rest, "/")
	if !found || commit == "" || tip == "" || strings.Contains(tip, "/") {
		return "", "", false
	}
	return commit, tip, true
}

// errNotLocal reports a local approval answered on a task that is not
// recorded under the local posture, which nothing in this build raises.
var errNotLocal = errors.New("service: the task is not recorded under the local delivery posture")

// localPosture reports whether the task is a delivery task recorded under the
// local posture. A delivery task whose row holds no posture (store.Task's
// Delivery says which rows can) is not local, so its push starts the full
// run. The task's answer states no policy for that row (taskView), because
// the answer says what the record holds and the record holds none; the push
// path defaults to the stricter posture because a push has to take one path
// or the other.
func localPosture(task store.Task) bool {
	posture, ok := task.Delivery.Get()
	return ok && task.Shape == store.TaskDelivery && posture == config.DeliveryLocal
}

// localPush answers whether a push's update is a local task's own push, which
// starts no run: the pusher is in the worker group of a task recorded under
// the local posture and the update is that task's branch. Everything else,
// a person's push and a pusher whose group could not be read among it, is
// not, and starts the run it always did.
func (s *Service) localPush(ctx context.Context, from ipc.Request, update gate.RefUpdate) (store.Task, bool) {
	m, worker, err := s.workerPeer(from)
	if err != nil || !worker {
		return store.Task{}, false
	}
	task, err := s.store.Task(ctx, m.Task)
	if err != nil || !localPosture(task) || task.Branch.Or("") != update.Branch() {
		return store.Task{}, false
	}
	return task, true
}

// offerLocal raises the approval hold for a local task's push and returns
// what the notification reports for it. The default branch is observed
// here, which is the anchor the approval is decided against. Nothing a
// failure here leaves is a run or a landing: the push stands in the gate,
// the reason is reported, and a later push offers again.
func (s *Service) offerLocal(ctx context.Context, task store.Task, update gate.RefUpdate) machine.Ignored {
	noRun := "task " + task.ID + " is delivered under the local posture, so its own push starts no run"
	ignored := func(rest string) machine.Ignored {
		return machine.Ignored{Ref: update.Ref, Reason: noRun + "; " + rest}
	}
	repository, err := s.store.Repository(ctx, task.RepositoryID.Or(""))
	if err != nil {
		return ignored("the change could not be offered for approval: " + err.Error())
	}
	target, primary, err := s.localTarget(ctx, repository)
	if err != nil {
		return ignored("the change could not be offered for approval: " + err.Error())
	}
	observed, err := safety.New(primary).Observe(ctx, target)
	if err == nil && !observed.State().Exists {
		err = fmt.Errorf("%s has no branch %s to fast-forward", repository.WorkingPath, repository.DefaultBranch)
	}
	if err != nil {
		return ignored("the change could not be offered for approval: " + err.Error())
	}
	h, err := s.store.RegisterHold(ctx, store.Hold{
		Key:     localApprovalKey(task.ID, update.New, observed.State().Commit),
		TaskID:  store.Known(task.ID),
		Subject: "whether to move your local " + repository.DefaultBranch + " to this work's change, which nothing validated",
		Detail: fmt.Sprintf("task %s pushed %s to %s. Answer %q to fast-forward %s in %s from %s to it, or %q to leave "+
			"%s where it is. Nothing leaves this machine either way. The answer is taken only from a command in the "+
			"foreground of a terminal, so it is yours and not the coordinator's.",
			task.ID, update.New, update.Branch(), machine.AnswerApprove, repository.DefaultBranch, repository.WorkingPath,
			observed.State().Commit, machine.AnswerStop, repository.DefaultBranch),
	})
	if err != nil {
		return ignored("the change could not be offered for approval: " + err.Error())
	}
	s.publishHoldState(h)
	return ignored("it holds on " + h.Key + " for your approval")
}

// localTarget is the reference a local landing moves - the repository's
// default branch in its working copy - and that working copy, opened.
func (s *Service) localTarget(ctx context.Context, repository store.Repository) (safety.Target, *vcs.Repository, error) {
	if repository.DefaultBranch == "" {
		return safety.Target{}, nil, fmt.Errorf("the repository record of %s names no default branch", repository.ID)
	}
	primary, err := vcs.OpenWorktree(ctx, repository.WorkingPath, s.git()...)
	if err != nil {
		return safety.Target{}, nil, err
	}
	return safety.Target{Remote: repository.WorkingPath, Ref: "refs/heads/" + repository.DefaultBranch}, primary, nil
}

// localFetchRef is where the commit a landing moves to is fetched in the
// working copy, named for the task and deleted once the landing is done.
func localFetchRef(task string) string { return "refs/assistant/local/" + task }

// landLocally performs an approved landing on the terms this file's opening
// comment states and returns the record it wrote. Every refusal is a
// *safety.Refusal or a typed vcs error wrapped with what was being done, and
// on every one nothing is recorded and the hold is left open.
func (s *Service) landLocally(ctx context.Context, req ipc.Request, task store.Task, hold store.Hold, note string) (store.LocalLanding, error) {
	commit, tip, ok := localApprovalOf(hold.Key, task.ID)
	if !ok {
		return store.LocalLanding{}, fmt.Errorf("service: hold %s is not a local approval of task %s", hold.Key, task.ID)
	}
	if !localPosture(task) {
		return store.LocalLanding{}, fmt.Errorf("%w: task %s", errNotLocal, task.ID)
	}
	repository, err := s.store.Repository(ctx, task.RepositoryID.Or(""))
	if err != nil {
		return store.LocalLanding{}, err
	}
	target, primary, err := s.localTarget(ctx, repository)
	if err != nil {
		return store.LocalLanding{}, err
	}
	gatePath, err := s.gatePathOf(ctx, repository)
	if err != nil {
		return store.LocalLanding{}, err
	}
	fetched := localFetchRef(task.ID)
	defer func() { _ = primary.DeleteRef(context.WithoutCancel(ctx), fetched) }()
	if err := primary.Fetch(ctx, vcs.FetchSpec{Remote: gatePath, Refspecs: []string{"+" + commit + ":" + fetched}}); err != nil {
		return store.LocalLanding{}, fmt.Errorf("service: fetching %s from the gate into %s: %w", commit, repository.WorkingPath, err)
	}
	if got, err := primary.ResolveCommit(ctx, fetched); err != nil {
		return store.LocalLanding{}, fmt.Errorf("service: the fetch of %s into %s does not resolve: %w", commit, repository.WorkingPath, err)
	} else if got != commit {
		return store.LocalLanding{}, fmt.Errorf("service: the fetch of %s into %s landed %s", commit, repository.WorkingPath, got)
	}
	anchor, err := safety.RestoreObservedFromCheckpoint(safety.ObservationRecord{
		Remote: target.Remote, Ref: target.Ref, Exists: true, Commit: tip,
	})
	if err != nil {
		return store.LocalLanding{}, fmt.Errorf("service: the anchor hold %s carries: %w", hold.Key, err)
	}
	decision, err := custody.FastForward(ctx, primary, safety.Update{Target: target, Proposed: commit, Anchor: anchor},
		repository.DefaultBranch, "assistant: approved local landing of task "+task.ID)
	if err != nil {
		var refusal *safety.Refusal
		if errors.As(err, &refusal) {
			return store.LocalLanding{}, err
		}
		return store.LocalLanding{}, fmt.Errorf("service: moving %s in %s: %w", repository.DefaultBranch, repository.WorkingPath, err)
	}
	before := decision.Anchor().State().Commit
	landing, err := s.store.RecordLocalLanding(ctx, store.LocalLanding{
		Task: task.ID, Commit: decision.Proposed(), Branch: target.Ref, Before: before, After: decision.Proposed(),
	}, store.HoldClosure{Hold: hold, Resolution: machine.AnswerApprove, Note: note, By: req.HoldResolver()})
	if err != nil {
		s.log.Printf("task %s: %s in %s was fast-forwarded from %s to %s and the landing could not be recorded: %v",
			task.ID, repository.DefaultBranch, repository.WorkingPath, before, decision.Proposed(), err)
		return store.LocalLanding{}, err
	}
	return landing, nil
}

// answerLocal acts on an answer to a local task's approval hold: approve
// lands the change, stop records the answer and moves nothing. Anything
// else is refused before anything is recorded. A landing refused now is an
// error carrying the reason, with the hold left open. A note rides with
// either answer on ResolveHoldNoted's terms.
func (s *Service) answerLocal(ctx context.Context, req ipc.Request, r machine.TaskRespondRequest, task store.Task,
	hold store.Hold) (machine.TaskAnswered, error) {
	switch r.Answer {
	case machine.AnswerStop:
		resolved, err := s.store.ResolveHoldNoted(ctx, hold.Key, r.Answer, r.Note, req.HoldResolver())
		if errors.Is(err, store.ErrHoldResolved) {
			return machine.TaskAnswered{}, fmt.Errorf("%w: %w", ipc.ErrRefused, err)
		}
		if err != nil {
			return machine.TaskAnswered{}, err
		}
		s.publishHoldState(resolved)
		s.log.Printf("task %s: the change offered on %s was declined; nothing was moved", task.ID, hold.Key)
		return machine.TaskAnswered{Hold: resolved, Effect: machine.AnswerRecorded}, nil
	case machine.AnswerApprove:
	default:
		return machine.TaskAnswered{}, fmt.Errorf("%w: task %s holds on %s, which is answered %q or %q, not %q",
			ipc.ErrInvalidRequest, task.ID, hold.Key, machine.AnswerApprove, machine.AnswerStop, r.Answer)
	}
	landing, err := s.landLocally(ctx, req, task, hold, r.Note)
	if err != nil {
		s.log.Printf("task %s: the approval on %s was not acted on: %v", task.ID, hold.Key, err)
		var refusal *safety.Refusal
		if errors.As(err, &refusal) || errors.Is(err, vcs.ErrBranchMoved) || errors.Is(err, vcs.ErrTreeNotClean) ||
			errors.Is(err, store.ErrLocalLandingRecorded) || errors.Is(err, errNotLocal) {
			return machine.TaskAnswered{}, fmt.Errorf("%w: the approval was not acted on and nothing was recorded: %w", ipc.ErrRefused, err)
		}
		return machine.TaskAnswered{}, err
	}
	resolved, err := s.store.Hold(ctx, hold.Key)
	if err != nil {
		return machine.TaskAnswered{}, err
	}
	s.publishHoldState(resolved)
	s.log.Printf("task %s: %s was fast-forwarded from %s to %s on approval", task.ID, landing.Branch, landing.Before, landing.After)
	s.cleanUpLocal(ctx, task.ID)
	return machine.TaskAnswered{Hold: resolved, Effect: machine.AnswerLanded}, nil
}

// cleanUpLocal settles a landed local task on landed.go's terms: it ends
// the worker this service holds for it and then asks for its copy's
// cleanup, with the terminal as this service's task reader resolved it, on
// cleanup.go's terms.
func (s *Service) cleanUpLocal(ctx context.Context, taskID string) {
	s.settleLanded(ctx, taskID)
}
