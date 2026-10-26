package service

import (
	"errors"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
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

// PRD section 13 and principle P6: a retired pass is never begun again.
// The coordinator's relaunch, a retry on a relaunch hold left open, and
// reconciliation at a restart are each refused and start no worker. The
// positive control is the same relaunch of a pass not retired, which begins
// one attempt.
func TestARetiredPassIsNeverRelaunched(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	ctx := t.Context()

	control := passRelaunchSubject(t)
	if _, err := control.restart.relaunchTask(ctx, control.task); err != nil {
		t.Fatalf("relaunching a pass not retired: %v", err)
	}
	if got := attemptsOf(t, control.records, control.task); got != 2 {
		t.Fatalf("the control relaunch left %d attempts, want 2", got)
	}

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
