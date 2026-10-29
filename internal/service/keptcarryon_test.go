package service_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/config"
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
	return plantKeptInvestigationAs(t, h, subject, repositoryID, "")
}

// plantKeptInvestigationAs is plantKeptInvestigation for a task that ran
// under the named role, which the record keeps whether or not the role is
// configured now.
func plantKeptInvestigationAs(t *testing.T, h *home.Home, subject, repositoryID, role string) (string, store.KeptWork) {
	t.Helper()
	id, head := plantInvestigationCopy(t, h, subject, repositoryID, role)
	entry := keepTaskCopy(t, h, id)
	if !entry.Established() || !slices.Equal(entry.Lacks, []string{head}) || entry.Reason != string(tasks.ReasonUnproven) {
		t.Fatalf("the real Keep recorded %+v; want an unproven entry lacking the worker's commit %s alone", entry, head)
	}
	return id, entry
}

// plantInvestigationCopy is plantKeptInvestigationAs up to the keep: the
// reported investigation, its instructions and its copy holding the
// worker's commit, which it returns with the task.
func plantInvestigationCopy(t *testing.T, h *home.Home, subject, repositoryID, role string) (string, string) {
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
		RepositoryID: store.Known(repositoryID), Role: role,
	}, store.Disposition{}); err != nil {
		t.Fatal(err)
	}
	if _, err := records.BeginFirstTaskAttempt(ctx, id, store.OnThisMachine()); err != nil {
		t.Fatal(err)
	}
	if err := records.SetAttemptSession(ctx, id, 1, "pane-gone", store.SessionHost{}); err != nil {
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
	return id, head
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
	// The worker starts in the kept copy, which stands, so it is swept first.
	service.RequireSweepClearsHere(t, "a carried-on task's worker starting in the kept copy")
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

// Carrying on a promoted task's copy hands the new task what the delivery
// is to do, the intent the promotion recorded, and not the investigation's
// intake text, the reviewer's sequence: an investigation told not to change
// code is promoted, in the store's own shape transition with the brief
// appended to its instructions as promotion appends it, with the intent to
// implement the fix; its copy is then kept by the real cleanup and Keep and
// carried on through the socket. The new task is a delivery task on the
// branch whose instructions open with the intent and carry nothing of the
// investigation's text. Without the transfer the new task is told only not
// to change code.
func TestCarryingOnAPromotedTaskTransfersItsDeliveryIntent(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, _ := plantInvestigationCopy(t, h, subject, repository.ID, "")
	instructions, err := h.TaskInstructions(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFile(instructions, "Investigate the parser. Do not change code.\n"); err != nil {
		t.Fatal(err)
	}
	const branch, intent = "task/promoted", "Implement the parser fix."
	records, err := store.Open(t.Context(), h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatal(err)
	}
	events, err := records.TaskEvents(t.Context(), old, 0, 0)
	if err != nil || len(events) == 0 {
		t.Fatalf("reading the investigation's history: %v (%d entries)", err, len(events))
	}
	if _, _, err := records.TransitionTaskShape(t.Context(), old, store.TaskInvestigation, store.TaskDelivery, repository.ID, branch,
		config.DeliveryGated, intent, events[len(events)-1].Sequence, "promoted in the test", store.ResolvedByMachineInterface(), nil); err != nil {
		t.Fatalf("promoting the investigation: %v", err)
	}
	_ = records.Close()
	f, err := os.OpenFile(instructions, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\n\n" + tasks.PromotionBrief(branch, intent, "true")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	entry := keepTaskCopy(t, h, old)
	if !entry.Open() {
		t.Fatalf("the promoted task's copy was kept as %+v; want an open entry", entry)
	}

	o, _ := workerOptions(t, h)
	withServiceOptions(t, o, func(under serviceUnderTest) {
		ctx := t.Context()
		var out machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, carryOnRequest(old, entry), &out); err != nil {
			t.Fatalf("carrying the promoted task's copy on: %v", err)
		}
		created, err := recordsOf(t, h).Task(ctx, out.Task)
		if err != nil || created.Shape != store.TaskDelivery || created.Branch.Or("") != branch {
			t.Fatalf("the new task reads %+v, %v; want a delivery task on %s", created, err, branch)
		}
		path, err := h.TaskInstructions(out.Task)
		if err != nil {
			t.Fatal(err)
		}
		text, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(text), intent) || strings.Contains(string(text), "Do not change code") {
			t.Fatalf("the new task's instructions do not open with the promotion's intent alone:\n%s", text)
		}
	})
}

// A kept copy that is not the clone its task recorded is not carried on:
// the copy is replaced by a linked worktree of the gate, which is not a
// clone a task's worker may be started in, and whose repository directory
// is not the clone's own .git, so the answer is refused by the
// verification every path asks before it reads a kept copy (round 28,
// tasks.Cleaner.VerifyKeptCopy), ahead of the isolation assertion that
// refused it before, records nothing, creates no task and moves nothing.
// The assertion's own refusals are TestAKeptCopyWithARemoteTheReshapeKeepsIsNotCarriedOn's
// and the HEAD ones below. Without either before the answer a task is
// created over it.
func TestAKeptCopyThatFailsTheAssertionIsNotCarriedOn(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)

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
		if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), "has no repository directory of its own") {
			t.Fatalf("carrying a worktree on = %v; want it refused as no repository directory of its own", err)
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

// A carry-on claim recorded by a service that stopped before it created
// the task is completed by the next start: the open entry claimed by a
// task with no row, written as the stopped service left it, is found at
// open, the task created with that identifier through intake, the
// copy moved and the note written, and the entry marked carried on; the
// task then dispatches into the copy. Without the completion at open the
// entry stays owed and no task exists.
func TestACarryOnAStoppedServiceDidNotCarryOutIsCompletedAtTheNextStart(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	// The worker starts in the kept copy, which stands, so it is swept first.
	service.RequireSweepClearsHere(t, "a carry on completed at the next start launching its worker")
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
	if _, err := records.ClaimKeptWorkCarryOn(t.Context(), entry.ID, reserved, kept.String(), store.ResolvedByMachineInterface()); err != nil {
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
// the claim is recorded, the row stands and no file does, so the claim is
// not withdrawn and the entry stays open and claimed; the next start
// finds the row, has intake write the file, moves the copy, writes the
// note, marks the entry, and the task dispatches into the copy. Without
// intake's second step at completion the row is taken as a finished intake,
// the copy is moved, the note cannot be written and the task never
// dispatches.
func TestACarryOnStoppedInsideIntakeIsCompletedAtTheNextStart(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	// The worker starts in the kept copy, which stands, so it is swept first.
	service.RequireSweepClearsHere(t, "a carry on stopped inside intake launching its worker once completed")
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
	if err != nil || !answered.CarryOnClaimed() || answered.CarriedOnAt.IsKnown() {
		t.Fatalf("after the stop the entry reads %+v, %v; want it open with its carry-on claim standing, since the task's row stands", answered, err)
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

// A carry on whose adoption fails leaves the claim owed, the entry open,
// and the new task undispatched, and a copy standing at the new task's
// path is not taken as the kept one by its path. The old task's directory
// is made to refuse the rename, so the claim is recorded and the task
// created, the copy stays
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
	// The worker starts in the kept copy, which stands, so it is swept first.
	service.RequireSweepClearsHere(t, "a failed adoption's task launching in the kept copy once adopted")
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
		if err != nil || !answered.CarryOnClaimed() || answered.CarriedOnAt.IsKnown() {
			t.Fatalf("after the failed adoption the entry reads %+v, %v; want it open with its carry-on claim owed", answered, err)
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

// A carry on whose intake is refused leaves the entry open and listed, the
// reviewer's sequence on #475: the kept investigation ran under a role the
// configuration no longer has, so intake refuses the task that would carry
// it on. The answer is refused naming why, no task is created, and the
// entry is still open, still read by the open-entry listing and status,
// and still holds the project's new tasks; the copy stands where it was.
// Without the answer kept apart from its outcome, the entry reads answered
// carry-on naming a task that never exists, is listed nowhere, and the
// project's tasks dispatch past kept work nobody decided.
func TestACarryOnWhoseIntakeIsRefusedLeavesTheEntryOpen(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigationAs(t, h, subject, repository.ID, "removed-since")
	o, _ := workerOptions(t, h)
	withServiceOptions(t, o, func(under serviceUnderTest) {
		ctx := t.Context()
		var out machine.KeptWorkAnswered
		err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, carryOnRequest(old, entry), &out)
		if err == nil || !strings.Contains(err.Error(), "removed-since") {
			t.Fatalf("the carry on under a role no longer configured = %+v, %v; want it refused naming the role", out, err)
		}
		after, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID)
		if err != nil || !after.Open() || after.CarriedOnTask.IsKnown() {
			t.Fatalf("after the refused intake the entry reads %+v, %v; want it open and naming no task", after, err)
		}
		open, err := recordsOf(t, h).OpenKeptWork(ctx)
		if err != nil || !slices.ContainsFunc(open, func(k store.KeptWork) bool { return k.ID == entry.ID }) {
			t.Fatalf("the open kept work reads %+v, %v; want entry %d", open, err, entry.ID)
		}
		if status := statusOf(t, under.client, subject); !slices.ContainsFunc(status.KeptWork, func(k store.KeptWork) bool { return k.ID == entry.ID }) {
			t.Fatalf("status lists kept work %+v; want entry %d", status.KeptWork, entry.ID)
		}
		next := recordDispatchedTaskUnder(t, h, repository, store.TaskInvestigation, "", store.Optional[config.Delivery]{})
		records := openRecords(t, h)
		defer func() { _ = records.Close() }()
		if holding, err := records.KeptWorkHolding(ctx, next); err != nil || len(holding) == 0 {
			t.Fatalf("a new task of the project is held by %+v, %v; want entry %d holding it", holding, err, entry.ID)
		}
		if _, err := os.Stat(filepath.Join(entry.CopyPath, "finding.txt")); err != nil {
			t.Fatalf("the kept copy is not where it was: %v", err)
		}
	})
}

// A carry-on answer completes the claim the entry holds when it completes,
// never one it read before, the reviewer's sequence: the answer claims task A
// and, before its completion reads the entry, another answer's intake was
// refused and withdrew A and a retry claimed task B (the store writes both,
// through the seam, as those answers write them). The answer then carries on
// with B or refuses; it never creates A or moves the copy to A's path. Without
// that, it created A and moved the kept copy into A's directory, the mark
// refused because the entry names B, and B could never be completed because
// the copy was gone from the path the entry records.
func TestACarryOnCompletesTheClaimTheEntryHoldsNotOneItReadBefore(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	var first, second string
	var once sync.Once
	o, _ := workerOptions(t, h)
	o = service.WithCarryOnClaimInterruptForTest(t, o, func() error {
		var err error
		once.Do(func() {
			ctx := context.Background()
			records, openErr := store.Open(ctx, h.Database(), store.WithRedactor(redact.New()))
			if openErr != nil {
				err = openErr
				return
			}
			defer func() { _ = records.Close() }()
			claimed, readErr := records.KeptWorkEntry(ctx, entry.ID)
			if readErr != nil {
				err = readErr
				return
			}
			first = claimed.CarriedOnTask.Or("")
			if _, err = records.WithdrawKeptWorkCarryOn(ctx, entry.ID, first); err != nil {
				return
			}
			if second, err = home.NewTaskID(); err != nil {
				return
			}
			_, err = records.ClaimKeptWorkCarryOn(ctx, entry.ID, second, claimed.CarriedOnCopy.Or(""), store.ResolvedByMachineInterface())
		})
		return err
	})
	withServiceOptions(t, o, func(under serviceUnderTest) {
		ctx := t.Context()
		var out machine.KeptWorkAnswered
		callErr := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, carryOnRequest(old, entry), &out)
		if first == "" || second == "" {
			t.Fatalf("the seam did not run (%q, %q): %+v, %v", first, second, out, callErr)
		}
		if _, err := recordsOf(t, h).Task(ctx, first); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("task %s, of the withdrawn claim, reads %v; want it never created (the answer read %+v, %v)", first, err, out, callErr)
		}
		firstPath, err := h.TaskCopy(first)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(firstPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("something stands at %s, the withdrawn claim's task's path: %v", firstPath, err)
		}
		// The answer again completes the claim standing, whatever the first
		// call did with it.
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, carryOnRequest(old, entry), &out); err != nil {
			t.Fatalf("carrying the entry on again: %v", err)
		}
		done := waitCarriedOn(t, h, entry.ID)
		if done.CarriedOnTask.Or("") != second || out.Task != second {
			t.Fatalf("the entry was carried on by %s (answer named %s); want the standing claim's %s", done.CarriedOnTask.Or(""), out.Task, second)
		}
		secondPath, err := h.TaskCopy(second)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(secondPath, "finding.txt")); err != nil {
			t.Fatalf("the kept copy is not task %s's copy: %v", second, err)
		}
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
	// The worker starts in the kept copy, which stands, so it is swept first.
	service.RequireSweepClearsHere(t, "the carried-on task's worker starting in the copy every entry names")
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

// An entry of the copy recorded after the carry on was claimed, and before
// the copy moved, is carried on with it, the final full review of 74949eb:
// the worker commits again and the copy is kept again through the real
// Keep inside the seam between the claim and the completion, so the second
// entry is kept after the claim. The completion moves the whole copy, the
// second commit with it, so the mark answers and marks both entries with
// the one task, and that task dispatches and its worker starts. Without
// it the second entry stayed open naming a path that no longer stands, its
// copy gone, and held the project's new task, the carried-on one among
// them, from dispatching for as long as it stood.
func TestACarryOnCarriesOnAnEntryOfTheCopyKeptAfterItsClaim(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	// The worker starts in the kept copy, which stands, so it is swept first.
	service.RequireSweepClearsHere(t, "the carried-on task's worker starting once an entry kept after the claim is carried on too")
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, first := plantKeptInvestigation(t, h, subject, repository.ID)
	// The seam pauses the answer once, after its claim, and the test's own
	// goroutine commits and keeps the copy again before letting it go on.
	paused, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	o, _ := workerOptions(t, h)
	o = service.WithCarryOnClaimInterruptForTest(t, o, func() error {
		once.Do(func() {
			close(paused)
			<-resume
		})
		return nil
	})
	withServiceOptions(t, o, func(under serviceUnderTest) {
		ctx := t.Context()
		var out machine.KeptWorkAnswered
		answered := make(chan error, 1)
		go func() { answered <- under.client.Call(ctx, ipc.MethodKeptWorkAnswer, carryOnRequest(old, first), &out) }()
		select {
		case <-paused:
		case err := <-answered:
			t.Fatalf("the answer returned %+v, %v before its claim's seam ran", out, err)
		}
		if err := writeFile(first.CopyPath+"/more.txt", "what the worker found after the claim\n"); err != nil {
			close(resume)
			t.Fatal(err)
		}
		git(t, first.CopyPath, "add", "-A")
		git(t, first.CopyPath, "commit", "--quiet", "-m", "the worker's commit after the claim")
		second := keepTaskCopy(t, h, old)
		close(resume)
		if err := <-answered; err != nil {
			t.Fatalf("carrying the first entry on: %v", err)
		}
		claimed, err := recordsOf(t, h).KeptWorkEntry(ctx, first.ID)
		if err != nil {
			t.Fatal(err)
		}
		if second.ID == 0 || second.ID == first.ID || second.CopyPath != first.CopyPath ||
			!second.KeptAt.After(claimed.CarryOnClaimedAt.Or(time.Time{})) {
			t.Fatalf("the copy kept again in the seam reads %+v; want a second entry of the copy kept after the claim at %v",
				second, claimed.CarryOnClaimedAt)
		}
		newPath, err := h.TaskCopy(out.Task)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(newPath, "more.txt")); err != nil {
			t.Fatalf("the commit made after the claim is not in task %s's copy: %v", out.Task, err)
		}
		for _, id := range []int64{first.ID, second.ID} {
			got, err := recordsOf(t, h).KeptWorkEntry(ctx, id)
			if err != nil || got.Answer.Or("") != store.KeptCarryOn || got.CarriedOnTask.Or("") != out.Task || !got.CarriedOnAt.IsKnown() {
				t.Fatalf("entry %d reads %+v, %v; want it carried on by task %s", id, got, err, out.Task)
			}
		}
		// A task an open entry of its project holds never launches, so the
		// launch is that nothing holds it.
		waitLaunched(t, h, out.Task)
	})
}

// A carry on stopped after the copy was moved is completed by the next
// start though the copy went on past what it was kept at, the reviewer's
// sequence: a detached investigation's copy is kept at the worker's commit
// A, the worker then commits B on it with no second Keep, so A is B's
// ancestor and no longer a tip, and the carry on is claimed and stopped
// through the seam right after the move. The next start takes the copy at
// the new task's path as the kept one, since it still contains A, marks
// the entry, and the task's worker starts. Without reading containment
// rather than tips the moved copy is refused and the task is held forever.
func TestACarryOnStoppedAfterTheMoveAdoptsACopyThatWentOn(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	// The worker starts in the kept copy, which stands, so it is swept first.
	service.RequireSweepClearsHere(t, "a carry on stopped after the move launching in the copy that went on")
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

// A carry-on note is written whole or not at all, and a retry completes a
// note an earlier append left partial, the final review of 3088420: the
// carry on is stopped between the copy's move and the entry's mark with the
// new task's instructions holding the note's heading and its first words
// alone, as an append the filesystem took only part of leaves them. The
// next start completes the carry on with the whole note in the file, once,
// and marks the entry only then. Without the note compared whole, the
// heading was taken as the note, the entry was marked and the worker would
// start without what the note tells it.
func TestACarryOnCompletesACarryOnNoteAnAppendLeftPartial(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)

	partial := "\n\n" + service.CarryOnHeading + "\n\nYou were started in the working copy of an earl"
	stopped := errors.New("the service stopped while the carry-on note was appended")
	o, _ := workerOptions(t, h)
	withServiceOptions(t, service.WithCarryOnInterruptForTest(t, o, func() error {
		owed, err := recordsOf(t, h).KeptWorkEntry(t.Context(), entry.ID)
		if err != nil {
			return err
		}
		instructions, err := h.TaskInstructions(owed.CarriedOnTask.Or(""))
		if err != nil {
			return err
		}
		f, err := os.OpenFile(instructions, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		if _, err := f.WriteString(partial); err != nil {
			return err
		}
		return stopped
	}), func(under serviceUnderTest) {
		var out machine.KeptWorkAnswered
		if err := under.client.Call(t.Context(), ipc.MethodKeptWorkAnswer, carryOnRequest(old, entry), &out); err == nil || !strings.Contains(err.Error(), "stopped while") {
			t.Fatalf("the carry on past the stopped seam = %+v, %v; want the stop", out, err)
		}
	})
	owed, err := recordsOf(t, h).KeptWorkEntry(t.Context(), entry.ID)
	if err != nil || owed.CarriedOnAt.IsKnown() {
		t.Fatalf("after the stop the entry reads %+v, %v; want it owed", owed, err)
	}
	reserved := owed.CarriedOnTask.Or("")
	instructions, err := h.TaskInstructions(reserved)
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(instructions); err != nil || !strings.HasSuffix(string(raw), partial) {
		t.Fatalf("after the stop the instructions read %q, %v; want them ending in the partial note", raw, err)
	}

	o, _ = workerOptions(t, h)
	withServiceOptions(t, o, func(under serviceUnderTest) {
		waitCarriedOn(t, h, entry.ID)
	})
	raw, err := os.ReadFile(instructions)
	if err != nil {
		t.Fatal(err)
	}
	// The note is its section: from its heading to the next section the
	// program appends (dispatch's isolation check, once the task is
	// dispatched) or the end of the file.
	text := string(raw)
	at := strings.Index(text, service.CarryOnHeading)
	note := text[max(at, 0):]
	if end := strings.Index(note, "\n\n## "); end >= 0 {
		note = note[:end]
	}
	if at < 0 || strings.Count(text, service.CarryOnHeading) != 1 || !strings.HasSuffix(note, "rather than starting over.\n") ||
		!strings.Contains(note, "You were started in the working copy of an earlier task, "+old+",") {
		t.Fatalf("the completed carry on left the instructions reading:\n%s\nwant the whole note, once", text)
	}
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
	// The worker starts in the kept copy, which stands, so it is swept first.
	service.RequireSweepClearsHere(t, "the carried-on task launching in the kept copy and not a clone")
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
	advanceUpstream(t, repository.UpstreamURL, "c.txt", "someone else's work\n", "someone else's work")

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
	// The worker starts in the kept copy, which stands, so it is swept first.
	service.RequireSweepClearsHere(t, "the carried-on task launching once the refused copy is fixed")
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
	// The worker starts in the kept copy, which stands, so it is swept first.
	service.RequireSweepClearsHere(t, "the carried-on task launching once the refused copy is fixed")
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
	if _, err := records.BeginFirstTaskAttempt(t.Context(), id, store.OnThisMachine()); err != nil {
		t.Fatal(err)
	}
	if err := records.SetAttemptSession(t.Context(), id, 1, "pane-gone", store.SessionHost{}); err != nil {
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

// A delivery copy whose task branch cannot be created is not carried on,
// though no branch of that exact name stands: a failed delivery task's
// clone, shaped as dispatch shapes one, is kept through the real cleanup
// and Keep, and then the task's branch task/kept is deleted and HEAD put
// on a new branch task/kept/side, under the task's branch's name. The
// answer is refused naming HEAD and the branch in the way, records nothing
// and creates no task. With that branch renamed task, the name the task's
// branch sits under, the answer is refused the same way; with it renamed
// side, the same answer carries the copy on and the new task's worker
// starts. Without the check on names under and above the task's branch the
// answer is recorded, the copy moved, the branch's creation then fails,
// and the task is held for good on an entry whose carry on cannot
// complete, with land and discard no longer open to it.
func TestAKeptCopyWhoseTaskBranchCannotBeCreatedIsNotCarriedOn(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	// The worker starts in the kept copy, which stands, so it is swept first.
	service.RequireSweepClearsHere(t, "the carried-on task launching once the refused copy is fixed")
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
	if _, err := records.BeginFirstTaskAttempt(t.Context(), id, store.OnThisMachine()); err != nil {
		t.Fatal(err)
	}
	if err := records.SetAttemptSession(t.Context(), id, 1, "pane-gone", store.SessionHost{}); err != nil {
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
	git(t, copyPath, "checkout", "--quiet", "--detach")
	git(t, copyPath, "branch", "--quiet", "-D", branch)
	git(t, copyPath, "checkout", "--quiet", "-b", branch+"/side")
	before := taskCount(t, h)

	o, _ := workerOptions(t, h)
	withServiceOptions(t, o, func(under serviceUnderTest) {
		ctx := t.Context()
		for _, inTheWay := range []string{branch + "/side", "task"} {
			if inTheWay != branch+"/side" {
				git(t, copyPath, "branch", "--quiet", "-m", inTheWay)
			}
			var out machine.KeptWorkAnswered
			err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, carryOnRequest(id, entry), &out)
			if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), string(tasks.ComparisonHead)) ||
				!strings.Contains(err.Error(), "refs/heads/"+inTheWay) {
				t.Fatalf("carrying on a copy whose branch %s stands in the way of %s = %+v, %v; want it refused naming %s and it",
					inTheWay, branch, out, err, tasks.ComparisonHead)
			}
			if still, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID); err != nil || !still.Open() {
				t.Fatalf("after the refusal over %s the entry reads %+v, %v; want it open", inTheWay, still, err)
			}
			if n := taskCount(t, h); n != before {
				t.Fatalf("the refusal over %s created %d task(s)", inTheWay, n-before)
			}
			if _, err := os.Lstat(copyPath); err != nil {
				t.Fatalf("the refusal over %s moved the copy: %v", inTheWay, err)
			}
		}

		git(t, copyPath, "branch", "--quiet", "-m", "side")
		var out machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, carryOnRequest(id, entry), &out); err != nil {
			t.Fatalf("carrying the copy on with nothing in the way of the task's branch: %v", err)
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
	// The worker starts in the kept copy, which stands, so it is swept first.
	service.RequireSweepClearsHere(t, "the carried-on task launching in the asserted copy")
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

// A carry on adopts a kept copy only once it is verified to be the one its
// task recorded (tasks.Cleaner.VerifyKeptCopy, on internal/safety's
// terms), round 28: a reported investigation's kept copy holds a symbolic
// link to a directory outside it, which a path inside the copy could pass
// through. The carry on is refused naming the link, with the entry open
// and unclaimed, the copy where it was, and no task created. The positive
// control is TestCarryingKeptWorkOnCreatesATaskWhoseWorkerStartsInTheKeptCopy.
func TestACarryOnVerifiesTheCopyBeforeAdoptingIt(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	if err := os.Symlink(t.TempDir(), filepath.Join(entry.CopyPath, "linked")); err != nil {
		t.Fatal(err)
	}
	withService(t, h, func(under serviceUnderTest) {
		var out machine.KeptWorkAnswered
		err := under.client.Call(t.Context(), ipc.MethodKeptWorkAnswer, carryOnRequest(old, entry), &out)
		if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("the carry on of a copy holding a link to a directory = %+v, %v; want it refused naming the link", out, err)
		}
		if got, err := recordsOf(t, h).KeptWorkEntry(t.Context(), entry.ID); err != nil || !got.Open() || got.CarryOnClaimed() {
			t.Fatalf("the entry reads %+v, %v after the refusal; want it open and unclaimed", got, err)
		}
		if _, err := os.Lstat(filepath.Join(entry.CopyPath, "finding.txt")); err != nil {
			t.Fatalf("the kept copy is not where it was: %v", err)
		}
		if n := taskCount(t, h); n != 1 {
			t.Fatalf("the home records %d tasks after the refusal; want the outlived one alone", n)
		}
	})
}

// A carry on taken up after its copy was moved verifies the copy where it
// stands, at the reserved task's path, before anything of it is read or
// written, round 29's sequence: a failed delivery task's clone, its own
// commit made on a detached HEAD, is kept, and the carry on is claimed and
// stopped right after the move. The moved copy's .git/refs/heads is then a
// link into another clone holding that commit. The next service takes the
// carry on up at open (completeCarryOnsAtOpen), before it serves anything,
// and refuses it naming the link, which its log records, with the other
// clone's references as they were (read as files: git skips a reference
// naming an object the clone lacks), the entry owed and not marked. The
// take-up is read at open and not through an answer sent once the service
// serves: the refusal at open asks for a dispatch pass, which holds the new
// task's slot for a moment, and an answer landing then is refused as busy
// instead, as CI's repeat run on macOS saw. Before, the recovery took
// the moved copy through verifyAdopted, the reshape and the assertion
// without the verification, and the reshape wrote refs/heads/task/kept
// into the other clone.
func TestACarryOnTakenUpAfterTheMoveVerifiesTheCopyWhereItStands(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
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
	if _, err := records.BeginFirstTaskAttempt(t.Context(), id, store.OnThisMachine()); err != nil {
		t.Fatal(err)
	}
	if err := records.SetAttemptSession(t.Context(), id, 1, "pane-gone", store.SessionHost{}); err != nil {
		t.Fatal(err)
	}
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
	git(t, copyPath, "checkout", "--quiet", "--detach")
	if err := writeFile(filepath.Join(copyPath, "own.txt"), "the task's own work\n"); err != nil {
		t.Fatal(err)
	}
	git(t, copyPath, "add", "-A")
	git(t, copyPath, "commit", "--quiet", "-m", "the task's own commit")
	own := git(t, copyPath, "rev-parse", "HEAD")
	entry := keepTaskCopy(t, h, id)

	stopped := errors.New("the service stopped between the copy's move and the entry's mark")
	o, _ := workerOptions(t, h)
	withServiceOptions(t, service.WithCarryOnInterruptForTest(t, o, func() error { return stopped }), func(under serviceUnderTest) {
		var out machine.KeptWorkAnswered
		if err := under.client.Call(t.Context(), ipc.MethodKeptWorkAnswer, carryOnRequest(id, entry), &out); err == nil || !strings.Contains(err.Error(), "stopped between") {
			t.Fatalf("the carry on past the stopped seam = %+v, %v; want the stop", out, err)
		}
	})
	answered, err := recordsOf(t, h).KeptWorkEntry(t.Context(), entry.ID)
	if err != nil || answered.CarriedOnAt.IsKnown() {
		t.Fatalf("after the stop the entry reads %+v, %v; want it owed", answered, err)
	}
	newPath, err := h.TaskCopy(answered.CarriedOnTask.Or(""))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(newPath, "own.txt")); err != nil {
		t.Fatalf("after the stop the copy is not at the reserved task's path: %v", err)
	}
	other := filepath.Join(t.TempDir(), "other")
	git(t, filepath.Dir(other), "clone", "--quiet", "--no-local", repository.UpstreamURL, other)
	git(t, other, "fetch", "--quiet", newPath, own)
	heads := filepath.Join(newPath, ".git", "refs", "heads")
	if err := os.RemoveAll(heads); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(other, ".git", "refs", "heads"), heads); err != nil {
		t.Fatal(err)
	}
	// The other clone's references are read as files, so a reference git
	// would skip, one naming an object the clone lacks, is seen too.
	refsBefore := refFiles(t, other)

	o, _ = workerOptions(t, h)
	withServiceOptions(t, o, func(under serviceUnderTest) {
		owed := fmt.Sprintf("kept work entry %d holds a carry-on claim that could not be completed: ", entry.ID)
		var refused string
		for _, line := range strings.Split(readServiceLog(t, h), "\n") {
			if strings.Contains(line, owed) {
				refused = line
			}
		}
		if !strings.Contains(refused, "symbolic link") {
			t.Fatalf("the carry on taken up at open over a copy with refs/heads linked into another clone logged %q; want it refused naming the link:\n%s",
				refused, readServiceLog(t, h))
		}
		if refs := refFiles(t, other); refs != refsBefore {
			t.Fatalf("the other clone's references read\n%s\nafter the refusal; want them as they were\n%s", refs, refsBefore)
		}
		if got, err := recordsOf(t, h).KeptWorkEntry(t.Context(), entry.ID); err != nil || got.CarriedOnAt.IsKnown() {
			t.Fatalf("after the refusal the entry reads %+v, %v; want it owed and not marked", got, err)
		}
	})
}

// refFiles is every file under a clone's .git/refs and its packed-refs,
// each with what it holds, read from the file system.
func refFiles(t *testing.T, clone string) string {
	t.Helper()
	var lines []string
	root := filepath.Join(clone, ".git")
	for _, start := range []string{filepath.Join(root, "refs"), filepath.Join(root, "packed-refs")} {
		err := filepath.WalkDir(start, func(p string, d os.DirEntry, err error) error {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil || d.IsDir() {
				return err
			}
			raw, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			lines = append(lines, strings.TrimPrefix(p, root)+" "+strings.TrimSpace(string(raw)))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}

// A carry on stopped after the move is completed from the entry alone,
// as the header promises of every step after the claim, when the kept copy
// amended the commit it was kept on (the final full review of feddd14): the
// entry holds C, the copy's HEAD was amended to D before the answer, so C
// stands on no reference and in HEAD's reflog alone. The uninterrupted
// answer takes that copy, so the completion of the stopped one must too:
// the next start adopts the copy at the new task's path, marks the entry
// and launches the worker. Before the reflog counted, the completion
// refused the copy as not containing C and left the claim and the
// project's dispatch held for good.
func TestACarryOnStoppedAfterTheMoveAdoptsACopyWhoseKeptCommitWasAmended(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	// The worker starts in the kept copy, which stands, so it is swept first.
	service.RequireSweepClearsHere(t, "a carry on stopped after the move launching in the amended copy")
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
	if err := writeFile(entry.CopyPath+"/finding.txt", "what the worker found, reworded\n"); err != nil {
		t.Fatal(err)
	}
	git(t, entry.CopyPath, "add", "-A")
	git(t, entry.CopyPath, "commit", "--quiet", "--amend", "-m", "the worker's commit, amended")
	if amended := git(t, entry.CopyPath, "rev-parse", "HEAD"); amended == kept {
		t.Fatal("the amend left HEAD where it was")
	}

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

	o, _ = workerOptions(t, h)
	withServiceOptions(t, o, func(under serviceUnderTest) {
		if done := waitCarriedOn(t, h, entry.ID); done.CarriedOnTask.Or("") != reserved {
			t.Fatalf("the completed carry on names task %s, want %s", done.CarriedOnTask.Or(""), reserved)
		}
		waitLaunched(t, h, reserved)
	})
}

// A copy that no longer holds a commit it was kept on at all is not
// carried on, and the same reading decides before the claim as the
// completion's does after the move (the final full review of feddd14): the
// kept commit C is amended away, its reflog entries expired and the object
// pruned, so neither HEAD, a reference nor a reflog of the copy reaches C.
// The answer is refused naming C before anything is claimed: the entry is
// open and unclaimed, no task is created and the copy stands where it was,
// so a land of what the copy holds now stays open to it. The copy whose
// reflog still records C is the control, carried on in
// TestACarryOnStoppedAfterTheMoveAdoptsACopyWhoseKeptCommitWasAmended.
func TestACarryOnIsRefusedForACopyThatNoLongerHoldsWhatItWasKeptOn(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	kept := git(t, entry.CopyPath, "rev-parse", "HEAD")
	if err := writeFile(entry.CopyPath+"/finding.txt", "what the worker found, reworded\n"); err != nil {
		t.Fatal(err)
	}
	git(t, entry.CopyPath, "add", "-A")
	git(t, entry.CopyPath, "commit", "--quiet", "--amend", "-m", "the worker's commit, amended")
	git(t, entry.CopyPath, "reflog", "expire", "--expire=now", "--expire-unreachable=now", "--all")
	git(t, entry.CopyPath, "prune", "--expire=now")
	if _, err := vcsResolve(t, entry.CopyPath, kept); err == nil {
		t.Fatalf("the kept commit %s is still in the copy after the prune", kept)
	}
	before := taskCount(t, h)

	o, _ := workerOptions(t, h)
	withServiceOptions(t, o, func(under serviceUnderTest) {
		ctx := t.Context()
		var out machine.KeptWorkAnswered
		err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, carryOnRequest(old, entry), &out)
		if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), kept) {
			t.Fatalf("carrying on a copy that no longer holds %s = %+v, %v; want it refused naming the commit", kept, out, err)
		}
		still, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID)
		if err != nil || !still.Open() || still.CarryOnClaimed() {
			t.Fatalf("after the refusal the entry reads %+v, %v; want it open and unclaimed", still, err)
		}
		if n := taskCount(t, h); n != before {
			t.Fatalf("the refusal created %d task(s)", n-before)
		}
		if _, err := os.Stat(entry.CopyPath + "/finding.txt"); err != nil {
			t.Fatalf("the refusal moved the copy: %v", err)
		}
	})
}

// vcsResolve resolves rev in the repository at path through internal/vcs,
// as the service reads a copy.
func vcsResolve(t *testing.T, path, rev string) (string, error) {
	t.Helper()
	copied, err := vcs.OpenWorktree(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	return copied.ResolveCommit(t.Context(), rev)
}

// A carry on completed from a standing claim, before the copy is moved,
// reads the copy the way the answer and the completion after the move do
// (the final full review of feddd14): the answer claims the entry, and
// before the completion reads it the kept commit C is amended away,
// expired and pruned from the copy. The completion refuses naming C with
// nothing moved: the claim stands owed, the copy at the kept path, and the
// next start refuses it the same way. Without the check before the move
// the copy is moved and marked while it no longer holds what the entry
// recorded.
func TestACarryOnCompletedBeforeTheMoveReadsTheCopyAsTheAnswerDid(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	kept := git(t, entry.CopyPath, "rev-parse", "HEAD")
	prune := func() error {
		if err := writeFile(entry.CopyPath+"/finding.txt", "what the worker found, reworded\n"); err != nil {
			return err
		}
		git(t, entry.CopyPath, "add", "-A")
		git(t, entry.CopyPath, "commit", "--quiet", "--amend", "-m", "the worker's commit, amended")
		git(t, entry.CopyPath, "reflog", "expire", "--expire=now", "--expire-unreachable=now", "--all")
		git(t, entry.CopyPath, "prune", "--expire=now")
		return nil
	}

	o, _ := workerOptions(t, h)
	withServiceOptions(t, service.WithCarryOnClaimInterruptForTest(t, o, prune), func(under serviceUnderTest) {
		ctx := t.Context()
		var out machine.KeptWorkAnswered
		err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, carryOnRequest(old, entry), &out)
		if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), kept) {
			t.Fatalf("completing a carry on of a copy that lost %s after the claim = %+v, %v; want it refused naming the commit", kept, out, err)
		}
		owed, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID)
		if err != nil || !owed.Open() || !owed.CarryOnClaimed() {
			t.Fatalf("after the refusal the entry reads %+v, %v; want it open with its claim owed", owed, err)
		}
		if _, err := os.Stat(entry.CopyPath + "/finding.txt"); err != nil {
			t.Fatalf("the refusal moved the copy: %v", err)
		}
		newPath, err := h.TaskCopy(owed.CarriedOnTask.Or(""))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(newPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("something stands at the reserved task's path after the refusal: %v", err)
		}
	})
}

