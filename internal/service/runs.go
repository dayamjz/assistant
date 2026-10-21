package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/runs"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// start attaches to the branch's run, or begins one when it has none, and
// blocks while it advances that run, answering at its next decision point or a
// terminal outcome. A run this service is already advancing is reported where
// it stands rather than advanced twice, so this is also the call that answers
// with a run still executing.
//
// Attaching rather than creating a second run is what PRD section 9's bare
// command means by "attach to this branch's active run; with no run, start
// one". A branch with a run that is held is reported as it stands, because a
// held run advances on an answer and not on being asked again. A branch with a
// run that stopped part way through is resumed, which is what makes a run
// survive the process that started it.
func (s *Service) start(ctx context.Context, req machine.StartRequest) (machine.Run, error) {
	repository, err := s.repositoryAt(ctx, req.WorkingPath)
	if err != nil {
		return machine.Run{}, err
	}
	working, err := vcs.OpenWorktree(ctx, req.WorkingPath, vcs.WithRedactor(redact.New()))
	if err != nil {
		return machine.Run{}, err
	}
	branch, err := working.HeadBranch(ctx)
	if err != nil {
		return machine.Run{}, fmt.Errorf("service: reading the branch to validate: %w", err)
	}
	head, err := working.ResolveCommit(ctx, "HEAD")
	if err != nil {
		return machine.Run{}, fmt.Errorf("service: reading the commit to validate: %w", err)
	}
	skip, err := parseStages(req.Skip)
	if err != nil {
		return machine.Run{}, err
	}
	// Standing consent asked for where the repository does not allow it is
	// refused before any run is recorded or touched, so the refusal leaves
	// nothing behind; consent.go owns the rule.
	if req.StandingConsent {
		allowed, err := s.standingConsentAllowed(ctx, repository)
		if err != nil {
			return machine.Run{}, err
		}
		if !allowed {
			return machine.Run{}, standingConsentRefused(repository)
		}
	}
	// Everything the record is built from is read before the branch is
	// claimed, so what the claim holds is the decision and the write - the
	// row, and the one fetch create makes to record the commit in the gate -
	// and not the reads that precede them.
	record, created, err := s.claimBranch(ctx, branchKey{repository: repository.ID, branch: branch}, run{
		repository: repository.ID,
		branch:     branch,
		head:       head,
		base:       baseCommit(ctx, working, head, repository.DefaultBranch),
		intent:     req.Intent,
		source:     intentSource(req),
		supplied:   req.IntentSupplied,
	})
	if err != nil {
		return machine.Run{}, err
	}
	if req.StandingConsent {
		if record, err = s.giveStandingConsent(ctx, record); err != nil {
			return machine.Run{}, err
		}
	}
	if !created {
		view, err := s.attach(ctx, record.ID)
		if err != nil {
			return machine.Run{}, err
		}
		// The branch already had a run, so this call is an attach and the
		// inputs a start would have built a record from went nowhere. The
		// attach is still the right answer - it is what the bare command is
		// for, and it is worth repeating - so they are reported rather than
		// refused or dropped without a word.
		view.NotApplied = startInputs(req)
		return view, nil
	}
	return s.begin(ctx, record, pipeline.Start{
		Branch:         branch,
		Base:           repository.DefaultBranch,
		Submitted:      head,
		Intent:         req.Intent,
		IntentSupplied: req.IntentSupplied,
		Skip:           skip,
	})
}

// startInputs names the run-starting inputs a request carried, by the field of
// machine.StartRequest each arrived on.
//
// It reads what the request holds rather than what a caller says it wrote: the
// wire shape has no way to tell a field that was set from one left at its zero
// value, and the two are the same thing for these three. An intent of
// whitespace is nothing given, which is the reading create already takes of it.
func startInputs(req machine.StartRequest) []string {
	var named []string
	if strings.TrimSpace(req.Intent) != "" {
		named = append(named, "intent")
	}
	if req.IntentSupplied {
		named = append(named, "intent_supplied")
	}
	if len(req.Skip) > 0 {
		named = append(named, "skip")
	}
	return named
}

// rerun starts a fresh run of the branch the caller is standing on, from that
// branch's last known head, which lastKnownHead defines, and inheriting the
// intent recorded there, and blocks while it advances that run, answering at
// its next decision point or a terminal outcome. It never answers with a run still executing, because the
// branch it would report one for is the branch it refuses.
//
// The branch is read from the working copy, exactly as start reads it. A verb
// that reached for the repository's newest run instead would restart a branch
// the caller did not name and is not standing on, and would answer with a
// branch they never mentioned. A branch that has never been run says so about
// that branch rather than borrowing another's.
//
// It refuses while a run of that branch is still active, because a rerun is a
// second run of the same branch and PRD section 8 has runs of one branch
// serialize. Ending the one in flight is a separate act with its own verb.
//
// An intent in the request replaces the inherited one, on the terms
// machine.RerunRequest states: the fresh run records it as supplied, which is
// what the intent stage reads as acceptance criteria. It is the only thing a
// replacement changes, and it is read before anything else so that an intent
// of whitespace alone is refused before the branch is claimed.
func (s *Service) rerun(ctx context.Context, req machine.RerunRequest) (machine.Run, error) {
	if req.Intent != "" && strings.TrimSpace(req.Intent) == "" {
		return machine.Run{}, fmt.Errorf("%w: a rerun's replacement intent is empty; give the intent, or none to inherit the recorded one",
			ipc.ErrInvalidRequest)
	}
	repository, err := s.repositoryAt(ctx, req.WorkingPath)
	if err != nil {
		return machine.Run{}, err
	}
	working, err := vcs.OpenWorktree(ctx, req.WorkingPath, vcs.WithRedactor(redact.New()))
	if err != nil {
		return machine.Run{}, err
	}
	branch, err := working.HeadBranch(ctx)
	if err != nil {
		return machine.Run{}, fmt.Errorf("service: reading the branch to run again: %w", err)
	}
	local, err := working.ResolveCommit(ctx, "HEAD")
	if err != nil {
		return machine.Run{}, fmt.Errorf("service: reading the commit this checkout stands at: %w", err)
	}
	record, err := s.claimRerun(ctx, branchKey{repository: repository.ID, branch: branch}, local, req.Intent)
	var moved rerunMoved
	if errors.As(err, &moved) {
		return machine.Run{}, s.placeMoved(ctx, repository, moved)
	}
	if err != nil {
		return machine.Run{}, err
	}
	return s.begin(ctx, record, pipeline.Start{
		Branch:         record.Branch,
		Base:           repository.DefaultBranch,
		Submitted:      record.SubmittedHead,
		Intent:         record.Intent,
		IntentSupplied: intentCountsAsSupplied(record.IntentSource),
	})
}

// claimRerun establishes, under the branch's exclusion, that the branch has no
// run in flight, and creates the fresh one that carries its last run forward.
//
// It takes the same gate a start takes, so the branch's decision that it has
// no run has one owner rather than one per verb: two reruns at once, and a
// start racing a rerun, are both the interleaving that would otherwise leave
// the branch with two runs. Reading the run being carried forward is inside
// the claim too, because the head and the intent the new record is built from
// have to be the ones that were there when the decision was made.
//
// local is the commit the caller's working copy stood at when the request was
// read. A rerun validates the branch's last known head, which lastKnownHead
// reads, so when local is a different commit the rerun is refused with
// rerunMoved naming both and nothing is created: validating that head would
// re-vet a commit the person has moved on from, or vet work they do not yet
// have, and say nothing about theirs. local is read before the claim, as
// start reads its head, so a commit made after that read and before the claim
// is not seen.
//
// replacement is the intent that replaces the one previous recorded, empty to
// inherit that one. A replacement is recorded as supplied, so the run's
// intent source says a caller stated it for this run rather than carrying the
// previous run's source over text that run never held.
func (s *Service) claimRerun(ctx context.Context, key branchKey, local, replacement string) (store.Run, error) {
	release, err := s.holdBranch(ctx, key)
	if err != nil {
		return store.Run{}, err
	}
	defer release()

	previous, err := s.latestRunOnBranch(ctx, key.repository, key.branch)
	if err != nil {
		return store.Run{}, err
	}
	if _, active, err := s.activeRun(ctx, key.repository, key.branch); err != nil {
		return store.Run{}, err
	} else if active {
		return store.Run{}, fmt.Errorf("service: %s already has a run in flight; end it before starting another", key.branch)
	}
	recorded, err := s.lastKnownHead(ctx, previous)
	if err != nil {
		return store.Run{}, err
	}
	if local != recorded {
		return store.Run{}, rerunMoved{
			moved: machine.RerunMoved{Branch: key.branch, Local: local, Recorded: recorded,
				Reason: machine.RerunMovedElsewhere},
			run:  previous.ID,
			work: recorded != previous.SubmittedHead,
		}
	}
	intent, source := previous.Intent, previous.IntentSource
	if replacement != "" {
		intent, source = replacement, intentSourceSupplied
	}
	return s.create(ctx, run{
		repository: key.repository,
		branch:     key.branch,
		head:       recorded,
		base:       previous.Base,
		intent:     intent,
		source:     source,
		supplied:   intentCountsAsSupplied(source),
	})
}

// lastKnownHead is the commit a rerun of previous's branch validates: what
// previous's pipeline last pushed, and the commit it was submitted with when
// it pushed nothing.
//
// What a run pushed has one owner, its checkpoint position, and this reads it
// through positionOf, the same read internal/custody classifies the branch
// from for assistant sync, so the commit a rerun refuses against and the
// commit sync reconciles the checkout with are one fact read one way. The
// record's CurrentHead is not consulted: nothing in this build writes it, and
// a second record of the push is what P14 forbids.
//
// A run with no checkpoint, or whose checkpoint records no push, pushed
// nothing, so it falls back to the submitted commit. A checkpoint that cannot
// be read is an error rather than that fallback, because validating the
// submitted commit of a run that did push would re-vet what the pipeline had
// already moved past.
func (s *Service) lastKnownHead(ctx context.Context, previous store.Run) (string, error) {
	position, err := s.positionOf(ctx, previous.ID)
	if err != nil {
		return "", err
	}
	if position.Pushed != "" {
		return position.Pushed, nil
	}
	return previous.SubmittedHead, nil
}

// rerunMoved is machine.RerunMoved as this service refuses with it: on the
// wire it is ipc.ErrRefused, because the request was readable and the records
// decided against it, and its message is the refusal's own text with no
// prefix. errors.As still reaches the machine.RerunMoved inside.
type rerunMoved struct {
	moved machine.RerunMoved
	// run is the run whose recorded head the rerun would have validated.
	run string
	// work is whether that head is the run's own work rather than the commit
	// it was submitted: a pushed commit that differs from the submitted one.
	work bool
}

