package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
	"github.com/dayamjz/assistant/internal/taskstate"
	"github.com/dayamjz/assistant/internal/vcs"
)

// Dispatching a task: the clone, the claim, the two snapshots, and the proof.
//
// PRD section 6 moves a task from Dispatched to Running only once isolation
// is proven, and ends it with the refusal otherwise. This file is the step
// between: for a task that resolves Dispatched it cuts the copy PRD section 5
// gives the task's shape, observes it, asserts it, and either holds the proof
// or records the refusal. Nothing in this file launches a worker. The proof
// it ends at is kept in this service's memory and nowhere else, and launch.go
// is what takes it: dispatchDue hands a proven dispatch straight to the
// launch, which asserts the copy again and starts the worker, so a dispatch
// that reaches proven is followed by a launch in the same pass. What the
// launch does and does not move is launch.go's to say.
//
// # The copy is a clone of the primary, with the gate as its one remote
//
// A delivery copy is vcs.Clone of the person's primary checkout, the working
// copy the repository record names, into home.TaskCopy, with exactly one
// remote: gate.RemoteName at this home's gate repository for the task's
// repository. The task branch is created in the clone at the primary's
// default-branch tip, which is read off the primary before the clone and is
// the base the task starts from. An investigation copy is the same clone with
// no remote at all, detached at that same tip, because it creates no branch
// and pushes nothing. The clone reads the primary and writes nothing in it,
// which is the stance ensureCopy takes toward a working copy and PRD
// principle P1's concern; what is read is recorded as the primary snapshot.
//
// vcs.Clone makes the copy under a remote name it then removes, and
// internal/vcs's own tests read the copy's configuration back to establish
// that only the named remote's URL survives, so the investigation copy's
// configuration is not scrubbed here: a key that could survive would be that
// package's to remove.
//
// # The claim is the attempt, and the dispatched entry is not it
//
// Two dispatchers of one task cannot both cut: the dispatch is claimed by
// store.BeginTaskAttempt, anchored to the highest attempt this dispatcher
// read, and the loser is refused with ErrTaskMoved before it reads the
// primary or touches the path. The "dispatched" entry intake or Release
// appended is the state fact the dispatch is decided from, and it is never
// the claim: it says the task is to be dispatched, not that anyone is.
//
// What the anchor refuses is a dispatcher that read the same latest attempt
// this one did. A dispatcher that read a later one is a retry by the record's
// lights, and nothing in the record tells an attempt that crashed from one
// still in flight: within one service the in-memory slot below keeps a retry
// from beginning behind a live dispatch, and across services the home's lock
// is what keeps there from being two. That is the residual gap, and it is
// the same one internal/runs names for its branch gate.
//
// A dispatch is of a task that was never launched. A task whose latest
// attempt names a terminal session is passed over and refused with
// errTaskLaunched, because what to do about a launched worker whose service
// restarted is PRD section 11's question and not this file's: reconcile.go
// reads the terminal, relaunches it in place when it is gone, and leaves it
// alone when the reading is ambiguous, which a second dispatch here would
// not.
//
// A retry after a crash is a new attempt of the same task, and it finds
// whatever the crashed one left at the path. A path already standing there is
// observed and asserted exactly as a fresh clone is, and is never removed or
// written into (P6; copyStanding takes the same stance toward a run's copy):
// a half-written directory fails the assertion, and that failure is recorded
// as the refusal with the path untouched. The one directory this file does
// remove is a clone this same call just made and could not shape - the task
// branch could not be created in it, or HEAD could not be detached - which
// holds no work and is the program's own; it is removed so that the next
// attempt cuts a fresh one rather than proving a clone standing on the wrong
// commit.
//
// # Records precede directories
//
// The order is PRD section 8's: the attempt row, then the primary snapshot
// row, then the clone, then the observation, and the copy's own snapshot only
// once the copy is proven. A task record names its copy's path from intake as
// a reservation, and the path home.TaskCopy composes is the one owner of it;
// a record whose reservation disagrees is refused before anything is claimed.
//
// # What the environment check is made of
//
// The assertion takes the environment a worker would be launched with, which
// agents.WorkerEnvironment computes from the same base and extras a
// WorkerRunner starts a worker under. This service passes os.Environ() and no
// extras, which is the base every runner the default catalog builds has;
// WorkerEnvironment's doc states the gap for a runner built with another.
// Because that builder strips the redirecting variables, the assertion's
// environment comparison cannot fail on this path, and nothing here claims it
// can. The environment half of P11 is therefore only as strong as every
// launch path building its environment through agents.WorkerEnvironment:
// launch.go's second assertion builds it the same way, and the worker is
// started by a WorkerRunner that builds it the same way again; no second
// strip list exists, and none should.
//
// # The second check
//
// tasks.Expect builds the shell line PRD principle P11 has a worker run
// before it creates a branch, from the same values the assertion compares
// against, and this appends it to the task's instructions file under a
// heading of its own once the copy is proven, when the file does not already
// carry the line. The instructions file is the one thing a worker is started
// with and the place intake wrote the coordinator's text, so it is the
// minimal place a line the worker is to obey can go. It is detection by an
// obedient worker and not prevention, which Expect's doc says.

