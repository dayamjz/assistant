package service_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
	"github.com/dayamjz/assistant/internal/vcs"
)

// plantKeptInvestigation records a reported investigation task of the
// repository whose worker is gone and whose copy, a clone of the upstream
// with no remote detached at a commit of the worker's that no remote has,
// is kept as an open entry by the real cleanup and Keep. It returns the
// task and the entry.
func plantKeptInvestigation(t *testing.T, h *home.Home, subject, repositoryID string) (string, store.KeptWork) {
	t.Helper()
	ctx := t.Context()
	records, err := store.Open(ctx, h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = records.Close() }()
	id, err := home.NewTaskID()
	if err != nil {
		t.Fatal(err)
	}
	copyPath, err := h.TaskCopy(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := records.CreateTask(ctx, store.Task{
		ID: id, Shape: store.TaskInvestigation, Project: "the parser", Mode: "headless", WorktreePath: copyPath,
		RepositoryID: store.Known(repositoryID),
	}, store.Disposition{}); err != nil {
		t.Fatal(err)
	}
	if _, err := records.BeginTaskAttempt(ctx, id, 0); err != nil {
		t.Fatal(err)
	}
	if err := records.SetAttemptSession(ctx, id, 1, "pane-gone"); err != nil {
		t.Fatal(err)
	}
	instructions, err := h.TaskInstructions(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(copyPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(instructions, "Investigate the parser.\n"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(copyPath); err != nil {
		t.Fatal(err)
	}
	// Cut as dispatch cuts an investigation's copy, a clone with no remote
	// detached at the default branch, and the worker's commit made on it.
	git(t, filepath.Dir(copyPath), "clone", "--quiet", git(t, subject, "remote", "get-url", "origin"), copyPath)
	git(t, copyPath, "remote", "remove", "origin")
	git(t, copyPath, "checkout", "--quiet", "--detach")
	if err := writeFile(copyPath+"/finding.txt", "what the worker found\n"); err != nil {
		t.Fatal(err)
	}
	git(t, copyPath, "add", "-A")
	git(t, copyPath, "commit", "--quiet", "-m", "the worker's own commit")
	head := git(t, copyPath, "rev-parse", "HEAD")
	if _, _, err := records.StoreTaskReport(ctx, store.TaskReport{TaskID: id, Body: `{"summary":"looked"}`, Report: []byte(`{"summary":"looked"}`)}, nil); err != nil {
		t.Fatal(err)
	}
	_ = records.Close()
	entry := keepTaskCopy(t, h, id)
	if !entry.Established() || !slices.Equal(entry.Lacks, []string{head}) || entry.Reason != string(tasks.ReasonUnproven) {
		t.Fatalf("the real Keep recorded %+v; want an unproven entry lacking the worker's commit %s alone", entry, head)
	}
	return id, entry
}

// carryOnRequest is the answer that carries an entry of a task's copy on.
func carryOnRequest(task string, entry store.KeptWork) machine.KeptWorkAnswerRequest {
	return machine.KeptWorkAnswerRequest{Task: task, Hold: entry.Key(), Answer: string(store.KeptCarryOn)}
}

// waitCarriedOn waits until the entry reads carried on and returns it.
func waitCarriedOn(t *testing.T, h *home.Home, id int64) store.KeptWork {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		got, err := recordsOf(t, h).KeptWorkEntry(t.Context(), id)
		if err == nil && got.CarriedOnAt.IsKnown() {
			return got
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("entry %d was never carried on (%+v, %v); the log reads:\n%s", id, got, err, readServiceLog(t, h))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// PRD section 6's carry on, through the real socket and the real dispatch: a
// reported investigation's kept copy is carried on. The answer creates a new
// task of the same project and shape through intake, with the kept copy
// moved to its own path as its copy and the instructions file carrying the
// intake text and the carry-on note; the entry closes naming the task, and
// the task the copy outlived keeps its record. The new task then dispatches
// over the copy and its worker starts and reads the instructions past the
// note. The same answer again names the same task and creates no second
// one. Without the completion that moves the copy the task never dispatches
// into it.
func TestCarryingKeptWorkOnCreatesATaskWhoseWorkerStartsInTheKeptCopy(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	o, _ := workerOptions(t, h)
	withServiceOptions(t, o, func(under serviceUnderTest) {
		ctx := t.Context()
		var out machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, carryOnRequest(old, entry), &out); err != nil {
			t.Fatalf("carrying the entry on: %v", err)
		}
		if out.Effect != machine.KeptWorkCarriedOn || out.Task == "" || out.Task == old ||
			out.Entry.Answer.Or("") != store.KeptCarryOn || out.Entry.CarriedOnTask.Or("") != out.Task || !out.Entry.CarriedOnAt.IsKnown() {
			t.Fatalf("the carry on answered %+v; want a new task named on the entry, carried on", out)
		}
		created, err := recordsOf(t, h).Task(ctx, out.Task)
		if err != nil || created.Shape != store.TaskInvestigation || created.Project != "the parser" ||
			created.RepositoryID.Or("") != repository.ID || created.Branch.IsKnown() {
			t.Fatalf("the new task reads %+v, %v; want an investigation of the parser in %s", created, err, repository.ID)
		}
		copyPath, err := h.TaskCopy(out.Task)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(copyPath + "/finding.txt"); err != nil {
			t.Fatalf("the kept copy is not the new task's copy: %v", err)
		}
		if _, err := os.Lstat(entry.CopyPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the kept copy still stands at its old path: %v", err)
		}
		instructions, err := h.TaskInstructions(out.Task)
		if err != nil {
			t.Fatal(err)
		}
		text, err := os.ReadFile(instructions)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(text), "Investigate the parser.") || !strings.Contains(string(text), "## Carried on, written by the program") ||
			!strings.Contains(string(text), old) || !strings.Contains(string(text), entry.Holds[0]) {
			t.Fatalf("the new task's instructions do not carry the intake text and the note naming %s and %s:\n%s", old, entry.Holds[0], text)
		}
		if untouched, err := recordsOf(t, h).Task(ctx, old); err != nil || untouched.Shape != store.TaskInvestigation {
			t.Fatalf("the task the copy outlived reads %+v, %v; want it as it was", untouched, err)
		}

		waitLaunched(t, h, out.Task)
		provider, launched := launchedSession(t, under.service, out.Task)
		waitCapture(t, provider, launched, "the second check was read")
		if got := observed(t, listedTask, under.client, out.Task); !readsRunning(got) {
			t.Fatalf("the new task reads %+v, want Running with its worker in the kept copy", got)
		}

		var again machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, carryOnRequest(old, entry), &again); err != nil || again.Task != out.Task {
			t.Fatalf("carrying on again = %+v, %v; want the same task named", again, err)
		}
		if n := taskCount(t, h); n != 2 {
			t.Fatalf("the home records %d tasks, want the outlived one and the one carrying it on", n)
		}
	})
}

// A kept copy that fails the assertion is not carried on: the copy is
// replaced by a linked worktree of the gate, which is not a clone a task's
// worker may be started in, so the answer is refused naming the comparison,
// records nothing, creates no task and moves nothing. Without the
// assertion before the answer a task is created over it.
func TestAKeptCopyThatFailsTheAssertionIsNotCarriedOn(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P11)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	if err := os.RemoveAll(entry.CopyPath); err != nil {
		t.Fatal(err)
	}
	bare, err := vcs.OpenBare(t.Context(), gateRepositoryOf(t, h, subject), vcs.WithRedactor(redact.New()))
	if err != nil {
		t.Fatal(err)
	}
	if err := bare.Fetch(t.Context(), vcs.FetchSpec{Remote: subject, Refspecs: []string{"+refs/heads/main:refs/assistant/kept-test"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := bare.AddWorktree(t.Context(), vcs.WorktreeSpec{Path: entry.CopyPath, Commit: "refs/assistant/kept-test"}); err != nil {
		t.Fatal(err)
	}
	before := taskCount(t, h)
	withService(t, h, func(under serviceUnderTest) {
		var out machine.KeptWorkAnswered
		err := under.client.Call(t.Context(), ipc.MethodKeptWorkAnswer, carryOnRequest(old, entry), &out)
		if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), string(tasks.ComparisonCommonDir)) {
			t.Fatalf("carrying a worktree on = %v; want it refused naming %s", err, tasks.ComparisonCommonDir)
		}
	})
	if still, err := recordsOf(t, h).KeptWorkEntry(t.Context(), entry.ID); err != nil || !still.Open() {
		t.Fatalf("after the refusal the entry reads %+v, %v; want it open", still, err)
	}
	if n := taskCount(t, h); n != before {
		t.Fatalf("the refusal created %d task(s)", n-before)
	}
	if _, err := os.Stat(entry.CopyPath); err != nil {
		t.Fatalf("the refused copy was moved: %v", err)
	}
}

// A carry-on answer recorded by a service that stopped before it created
// the task is completed by the next start: the entry answered carry-on
// naming a task with no row, written as the stopped service left it, is
// found at open, the task created with that identifier through intake, the
// copy moved and the note written, and the entry marked carried on; the
// task then dispatches into the copy. Without the completion at open the
// entry stays owed and no task exists.
func TestACarryOnAStoppedServiceDidNotCarryOutIsCompletedAtTheNextStart(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	_, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	reserved, err := home.NewTaskID()
	if err != nil {
		t.Fatal(err)
	}
	records, err := store.Open(t.Context(), h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatal(err)
	}
	kept, err := tasks.IdentifyCopy(entry.CopyPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := records.AnswerKeptWorkCarryOn(t.Context(), entry.ID, reserved, kept.String(), store.ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	_ = records.Close()

	o, _ := workerOptions(t, h)
	withServiceOptions(t, o, func(under serviceUnderTest) {
		done := waitCarriedOn(t, h, entry.ID)
		if done.CarriedOnTask.Or("") != reserved {
			t.Fatalf("the completed carry on names task %s, want the reserved %s", done.CarriedOnTask.Or(""), reserved)
		}
		if _, err := recordsOf(t, h).Task(t.Context(), reserved); err != nil {
			t.Fatalf("the next start did not create task %s: %v", reserved, err)
		}
		copyPath, err := h.TaskCopy(reserved)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(copyPath + "/finding.txt"); err != nil {
			t.Fatalf("the kept copy is not the task's copy: %v", err)
		}
		instructions, err := h.TaskInstructions(reserved)
		if err != nil {
			t.Fatal(err)
		}
		if text, err := os.ReadFile(instructions); err != nil || !strings.Contains(string(text), "## Carried on, written by the program") {
			t.Fatalf("the task's instructions carry no note (%v):\n%s", err, text)
		}
		waitLaunched(t, h, reserved)
	})
}

// waitLaunched waits until the task's latest attempt names a session, which
// is a worker started.
func waitLaunched(t *testing.T, h *home.Home, task string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		attempts := attemptsOfTask(t, h, task)
		if n := len(attempts); n > 0 && attempts[n-1].SessionRef.IsKnown() {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("task %s's worker never started; the log reads:\n%s", task, readServiceLog(t, h))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A carry on stopped inside the task's intake, after its row and before its
// instructions file, is completed by the next start in intake's own order:
// the first service's intake is stopped at that point through the seam, so
// the answer is recorded, the row stands and no file does; the next start
// finds the row, has intake write the file, moves the copy, writes the
// note, marks the entry, and the task dispatches into the copy. Without
// intake's second step at completion the row is taken as a finished intake,
// the copy is moved, the note cannot be written and the task never
// dispatches.
func TestACarryOnStoppedInsideIntakeIsCompletedAtTheNextStart(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	stopped := errors.New("the service stopped between the task's row and its instructions file")
	o, _ := workerOptions(t, h)
	withServiceOptions(t, service.WithIntakeInterruptForTest(t, o, func() error { return stopped }), func(under serviceUnderTest) {
		var out machine.KeptWorkAnswered
		err := under.client.Call(t.Context(), ipc.MethodKeptWorkAnswer, carryOnRequest(old, entry), &out)
		if err == nil {
			t.Fatalf("the carry on completed past the stopped intake: %+v", out)
		}
	})
	answered, err := recordsOf(t, h).KeptWorkEntry(t.Context(), entry.ID)
	if err != nil || answered.Answer.Or("") != store.KeptCarryOn || answered.CarriedOnAt.IsKnown() {
		t.Fatalf("after the stop the entry reads %+v, %v; want it answered carry-on and not carried on", answered, err)
	}
	reserved := answered.CarriedOnTask.Or("")
	if _, err := recordsOf(t, h).Task(t.Context(), reserved); err != nil {
		t.Fatalf("after the stop task %s has no row: %v", reserved, err)
	}
	instructions, err := h.TaskInstructions(reserved)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(instructions); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("after the stop task %s has an instructions file: %v", reserved, err)
	}
	if _, err := os.Stat(entry.CopyPath); err != nil {
		t.Fatalf("the stopped carry on moved the copy: %v", err)
	}

	o, _ = workerOptions(t, h)
	withServiceOptions(t, o, func(under serviceUnderTest) {
		waitCarriedOn(t, h, entry.ID)
		text, err := os.ReadFile(instructions)
		if err != nil || !strings.HasPrefix(string(text), "Investigate the parser.") || !strings.Contains(string(text), "## Carried on, written by the program") {
			t.Fatalf("the next start left task %s's instructions as %q (%v); want the intake text and the note", reserved, text, err)
		}
		waitLaunched(t, h, reserved)
	})
}

// A carry on whose adoption fails leaves the answer owed and the new task
// undispatched, and a copy standing at the new task's path is not taken as
// the kept one by its path. The old task's directory is made to refuse the
// rename, so the answer is recorded and the task created, the copy stays
// where it was, and a dispatch pass dispatches nothing: the task is held on
// the entry it carries on. A fresh clone of the upstream is then put at the
// new task's path while the kept copy still stands, and the answer again is
// refused and marks nothing, and so it is with the kept copy away from its
// path, since the clone is another directory than the one the answer read. With the
// clone gone and the directory writable, the next start carries the copy on
// and the task's worker starts in it. Without the hold on the entry a dispatch pass cuts the task a
// fresh clone without the kept work; without the identity check the clone
// is taken as the kept copy.
func TestAFailedAdoptionHoldsTheTaskAndAFreshCloneIsNotTakenForTheCopy(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	oldDir := filepath.Dir(entry.CopyPath)
	if err := os.Chmod(oldDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(oldDir, 0o700) })

	var reserved, newPath string
	o, _ := workerOptions(t, h)
	withServiceOptions(t, o, func(under serviceUnderTest) {
		ctx := t.Context()
		var out machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, carryOnRequest(old, entry), &out); err == nil {
			t.Fatalf("the carry on completed past a rename the directory refuses: %+v", out)
		}
		answered, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID)
		if err != nil || answered.Answer.Or("") != store.KeptCarryOn || answered.CarriedOnAt.IsKnown() {
			t.Fatalf("after the failed adoption the entry reads %+v, %v; want it answered carry-on and owed", answered, err)
		}
		reserved = answered.CarriedOnTask.Or("")
		if _, err := recordsOf(t, h).Task(ctx, reserved); err != nil {
			t.Fatalf("after the failed adoption task %s has no row: %v", reserved, err)
		}
		if newPath, err = h.TaskCopy(reserved); err != nil {
			t.Fatal(err)
		}
		under.service.DispatchDueForTest(ctx)
		if attempts := attemptsOfTask(t, h, reserved); len(attempts) != 0 {
			t.Fatalf("task %s was dispatched (%+v) while the copy it carries on had not been moved to it", reserved, attempts)
		}
		if _, err := os.Lstat(newPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("something was cut at task %s's path while it was held: %v", reserved, err)
		}

		git(t, filepath.Dir(newPath), "clone", "--quiet", repository.UpstreamURL, newPath)
		err = under.client.Call(ctx, ipc.MethodKeptWorkAnswer, carryOnRequest(old, entry), &out)
		if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), "is not the kept copy") {
			t.Fatalf("the carry on over a fresh clone at the new path = %+v, %v; want it refused as not the kept copy", out, err)
		}
		if still, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID); err != nil || still.CarriedOnAt.IsKnown() {
			t.Fatalf("after the refusal the entry reads %+v, %v; want it owed", still, err)
		}
		under.service.DispatchDueForTest(ctx)
		if attempts := attemptsOfTask(t, h, reserved); len(attempts) != 0 {
			t.Fatalf("task %s was dispatched (%+v) over a clone that is not the kept copy", reserved, attempts)
		}

		// With the kept copy away from its path, the clone at the new path
		// is still not taken: it is another directory.
		if err := os.Chmod(oldDir, 0o700); err != nil {
			t.Fatal(err)
		}
		away := filepath.Join(t.TempDir(), "kept-copy-away")
		if err := os.Rename(entry.CopyPath, away); err != nil {
			t.Fatal(err)
		}
		err = under.client.Call(ctx, ipc.MethodKeptWorkAnswer, carryOnRequest(old, entry), &out)
		if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), "is not the kept copy") {
			t.Fatalf("the carry on over a fresh clone with the kept copy away = %+v, %v; want it refused as not the kept copy", out, err)
		}
		if err := os.Rename(away, entry.CopyPath); err != nil {
			t.Fatal(err)
		}
	})

	if err := os.RemoveAll(newPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(oldDir, 0o700); err != nil {
		t.Fatal(err)
	}
	o, _ = workerOptions(t, h)
	withServiceOptions(t, o, func(under serviceUnderTest) {
		if done := waitCarriedOn(t, h, entry.ID); done.CarriedOnTask.Or("") != reserved {
			t.Fatalf("the completed carry on names task %s, want %s", done.CarriedOnTask.Or(""), reserved)
		}
		if _, err := os.Stat(newPath + "/finding.txt"); err != nil {
			t.Fatalf("the kept copy is not task %s's copy: %v", reserved, err)
		}
		waitLaunched(t, h, reserved)
	})
}

