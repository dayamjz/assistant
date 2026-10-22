package service

import (
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
)

// plantKeptRun records a repository and a failed run of it on branch, with
// an open kept work entry about the run's copy, as a person's push leaves
// one, and returns the entry. The copy is not on disk, so the service's
// recovery leaves the entry open and asks nothing of it.
func plantKeptRun(t *testing.T, records *store.Store, runID, branch string) store.KeptWork {
	t.Helper()
	ctx := t.Context()
	if _, err := records.UpsertRepository(ctx, store.Repository{
		ID: "subject", WorkingPath: t.TempDir(), UpstreamURL: "/nowhere/upstream.git", DefaultBranch: "main",
	}); err != nil {
		t.Fatal(err)
	}
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := records.CreateRun(ctx, store.Run{
		ID: runID, RepositoryID: "subject", Branch: branch, SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "validate", IntentSource: "push", ConfigDigest: "cfg", Build: build,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := records.TransitionRun(ctx, runID, []store.RunStatus{store.RunPending}, store.RunRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := records.TransitionRun(ctx, runID, []store.RunStatus{store.RunRunning}, store.RunFailed); err != nil {
		t.Fatal(err)
	}
	entry, _, err := records.RecordKeptWork(ctx, store.KeptCopy{RunID: runID, CopyPath: "/nowhere/worktrees/" + runID,
		Holds: []string{"aaaa"}, Reason: "unproven", Against: "dddd"})
	if err != nil {
		t.Fatalf("RecordKeptWork: %v", err)
	}
	return entry
}

// keptWorkWakeDetections is a pass's detections over the kept work no task
// names, for one entry.
func keptWorkWakeDetections(r passReport, entry int64) []detection {
	return r.KeptWork[entry].Detections
}

// PRD section 8's kept-work wake surfaces an entry once whichever shape
// carries it, through the real service and its loop. An entry about a run
// no task names is raised once on the queue's third shape, actionable and
// appended, and no later pass raises it again; when a task is then linked
// to that run, the entry is on the task and is still not raised on the
// task's wake. Without the task path's surfaced check the linked task
// raises it a second time.
func TestAKeptWorkEntryRaisedWithNoTaskIsNotRaisedAgainOnATaskLinkedLater(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	h, records := loopHome(t)
	entry := plantKeptRun(t, records, "run-a-persons-push", "work")
	l := openLoop(t, h, records, newScriptedTerminals(), &fakeClock{now: time.Now()})
	defer func() { _ = l.service.Close() }()

	got := keptWorkWakeDetections(l.nextPass(t), entry.ID)
	if len(got) != 1 || got[0].Kind != supervisor.KindKeptWork || got[0].Marker != keptWorkMarker(entry.ID) ||
		got[0].Verdict.Reason != supervisor.ReasonActionable || got[0].Disposition != store.WakeAppended {
		t.Fatalf("the pass over entry %d = %+v, want it raised as actionable and appended", entry.ID, got)
	}
	for range 2 {
		if again := l.pass(t); len(again.KeptWork) != 0 {
			t.Fatalf("a later pass raised kept work no task names again: %+v", again.KeptWork)
		}
	}
	rows, err := l.records(t).KeptWorkWakes(t.Context())
	if err != nil || len(rows) != 1 || rows[0].KeptWork != entry.ID || rows[0].Project != store.Known("subject") || rows[0].Branch != store.Known("work") {
		t.Fatalf("the kept-work wakes are %+v, %v; want entry %d's one, under project subject and branch work", rows, err, entry.ID)
	}
	if surfaced, err := l.records(t).KeptWorkEntry(t.Context(), entry.ID); err != nil || surfaced.SurfacedAt != store.Known(rows[0].CreatedAt) {
		t.Fatalf("the entry reads %+v, %v; want it surfaced at its wake's creation", surfaced, err)
	}

	task := plantRunning(t, l.records(t), "linkedlaterb", "").ID
	if err := l.records(t).SetTaskRun(t.Context(), task, "run-a-persons-push"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		r := l.pass(t)
		if d := detectionsOf(r, task, supervisor.KindKeptWork); len(d) != 0 {
			t.Fatalf("the task linked to the run raised the entry again: %+v", d)
		}
		if len(r.KeptWork) != 0 {
			t.Fatalf("a pass raised kept work no task names again once a task named it: %+v", r.KeptWork)
		}
	}
	if w := wakesOfKind(t, l.records(t), task, supervisor.KindKeptWork); len(w) != 0 {
		t.Fatalf("the linked task has kept-work wakes %+v, want none", w)
	}
}

// The other direction: an entry raised on a task's wake, about a run
// attributed to the task by its branch, is not raised on the queue's third
// shape once the task's relaunch moves the attribution cutoff past the run
// and no task names it any more. Without the third shape's surfaced check
// the pass raises it a second time.
func TestAKeptWorkEntryRaisedOnATaskIsNotRaisedAgainOnceNoTaskNamesIt(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	h, records := loopHome(t)
	ctx := t.Context()
	if _, err := records.UpsertRepository(ctx, store.Repository{
		ID: "subject", WorkingPath: t.TempDir(), UpstreamURL: "/nowhere/upstream.git", DefaultBranch: "main",
	}); err != nil {
		t.Fatal(err)
	}
	const task = "attributedab"
	if _, err := records.CreateTask(ctx, store.Task{
		ID: task, Shape: store.TaskDelivery, Project: "one", Mode: "headless", WorktreePath: "/w/" + task,
		RepositoryID: store.Known("subject"), Branch: store.Known("task/one"),
	}, store.Disposition{}); err != nil {
		t.Fatal(err)
	}
	if _, err := records.AppendTaskEvent(ctx, task, "started", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := records.BeginTaskAttempt(ctx, task, 0); err != nil {
		t.Fatal(err)
	}
	entry := plantKeptRun(t, records, "run-of-the-task", "task/one")
	l := openLoop(t, h, records, newScriptedTerminals(), &fakeClock{now: time.Now()})
	defer func() { _ = l.service.Close() }()

	r := l.nextPass(t)
	if d := detectionsOf(r, task, supervisor.KindKeptWork); len(d) != 1 || d[0].Marker != keptWorkMarker(entry.ID) || d[0].Disposition != store.WakeAppended {
		t.Fatalf("the pass raised %+v on task %s, want entry %d about its attributed run once", d, task, entry.ID)
	}
	if len(r.KeptWork) != 0 {
		t.Fatalf("the entry on a task was raised on the third shape too: %+v", r.KeptWork)
	}
	// A relaunch's attempt starts after the run, so the run is no longer
	// the task's and no task names it.
	if _, err := l.records(t).BeginTaskAttempt(ctx, task, 1); err != nil {
		t.Fatal(err)
	}
	snap, err := l.records(t).TaskSnapshot(ctx, task)
	if err != nil || len(snap.Runs) != 0 {
		t.Fatalf("after the relaunch the task is attributed runs %+v, %v; want none, so no task names the entry", snap.Runs, err)
	}
	for range 2 {
		if again := l.pass(t); len(again.KeptWork) != 0 || len(detectionsOf(again, task, supervisor.KindKeptWork)) != 0 {
			t.Fatalf("once no task named it the entry was raised again: %+v", again.KeptWork)
		}
	}
	if rows, err := l.records(t).KeptWorkWakes(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("the kept-work wakes are %+v, %v; want none, since the entry already surfaced on its task", rows, err)
	}
}