// errTaskNotDispatched reports a dispatch asked of a task that does not
// resolve Dispatched from its history: it is queued, already running, ended,
// or decided by a higher tier. Nothing is recorded. It and the two below are
// unexported because their one consumer is dispatchDue's log: nothing outside
// this package asks for a dispatch yet, and a type nothing consumes answers
// to nobody.
var errTaskNotDispatched = errors.New("service: the task does not resolve dispatched, so there is nothing to dispatch")

// errTaskLaunched reports a dispatch asked of a task whose latest attempt
// already names a terminal session: it was launched, and whether that
// terminal is gone is reconciliation's question, answered by reconcile.go's
// table from a probe, never by dispatching again. A second dispatch would
// start a second worker in the copy beside one that may still be there,
// which is PRD section 11's two supervisors for one task. Nothing is
// recorded.
var errTaskLaunched = errors.New("service: the task's latest attempt already runs in a terminal session, so a restart is a relaunch question and not a dispatch")

// errTaskRecord reports a task record a dispatch cannot cut a copy from as
// recorded: no repository, a reserved copy path that is not home.TaskCopy, or
// no instructions file. Nothing is recorded; the record is the thing to fix.
var errTaskRecord = errors.New("service: the task's record cannot be dispatched as recorded")

// isolationRefused reports a dispatch whose copy failed PRD principle P11's
// assertion. The refusal has been recorded in the task's history as an
// "isolation-refused" event carrying Reason, which is the closed token
// internal/taskstate defines, and the task resolves Failed from it; what was
// found is in Cause, which went to the service log and not into the record.
// It unwraps to tasks.ErrNotIsolated, which is the class a caller tests.
type isolationRefused struct {
	// Task names the task.
	Task string
	// Attempt is the attempt the dispatch was claimed as.
	Attempt int64
	// Reason is the recorded token.
	Reason taskstate.IsolationRefusal
	// Event is the entry recorded.
	Event store.TaskEvent
	// Cause is the assertion's own refusal, or the observation's failure
	// when the path could not be observed as a repository at all.
	Cause error
}

// Error names the task, the token and the cause.
func (e *isolationRefused) Error() string {
	return fmt.Sprintf("service: task %s attempt %d was refused a worker (%s): %v", e.Task, e.Attempt, e.Reason, e.Cause)
}

// Unwrap makes every refused dispatch match tasks.ErrNotIsolated.
func (e *isolationRefused) Unwrap() error { return tasks.ErrNotIsolated }

// secondCheckHeading is the heading the second check is appended to the
// instructions under. It is the marker that keeps the append idempotent
// across attempts, together with the line itself.
const secondCheckHeading = "## Isolation check, written by the program at dispatch"

