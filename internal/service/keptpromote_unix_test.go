//go:build unix

package service_test

import (
	"errors"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
)

// keptOnCopyOf records an open kept work entry on the launched task's own
// copy, holding and lacking the commit HEAD stands on, while the service
// runs.
func keptOnCopyOf(t *testing.T, w launched) store.KeptWork {
	t.Helper()
	copyPath, err := w.home.TaskCopy(w.task)
	if err != nil {
		t.Fatal(err)
	}
	head := git(t, copyPath, "rev-parse", "HEAD")
	records, err := store.Open(t.Context(), w.home.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = records.Close() }()
	entry, _, err := records.RecordKeptWork(t.Context(), store.KeptCopy{TaskID: w.task, CopyPath: copyPath, Holds: []string{head},
		Lacks: []string{head}, Against: git(t, w.repository.WorkingPath, "rev-parse", "HEAD"), Reason: string(tasks.ReasonUnproven)})
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

// PRD section 6: a promotion of a task whose copy has an open kept work
// entry records the entry's answer as carry on and closes it, since the
// worker continues in that same copy. Through the real socket: a running
// investigation's copy has an open entry, which holds a delivery task
// created in the project; the promotion closes the entry as carry on
// naming the promoted task itself, recorded as the machine interface's
// answer, in the flip, and the held task then dispatches. Without the write
// in the flip the entry stays open after the promotion and the task never
// dispatches.
func TestAPromotionAnswersItsCopysKeptWorkAsCarryOn(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P8)
	streamWorkerOf(t, store.TaskInvestigation, "", nil, func(w launched, _ *standin.Agent) {
		ctx := t.Context()
		entry := keptOnCopyOf(t, w)
		var created machine.TaskCreated
		if err := w.running.client.Call(ctx, ipc.MethodTaskCreate, machine.TaskCreateRequest{
			Shape: "delivery", Project: "one", Instructions: "Do the work.\n", Mode: "interactive",
			Repository: w.repository.ID, Branch: "task/held-by-kept",
		}, &created); err != nil {
			t.Fatal(err)
		}
		waitLog(t, w.home, "task "+created.Record.ID+" waits: ")

		if _, err := promoteRunningInvestigation(t, w, "task/promoted", ""); err != nil {
			t.Fatalf("promoting: %v", err)
		}
		closed, err := recordsOf(t, w.home).KeptWorkEntry(ctx, entry.ID)
		if err != nil || closed.Open() || closed.Answer.Or("") != store.KeptCarryOn || closed.CarriedOnTask.Or("") != w.task ||
			!closed.CarriedOnAt.IsKnown() || closed.AnsweredBy.Or(store.Resolver{}) != store.ResolvedByMachineInterface() {
			t.Fatalf("after the promotion the entry reads %+v, %v; want it answered carry-on by the machine interface, carried on by task %s",
				closed, err, w.task)
		}
		waitAttempts(t, w.home, created.Record.ID)
	})
}

// A promotion touches no entry but its copy's: a refused promotion leaves
// the copy's entry open, a promotion of a task whose copy has no entry
// writes nothing, and an open entry on another task's copy in the project
// stays as it is through a promotion that closes the first. The refusal is
// the posture rule's: local asked for in a gated project is lighter than the
// project's and is refused before the flip.
func TestAPromotionTouchesNoKeptWorkButItsCopys(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	streamWorkerOf(t, store.TaskInvestigation, "", nil, func(w launched, _ *standin.Agent) {
		ctx := t.Context()
		// The other investigation ran, and so was dispatched, before the
		// running one's entry was open, which would have held it.
		_, other := plantKeptInvestigation(t, w.home, w.repository.WorkingPath, w.repository.ID)
		own := keptOnCopyOf(t, w)

		if _, err := promoteRunningInvestigation(t, w, "task/promoted", "local"); err == nil {
			t.Fatal("a promotion asking for a posture lighter than the project's was not refused")
		}
		stillAnInvestigation(t, w, "the refused promotion")
		if still, err := recordsOf(t, w.home).KeptWorkEntry(ctx, own.ID); err != nil || !still.Open() {
			t.Fatalf("after the refused promotion the copy's entry reads %+v, %v; want it open", still, err)
		}

		if _, err := promoteRunningInvestigation(t, w, "task/promoted", ""); err != nil {
			t.Fatalf("promoting: %v", err)
		}
		if closed, err := recordsOf(t, w.home).KeptWorkEntry(ctx, own.ID); err != nil || closed.CarriedOnTask.Or("") != w.task {
			t.Fatalf("after the promotion the copy's entry reads %+v, %v; want it carried on by task %s", closed, err, w.task)
		}
		if untouched, err := recordsOf(t, w.home).KeptWorkEntry(ctx, other.ID); err != nil || !untouched.Open() {
			t.Fatalf("the promotion touched another copy's entry: %+v, %v", untouched, err)
		}
		if _, err := recordsOf(t, w.home).KeptWorkEntry(ctx, max(own.ID, other.ID)+1); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("the promotion wrote an entry of its own: %v", err)
		}
	})
}
