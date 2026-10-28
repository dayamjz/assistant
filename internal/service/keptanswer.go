package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
	"github.com/dayamjz/assistant/internal/vcs"
)

// Answering kept work: PRD section 6's three answers, given on section 9's
// hold answer with --hold naming the entry's key (store.KeptWork.Key), and
// recorded once on the kept work row, the one owner of the answer. No hold
// row is written for an entry and no task state changes because of one,
// which is the owner's decision on #411: the hold answer is the surface,
// and the kept work record is the record.
//
// This build takes all three answers. Land is below, carrying on is
// keptcarryon.go's and discarding is keptdiscard.go's.
//
// # Land
//
// PRD section 6: "the program carries the work to the gate as a delivery
// and asks nothing it would not ask of a push". Its owner must name a
// branch of a project to land on: a run always does, and a task does when
// it is a delivery task; an investigation's copy is refused, since its
// delivery is a promotion. The copy's HEAD is then fetched into the
// project's gate under refs/assistant/kept/<entry> and a run of the
// owner's branch is started at it through startPushedBranch, the path a
// push to the gate takes, so the run supersedes the branch's run in
// flight, takes the nine stages and holds where a push's would. Its intent
// is the owner's: a task's, as its worker's push would carry, or the owner
// run's own.
//
// What is landed is HEAD's commit, and the entry is answered as a whole,
// so the copy must hold nothing a landing leaves behind, decided from the
// copy as it stands by the one computation a discard shows,
// internal/tasks' Loss, and never from the entry's record of the copy
// when it was kept (store.KeptWork.Lacks), which says nothing of what the
// copy gained since: work answered and never carried is kept work gone
// silent (P15). So a landing is refused, naming what it would leave
// behind, when Loss shows a path of the working tree or the index no
// commit records, a changed, new, staged or deleted path, a file git
// ignores among them, since a push carries commits and nothing else and
// committing a person's uncommitted work is not this answer's to decide;
// when it shows one of git's indicators, a tracked change, an untracked
// file or a stash, a run's own entries of the gate's; and when it shows a
// commit lost that HEAD does not reach, or another object lost that HEAD's
// history does not hold (tasks.Cleaner.LeftBehind, read from the object
// graph Loss reads): a commit on another reference, in a stash or recorded
// only by a reflog entry's either value, a commit amended
// or rebased away among them, and an annotated tag only the copy holds,
// which a landing never carries. Nothing Loss takes as held refuses it, so
// what the upstream holds now, a tag or a branch cloned with the copy and
// what a remote-tracking reference names that the upstream's branch still
// reaches among it, is not left behind, while a commit only a stale
// remote-tracking reference names refuses it; nor
// is a file git reports clean whose bytes its line-ending conversion or
// filters changed. A default branch or an upstream that cannot be read now
// refuses it as not established. A run's worktree is read as a discard
// reads it, so a commit only the gate's references or reflogs, or another
// worktree's stash entries, record refuses nothing, and one the
// worktree's own names reach refuses it whatever the gate records too.
//
// What the check does not cover, and what that costs. The entries of the
// repository directory Loss shows (LostRepository: the configuration, a
// hook, info/exclude, the pointer files git keeps) are not refused, since
// every clone holds some that differ from a new repository's and a
// landing would then refuse every copy; a hook or a configuration of the
// person's own is not carried and is not named. Loss lists what fails
// toward showing, so the refusal does too: a commit only a reflog records
// refuses a landing though the copy's own branch carries what replaced
// it, and so does a tag of the person's checkout the upstream does not
// have, cloned with the copy; the person expires the record or removes
// the name and answers again, or lands by hand. And the moment between the check and
// the fetch of HEAD's commit is not closed: what the copy gains in it is
// neither carried nor named, and the landing carries the commit the check
// read, by its identifier, whatever HEAD names by then.
//
// The checks run before anything is recorded, so a refused land records
// nothing. A landing is then recorded as two facts, so the entry is never
// answered land before its run exists. The claim is written first,
// store.ClaimKeptWorkLanding, once, with who gave the answer, and leaves
// the entry open: status, the catch-up read and the supervisor loop list
// it, and its project's new tasks still wait on it. Then the run is
// started through startPushedBranch, and the entry is answered land naming
// the run in one write, store.RecordKeptWorkLanding, which is when it
// closes and the project's tasks dispatch. Two answers landing one entry
// get one claim between them, and the window between the claim and the
// record, where a second answer finds the claim written and no run yet, is
// held in this service's memory, landings, which completeLanding takes for
// the entry before it reads the claim and gives back when it has recorded
// the run or failed, so a second answer or the open-time completer meeting
// a landing under way here is refused as being landed now rather than
// starting a second run. One service holds a home, so the memory is the
// whole of it; the claim covers a service that stopped inside the window.
//
// A run that cannot be started, the copy's commit not brought into the gate
// or the run refused, leaves nothing to land through, so the claim is
// withdrawn (store.WithdrawKeptWorkLanding) and the answer is refused
// naming why, with the entry open as it was. A service that stops between
// the claim and the record leaves an open entry with a claim and no run,
// and the next call or the next start completes it: completeLanding first
// looks for the run the claim started, a run of the branch at the copy's
// commit recorded since the claim whose submitted reference stands in the
// gate at that commit and which has started validation, a checkpoint
// recorded (landedRunSince), which it names rather than starting twice. A
// run with no reference or no checkpoint, a create stopped before its
// reference was written, pending or ended, is not that run: nothing of the
// work reached validation through it, so the retry starts another. A
// completion checks the copy again as it stands (landingOf), so a copy
// that changed after the claim lands what it holds then or is refused for
// what it would leave behind. What that does not cover: a completion
// refused so, or one that cannot read what it would land, leaves the claim
// standing, the entry open and listed and no other answer taken on the
// copy, until a later call or start carries it out once the copy holds
// nothing a landing leaves behind; withdrawing such a claim is #530's.

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
	// A subordinate coordinator is refused whichever owner the entry names,
	// on refuseDecision's terms, which records the ask as an escalation only
	// for a task in its domain; an entry a run left has no task to escalate
	// about and is refused the same.
	if _, err := s.refuseDecision(ctx, req, r.Task, fmt.Sprintf("asked to answer kept work %s of task %q, run %q, with %q",
		r.Hold, r.Task, r.Run, r.Answer)); err != nil {
		return machine.KeptWorkAnswered{}, err
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
	if r.Confirm != "" && store.KeptAnswer(r.Answer) != store.KeptDiscard {
		return machine.KeptWorkAnswered{}, fmt.Errorf("%w: a confirmation is read with the discard answer alone, and was given with %q",
			ipc.ErrInvalidRequest, r.Answer)
	}
	switch store.KeptAnswer(r.Answer) {
	case store.KeptLand:
		return s.landKeptWork(ctx, req, entry)
	case store.KeptCarryOn:
		return s.carryOnKeptWork(ctx, req, entry)
	case store.KeptDiscard:
		return s.discardKeptWork(ctx, req, entry, r.Confirm)
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
	// A claim this answer finds standing, one a stopped service or another
	// answer left, is completed rather than claimed again.
	if entry.Open() && !entry.LandingClaimed() {
		if _, err := s.landingOf(ctx, entry); err != nil {
			return machine.KeptWorkAnswered{}, err
		}
		claimed, err := s.store.ClaimKeptWorkLanding(ctx, entry.ID, req.HoldResolver())
		switch {
		case errors.Is(err, store.ErrKeptWorkAnswered) || errors.Is(err, store.ErrKeptWorkClosed) ||
			errors.Is(err, store.ErrKeptWorkLandingClaimed):
			// Answered, closed or claimed by another answer in between: a
			// landing it reads is completed, anything else refused.
			again, readErr := s.store.KeptWorkEntry(ctx, entry.ID)
			if readErr != nil {
				return machine.KeptWorkAnswered{}, readErr
			}
			if again.Answer.Or("") != store.KeptLand && !again.LandingClaimed() {
				return machine.KeptWorkAnswered{}, refusedAnswered(again)
			}
			entry = again
		case errors.Is(err, store.ErrKeptWorkCopyOwed):
			return machine.KeptWorkAnswered{}, refusedOwed(entry, err)
		case err != nil:
			return machine.KeptWorkAnswered{}, err
		default:
			entry = claimed
		}
	}
	return s.completeLanding(ctx, entry)
}

// Carrying on is keptcarryon.go's.

// refusedOwed is the refusal of an answer to an entry of a copy that holds
// an answer not yet carried out, store.ErrKeptWorkCopyOwed.
func refusedOwed(k store.KeptWork, err error) error {
	return fmt.Errorf("%w: kept work entry %d was not answered: %w; that answer is carried out, or a carry on whose task "+
		"cannot be created is withdrawn, by its own next call or the next start, "+
		"and this entry can be answered after",
		ipc.ErrRefused, k.ID, err)
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
	// What the copy holds is read as it stands now, by the one computation
	// a discard shows (tasks.Loss), never from the entry's record of the
	// copy when it was kept.
	gatePath, err := s.keptGatePath(ctx, entry)
	if err != nil {
		return landing{}, err
	}
	loss, err := s.cleaner.Loss(ctx, entry, gatePath)
	switch {
	case errors.Is(err, tasks.ErrCopyGone):
		return refuse("its copy at " + entry.CopyPath + " is gone")
	case errors.Is(err, tasks.ErrLossUnestablished):
		return refuse("what its copy holds that the default branch lacks is not established now (" + err.Error() +
			"), so a landing cannot say what it would leave behind")
	case errors.Is(err, tasks.ErrLayoutUnsupported):
		return refuse(err.Error())
	case err != nil:
		return landing{}, err
	}
	var paths []string
	for _, f := range loss.Files {
		if f.Kind != tasks.LostRepository {
			paths = append(paths, string(f.Kind)+" "+f.Path)
		}
	}
	switch {
	case len(paths) > 0:
		return refuse("its copy holds what no commit records, which a landing would leave behind: " + strings.Join(paths, ", ") +
			"; a landing carries commits only, so commit or remove them in " + entry.CopyPath)
	case loss.Dirty:
		return refuse("its copy holds a tracked change no commit records, and a landing carries commits only; commit or discard it in " + entry.CopyPath)
	case loss.Untracked:
		return refuse("its copy holds a file no commit records, and a landing carries commits only; commit or remove it in " + entry.CopyPath)
	case loss.Stash:
		return refuse("its copy holds a stash, and a landing carries commits only; apply or drop it in " + entry.CopyPath)
	case loss.Head == "":
		return refuse("its copy's HEAD names no commit")
	}
	out.head = loss.Head
	behind, err := s.cleaner.LeftBehind(ctx, entry, loss)
	if err != nil {
		return landing{}, err
	}
	if len(behind) > 0 {
		return refuse(fmt.Sprintf("a landing carries HEAD, %s, and its copy holds %s, which HEAD does not carry, so landing it would "+
			"leave that behind: a commit on another reference, in a stash or recorded only by a reflog, or an object a name "+
			"records; put it on the branch or remove it in %s first, or land what HEAD holds by hand",
			out.head, strings.Join(behind, ", "), entry.CopyPath))
	}
	return out, nil
}

// completeLanding carries out a land answer already recorded on the entry,
// on this file's terms, and names its run on the row.
func (s *Service) completeLanding(ctx context.Context, entry store.KeptWork) (machine.KeptWorkAnswered, error) {
	if run, ok := entry.LandedRun.Get(); ok {
		return machine.KeptWorkAnswered{Entry: entry, Effect: machine.KeptWorkLanded, Run: run}, nil
	}
	if !s.takeLanding(entry.ID) {
		return machine.KeptWorkAnswered{}, fmt.Errorf("%w: kept work entry %d is being landed by another answer now", ipc.ErrRefused, entry.ID)
	}
	defer s.releaseLanding(entry.ID)
	// The entry is read again under the slot, so a landing recorded between
	// this answer's read and the slot is found rather than done twice.
	var err error
	if entry, err = s.store.KeptWorkEntry(ctx, entry.ID); err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	if run, ok := entry.LandedRun.Get(); ok {
		return machine.KeptWorkAnswered{Entry: entry, Effect: machine.KeptWorkLanded, Run: run}, nil
	}
	if !entry.LandingClaimed() {
		// Withdrawn, or answered otherwise, by another answer in between.
		if entry.Answered() || entry.Closure.IsKnown() {
			return machine.KeptWorkAnswered{}, refusedAnswered(entry)
		}
		return machine.KeptWorkAnswered{}, fmt.Errorf("%w: the landing of kept work entry %d was withdrawn; the entry is open", ipc.ErrRefused, entry.ID)
	}
	plan, err := s.landingOf(ctx, entry)
	if err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	gatePath, err := s.gatePathOf(ctx, plan.repository)
	if err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	bare, err := vcs.OpenBare(ctx, gatePath, s.git()...)
	if err != nil {
		return machine.KeptWorkAnswered{}, fmt.Errorf("service: opening the gate of %s: %w", plan.repository.ID, err)
	}
	// A claim may be one a landing that stopped between its run and its
	// record left, whose run is looked for before another is started.
	run, err := s.landedRunSince(ctx, entry, plan, bare)
	if err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	if run == "" {
		if s.betweenLandingClaimAndStart != nil {
			s.betweenLandingClaimAndStart()
		}
		started, err := s.startLanding(ctx, entry, plan, bare)
		if err != nil {
			// No run was handed to execution, here or by a landing before
			// (none was found), so the claim is withdrawn and the entry is
			// open again, listed and holding its project's dispatch as it
			// was all along.
			if _, withdrawErr := s.store.WithdrawKeptWorkLanding(ctx, entry.ID); withdrawErr != nil {
				return machine.KeptWorkAnswered{}, fmt.Errorf("%w; the landing of kept work entry %d stands, owed: %w", err, entry.ID, withdrawErr)
			}
			s.log.Printf("kept work entry %d was not landed: its run could not be started (%v); the claim was withdrawn", entry.ID, err)
			return machine.KeptWorkAnswered{}, fmt.Errorf("%w; kept work entry %d was not landed, and is open again", err, entry.ID)
		}
		run = started
		if s.betweenLandingStartAndRecord != nil {
			if err := s.betweenLandingStartAndRecord(run); err != nil {
				return machine.KeptWorkAnswered{}, err
			}
		}
	}
	landed, err := s.store.RecordKeptWorkLanding(ctx, entry.ID, run)
	if err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	s.log.Printf("kept work entry %d was answered land: run %s validates %s on %s", entry.ID, run, plan.head, plan.branch)
	// The entry is answered now, so it no longer holds its project's
	// dispatch.
	s.dispatchInBackground()
	return machine.KeptWorkAnswered{Entry: landed, Effect: machine.KeptWorkLanded, Run: run}, nil
}

// startLanding brings the copy's commit into the gate and starts the run of
// the landing's branch at it, the path a push to the gate takes, and
// returns the run. An error means no run was handed to execution:
// startPushedBranch hands it over only after its last step that can fail.
// A create that failed part way can leave a run's row with no reference or
// no checkpoint, which landedRunSince never takes for the landing's run.
func (s *Service) startLanding(ctx context.Context, entry store.KeptWork, plan landing, bare *vcs.Repository) (string, error) {
	ref := fmt.Sprintf("refs/assistant/kept/%d", entry.ID)
	if err := bare.Fetch(ctx, vcs.FetchSpec{Remote: entry.CopyPath, Refspecs: []string{"+" + plan.head + ":" + ref}}); err != nil {
		return "", fmt.Errorf("service: bringing kept work entry %d's commit into the gate: %w", entry.ID, err)
	}
	started, err := s.startPushedBranch(ctx, subject{workingPath: plan.repository.WorkingPath, repository: plan.repository},
		plan.branch, plan.head, plan.intent)
	if err != nil {
		return "", err
	}
	return started.Run, nil
}

// takeLanding takes the entry's landing slot in this service, and reports
// false when a landing of the entry holds it now.
func (s *Service) takeLanding(id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.landings == nil {
		s.landings = make(map[int64]bool)
	}
	if s.landings[id] {
		return false
	}
	s.landings[id] = true
	return true
}

// releaseLanding gives the entry's landing slot back.
func (s *Service) releaseLanding(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.landings, id)
}