// dispatchDue dispatches every task that resolves Dispatched and that this
// service has not already dispatched since it opened, and logs what came of
// each. It is called at open, after the queued tasks have been released, and
// after every release, which are the moments a task can have become
// Dispatched. Each dispatch runs on the caller's path, which for a release
// after a run settled is that run's segment; a clone of a local checkout is
// what that costs, and nothing here moves it to a goroutine of the service's.
//
// A task this service holds a slot for is passed over, whether that slot is a
// proof, a dispatch in flight, or a dispatch that ended without a proof, so
// one service begins at most one attempt of a task however it came out: a
// dispatch that failed after its claim, for a reason other than the
// assertion, is retried at the next open and not at the next release, which
// is what keeps a failure that repeats from adding an attempt per run that
// settles. A task refused resolves Failed from the entry the refusal appended
// and is kept out by the resolver as well as by the slot.
//
// A dispatch that reaches proven is launched in the same pass, through
// launch.go, and the slot covers the launch as well: a launch that failed is
// not retried until the next open either, because the attempt it would be
// launching was dispatch's and a second launch of one attempt would be a
// second process against one record. What came of the launch is logged, and
// a refusal of the launch's own assertion is recorded the way dispatch's is.
func (s *Service) dispatchDue(ctx context.Context) {
	resolved, err := s.tasks().All(ctx)
	if err != nil {
		s.log.Printf("dispatching: the tasks could not be read: %v", err)
		return
	}
	for _, r := range resolved {
		if r.State.State != taskstate.Dispatched || r.State.Source != taskstate.SourceEvent {
			continue
		}
		if _, held := s.isolationOf(r.Record.ID); held {
			continue
		}
		// The slot is taken for the service's lifetime, so a task that is a
		// relaunch question is passed over before the slot rather than
		// refused behind it, where it would keep a relaunch out too.
		// dispatchClaimed makes the same refusal for a caller below this.
		attempts, err := s.store.TaskAttempts(ctx, r.Record.ID)
		if err != nil {
			s.log.Printf("dispatching: the attempts of task %s could not be read: %v", r.Record.ID, err)
			continue
		}
		if _, launched := latestSession(attempts); launched {
			s.log.Printf("task %s was not dispatched here: %v", r.Record.ID, errTaskLaunched)
			continue
		}
		d, err := s.dispatchTask(ctx, r.Record.ID)
		switch {
		case err == nil:
			s.log.Printf("task %s was dispatched: its copy at %s is proven isolated", r.Record.ID, d.proof.Copy)
		case errors.Is(err, store.ErrTaskMoved):
			s.log.Printf("task %s was not dispatched here: %v", r.Record.ID, err)
			continue
		default:
			s.log.Printf("task %s was not dispatched: %v", r.Record.ID, err)
			continue
		}
		launched, err := s.launchTask(ctx, d)
		var undeclared *agents.CapabilityError
		switch {
		case err == nil:
			s.log.Printf("task %s attempt %d was launched: its worker runs in terminal session %s",
				d.id, d.attempt, launched.Reference())
		case errors.As(err, &undeclared):
			s.log.Printf("task %s attempt %d was dispatched and not launched, and stays dispatched: %v",
				d.id, d.attempt, err)
		default:
			s.log.Printf("task %s attempt %d was not launched: %v", d.id, d.attempt, err)
		}
	}
}

// dispatched is what a dispatch that reached proven established, and what
// the launch takes: the proof, the attempt the dispatch claimed, the
// expectation the copy was asserted against, the instructions file the
// worker is started with, and the history sequence the dispatch was decided
// from, which anchors a refusal the launch records.
type dispatched struct {
	id           string
	proof        tasks.Isolation
	attempt      int64
	want         tasks.Expectation
	instructions string
	anchor       int64
	// note is text the launch appends to the instructions after the file's
	// own, and empty for a first launch. A relaunch fills it with PRD
	// section 11's note about what already happened; relaunch.go owns the
	// text.
	note string
}

// dispatchTask dispatches one task, on the terms this file's opening comment
// states, and returns what it established. The order of what it does is the order of
// the records it writes: the attempt claims the dispatch, the primary
// snapshot records what was read, the clone is cut, the copy is observed and
// asserted, and the copy's snapshot and the proof are written last. A task
// that does not resolve Dispatched is errTaskNotDispatched; a record that
// cannot be dispatched as written is errTaskRecord; a dispatch another caller
// claimed first is store.ErrTaskMoved; a copy that fails the assertion is an
// *isolationRefused, recorded. Every other error is a failure of a read or a
// write, and leaves the attempt standing for a retry to find what it left.
func (s *Service) dispatchTask(ctx context.Context, id string) (dispatched, error) {
	settle, err := s.claimDispatch(id)
	if err != nil {
		return dispatched{}, err
	}
	d, err := s.dispatchClaimed(ctx, id)
	settle(d.proof)
	return d, err
}