// placeMoved settles which remedy a refused rerun names, and returns the
// refusal.
//
// It is RerunMovedBehind only when the recorded head is the run's own work and
// the gate repository the run was cut from establishes the local commit as
// its ancestor; that is the case assistant sync reconciles. Every other case
// keeps RerunMovedElsewhere, including a gate repository that cannot be
// opened and an ancestry it cannot answer, such as a local commit it does
// not hold, because a remedy chosen on a read that failed would be a guess.
// A local commit behind the commit that was submitted is RerunMovedElsewhere
// too: the head a rerun validates is then the submitted commit rather than
// work the run pushed, so sync has nothing of the run's to bring in. Sync's
// own plan is internal/custody's, which also reads the run's copy and the
// local branch; this names sync on the pushed commit and ancestry alone, and
// sync decides what it does when it is asked.
//
// It reads the gate repository and writes nothing anywhere.
func (s *Service) placeMoved(ctx context.Context, repository store.Repository, moved rerunMoved) error {
	if !moved.work {
		return moved
	}
	bare, err := s.gateRepositoryOf(ctx, repository, moved.run)
	if err != nil {
		return moved
	}
	behind, err := bare.IsAncestor(ctx, moved.moved.Local, moved.moved.Recorded)
	if err == nil && behind {
		moved.moved.Reason = machine.RerunMovedBehind
	}
	return moved
}

// Error is the refusal's own text.
func (r rerunMoved) Error() string { return r.moved.Error() }

// Unwrap resolves to the wire category and to the refusal.
func (r rerunMoved) Unwrap() []error { return []error{ipc.ErrRefused, r.moved} }

// cancelRefused is machine.CancelRefused as this service refuses with it: on
// the wire it is ipc.ErrInvalidRequest, because the caller named a run there
// is nothing to end in, which is theirs to correct and which a command line
// therefore exits from as incorrect usage. Its message is the refusal's own
// text with no prefix, and errors.As still reaches the machine.CancelRefused
// inside.
type cancelRefused struct{ refused machine.CancelRefused }

// Error is the refusal's own text.
func (c cancelRefused) Error() string { return c.refused.Error() }

// Unwrap resolves to the wire category and to the refusal.
func (c cancelRefused) Unwrap() []error { return []error{ipc.ErrInvalidRequest, c.refused} }

// respond answers the decision a run is holding on and blocks until it reaches
// the next one or a terminal outcome.
//
// The record is reconciled against the durable checkpoint first, so a run left
// running by a service that died between the halt and the record is answerable
// rather than stuck: responding is refused for a run that is not held, and the
// checkpoint is what says it is.
func (s *Service) respond(ctx context.Context, req machine.RespondRequest) (machine.Run, error) {
	record, err := s.reconcile(ctx, req.Run)
	if err != nil {
		return machine.Run{}, err
	}
	if record.Status != store.RunHeld {
		return machine.Run{}, fmt.Errorf("service: run %s is %s and is not waiting on an answer", record.ID, record.Status)
	}
	// Before the hold the checks stage raised at checks_timeout elapse is
	// resumed, and only that hold, the pull request is re-read; a positively
	// read merged or closed state closes the hold and ends the run, and the
	// answer given here answers a question that no longer exists. What is
	// returned is the run as it then stands, which says how it ended, and
	// that the answer was not applied. Anything short of a positive read -
	// the pull request not read, the record not readable - leaves the hold
	// to the person and their answer is taken below: checkshold.go says
	// why, and the one error that reaches here is a write refused after a
	// positive read.
	if ended, err := s.closeChecksHoldIfPullRequestEnded(ctx, record); err != nil {
		return machine.Run{}, err
	} else if ended != "" {
		run, err := s.view(ctx, record.ID)
		if err != nil {
			return machine.Run{}, err
		}
		run.AnswerNotApplied = "the pull request had already " + ended.String() +
			" when the hold was re-read, so the hold was closed on that and the run ended as reported"
		return run, nil
	}
	built, err := s.driverFor(ctx)
	if err != nil {
		return machine.Run{}, err
	}
	// The answer resumes the run, so its configuration is resolved again on
	// the same terms attach states.
	resolved, err := s.resolveRunConfig(ctx, record)
	if err != nil {
		return machine.Run{}, err
	}
	topo, err := s.topologyFor(built, resolved)
	if err != nil {
		return machine.Run{}, err
	}
	detail, err := s.fixRequestFor(ctx, record.ID, req)
	if err != nil {
		return machine.Run{}, err
	}
	if _, err := built.runs.Release(ctx, record.ID); err != nil {
		return machine.Run{}, err
	}
	// The held findings' paths are read before the answer moves the run, and
	// recorded as what followed the decision-model answers about them only
	// once the answer is accepted; observe.go says what that record is.
	stage, paths, mark := s.heldPaths(ctx, record.ID)
	answered, err := s.advance(ctx, record.ID, topo, func(ctx context.Context) (graph.Result, error) {
		return topo.executor.AnswerWith(ctx, record.ID, req.Answer, detail)
	})
	if err == nil {
		s.recordAnswered(record.ID, stage, req.Answer, paths, mark)
	}
	// An answer the graph refused because the run is past its usage budget
	// was not applied, and the run is parked on the budget rather than left
	// held behind the refusal; parkHeldPastUsage owns the decision. The
	// answer to this call is then the run as it stands, parked, whose record
	// names the budget and the spend.
	if errors.Is(err, graph.ErrUsageSpent) {
		current, rerr := s.reconcile(ctx, record.ID)
		if rerr != nil {
			return machine.Run{}, errors.Join(err, rerr)
		}
		if view, parked, perr := s.parkHeldPastUsage(ctx, current); parked {
			return view, perr
		}
	}
	return answered, err
}

// fixRequestFor is the detail an answer carries to the graph: the encoded fix
// request when the answer asks for a fix round, and nothing otherwise.
//
// A fix answer is checked against the held stage's recorded report here,
// before anything is recorded, with the same pipeline.FixRequest.Select the
// request node applies, so a request selecting a finding the stage never
// reported is refused to the caller with the decision still open rather than
// failing the run once the round starts. A request given with any other answer
// is refused too: it would otherwise be an instruction nobody acted on.
// internal/graph refuses both shapes again, so this is the early half and not
// the only one.
//
// A fix request that selects nothing, selects a finding the held stage's
// report does not carry, or adds a finding with no description is the
// caller's request being wrong rather than this service failing, so it crosses
// the wire as ipc.ErrInvalidRequest with pipeline's refusal beneath it, which
// names the identifiers the report does carry. The refusals about where the
// run holds are not that and keep their own codes.
func (s *Service) fixRequestFor(ctx context.Context, runID string, req machine.RespondRequest) (string, error) {
	if req.Answer != string(pipeline.OutcomeFixRequested) {
		if req.Fix != nil {
			return "", fmt.Errorf("%w: a fix request travels with the %q answer and run %s was answered %q",
				graph.ErrDetailNotAccepted, pipeline.OutcomeFixRequested, runID, req.Answer)
		}
		return "", nil
	}
	if req.Fix == nil {
		return "", fmt.Errorf("%w: %w: run %s was answered %q with nothing selected to fix",
			ipc.ErrInvalidRequest, pipeline.ErrEmptyFixRequest, runID, req.Answer)
	}
	cp, err := s.checkpoints.Latest(ctx, runID)
	if err != nil {
		return "", fmt.Errorf("service: reading where run %s holds: %w", runID, err)
	}
	stage, ok := heldStage(cp.Position)
	if !ok {
		return "", fmt.Errorf("service: run %s holds at %q, which is no stage's hold", runID, cp.Position)
	}
	if err := pipeline.CheckRequestable(stage); err != nil {
		return "", fmt.Errorf("run %s: %w", runID, err)
	}
	report, err := pipeline.StageReport(cp.State, stage)
	if err != nil {
		return "", err
	}
	if _, err := req.Fix.Select(report); err != nil {
		return "", fmt.Errorf("%w: run %s: %w", ipc.ErrInvalidRequest, runID, err)
	}
	return req.Fix.Encode()
}

// heldStage is the stage whose hold a run stands at.
func heldStage(position string) (pipeline.Stage, bool) {
	for _, stage := range pipeline.Order() {
		if position == stage.HoldNode() {
			return stage, true
		}
	}
	return pipeline.StageInvalid, false
}

// cancel ends a run. It records the ending in the run's slot and cancels the
// context of the segment executing it before it moves the record, so a segment
// that is between cancellation points stops rather than carrying on past a run
// that is over, and this service does not follow that segment with a
// continuation of its own.
//
// That is a signal and not a join. Nothing here waits for the advancing
// goroutine to leave the node it is in and give its slot back, so the record
// can reach terminated while a node body is still returning, and the wider it
// is between cancellation points the longer that window is. It is not
// observable while every stage body returns at once, and it becomes real with
// the first body that does work - an agent process, per PRD section 8's
// process lifetime rule.
//
// What the window cannot do is rewrite the run's position: internal/graph
// anchors every checkpoint to the one the segment read, and the record's own
// move is what a later reader reconciles against. Making the sentence a join
// is a change to what cancel costs a caller, which is its own decision rather
// than this one.
//
// What the slot does close is the other half: the segment this cancels is
// never followed by a continuation. endRun records the ending where carryOn
// reads it, under the mutex the slot is taken and given back under. What that
// bounds on each of endRun's two branches, and what it leaves open, is written
// down there and nowhere else.
//
// A run with nothing to end is refused with machine.CancelRefused before any
// of that: an identifier no record carries, and a run whose record already
// says it ended, which runs.Finished decides rather than a list kept here.
// The record is read before the move, so a run that ends between the read and
// the move is not that refusal on the read's evidence. The move then settles
// it: internal/runs refuses to terminate a run that passed or failed, and that
// refusal is reported as the run having ended too. A run something else
// terminated in that window is the one case the move does not refuse, because
// a terminate is safe to repeat, so it is answered as the ended run it now is
// rather than refused; the run the caller asked to end has ended either way.
func (s *Service) cancel(ctx context.Context, req machine.CancelRequest) (machine.Run, error) {
	record, err := s.store.Run(ctx, req.Run)
	if errors.Is(err, store.ErrNotFound) {
		return machine.Run{}, cancelRefused{machine.CancelRefused{Run: req.Run, Reason: machine.CancelUnknownRun}}
	}
	if err != nil {
		return machine.Run{}, err
	}
	if runs.Finished(record.Status) {
		return machine.Run{}, cancelRefused{machine.CancelRefused{Run: req.Run, Reason: machine.CancelRunEnded, Status: record.Status}}
	}
	built, err := s.driverFor(ctx)
	if err != nil {
		return machine.Run{}, err
	}
	forget := s.endRun(req.Run)
	defer forget()
	terminated, err := built.runs.Terminate(ctx, req.Run)
	if err != nil {
		var moved *store.RunStatusError
		if errors.As(err, &moved) && runs.Finished(moved.Actual) {
			return machine.Run{}, cancelRefused{machine.CancelRefused{Run: req.Run, Reason: machine.CancelRunEnded, Status: moved.Actual}}
		}
		return machine.Run{}, err
	}
	// The run finished, so its copy is owed the cleanup decision now, as
	// settle owes it, rather than at the next start: until it is decided it
	// is a copy eject has to refuse over, and one holding unlanded work is
	// kept work nobody has been told about. A run a segment is still inside
	// is left to that segment, which reclaims once it returns, through
	// settle or, when it returns without settling, through carryOn, so a
	// copy is never examined under a stage body still writing to it.
	if !s.isAdvancing(req.Run) {
		s.reclaimed(ctx, terminated)
	}
	// The run finished, so a task queued behind the one it was attributed to
	// may have cleared; tasks.go names this among the sites that release.
	s.releaseQueued(ctx)
	return s.view(ctx, req.Run)
}

