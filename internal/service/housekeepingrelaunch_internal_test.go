package service

import (
	"errors"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/checkpoints"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// passRelaunchSubject is relaunchSubject over a housekeeping pass rather
// than a delivery task: its first attempt was dispatched and launched under
// the housekeeping role, and its worker then ended, standing in a service
// that has since been replaced.
func passRelaunchSubject(t *testing.T) *relaunchSubject {
	t.Helper()
	ctx := t.Context()
	dispatch := newDispatchSubjectOf(t, func(id, copyPath string) (store.Task, store.Disposition) {
		return store.Task{
			ID: id, Shape: store.TaskInvestigation, Project: "one", Mode: "interactive", WorktreePath: copyPath,
			RepositoryID: store.Known("subject"), Role: config.RoleHousekeeping, Housekeeping: true,
		}, store.Disposition{Delivered: taskstate.Delivered}
	})
	sub := launchSubjectOver(t, workerStandIn(t), dispatch)
	sub.service.terminals = heldTerminals{sub.service}
	d, err := sub.service.dispatchTask(ctx, sub.task)
	if err != nil {
		t.Fatalf("dispatching the pass: %v", err)
	}
	first, err := sub.service.launchTask(ctx, d)
	if err != nil {
		t.Fatalf("launching the pass: %v", err)
	}
	provider, _, _ := sub.service.sessionOf(sub.task)
	for _, err := range sub.service.closeSessions() {
		t.Fatalf("ending the pass's worker: %v", err)
	}
	waitLiveness(t, provider, first.Reference().String(), taskstate.Dead)
	restart, agent := restartedService(t, sub)
	return &relaunchSubject{launchSubject: sub, first: first, restart: restart, restartA: agent}
}

// attemptsOf is how many attempts the task has.
func attemptsOf(t *testing.T, records *store.Store, id string) int {
	t.Helper()
	attempts, err := records.TaskAttempts(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return len(attempts)
}

// PRD section 10's roles: a pass runs under the housekeeping role, which
// resolves to the fix row when the roles table does not name housekeeping,
// so with the default table its worker is the fixer's agent, cursor, and the
// attempt records the housekeeping role by name.
func TestAPassLaunchesOnTheFixRow(t *testing.T) {
	t.Parallel()
	sub := passRelaunchSubject(t)
	attempts, err := sub.records.TaskAttempts(t.Context(), sub.task)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts %+v (%v)", attempts, err)
	}
	if got := attempts[0].Adapter.Or(""); got != agents.CursorName {
		t.Fatalf("the pass launched on %q, want the fix row's %s", got, agents.CursorName)
	}
	if got := attempts[0].Role.Or(""); got != config.RoleHousekeeping {
		t.Fatalf("the attempt records role %q, want %s", got, config.RoleHousekeeping)
	}
}

// The positive control for TestARetiredPassIsNeverRelaunched: the same
// relaunch of a dead pass that is not retired begins one attempt, so the
// refusals there are the retirement's and not a pass's. It needs a process
// sweep that clears on this host, which the retired pass's refusal, made
// before any sweep, does not.
func TestAPassNotRetiredIsRelaunched(t *testing.T) {
	t.Parallel()
	requireSweepClearsHere(t, "that a pass not retired is relaunched")
	control := passRelaunchSubject(t)
	if _, err := control.restart.relaunchTask(t.Context(), control.task); err != nil {
		t.Fatalf("relaunching a pass not retired: %v", err)
	}
	if got := attemptsOf(t, control.records, control.task); got != 2 {
		t.Fatalf("the control relaunch left %d attempts, want 2", got)
	}
}

// PRD section 13 and principle P6: a retired pass is never begun again.
// The coordinator's relaunch, a retry on a relaunch hold left open, and
// reconciliation at a restart are each refused and start no worker, each
// before any sweep of its copy. The positive control is
// TestAPassNotRetiredIsRelaunched.
func TestARetiredPassIsNeverRelaunched(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	ctx := t.Context()

	sub := passRelaunchSubject(t)
	retire := store.Hold{Key: retireHold{Task: sub.task, Epoch: 1, Attempt: 1}.key(), TaskID: store.Known(sub.task),
		Subject: "retire?", Detail: "d"}
	if _, err := sub.records.RegisterHold(ctx, retire); err != nil {
		t.Fatal(err)
	}
	r := retirementOf(sub.task, 1, "")
	r.Hold, r.By = retire.Key, (ipc.Request{}).HoldResolver()
	if _, err := sub.records.RetireHousekeepingPass(ctx, r); err != nil {
		t.Fatalf("retiring the pass: %v", err)
	}

	if _, err := sub.restart.relaunchTask(ctx, sub.task); !errors.Is(err, errRelaunchRetired) || !errors.Is(err, store.ErrPassRetired) {
		t.Fatalf("the coordinator's relaunch of a retired pass = %v, want it refused", err)
	}
	if _, err := sub.records.RegisterHold(ctx, store.Hold{Key: relaunchBoundKey(sub.task, 1), TaskID: store.Known(sub.task),
		Subject: "whether to begin the work again", Detail: "d"}); err != nil {
		t.Fatal(err)
	}
	answered, err := sub.restart.respondTask(ctx, ipc.Request{}, machine.TaskRespondRequest{Task: sub.task,
		Hold: relaunchBoundKey(sub.task, 1), Answer: machine.AnswerRetry})
	if err != nil || !answered.Effect.RetryNotBegun() {
		t.Fatalf("a retry on a relaunch hold of a retired pass answered %+v, %v, want the retry recorded and nothing begun", answered, err)
	}
	sub.restart.reconcileTasks(ctx)
	if got := attemptsOf(t, sub.records, sub.task); got != 1 {
		t.Fatalf("the retired pass has %d attempts, want only the one it was retired with", got)
	}
	if err := refusalOf(errRelaunchRetired); !errors.Is(err, ipc.ErrRefused) {
		t.Fatalf("the refusal reaches the wire as %v, want refused", err)
	}
}

// PRD principle P6 and section 6's "an end it cannot verify ... writes
// nothing": an on-proof retirement whose proof is no longer current, because
// a newer run of the pass started after the proven one passed, refuses
// before it ends anything, so the worker this service holds keeps running.
// The run here starts after the loop chose the pass and before the
// retirement reads it again, which is the window the read-again closes.
func TestAnOnProofRetirementWhoseProofIsStaleEndsNoWorker(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	ctx := t.Context()
	dispatch := newDispatchSubjectOf(t, func(id, copyPath string) (store.Task, store.Disposition) {
		return store.Task{
			ID: id, Shape: store.TaskInvestigation, Project: "one", Mode: "interactive", WorktreePath: copyPath,
			RepositoryID: store.Known("subject"), Role: config.RoleHousekeeping, Housekeeping: true,
		}, store.Disposition{Delivered: taskstate.Delivered}
	})
	sub := launchSubjectOver(t, workerStandIn(t), dispatch)
	sub.service.terminals = heldTerminals{sub.service}
	sub.service.checkpoints = checkpoints.New(sub.records)
	d, err := sub.service.dispatchTask(ctx, sub.task)
	if err != nil {
		t.Fatalf("dispatching the pass: %v", err)
	}
	worker, err := sub.service.launchTask(ctx, d)
	if err != nil {
		t.Fatalf("launching the pass: %v", err)
	}
	promotePass(t, sub.records, sub.task)
	proven := passRun(t, sub.records, "run-proven", store.RunPassed, true)
	passRun(t, sub.records, "run-newer", store.RunRunning, false)

	err = sub.service.retireOnProof(ctx, sub.task, proven.ID)
	if !errors.Is(err, store.ErrPassMoves) {
		t.Fatalf("the on-proof retirement after a newer run started = %v, want ErrPassMoves", err)
	}
	provider, _, held := sub.service.sessionOf(sub.task)
	if !held {
		t.Fatal("the refused retirement let go of the pass's worker")
	}
	if got := provider.Probe(ctx, worker.Reference().String()).Liveness; got != taskstate.Live {
		t.Fatalf("the refused retirement left the worker %s, want it still live", got)
	}
	if _, found, err := sub.records.RetirementOf(ctx, sub.task); err != nil || found {
		t.Fatalf("the refused retirement wrote a record (%v)", err)
	}
}

// startsBehind reports whether a caller is queued on the branch's exclusion
// behind its holder.
func startsBehind(s *Service, key branchKey) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	gate, ok := s.starting[key]
	return ok && gate.waiting >= 2
}

