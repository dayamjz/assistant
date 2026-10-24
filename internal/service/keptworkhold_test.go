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

// PRD section 6's "kept work is answered before new task work starts",
// through the real socket and the real cleanup decision. A run's copy is
// kept on an unpushed commit, so the subject's project holds one open
// entry. A delivery task created in that project is not dispatched: the
// service log says it waits, it has no attempt, and its answer names the
// entry and reads as waiting on that decision. A task created in another
// project at the same time is dispatched, so kept work in one project
// holds no other. A run a person starts in the held project goes ahead and
// its answer names the kept work. Once the kept commit is pushed upstream,
// the next service's recovery removes the copy on the remote-branch proof,
// which closes the entry, and the held task is dispatched from that
// removal, with nothing but the kept work record deciding either way (P8).
// Without the hold the first task is dispatched at once; without the
// dispatch that follows a removal it stays waiting after the entry closed.
func TestKeptWorkHoldsANewTaskInItsProjectUntilTheEntryCloses(t *testing.T) {
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
	var held, elsewhere string
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
		held = create(repository.ID)
		elsewhere = create(otherRepository.ID)

		waitLog(t, h, "task "+held+" waits: ")
		waitAttempts(t, h, elsewhere)
		if attempts := attemptsOfTask(t, h, held); len(attempts) != 0 {
			t.Fatalf("the held task has attempts %+v, want none while its project holds kept work", attempts)
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
		waitAttempts(t, h, held)
		closed, err := recordsOf(t, h).KeptWorkEntry(t.Context(), entry.ID)
		if err != nil || closed.Closure.Or("") != store.KeptRemovedOnProof {
			t.Fatalf("entry %d reads %+v (%v), want it closed on the proof before the held task dispatched", entry.ID, closed, err)
		}
	})
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