// Carrying on records the new task under the outlived task's posture when
// that is stricter than the project's, and under the project's otherwise
// (the final full review of feddd14): in a project whose trusted copy says
// local, a delivery task recorded gated is carried on, and its successor
// reads gated, so its own push starts a validation run as the outlived
// task's would have; with the trusted copy tightened to gated, a task
// recorded local is carried on and its successor reads gated, the
// project's. Without the posture carried the first successor read local,
// and the continued work's push started no run.
func TestCarryingOnKeepsTheOutlivedTasksStricterPosture(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)

	h := newHome(t)
	homeDelivery(t, h, config.DeliveryLocal)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	publishTrusted(t, subject, `{"delivery": "local"}`)
	// Both copies are cut before either is kept, since a kept entry holds
	// every later dispatch of its project.
	gatedOld := plantDeliveryCopyUnder(t, h, subject, repository, "task/gated", config.DeliveryGated)
	localOld := plantDeliveryCopyUnder(t, h, subject, repository, "task/local", config.DeliveryLocal)
	gatedEntry, localEntry := keepTaskCopy(t, h, gatedOld), keepTaskCopy(t, h, localOld)
	if !gatedEntry.Open() || !localEntry.Open() {
		t.Fatalf("the copies were kept as %+v and %+v; want two open entries", gatedEntry, localEntry)
	}

	o, _ := workerOptions(t, h)
	withServiceOptions(t, o, func(under serviceUnderTest) {
		ctx := t.Context()
		var out machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, carryOnRequest(gatedOld, gatedEntry), &out); err != nil {
			t.Fatalf("carrying the gated task's copy on in a local project: %v", err)
		}
		if created, err := recordsOf(t, h).Task(ctx, out.Task); err != nil || created.Delivery != store.Known(config.DeliveryGated) {
			t.Fatalf("the gated task's successor reads %+v, %v; want it recorded gated, the outlived task's stricter posture", created, err)
		}

		publishTrusted(t, subject, `{"delivery": "gated"}`)
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, carryOnRequest(localOld, localEntry), &out); err != nil {
			t.Fatalf("carrying the local task's copy on in a project tightened to gated: %v", err)
		}
		if created, err := recordsOf(t, h).Task(ctx, out.Task); err != nil || created.Delivery != store.Known(config.DeliveryGated) {
			t.Fatalf("the local task's successor reads %+v, %v; want it recorded gated, the project's posture now", created, err)
		}
	})
}

