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
// blocks until that run reaches its next decision point or a terminal outcome.
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
		return s.attach(ctx, record.ID)
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

// rerun starts a fresh run of the branch the caller is standing on, from that
// branch's last known head and inheriting the intent recorded there, and
// blocks on the same terms as start.
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

// cancel ends a run. It ends the segment executing it first, because a run
// whose record says terminated while a segment of it is still running a node
// is a run this service reported as over and is still driving.
func (s *Service) cancel(ctx context.Context, req machine.CancelRequest) (machine.Run, error) {
	built, err := s.driverFor(ctx)
	if err != nil {
		return machine.Run{}, err
	}
	s.mu.Lock()
	stop := s.advancing[req.Run]
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
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
	if errors.Is(err, ErrRunAdvancing) {
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
	defer s.release(runID)
	// The caller's context ends the segment as well as the service's, so a
	// client that gave up does not leave a run walking nodes nobody is waiting
	// for. It is derived rather than used directly so that a cancel through
	// the protocol ends it too.
	stop := context.AfterFunc(ctx, cancel)
	defer stop()

	result, err := step(segment)
	if err != nil {
		s.log.Printf("run %s stopped: %v", runID, err)
		return machine.Run{}, err
	}
	if err := s.settle(ctx, runID, result); err != nil {
		return machine.Run{}, err
	}
	return s.report(ctx, runID, &result)
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

// continueRun resumes an interrupted run in the background, so recovery does
// not hold up serving.
func (s *Service) continueRun(runID string) {
	s.work.Add(1)
	go func() {
		defer s.work.Done()
		ctx, cancel := context.WithCancel(s.stopCtx)
		defer cancel()
		s.log.Printf("recovery is resuming run %s", runID)
		if _, err := s.attach(ctx, runID); err != nil {
			s.log.Printf("recovery could not resume run %s: %v", runID, err)
		}
	}()
}

// claim takes the one slot a run advances in.
func (s *Service) claim(runID string, cancel context.CancelFunc) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, taken := s.advancing[runID]; taken {
		return fmt.Errorf("%w: %s", ErrRunAdvancing, runID)
	}
	s.advancing[runID] = cancel
	return nil
}

// release gives the slot back.
func (s *Service) release(runID string) {
	s.mu.Lock()
	delete(s.advancing, runID)
	s.mu.Unlock()
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
			// A run with no checkpoint has not executed. Its record is the
			// whole of what is known, and saying so is better than reporting a
			// position it never reached.
			answer.Outcome = machine.OutcomeOf(record.Status, graph.StatusInvalid, graph.State{})
			answer.NextAction = "This run never began executing, so there is no position to carry it on from. End it and start a fresh run."
			return answer, nil
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
	answer.Outcome = machine.OutcomeOf(record.Status, result.Status, result.State)
	answer.NextAction = answer.Outcome.NextAction()
	answer.Stages = stageViews(result.State)
	if result.Decision != nil {
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

// repositoryAt returns the repository record for a working copy.
func (s *Service) repositoryAt(ctx context.Context, workingPath string) (store.Repository, error) {
	if !filepath.IsAbs(workingPath) {
		return store.Repository{}, fmt.Errorf("service: %q is not an absolute working copy path", workingPath)
	}
	repositories, err := s.store.Repositories(ctx)
	if err != nil {
		return store.Repository{}, err
	}
	for _, repository := range repositories {
		if repository.WorkingPath == workingPath {
			return repository, nil
		}
	}
	return store.Repository{}, fmt.Errorf("%w: %s", ErrNoRepository, workingPath)
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
// The residual gap is that offered and absent drive nothing different. Both
// give IntentSupplied false, so the graph state a run begins from is identical
// for the two, and the only other difference between them is that the Intent
// column holds text, which a reader gets from that column rather than from
// this one. The stage that would weigh a hint differently from a contract is
// the intent stage, and it has no body, so nothing will act on the difference
// until it does. The three are kept apart anyway because the record must not
// say that a run holds intent text and that nothing was given.
const (
	// intentSourceSupplied is an intent a person or a driving agent stated as
	// acceptance criteria.
	intentSourceSupplied = "supplied"
	// intentSourceOffered is an intent a caller gave as a hint, having
	// declined to claim it as acceptance criteria.
	intentSourceOffered = "offered"
	// intentSourceAbsent is a run started with no intent at all, which leaves
	// the intent stage to infer one.
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