// Carrying on moves the whole copy, so it answers every open entry of that
// copy: the copy is kept at the worker's commit, then the worker commits
// again and the copy is kept again, both through the real Keep, so two
// entries name the one copy. Carrying the first on answers and marks both
// with the one task, and that task dispatches and its worker starts.
// Without the answer reaching the second entry it stays open over a copy
// that has moved, and holds the project's new task from dispatching for
// as long as it stands.
func TestCarryingOnAnswersEveryEntryOfTheCopy(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, first := plantKeptInvestigation(t, h, subject, repository.ID)
	if err := writeFile(first.CopyPath+"/more.txt", "what the worker found next\n"); err != nil {
		t.Fatal(err)
	}
	git(t, first.CopyPath, "add", "-A")
	git(t, first.CopyPath, "commit", "--quiet", "-m", "the worker's second commit")
	second := keepTaskCopy(t, h, old)
	if second.ID == first.ID || second.CopyPath != first.CopyPath || !second.Open() || !first.Open() {
		t.Fatalf("the copy kept again reads %+v beside %+v; want a second open entry of the same copy", second, first)
	}

	o, _ := workerOptions(t, h)
	withServiceOptions(t, o, func(under serviceUnderTest) {
		ctx := t.Context()
		var out machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, carryOnRequest(old, first), &out); err != nil {
			t.Fatalf("carrying the first entry on: %v", err)
		}
		for _, id := range []int64{first.ID, second.ID} {
			got, err := recordsOf(t, h).KeptWorkEntry(ctx, id)
			if err != nil || got.Answer.Or("") != store.KeptCarryOn || got.CarriedOnTask.Or("") != out.Task || !got.CarriedOnAt.IsKnown() {
				t.Fatalf("entry %d reads %+v, %v; want it carried on by task %s", id, got, err, out.Task)
			}
		}
		waitLaunched(t, h, out.Task)
	})
}

