package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// Answering kept work: PRD section 6's three answers, given on section 9's
// hold answer with --hold naming the entry's key (store.KeptWork.Key), and
// recorded once on the kept work row, the one owner of the answer. No hold
// row is written for an entry and no task state changes because of one,
// which is the owner's decision on #411: the hold answer is the surface,
// and the kept work record is the record.
//
// This build takes land. Carry on and discard are refused with
// ipc.ErrRefused and record nothing: carrying on waits on the owner's
// answer to which worker a kept copy's owner can be relaunched as, and
// discard on #466, the entry listing only what the default branch lacks,
// which its preview is built from.
//
// # Land
//
// PRD section 6: "the program carries the work to the gate as a delivery
// and asks nothing it would not ask of a push". The entry's copy must
// stand, and must hold nothing a commit does not record: a tracked change,
// an untracked file or a stash is refused, because a push carries commits
// and nothing else, and committing a person's uncommitted work is not this
// answer's to decide. Its owner must name a branch of a project to land on:
// a run always does, and a task does when it is a delivery task; an
// investigation's copy is refused, since its delivery is a promotion. The
// copy's HEAD is then fetched into the project's gate under
// refs/assistant/kept/<entry> and a run of the owner's branch is started at
// it through startPushedBranch, the path a push to the gate takes, so the
// run supersedes the branch's run in flight, takes the nine stages and
// holds where a push's would. Its intent is the owner's: a task's, as its
// worker's push would carry, or the owner run's own.
//
// The checks run before the answer is recorded, so a refused land records
// nothing. The answer is then recorded through store.AnswerKeptWork, which
// lets exactly one answer stand, and the landing is carried out and named on
// the row through store.RecordKeptWorkLanding. A service that stops between
// the two leaves an entry answered land with no landing run, and the next
// call or the next start completes it: completeLanding first looks for a
// run of the branch at the copy's commit recorded since the answer, so a
// landing whose run was started before the stop is named rather than
// started twice. What that does not cover: a copy that changed after the
// answer lands what it holds when the landing is completed.
//
// Once the answer is recorded the entry no longer holds its project's
// dispatch, so a held task is asked for again through dispatchInBackground.

// answerKeptWork is MethodKeptWorkAnswer, on this file's terms.
func (s *Service) answerKeptWork(ctx context.Context, req ipc.Request, r machine.KeptWorkAnswerRequest) (machine.KeptWorkAnswered, error) {
	if err := s.refuseWorker(req); err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	id, ok := store.ParseKeptWorkKey(r.Hold)
	switch {
	case !ok:
		return machine.KeptWorkAnswered{}, fmt.Errorf("%w: %q is not a kept work entry's key, which is kept-work/ and its number",
			ipc.ErrInvalidRequest, r.Hold)
	case (strings.TrimSpace(r.Task) == "") == (strings.TrimSpace(r.Run) == ""):
		return machine.KeptWorkAnswered{}, fmt.Errorf("%w: an answer to kept work names exactly one owner, the task or the run whose copy it is",
			ipc.ErrInvalidRequest)
	}
	if r.Task != "" {
		if _, err := s.refuseDecision(ctx, req, r.Task, fmt.Sprintf("asked to answer kept work %s of task %s with %q",
			r.Hold, r.Task, r.Answer)); err != nil {
			return machine.KeptWorkAnswered{}, err
		}
	}
	entry, err := s.store.KeptWorkEntry(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return machine.KeptWorkAnswered{}, fmt.Errorf("%w: %w", ipc.ErrRefused, err)
	} else if err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	if entry.TaskID.Or("") != r.Task || entry.RunID.Or("") != r.Run {
		return machine.KeptWorkAnswered{}, fmt.Errorf("%w: kept work entry %d is %s, not the one named", ipc.ErrRefused, id, keptOwner(entry))
	}
	switch store.KeptAnswer(r.Answer) {
	case store.KeptLand:
		return s.landKeptWork(ctx, req, entry)
	case store.KeptCarryOn:
		return machine.KeptWorkAnswered{}, fmt.Errorf("%w: kept work entry %d was not answered: carrying kept work on is not in this build, "+
			"so nothing was recorded", ipc.ErrRefused, id)
	case store.KeptDiscard:
		return machine.KeptWorkAnswered{}, fmt.Errorf("%w: kept work entry %d was not answered: discarding kept work is not in this build, "+
			"so nothing was recorded and nothing was removed", ipc.ErrRefused, id)
	default:
		return machine.KeptWorkAnswered{}, fmt.Errorf("%w: %q is not an answer to kept work; the answers are land, carry-on and discard",
			ipc.ErrInvalidRequest, r.Answer)
	}
}