// plantDeliveryCopyUnder records a failed delivery task of the repository
// on branch under the posture, or with none recorded when posture is "",
// as a task from before intake recorded one, with its copy shaped as
// dispatch shapes one and holding its own commit, ready for keepTaskCopy.
// It returns the task.
func plantDeliveryCopyUnder(t *testing.T, h *home.Home, subject string, repository store.Repository, branch string,
	posture config.Delivery) string {
	t.Helper()
	ctx := t.Context()
	id, err := home.NewTaskID()
	if err != nil {
		t.Fatal(err)
	}
	copyPath, err := h.TaskCopy(id)
	if err != nil {
		t.Fatal(err)
	}
	records, err := store.Open(ctx, h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatal(err)
	}
	row := store.Task{
		ID: id, Shape: store.TaskDelivery, Project: "the parser", Mode: "headless", WorktreePath: copyPath,
		RepositoryID: store.Known(repository.ID), Branch: store.Known(branch),
	}
	if posture != "" {
		row.Delivery = store.Known(posture)
	}
	if _, err := records.CreateTask(ctx, row, store.Disposition{}); err != nil {
		t.Fatal(err)
	}
	if _, err := records.BeginFirstTaskAttempt(ctx, id, store.OnThisMachine()); err != nil {
		t.Fatal(err)
	}
	if err := records.SetAttemptSession(ctx, id, 1, "pane-gone", store.SessionHost{}); err != nil {
		t.Fatal(err)
	}
	if _, err := records.AppendTaskEvent(ctx, id, "failed", "the worker gave up"); err != nil {
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
	if err := writeFile(filepath.Join(copyPath, "own.txt"), "the task's own work on "+branch+"\n"); err != nil {
		t.Fatal(err)
	}
	git(t, copyPath, "add", "-A")
	git(t, copyPath, "commit", "--quiet", "-m", "the task's own commit")
	return id
}

// A kept copy is carried on when everything the entry recorded it standing
// on is still named in it, whatever names it (the delta review of 9ef7722):
// a tag on a blob, which Keep records by the blob, since the tag stands on
// it, is named by the tag still; and a commit HEAD was reset away from,
// with the reflogs expired, is named by ORIG_HEAD, the root reference the
// reset wrote. Both copies are carried on and their workers start. Before
// the reading took every name, each was refused as no longer holding what
// it was kept on, though nothing of it had gone, and the project stayed
// held.
func TestACarryOnTakesACopyWhoseKeptObjectsAreStillNamed(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)

	for _, c := range []struct {
		name string
		make func(t *testing.T, copyPath string) (object string, after func())
	}{
		{"a tag on a blob", func(t *testing.T, copyPath string) (string, func()) {
			if err := writeFile(copyPath+"/data.bin", "bytes the tag names\n"); err != nil {
				t.Fatal(err)
			}
			blob := git(t, copyPath, "hash-object", "-w", "data.bin")
			if err := os.Remove(copyPath + "/data.bin"); err != nil {
				t.Fatal(err)
			}
			git(t, copyPath, "tag", "data", blob)
			return blob, func() {}
		}},
		{"a commit ORIG_HEAD names after a reset", func(t *testing.T, copyPath string) (string, func()) {
			kept := git(t, copyPath, "rev-parse", "HEAD")
			return kept, func() {
				git(t, copyPath, "reset", "--quiet", "--hard", "HEAD~1")
				git(t, copyPath, "reflog", "expire", "--expire=now", "--expire-unreachable=now", "--all")
				if got := git(t, copyPath, "rev-parse", "ORIG_HEAD"); got != kept {
					t.Fatalf("ORIG_HEAD is %s after the reset, want the kept commit %s", got, kept)
				}
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			service.RequireSweepClearsHere(t, "the carried-on task launching in the copy still naming its kept objects")
			h := newHome(t)
			subject := newSubject(t)
			repository := recordRepository(t, h, subject)
			old, _ := plantInvestigationCopy(t, h, subject, repository.ID, "")
			copyPath, err := h.TaskCopy(old)
			if err != nil {
				t.Fatal(err)
			}
			object, after := c.make(t, copyPath)
			entry := keepTaskCopy(t, h, old)
			if !entry.Open() || !slices.Contains(entry.Holds, object) {
				t.Fatalf("the real Keep recorded %+v; want an open entry holding %s", entry, object)
			}
			after()

			o, _ := workerOptions(t, h)
			withServiceOptions(t, o, func(under serviceUnderTest) {
				var out machine.KeptWorkAnswered
				if err := under.client.Call(t.Context(), ipc.MethodKeptWorkAnswer, carryOnRequest(old, entry), &out); err != nil {
					t.Fatalf("carrying on a copy still naming %s: %v", object, err)
				}
				waitLaunched(t, h, out.Task)
			})
		})
	}
}

// A carry on closes every open entry of the copy, so it holds the copy to
// every one of them, not to the answered one alone (the final full review
// of ff040fc, P15): the copy is kept at C as entry A, kept again at B as a
// second entry, and then put back at C with B expired and pruned. Answering
// the second entry is refused naming B, and so is answering A, though A's
// own commit stands: the mark would close the second entry over work the
// successor never receives. Nothing is claimed and no task is created.
// Before, answering A succeeded and marked both entries carried on.
func TestACarryOnHoldsTheCopyToEveryEntryItWouldClose(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, first := plantKeptInvestigation(t, h, subject, repository.ID)
	c := git(t, first.CopyPath, "rev-parse", "HEAD")
	if err := writeFile(first.CopyPath+"/more.txt", "what the worker found next\n"); err != nil {
		t.Fatal(err)
	}
	git(t, first.CopyPath, "add", "-A")
	git(t, first.CopyPath, "commit", "--quiet", "-m", "the worker's second commit")
	b := git(t, first.CopyPath, "rev-parse", "HEAD")
	second := keepTaskCopy(t, h, old)
	if second.ID == first.ID || !second.Open() || !slices.Contains(second.Holds, b) || slices.Contains(first.Holds, b) {
		t.Fatalf("the copy kept again reads %+v beside %+v; want a second open entry holding %s, which the first does not", second, first, b)
	}
	// Back to C without a root reference naming B, then B expired and
	// pruned, so nothing the copy names reaches it.
	git(t, first.CopyPath, "checkout", "--quiet", "--detach", c)
	git(t, first.CopyPath, "reflog", "expire", "--expire=now", "--expire-unreachable=now", "--all")
	git(t, first.CopyPath, "prune", "--expire=now")
	if _, err := vcsResolve(t, first.CopyPath, b); err == nil {
		t.Fatalf("the second commit %s is still in the copy after the prune", b)
	}
	before := taskCount(t, h)

	o, _ := workerOptions(t, h)
	withServiceOptions(t, o, func(under serviceUnderTest) {
		ctx := t.Context()
		for _, entry := range []store.KeptWork{second, first} {
			var out machine.KeptWorkAnswered
			err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, carryOnRequest(old, entry), &out)
			if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), b) {
				t.Fatalf("carrying entry %d on while the copy no longer holds %s = %+v, %v; want it refused naming the commit", entry.ID, b, out, err)
			}
		}
		for _, id := range []int64{first.ID, second.ID} {
			got, err := recordsOf(t, h).KeptWorkEntry(ctx, id)
			if err != nil || !got.Open() || got.CarryOnClaimed() {
				t.Fatalf("after the refusals entry %d reads %+v, %v; want it open and unclaimed", id, got, err)
			}
		}
		if n := taskCount(t, h); n != before {
			t.Fatalf("the refusals created %d task(s)", n-before)
		}
	})
}