// A carry on stopped after the copy was moved is completed by the next
// start though the copy went on past what it was kept at, the reviewer's
// sequence: a detached investigation's copy is kept at the worker's commit
// A, the worker then commits B on it with no second Keep, so A is B's
// ancestor and no longer a tip, and the carry on is answered and stopped
// through the seam right after the move. The next start takes the copy at
// the new task's path as the kept one, since it still contains A, marks
// the entry, and the task's worker starts. Without reading containment
// rather than tips the moved copy is refused and the task is held forever.
func TestACarryOnStoppedAfterTheMoveAdoptsACopyThatWentOn(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	kept := git(t, entry.CopyPath, "rev-parse", "HEAD")
	if !slices.Contains(entry.Holds, kept) {
		t.Fatalf("the entry holds %v; want the worker's commit %s among them", entry.Holds, kept)
	}
	if err := writeFile(entry.CopyPath+"/more.txt", "what the worker found next\n"); err != nil {
		t.Fatal(err)
	}
	git(t, entry.CopyPath, "add", "-A")
	git(t, entry.CopyPath, "commit", "--quiet", "-m", "the worker's second commit")

	stopped := errors.New("the service stopped between the copy's move and the entry's mark")
	o, _ := workerOptions(t, h)
	withServiceOptions(t, service.WithCarryOnInterruptForTest(t, o, func() error { return stopped }), func(under serviceUnderTest) {
		var out machine.KeptWorkAnswered
		if err := under.client.Call(t.Context(), ipc.MethodKeptWorkAnswer, carryOnRequest(old, entry), &out); err == nil || !strings.Contains(err.Error(), "stopped between") {
			t.Fatalf("the carry on past the stopped seam = %+v, %v; want the stop", out, err)
		}
	})
	answered, err := recordsOf(t, h).KeptWorkEntry(t.Context(), entry.ID)
	if err != nil || answered.CarriedOnAt.IsKnown() {
		t.Fatalf("after the stop the entry reads %+v, %v; want it owed", answered, err)
	}
	reserved := answered.CarriedOnTask.Or("")
	newPath, err := h.TaskCopy(reserved)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(newPath + "/more.txt"); err != nil {
		t.Fatalf("after the stop the copy is not at task %s's path: %v", reserved, err)
	}

	o, _ = workerOptions(t, h)
	withServiceOptions(t, o, func(under serviceUnderTest) {
		if done := waitCarriedOn(t, h, entry.ID); done.CarriedOnTask.Or("") != reserved {
			t.Fatalf("the completed carry on names task %s, want %s", done.CarriedOnTask.Or(""), reserved)
		}
		waitLaunched(t, h, reserved)
	})
}

