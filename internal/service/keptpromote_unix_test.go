//go:build unix

package service_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// A promotion answers its copy's kept work only for a copy the kept-copy
// verification takes, the final full review of 3124096: as above, a
// running investigation's copy has an open entry, and the copy's .git is
// then made a link to another clone's .git. The promotion is refused before
// anything is written, the same refusal every other answer to that copy
// meets (tasks.ErrLayoutUnsupported): the task is still the investigation
// it was, the entry is still open, and the other clone's remotes and HEAD
// are as they were. Without the verification the flip answers the entry
// carry on and the promotion writes the gate's remote into the other clone
// before its isolation assertion refuses the shaped copy.
func TestAPromotionRefusesACopyThatIsNotTheOneItsRegistryRecorded(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P1)
	streamWorkerOf(t, store.TaskInvestigation, "", nil, func(w launched, _ *standin.Agent) {
		ctx := t.Context()
		entry := keptOnCopyOf(t, w)
		copyPath, err := w.home.TaskCopy(w.task)
		if err != nil {
			t.Fatal(err)
		}
		other := filepath.Join(t.TempDir(), "other")
		git(t, w.repository.WorkingPath, "clone", "--quiet", w.repository.WorkingPath, other)
		remotes, head := git(t, other, "remote", "-v"), git(t, other, "symbolic-ref", "HEAD")
		own := filepath.Join(copyPath, ".git")
		if err := os.Rename(own, own+".own"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(other, ".git"), own); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.Remove(own); err != nil {
				t.Error(err)
			}
			if err := os.Rename(own+".own", own); err != nil {
				t.Error(err)
			}
		}()

		_, err = promoteRunningInvestigation(t, w, "task/promoted", "")
		if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), "the kept copy at "+copyPath) {
			t.Fatalf("promoting a copy whose .git links to another clone = %v; want it refused by the kept-copy verification", err)
		}
		stillAnInvestigation(t, w, "the refused promotion")
		if still, err := recordsOf(t, w.home).KeptWorkEntry(ctx, entry.ID); err != nil || !still.Open() {
			t.Fatalf("after the refused promotion the entry reads %+v, %v; want it open", still, err)
		}
		if got := git(t, other, "remote", "-v"); got != remotes {
			t.Errorf("the other clone's remotes became %q, were %q", got, remotes)
		}
		if got := git(t, other, "symbolic-ref", "HEAD"); got != head {
			t.Errorf("the other clone's HEAD became %s, was %s", got, head)
		}
	})
}
