package service_test

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
	"github.com/dayamjz/assistant/internal/vcs"
)

// plantKeptInvestigation records a reported investigation task of the
// repository whose worker is gone and whose copy, a repository of its own
// detached at a commit of the worker's that no remote has, is kept as an
// open entry. It returns the task and the entry.
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
	git(t, copyPath, "init", "--quiet", "-b", "main", ".")
	git(t, copyPath, "config", "gc.auto", "0")
	git(t, copyPath, "config", "maintenance.auto", "false")
	if err := writeFile(copyPath+"/finding.txt", "what the worker found\n"); err != nil {
		t.Fatal(err)
	}
	git(t, copyPath, "add", "-A")
	git(t, copyPath, "commit", "--quiet", "-m", "the worker's own commit")
	head := git(t, copyPath, "rev-parse", "HEAD")
	git(t, copyPath, "checkout", "--quiet", "--detach")
	if _, _, err := records.StoreTaskReport(ctx, store.TaskReport{TaskID: id, Body: `{"summary":"looked"}`, Report: []byte(`{"summary":"looked"}`)}, nil); err != nil {
		t.Fatal(err)
	}
	entry, _, err := records.RecordKeptWork(ctx, store.KeptCopy{TaskID: id, CopyPath: copyPath, Holds: []string{head}, Lacks: []string{head},
		Against: git(t, subject, "rev-parse", "HEAD"), Reason: string(tasks.ReasonUnproven)})
	if err != nil {
		t.Fatal(err)
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
	if _, err := records.AnswerKeptWorkCarryOn(t.Context(), entry.ID, reserved, store.ResolvedByMachineInterface()); err != nil {
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