// A carry on closes no entry it did not hold the copy to (the carry-on
// audit of b448115, P6, P15): the copy is kept at C as entry A, and a late
// keep records a second entry standing on C and B after the completion's
// last reading of the copy, in the seam after the move, while the moved
// copy does not hold B. The mark refuses that entry unread, so the answer
// is refused with the claim standing and the late entry open; the same
// answer again reads both entries and is refused naming B. Before, the
// mark closed the late entry with A, carried on over work the successor
// never received.
func TestACarryOnClosesNoEntryKeptAfterItsLastReading(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, first := plantKeptInvestigation(t, h, subject, repository.ID)
	c := git(t, first.CopyPath, "rev-parse", "HEAD")
	if err := writeFile(first.CopyPath+"/more.txt", "what the worker found next\n"); err != nil {
		t.Fatal(err)
	}
	git(t, first.CopyPath, "add", "-A")
	git(t, first.CopyPath, "commit", "--quiet", "-m", "the worker's second commit")
	b := git(t, first.CopyPath, "rev-parse", "HEAD")
	git(t, first.CopyPath, "checkout", "--quiet", "--detach", c)
	git(t, first.CopyPath, "reflog", "expire", "--expire=now", "--expire-unreachable=now", "--all")
	git(t, first.CopyPath, "prune", "--expire=now")
	if _, err := vcsResolve(t, first.CopyPath, b); err == nil {
		t.Fatalf("the second commit %s is still in the copy after the prune", b)
	}
	var late store.KeptWork
	keepLate := func() error {
		records, err := store.Open(t.Context(), h.Database(), store.WithRedactor(redact.New()))
		if err != nil {
			return err
		}
		defer func() { _ = records.Close() }()
		late, _, err = records.RecordKeptWork(t.Context(), store.KeptCopy{TaskID: old, CopyPath: first.CopyPath,
			Holds: []string{c, b}, Lacks: []string{c, b}, Against: first.Against.Or(c), Reason: string(tasks.ReasonUnproven)})
		return err
	}

	o, _ := workerOptions(t, h)
	withServiceOptions(t, service.WithCarryOnInterruptForTest(t, o, keepLate), func(under serviceUnderTest) {
		ctx := t.Context()
		var out machine.KeptWorkAnswered
		err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, carryOnRequest(old, first), &out)
		if !errors.Is(err, ipc.ErrRefused) {
			t.Fatalf("the carry on with an entry kept after its last reading = %+v, %v; want it refused", out, err)
		}
		if got, err := recordsOf(t, h).KeptWorkEntry(ctx, late.ID); err != nil || !got.Open() {
			t.Fatalf("the late entry reads %+v, %v after the refused mark; want it open", got, err)
		}
		if got, err := recordsOf(t, h).KeptWorkEntry(ctx, first.ID); err != nil || !got.Open() || !got.CarryOnClaimed() {
			t.Fatalf("the answered entry reads %+v, %v; want it open with its claim standing", got, err)
		}
		err = under.client.Call(ctx, ipc.MethodKeptWorkAnswer, carryOnRequest(old, first), &out)
		if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), b) {
			t.Fatalf("the same answer again = %+v, %v; want it refused naming %s, which the late entry recorded", out, err, b)
		}
		for _, id := range []int64{first.ID, late.ID} {
			if got, err := recordsOf(t, h).KeptWorkEntry(ctx, id); err != nil || !got.Open() {
				t.Fatalf("entry %d reads %+v, %v; want it open", id, got, err)
			}
		}
	})
}

