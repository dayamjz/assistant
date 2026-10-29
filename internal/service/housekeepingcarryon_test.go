package service_test

import (
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// PRD section 6's kept work, for a housekeeping pass (the review of #685 at
// b73ff76): a pass whose copy is kept with work not landed is carried on
// like any task's, as a new task of the same project, and that task is no
// pass, since the mark is written only at the coordinator's create, so it
// cannot run under the housekeeping role the program reserves for a pass.
// It is created under the worker role, and the entry closes carried on,
// rather than intake refusing the role and leaving the entry open, holding
// every new task in the project (P15).
func TestCarryingOnAHousekeepingPassesKeptWorkCreatesAnOrdinaryTask(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	service.RequireSweepClearsHere(t, "a carried-on pass's successor starting in the kept copy")
	principles.Cite(t, principles.P15)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, head := plantInvestigationCopyOf(t, h, subject, func(id, copyPath string) (store.Task, store.Disposition) {
		return store.Task{
			ID: id, Shape: store.TaskInvestigation, Project: "the parser", Mode: "headless", WorktreePath: copyPath,
			RepositoryID: store.Known(repository.ID), Role: config.RoleHousekeeping, Housekeeping: true,
		}, store.Disposition{Delivered: taskstate.Delivered}
	})
	entry := keepTaskCopy(t, h, old)
	if !entry.Established() || len(entry.Lacks) != 1 || entry.Lacks[0] != head {
		t.Fatalf("the real Keep recorded %+v; want an entry lacking the worker's commit %s", entry, head)
	}
	o, _ := workerOptions(t, h)
	withServiceOptions(t, o, func(under serviceUnderTest) {
		ctx := t.Context()
		var out machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, carryOnRequest(old, entry), &out); err != nil {
			t.Fatalf("carrying the pass's kept work on: %v", err)
		}
		if out.Effect != machine.KeptWorkCarriedOn || out.Task == "" || out.Task == old {
			t.Fatalf("the carry on answered %+v; want a new task, carried on", out)
		}
		created, err := recordsOf(t, h).Task(ctx, out.Task)
		if err != nil || created.Housekeeping || created.Role != "" || created.Project != "the parser" {
			t.Fatalf("the new task reads %+v, %v; want an ordinary task of the parser under the worker role", created, err)
		}
		if closed := waitCarriedOn(t, h, entry.ID); closed.CarriedOnTask.Or("") != out.Task {
			t.Fatalf("the entry reads %+v, want it carried on by %s", closed, out.Task)
		}
	})
}
