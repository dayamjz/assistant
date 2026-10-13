package taskstate

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// merged and closed build the merge records the pure tests hand Resolve, in
// the shape store.TransitionRunOnPullRequest writes: a run, a pull request
// number, an end.
func merged(run string, pr int) store.MergeRecord {
	return store.MergeRecord{Run: run, PullRequest: pr, Head: "aaaa", End: store.PullRequestMerged, RecordedAt: epoch}
}

func closed(run string, pr int) store.MergeRecord {
	return store.MergeRecord{Run: run, PullRequest: pr, Head: "aaaa", End: store.PullRequestClosed, RecordedAt: epoch}
}

func cleanup(removed bool) store.Optional[store.TaskCleanup] {
	c := store.TaskCleanup{TaskID: "t", Proof: store.CleanupProofMergedPullRequest, Examined: []string{"aaaa"}, RecordedAt: epoch}
	if removed {
		c.RemovedAt = store.Known(epoch.Add(time.Hour))
	}
	return store.Known(c)
}

// The terminal tier's precedence, over Inputs. A merge record reading merged
// on an attributed run is Landed from the record, above a later run attributed
// to the task whatever its status; Complete, a delivery task landed and
// removed, outranks Landed; and nothing else reaches the tier: a record
// reading closed, a record naming a run outside Runs, a removal on a task
// that has not landed, and an investigation task's removal all leave the
// answer to the run tier or stay Landed.
func TestTheRecordTierOutranksTheRun(t *testing.T) {
	delivery := store.Task{ID: "t", Shape: store.TaskDelivery}
	landedThenFailed := []store.Run{runIn("r1", store.RunPassed, 1), runIn("r2", store.RunFailed, 2)}

	got := Resolve(Inputs{Task: delivery, Runs: landedThenFailed, MergeRecords: []store.MergeRecord{merged("r1", 7)}})
	if got.State != Landed || got.Source != SourceRecord || got.Basis.LandedRun != "r1" || got.Basis.RunID != "r2" {
		t.Fatalf("a landed task with a later failed run resolved %s from %s (landed by %q, newest run %q), want landed from the record",
			got.State, got.Source, got.Basis.LandedRun, got.Basis.RunID)
	}
	if got.Progress {
		t.Fatal("a terminal record reads as progress")
	}
	// Control: the same runs with no record are the run tier's, and the run
	// tier reads the newest, so the record is what decided above.
	if got := Resolve(Inputs{Task: delivery, Runs: landedThenFailed}); got.State != Failed || got.Source != SourceRun {
		t.Fatalf("without the record the runs resolved %s from %s, want failed from the run", got.State, got.Source)
	}

	got = Resolve(Inputs{Task: delivery, Runs: landedThenFailed, MergeRecords: []store.MergeRecord{merged("r1", 7)}, Cleanup: cleanup(true)})
	if got.State != Complete || got.Source != SourceRecord || !got.Basis.CleanupRecorded || !got.Basis.CleanupRemoved {
		t.Fatalf("landed and removed resolved %s from %s (basis %+v), want complete from the record", got.State, got.Source, got.Basis)
	}
	got = Resolve(Inputs{Task: delivery, Runs: landedThenFailed, MergeRecords: []store.MergeRecord{merged("r1", 7)}, Cleanup: cleanup(false)})
	if got.State != Landed || !got.Basis.CleanupRecorded || got.Basis.CleanupRemoved {
		t.Fatalf("landed with a proof and no removal resolved %s (basis %+v), want landed", got.State, got.Basis)
	}

	// The newest attributed run with a merged record is the one named.
	twice := []store.MergeRecord{merged("r1", 7), merged("r2", 8)}
	both := []store.Run{runIn("r1", store.RunPassed, 1), runIn("r2", store.RunPassed, 2)}
	if got := Resolve(Inputs{Task: delivery, Runs: both, MergeRecords: twice}); got.Basis.LandedRun != "r2" {
		t.Fatalf("two merged runs named %q as the landing run, want the newest, r2", got.Basis.LandedRun)
	}

	for name, in := range map[string]Inputs{
		"a closed pull request":                    {Task: delivery, Runs: []store.Run{runIn("r1", store.RunFailed, 1)}, MergeRecords: []store.MergeRecord{closed("r1", 7)}},
		"a record naming a run outside the task's": {Task: delivery, Runs: []store.Run{runIn("r1", store.RunPassed, 1)}, MergeRecords: []store.MergeRecord{merged("other", 7)}},
		"a removal without landing":                {Task: delivery, Runs: []store.Run{runIn("r1", store.RunPassed, 1)}, Cleanup: cleanup(true)},
	} {
		got := Resolve(in)
		if got.Source != SourceRun || got.Basis.LandedRun != "" {
			t.Errorf("%s resolved %s from %s (landed by %q), want the run tier's answer", name, got.State, got.Source, got.Basis.LandedRun)
		}
	}

	// An investigation task that names a merged run is Landed and no more:
	// Complete on its edge needs facts no record holds in this build.
	investigation := store.Task{ID: "t", Shape: store.TaskInvestigation}
	got = Resolve(Inputs{Task: investigation, Runs: []store.Run{runIn("r1", store.RunPassed, 1)}, MergeRecords: []store.MergeRecord{merged("r1", 7)}, Cleanup: cleanup(true)})
	if got.State != Landed {
		t.Fatalf("an investigation task landed and removed resolved %s, want landed: nothing records its report or its holds", got.State)
	}
}