// PRD principle P6: an on-proof retirement and a run of the pass's branch
// starting while the retirement ends the worker are ordered, never
// interleaved. The run here starts the way every run does, under its
// branch's exclusion, after the store answered that the retirement stands
// and before the worker is ended. Either the run lands first and the
// retirement ends nothing, or the retirement ends the worker and writes its
// record first and the run starts after it; a worker ended for a retirement
// then refused is the one outcome refused.
func TestARunStartingDuringAnOnProofRetirementIsOrderedWithIt(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	ctx := t.Context()
	dispatch := newDispatchSubjectOf(t, func(id, copyPath string) (store.Task, store.Disposition) {
		return store.Task{
			ID: id, Shape: store.TaskInvestigation, Project: "one", Mode: "interactive", WorktreePath: copyPath,
			RepositoryID: store.Known("subject"), Role: config.RoleHousekeeping, Housekeeping: true,
		}, store.Disposition{Delivered: taskstate.Delivered}
	})
	sub := launchSubjectOver(t, workerStandIn(t), dispatch)
	sub.service.terminals = heldTerminals{sub.service}
	sub.service.checkpoints = checkpoints.New(sub.records)
	d, err := sub.service.dispatchTask(ctx, sub.task)
	if err != nil {
		t.Fatalf("dispatching the pass: %v", err)
	}
	worker, err := sub.service.launchTask(ctx, d)
	if err != nil {
		t.Fatalf("launching the pass: %v", err)
	}
	promotePass(t, sub.records, sub.task)
	proven := passRun(t, sub.records, "run-proven", store.RunPassed, true)

	key := branchKey{repository: "subject", branch: hkBranch}
	started := make(chan struct{})
	sub.service.betweenRetirementCheckAndEnd = func() {
		go func() {
			defer close(started)
			release, err := sub.service.holdBranch(ctx, key)
			if err != nil {
				t.Errorf("taking the branch: %v", err)
				return
			}
			defer release()
			passRun(t, sub.records, "run-racing", store.RunRunning, false)
		}()
		// Wait until the run is in or is queued behind the retirement.
		for {
			select {
			case <-started:
				return
			default:
			}
			if startsBehind(sub.service, key) {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	err = sub.service.retireOnProof(ctx, sub.task, proven.ID)
	<-started
	_, retired, readErr := sub.records.RetirementOf(ctx, sub.task)
	if readErr != nil {
		t.Fatal(readErr)
	}
	provider, _, held := sub.service.sessionOf(sub.task)
	live := held && provider.Probe(ctx, worker.Reference().String()).Liveness == taskstate.Live
	switch {
	case err == nil && retired && !live:
		// The retirement went first; the run started after its record.
	case errors.Is(err, store.ErrPassMoves) && !retired && live:
		// The run went first; nothing was ended.
	default:
		t.Fatalf("the retirement answered %v with a record %v and the worker live %v: a worker was ended for a retirement that "+
			"did not stand, or a record was written beside a live worker", err, retired, live)
	}
}