// Carrying on a delivery task recorded before intake wrote a posture holds
// its successor to gated, the posture its own push is held to
// (localPosture: a row with no posture is not local), in a project that
// now permits local (the carry-on audit of b448115). The control is a task
// recorded gated, carried on gated. Before, the successor of the legacy
// task was recorded local and its push started no run.
func TestCarryingOnKeepsALegacyDeliveryTasksGatedPosture(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)

	h := newHome(t)
	homeDelivery(t, h, config.DeliveryLocal)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	publishTrusted(t, subject, `{"delivery": "local"}`)
	legacyOld := plantDeliveryCopyUnder(t, h, subject, repository, "task/legacy", "")
	gatedOld := plantDeliveryCopyUnder(t, h, subject, repository, "task/gated", config.DeliveryGated)
	legacyEntry, gatedEntry := keepTaskCopy(t, h, legacyOld), keepTaskCopy(t, h, gatedOld)
	if row, err := recordsOf(t, h).Task(t.Context(), legacyOld); err != nil || row.Delivery.IsKnown() {
		t.Fatalf("the legacy task reads %+v, %v; want no posture recorded", row, err)
	}

	o, _ := workerOptions(t, h)
	withServiceOptions(t, o, func(under serviceUnderTest) {
		ctx := t.Context()
		for _, c := range []struct {
			name  string
			old   string
			entry store.KeptWork
		}{{"legacy", legacyOld, legacyEntry}, {"gated", gatedOld, gatedEntry}} {
			var out machine.KeptWorkAnswered
			if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, carryOnRequest(c.old, c.entry), &out); err != nil {
				t.Fatalf("carrying the %s task's copy on in a local project: %v", c.name, err)
			}
			if created, err := recordsOf(t, h).Task(ctx, out.Task); err != nil || created.Delivery != store.Known(config.DeliveryGated) {
				t.Fatalf("the %s task's successor reads %+v, %v; want it recorded gated", c.name, created, err)
			}
		}
	})
}