// attach reports a run as it stands, resuming it when it is one this service
// stopped part way through.
func (s *Service) attach(ctx context.Context, runID string) (machine.Run, error) {
	settled, err := s.reconcile(ctx, runID)
	if err != nil {
		return machine.Run{}, err
	}
	// A held run past its usage budget is parked rather than reported held or
	// answered under consent, whose answer the graph would refuse;
	// parkHeldPastUsage owns the decision and says why.
	if settled.Status == store.RunHeld {
		if view, parked, err := s.parkHeldPastUsage(ctx, settled); parked {
			return view, err
		}
	}
	if settled.Status == store.RunHeld && settled.StandingConsent.IsKnown() {
		return s.answerHeldUnderConsent(ctx, settled)
	}
	if settled.Status != store.RunRunning && settled.Status != store.RunPending {
		return s.view(ctx, settled.ID)
	}
	if _, err := s.checkpoints.Latest(ctx, settled.ID); err != nil {
		if !errors.Is(err, graph.ErrNoSuchRun) {
			// The run has a position and it could not be read. Reporting it as
			// a run that never executed would offer a caller the answer for
			// one, which is to end it and start again, and that answer would
			// throw away work the checkpoint history still holds.
			return machine.Run{}, fmt.Errorf("service: reading the position of run %s: %w", settled.ID, err)
		}
		// A run with no checkpoint never executed a node, so there is no
		// position to resume from. Its inputs are on its record, but the
		// stages it was told to skip are not, and starting it again from the
		// record would silently drop that choice. It is reported instead, and
		// the report says what to do about it.
		return s.view(ctx, settled.ID)
	}
	built, err := s.driverFor(ctx)
	if err != nil {
		return machine.Run{}, err
	}
	// A resumed run's configuration is resolved again, on the terms layers.go
	// states: the trusted copy is read at a fresh fetch, per P7, and the
	// pushed copy at the run's own submitted commit is unchanged by
	// construction. A resolution whose bounds moved hands the run a different
	// topology, and internal/graph's identity check is then what decides
	// whether the position can be resumed under it, rather than this package
	// quietly resuming a run under bounds it did not run under.
	resolved, err := s.resolveRunConfig(ctx, settled)
	if err != nil {
		return machine.Run{}, err
	}
	topo, err := s.topologyFor(built, resolved)
	if err != nil {
		return machine.Run{}, err
	}
	if settled.Status == store.RunPending {
		if _, err := built.runs.Start(ctx, settled.ID); err != nil {
			return machine.Run{}, err
		}
	}
	// A run this service is already advancing is reported rather than refused:
	// attaching asks where a run stands, and "it is moving" is an answer to
	// that. Taking the slot and reporting the refusal is what makes this a
	// decision rather than a check that another caller can win the race to.
	view, err := s.advance(ctx, settled.ID, topo, func(ctx context.Context) (graph.Result, error) {
		return topo.executor.Resume(ctx, settled.ID)
	})
	if errors.Is(err, ErrRunAdvancing) || errors.Is(err, ErrRunEnding) {
		return s.view(ctx, settled.ID)
	}
	return view, err
}

// begin records the run as started and walks it from its initial state.
//
// The run's configuration is resolved between the copy and the record's move
// to running, and that placement is PRD section 10's ordering: the pushed
// document is read at the submitted commit, which the copy's build is what
// fetches into the gate repository, and a trusted copy that cannot be read
// stops the run here, before anything is recorded as running and before any
// stage body or agent launches.
//
// Both refusals begin makes after the run is claimed, the code host's and the
// configuration's, end through endRefused, which ends the run's record as
// terminated so the branch's next start is a fresh run, and releases the
// tasks queued behind it. When the record cannot be ended the caller gets a
// *RunNotEndedError rather than the refusal alone, so a run left unfinished
// is never reported as a clean refusal. Other failures here (recording the
// code host, building the copy, recording the resolution) return as they
// stand and leave the pending row.
func (s *Service) begin(ctx context.Context, record store.Run, start pipeline.Start) (machine.Run, error) {
	built, err := s.driverFor(ctx)
	if err != nil {
		return machine.Run{}, err
	}
	// Which repository and which run are taken from the record rather than
	// from the caller's start, so the two facts a stage body locates its
	// isolated copy from cannot disagree with the row that copy is reclaimed
	// against, and no call site can forget them.
	start.Repository = record.RepositoryID
	start.Run = record.ID
	// Which repository on the code host is settled here for the same reason
	// and from the same record. A pull request is outward-facing and not
	// undoable, so where it lands may not come from a caller's start, from
	// configuration, or from whatever remote a working copy happens to carry:
	// it is derived from the upstream this run's repository row records, which
	// is the same row PRD section 8 makes authoritative for it.
	forgeRepository, forkRepository, noCodeHost, err := s.forgeRepositoryFor(ctx, record.RepositoryID)
	if err != nil {
		// The branch was already claimed as pending (start, rerun, or a push).
		// Ending it here is what keeps an unsupported-host refusal from leaving
		// an unfinished run that a later start would attach to as executing.
		// create does the same when the submitted commit cannot be recorded.
		return machine.Run{}, s.endRefused(ctx, built, record.ID, err)
	}
	// Persist the trichotomy on the run row so listings that have only the
	// record still phrase a completed no-code-host run as pushed.
	if noCodeHost {
		if err := s.store.SetRunNoCodeHost(ctx, record.ID); err != nil {
			return machine.Run{}, err
		}
	} else {
		if err := s.store.SetRunHasCodeHost(ctx, record.ID); err != nil {
			return machine.Run{}, err
		}
	}
	start.ForgeRepository, start.ForkRepository, start.NoCodeHost = forgeRepository, forkRepository, noCodeHost
	// The copy is built before the run is recorded as started, so a run that
	// is walking has somewhere to walk. PRD section 8 orders the row before
	// the directory and create wrote the row, so this is the second half of
	// that ordering rather than a departure from it: a directory with no row
	// is what the rule forbids, and a row whose directory could not be built
	// leaves a run that never started.
	if err := s.ensureCopy(ctx, record); err != nil {
		return machine.Run{}, err
	}
	// The subject is settled here, from the record, for the reason the code
	// host's repository is: every way a run starts comes through this, so a
	// push, a start, and a rerun all carry one and no call site can forget it.
	// The gate repository holds the submitted commit whichever way the run
	// started, because create recorded it there under the run's own
	// reference before this was called.
	start.SubmittedSubject = s.submittedSubject(ctx, record)
	resolved, err := s.resolveRunConfig(ctx, record)
	if err != nil {
		// A refused resolution ends the run on the code-host refusal's terms
		// above: the run has no checkpoint, so nothing could ever resume it,
		// and left pending it is an unfinished run a later start on the
		// branch would attach to and report as executing, rather than one a
		// fresh start replaces once the document is fixed.
		return machine.Run{}, s.endRefused(ctx, built, record.ID, err)
	}
	// The record catches up to the run's own resolution, so a surprising
	// verdict traces to the three documents the run was actually given rather
	// than to the operator-layer placeholder create wrote. The keys that
	// resolution rejected ride the same write, and the resolved agent's name
	// is recorded beside them with every entry the resolution passed over
	// and why, so those facts reach the surfaces that report the record
	// rather than only this service's log. A run refused before this point
	// carries none of them: the record understates.
	if err := s.store.SetRunConfigResolution(ctx, record.ID, resolved.digest, resolved.rejected); err != nil {
		return machine.Run{}, err
	}
	if err := s.store.SetRunAgentResolution(ctx, record.ID, built.agent.Name, skippedAgents(built.agent.Skipped)); err != nil {
		return machine.Run{}, err
	}
	topo, err := s.topologyFor(built, resolved)
	if err != nil {
		return machine.Run{}, err
	}
	initial, err := topo.pipeline.NewState(start)
	if err != nil {
		return machine.Run{}, err
	}
	if _, err := built.runs.Start(ctx, record.ID); err != nil {
		return machine.Run{}, err
	}
	return s.advance(ctx, record.ID, topo, func(ctx context.Context) (graph.Result, error) {
		return topo.executor.Run(ctx, record.ID, initial)
	})
}

// endRefused is the one ending of a run begin refused after the run was
// claimed. It ends the record as terminated, and only once that move is
// made does it release the tasks queued behind the run and publish the
// run's state, so a successor queued behind the task the run was attributed
// to is released without a restart; tasks.go names this among the sites
// that release. The ending is made under a context that outlives the
// caller's cancellation, because a refusal returned to a caller that went
// away still leaves the row to end.
//
// It answers refusal when the run was ended, and a *RunNotEndedError
// carrying both when the move was refused or failed, in which case nothing
// is released and the row stays in whatever status the store read. That
// answer does not cross the wire as refused even when the refusal would
// have: a refused code tells a caller to fix what was decided against and
// start again, and that start would attach to the run left unfinished, so
// the refusal it carries is unclassifiedRefusal's and the code is whatever
// the ending's failure is, internal when that names none.
func (s *Service) endRefused(ctx context.Context, built *driver, runID string, refusal error) error {
	if s.beforeRefusalEnd != nil {
		s.beforeRefusalEnd()
	}
	endCtx := context.WithoutCancel(ctx)
	if _, err := built.runs.Terminate(endCtx, runID); err != nil {
		return &RunNotEndedError{Run: runID, Refusal: unclassifiedRefusal(refusal), End: err}
	}
	s.releaseQueued(endCtx)
	s.publishRunState(endCtx, runID)
	return refusal
}

// RunNotEndedError is begin's answer when it refused a run and then could
// not end the run's record: Refusal is why the run was refused, and End is
// why the move to terminated did not happen. The run is left unfinished, so
// a later start on its branch may attach to it rather than replace it, and
// no task queued behind it was released by this refusal. It matches both
// errors through errors.Is and errors.As.
type RunNotEndedError struct {
	Run     string
	Refusal error
	End     error
}

func (e *RunNotEndedError) Error() string {
	return fmt.Sprintf("service: run %s was refused and could not be ended, so it is left unfinished: %v; ending it: %v",
		e.Run, e.Refusal, e.End)
}

// Unwrap returns the refusal and the ending's failure, in that order.
func (e *RunNotEndedError) Unwrap() []error { return []error{e.Refusal, e.End} }