// landedRunSince finds the run a claimed landing of the entry already
// started: a run of the landing's branch at its commit recorded at or after
// the claim that carries its submitted reference in the gate at that commit,
// has started validation, a checkpoint recorded, and is live (pending,
// running or held) or passed, which is how a landing stopped between its
// run and its record is completed without a second run. A run with no
// reference or no checkpoint, a create stopped before its reference was
// written or a run ended before it executed, is not that run, and neither
// is one that failed or was terminated after it checkpointed (round 45:
// naming it closed the entry and lifted the dispatch hold with nothing
// validating the work, against P15): nothing of the work reaches the gate
// through it, so the retry starts another, which supersedes it. A
// reference or a checkpoint that cannot be read is an error and not that
// absence (the final full review of feddd14), and so is a run status this
// build does not recognize: the completion fails, the claim stands, and
// the next call or start looks again.
func (s *Service) landedRunSince(ctx context.Context, entry store.KeptWork, plan landing, bare *vcs.Repository) (string, error) {
	claimed, ok := entry.LandingClaimedAt.Get()
	if !ok {
		return "", nil
	}
	records, err := s.store.RunsForRepository(ctx, plan.repository.ID)
	if err != nil {
		return "", err
	}
	for _, r := range records {
		if r.Branch != plan.branch || r.SubmittedHead != plan.head || r.CreatedAt.Before(claimed) {
			continue
		}
		// A reference that cannot be read is a read that failed, never a
		// run that is not the landing's: taking it for no run would start a
		// second run that supersedes the one validating the work.
		switch at, err := bare.ResolveCommit(ctx, "refs/assistant/runs/"+r.ID); {
		case errors.Is(err, vcs.ErrRefNotFound):
			continue
		case err != nil:
			return "", fmt.Errorf("service: reading the submitted reference of run %s in the gate of %s: %w", r.ID, plan.repository.ID, err)
		case at != plan.head:
			continue
		}
		if _, err := s.store.LatestGraphCheckpoint(ctx, r.ID); errors.Is(err, store.ErrNotFound) {
			continue
		} else if err != nil {
			return "", err
		}
		switch r.Status {
		case store.RunPending, store.RunRunning, store.RunHeld, store.RunPassed:
			return r.ID, nil
		case store.RunFailed, store.RunTerminated:
			continue
		default:
			return "", fmt.Errorf("service: run %s of %s is %q, a status this build does not recognize, so the landing of kept work entry %d is not completed by it", r.ID, plan.repository.ID, r.Status, entry.ID)
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
			s.log.Printf("kept work entry %d holds a landing claim that could not be completed: %v", entry.ID, err)
		}
	}
}
