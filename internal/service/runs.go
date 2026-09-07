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

	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/redact"
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
	// Everything the record is built from is read before the branch is
	// claimed, so what the claim holds is the decision and the write and not
	// two git invocations between them.
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
// branch's last known head and inheriting the intent recorded there, and
// blocks while it advances that run, answering at its next decision point or a
// terminal outcome. It never answers with a run still executing, because the
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
func (s *Service) rerun(ctx context.Context, req machine.RerunRequest) (machine.Run, error) {
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
	record, err := s.claimRerun(ctx, branchKey{repository: repository.ID, branch: branch})
	if err != nil {
		return machine.Run{}, err
	}
	return s.begin(ctx, record, pipeline.Start{
		Branch:         record.Branch,
		Base:           repository.DefaultBranch,
		Submitted:      record.SubmittedHead,
		Intent:         record.Intent,
		IntentSupplied: record.IntentSource == intentSourceSupplied,
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
func (s *Service) claimRerun(ctx context.Context, key branchKey) (store.Run, error) {
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
	return s.create(ctx, run{
		repository: key.repository,
		branch:     key.branch,
		head:       previous.CurrentHead.Or(previous.SubmittedHead),
		base:       previous.Base,
		intent:     previous.Intent,
		source:     previous.IntentSource,
		supplied:   previous.IntentSource == intentSourceSupplied,
	})
}

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
	built, err := s.driverFor(ctx)
	if err != nil {
		return machine.Run{}, err
	}
	if _, err := built.runs.Release(ctx, record.ID); err != nil {
		return machine.Run{}, err
	}
	return s.advance(ctx, record.ID, func(ctx context.Context) (graph.Result, error) {
		return built.executor.Answer(ctx, record.ID, req.Answer)
	})
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
func (s *Service) cancel(ctx context.Context, req machine.CancelRequest) (machine.Run, error) {
	built, err := s.driverFor(ctx)
	if err != nil {
		return machine.Run{}, err
	}
	forget := s.endRun(req.Run)
	defer forget()
	if _, err := built.runs.Terminate(ctx, req.Run); err != nil {
		return machine.Run{}, err
	}
	return s.view(ctx, req.Run)
}

// attach reports a run as it stands, resuming it when it is one this service
// stopped part way through.
func (s *Service) attach(ctx context.Context, runID string) (machine.Run, error) {
	settled, err := s.reconcile(ctx, runID)
	if err != nil {
		return machine.Run{}, err
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
	if settled.Status == store.RunPending {
		if _, err := built.runs.Start(ctx, settled.ID); err != nil {
			return machine.Run{}, err
		}
	}
	// A run this service is already advancing is reported rather than refused:
	// attaching asks where a run stands, and "it is moving" is an answer to
	// that. Taking the slot and reporting the refusal is what makes this a
	// decision rather than a check that another caller can win the race to.
	view, err := s.advance(ctx, settled.ID, func(ctx context.Context) (graph.Result, error) {
		return built.executor.Resume(ctx, settled.ID)
	})
	if errors.Is(err, ErrRunAdvancing) || errors.Is(err, ErrRunEnding) {
		return s.view(ctx, settled.ID)
	}
	return view, err
}

// begin records the run as started and walks it from its initial state.
func (s *Service) begin(ctx context.Context, record store.Run, start pipeline.Start) (machine.Run, error) {
	built, err := s.driverFor(ctx)
	if err != nil {
		return machine.Run{}, err
	}
	initial, err := built.pipeline.NewState(start)
	if err != nil {
		return machine.Run{}, err
	}
	if _, err := built.runs.Start(ctx, record.ID); err != nil {
		return machine.Run{}, err
	}
	return s.advance(ctx, record.ID, func(ctx context.Context) (graph.Result, error) {
		return built.executor.Run(ctx, record.ID, initial)
	})
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
}

// create records a new run. PRD section 8 requires the row to precede the run's
// directory, and nothing here creates one, so a stage that needs an isolated
// copy makes it after this.
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
	return built.runs.Create(ctx, store.Run{
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
func (s *Service) advance(ctx context.Context, runID string, step func(context.Context) (graph.Result, error)) (machine.Run, error) {
	segment, cancel := context.WithCancel(context.WithoutCancel(ctx))
	if err := s.claim(runID, cancel); err != nil {
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
	switch {
	case result.Status == graph.StatusHalted:
		move = built.runs.Hold
	case result.Status == graph.StatusCompleted && pipeline.Cancelled(result.State):
		move = built.runs.Terminate
	case result.Status == graph.StatusCompleted:
		move = built.runs.Pass
	case result.Status.Stopped():
		move = built.runs.Terminate
	default:
		return nil
	}
	if _, err := move(ctx, runID); err != nil {
		var wrong *store.RunStatusError
		if !errors.As(err, &wrong) {
			return err
		}
		s.log.Printf("run %s was %s rather than where this segment left it", runID, wrong.Actual)
	}
	s.publishRunState(ctx, runID)
	return nil
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
	if err := s.settle(ctx, runID, graph.Result{Status: latest.Status, State: latest.State}); err != nil {
		return store.Run{}, err
	}
	return s.store.Run(ctx, runID)
}

// recover reconciles every unfinished run against its checkpoint and continues
// the ones that were executing when the last service ended.
//
// Continuing happens on a goroutine of its own, so a home holding several
// interrupted runs comes back serving immediately rather than after the
// longest of them reaches a decision.
func (s *Service) recover(ctx context.Context) {
	repositories, err := s.store.Repositories(ctx)
	if err != nil {
		s.log.Printf("recovery could not list repositories: %v", err)
		return
	}
	for _, repository := range repositories {
		records, err := s.store.RunsForRepository(ctx, repository.ID)
		if err != nil {
			s.log.Printf("recovery could not list the runs of %s: %v", repository.ID, err)
			continue
		}
		for _, record := range records {
			if record.Status != store.RunRunning && record.Status != store.RunPending {
				continue
			}
			settled, err := s.reconcile(ctx, record.ID)
			if err != nil {
				s.log.Printf("recovery could not reconcile run %s: %v", record.ID, err)
				continue
			}
			if settled.Status != store.RunRunning {
				continue
			}
			s.continueRun(settled.ID)
		}
	}
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
func (s *Service) claim(runID string, cancel context.CancelFunc) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if held, taken := s.advancing[runID]; taken {
		if held.cancel == nil {
			return fmt.Errorf("%w: %s", ErrRunEnding, runID)
		}
		return fmt.Errorf("%w: %s", ErrRunAdvancing, runID)
	}
	s.advancing[runID] = &slot{cancel: cancel}
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
// an eject for as long as it stands. Nothing slow runs under the claim - the
// record's inputs are read before it - so branches do not queue behind each
// other's git.
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
	standing := machine.Standing{
		Record:    record.Status,
		Execution: machine.ExecutionUnrecorded(),
		Advancing: result == nil && s.isAdvancing(runID),
	}
	answer := machine.Run{Record: record}
	if result == nil {
		latest, err := s.checkpoints.Latest(ctx, runID)
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
		result = &graph.Result{
			Status:   latest.Status,
			Position: latest.Position,
			Decision: latest.Decision,
			State:    latest.State,
			Reason:   latest.Reason,
			Steps:    latest.Counters.Steps,
			Budget:   latest.Counters.Budget,
		}
	}
	status := result.Status
	answer.Progress = &status
	answer.Position = result.Position
	answer.Reason = result.Reason
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

// stageViews reports what became of each of the nine stages, in the order a
// run takes them.
func stageViews(state graph.State) []machine.Stage {
	views := make([]machine.Stage, 0, len(pipeline.Order()))
	for _, stage := range pipeline.Order() {
		view := machine.Stage{
			Stage:   stage.String(),
			Outcome: pipeline.StageOutcome(state, stage),
			Ran:     pipeline.StageRan(state, stage),
			Fix:     pipeline.FixSummary(state, stage),
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
			return nil, fmt.Errorf("service: %q is not one of the stages: %s", name, strings.Join(stageNames(), ", "))
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
// run to decide pipeline.Start.IntentSupplied, and it crosses the wire as
// store.Run's intent_source, which is what a driving agent reads off a
// reported run. The distinction that is acted on is supplied against not
// supplied, and that is the whole of it.
//
// The residual gap is that this column is not what drives the difference
// between offered and absent. Both give IntentSupplied false, so the graph
// state a run begins from differs only in the intent text itself, and a reader
// gets that from the Intent column rather than from this one.
//
// The stage that weighs a hint differently from a contract is the intent
// stage, and it has a body. It reads the intent text and the supplied bit out
// of graph state, never this column, and reports the three apart: a supplied
// intent as authoritative acceptance criteria, an offered one as a hint the
// change is not held to and not a defect to depart from, and no intent text at
// all as absent. It decides that from the same two facts this function reads,
// so what it reports matches what was recorded here. There is no corner where
// the two disagree: create refuses a supplied claim with nothing behind it
// before a row exists, so a supplied source always stands over text.
//
// The three are kept apart here anyway because the record must not say that a
// run holds intent text and that nothing was given.
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
)

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