// A kept copy whose core.worktree names where it stands is not carried on
// (the carry-on audit of b448115): carrying on moves the copy, and git
// would go on addressing the old path, so the reshape after the move fails
// and the claim is stranded. The answer is refused before anything is
// claimed, naming the setting, with the copy where it was; with the
// setting removed the same answer carries the copy on and its worker
// starts.
func TestAKeptCopyWithACoreWorktreeIsNotCarriedOn(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	// The worker starts in the kept copy, which stands, so it is swept first.
	service.RequireSweepClearsHere(t, "the carried-on task launching once core.worktree is unset")
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	git(t, entry.CopyPath, "config", "core.worktree", entry.CopyPath)
	before := taskCount(t, h)

	o, _ := workerOptions(t, h)
	withServiceOptions(t, o, func(under serviceUnderTest) {
		ctx := t.Context()
		var out machine.KeptWorkAnswered
		err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, carryOnRequest(old, entry), &out)
		if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), "core.worktree") {
			t.Fatalf("carrying on a copy with core.worktree set = %+v, %v; want it refused naming the setting", out, err)
		}
		if still, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID); err != nil || !still.Open() || still.CarryOnClaimed() {
			t.Fatalf("after the refusal the entry reads %+v, %v; want it open and unclaimed", still, err)
		}
		if _, err := os.Stat(entry.CopyPath + "/finding.txt"); err != nil {
			t.Fatalf("the refusal moved the copy: %v", err)
		}
		if n := taskCount(t, h); n != before {
			t.Fatalf("the refusal created %d task(s)", n-before)
		}
		git(t, entry.CopyPath, "config", "--unset", "core.worktree")
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, carryOnRequest(old, entry), &out); err != nil {
			t.Fatalf("carrying the copy on with core.worktree unset: %v", err)
		}
		waitLaunched(t, h, out.Task)
	})
}

