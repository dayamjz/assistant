package service

import (
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// A task in internal/taskstate's terminal tier, Landed or Complete, has its
// terminal left unread by the loop (the owner's decision on #320), so a
// worker that ends after its task landed raises no terminal-gone wake even
// while a queued wake nobody acknowledges keeps the task watched, which is
// the #268 noise a coordinator that drains and never acknowledges brought
// back. Each finished task carries an open hold, so the first pass queues a
// hold-opened wake for it and every later pass watches it: what keeps the
// terminal unread is the terminal table and not the task falling out of the
// watched set. The control is a Running task whose terminal probes Dead
// through the same Terminals double, and it surfaces terminal gone on the
// first pass. The landed task's Dead terminal still reaches landed.go: the
// resolution's own probe is what its pass reads, and the cleanup decision
// is asked under that reference.
func TestALandedOrCompleteTaskWithADeadTerminalRaisesNoTerminalGone(t *testing.T) {
	t.Parallel()
	h, records := loopHome(t)
	control := plantRunning(t, records, "runningdeadx", "pane-running").ID
	landed := plantFinished(t, records, "landeddeadxx", "pane-landed", taskstate.Landed)
	complete := plantFinished(t, records, "completedead", "pane-complete", taskstate.Complete)
	terminals := newScriptedTerminals()
	for _, ref := range []string{"pane-running", "pane-landed", "pane-complete"} {
		terminals.dead(ref)
	}
	l := openLoop(t, h, records, terminals, nil)
	defer func() { _ = l.service.Close() }()

	passes := []passReport{l.nextPass(t), l.pass(t), l.pass(t)}
	if got := detectionsOf(passes[0], control, supervisor.KindTerminalGone); len(got) != 1 ||
		!strings.HasPrefix(got[0].Marker, "terminal-gone:dead:pane-running:") || got[0].Disposition != store.WakeAppended {
		t.Fatalf("the running control's terminal-gone detections = %+v, want one appended as dead", got)
	}
	for _, task := range []string{landed, complete} {
		if got := detectionsOf(passes[0], task, supervisor.KindHoldOpened); len(got) != 1 || got[0].Disposition != store.WakeAppended {
			t.Fatalf("task %s's hold-opened detections = %+v, want one appended", task, got)
		}
		for i, r := range passes {
			if i > 0 && !r.Tasks[task].Watched {
				t.Fatalf("pass %d did not watch task %s, whose hold-opened wake is queued", i+1, task)
			}
			if got := detectionsOf(r, task, supervisor.KindTerminalGone); len(got) != 0 {
				t.Fatalf("pass %d detected terminal gone for task %s: %+v", i+1, task, got)
			}
			if got := detectionsOf(r, task, supervisor.KindTerminalUnchanged); len(got) != 0 {
				t.Fatalf("pass %d detected terminal unchanged for task %s: %+v", i+1, task, got)
			}
		}
		if rows := wakesOfKind(t, l.records(t), task, supervisor.KindTerminalGone); len(rows) != 0 {
			t.Fatalf("task %s has terminal-gone wakes %+v, want none", task, rows)
		}
		if rows := wakesOfKind(t, l.records(t), task, supervisor.KindHoldOpened); len(rows) != 1 || !rows[0].Queued() {
			t.Fatalf("task %s's hold-opened wakes = %+v, want one still queued", task, rows)
		}
	}
	deadline := time.Now().Add(loopWait)
	for landedAskedUnder(l.service, landed, "pane-landed") == 0 {
		if !time.Now().Before(deadline) {
			t.Fatal("landed.go never asked the cleanup decision under the landed task's Dead terminal")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// landedAskedUnder is how many times landed.go counted an ask of the cleanup
// decision for the task under ref, read from the memory it keeps.
func landedAskedUnder(s *Service, id, ref string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if tried, ok := s.landedTried[id]; ok && tried.ref == ref {
		return tried.asks
	}
	return 0
}

// plantFinished plants a delivery task that resolves want, Landed or
// Complete, from internal/taskstate's terminal tier: an attributed run whose
// pull request was read as merged and, for Complete, a cleanup record with
// the copy removed. The task also has an attempt naming ref and an open hold
// naming it, which resolves below the terminal tier.
func plantFinished(t *testing.T, records *store.Store, id, ref string, want taskstate.State) string {
	t.Helper()
	ctx := t.Context()
	repository, branch := "repo-"+id, "branch-"+id
	if _, err := records.UpsertRepository(ctx, store.Repository{
		ID: repository, WorkingPath: "/checkouts/" + id, UpstreamURL: "https://example.test/" + id + ".git", DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("recording the repository: %v", err)
	}
	build, _ := store.CurrentBuild()
	run, err := records.CreateRun(ctx, store.Run{
		ID: "run-" + id, RepositoryID: repository, Branch: branch, SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "validate", IntentSource: "push", Build: build, ConfigDigest: "cfg",
	})
	if err != nil {
		t.Fatalf("creating the run: %v", err)
	}
	if _, err := records.TransitionRun(ctx, run.ID, []store.RunStatus{store.RunPending}, store.RunRunning); err != nil {
		t.Fatalf("moving the run to running: %v", err)
	}
	if _, err := records.TransitionRunOnPullRequest(ctx, run.ID, []store.RunStatus{store.RunRunning}, store.RunPassed,
		store.PullRequestRead{PullRequest: 1, Head: "aaaa", End: store.PullRequestMerged}, store.HoldClosure{}); err != nil {
		t.Fatalf("recording the merge: %v", err)
	}
	if _, err := records.CreateTask(ctx, store.Task{
		ID: id, Shape: store.TaskDelivery, Project: "one", Mode: "headless", WorktreePath: "/w/" + id,
		RepositoryID: store.Known(repository), Branch: store.Known(branch),
	}, store.Disposition{}); err != nil {
		t.Fatalf("creating the task: %v", err)
	}
	if err := records.SetTaskRun(ctx, id, run.ID); err != nil {
		t.Fatalf("attributing the run: %v", err)
	}
	if _, err := records.BeginFirstTaskAttempt(ctx, id, store.OnThisMachine()); err != nil {
		t.Fatalf("beginning the attempt of %s: %v", id, err)
	}
	if err := records.SetAttemptSession(ctx, id, 1, ref, store.SessionHost{}); err != nil {
		t.Fatalf("recording the session of %s: %v", id, err)
	}
	if want == taskstate.Complete {
		if _, err := records.RecordTaskCleanup(ctx, store.TaskCleanup{TaskID: id, Proof: store.CleanupProofRemoteBranch}, store.TaskDelivery); err != nil {
			t.Fatalf("recording the cleanup: %v", err)
		}
		if err := records.MarkCleanupRemoved(ctx, id, time.Now()); err != nil {
			t.Fatalf("marking the copy removed: %v", err)
		}
	}
	if _, err := records.RegisterHold(ctx, store.Hold{Key: "hold-" + id, TaskID: store.Known(id), Subject: "a decision", Detail: "which"}); err != nil {
		t.Fatalf("registering the hold: %v", err)
	}
	resolved, err := taskstate.NewReader(records, nil, nil).Task(ctx, id)
	if err != nil || resolved.State != want || resolved.Source != taskstate.SourceRecord {
		t.Fatalf("the planted task resolves %s from %s (%v), want %s from the terminal tier", resolved.State, resolved.Source, err, want)
	}
	return id
}
