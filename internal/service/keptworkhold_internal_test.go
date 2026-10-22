package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/config"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// keepWorkIn records an open kept work entry in the subject's project,
// owned by a second task of that project, and returns it.
func keepWorkIn(t *testing.T, records *store.Store) store.KeptWork {
	t.Helper()
	ctx := t.Context()
	owner, err := home.NewTaskID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := records.CreateTask(ctx, store.Task{
		ID: owner, Shape: store.TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: "/worktrees/" + owner,
		RepositoryID: store.Known("subject"), Branch: store.Known("task/kept"),
	}, store.Disposition{}); err != nil {
		t.Fatalf("recording the kept copy's owner: %v", err)
	}
	entry, _, err := records.RecordKeptWork(ctx, store.KeptCopy{TaskID: owner, CopyPath: "/worktrees/" + owner,
		Holds: []string{"aaaa"}, Reason: "unproven", Against: "mmmm"})
	if err != nil {
		t.Fatalf("recording kept work: %v", err)
	}
	return entry
}

// Kept work recorded after a new task's dispatch claimed its attempt, while
// its copy was being cut and proven, still keeps its worker from starting:
// the launch claim asks the hold in the transaction that would record it,
// refuses with the typed refusal, starts no process and records no
// session, and gives the task's slot back so a later pass dispatches it.
// The positive control is TestLaunchRecordsTheRoutedAdapterAndReason, the
// same launch with nothing kept. Without the launch claim's check the worker
// starts.
func TestKeptWorkRecordedBeforeTheLaunchKeepsANewWorkerFromStarting(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	ctx := t.Context()
	subject := newLaunchSubject(t, workerStandIn(t))
	subject.service.cfg.Agent = []string{subject.agent.Runner().Name()}
	d, err := subject.service.dispatchTask(ctx, subject.task)
	if err != nil {
		t.Fatalf("dispatching: %v", err)
	}
	entry := keepWorkIn(t, subject.records)

	launched, err := subject.service.launchTask(ctx, d)
	var held *store.DispatchHeldError
	if launched != nil || !errors.As(err, &held) || len(held.Entries) != 1 || held.Entries[0].ID != entry.ID {
		t.Fatalf("the launch = %v, %v; want it refused naming entry %d", launched, err, entry.ID)
	}
	attempts, err := subject.records.TaskAttempts(ctx, subject.task)
	if err != nil || len(attempts) != 1 || attempts[0].SessionRef.IsKnown() {
		t.Fatalf("the task's attempts = %+v, %v; want one with no session", attempts, err)
	}
	if errs := subject.service.closeSessions(); len(errs) != 0 {
		t.Fatalf("closing sessions: %v", errs)
	}
	subject.service.mu.Lock()
	_, running := subject.service.launched[subject.task]
	subject.service.mu.Unlock()
	if running {
		t.Fatal("the held launch left a worker this service holds")
	}
	if _, slot := subject.service.isolationOf(subject.task); slot {
		t.Fatal("the held launch kept the task's slot, so no later pass would dispatch it")
	}
}

// A dispatch pass that runs while a held dispatch still has the task's slot
// passes the task over, and the entry that held it closes inside that
// window: exactly the order where a removal's pass is lost. The window is
// held open by the seam between the refusal and the slot's release, inside
// which the entry is answered and a whole pass runs and is shown to skip the
// task. The release then reads the hold again, finds nothing holding the
// task, and asks for another pass, which dispatches it. Without that read
// the task waits with no open entry and nothing asking for it.
func TestAnEntryClosedWhileAHeldDispatchHasTheSlotStillDispatchesTheTask(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	ctx := t.Context()
	subject := newDispatchSubject(t)
	subject.service.stopCtx = ctx
	entry := keepWorkIn(t, subject.records)

	inWindow := false
	subject.service.betweenHeldAndRelease = func() {
		inWindow = true
		subject.service.betweenHeldAndRelease = nil
		if _, err := subject.records.ClaimKeptWorkLanding(ctx, entry.ID, store.ResolvedByMachineInterface()); err != nil {
			t.Errorf("answering the entry: %v", err)
		} else if _, err := subject.records.RecordKeptWorkLanding(ctx, entry.ID, "run-landing"); err != nil {
			t.Errorf("answering the entry: %v", err)
		}
		subject.service.dispatchDue(ctx)
		if attempts, err := subject.records.TaskAttempts(ctx, subject.task); err != nil || len(attempts) != 0 {
			t.Errorf("the pass inside the window dispatched %+v, %v; want it to pass the taken slot over", attempts, err)
		}
	}
	if _, err := subject.service.dispatchTask(ctx, subject.task); !errors.Is(err, store.ErrDispatchHeld) {
		t.Fatalf("the first dispatch = %v; want it held", err)
	}
	if !inWindow {
		t.Fatal("the held dispatch never reached the window between its refusal and its release")
	}
	subject.service.work.Wait()
	attempts, err := subject.records.TaskAttempts(ctx, subject.task)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("after the entry closed the task has attempts %+v, %v; want the pass its release asked for to have dispatched it", attempts, err)
	}
}

