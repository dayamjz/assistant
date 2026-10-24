//go:build unix

package service_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/service"
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

// A promotion that fails after its flip still asks for the dispatch pass
// the flip's answer to kept work calls for, the final full review of
// c5c581c: as above, a running investigation's copy has an open entry that
// holds a delivery task of the project, and the branch the promotion is to
// create already stands in the copy, so the flip answers the entry carry on
// and the promotion then fails creating the branch. The held task
// dispatches. Before the pass was asked whatever the promotion returned, it
// stayed undispatched until something unrelated asked for a pass.
func TestAPromotionThatFailsAfterItsFlipStillDispatchesWhatItsAnswerFreed(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
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
		copyPath, err := w.home.TaskCopy(w.task)
		if err != nil {
			t.Fatal(err)
		}
		git(t, copyPath, "branch", "task/promoted")

		if _, err := promoteRunningInvestigation(t, w, "task/promoted", ""); err == nil {
			t.Fatal("the promotion onto a branch already standing in the copy succeeded; want it to fail after its flip")
		}
		closed, err := recordsOf(t, w.home).KeptWorkEntry(ctx, entry.ID)
		if err != nil || closed.Open() || closed.Answer.Or("") != store.KeptCarryOn {
			t.Fatalf("after the failed promotion the entry reads %+v, %v; want the flip's carry-on answer, or the test reads nothing", closed, err)
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

// A promotion's worker continues in the task's copy only with everything a
// discard that did not finish set aside put back, round 24's second
// finding as it reaches a promotion: a running investigation's copy has an
// open entry and holds a file no commit records, notes.txt, and a discard
// of it is confirmed while a file is written where notes.txt was, so its
// restore leaves notes.txt beside the copy and withdraws the confirmation.
// The promotion is then refused naming notes.txt, with the task still an
// investigation and the entry open; once the file written meanwhile is
// removed, the promotion puts notes.txt back into the copy and closes the
// entry carry on. Before, the promotion closed the entry over a copy
// without it.
func TestAPromotionTakesItsCopyOnlyWithWhatADiscardSetAsidePutBack(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	var notes string
	var once sync.Once
	collide := func() error {
		var err error
		once.Do(func() { err = writeFile(notes, "written meanwhile\n") })
		return err
	}
	with := func(o service.Options) service.Options { return service.WithDiscardAsideSeamForTest(t, o, collide) }
	streamWorkerOfWith(t, store.TaskInvestigation, "", nil, with, func(w launched, _ *standin.Agent) {
		ctx := t.Context()
		entry := keptOnCopyOf(t, w)
		notes = filepath.Join(entry.CopyPath, "notes.txt")
		if err := writeFile(notes, "not committed\n"); err != nil {
			t.Fatal(err)
		}
		discard := machine.KeptWorkAnswerRequest{Task: w.task, Hold: entry.Key(), Answer: string(store.KeptDiscard)}
		var shown machine.KeptWorkAnswered
		if err := w.running.client.Call(ctx, ipc.MethodKeptWorkAnswer, discard, &shown); err != nil || shown.Loss == nil {
			t.Fatalf("the discard shown = %+v, %v", shown, err)
		}
		discard.Confirm = shown.Loss.Digest
		var refused machine.KeptWorkAnswered
		if err := w.running.client.Call(ctx, ipc.MethodKeptWorkAnswer, discard, &refused); !errors.Is(err, ipc.ErrRefused) {
			t.Fatalf("the discard over a file written where one was set aside = %+v, %v; want it refused", refused, err)
		}
		aside := filepath.Join(filepath.Dir(entry.CopyPath), "."+filepath.Base(entry.CopyPath)+".discarding", "tree", "notes.txt")
		if raw, err := os.ReadFile(aside); err != nil || string(raw) != "not committed\n" {
			t.Fatalf("notes.txt beside the copy holds %q, %v; want it set aside as shown, the case under test", raw, err)
		}

		if _, err := promoteRunningInvestigation(t, w, "task/promoted", ""); !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), "notes.txt") {
			t.Fatalf("the promotion with notes.txt beside its copy = %v; want it refused naming notes.txt", err)
		}
		stillAnInvestigation(t, w, "the refused promotion")
		if got, err := recordsOf(t, w.home).KeptWorkEntry(ctx, entry.ID); err != nil || !got.Open() {
			t.Fatalf("the entry reads %+v, %v after the refused promotion; want it open", got, err)
		}

		if err := os.Remove(notes); err != nil {
			t.Fatal(err)
		}
		if _, err := promoteRunningInvestigation(t, w, "task/promoted", ""); err != nil {
			t.Fatalf("the promotion once the file written meanwhile is removed: %v", err)
		}
		if raw, err := os.ReadFile(notes); err != nil || string(raw) != "not committed\n" {
			t.Fatalf("the promoted task's copy holds notes.txt as %q, %v; want it as shown", raw, err)
		}
		if got, err := recordsOf(t, w.home).KeptWorkEntry(ctx, entry.ID); err != nil || got.Open() || got.Answer.Or("") != store.KeptCarryOn {
			t.Fatalf("the entry reads %+v, %v after the promotion; want it answered carry on", got, err)
		}
	})
}