// claimDispatch takes the task's slot, so a second pass over the tasks does
// not begin a second attempt behind this one, and returns the function that
// settles it with what the dispatch ended at: the proof, or the zero value
// for a dispatch that ended without one. The slot is kept either way, which
// is dispatchDue's one-attempt-per-service rule. The store's anchor is what
// refuses a second dispatcher in another service or on another path; this
// is the cheaper refusal within one.
func (s *Service) claimDispatch(id string) (func(proof tasks.Isolation), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isolated == nil {
		// Open makes the map; a service built by hand in this package's
		// tests may not have, and a nil map is no slot at all.
		s.isolated = make(map[string]tasks.Isolation)
	}
	if _, taken := s.isolated[id]; taken {
		return nil, fmt.Errorf("service: task %s has already been dispatched here", id)
	}
	s.isolated[id] = tasks.Isolation{}
	return func(proof tasks.Isolation) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.isolated[id] = proof
	}, nil
}

// isolationOf reports the proof this service holds for a task, and whether it
// holds a slot for it at all: a slot with an unproven value is a dispatch in
// flight or one that ended without a proof, and Proven is what tells a proof
// from either. It is the one reader of the slot map, and the slot is this
// service's memory: a service that restarts holds nothing and dispatches the
// task again, which is the retry this file describes.
func (s *Service) isolationOf(id string) (tasks.Isolation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	proof, held := s.isolated[id]
	return proof, held
}

// dispatchClaimed is dispatchTask once the in-memory slot is taken.
func (s *Service) dispatchClaimed(ctx context.Context, id string) (dispatched, error) {
	resolved, err := s.tasks().Resolved(ctx, id)
	if err != nil {
		return dispatched{}, err
	}
	if resolved.State.State != taskstate.Dispatched || resolved.State.Source != taskstate.SourceEvent {
		return dispatched{}, fmt.Errorf("%w: task %s resolves %s from %s", errTaskNotDispatched, id,
			resolved.State.State, resolved.State.Source)
	}
	record := resolved.Record
	e, err := s.expectationOf(ctx, record)
	if err != nil {
		return dispatched{}, err
	}
	repository, copyPath, instructions, gatePath, want, secondCheck :=
		e.repository, e.copyPath, e.instructions, e.gatePath, e.want, e.secondCheck

	// The claim. The anchor is the highest attempt read now, through the
	// attempts rather than the snapshot's copy of them, so what is anchored
	// is what this call saw.
	attempts, err := s.store.TaskAttempts(ctx, id)
	if err != nil {
		return dispatched{}, err
	}
	var latest int64
	for _, a := range attempts {
		latest = max(latest, a.Sequence)
	}
	if _, launched := latestSession(attempts); launched {
		return dispatched{}, fmt.Errorf("%w: task %s attempt %d", errTaskLaunched, id, latest)
	}
	if s.betweenAttemptReadAndClaim != nil {
		s.betweenAttemptReadAndClaim()
	}
	attempt, err := s.store.BeginTaskAttempt(ctx, id, latest)
	if err != nil {
		return dispatched{}, err
	}

	// The primary, read only, and recorded before the clone that reads it
	// again.
	base, err := s.recordPrimary(ctx, id, attempt.Sequence, repository)
	if err != nil {
		return dispatched{}, err
	}

	// The copy: whatever stands at the path, or a fresh clone when nothing
	// does.
	if _, err := os.Lstat(copyPath); errors.Is(err, os.ErrNotExist) {
		if err := s.cutTaskCopy(ctx, record, repository.WorkingPath, copyPath, gatePath, base); err != nil {
			return dispatched{}, err
		}
	} else if err != nil {
		return dispatched{}, fmt.Errorf("service: reading the copy of task %s at %s: %w", id, copyPath, err)
	}

	// The observation and the proof.
	env := agents.WorkerEnvironment(os.Environ(), nil)
	obs, err := vcs.Observe(ctx, copyPath, vcs.WithRedactor(redact.New()))
	if err != nil {
		if errors.Is(err, vcs.ErrNotARepository) {
			return dispatched{}, s.refuseIsolation(ctx, id, attempt.Sequence, resolved.State.Basis.EventSequence,
				taskstate.RefusedNotIsolated, err)
		}
		return dispatched{}, fmt.Errorf("service: observing the copy of task %s: %w", id, err)
	}
	proof, err := tasks.AssertIsolated(obs, env, want)
	if err != nil {
		reason, refused := tasks.Refusal(err)
		if !refused {
			return dispatched{}, fmt.Errorf("service: asserting the copy of task %s: %w", id, err)
		}
		return dispatched{}, s.refuseIsolation(ctx, id, attempt.Sequence, resolved.State.Basis.EventSequence, reason, err)
	}

	// The copy's snapshot, then the line the worker is told to run.
	snapshot, err := copySnapshot(ctx, proof.Copy)
	if err != nil {
		return dispatched{}, fmt.Errorf("service: reading the references of the copy of task %s: %w", id, err)
	}
	if err := s.store.SetTaskStartSnapshot(ctx, id, snapshot); err != nil {
		return dispatched{}, err
	}
	if err := appendSecondCheck(instructions, secondCheck); err != nil {
		return dispatched{}, fmt.Errorf("service: writing the second check into the instructions of task %s: %w", id, err)
	}
	return dispatched{
		id: id, proof: proof, attempt: attempt.Sequence, want: want, instructions: instructions,
		anchor: resolved.State.Basis.EventSequence,
	}, nil
}