// A launch whose claim committed and whose worker never started leaves its
// task held like a fresh one. Attempt 1 is dispatched and its launch claimed
// with no session recorded, the record a provider that refused the process
// leaves; kept work is then recorded in the project, and a service over the
// same records, which is what the next start is, dispatches nothing: the
// task waits and no attempt 2 is claimed. Once the entry is answered the
// same pass dispatches attempt 2 and its worker starts, which is the
// positive control. Without the hold reading a recorded session alone, the
// claim exempts the task and attempt 2's worker starts while the entry is
// open.
func TestALaunchClaimedWhoseWorkerNeverStartedIsHeldAtTheNextDispatch(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	// Attempt 2 is dispatched over the copy attempt 1 left standing, which
	// the process sweep has to clear first, on sweep_internal_test.go's
	// terms for a host whose sweeps do not.
	const unestablished = "that a launch claimed with no worker started leaves its task held until the entry is answered"
	requireSweepClearsHere(t, unestablished)
	ctx := t.Context()
	subject := newLaunchSubject(t, workerStandIn(t))
	subject.service.cfg.Agent = []string{subject.agent.Runner().Name()}
	subject.service.stopCtx = ctx
	d, err := subject.service.dispatchTask(ctx, subject.task)
	if err != nil {
		t.Fatalf("dispatching: %v", err)
	}
	if err := subject.records.ClaimAttemptLaunch(ctx, subject.task, d.attempt); err != nil {
		t.Fatalf("claiming attempt 1's launch: %v", err)
	}
	entry := keepWorkIn(t, subject.records)

	// The next start: a service holding no slot for the task.
	subject.service.mu.Lock()
	delete(subject.service.isolated, subject.task)
	subject.service.mu.Unlock()
	subject.service.dispatchDue(ctx)
	attempts, err := subject.records.TaskAttempts(ctx, subject.task)
	if err != nil || len(attempts) != 1 || attempts[0].SessionRef.IsKnown() {
		t.Fatalf("with the entry open the task has attempts %+v, %v; want attempt 1 alone, with no worker", attempts, err)
	}

	if _, err := subject.records.ClaimKeptWorkLanding(ctx, entry.ID, store.ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	if _, err := subject.records.RecordKeptWorkLanding(ctx, entry.ID, "run-landing"); err != nil {
		t.Fatal(err)
	}
	subject.service.dispatchDue(ctx)
	attempts, err = subject.records.TaskAttempts(ctx, subject.task)
	if err != nil || len(attempts) != 2 || !attempts[1].SessionRef.IsKnown() {
		fatalUnlessSweepsStoppedClearing(t, unestablished,
			"once the entry was answered the task has attempts %+v, %v; want attempt 2 with its worker started", attempts, err)
	}
}

// Kept work that appears while a worker handoff is under way is not a
// worker failure. The primary worker's process could not start, which is
// the handoff switchWorker is asked with no worker (launchTask asks it so
// when host.Create fails with a failure agents.FallbackAllowed admits), so
// the configured backup is selected and its attempt dispatched; kept work
// is recorded in the project between the attempts' read and the claim, so
// the claim refuses the backup's attempt as it refuses any new task's. No
// worker-fallback hold is raised, the task's slot is given back, and once
// the entry is answered a dispatch pass starts the task on its own, on the
// backup the sealed selection names. Without the handoff reading the
// refusal as kept work the task is left held on the fallback's hold with
// one attempt and no session, and the answer dispatches nothing.
func TestKeptWorkRefusingAWorkerHandoffRaisesNoFallbackHold(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	// The handoff dispatches the backup's attempt over the copy the first
	// attempt left standing, which the process sweep has to clear before
	// the claim, on sweep_internal_test.go's terms for a host whose sweeps
	// do not.
	const unestablished = "that kept work refusing a worker handoff raises no fallback hold"
	requireSweepClearsHere(t, unestablished)
	ctx := t.Context()
	// The backup answers the instructions file's line, "work", on the
	// stream the Claude adapter reads, so a worker that started shows
	// its answer in the capture and one that did not shows nothing.
	backup := standin.New(t, standin.Script{Worker: &standin.WorkerScript{
		Exchanges: []standin.Exchange{{Contains: "work", Reply: backupAnswer}},
	}})
	subject := newLaunchSubject(t, backup)
	s := subject.service
	role := config.Role{Agent: "cursor", Fallback: &config.Role{Agent: "claude"}}
	s.cfg.Roles = config.Roles{config.RoleWorker: role}
	s.terminals = heldTerminals{s}
	s.stopCtx, s.stopCancel = context.WithCancel(ctx)
	t.Cleanup(func() { s.stopCancel(); s.stopWork(); s.work.Wait() })
	// The seam runs in every dispatch between its read and its claim; the
	// first is the task's own dispatch, and the second the handoff's, which
	// is where the kept work is recorded.
	var entry store.KeptWork
	claims := 0
	s.betweenAttemptReadAndClaim = func() {
		if claims++; claims == 2 {
			entry = keepWorkIn(t, subject.records)
		}
	}
	d, err := s.dispatchTask(ctx, subject.task)
	if err != nil {
		t.Fatalf("dispatching: %v", err)
	}
	task, err := s.store.Task(ctx, subject.task)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.switchWorker(ctx, task, d.attempt, nil, nil, role.Agent, role, agents.FailureUnavailable); !errors.Is(err, store.ErrDispatchHeld) {
		fatalUnlessSweepsStoppedClearing(t, unestablished, "the handoff = %v; want the backup's attempt refused for kept work", err)
	}
	if entry.ID == 0 {
		t.Fatal("the handoff never reached the claim where the kept work was recorded")
	}
	if _, selected, err := s.store.TaskFallback(ctx, subject.task); err != nil || !selected {
		t.Fatalf("the handoff was not recorded: %v, %v", selected, err)
	}
	holds, err := s.store.OpenHolds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range holds {
		if h.TaskID.Or("") == subject.task {
			t.Fatalf("the refused handoff raised the hold %s", h.Key)
		}
	}
	if _, slot := s.isolationOf(subject.task); slot {
		t.Fatal("the refused handoff kept the task's slot, so no pass would dispatch it")
	}
	if _, err := subject.records.ClaimKeptWorkLanding(ctx, entry.ID, store.ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	if _, err := subject.records.RecordKeptWorkLanding(ctx, entry.ID, "run-landing"); err != nil {
		t.Fatal(err)
	}
	s.dispatchDue(ctx)
	deadline := time.Now().Add(30 * time.Second)
	for {
		attempts, err := subject.records.TaskAttempts(ctx, subject.task)
		if err != nil {
			t.Fatal(err)
		}
		if len(attempts) == 2 && attempts[1].SessionRef.IsKnown() {
			if attempts[1].Adapter.Or("") != "claude" {
				t.Fatalf("attempt 2 was started on %s, want the backup, claude", attempts[1].Adapter.Or(""))
			}
			break
		}
		if !time.Now().Before(deadline) {
			fatalUnlessSweepsStoppedClearing(t, unestablished, "after the answer the task has attempts %+v; want attempt 2 started on the backup", attempts)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The backup started and read the work: its answer is in the capture,
	// which a worker that exited before reading never writes.
	host, worker, ok := s.sessionOf(subject.task)
	if !ok {
		t.Fatal("the service holds no session for the task after the answer")
	}
	waitCaptured(t, host, worker, backupAnswer)
}

// backupAnswer is what the backup worker answers the work line with.
const backupAnswer = "the backup is on the work"
