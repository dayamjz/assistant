package service

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/tasks"
)

// PRD principle P15's "kept work is never silent", at the task-copy site,
// through the real service and its supervisor loop: a reported
// investigation whose worker has ended is owed its copy's cleanup, which the
// loop's pass asks, and a copy holding a commit no proof can place anywhere
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
	if _, err := reporter.StoreReport(ctx, id, `{"summary":"looked and committed what it found"}`); err != nil {
		t.Fatalf("storing the report: %v", err)
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