// expectation is what a task's copy is asserted against and launched with,
// derived from the task's record: the repository it works in, the copy path
// the record reserved, its instructions file, the gate repository its one
// remote must resolve to, and tasks.Expect's expectation and second check.
// Dispatch and relaunch derive it through expectationOf, so the two cannot
// read the record differently.
type expectation struct {
	repository   store.Repository
	copyPath     string
	instructions string
	gatePath     string
	want         tasks.Expectation
	secondCheck  string
}

// expectationOf derives the expectation from a task's record, refusing with
// errTaskRecord a record that names no repository, reserved its copy
// somewhere other than home.TaskCopy, has no instructions file, or that
// tasks.Expect refuses. It reads and writes nothing of the task's.
func (s *Service) expectationOf(ctx context.Context, record store.Task) (expectation, error) {
	id := record.ID
	repositoryID, ok := record.RepositoryID.Get()
	if !ok {
		return expectation{}, fmt.Errorf("%w: task %s names no repository to cut a copy of", errTaskRecord, id)
	}
	repository, err := s.store.Repository(ctx, repositoryID)
	if err != nil {
		return expectation{}, fmt.Errorf("service: reading the repository task %s works in: %w", id, err)
	}
	copyPath, err := s.home.TaskCopy(id)
	if err != nil {
		return expectation{}, err
	}
	if filepath.Clean(record.WorktreePath) != copyPath {
		return expectation{}, fmt.Errorf("%w: task %s reserved its copy at %s and this home puts it at %s",
			errTaskRecord, id, record.WorktreePath, copyPath)
	}
	instructions, err := s.home.TaskInstructions(id)
	if err != nil {
		return expectation{}, err
	}
	if _, err := os.Stat(instructions); err != nil {
		return expectation{}, fmt.Errorf("%w: task %s has no instructions file at %s: %w", errTaskRecord, id, instructions, err)
	}
	gatePath, err := s.gatePathOf(ctx, repository)
	if err != nil {
		return expectation{}, err
	}
	want, secondCheck, err := tasks.Expect(s.home, id, record.Shape, gatePath, repository.WorkingPath, record.Branch.Or(""))
	if err != nil {
		return expectation{}, fmt.Errorf("%w: task %s: %w", errTaskRecord, id, err)
	}
	return expectation{repository: repository, copyPath: copyPath, instructions: instructions, gatePath: gatePath,
		want: want, secondCheck: secondCheck}, nil
}