// Containment is not identity: a fresh clone of an upstream that took the
// recorded commits up is not taken for the kept copy, the reviewer's
// sequence. A detached investigation's copy is kept at the worker's commit
// A, the worker commits B with no second Keep, and A lands upstream with an
// unrelated C after it, so a clone of the upstream contains every commit
// the entry records. The carry on is stopped through the seam right after
// the move; the moved copy is then put aside and a fresh clone of the
// upstream put at the new task's path. The next start refuses the clone,
// leaves the entry owed and the task undispatched. With the kept copy back
// at the path, which went on to B and is still the same directory, the
// start after adopts it and the task's worker starts. Without the identity
// the clone is taken for the kept copy and the task starts without B.
func TestACarryOnTakesTheKeptCopyByIdentityAndNotAnUpstreamClone(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	a := git(t, entry.CopyPath, "rev-parse", "HEAD")
	if err := writeFile(entry.CopyPath+"/more.txt", "what the worker found next\n"); err != nil {
		t.Fatal(err)
	}
	git(t, entry.CopyPath, "add", "-A")
	git(t, entry.CopyPath, "commit", "--quiet", "-m", "the worker's second commit")
	b := git(t, entry.CopyPath, "rev-parse", "HEAD")
	git(t, entry.CopyPath, "push", "--quiet", repository.UpstreamURL, a+":refs/heads/main")
	advanceUpstream(t, repository.UpstreamURL, "c.txt")

	stopped := errors.New("the service stopped between the copy's move and the entry's mark")
	o, _ := workerOptions(t, h)
	withServiceOptions(t, service.WithCarryOnInterruptForTest(t, o, func() error { return stopped }), func(under serviceUnderTest) {
		var out machine.KeptWorkAnswered
		if err := under.client.Call(t.Context(), ipc.MethodKeptWorkAnswer, carryOnRequest(old, entry), &out); err == nil || !strings.Contains(err.Error(), "stopped between") {
			t.Fatalf("the carry on past the stopped seam = %+v, %v; want the stop", out, err)
		}
	})
	answered, err := recordsOf(t, h).KeptWorkEntry(t.Context(), entry.ID)
	if err != nil || answered.CarriedOnAt.IsKnown() {
		t.Fatalf("after the stop the entry reads %+v, %v; want it owed", answered, err)
	}
	reserved := answered.CarriedOnTask.Or("")
	newPath, err := h.TaskCopy(reserved)
	if err != nil {
		t.Fatal(err)
	}
	away := filepath.Join(t.TempDir(), "kept-copy-away")
	if err := os.Rename(newPath, away); err != nil {
		t.Fatal(err)
	}
	git(t, filepath.Dir(newPath), "clone", "--quiet", repository.UpstreamURL, newPath)
	// The clone contains every commit the entry records, so containment
	// alone would take it; git fails the test here if it does not.
	for _, held := range entry.Holds {
		git(t, newPath, "merge-base", "--is-ancestor", held, "HEAD")
	}

	o, _ = workerOptions(t, h)
	withServiceOptions(t, o, func(under serviceUnderTest) {
		deadline := time.Now().Add(5 * time.Second)
		for !strings.Contains(readServiceLog(t, h), "is not the kept copy") {
			if !time.Now().Before(deadline) {
				t.Fatalf("the next start did not refuse the clone; the log reads:\n%s", readServiceLog(t, h))
			}
			time.Sleep(20 * time.Millisecond)
		}
		if still, err := recordsOf(t, h).KeptWorkEntry(t.Context(), entry.ID); err != nil || still.CarriedOnAt.IsKnown() {
			t.Fatalf("after the refusal the entry reads %+v, %v; want it owed", still, err)
		}
		under.service.DispatchDueForTest(t.Context())
		if attempts := attemptsOfTask(t, h, reserved); len(attempts) != 0 {
			t.Fatalf("task %s was dispatched (%+v) over a clone that is not the kept copy", reserved, attempts)
		}
	})

	if err := os.RemoveAll(newPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(away, newPath); err != nil {
		t.Fatal(err)
	}
	o, _ = workerOptions(t, h)
	withServiceOptions(t, o, func(under serviceUnderTest) {
		if done := waitCarriedOn(t, h, entry.ID); done.CarriedOnTask.Or("") != reserved {
			t.Fatalf("the completed carry on names task %s, want %s", done.CarriedOnTask.Or(""), reserved)
		}
		if head := git(t, newPath, "rev-parse", "HEAD"); head != b {
			t.Fatalf("the adopted copy stands at %s, want the worker's second commit %s", head, b)
		}
		waitLaunched(t, h, reserved)
	})
}

// A kept copy whose remote set the reshape would not repair is not carried
// on, the reviewer's sequence: an investigation's kept copy is given a
// remote named upstream, which an investigation's copy never has and which
// carrying on does not remove, since removing a remote deletes its
// remote-tracking references and the program discards nothing of a kept
// copy on its own. The answer is refused naming the remote, records
// nothing, creates no task and moves nothing, and the remote is still
// there. Once the person removes it, the same answer carries the copy on
// and the new task's worker starts. Without the refusal the entry is
// carried on and dispatch's own assertion then refuses the copy, so no
// worker ever starts.
func TestAKeptCopyWithARemoteTheReshapeKeepsIsNotCarriedOn(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P11)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	git(t, entry.CopyPath, "remote", "add", "upstream", repository.UpstreamURL)
	before := taskCount(t, h)

	o, _ := workerOptions(t, h)
	withServiceOptions(t, o, func(under serviceUnderTest) {
		ctx := t.Context()
		var out machine.KeptWorkAnswered
		err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, carryOnRequest(old, entry), &out)
		if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), string(tasks.ComparisonRemotes)) ||
			!strings.Contains(err.Error(), "upstream") {
			t.Fatalf("carrying on a copy with a remote named upstream = %+v, %v; want it refused naming %s and the remote",
				out, err, tasks.ComparisonRemotes)
		}
		if still, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID); err != nil || !still.Open() {
			t.Fatalf("after the refusal the entry reads %+v, %v; want it open", still, err)
		}
		if n := taskCount(t, h); n != before {
			t.Fatalf("the refusal created %d task(s)", n-before)
		}
		if remotes := git(t, entry.CopyPath, "remote"); remotes != "upstream" {
			t.Fatalf("after the refusal the copy's remotes read %q; want upstream still there", remotes)
		}

		git(t, entry.CopyPath, "remote", "remove", "upstream")
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, carryOnRequest(old, entry), &out); err != nil {
			t.Fatalf("carrying the copy on once the remote is removed: %v", err)
		}
		waitLaunched(t, h, out.Task)
	})
}

