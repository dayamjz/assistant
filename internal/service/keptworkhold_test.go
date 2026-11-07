package service_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// PRD section 6's "kept work is answered before new task work starts where
// that work is", through the real socket and the real cleanup decision. A
// run's copy is kept on an unpushed commit of the branch the run is of, so
// the subject's project holds one open entry on that branch. A delivery
// task created in that project on another branch is dispatched, not held:
// the hold compares recorded branch names only, which is the narrowing the
// owner decided on 2026-11-06 (PRD decision 21), and before it this same
// task waited for an entry it could not touch. A task created in another
// project at the same time is dispatched too, so kept work in one project
// holds no other. A run a person starts in the held project goes ahead and
// its answer names the kept work. Once the kept commit is pushed upstream,
// the next service's recovery removes the copy on the remote-branch proof,
// which closes the entry, with nothing but the kept work record deciding
// either way (P8). What the entry does hold, and what releases it, is
// TestAnEntryNamingNoBranchHoldsTheInvestigationsOfItsProject; the branch
// the entry records is compared with the task's in
// store.TestOpenKeptWorkHoldsANewTasksDispatchOnItsBranchOnly.
//
// No held task waits here, and none can: the entry is a run's copy on the
// run's branch, and intake refuses a new task on a branch a run already
// has as a person's. That is why nothing asserts cleanup's redispatch
// after a removal (cleanup.go's redispatchAfterRemoval) end to end. The
// only kept copy a service removes without a person's answer is a run's,
// reclaimed at recovery, and a run's entry always names the branch no new
// task may be created on; a task's kept copy standing is re-examined only
// when something asks CleanUp for it. The store test above holds the half
// this reaches, that a closed entry stops holding.
func TestKeptWorkOnOneBranchHoldsNoNewTaskOnAnother(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P8)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	other := newSubject(t)
	otherRepository := recordRepositoryAs(t, h, "other", other)

	var entry store.KeptWork
	withService(t, h, func(under serviceUnderTest) {
		ctx := t.Context()
		started := startRunSkipping(t, under.client, subject, afterIntent()...)
		runByID(t, under.client, started.Record.ID)
		status := statusOf(t, under.client, subject)
		if len(status.KeptWork) != 1 {
			t.Fatalf("status lists kept work %+v, want the run's one entry", status.KeptWork)
		}
		entry = status.KeptWork[0]

		create := func(repositoryID string) string {
			t.Helper()
			var created machine.TaskCreated
			if err := under.client.Call(ctx, ipc.MethodTaskCreate, machine.TaskCreateRequest{
				Shape: "delivery", Project: "one", Instructions: "Do the work.\n", Mode: "interactive",
				Repository: repositoryID, Branch: "task/new",
			}, &created); err != nil {
				t.Fatalf("creating a task in %s: %v", repositoryID, err)
			}
			return created.Record.ID
		}
		onAnotherBranch := create(repository.ID)
		elsewhere := create(otherRepository.ID)

		waitAttempts(t, h, onAnotherBranch)
		waitAttempts(t, h, elsewhere)
		var task machine.Task
		if err := under.client.Call(ctx, ipc.MethodTaskGet, machine.TaskRequest{Task: onAnotherBranch}, &task); err != nil {
			t.Fatal(err)
		}
		if len(task.KeptWork) != 0 {
			t.Fatalf("a task of the kept copy's project on another branch names kept work %+v, want none holding it", task.KeptWork)
		}

		again := startRunSkipping(t, under.client, subject, afterIntent()...)
		run := runByID(t, under.client, again.Record.ID)
		if run.Record.Status == store.RunPending || len(run.KeptWork) == 0 || run.KeptWork[0].ID != entry.ID {
			t.Fatalf("a run started in the held project reads %s naming kept work %+v, want it to go ahead naming entry %d",
				run.Record.Status, run.KeptWork, entry.ID)
		}
		if _, ok := machine.RunKeptWorkPhrase(run); !ok {
			t.Fatal("the run's answer has no line about its project's kept work")
		}
	})

	git(t, subject, "push", "--quiet", "origin", "work")
	withService(t, h, func(under serviceUnderTest) {
		waitClosed(t, h, entry.ID)
	})
}