// gatePathOf is the gate repository of a repository, which a task's copy
// names as its one remote and must stand outside of. A repository bound to no
// gate is refused naming the command that binds one, on branchInGate's terms.
func (s *Service) gatePathOf(ctx context.Context, repository store.Repository) (string, error) {
	binding, err := s.store.GateBinding(ctx, repository.WorkingPath)
	if err != nil {
		return "", fmt.Errorf("service: %s is not bound to a gate, so a task of it cannot be cut a copy; "+
			"run assistant init there: %w", repository.WorkingPath, err)
	}
	gatePath, err := gate.RepositoryFor(s.home.Root(), binding.GateID)
	if err != nil {
		return "", fmt.Errorf("service: locating the gate repository of %s: %w", repository.ID, err)
	}
	return gatePath, nil
}

// recordPrimary reads the primary checkout through internal/vcs's read-only
// operations - HEAD, the branch it is on, every local branch - records them
// as the attempt's primary snapshot, and returns the default branch's tip,
// which is the base a task's copy starts from. A primary with no local
// default branch is refused: the base would otherwise be invented.
func (s *Service) recordPrimary(ctx context.Context, id string, attempt int64, repository store.Repository) (string, error) {
	primary, err := vcs.OpenWorktree(ctx, repository.WorkingPath, vcs.WithRedactor(redact.New()))
	if err != nil {
		return "", fmt.Errorf("service: opening the primary checkout of task %s: %w", id, err)
	}
	head, err := primary.ResolveCommit(ctx, "HEAD")
	if err != nil {
		return "", fmt.Errorf("service: reading HEAD of the primary checkout of task %s: %w", id, err)
	}
	snapshot := store.PrimarySnapshot{TaskID: id, Attempt: attempt, Head: head}
	switch branch, err := primary.HeadBranch(ctx); {
	case err == nil:
		snapshot.HeadBranch = store.Known(branch)
	case !errors.Is(err, vcs.ErrDetachedHead):
		return "", fmt.Errorf("service: reading the branch of the primary checkout of task %s: %w", id, err)
	}
	branches, err := primary.ListRefs(ctx, "refs/heads/")
	if err != nil {
		return "", fmt.Errorf("service: reading the branches of the primary checkout of task %s: %w", id, err)
	}
	snapshot.Branches = make([]store.SnapshotRef, 0, len(branches))
	for _, b := range branches {
		snapshot.Branches = append(snapshot.Branches, store.SnapshotRef{Name: b.Name, Commit: b.Commit})
	}
	if repository.DefaultBranch == "" {
		return "", fmt.Errorf("%w: the repository task %s works in names no default branch to start from", errTaskRecord, id)
	}
	base, err := primary.ResolveCommit(ctx, "refs/heads/"+repository.DefaultBranch)
	if err != nil {
		return "", fmt.Errorf("service: the primary checkout of task %s has no local branch %s to start the task from: %w",
			id, repository.DefaultBranch, err)
	}
	if _, err := s.store.RecordPrimarySnapshot(ctx, snapshot); err != nil {
		return "", err
	}
	return base, nil
}

// cutTaskCopy clones the primary into the task's copy path and shapes the
// clone for the task's shape: a delivery copy gets the gate as its one remote
// and the task's branch at base; an investigation copy gets no remote and a
// detached HEAD at base. A clone that cannot be shaped is removed, on the
// terms this file's opening comment states, and the error reported.
func (s *Service) cutTaskCopy(ctx context.Context, record store.Task, primary, copyPath, gatePath, base string) error {
	if err := os.MkdirAll(filepath.Dir(copyPath), 0o700); err != nil {
		return fmt.Errorf("service: creating the directory of task %s: %w", record.ID, err)
	}
	cloned, err := vcs.Clone(ctx, primary, copyPath, "", vcs.WithRedactor(redact.New()))
	if err != nil {
		return fmt.Errorf("service: cloning the primary checkout for task %s: %w", record.ID, err)
	}
	shape := func() error {
		switch record.Shape {
		case store.TaskDelivery:
			if err := cloned.SetRemote(ctx, gate.RemoteName, gatePath); err != nil {
				return fmt.Errorf("pointing %s at the gate: %w", gate.RemoteName, err)
			}
			if err := cloned.SwitchToNewBranch(ctx, record.Branch.Or(""), base); err != nil {
				return fmt.Errorf("creating the task's branch %s at %s: %w", record.Branch.Or(""), base, err)
			}
		case store.TaskInvestigation:
			if err := cloned.Detach(ctx, base); err != nil {
				return fmt.Errorf("detaching at %s: %w", base, err)
			}
		default:
			return fmt.Errorf("shape %q is not one a copy is cut for", record.Shape)
		}
		return nil
	}
	if err := shape(); err != nil {
		if removeErr := os.RemoveAll(copyPath); removeErr != nil {
			s.log.Printf("the clone cut for task %s could not be shaped and was not removed either: %v", record.ID, removeErr)
		}
		return fmt.Errorf("service: shaping the copy of task %s: %w", record.ID, err)
	}
	return nil
}