// A delivery copy whose HEAD the reshape cannot put on the task's branch is
// not carried on: a failed delivery task's clone, shaped as dispatch shapes
// one (the gate its one remote, the task's branch checked out), holds the
// task's commit and is kept through the real cleanup and Keep; then HEAD
// moves to another branch with a commit of its own, while the task's
// branch stands where it was. The reshape creates the task's branch at
// HEAD and moves no branch, so the answer is refused naming HEAD and the
// branch, records nothing and creates no task. With the task's branch
// checked out again, the same answer carries the copy on and the new
// task's worker starts. Without the refusal the answer is recorded, the
// branch's creation then fails, and the task is held for good on an entry
// whose carry on cannot complete.
func TestAKeptCopyWhoseBranchStandsAwayFromHEADIsNotCarriedOn(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	const branch = "task/kept"
	id, err := home.NewTaskID()
	if err != nil {
		t.Fatal(err)
	}
	copyPath, err := h.TaskCopy(id)
	if err != nil {
		t.Fatal(err)
	}
	records, err := store.Open(t.Context(), h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := records.CreateTask(t.Context(), store.Task{
		ID: id, Shape: store.TaskDelivery, Project: "the parser", Mode: "headless", WorktreePath: copyPath,
		RepositoryID: store.Known(repository.ID), Branch: store.Known(branch),
	}, store.Disposition{}); err != nil {
		t.Fatal(err)
	}
	if _, err := records.BeginTaskAttempt(t.Context(), id, 0); err != nil {
		t.Fatal(err)
	}
	if err := records.SetAttemptSession(t.Context(), id, 1, "pane-gone"); err != nil {
		t.Fatal(err)
	}
	// The task ended, as a task whose copy is kept has, so it no longer
	// holds its branch against the task carrying it on.
	if _, err := records.AppendTaskEvent(t.Context(), id, "failed", "the worker gave up"); err != nil {
		t.Fatal(err)
	}
	_ = records.Close()
	instructions, err := h.TaskInstructions(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(copyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(instructions, "Fix the parser.\n"); err != nil {
		t.Fatal(err)
	}
	git(t, filepath.Dir(copyPath), "clone", "--quiet", repository.UpstreamURL, copyPath)
	git(t, copyPath, "remote", "remove", "origin")
	git(t, copyPath, "remote", "add", "assistant", gateRepositoryOf(t, h, subject))
	git(t, copyPath, "checkout", "--quiet", "-b", branch)
	if err := writeFile(filepath.Join(copyPath, "own.txt"), "the task's own work\n"); err != nil {
		t.Fatal(err)
	}
	git(t, copyPath, "add", "-A")
	git(t, copyPath, "commit", "--quiet", "-m", "the task's own commit")
	entry := keepTaskCopy(t, h, id)
	git(t, copyPath, "checkout", "--quiet", "-b", "aside")
	if err := writeFile(filepath.Join(copyPath, "aside.txt"), "work on another branch\n"); err != nil {
		t.Fatal(err)
	}
	git(t, copyPath, "add", "-A")
	git(t, copyPath, "commit", "--quiet", "-m", "work on another branch")
	before := taskCount(t, h)

	o, _ := workerOptions(t, h)
	withServiceOptions(t, o, func(under serviceUnderTest) {
		ctx := t.Context()
		var out machine.KeptWorkAnswered
		err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, carryOnRequest(id, entry), &out)
		if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), string(tasks.ComparisonHead)) ||
			!strings.Contains(err.Error(), branch) {
			t.Fatalf("carrying on a copy whose branch stands away from HEAD = %+v, %v; want it refused naming %s and %s",
				out, err, tasks.ComparisonHead, branch)
		}
		if still, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID); err != nil || !still.Open() {
			t.Fatalf("after the refusal the entry reads %+v, %v; want it open", still, err)
		}
		if n := taskCount(t, h); n != before {
			t.Fatalf("the refusal created %d task(s)", n-before)
		}

		git(t, copyPath, "checkout", "--quiet", branch)
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, carryOnRequest(id, entry), &out); err != nil {
			t.Fatalf("carrying the copy on with the task's branch checked out: %v", err)
		}
		waitLaunched(t, h, out.Task)
	})
}