// forgeRepositoryFor returns the specifier the code host addresses this
// repository by, the fork's specifier, whether the run has no code host, and
// an error when the run must not start, all read from its record's upstream
// URL through codeHostOf, which owns the decision.
func (s *Service) forgeRepositoryFor(ctx context.Context, repositoryID string) (string, string, bool, error) {
	repository, err := s.store.Repository(ctx, repositoryID)
	if err != nil {
		return "", "", false, fmt.Errorf("service: reading the repository a run acts on: %w", err)
	}
	host, err := codeHostOf(repository)
	if err != nil {
		return "", "", false, err
	}
	return host.repository, host.fork, host.none, nil
}

// codeHost is where a run of one repository proposes its change, as
// codeHostOf decided it from the repository's record.
type codeHost struct {
	// repository is the specifier the code host addresses the upstream by,
	// empty when none is true.
	repository string
	// fork is the fork's specifier for a record that names a fork, and empty
	// otherwise.
	fork string
	// none is true for a repository with no code host: a local path or
	// file:// upstream, whose run ends pushed.
	none bool
}

// codeHostOf is the one decision of where a run of repository proposes its
// change. forgeRepositoryFor makes it at run start and taskView states it on
// a delivery task's answer, so the two cannot disagree. An error is a
// repository no run can start against.
//
// forge.RepositoryOf decides the three-way split issue #283 settled: a
// repository on a host this build addresses (github.com or gitlab.com), a
// local path or file:// remote with no code host, or a network host this
// build cannot talk to. The first yields a specifier, which names its host
// where the host is gitlab.com, so the service's forge.Hosts opens it on the
// adapter it belongs to. The second yields an empty specifier and none true,
// so the pull request and checks stages skip after push and the run ends
// pushed. The third is refused with ErrUnsupportedHost; begin ends the
// pending record through endRefused before that refusal returns, or answers
// *RunNotEndedError when the record cannot be ended.
//
// A fork is the strict case: a run that names one pushes there and proposes
// from there, so a record naming a fork that does not resolve, or one on
// another host than its upstream, or naming one while its upstream does not
// resolve, or naming the upstream itself as its fork, is refused with
// ErrForkUnresolved before anything is recorded as running. Pushing to the
// upstream instead, or to the fork with no pull request to propose, would
// each be a guess about where the run's work goes. assistant init refuses all
// four before it records a fork, so this is the record being read as it is
// rather than a second place the rule is decided.
func codeHostOf(repository store.Repository) (codeHost, error) {
	specifier, kind, upstreamResolved := forge.RepositoryOf(repository.UpstreamURL)
	forkURL, named := repository.ForkURL.Get()
	if !named {
		switch {
		case upstreamResolved:
			return codeHost{repository: specifier}, nil
		case kind == forge.RemoteLocal:
			return codeHost{none: true}, nil
		default:
			return codeHost{}, fmt.Errorf("%w: repository %s upstream %q",
				ErrUnsupportedHost, repository.ID, repository.UpstreamURL)
		}
	}
	fork, forkKind, forkResolved := forge.RepositoryOf(forkURL)
	switch {
	case !upstreamResolved:
		return codeHost{}, fmt.Errorf("%w: repository %s names a fork and an upstream that is not a repository on a host "+
			"this build addresses, so there is nothing to propose its pull request to", ErrForkUnresolved, repository.ID)
	case !forkResolved:
		return codeHost{}, fmt.Errorf("%w: repository %s names a fork that is not a repository on a host this build addresses",
			ErrForkUnresolved, repository.ID)
	case forkKind != kind:
		return codeHost{}, fmt.Errorf("%w: repository %s names a fork on %s and an upstream on %s, and a pull request "+
			"is proposed between two repositories on one host", ErrForkUnresolved, repository.ID, forkKind, kind)
	case strings.EqualFold(fork, specifier):
		return codeHost{}, fmt.Errorf("%w: repository %s names its upstream as its fork", ErrForkUnresolved, repository.ID)
	}
	return codeHost{repository: specifier, fork: fork}, nil
}

// submittedSubject is the subject of the commit a run was submitted, read from
// the gate repository through internal/vcs and passed through internal/redact,
// because it reaches graph state, every checkpoint, and through the pull
// request stage's title a code host. It becomes pipeline.KeySubmittedSubject,
// and that stage titles a run without intent with it.
//
// A subject that cannot be read is logged and answered as empty, so a run
// without intent is titled with its branch, as every such run was before the
// subject was read. That is the fallback issue #217 settles on for an empty
// subject rather than a refusal relaxed: a title is the one thing the subject
// decides, and a run that could not read it validates the same commit either
// way. The read
// failing at all is unexpected, since create recorded the commit in the gate
// repository before begin was called.
func (s *Service) submittedSubject(ctx context.Context, record store.Run) string {
	repository, err := s.store.Repository(ctx, record.RepositoryID)
	if err != nil {
		s.log.Printf("run %s: the repository record could not be read, so the submitted commit's subject "+
			"is not recorded and its pull request is titled without it: %v", record.ID, err)
		return ""
	}
	bare, err := s.gateRepositoryOf(ctx, repository, record.ID)
	if err == nil {
		var subject string
		if subject, err = bare.CommitSubject(ctx, record.SubmittedHead); err == nil {
			return redact.New().Redact(subject)
		}
	}
	s.log.Printf("run %s: the subject of %s could not be read, so its pull request is titled without it: %v",
		record.ID, record.SubmittedHead, err)
	return ""
}

// run is what a new run is recorded from. It is a struct because the six
// facts are all strings and a positional call of six of those is one a caller
// can get subtly wrong without the compiler noticing.
type run struct {
	repository string
	branch     string
	// head is the commit under validation, which P1 makes the consent
	// boundary.
	head string
	// base is the commit the change is measured against, empty when it could
	// not be established. It is a commit and not a branch name: a branch moves
	// and a record that named one would stop describing what the run measured.
	base     string
	intent   string
	source   string
	supplied bool
	// heldByGate is whether the gate repository already holds head, which is
	// true of a commit a push landed there and false of one read off a
	// working copy, which create then fetches from that working copy. It is
	// the one place the two ways a run starts are told apart; copy.go says
	// why that is here and not where the copy is cut.
	heldByGate bool
}

// create records a new run. PRD section 8 requires the row to precede the run's
// directory, and this writes the row: begin builds the directory afterwards,
// which is the ordering that rule asks for. The run's reference in the gate
// repository is written here too, after the row, so the record precedes the
// reference it names on the same terms; a reference that cannot be written
// leaves no run standing, because the row is moved to terminated before the
// failure is reported, and a caller that starts again creates a fresh one.
func (s *Service) create(ctx context.Context, r run) (store.Run, error) {
	built, err := s.driverFor(ctx)
	if err != nil {
		return store.Run{}, err
	}
	if r.supplied && strings.TrimSpace(r.intent) == "" {
		return store.Run{}, errors.New("service: a run cannot claim a supplied intent with nothing behind it")
	}
	id, err := newRunID()
	if err != nil {
		return store.Run{}, err
	}
	record, err := built.runs.Create(ctx, store.Run{
		ID:            id,
		RepositoryID:  r.repository,
		Branch:        r.branch,
		SubmittedHead: r.head,
		Base:          r.base,
		Intent:        r.intent,
		IntentSource:  r.source,
		Build:         s.build,
		ConfigDigest:  s.digest,
	})
	if err != nil {
		return store.Run{}, err
	}
	if err := s.recordSubmittedCommit(ctx, record, r.heldByGate); err != nil {
		if _, ended := built.runs.Terminate(ctx, record.ID); ended != nil {
			s.log.Printf("run %s could not be ended after its commit could not be recorded in the gate: %v", record.ID, ended)
		}
		return store.Run{}, err
	}
	return record, nil
}

// baseCommit is the commit the change is measured against: where this branch
// and the default branch last agreed.
//
// It reads what the working copy already holds and fetches nothing, so a
// default branch this copy has never seen leaves the base unrecorded. That is
// the honest answer rather than the head of something else: internal/store
// takes an empty base, and a base nobody established must not be filled in
// with a commit that is not one.
func baseCommit(ctx context.Context, working *vcs.Repository, head, defaultBranch string) string {
	if defaultBranch == "" {
		return ""
	}
	base, err := working.MergeBase(ctx, head, defaultBranch)
	if err != nil {
		return ""
	}
	return base
}

// advance runs one segment of a run and reports where it stopped.
//
// One run advances in one place at a time. The slot is taken before the
// segment starts and released after the record has been reconciled, so a
// caller that reads an answer and immediately asks again finds the record
// already settled rather than the run still moving.
//
// topo is the topology the segment executes. The slot holds its graph for as
// long as it stands, so a read can ask the run's counters what they say about
// a stage; activity says why that is the slot's to hold.
func (s *Service) advance(ctx context.Context, runID string, topo *topology, step func(context.Context) (graph.Result, error)) (machine.Run, error) {
	// The segment carries which run it advances, so the containment wrapper
	// around the resolved agent can register an invocation under the run that
	// launched it without any stage body carrying the fact there.
	segment, cancel := context.WithCancel(withAdvancing(context.WithoutCancel(ctx), runID))
	if err := s.claim(runID, cancel, topo.pipeline.Graph()); err != nil {
		cancel()
		return machine.Run{}, err
	}
	// Registered before the release below and so run after it: carrying the
	// run on takes the same slot, and a slot this call had not given back yet
	// would refuse it. The release is what reads the ending out of the slot,
	// so how is complete by the time carryOn is given it.
	var how ending
	defer func() { s.carryOn(runID, how) }()
	defer func() {
		how.byContext = segment.Err() != nil
		how.byProtocol = s.release(runID)
	}()
	// The caller's context ends the segment as well as the service's, so a
	// node in flight stops rather than running on for a client that is gone.
	// It bounds the segment and not the run: a run is durable and outlives the
	// process that asked for it, and what happens to one whose caller left is
	// carryOn's. It is derived rather than used directly so that a cancel
	// through the protocol ends it too.
	stop := context.AfterFunc(ctx, cancel)
	defer stop()

	result, err := step(segment)
	if err == nil {
		// A decision standing consent answers is answered here, in this
		// segment and this slot, so the run is not recorded held at a
		// decision the program is about to answer; consent.go owns what it
		// answers and what it never does.
		result, err = s.underStandingConsent(segment, runID, topo, result)
	}
	if err != nil {
		s.log.Printf("run %s stopped: %v", runID, err)
		how.stranded = true
		return machine.Run{}, err
	}
	// Recording where the segment stopped runs on a context the caller's
	// departure does not end. The result is already in hand, and a write
	// abandoned here is what leaves the record and the position disagreeing -
	// the run nobody can answer this file exists to stop. A failure to record
	// it is still a segment that settled nothing, so it strands the run the
	// same way a failed step does.
	if err := s.settle(context.WithoutCancel(ctx), runID, result); err != nil {
		how.stranded = true
		return machine.Run{}, err
	}
	return s.report(ctx, runID, &result)
}