// keptOwner names an entry's owner for a refusal.
func keptOwner(k store.KeptWork) string {
	if task, ok := k.TaskID.Get(); ok {
		return "the copy of task " + task
	}
	return "the copy of run " + k.RunID.Or("")
}

// landing is what a land answer carries out, read from the entry's owner
// and its copy.
type landing struct {
	repository store.Repository
	branch     string
	head       string
	intent     pushedIntent
}

// landKeptWork is the land answer, on this file's terms.
func (s *Service) landKeptWork(ctx context.Context, req ipc.Request, entry store.KeptWork) (machine.KeptWorkAnswered, error) {
	if !entry.Open() && entry.Answer.Or("") != store.KeptLand {
		return machine.KeptWorkAnswered{}, refusedAnswered(entry)
	}
	if entry.Open() {
		if _, err := s.landingOf(ctx, entry); err != nil {
			return machine.KeptWorkAnswered{}, err
		}
		answered, err := s.store.AnswerKeptWork(ctx, entry.ID, store.KeptLand, req.HoldResolver())
		switch {
		case errors.Is(err, store.ErrKeptWorkAnswered) || errors.Is(err, store.ErrKeptWorkClosed):
			again, readErr := s.store.KeptWorkEntry(ctx, entry.ID)
			if readErr != nil {
				return machine.KeptWorkAnswered{}, readErr
			}
			if again.Answer.Or("") != store.KeptLand {
				return machine.KeptWorkAnswered{}, refusedAnswered(again)
			}
			entry = again
		case err != nil:
			return machine.KeptWorkAnswered{}, err
		default:
			entry = answered
			s.dispatchInBackground()
		}
	}
	return s.completeLanding(ctx, entry)
}

// refusedAnswered is the refusal of an answer to an entry already answered
// some other way or closed.
func refusedAnswered(k store.KeptWork) error {
	if closure, ok := k.Closure.Get(); ok {
		return fmt.Errorf("%w: kept work entry %d was closed (%s) and takes no answer", ipc.ErrRefused, k.ID, closure)
	}
	return fmt.Errorf("%w: kept work entry %d is already answered %s, and the first answer stands", ipc.ErrRefused, k.ID, k.Answer.Or(""))
}

// landingOf reads what a landing of the entry would carry, refusing with
// ipc.ErrRefused anything this file's Land section refuses.
func (s *Service) landingOf(ctx context.Context, entry store.KeptWork) (landing, error) {
	refuse := func(why string) (landing, error) {
		return landing{}, fmt.Errorf("%w: kept work entry %d was not landed: %s", ipc.ErrRefused, entry.ID, why)
	}
	var out landing
	repositoryID, branch := "", ""
	if taskID, ok := entry.TaskID.Get(); ok {
		task, err := s.store.Task(ctx, taskID)
		if err != nil {
			return landing{}, err
		}
		if task.Shape != store.TaskDelivery {
			return refuse("it is the copy of an investigation, whose work is delivered by promoting it, not by landing")
		}
		repositoryID, branch = task.RepositoryID.Or(""), task.Branch.Or("")
		out.intent = pushedIntent{text: s.taskIntent(task), source: intentSourceTask}
	} else {
		owner, err := s.store.Run(ctx, entry.RunID.Or(""))
		if err != nil {
			return landing{}, err
		}
		repositoryID, branch = owner.RepositoryID, owner.Branch
		out.intent = pushedIntent{text: owner.Intent, source: owner.IntentSource}
		if owner.IntentSource == "" {
			out.intent = noPushedIntent
		}
	}
	if repositoryID == "" || branch == "" {
		return refuse("its owner names no branch of a project to land on")
	}
	repository, err := s.store.Repository(ctx, repositoryID)
	if err != nil {
		return landing{}, err
	}
	out.repository, out.branch = repository, branch
	if _, err := os.Lstat(entry.CopyPath); errors.Is(err, os.ErrNotExist) {
		return refuse("its copy at " + entry.CopyPath + " is gone")
	} else if err != nil {
		return landing{}, fmt.Errorf("service: reading the kept copy at %s: %w", entry.CopyPath, err)
	}
	copyRepo, err := vcs.OpenWorktree(ctx, entry.CopyPath, vcs.WithRedactor(redact.New()))
	if err != nil {
		return landing{}, fmt.Errorf("service: opening the kept copy at %s: %w", entry.CopyPath, err)
	}
	inv, err := copyRepo.Inventory(ctx)
	if err != nil {
		return landing{}, fmt.Errorf("service: reading the kept copy at %s: %w", entry.CopyPath, err)
	}
	switch {
	case inv.Dirty:
		return refuse("its copy holds a tracked change no commit records, and a landing carries commits only; commit or discard it in " + entry.CopyPath)
	case inv.Untracked:
		return refuse("its copy holds a file no commit records, and a landing carries commits only; commit or remove it in " + entry.CopyPath)
	case inv.Stash:
		return refuse("its copy holds a stash, and a landing carries commits only; apply or drop it in " + entry.CopyPath)
	case inv.Head.Commit == "":
		return refuse("its copy's HEAD names no commit")
	}
	out.head = inv.Head.Commit
	return out, nil
}

