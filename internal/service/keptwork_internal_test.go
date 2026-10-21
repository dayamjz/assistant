package service

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/tasks"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// PRD principle P15's "kept work is never silent", at the task-copy site,
// through the real service and its supervisor loop: an investigation whose
// findings are recorded relayed and whose worker has ended is owed its
// copy's cleanup, which the loop's pass asks (a report alone keeps the copy
// for a promotion, on landed.go's terms), and a copy holding a commit no proof can place anywhere
// safe is refused as unproven and kept. That refusal becomes one kept work
// entry naming the task, its reason and the commits the copy holds; the next
// pass raises it as one kept-work wake, actionable and appended, records
// when it surfaced, and no later pass raises it again; the catch-up read
// lists it; and the copy is left standing. Without the entry's writer the
// poll for the entry fails, and without the loop's detection the pass
// raises nothing.
func TestARefusedTaskCleanupIsKeptWorkThatSurfacesOnce(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	h, records := loopHome(t)
	ctx := t.Context()
	const id = "keptinvestig"
	copyPath, err := h.TaskCopy(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := records.CreateTask(ctx, store.Task{
		ID: id, Shape: store.TaskInvestigation, Project: "one", Mode: "headless", WorktreePath: copyPath,
	}, store.Disposition{}); err != nil {
		t.Fatalf("recording the task: %v", err)
	}
	if _, err := records.BeginTaskAttempt(ctx, id, 0); err != nil {
		t.Fatal(err)
	}
	if err := records.SetAttemptSession(ctx, id, 1, "pane-kept"); err != nil {
		t.Fatal(err)
	}
	// The copy holds a commit of the worker's that no remote has, and the
	// task's record names no repository, so none of P12's proofs can be made.
	if err := os.MkdirAll(copyPath, 0o700); err != nil {
		t.Fatal(err)
	}
	heldGit(t, copyPath, "init", "--quiet", "-b", "main", ".")
	if err := os.WriteFile(copyPath+"/finding.txt", []byte("what the worker found\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	heldGit(t, copyPath, "add", "-A")
	heldGit(t, copyPath, "commit", "--quiet", "-m", "the worker's own commit")
	head := heldGit(t, copyPath, "rev-parse", "HEAD")
	reporter, err := tasks.NewCleaner(tasks.CleanupDeps{Home: h, Store: records, Forge: forge.NewGitHubHost(redact.New(), forge.WithBinary(filepath.Join(t.TempDir(), "absent-gh")))})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := reporter.StoreReport(ctx, id, `{"summary":"looked and committed what it found"}`)
	if err != nil {
		t.Fatalf("storing the report: %v", err)
	}
	// The coordinator relays the findings, which is what owes the copy its
	// cleanup; the record is task.relay's, anchored on the report's entry.
	if _, _, err := records.RecordTaskRelay(ctx, id, stored.Event.Sequence); err != nil {
		t.Fatalf("recording the findings relayed: %v", err)
	}

	terminals := newScriptedTerminals()
	terminals.dead("pane-kept")
	l := openLoop(t, h, records, terminals, &fakeClock{now: time.Now()})
	defer func() { _ = l.service.Close() }()
	l.nextPass(t)

	var entry store.KeptWork
	deadline := time.Now().Add(loopWait)
	for {
		kept, err := l.records(t).OpenKeptWork(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(kept) > 0 {
			if len(kept) != 1 {
				t.Fatalf("the refused cleanup wrote %d entries, want one: %+v", len(kept), kept)
			}
			entry = kept[0]
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("no kept work entry was written for the refused cleanup; the log reads:\n%s", serviceLogOf(t, h))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if entry.TaskID.Or("") != id || entry.RunID.IsKnown() || entry.Reason != string(tasks.ReasonUnproven) ||
		!slices.Contains(entry.Holds, head) || entry.CopyPath != copyPath || entry.Answered() {
		t.Fatalf("the entry = %+v, want task %s's unanswered copy at %s, unproven, holding %s", entry, id, copyPath, head)
	}
	if redact.New().Redact(entry.Detail) != entry.Detail || entry.Detail == "" {
		t.Fatalf("the entry's detail %q is not the redacted account of the refusal", entry.Detail)
	}

	got := detectionsOf(l.pass(t), id, supervisor.KindKeptWork)
	if len(got) != 1 || got[0].Marker != keptWorkMarker(entry.ID) ||
		got[0].Verdict.Reason != supervisor.ReasonActionable || got[0].Disposition != store.WakeAppended {
		t.Fatalf("the pass after the entry = %+v, want entry %d surfaced as actionable and appended", got, entry.ID)
	}
	for range 3 {
		if again := detectionsOf(l.pass(t), id, supervisor.KindKeptWork); len(again) != 0 {
			t.Fatalf("a later pass raised the entry again: %+v", again)
		}
	}
	if rows := wakesOfKind(t, l.records(t), id, supervisor.KindKeptWork); len(rows) != 1 || rows[0].Marker != keptWorkMarker(entry.ID) {
		t.Fatalf("the task's kept-work wakes = %+v, want exactly one about entry %d", rows, entry.ID)
	}
	if after, err := l.records(t).KeptWorkEntry(ctx, entry.ID); err != nil || !after.SurfacedAt.IsKnown() || after.Answered() {
		t.Fatalf("after the wake the entry reads %+v (%v), want it surfaced and still unanswered", after, err)
	}
	read, err := l.service.catchUp(ctx)
	if err != nil {
		t.Fatalf("the catch-up read: %v", err)
	}
	if len(read.Kept) != 1 || read.Kept[0].ID != entry.ID {
		t.Fatalf("the catch-up read lists kept work %+v, want entry %d", read.Kept, entry.ID)
	}
	if _, err := os.Stat(copyPath + "/finding.txt"); err != nil {
		t.Fatalf("the kept copy was touched: %v", err)
	}
}

// The task-copy half of completing an interrupted removal at the next open,
// through the real decision end to end. A landed delivery task's copy holds
// a commit on no remote, so the real cleanup refuses it as unproven and the
// refusal becomes a kept work entry. The commit is then pushed to the
// upstream's task branch, the real cleanup establishes the remote-branch
// proof and removes the copy, and the test-only seam stops it before the
// mark, leaving a proof, no copy and an open entry. The next open's
// recovery completes the mark from the recorded proof and the entry closes
// with it. Without recovery's task half the entry stays open.
func TestAnInterruptedTaskCopyRemovalIsCompletedAtTheNextOpen(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	h, records := loopHome(t)
	ctx := t.Context()
	primary, upstream, _ := heldSubject(t)
	repository, err := records.UpsertRepository(ctx, store.Repository{
		ID: "subject", WorkingPath: primary, UpstreamURL: upstream, DefaultBranch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	const id, branch = "keptunmarked", "task/one"
	copyPath, err := h.TaskCopy(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := records.CreateTask(ctx, store.Task{
		ID: id, Shape: store.TaskDelivery, Project: "one", Mode: "headless", WorktreePath: copyPath,
		RepositoryID: store.Known(repository.ID), Branch: store.Known(branch),
	}, store.Disposition{}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(copyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	heldGit(t, filepath.Dir(copyPath), "clone", "--quiet", primary, copyPath)
	heldGit(t, copyPath, "checkout", "--quiet", "-b", branch)
	if err := os.WriteFile(filepath.Join(copyPath, "work.txt"), []byte("the task's work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	heldGit(t, copyPath, "add", "-A")
	heldGit(t, copyPath, "commit", "--quiet", "-m", "the task's work")
	// The task is landed by a merge record of a run attributed to it, so a
	// restart neither dispatches nor relaunches it and its copy is owed the
	// cleanup decision.
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := records.CreateRun(ctx, store.Run{
		ID: "run-landed", RepositoryID: repository.ID, Branch: branch, SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "validate", IntentSource: "push", ConfigDigest: "cfg", Build: build,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := records.TransitionRun(ctx, "run-landed", []store.RunStatus{store.RunPending}, store.RunRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := records.TransitionRunOnPullRequest(ctx, "run-landed", []store.RunStatus{store.RunRunning}, store.RunPassed,
		store.PullRequestRead{PullRequest: 7, Head: "aaaa", End: store.PullRequestMerged}, store.HoldClosure{}); err != nil {
		t.Fatal(err)
	}

	cleaner, err := tasks.NewCleaner(tasks.CleanupDeps{Home: h, Store: records,
		Forge: forge.NewGitHubHost(redact.New(), forge.WithBinary(filepath.Join(t.TempDir(), "absent-gh")))})
	if err != nil {
		t.Fatal(err)
	}
	_, err = cleaner.CleanUp(ctx, tasks.TaskCopy{Task: id, Terminal: taskstate.Dead})
	var refused *tasks.CleanupRefusal
	if !errors.As(err, &refused) || refused.Reason != tasks.ReasonUnproven {
		t.Fatalf("the first cleanup = %v, want an unproven refusal over work on no remote", err)
	}
	kept, ok := tasks.KeptOf(refused)
	if !ok {
		t.Fatal("an unproven refusal is not kept work")
	}
	entry, err := cleaner.Keep(ctx, kept)
	if err != nil || !entry.Recorded {
		t.Fatalf("Keep = %+v, %v", entry, err)
	}

	heldGit(t, copyPath, "push", "--quiet", upstream, "HEAD:refs/heads/"+branch)
	stopped := errors.New("the service stopped between the removal and the mark")
	if err := cleaner.InterruptBeforeMarkForTest(t, func() error { return stopped }); err != nil {
		t.Fatal(err)
	}
	if _, err := cleaner.CleanUp(ctx, tasks.TaskCopy{Task: id, Terminal: taskstate.Dead}); !errors.Is(err, stopped) {
		t.Fatalf("the second cleanup = %v, want it stopped between the removal and the mark", err)
	}
	if _, err := os.Stat(copyPath); !os.IsNotExist(err) {
		t.Fatalf("the interrupted removal left the copy: %v", err)
	}
	recorded, err := records.TaskCleanup(ctx, id)
	if err != nil || recorded.Proof != store.CleanupProofRemoteBranch || recorded.RemovedAt.IsKnown() {
		t.Fatalf("after the interruption the cleanup record reads %+v (%v), want the remote-branch proof unmarked", recorded, err)
	}
	if got, err := records.KeptWorkEntry(ctx, entry.Entry.ID); err != nil || !got.Open() {
		t.Fatalf("after the interruption the entry reads %+v (%v), want it open, which is the window under test", got, err)
	}

	l := openLoop(t, h, records, newScriptedTerminals(), &fakeClock{now: time.Now()})
	defer func() { _ = l.service.Close() }()
	deadline := time.Now().Add(loopWait)
	for {
		got, err := l.records(t).KeptWorkEntry(ctx, entry.Entry.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Open() {
			if got.Closure.Or("") != store.KeptRemovedOnProof || got.ClosedProof.Or("") != store.CleanupProofRemoteBranch {
				t.Fatalf("the entry closed as %+v, want removed on the remote-branch proof", got)
			}
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the next open left the entry open over a copy removed on a recorded proof; the log reads:\n%s", serviceLogOf(t, h))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if cleanup, err := l.records(t).TaskCleanup(ctx, id); err != nil || !cleanup.RemovedAt.IsKnown() {
		t.Fatalf("the task's cleanup record reads %+v (%v), want the removal marked", cleanup, err)
	}
}

// A cancel that arrives while a stage body is still inside the run leaves
// the copy's cleanup to the segment, and the segment honours it on the path
// where its body returns context.Canceled without settling: the run's copy,
// holding a file no commit records, becomes a kept work entry once the
// segment returns. The body is held inside past the cancel and then let out,
// so it returns its cancelled context's error with the run already ended. Without carryOn's reclaim of a finished run nothing asks
// the cleanup and the kept work is silent.
func TestACancelInsideAStageBodyStillRecordsTheRunsKeptWork(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	held := newHeldService(t)
	held.pastCancel.Store(true)
	record := held.begin(t)
	copyPath := held.service.home.Worktree("subject", record.ID)
	if err := os.WriteFile(filepath.Join(copyPath, "uncommitted.txt"), []byte("work the body left\n"), 0o600); err != nil {
		t.Fatalf("leaving work in the run's copy: %v", err)
	}
	if !held.service.isAdvancing(record.ID) {
		t.Fatal("the segment is not inside the run, so this is not the case under test")
	}
	if _, err := held.service.cancel(t.Context(), machine.CancelRequest{Run: record.ID}); err != nil {
		t.Fatalf("ending the run: %v", err)
	}
	// The body is still inside, so the cancel left the reclaim to it.
	if kept, err := held.service.store.OpenKeptWork(t.Context()); err != nil || len(kept) != 0 {
		t.Fatalf("the cancel reclaimed under a body still inside the run: %+v, %v", kept, err)
	}
	held.let()
	held.awaitSegment(t)

	kept, err := held.service.store.OpenKeptWork(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 || kept[0].RunID.Or("") != record.ID || kept[0].Reason != string(tasks.ReasonCopyUntracked) {
		t.Fatalf("after the cancelled segment returned the open kept work is %+v, want run %s's copy kept for its untracked file", kept, record.ID)
	}
	if _, err := os.Stat(filepath.Join(copyPath, "uncommitted.txt")); err != nil {
		t.Fatalf("the kept copy's file was touched: %v", err)
	}
}