// ending is what advance knows about how a segment ended, and it is all
// carryOn decides from. Each field is a fact advance held rather than a
// reading of the error a step returned: an error that merely wraps a context
// one says nothing about which context ended, or whether one did.
type ending struct {
	// stranded is whether the segment returned leaving the record unsettled,
	// which is the only shape carryOn has anything to do about.
	stranded bool
	// byContext is whether a context ended the segment, read off the segment
	// context itself.
	byContext bool
	// byProtocol is whether the run was ended through the protocol while this
	// segment held the run's slot. It is written by endRun and read by release
	// under the one mutex, which is what orders an ending against the segment
	// it ends.
	byProtocol bool
}

// carryOn continues a run whose segment ended leaving nobody to answer for it.
//
// A segment that returns an error settles nothing, so the run is left recorded
// running at the position its checkpoint holds - which is a run something can
// resume, and a run nothing is resuming. Leaving one of those is the stall
// that looks alive, and PRD section 9 makes that worse than an error. This is
// the half of the answer that stops the state existing; report consults the
// slot for the windows that remain, because a service killed mid-segment
// leaves the same shape from a process that runs no more code.
//
// What it continues is decided by what ended the segment, and the answers are
// different because the runs are. A context ended it means nothing about the
// run went wrong and the caller who would have been told is gone, so the
// service picks it up. A step that failed is a failure the caller was told
// about, in the error that call returned, and repeating it is a loop rather
// than progress: that run stands where it is, and the answer a read gives it
// says what carries it on.
//
// Two context endings carry nothing. A run ended through the protocol is one a
// caller has been told is over, so this service does not pick it up again; the
// fact is read out of the slot rather than out of the record, because the
// record is written on another goroutine and nothing orders that write against
// this. The service stopping is the other: recovery reconciles and continues
// every unfinished run on the next open, which is where a run interrupted by a
// shutdown is picked up, and starting work here would be starting work the
// service is in the middle of giving up.
//
// A continuation cannot cause another. It runs under the service's own context
// through continueRun, so the only contexts that can end its segment are the
// service stopping and an ending through the protocol, and both are refused
// here. That is a bound on the shape of the thing rather than a counter
// somebody has to keep.
func (s *Service) carryOn(runID string, how ending) {
	// A segment that returned without settling settled no reclaim either, so
	// a run its ending left finished, a cancel while a stage body was inside
	// it among them, has its copy asked of the cleanup decision here, from the
	// recorded status: cancel leaves such a run's reclaim to its segment, and
	// this is that segment's half on every path it can end by.
	if how.stranded && s.stopCtx.Err() == nil {
		s.reclaimIfFinished(runID)
	}
	switch how.disposition(s.stopCtx.Err() != nil) {
	case dispositionEnded:
		s.log.Printf("run %s was ended through the protocol; nothing carries it on", runID)
	case dispositionRecovered:
		s.log.Printf("run %s was interrupted by this service stopping; recovery will classify it", runID)
	case dispositionContinued:
		s.log.Printf("run %s lost the caller waiting on it; carrying it on", runID)
		s.continueRun(runID)
	}
}

// reclaimIfFinished asks reclaimed of the run when its record says it is
// finished, and of nothing else, reading the status through runs.Finished on
// sweepFinishedAtRecovery's terms.
func (s *Service) reclaimIfFinished(runID string) {
	record, err := s.store.Run(s.stopCtx, runID)
	if err != nil {
		s.log.Printf("the copy of run %s was not reclaimed after its segment ended: the record could not be read: %v", runID, err)
		return
	}
	if !record.Status.Recognized() || !runs.Finished(record.Status) {
		return
	}
	s.reclaimed(s.stopCtx, record)
}

// disposition is what becomes of a run whose segment has ended, and the closed
// set of answers carryOn acts on.
type disposition string

const (
	// dispositionSettled is an ending that left no run standing, because the
	// segment recorded where it stopped before it returned.
	dispositionSettled disposition = "settled"
	// dispositionReported is a run left standing by a step that failed. The
	// caller was told, in the error that call returned, and taking the same
	// step again would be a loop rather than progress.
	dispositionReported disposition = "reported"
	// dispositionEnded is a run left standing by an ending through the
	// protocol. A caller has been told it is over, so this service does not
	// pick it up.
	dispositionEnded disposition = "ended"
	// dispositionRecovered is a run left standing by this service giving up
	// the home, which recovery continues on the next open.
	dispositionRecovered disposition = "recovered"
	// dispositionContinued is a run left standing with nobody to answer for
	// it, which this service picks up itself.
	dispositionContinued disposition = "continued"
)

// disposition weighs an ending against whether this service is stopping.
//
// It is separate from carryOn so that the decision can be driven directly.
// The endings it tells apart are signalled from other goroutines within
// microseconds of each other - a caller giving up, an ending through the
// protocol, the service stopping - so a test that tried to produce a chosen
// one of them by timing would be a race it cannot be relied on to win.
func (e ending) disposition(stopping bool) disposition {
	switch {
	case !e.stranded:
		return dispositionSettled
	case !e.byContext:
		return dispositionReported
	case e.byProtocol:
		return dispositionEnded
	case stopping:
		return dispositionRecovered
	}
	return dispositionContinued
}

// settle moves the run's record to match where its execution stopped.
//
// Every move is one of internal/runs' rows, and a move refused because the run
// is somewhere else is not an error here: the record is authoritative, and
// this is reconciliation rather than a decision about what the run may do.
func (s *Service) settle(ctx context.Context, runID string, result graph.Result) error {
	built, err := s.driverFor(ctx)
	if err != nil {
		return err
	}
	var move func(context.Context, string) (store.Run, error)
	checksPassed := false
	switch {
	case result.Status == graph.StatusHalted:
		move = built.runs.Hold
	case result.Status == graph.StatusCompleted && pipeline.Cancelled(result.State):
		move = built.runs.Terminate
	case result.Status == graph.StatusCompleted:
		// A completed run passed, unless the checks stage recorded that the
		// pull request's own state ended its wait: PRD section 5 has a merge
		// complete the run without validating it and a close fail it. The
		// stage wrote what it read; which move that is, is decided here and
		// nowhere else, and a word this build cannot read is refused rather
		// than read as either. The move writes PRD section 8's merge record
		// from what the stage read and the number the pull request stage
		// recorded, which is the one record of the ending.
		endText, head := pipeline.PullRequestEnd(result.State)
		end := store.PullRequestEnd(endText)
		if end == "" {
			move, checksPassed = built.runs.Pass, true
			break
		}
		_, number, err := recordedPullRequest(result.State)
		if err != nil {
			return fmt.Errorf("service: run %s completed on its pull request's state: %w", runID, err)
		}
		read := store.PullRequestRead{PullRequest: number, Head: head, End: end}
		switch end {
		case store.PullRequestMerged:
			move = func(ctx context.Context, id string) (store.Run, error) {
				return built.runs.PassOnMerge(ctx, id, read, store.HoldClosure{})
			}
		case store.PullRequestClosed:
			move = func(ctx context.Context, id string) (store.Run, error) {
				return built.runs.FailOnClose(ctx, id, read, store.HoldClosure{})
			}
		default:
			return fmt.Errorf("service: run %s completed with %w: %q", runID, store.ErrUnknownPullRequestEnd, end)
		}
	case result.Status.Parked():
		// A park ends the run's record with the bound that parked it written
		// beside the status, read off the result this segment or the
		// checkpoint returned rather than inferred from where the run
		// stands, so a park and a cancellation never read alike on the
		// record.
		park, err := parkRecord(result)
		if err != nil {
			return fmt.Errorf("service: run %s parked: %w", runID, err)
		}
		s.log.Printf("run %s parked: %s", runID, describePark(park, result.Reason))
		move = func(ctx context.Context, id string) (store.Run, error) {
			return built.runs.Park(ctx, id, park)
		}
	case result.Status.Stopped():
		move = built.runs.Terminate
	default:
		return nil
	}
	moved, err := move(ctx, runID)
	if err != nil {
		var wrong *store.RunStatusError
		if !errors.As(err, &wrong) {
			return err
		}
		s.log.Printf("run %s was %s rather than where this segment left it", runID, wrong.Actual)
	} else if runs.Finished(moved.Status) {
		// A held run keeps its copy, because answering the hold resumes the
		// run in it. Whether this status is one no move leads out of is
		// internal/runs' fact and is asked there rather than decided from
		// which move was chosen above.
		s.reclaimed(ctx, moved)
		// A run that finished may have cleared a task queued behind the one
		// it was attributed to; tasks.go says what the release does about
		// that and what it reports. A run its pull request's merge ended
		// has landed the task it was attributed to, whose copy cleanup.go
		// decides about first.
		s.cleanUpLanded(ctx, moved)
		s.releaseQueued(ctx)
		// A run that ended checks-passed is merged here when its repository
		// is one autonomy.merge lists; autonomy.go owns the rule and every
		// refusal, which is reported and never retried around.
		if facts, ok := passedPullRequestOf(result.State); ok && checksPassed && moved.Status == store.RunPassed {
			if err := s.mergeUnderAutonomy(ctx, built, moved, facts); err != nil {
				s.log.Printf("%v", err)
			}
		}
	}
	s.publishRunState(ctx, runID)
	return nil
}

// parkRecord is the record of which bound parked a run, read off the result
// that parked it: the bound is the result's status, the stage is
// internal/pipeline's for the edge the checkpoint recorded or, without one,
// for the node the run stands at, and the checkpoint is the one the result
// was read from. It refuses a result that is not a park, and a round limit or
// convergence park whose node belongs to no stage, rather than recording a
// park nobody can act on.
func parkRecord(result graph.Result) (store.RunPark, error) {
	park, ok := result.Park()
	if !ok {
		return store.RunPark{}, fmt.Errorf("a run that is %s was not parked", result.Status)
	}
	record := store.RunPark{Node: park.Node, Checkpoint: result.Checkpoint.Seq}
	if stage, ok := pipeline.ParkedStage(park); ok {
		record.Stage = stage.String()
	}
	switch park.Status {
	case graph.StatusRoundsExhausted:
		record.Bound = store.ParkRoundLimit
		if park.Edge != nil {
			record.Limit = park.Edge.Rounds
		}
	case graph.StatusConverged:
		record.Bound = store.ParkConvergence
	case graph.StatusBudgetExhausted:
		record.Bound = store.ParkStepBudget
		record.Limit = park.Budget
	case graph.StatusUsageExhausted:
		record.Bound = store.ParkUsageBudget
		record.UsageBudget = park.UsageBudget
		if park.Usage != nil {
			record.Spent = park.Usage.Spent
			record.Unreported = park.Usage.Unreported
		}
	}
	if err := record.Validate(); err != nil {
		return store.RunPark{}, err
	}
	return record, nil
}

// describePark is the service log's line for a park: the bound, the stage and
// node it names, and the reason the checkpoint carries.
func describePark(p store.RunPark, reason string) string {
	line := fmt.Sprintf("%s at stage %q, node %q", p.Bound, p.Stage, p.Node)
	if p.Limit > 0 {
		line += fmt.Sprintf(", limit %d", p.Limit)
	}
	if p.UsageBudget > 0 {
		line += fmt.Sprintf(", %d spent and %d unreported against a usage budget of %d", p.Spent, p.Unreported, p.UsageBudget)
	}
	if reason != "" {
		line += ": " + reason
	}
	return line
}

