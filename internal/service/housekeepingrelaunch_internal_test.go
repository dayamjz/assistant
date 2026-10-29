package service

import (
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/config"
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