// waitClosed waits until the kept work entry is closed and returns it.
func waitClosed(t *testing.T, h *home.Home, id int64) store.KeptWork {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		got, err := recordsOf(t, h).KeptWorkEntry(t.Context(), id)
		if err == nil && got.Closure.Or("") == store.KeptRemovedOnProof {
			return got
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("entry %d was never closed on a proof (%+v, %v); the log reads:\n%s", id, got, err, readServiceLog(t, h))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitLog waits until the service log carries text.
func waitLog(t *testing.T, h *home.Home, text string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !strings.Contains(readServiceLog(t, h), text) {
		if !time.Now().Before(deadline) {
			t.Fatalf("the service log never said %q; it reads:\n%s", text, readServiceLog(t, h))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitAttempts waits until the task has an attempt, which is a dispatch's
// claim.
func waitAttempts(t *testing.T, h *home.Home, task string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for len(attemptsOfTask(t, h, task)) == 0 {
		if !time.Now().Before(deadline) {
			log, _ := os.ReadFile(h.ServiceLog())
			t.Fatalf("task %s was never dispatched; the log reads:\n%s", task, log)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// attemptsOfTask reads the task's attempts off the store.
func attemptsOfTask(t *testing.T, h *home.Home, task string) []store.TaskAttempt {
	t.Helper()
	attempts, err := recordsOf(t, h).TaskAttempts(t.Context(), task)
	if err != nil {
		t.Fatal(err)
	}
	return attempts
}

// The other half of PRD section 6's hold, through the real socket: what an
// entry does hold, and that answering it releases exactly that. A reported
// investigation's copy is kept, and an investigation's copy names no
// branch, so the entry stands at its project's base. A new investigation
// task of that project, which names no branch either, is not dispatched:
// the service log says it waits, it has no attempt, and its answer names
// the entry and reads as waiting on that decision, naming the three
// answers. A delivery task created in the same project on a branch is
// dispatched meanwhile, since the entry's base is not its branch. The entry
// is then answered carry on, and the held task dispatches from that answer.
// Without the base reading the delivery task waits too; without the hold
// the investigation task is dispatched at once.
func TestAnEntryNamingNoBranchHoldsTheInvestigationsOfItsProject(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	outlived, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	if entry.Branch.IsKnown() {
		t.Fatalf("the investigation's entry records branch %v, want none: the hold's base case", entry.Branch)
	}

	withService(t, h, func(under serviceUnderTest) {
		ctx := t.Context()
		create := func(request machine.TaskCreateRequest) string {
			t.Helper()
			var created machine.TaskCreated
			if err := under.client.Call(ctx, ipc.MethodTaskCreate, request, &created); err != nil {
				t.Fatalf("creating a task: %v", err)
			}
			return created.Record.ID
		}
		held := create(machine.TaskCreateRequest{
			Shape: "investigation", Project: "the parser", Instructions: "Look again.\n", Mode: "interactive",
			Repository: repository.ID,
		})
		onABranch := create(machine.TaskCreateRequest{
			Shape: "delivery", Project: "the parser", Instructions: "Do the work.\n", Mode: "interactive",
			Repository: repository.ID, Branch: "task/new",
		})

		waitLog(t, h, "task "+held+" waits: ")
		waitAttempts(t, h, onABranch)
		if attempts := attemptsOfTask(t, h, held); len(attempts) != 0 {
			t.Fatalf("the held task has attempts %+v, want none while an entry of its project stands at its base", attempts)
		}
		var task machine.Task
		if err := under.client.Call(ctx, ipc.MethodTaskGet, machine.TaskRequest{Task: held}, &task); err != nil {
			t.Fatal(err)
		}
		if task.State.State != taskstate.Dispatched || len(task.KeptWork) != 1 || task.KeptWork[0].ID != entry.ID {
			t.Fatalf("the held task reads %s naming kept work %+v, want Dispatched naming entry %d", task.State.State, task.KeptWork, entry.ID)
		}
		if phrase := machine.TaskPhrase(task); phrase == machine.TaskPhrase(machine.Task{State: task.State}) ||
			!strings.Contains(phrase.String(), "land, carry on or discard") {
			t.Fatalf("the held task is phrased %q, want the row naming the kept work's three answers", phrase)
		}

		var answered machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, carryOnRequest(outlived, entry), &answered); err != nil {
			t.Fatalf("carrying the entry on: %v", err)
		}
		waitCarriedOn(t, h, entry.ID)
		waitAttempts(t, h, held)
	})
}