// reconcile moves a run's record to match its durable checkpoint, and returns
// the record as it then stands.
//
// It is what a restart owes a run and what every operation on a run does
// first. A record that says running and a checkpoint that says halted is a run
// nobody can answer, because responding is refused for a run that is not held.
func (s *Service) reconcile(ctx context.Context, runID string) (store.Run, error) {
	record, err := s.store.Run(ctx, runID)
	if err != nil {
		return store.Run{}, err
	}
	if record.Status != store.RunRunning && record.Status != store.RunPending {
		return record, nil
	}
	latest, err := s.checkpoints.Latest(ctx, runID)
	if err != nil {
		if !errors.Is(err, graph.ErrNoSuchRun) {
			// Reconciling is what makes a run answerable, so a position that
			// cannot be read is refused rather than read as an absent one: the
			// record would otherwise be left saying running against a
			// checkpoint nobody looked at, which is the state this exists to
			// remove.
			return store.Run{}, fmt.Errorf("service: reading the position of run %s: %w", runID, err)
		}
		// A run with no checkpoint has not executed, which is exactly what a
		// pending run is. There is nothing to reconcile against.
		return record, nil
	}
	if err := s.settle(ctx, runID, latest.Result()); err != nil {
		return store.Run{}, err
	}
	return s.store.Run(ctx, runID)
}

// recover reconciles every unfinished run against its checkpoint and continues
// the ones that were executing when the last service ended.
//
// Continuing happens on a goroutine of its own, so a home holding several
// interrupted runs comes back serving immediately rather than after the
// longest of them reaches a decision. The one thing done here before serving
// is PRD section 11's step 3: a run held on an elapsed checks wait has its
// pull request re-read, which is one provider call per such run under
// ChecksHoldReadBound of its own, and the service does not serve until each
// has answered or run out, so opening takes up to that bound times the number
// of such holds. A read that ran out is not read, and the hold stands. A run
// that was waiting on checks when the service stopped is not re-read here,
// because it is continued, and the first read of the resumed wait reads the
// pull request itself, which is step 1.
func (s *Service) recover(ctx context.Context) {
	// Standing consent writes a decision's hold row before it applies the
	// answer, so a service that stopped between the two left a row open.
	// Each is finished from the run's history before any run is continued,
	// so nothing has moved a run past the checkpoint its row names since
	// that service stopped; consent.go owns how.
	s.finishConsentRows(ctx)
	// An eject that died after reserving its repository's removal left a
	// reservation that refuses every run of it; P15's restart rule has the
	// next service release what an owner that is gone left, and
	// releaseStaleRemovals does, before anything is reclaimed or started.
	s.releaseStaleRemovals(ctx)
	repositories, err := s.store.Repositories(ctx)
	if err != nil {
		s.log.Printf("recovery could not list repositories: %v", err)
		return
	}
	// The runs as recovery first read them, before any is reconciled: the
	// ones this record shows finished are what the sweep below is asked of.
	var read []store.Run
	for _, repository := range repositories {
		records, err := s.store.RunsForRepository(ctx, repository.ID)
		if err != nil {
			s.log.Printf("recovery could not list the runs of %s: %v", repository.ID, err)
			continue
		}
		read = append(read, records...)
		for _, record := range records {
			if record.Status == store.RunHeld {
				// PRD section 11's step 3, before serving anything: the one
				// hold the pull request's state may close is re-read now,
				// and every other held run is left for the person it waits
				// on. checkshold.go owns which hold that is. A held record
				// has nothing to reconcile: it is the checkpoint's halt
				// already written down.
				s.closeChecksHoldAtRecovery(ctx, record)
				continue
			}
			if record.Status != store.RunRunning && record.Status != store.RunPending {
				continue
			}
			settled, err := s.reconcile(ctx, record.ID)
			if err != nil {
				s.log.Printf("recovery could not reconcile run %s: %v", record.ID, err)
				continue
			}
			if settled.Status == store.RunHeld {
				// A record a service died between the halt and the record
				// of was just reconciled to held, and is the same case.
				s.closeChecksHoldAtRecovery(ctx, settled)
				continue
			}
			if settled.Status != store.RunRunning {
				continue
			}
			s.continueRun(settled.ID)
		}
	}
	// PRD section 11's step 5, after the runs are reconciled: every agent
	// process an earlier service of this home recorded and did not live to
	// end is looked for by its recorded identity and ended as its group,
	// unless its run is still unfinished. reap.go owns the decision and its
	// outcomes; what it does not reach is stated there and in doc.go.
	s.reaped = s.reapOrphans(ctx)
	// A run that was already finished when recovery read it, and whose
	// isolated copy still stands, may have finished under a service that
	// stopped between the move and the sweep that move asks, so its copy is
	// swept now, after its recorded groups were reaped and before the fleet
	// pass below can launch anything. Whether a run is finished is read off
	// the record through internal/runs, never off the copy, so no active
	// run's copy is touched; runsweep.go owns the sweep and what it records.
	s.sweepFinishedAtRecovery(ctx, read)
	// PRD section 11's "remove isolated copies no run owns": the same
	// finished runs' standing copies are then asked of the cleanup decision
	// again, off this goroutine, so a copy whose work has since landed is
	// removed and one still holding unlanded work becomes a kept work entry
	// rather than staying on disk unrecorded; cleanup.go owns what a refusal
	// becomes.
	s.reclaimAtRecovery(ctx, read)
	// A land answer to kept work that a stopped service recorded and did
	// not carry out is completed now, on keptanswer.go's terms, before the
	// tasks its entry held are dispatched below.
	s.completeLandingsAtOpen(ctx)
	s.completeCarryOnsAtOpen(ctx)
	// PRD section 11's fleet reconciliation, after the runs and before
	// anything is served: the queued wakes are drained as input, every task
	// of this home is read with its terminal probed under a bound, and a
	// terminal gone with the work not landed is relaunched in its own copy.
	// reconcile.go owns the table and the bound; the launch itself goes to a
	// goroutine of this service's, so a home with several dead terminals
	// comes back serving after the claims and not after the launches.
	s.reconcileTasks(ctx)
	// PRD section 11's row for a subordinate coordinator's terminal gone,
	// after the tasks: every standing delegation's session is re-probed on
	// the same terms, and a dead one is relaunched as a new delegation
	// attempt; subordinates.go owns the decision and its bound.
	s.reconcileSubordinates(ctx)
	// A blocker may have cleared while no service was serving the home, so
	// the queued tasks are released once the records are reconciled, before
	// anything is served: the Queued row of the reconciliation table is this
	// call, which is idempotent. Every task that then resolves Dispatched is
	// dispatched, which for one a service died part way through dispatching
	// is the retry dispatch.go describes; a task a relaunch just dispatched
	// holds its slot and is passed over.
	s.releaseQueued(ctx)
}

// continueRun resumes an interrupted run in the background, so neither
// recovery nor a caller that walked away holds up serving.
//
// It runs under the service's own context rather than any caller's, which is
// what makes it a continuation rather than a second attempt at somebody's
// request: there is nobody to answer. What can end the segment it starts is
// stated at carryOn.
//
// Whether it begins at all is startWork's, and that is where the ordering
// against Close lives rather than in the disposition that got here. carryOn
// reads the stop to classify an ending, and a read is not an ordering: a
// continuation decided the instant a stop arrives would otherwise register
// itself after Close had already waited for everything registered, and run its
// store reads against a database Close had gone on to shut.
func (s *Service) continueRun(runID string) {
	if !s.startWork() {
		s.log.Printf("run %s was not carried on because this service is giving up the home; recovery continues it on the next open", runID)
		return
	}
	go func() {
		defer s.work.Done()
		ctx, cancel := context.WithCancel(s.stopCtx)
		defer cancel()
		s.log.Printf("resuming run %s", runID)
		if _, err := s.attach(ctx, runID); err != nil {
			s.log.Printf("could not resume run %s: %v", runID, err)
		}
	}()
}

// startWork registers one piece of background work and reports whether it may
// begin, which is no once Close has stopped taking any.
//
// It is the one place work this service will wait for is registered, and it
// takes the same mutex stopWork does, so the registration and the decision
// that no more will be registered cannot interleave. Either this runs first
// and Close waits for what it registered, or Close ran first and this refuses;
// there is no order in which work is registered after Close has waited.
func (s *Service) startWork() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.givingUp {
		return false
	}
	s.work.Add(1)
	return true
}

// stopWork refuses every further piece of background work, so that what Close
// waits for is a set that cannot grow while it waits.
func (s *Service) stopWork() {
	s.mu.Lock()
	s.givingUp = true
	s.mu.Unlock()
}

// slot is the one place a run advances in, and the one place an ending of that
// run through the protocol is recorded while it is being written.
//
// Both fields are read and written under Service.mu, and that is the whole of
// what orders a run's ending against the segment advancing it: the record and
// the checkpoint are written on other goroutines, and nothing orders a read of
// either against a cancel that has not committed yet.
type slot struct {
	// cancel ends the segment holding this slot. It is nil when the slot
	// stands for an ending rather than for a segment, so nothing is executing
	// under it and there is nothing to end.
	cancel context.CancelFunc
	// ended is whether the run was ended through the protocol while this slot
	// stood.
	ended bool
	// graph is the topology the segment under this slot executes, nil for a
	// slot standing for an ending. It is what the run's counters were kept
	// against, so it is the one graph a reading of them may be made against
	// while the segment stands; activity reads it.
	graph *graph.Graph
}

// claim takes the one slot a run advances in.
//
// A slot endRun is holding refuses it, which is what keeps a segment from
// beginning on a run whose ending is being written. That refusal answers with
// ErrRunEnding rather than ErrRunAdvancing, because the two slots hold
// different things and a caller told the wrong one is told the opposite of
// what isAdvancing reports about the same slot: nothing is executing under an
// ending's, and saying a run is advancing when it is being ended is the
// disagreement this package exists to keep out of its answers.
func (s *Service) claim(runID string, cancel context.CancelFunc, topology *graph.Graph) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if held, taken := s.advancing[runID]; taken {
		if held.cancel == nil {
			return fmt.Errorf("%w: %s", ErrRunEnding, runID)
		}
		return fmt.Errorf("%w: %s", ErrRunAdvancing, runID)
	}
	s.advancing[runID] = &slot{cancel: cancel, graph: topology}
	return nil
}

// release gives the slot back, and reports whether the run was ended through
// the protocol while this segment held it.
func (s *Service) release(runID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	held, taken := s.advancing[runID]
	delete(s.advancing, runID)
	return taken && held.ended
}