// A carry on closes no entry over work the copy lost after the move: the
// copy is read last right before the mark, where it stands, so every step
// of the completion between the move and the mark, the reshape, the
// assertion and the note, is behind that reading (round 47, P6). The kept
// commit C is checked out from under, expired and pruned in the seam after
// the move; the answer is refused naming C, with the copy at the task's
// path and the claim standing owed, and the same answer again is refused
// the same way. Before, the mark closed the entry with the set the reading
// before the move had verified, and the successor owned a copy without C.
func TestACarryOnClosesNoEntryOverWorkLostAfterTheMove(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P6)
	principles.Cite(t, principles.P15)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	kept := git(t, entry.CopyPath, "rev-parse", "HEAD")
	var moved string
	lose := func() error {
		records, err := store.Open(t.Context(), h.Database(), store.WithRedactor(redact.New()))
		if err != nil {
			return err
		}
		defer func() { _ = records.Close() }()
		owed, err := records.KeptWorkEntry(t.Context(), entry.ID)
		if err != nil {
			return err
		}
		if moved, err = h.TaskCopy(owed.CarriedOnTask.Or("")); err != nil {
			return err
		}
		git(t, moved, "checkout", "--quiet", "--detach", "HEAD~1")
		git(t, moved, "reflog", "expire", "--expire=now", "--expire-unreachable=now", "--all")
		git(t, moved, "prune", "--expire=now")
		if _, err := vcsResolve(t, moved, kept); err == nil {
			return errors.New("the kept commit is still in the moved copy after the prune")
		}
		return nil
	}

	o, _ := workerOptions(t, h)
	withServiceOptions(t, service.WithCarryOnInterruptForTest(t, o, lose), func(under serviceUnderTest) {
		ctx := t.Context()
		for _, when := range []string{"the answer", "the same answer again"} {
			// A concurrent dispatch can hold the new task's slot and refuse
			// with PhraseCarryOnSlotBusy; answer again until the lost-commit
			// refusal, bounded by the test's deadline (#707, #731).
			deadline := time.Now().Add(30 * time.Second)
			var out machine.KeptWorkAnswered
			var err error
			for {
				err = under.client.Call(ctx, ipc.MethodKeptWorkAnswer, carryOnRequest(old, entry), &out)
				if errors.Is(err, ipc.ErrRefused) && strings.Contains(err.Error(), kept) {
					break
				}
				if errors.Is(err, ipc.ErrRefused) && strings.Contains(err.Error(), machine.PhraseCarryOnSlotBusy.String()) {
					if !time.Now().Before(deadline) {
						t.Fatalf("%s kept getting the slot-busy refusal past the deadline = %v", when, err)
					}
					continue
				}
				t.Fatalf("%s, with %s lost after the move = %+v, %v; want it refused naming the commit", when, kept, out, err)
			}
			owed, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID)
			if err != nil || !owed.Open() || !owed.CarryOnClaimed() {
				t.Fatalf("after %s the entry reads %+v, %v; want it open with its claim owed", when, owed, err)
			}
			if _, err := os.Stat(moved + "/.git"); err != nil {
				t.Fatalf("after %s the copy does not stand at the task's path: %v", when, err)
			}
			if _, err := os.Lstat(entry.CopyPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("after %s something stands at the kept path: %v", when, err)
			}
		}
	})
}