// landRun records a merge of the run's pull request through the one accessor
// that writes a merge record, moving the run from running to passed the way
// internal/runs' PassOnMerge does.
func landRun(t *testing.T, s *store.Store, id string, pr int) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.CreateRun(ctx, store.Run{
		ID: id, RepositoryID: repo, Branch: branch, SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "validate", IntentSource: "push", ConfigDigest: "cfg",
		Build: store.Build{Version: "v0", Revision: "0123456789abcdef", Go: "go1.25.0"},
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if _, err := s.TransitionRun(ctx, id, []store.RunStatus{store.RunPending}, store.RunRunning); err != nil {
		t.Fatalf("TransitionRun running: %v", err)
	}
	if _, err := s.TransitionRunOnPullRequest(ctx, id, []store.RunStatus{store.RunRunning}, store.RunPassed,
		store.PullRequestRead{PullRequest: pr, Head: "aaaa", End: store.PullRequestMerged}, store.HoldClosure{}); err != nil {
		t.Fatalf("TransitionRunOnPullRequest: %v", err)
	}
}

// Landed through the store. A merge record written by the store's one
// accessor for a run attributed to the task resolves Landed from the record,
// naming the run; a record on a run attributed to no task lands nothing; a
// record on a run older than a later task's attempt does not land the later
// task; and a run the task names by id afterwards, failed, does not displace
// the record (PRD section 6: a terminal record is final).
func TestAMergeRecordLandsTheTaskItsRunIsAttributedTo(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	task := newTask(t, s, "taaaaaaaaaab", true)
	appendEvents(t, s, task.ID, "started", "handed-off")

	landRun(t, s, "run-1", 7)
	got := resolveTask(t, s, task.ID)
	if got.State != Landed || got.Source != SourceRecord || got.Basis.LandedRun != "run-1" || got.Basis.RunID != "run-1" {
		t.Fatalf("resolved %s from %s (landed by %q, run %q), want landed from the record by run-1", got.State, got.Source, got.Basis.LandedRun, got.Basis.RunID)
	}
	// The newest event, handed-off, claims Validating and is superseded by
	// the record; the detail names the record and never the event's text.
	if len(got.Superseded) != 1 || got.Superseded[0].Kind != "handed-off" || got.Superseded[0].By != SourceRecord {
		t.Fatalf("superseded = %+v, want the handed-off event by the record", got.Superseded)
	}
	if detail := got.Detail.Or(""); detail == "" || detail == secret {
		t.Fatalf("detail %q", detail)
	}

	// A later run the task names by id, failed, is the newest run and still
	// does not displace the record.
	time.Sleep(2 * time.Millisecond)
	if _, err := s.CreateRun(ctx, store.Run{
		ID: "run-2", RepositoryID: repo, Branch: "elsewhere", SubmittedHead: "cccc", Base: "bbbb",
		Intent: "validate", IntentSource: "push", ConfigDigest: "cfg",
		Build: store.Build{Version: "v0", Revision: "0123456789abcdef", Go: "go1.25.0"},
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	for _, m := range []struct{ from, to store.RunStatus }{{store.RunPending, store.RunRunning}, {store.RunRunning, store.RunFailed}} {
		if _, err := s.TransitionRun(ctx, "run-2", []store.RunStatus{m.from}, m.to); err != nil {
			t.Fatalf("TransitionRun %s: %v", m.to, err)
		}
	}
	if err := s.SetTaskRun(ctx, task.ID, "run-2"); err != nil {
		t.Fatalf("SetTaskRun: %v", err)
	}
	got = resolveTask(t, s, task.ID)
	if got.State != Landed || got.Source != SourceRecord || got.Basis.LandedRun != "run-1" || got.Basis.RunID != "run-2" {
		t.Fatalf("after a later failed run named by id, resolved %s from %s (landed by %q, newest run %q), want landed by run-1 with run-2 newest",
			got.State, got.Source, got.Basis.LandedRun, got.Basis.RunID)
	}

	// A task with no attributed run is landed by nothing: run-1's record is
	// run-1's, and this task has no attempt, so it is attributed no run by
	// its branch.
	other := store.Task{ID: "otheraaaaaaa", Shape: store.TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: "/w/other",
		RepositoryID: store.Known(repo), Branch: store.Known(branch)}
	if _, err := s.CreateTask(ctx, other, store.Disposition{Blocker: store.Known(task.ID), Observed: task.Row}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if got := resolveTask(t, s, other.ID); got.State != Queued || got.Basis.LandedRun != "" {
		t.Fatalf("a task attributed no run resolved %s (landed by %q), want queued from its intake", got.State, got.Basis.LandedRun)
	}
	// Once its attempt begins, after the merge was recorded, the merged run
	// is older than the attempt and still not its.
	beginAttempt(t, s, other.ID, 0)
	if got := resolveTask(t, s, other.ID); got.State == Landed || got.Basis.LandedRun != "" {
		t.Fatalf("a later task on the branch resolved %s (landed by %q) from a merge before its attempt", got.State, got.Basis.LandedRun)
	}
}

// Both directions over the store's mechanism, as the consumer sees them: a
// run moved to passed without a pull request read has no merge record and
// resolves Ready; a transition the store refuses writes no record, so no
// record exists without its run's move.
func TestNoRecordWithoutTheMoveAndNoLandingWithoutTheRecord(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	task := newTask(t, s, "taaaaaaaaaab", true)

	finishRun(t, s, "run-1")
	if _, ended, err := s.MergeRecord(ctx, "run-1"); err != nil || ended {
		t.Fatalf("a run passed by its stages has a merge record (%v, %v)", ended, err)
	}
	if got := resolveTask(t, s, task.ID); got.State != Ready || got.Source != SourceRun || got.Basis.LandedRun != "" {
		t.Fatalf("a passed run with no record resolved %s from %s, want ready from the run", got.State, got.Source)
	}

	// Refused: run-1 is passed, and the move to failed is asked from running.
	// (A move to the status the run is already in is the store's documented
	// no-op, so the refusal is asked of a move to another one.)
	_, err := s.TransitionRunOnPullRequest(ctx, "run-1", []store.RunStatus{store.RunRunning}, store.RunFailed,
		store.PullRequestRead{PullRequest: 7, Head: "aaaa", End: store.PullRequestClosed}, store.HoldClosure{})
	var wrong *store.RunStatusError
	if !errors.As(err, &wrong) {
		t.Fatalf("a move from the wrong status returned %v, want a status refusal", err)
	}
	if _, ended, err := s.MergeRecord(ctx, "run-1"); err != nil || ended {
		t.Fatalf("a refused move left a merge record (%v, %v)", ended, err)
	}
	if got := resolveTask(t, s, task.ID); got.State != Ready {
		t.Fatalf("after a refused move the task resolved %s, want ready still", got.State)
	}
}

// P8: a worker appending an event named landed or complete changes nothing.
// Neither word is an event kind, so the log tier answers Unknown for a task
// with nothing above it, and a task the run tier answers for is unmoved.
// The control is the record: written through the store, it is what lands the
// task, so the test tells the event from the fact it imitates.
func TestAWorkerEventNamedLandedChangesNothing(t *testing.T) {
	principles.Cite(t, principles.P8)
	s := openStore(t)
	for _, word := range []string{"landed", "complete"} {
		if _, ok := Lookup(word); ok {
			t.Fatalf("%q is an event kind", word)
		}
	}

	task := newTask(t, s, "taaaaaaaaaab", true)
	finishRun(t, s, "run-1")
	before := resolveTask(t, s, task.ID)
	appendEvents(t, s, task.ID, "landed", "complete")
	after := resolveTask(t, s, task.ID)
	if after.State != before.State || after.Source != before.Source || after.State != Ready {
		t.Fatalf("after the events the task resolved %s from %s, was %s from %s", after.State, after.Source, before.State, before.Source)
	}
	if after.Basis.LandedRun != "" || len(after.Superseded) != 0 {
		t.Fatalf("the events were read as a claim: basis %+v, superseded %+v", after.Basis, after.Superseded)
	}

	// With no run to outrank it the log answers, and the answer is still not
	// Landed: the word has no meaning.
	alone := newTask(t, s, "aloneaaaaaaa", false)
	appendEvents(t, s, alone.ID, "landed")
	if got := resolveTask(t, s, alone.ID); got.State != Unknown || got.Source != SourceNone {
		t.Fatalf("a task whose newest event is named landed resolved %s from %s, want unknown from nothing", got.State, got.Source)
	}

	// Control: the record lands it.
	landRun(t, s, "run-2", 7)
	if got := resolveTask(t, s, task.ID); got.State != Landed || got.Source != SourceRecord {
		t.Fatalf("the merge record resolved %s from %s, so the fixture cannot tell an event from the record", got.State, got.Source)
	}
}

// Complete through the store. A delivery task's cleanup record, proof then
// removal, on a Landed task resolves Complete and on a Ready task leaves it
// Ready. An investigation task with a report-ready entry, a registered hold
// and a removal stays Reported: PRD section 5's edge asks for a stored report
// and registered holds, and no record in this build holds the first, so the
// state is unreachable rather than approximated. When such a record exists
// this is the test that moves to Complete.
func TestCompleteIsLandedAndRemovedForADeliveryTask(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)

	ready := newTask(t, s, "readyaaaaaaa", true)
	finishRun(t, s, "run-1")
	recordCleanup := func(id string, shape store.TaskShape) {
		t.Helper()
		if _, err := s.RecordTaskCleanup(ctx, store.TaskCleanup{TaskID: id, Proof: store.CleanupProofRemoteBranch, Examined: []string{"aaaa"}}, shape); err != nil {
			t.Fatalf("RecordTaskCleanup: %v", err)
		}
		if err := s.MarkCleanupRemoved(ctx, id, time.Now()); err != nil {
			t.Fatalf("MarkCleanupRemoved: %v", err)
		}
	}
	recordCleanup(ready.ID, store.TaskDelivery)
	if got := resolveTask(t, s, ready.ID); got.State != Ready || got.Source != SourceRun || !got.Basis.CleanupRemoved {
		t.Fatalf("a removed copy on a ready task resolved %s from %s (basis %+v), want ready", got.State, got.Source, got.Basis)
	}

	landed := store.Task{ID: "landedaaaaaa", Shape: store.TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: "/w/landed",
		RepositoryID: store.Known(repo), Branch: store.Known("other")}
	if _, err := s.CreateTask(ctx, landed, store.Disposition{}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	beginAttempt(t, s, landed.ID, 0)
	if _, err := s.CreateRun(ctx, store.Run{
		ID: "run-2", RepositoryID: repo, Branch: "other", SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "validate", IntentSource: "push", ConfigDigest: "cfg",
		Build: store.Build{Version: "v0", Revision: "0123456789abcdef", Go: "go1.25.0"},
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if _, err := s.TransitionRun(ctx, "run-2", []store.RunStatus{store.RunPending}, store.RunRunning); err != nil {
		t.Fatalf("TransitionRun: %v", err)
	}
	if _, err := s.TransitionRunOnPullRequest(ctx, "run-2", []store.RunStatus{store.RunRunning}, store.RunPassed,
		store.PullRequestRead{PullRequest: 9, Head: "aaaa", End: store.PullRequestMerged}, store.HoldClosure{}); err != nil {
		t.Fatalf("TransitionRunOnPullRequest: %v", err)
	}
	if got := resolveTask(t, s, landed.ID); got.State != Landed {
		t.Fatalf("before cleanup the task resolved %s, want landed", got.State)
	}
	if _, err := s.RecordTaskCleanup(ctx, store.TaskCleanup{TaskID: landed.ID, Proof: store.CleanupProofMergedPullRequest, Examined: []string{"aaaa"}}, store.TaskDelivery); err != nil {
		t.Fatalf("RecordTaskCleanup: %v", err)
	}
	if got := resolveTask(t, s, landed.ID); got.State != Landed || !got.Basis.CleanupRecorded || got.Basis.CleanupRemoved {
		t.Fatalf("with the proof and no removal the task resolved %s (basis %+v), want landed", got.State, got.Basis)
	}
	if err := s.MarkCleanupRemoved(ctx, landed.ID, time.Now()); err != nil {
		t.Fatalf("MarkCleanupRemoved: %v", err)
	}
	if got := resolveTask(t, s, landed.ID); got.State != Complete || got.Source != SourceRecord || got.Basis.LandedRun != "run-2" {
		t.Fatalf("landed and removed resolved %s from %s (basis %+v), want complete from the record", got.State, got.Source, got.Basis)
	}

	// newTask records a delivery task; this one is recorded as an
	// investigation directly.
	inquiry := store.Task{ID: "lookaaaaaaaa", Shape: store.TaskInvestigation, Project: "one", Mode: "interactive", WorktreePath: "/w/look"}
	if _, err := s.CreateTask(ctx, inquiry, store.Disposition{}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	appendEvents(t, s, inquiry.ID, "started", "report-ready")
	if _, err := s.RegisterHold(ctx, store.Hold{Key: "task/" + inquiry.ID + "/ask-1", TaskID: store.Known(inquiry.ID), Subject: "decide"}); err != nil {
		t.Fatalf("RegisterHold: %v", err)
	}
	if got := resolveTask(t, s, inquiry.ID); got.State != Reported || got.Source != SourceEvent {
		t.Fatalf("an investigation with a report entry and a hold resolved %s from %s, want reported", got.State, got.Source)
	}
	recordCleanup(inquiry.ID, store.TaskInvestigation)
	if got := resolveTask(t, s, inquiry.ID); got.State != Reported || got.Source != SourceEvent || !got.Basis.CleanupRemoved {
		t.Fatalf("an investigation removed resolved %s from %s (basis %+v); Complete needs a stored report, which no record holds",
			got.State, got.Source, got.Basis)
	}
}