// endRun records that a run is being ended through the protocol and ends the
// segment advancing it, and returns the function that forgets the record.
//
// It is what makes carryOn's refusal a guard rather than a guess, and it has a
// case for each side of the segment's release because the ending can arrive on
// either. A slot that is taken is marked and whatever segment stands under it
// is ended, so the release that segment makes reports the ending and no
// continuation follows it. A run whose slot is free has this stand in it while
// the caller writes the ending, so no segment begins under an ending that is
// still in flight.
//
// Those two sentences are the whole of what it buys, and neither reaches back
// past the call. What follows is what it does not close, and this is the one
// place that is written down: everywhere else in this package points here.
//
// A segment already inside a node still has to return, which is the window
// cancel's own documentation describes.
//
// A record read that predates the ending reaching the run's record can still
// lead to a claim, on either branch, once the slot is given back. The bound is
// that commit and not this call, and the two are not the same moment: cancel
// records the ending in the slot here and moves the record afterwards, so a
// read made entirely after this returned can still find the run unfinished and
// go on to claim a slot this has already let go. One of the readers that
// covers is this package's own continuation, which reads the record through
// attach; a segment such a reader starts is not one this ending marked, so its
// own carryOn may carry it on again.
//
// A second ending of the same run in flight at the same time takes the marked
// branch and gets a forget that does nothing, so the first caller's forget
// gives the slot back while the second caller's move is still unfinished.
func (s *Service) endRun(runID string) func() {
	s.mu.Lock()
	held, taken := s.advancing[runID]
	if !taken {
		s.advancing[runID] = &slot{ended: true}
		s.mu.Unlock()
		return func() { s.release(runID) }
	}
	held.ended = true
	stop := held.cancel
	s.mu.Unlock()
	// A slot another ending of the same run is standing in has no segment
	// under it, and the caller that took it is the one that gives it back.
	if stop != nil {
		stop()
	}
	return func() {}
}

// isAdvancing reports whether a segment of this run is executing here.
//
// It is the one owner of that fact, and it is the reason report asks it rather
// than deriving it. A run's record says whether the run is unfinished and its
// checkpoint says where its execution stopped; neither says whether anything
// is moving it, and both read the same for a run being walked this instant and
// a run whose segment stopped without settling. The slot is taken before a
// segment starts and given back when it returns however it returns, so this
// tracks a segment rather than an intention to run one.
//
// A slot standing for an ending rather than for a segment answers false, which
// is the honest answer: nothing is executing under it. claim tells the two
// apart the same way, so a caller refused that slot is told the run is ending
// rather than that it is advancing.
//
// It holds nothing durable, which is not an omission: a service that died
// mid-segment is not advancing anything, and a record of the claim it left
// behind would say it was. That is what recover reconciles and continues.
func (s *Service) isAdvancing(runID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	held, taken := s.advancing[runID]
	return taken && held.cancel != nil
}

// advancingGraph is the graph the segment advancing a run executes, and false
// when no segment is advancing it. It answers from the same slot isAdvancing
// does, under the same lock, so a graph is only ever handed out for a run
// that fact says is in flight.
func (s *Service) advancingGraph(runID string) (*graph.Graph, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	held, taken := s.advancing[runID]
	if !taken || held.cancel == nil || held.graph == nil {
		return nil, false
	}
	return held.graph, true
}

// branchKey is the branch of one repository, which is what a run is started
// for and what PRD section 8 has runs serialize on.
type branchKey struct {
	repository string
	branch     string
}

// branchGate is the exclusion one branch's starts take in turn. waiting counts
// the callers that hold or are queued for it, so the gate is forgotten once
// nobody is using it and a service that has seen many branches does not keep
// one per branch it ever saw.
type branchGate struct {
	held    chan struct{}
	waiting int
}

// claimBranch decides whether a branch already has a run and creates one when
// it does not, with the decision and the write under one exclusion. It reports
// the run and whether this call is the one that created it.
//
// It is the start path's use of the branch gate; claimRerun is the other, and
// they are the only two places a run is created, so every decision that a
// branch has no run is made holding the same gate.
//
// The check and the create are one step because they are one decision: a check
// another caller can win the race to is what leaves a branch with two runs, of
// which the older is unreachable through every branch-scoped verb and blocks
// an eject for as long as it stands. The record's inputs are read before the
// claim; the one git invocation under it is the fetch create makes to record
// the commit in the gate, and the gate is one branch's, so what waits on it
// is a second start of the same branch and not another branch's.
//
// The residual gap is the same one internal/runs names for a run's fixer: this
// is exclusion within one service, and PRD section 8 gives a home one service,
// so it holds for the arrangement that produces. Two services over one store
// would not see each other's claims, and nothing in the schema refuses the
// second run they could then create between them.
func (s *Service) claimBranch(ctx context.Context, key branchKey, spec run) (store.Run, bool, error) {
	release, err := s.holdBranch(ctx, key)
	if err != nil {
		return store.Run{}, false, err
	}
	defer release()

	active, found, err := s.activeRun(ctx, key.repository, key.branch)
	if err != nil {
		return store.Run{}, false, err
	}
	if found {
		return active, false, nil
	}
	record, err := s.create(ctx, spec)
	if err != nil {
		return store.Run{}, false, err
	}
	return record, true, nil
}

// holdBranch takes one branch's gate and returns the function that gives it
// back. A caller whose context ends while queued gives up rather than holding
// up the branch behind it.
func (s *Service) holdBranch(ctx context.Context, key branchKey) (func(), error) {
	s.mu.Lock()
	gate, ok := s.starting[key]
	if !ok {
		gate = &branchGate{held: make(chan struct{}, 1)}
		s.starting[key] = gate
	}
	gate.waiting++
	s.mu.Unlock()

	forget := func() {
		s.mu.Lock()
		gate.waiting--
		if gate.waiting == 0 {
			delete(s.starting, key)
		}
		s.mu.Unlock()
	}
	select {
	case gate.held <- struct{}{}:
		return func() { <-gate.held; forget() }, nil
	case <-ctx.Done():
		forget()
		return nil, ctx.Err()
	}
}

// activeRuns returns every run this home has that has not finished.
func (s *Service) activeRuns(ctx context.Context) ([]store.Run, error) {
	repositories, err := s.store.Repositories(ctx)
	if err != nil {
		return nil, err
	}
	var active []store.Run
	for _, repository := range repositories {
		records, err := s.store.RunsForRepository(ctx, repository.ID)
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			if unfinished(record.Status) {
				active = append(active, record)
			}
		}
	}
	return active, nil
}

// activeRun returns the newest unfinished run of one branch.
func (s *Service) activeRun(ctx context.Context, repositoryID, branch string) (store.Run, bool, error) {
	records, err := s.store.RunsForRepository(ctx, repositoryID)
	if err != nil {
		return store.Run{}, false, err
	}
	for _, record := range records {
		if record.Branch == branch && unfinished(record.Status) {
			return record, true, nil
		}
	}
	return store.Run{}, false, nil
}

// latestRunOnBranch returns the newest run of one branch, which is what a
// rerun of that branch carries forward. A branch with no run is refused by
// name, so a caller is told about the branch they are standing on rather than
// handed another branch's run.
func (s *Service) latestRunOnBranch(ctx context.Context, repositoryID, branch string) (store.Run, error) {
	records, err := s.store.RunsForRepository(ctx, repositoryID)
	if err != nil {
		return store.Run{}, err
	}
	for _, record := range records {
		if record.Branch == branch {
			return record, nil
		}
	}
	return store.Run{}, fmt.Errorf("service: %s has no run to start again from", branch)
}

// unfinished reports whether a run in this status may still move.
//
// The statuses a run ends in are internal/store's closed set, and this names
// the three that are not terminal rather than the three that are, so a status
// this build does not recognize reads as finished: a run this service cannot
// interpret is one it must not take further.
func unfinished(status store.RunStatus) bool {
	switch status {
	case store.RunPending, store.RunRunning, store.RunHeld:
		return true
	case store.RunPassed, store.RunFailed, store.RunTerminated:
		return false
	default:
		return false
	}
}

// view reports a run from its record and its latest checkpoint.
func (s *Service) view(ctx context.Context, runID string) (machine.Run, error) {
	return s.report(ctx, runID, nil)
}

// report builds the answer for one run. result is the segment that just ran
// when there was one, and its absence means the run is being read rather than
// advanced, in which case the checkpoint is where it stands.
func (s *Service) report(ctx context.Context, runID string, result *graph.Result) (machine.Run, error) {
	record, err := s.store.Run(ctx, runID)
	if err != nil {
		return machine.Run{}, err
	}
	// A caller that advanced the run is answering for a segment that has
	// already stopped, and it still holds that run's slot until it returns, so
	// no other caller can be advancing it either. A read is the only answer
	// that can find a segment in flight, and it is the only one that can find
	// the run standing still with nothing moving it.
	// PRD section 8 derives the run's passed on a merge from the merge
	// record, so the answer reads that record here and carries it; nothing
	// on the run's own row restates it.
	merge, ended, err := s.store.MergeRecord(ctx, runID)
	if err != nil {
		return machine.Run{}, err
	}
	// What became of the run's copy is the cleanup record, composed on the
	// same terms as the merge record: present exactly when the decision
	// wrote one, and the one place status reads that a copy was kept.
	cleanup, err := s.store.RunCleanup(ctx, runID)
	switch {
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
	default:
		return machine.Run{}, err
	}
	// What the latest sweep of the run's copy came to is its own record,
	// composed on the same terms: present exactly when a sweep was recorded.
	sweep, err := s.store.RunCopySweep(ctx, runID)
	switch {
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
	default:
		return machine.Run{}, err
	}
	standing := machine.Standing{
		Record:     record.Status,
		Park:       record.Park,
		NoCodeHost: record.NoCodeHost,
		Execution:  machine.ExecutionUnrecorded(),
		Advancing:  result == nil && s.isAdvancing(runID),
	}
	answer := machine.Run{Record: record}
	if cleanup.Run != "" {
		answer.Cleanup = &cleanup
	}
	if sweep.Run != "" {
		answer.CopySweep = &sweep
	}
	if ended {
		standing.PullRequestEnd = merge.End
		answer.MergeRecord = &merge
		// A run its pull request closed carries the reason the record
		// renders for that end; a parked run's reason is the checkpoint's,
		// below, and the two cannot both apply because a run ended by its
		// pull request was not parked.
		if merge.End == store.PullRequestClosed {
			answer.Reason = merge.End.Reason()
		}
	}
	// The proposal is read off the store as it stands now and never comes
	// from graph state; decision.go says why.
	answer.Proposals = s.proposals(ctx, record)
	// The project's kept work, which holds new tasks and never a run, is
	// named on every answer from the record as it stands now; a read that
	// fails is logged and the answer names none.
	if kept, err := s.store.OpenKeptWorkOf(ctx, record.RepositoryID); err != nil {
		s.log.Printf("the kept work of run %s's project could not be read for its answer: %v", runID, err)
	} else {
		answer.KeptWork = kept
	}
	if result == nil {
		latest, written, err := s.checkpoints.LatestWritten(ctx, runID)
		if err != nil {
			if !errors.Is(err, graph.ErrNoSuchRun) {
				// A position that is there and cannot be read is a failure to
				// report, not a run with no position. The two answers differ
				// in what they tell a caller to do, and the wrong one here
				// tells them to end a run whose history is intact.
				return machine.Run{}, fmt.Errorf("service: reading the position of run %s: %w", runID, err)
			}
			// A run with no checkpoint has not executed. That is an absence
			// rather than a place execution stopped, so it goes to the answer
			// as one, and what separates a run between its start and its first
			// checkpoint from one nothing is carrying on is the same fact that
			// separates them everywhere else.
			return answer.Decide(standing), nil
		}
		read := latest.Result()
		result = &read
		if standing.Advancing {
			answer.Activity = s.activity(runID, latest, written, time.Now())
		}
	}
	status := result.Status
	answer.Progress = &status
	answer.Position = result.Position
	if result.Reason != "" {
		answer.Reason = result.Reason
	}
	answer.Steps = result.Steps
	answer.Budget = result.Budget
	standing.Execution = machine.ExecutionAt(result.Status, result.State)
	// PRD section 9 makes a stall that looks alive worse than an error, and an
	// executing run is the one answer that describes both a run in flight and
	// a run nothing is carrying on. Nothing here chooses between them: the
	// three facts go to machine.Run.Decide together, which is the only writer
	// of a next action there is, so this cannot hand back one that disagrees
	// with the outcome beside it.
	answer = answer.Decide(standing)
	answer.Stages = stageViews(result.State)
	// The repairs are read off the same state the stages are. A record that
	// cannot be read leaves the answer without them and the service log
	// saying why, on the stage times' terms below: it describes the run and
	// decides nothing about it.
	if repairs, err := pipeline.RepairsOf(result.State); err != nil {
		s.log.Printf("reporting run %s without its repairs: %v", runID, err)
	} else {
		answer.Repairs = repairs
	}
	// When each stage executed is the store's history rather than the
	// checkpoint's. It describes the run and decides nothing about it, so a
	// history that cannot be read leaves the stages without times and the
	// service log saying why, rather than failing an answer whose outcome,
	// position, and decision were all read. Nothing on the answer says the
	// times are missing, which is the residual gap: a stage with no
	// executions reads the same as one whose executions were not read.
	executions, err := s.store.StageExecutions(ctx, runID)
	if err != nil {
		s.log.Printf("reporting run %s without stage times: %v", runID, err)
		executions = nil
	}
	times := stageTimes(executions)
	for i := range answer.Stages {
		answer.Stages[i].Executions = times[answer.Stages[i].Stage]
	}
	// The record decides before the checkpoint does, which is machine.OutcomeOf's
	// rule applied to the decision as well as to the outcome: a run ended from
	// outside its own execution has a terminal record and a checkpoint that
	// still stands at a hold, and carrying that decision on would invite an
	// answer this surface then refuses. The stage reports are untouched, so
	// what each stage found is still there; what goes is the invitation.
	if result.Decision != nil && unfinished(record.Status) {
		answer.Decision = decisionView(*result.Decision, result.State)
	}
	return answer, nil
}