// A carry-on answer that finds the new task's slot held by a dispatch pass
// is refused at once with PhraseCarryOnSlotBusy, never with "being carried
// on by task T now" for a carry-on that is not under way (#707, #731, P15).
// The first answer is refused after the move with the kept commit lost; the
// dispatch that refusal asks for is held open between its held refusal and
// the slot's release while the same answer is given again.
func TestACarryOnAnswerRefusesWhileADispatchPassHoldsItsSlot(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	kept := git(t, entry.CopyPath, "rev-parse", "HEAD")
	lose := func() error {
		records, err := store.Open(t.Context(), h.Database(), store.WithRedactor(redact.New()))
		if err != nil {
			return err
		}
		defer func() { _ = records.Close() }()
		owed, err := records.KeptWorkEntry(t.Context(), entry.ID)
		if err != nil {
			return err
		}
		moved, err := h.TaskCopy(owed.CarriedOnTask.Or(""))
		if err != nil {
			return err
		}
		git(t, moved, "checkout", "--quiet", "--detach", "HEAD~1")
		git(t, moved, "reflog", "expire", "--expire=now", "--expire-unreachable=now", "--all")
		git(t, moved, "prune", "--expire=now")
		return nil
	}

	type answered struct {
		out machine.KeptWorkAnswered
		err error
	}
	var (
		once     sync.Once
		letGo    sync.Once
		inWindow = make(chan string, 1)
		second   = make(chan answered, 1)
		leave    = make(chan struct{})
	)
	release := func() { letGo.Do(func() { close(leave) }) }
	t.Cleanup(release)
	held := func(id string) {
		first := false
		once.Do(func() { first = true })
		if !first {
			return
		}
		inWindow <- id
		<-leave
	}

	o, _ := workerOptions(t, h)
	o = service.WithHeldDispatchWindowForTest(service.WithCarryOnInterruptForTest(t, o, lose), held)
	withServiceOptions(t, o, func(under serviceUnderTest) {
		defer release()
		ctx := t.Context()
		var out machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, carryOnRequest(old, entry), &out); !errors.Is(err, ipc.ErrRefused) ||
			!strings.Contains(err.Error(), kept) {
			t.Fatalf("the answer, with %s lost after the move = %+v, %v; want it refused naming the commit", kept, out, err)
		}
		owed, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID)
		if err != nil || !owed.CarryOnClaimed() {
			t.Fatalf("after the first answer the entry reads %+v, %v; want its claim owed", owed, err)
		}
		var id string
		select {
		case id = <-inWindow:
		case <-ctx.Done():
			t.Fatal("no dispatch pass held the carried-on task after the refused answer")
		}
		if want := owed.CarriedOnTask.Or(""); id != want {
			release()
			t.Fatalf("the held dispatch was of task %s; want the carried-on task %s", id, want)
		}

		go func() {
			var again machine.KeptWorkAnswered
			err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, carryOnRequest(old, entry), &again)
			second <- answered{again, err}
		}()
		var got answered
		select {
		case got = <-second:
		case <-time.After(2 * time.Second):
			release()
			t.Fatal("the same answer again still waited while a dispatch held the slot; want an immediate busy refusal")
		case <-ctx.Done():
			release()
			t.Fatal("the same answer again neither refused nor returned")
		}
		busy := machine.PhraseCarryOnSlotBusy.String()
		line := ""
		if got.err != nil {
			line = got.err.Error()
		}
		if !errors.Is(got.err, ipc.ErrRefused) || !strings.Contains(line, busy) {
			release()
			t.Fatalf("the same answer again, while a dispatch held task %s's slot = %+v, %v; want it refused naming %q", id, got.out, got.err, busy)
		}
		if !strings.Contains(line, entry.Key()) {
			release()
			t.Fatalf("the busy refusal %q does not name the hold key %s the person typed", line, entry.Key())
		}
		if strings.Contains(line, id) {
			release()
			t.Fatalf("the busy refusal names the task identifier %s: %q", id, line)
		}
		for _, term := range []string{"dispatch", "claim", "slot", "settled", "being carried on by task"} {
			if strings.Contains(line, term) {
				release()
				t.Fatalf("the busy refusal carries internal wording %q: %q", term, line)
			}
		}
		if owed, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID); err != nil || !owed.Open() || !owed.CarryOnClaimed() {
			release()
			t.Fatalf("after the busy refusal the entry reads %+v, %v; want it open with its claim owed", owed, err)
		}
		release()
	})
}

// A kept copy with a linked worktree is not carried on (round 47): a
// linked worktree names the copy's repository by the path it stands at,
// so the move would leave the worktree unable to read it, and the program
// writes into no worktree it did not make. The answer is refused before
// anything is claimed, naming the worktree, with the copy where it was;
// with the worktree removed the same answer carries the copy on and its
// worker starts.
func TestAKeptCopyWithALinkedWorktreeIsNotCarriedOn(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	// The worker starts in the kept copy, which stands, so it is swept first.
	service.RequireSweepClearsHere(t, "the carried-on task launching once the worktree is removed")
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	linked := t.TempDir() + "/linked"
	git(t, entry.CopyPath, "worktree", "add", "--quiet", "--detach", linked)
	before := taskCount(t, h)

	o, _ := workerOptions(t, h)
	withServiceOptions(t, o, func(under serviceUnderTest) {
		ctx := t.Context()
		var out machine.KeptWorkAnswered
		err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, carryOnRequest(old, entry), &out)
		if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), "worktree") || !strings.Contains(err.Error(), "linked") {
			t.Fatalf("carrying on a copy with a linked worktree = %+v, %v; want it refused naming the worktree", out, err)
		}
		if still, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID); err != nil || !still.Open() || still.CarryOnClaimed() {
			t.Fatalf("after the refusal the entry reads %+v, %v; want it open and unclaimed", still, err)
		}
		if _, err := os.Stat(entry.CopyPath + "/finding.txt"); err != nil {
			t.Fatalf("the refusal moved the copy: %v", err)
		}
		if n := taskCount(t, h); n != before {
			t.Fatalf("the refusal created %d task(s)", n-before)
		}
		if head := git(t, linked, "rev-parse", "HEAD"); head == "" {
			t.Fatal("the linked worktree no longer reads its HEAD after the refusal")
		}
		git(t, entry.CopyPath, "worktree", "remove", "--force", linked)
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, carryOnRequest(old, entry), &out); err != nil {
			t.Fatalf("carrying the copy on with the worktree removed: %v", err)
		}
		waitLaunched(t, h, out.Task)
	})
}