// refuseIsolation records the refusal as the task's "isolation-refused"
// event, anchored to the history the dispatch was decided from, and returns
// the typed refusal. The token is checked against the kind's own decoder
// before it is appended, because the store records any text and
// internal/taskstate's table is the one place the vocabulary is defined. The
// assertion's own words go to the log: the record carries the token and the
// history is not the place for prose.
func (s *Service) refuseIsolation(ctx context.Context, id string, attempt, anchor int64,
	reason taskstate.IsolationRefusal, cause error) error {
	row, ok := taskstate.Lookup(isolationRefusedKind)
	if !ok || row.Detail == nil {
		return fmt.Errorf("service: the %q kind has no decoder to check %q against", isolationRefusedKind, reason)
	}
	if err := row.Detail(string(reason)); err != nil {
		return fmt.Errorf("service: task %s: %w", id, err)
	}
	s.log.Printf("task %s attempt %d was refused a worker: %v", id, attempt, cause)
	event, err := s.store.AppendTaskEventAfter(ctx, id, anchor, isolationRefusedKind, string(reason))
	if err != nil {
		return fmt.Errorf("service: recording the refusal of task %s: %w", id, err)
	}
	return &isolationRefused{Task: id, Attempt: attempt, Reason: reason, Event: event, Cause: cause}
}

// isolationRefusedKind is the kind internal/taskstate gives the refusal. It is
// spelled here because that package exports the row and not the name; Lookup
// is what checks the spelling is one that package knows.
const isolationRefusedKind = "isolation-refused"

// copySnapshot reads what the proven copy holds: HEAD, the branch it is on,
// and every reference, through internal/vcs's read-only operations.
func copySnapshot(ctx context.Context, copyPath string) (store.CopySnapshot, error) {
	copied, err := vcs.OpenWorktree(ctx, copyPath, vcs.WithRedactor(redact.New()))
	if err != nil {
		return store.CopySnapshot{}, err
	}
	head, err := copied.ResolveCommit(ctx, "HEAD")
	if err != nil {
		return store.CopySnapshot{}, err
	}
	snapshot := store.CopySnapshot{Head: head, Refs: []store.SnapshotRef{}}
	switch branch, err := copied.HeadBranch(ctx); {
	case err == nil:
		snapshot.Branch = branch
	case !errors.Is(err, vcs.ErrDetachedHead):
		return store.CopySnapshot{}, err
	}
	refs, err := copied.ListRefs(ctx)
	if err != nil {
		return store.CopySnapshot{}, err
	}
	for _, r := range refs {
		snapshot.Refs = append(snapshot.Refs, store.SnapshotRef{Name: r.Name, Commit: r.Commit})
	}
	return snapshot, nil
}

// appendSecondCheck appends the second check to the instructions file under
// its own heading, unless the file already carries the line, so a retry does
// not tell the worker twice.
func appendSecondCheck(path, line string) error {
	existing, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if strings.Contains(string(existing), line) {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	text := "\n\n" + secondCheckHeading + "\n\n" +
		"Before creating any branch or committing anything, run this line in the working copy you were started in " +
		"and stop if it exits non-zero: it checks that you are in the isolated copy the program cut for this task " +
		"and nowhere else.\n\n    " + line + "\n"
	if _, err := f.WriteString(text); err != nil {
		return err
	}
	return f.Sync()
}