// activity is what the segment advancing a run is doing, read off the
// checkpoint it last wrote and when internal/store appended it, or nil when
// there is nothing in flight at a stage to describe.
//
// Every part of it is read from its owner. The stage is the checkpoint's
// position, which a running run's checkpoint names as the node about to run.
// The round is pipeline.RoundOf over the checkpoint's counters, read against
// the graph the advancing slot holds: those counters are positional and mean
// something only against the edges they were kept against, and the segment
// in flight is executing exactly that graph, so the slot rather than a fresh
// resolution of the run's configuration is where the graph is taken from. A
// segment that ended between the advancing read and this one has no slot,
// and the answer then carries no activity rather than a reading made against
// a graph nobody is running. The time is the entry's append, so nothing here
// keeps a clock of its own for a run.
//
// Nothing decides anything from it. It describes a node in flight for a
// person or an agent to read, which is why a reading that cannot be made is
// logged and left out rather than failing an answer whose outcome, position
// and decision were all read.
func (s *Service) activity(runID string, latest graph.Checkpoint, written, now time.Time) *machine.Activity {
	if latest.Status != graph.StatusRunning {
		return nil
	}
	stage, ok := pipeline.StageOfNode(latest.Position)
	if !ok {
		return nil
	}
	topology, ok := s.advancingGraph(runID)
	if !ok {
		return nil
	}
	round, err := pipeline.RoundOf(topology, latest.Counters, stage)
	if err != nil {
		if s.log != nil {
			s.log.Printf("reporting run %s without its %s stage's round: %v", runID, stage, err)
		}
		return nil
	}
	active := now.Sub(written)
	if active < 0 {
		active = 0
	}
	return &machine.Activity{Stage: stage.String(), Round: round.Taken, Limit: round.Limit, Since: written, Active: active}
}

// stageViews reports what became of each of the nine stages, in the order a
// run takes them.
func stageViews(state graph.State) []machine.Stage {
	views := make([]machine.Stage, 0, len(pipeline.Order()))
	for _, stage := range pipeline.Order() {
		view := machine.Stage{
			Stage:      stage.String(),
			Outcome:    pipeline.StageOutcome(state, stage),
			Ran:        pipeline.StageRan(state, stage),
			Fix:        pipeline.FixSummary(state, stage),
			SkipReason: pipeline.StageSkipReason(state, stage),
		}
		if view.Ran {
			if report, err := pipeline.StageReport(state, stage); err == nil {
				view.Report = &report
			}
		}
		views = append(views, view)
	}
	return views
}

// decisionView carries the open decision with the findings that produced it.
//
// PRD section 9 requires a finding that needs a decision to be relayed with
// its full text, unsummarized and unjudged, so what travels is the stage's
// report as the stage recorded it. Nothing here shortens, ranks, or rewrites
// it, and a report that cannot be read is left out rather than replaced by
// this package's account of it.
func decisionView(decision graph.Decision, state graph.State) *machine.Decision {
	view := &machine.Decision{Decision: decision}
	for _, stage := range pipeline.Order() {
		if decision.Node != stage.HoldNode() {
			continue
		}
		view.Stage = stage.String()
		if report, err := pipeline.StageReport(state, stage); err == nil {
			view.Findings = report.Held()
		}
		break
	}
	return view
}

// publishRunState tells subscribers that a run moved. It is a state event, so
// it carries a revision and a consumer applies it only when it is newer than
// what it holds.
func (s *Service) publishRunState(ctx context.Context, runID string) {
	record, err := s.store.Run(ctx, runID)
	if err != nil {
		return
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return
	}
	if err := s.events.Publish(ipc.Event{
		Type:     ipc.TypeRunState,
		Revision: s.revision.Add(1),
		Payload:  payload,
	}); err != nil {
		s.log.Printf("could not publish the state of run %s: %v", runID, err)
	}
}

// repositoryAt returns the repository record for a working copy. The lookup is
// internal/store's, so a surface that reports whether a run could start asks
// the same question this does rather than a second one shaped like it.
func (s *Service) repositoryAt(ctx context.Context, workingPath string) (store.Repository, error) {
	if !filepath.IsAbs(workingPath) {
		return store.Repository{}, fmt.Errorf("service: %q is not an absolute working copy path", workingPath)
	}
	repository, err := s.store.RepositoryAt(ctx, workingPath)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.Repository{}, fmt.Errorf("%w: %s", ErrNoRepository, workingPath)
		}
		return store.Repository{}, err
	}
	return repository, nil
}

// parseStages reads the stage names a run asks to skip, refusing a name that
// is not one of the nine rather than skipping nothing and saying it did.
func parseStages(names []string) ([]pipeline.Stage, error) {
	out := make([]pipeline.Stage, 0, len(names))
	for _, name := range names {
		stage, ok := pipeline.ParseStage(name)
		if !ok {
			return nil, fmt.Errorf("%w: %q is not one of the stages: %s", ipc.ErrInvalidRequest, name, strings.Join(stageNames(), ", "))
		}
		out = append(out, stage)
	}
	return out, nil
}

// stageNames is the nine stage names, for a refusal that says what was
// expected.
func stageNames() []string {
	names := make([]string, 0, len(pipeline.Order()))
	for _, stage := range pipeline.Order() {
		names = append(names, stage.String())
	}
	return names
}

// The intent sources a run records. PRD section 8 has the run carry its intent
// and where the intent came from, so a report can say who asked rather than
// only what was asked.
//
// What reads the value, and what it changes: rerun reads it off the previous
// run to decide pipeline.Start.IntentSupplied, or records supplied when the
// rerun replaces the intent, and it crosses the wire as
// store.Run's intent_source, which is what a driving agent reads off a
// reported run. The distinction that is acted on is supplied against not
// supplied, and that is the whole of it.
//
// supplied and task both set IntentSupplied, so the intent stage holds the
// change to either. offered and absent do not. The column is what says
// whether the text was stated for the run or read off the task. The stage
// reads the supplied bit out of graph state, never this column, and reports
// a supplied intent as acceptance criteria and no intent text as absent. An
// intent taken from a task is supplied, and only this column says it came
// from the task. A person's push of that branch stays absent.
//
// The four are kept apart here anyway because the record must not say that a
// run holds intent text and that nothing was given, nor that a person said
// what the program read off a task.
const (
	// intentSourceSupplied is an intent a person or a driving agent stated as
	// acceptance criteria.
	intentSourceSupplied = "supplied"
	// intentSourceOffered is an intent a caller gave as a hint, having
	// declined to claim it as acceptance criteria.
	intentSourceOffered = "offered"
	// intentSourceAbsent is a run started with no intent at all. The intent
	// stage reports it as absent; inferring one is deferred work, so nothing
	// in this build fills it in.
	intentSourceAbsent = "absent"
	// intentSourceTask is an intent the program read off the task whose
	// worker pushed the run's branch: the task's project and, for a
	// promoted task, the delivery intent its promotion recorded, or
	// otherwise the instructions the coordinator wrote at intake, which
	// workers.go's pushIntent decides from the worker group set and the
	// task's records. It is the acceptance criteria of that run.
	// A person's push of the same branch never carries it.
	intentSourceTask = "task"
)

// intentCountsAsSupplied reports whether a recorded source is acceptance
// criteria. A person's supplied intent and the instructions read off a task
// both are. An offered hint and an absent intent are not.
func intentCountsAsSupplied(source string) bool {
	return source == intentSourceSupplied || source == intentSourceTask
}

// intentSource says where a run's intent came from.
//
// The three are distinct facts about the request, not two facts and a default.
// A request carrying intent text without the supplied flag is one the wire
// shape permits, and recording it as absent would have a run whose Intent
// column holds text also record that nothing was given.
func intentSource(req machine.StartRequest) string {
	switch {
	case req.IntentSupplied:
		return intentSourceSupplied
	case strings.TrimSpace(req.Intent) != "":
		return intentSourceOffered
	default:
		return intentSourceAbsent
	}
}

// newRunID returns an identifier for a new run. It is random rather than
// sequential so that two services over two homes never mint the same one, and
// it carries no meaning: what a run is about is on its record.
func newRunID() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("service: generating a run identifier: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}