// A carried-on copy is asserted at its new path once shaped, before the
// entry is marked, as dispatch will assert it: the carry on is stopped
// through the seam right after the move, and a remote named upstream is
// then added to the moved copy, where the assertion before the move no
// longer looks. The next start adopts the copy, shapes it, refuses it
// naming the remote set, and leaves the entry owed and the task
// undispatched; with the remote removed, the start after marks the entry
// and the worker starts. Without the assertion after the shaping the entry
// is marked over a copy dispatch refuses, and no worker ever starts.
func TestACarriedOnCopyIsAssertedOnceShapedBeforeItIsMarked(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P11)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)

	stopped := errors.New("the service stopped between the copy's move and the entry's mark")
	o, _ := workerOptions(t, h)
	withServiceOptions(t, service.WithCarryOnInterruptForTest(t, o, func() error { return stopped }), func(under serviceUnderTest) {
		var out machine.KeptWorkAnswered
		if err := under.client.Call(t.Context(), ipc.MethodKeptWorkAnswer, carryOnRequest(old, entry), &out); err == nil || !strings.Contains(err.Error(), "stopped between") {
			t.Fatalf("the carry on past the stopped seam = %+v, %v; want the stop", out, err)
		}
	})
	answered, err := recordsOf(t, h).KeptWorkEntry(t.Context(), entry.ID)
	if err != nil || answered.CarriedOnAt.IsKnown() {
		t.Fatalf("after the stop the entry reads %+v, %v; want it owed", answered, err)
	}
	reserved := answered.CarriedOnTask.Or("")
	newPath, err := h.TaskCopy(reserved)
	if err != nil {
		t.Fatal(err)
	}
	git(t, newPath, "remote", "add", "upstream", repository.UpstreamURL)

	o, _ = workerOptions(t, h)
	withServiceOptions(t, o, func(under serviceUnderTest) {
		deadline := time.Now().Add(5 * time.Second)
		for !strings.Contains(readServiceLog(t, h), "once shaped") {
			if !time.Now().Before(deadline) {
				t.Fatalf("the next start did not refuse the shaped copy; the log reads:\n%s", readServiceLog(t, h))
			}
			time.Sleep(20 * time.Millisecond)
		}
		if log := readServiceLog(t, h); !strings.Contains(log, "("+string(tasks.ComparisonRemotes)+")") {
			t.Fatalf("the refusal does not name the remote set; the log reads:\n%s", log)
		}
		if still, err := recordsOf(t, h).KeptWorkEntry(t.Context(), entry.ID); err != nil || still.CarriedOnAt.IsKnown() {
			t.Fatalf("after the refusal the entry reads %+v, %v; want it owed", still, err)
		}
		under.service.DispatchDueForTest(t.Context())
		if attempts := attemptsOfTask(t, h, reserved); len(attempts) != 0 {
			t.Fatalf("task %s was dispatched (%+v) over a copy that fails the assertion", reserved, attempts)
		}
	})

	git(t, newPath, "remote", "remove", "upstream")
	o, _ = workerOptions(t, h)
	withServiceOptions(t, o, func(under serviceUnderTest) {
		if done := waitCarriedOn(t, h, entry.ID); done.CarriedOnTask.Or("") != reserved {
			t.Fatalf("the completed carry on names task %s, want %s", done.CarriedOnTask.Or(""), reserved)
		}
		waitLaunched(t, h, reserved)
	})
}