// completeLanding carries out a land answer already recorded on the entry,
// on this file's terms, and names its run on the row.
func (s *Service) completeLanding(ctx context.Context, entry store.KeptWork) (machine.KeptWorkAnswered, error) {
	if run, ok := entry.LandedRun.Get(); ok {
		return machine.KeptWorkAnswered{Entry: entry, Effect: machine.KeptWorkLanded, Run: run}, nil
	}
	plan, err := s.landingOf(ctx, entry)
	if err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	run, err := s.landedRunSince(ctx, entry, plan)
	if err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	if run == "" {
		gatePath, err := s.gatePathOf(ctx, plan.repository)
		if err != nil {
			return machine.KeptWorkAnswered{}, err
		}
		bare, err := vcs.OpenBare(ctx, gatePath, vcs.WithRedactor(redact.New()))
		if err != nil {
			return machine.KeptWorkAnswered{}, fmt.Errorf("service: opening the gate of %s: %w", plan.repository.ID, err)
		}
		ref := fmt.Sprintf("refs/assistant/kept/%d", entry.ID)
		if err := bare.Fetch(ctx, vcs.FetchSpec{Remote: entry.CopyPath, Refspecs: []string{"+" + plan.head + ":" + ref}}); err != nil {
			return machine.KeptWorkAnswered{}, fmt.Errorf("service: bringing kept work entry %d's commit into the gate: %w", entry.ID, err)
		}
		started, err := s.startPushedBranch(ctx, subject{workingPath: plan.repository.WorkingPath, repository: plan.repository},
			plan.branch, plan.head, plan.intent)
		if err != nil {
			return machine.KeptWorkAnswered{}, err
		}
		run = started.Run
	}
	landed, err := s.store.RecordKeptWorkLanding(ctx, entry.ID, run)
	if err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	s.log.Printf("kept work entry %d was answered land: run %s validates %s on %s", entry.ID, run, plan.head, plan.branch)
	return machine.KeptWorkAnswered{Entry: landed, Effect: machine.KeptWorkLanded, Run: run}, nil
}

// landedRunSince finds the run a landing of the entry already started: a run
// of the landing's branch at its commit recorded at or after the answer,
// which is how a landing stopped between its run and its record is
// completed without a second run.
func (s *Service) landedRunSince(ctx context.Context, entry store.KeptWork, plan landing) (string, error) {
	answered, ok := entry.AnsweredAt.Get()
	if !ok {
		return "", nil
	}
	records, err := s.store.RunsForRepository(ctx, plan.repository.ID)
	if err != nil {
		return "", err
	}
	for _, r := range records {
		if r.Branch == plan.branch && r.SubmittedHead == plan.head && !r.CreatedAt.Before(answered) {
			return r.ID, nil
		}
	}
	return "", nil
}

// completeLandingsAtOpen completes every land answer a stopped service
// recorded and did not carry out, on this file's terms, and logs what it
// could not.
func (s *Service) completeLandingsAtOpen(ctx context.Context) {
	owed, err := s.store.KeptWorkLandingsOwed(ctx)
	if err != nil {
		s.log.Printf("the kept work landings owed could not be read at open: %v", err)
		return
	}
	for _, entry := range owed {
		if _, err := s.completeLanding(ctx, entry); err != nil {
			s.log.Printf("kept work entry %d was answered land and its landing could not be completed: %v", entry.ID, err)
		}
	}
}
