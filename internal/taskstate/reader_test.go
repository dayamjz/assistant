package taskstate

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
)

const (
	repo   = "repo-1"
	branch = "topic"
	secret = "SENTINEL-the-run-is-holding-on-a-decision"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.UpsertRepository(context.Background(), store.Repository{
		ID: repo, WorkingPath: "/checkouts/one", UpstreamURL: "https://example.test/one.git", DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("UpsertRepository: %v", err)
	}
	return s
}

// newTask creates an independent task and, when it works on a branch, begins
// its first attempt, because a task with no attempt is attributed no run by
// its branch. The attempt is what every test here that finishes a run on the
// branch rests on.
func newTask(t *testing.T, s *store.Store, id string, withBranch bool) store.Task {
	t.Helper()
	task := store.Task{ID: id, Shape: store.TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: "/w/" + id}
	if withBranch {
		task.RepositoryID, task.Branch = store.Known(repo), store.Known(branch)
	}
	task, err := s.CreateTask(context.Background(), task, store.Disposition{})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if withBranch {
		beginAttempt(t, s, id, 0)
	}
	return task
}

// beginAttempt records the next attempt of a task, anchored to expected.
func beginAttempt(t *testing.T, s *store.Store, id string, expected int64) store.TaskAttempt {
	t.Helper()
	a, err := s.BeginTaskAttempt(context.Background(), id, expected)
	if err != nil {
		t.Fatalf("BeginTaskAttempt: %v", err)
	}
	return a
}

func appendEvents(t *testing.T, s *store.Store, id string, kinds ...string) {
	t.Helper()
	for _, k := range kinds {
		if _, err := s.AppendTaskEvent(context.Background(), id, k, secret); err != nil {
			t.Fatalf("AppendTaskEvent %s: %v", k, err)
		}
	}
}

// finishRun records a run of the task's branch and moves it through a hold to
// passed, the way a validation run does.
func finishRun(t *testing.T, s *store.Store, id string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.CreateRun(ctx, store.Run{
		ID: id, RepositoryID: repo, Branch: branch, SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "validate", IntentSource: "push", ConfigDigest: "cfg",
		Build: store.Build{Version: "v0", Revision: "0123456789abcdef", Go: "go1.25.0"},
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	for _, m := range []struct{ from, to store.RunStatus }{
		{store.RunPending, store.RunRunning}, {store.RunRunning, store.RunHeld},
		{store.RunHeld, store.RunRunning}, {store.RunRunning, store.RunPassed},
	} {
		if _, err := s.TransitionRun(ctx, id, []store.RunStatus{m.from}, m.to); err != nil {
			t.Fatalf("TransitionRun %s: %v", m.to, err)
		}
	}
}

func resolveTask(t *testing.T, s *store.Store, id string) Resolution {
	t.Helper()
	got, err := NewReader(s, nil).Task(context.Background(), id)
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	return got
}

func seqs(r Resolution) []int64 {
	var out []int64
	for _, s := range r.Superseded {
		out = append(out, s.Sequence)
	}
	return out
}

// tailOnly is the reader P8 forbids: it maps the newest event and nothing
// else. It is test-local, and it exists so the fixture below can be shown to
// tell it from Resolve.
func tailOnly(events []store.TaskEvent) State {
	row, ok := Lookup(events[len(events)-1].Kind)
	if !ok {
		return Unknown
	}
	return row.State
}

// P8: an event log is not current state. The worker's log ends on an open
// decision and the run attributed to its branch has passed, all through the
// store; the resolver answers from the run, marks the decision superseded, and
// does not let the decision's text into the answer.
func TestAnEventLogIsNotCurrentState(t *testing.T) {
	principles.Cite(t, principles.P8)
	s := openStore(t)
	task := newTask(t, s, "t1", true)
	appendEvents(t, s, task.ID, "started", "handed-off", "decision-open")

	// Positive control C1: with nothing outranking it the log is honored, and
	// the same log then supersedes nothing. It kills "ignore the events".
	before := resolveTask(t, s, task.ID)
	if before.State != Held || before.Source != SourceEvent || len(before.Superseded) != 0 {
		t.Fatalf("with no run the log resolved to %s from %s superseding %v, want held from the event",
			before.State, before.Source, before.Superseded)
	}

	finishRun(t, s, "run-1")
	got := resolveTask(t, s, task.ID)
	if got.State != Ready || got.Source != SourceRun {
		t.Fatalf("resolved %s from %s, want ready from the run", got.State, got.Source)
	}
	// Opening event is 1, dispatched 2: started 3, handed-off 4, decision-open 5.
	if want := []int64{5}; !slices.Equal(seqs(got), want) {
		t.Fatalf("superseded = %v, want %v", seqs(got), want)
	}
	if s0 := got.Superseded[0]; s0.Kind != "decision-open" || s0.Claimed != Held || s0.By != SourceRun {
		t.Fatalf("superseded entry = %+v", s0)
	}
	if strings.Contains(got.Detail.Or(""), secret) {
		t.Fatalf("detail %q carries the decision event's text", got.Detail.Or(""))
	}

	// Control C2: the fixture tells a tail reader from the resolver. If the run
	// ever stopped outranking the log this would fail instead of the test
	// above passing for the wrong reason.
	snap, err := s.TaskSnapshot(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("TaskSnapshot: %v", err)
	}
	if tail := tailOnly(snap.Events); tail == got.State {
		t.Fatalf("a tail-only reader agrees with the resolver (%s), so this fixture shows nothing", tail)
	}
	// Control C3: dropping the run tier is what changes the answer.
	dropped := Resolve(Inputs{Task: snap.Task, Events: snap.Events})
	if dropped.State != Held || dropped.Source != SourceEvent {
		t.Fatalf("without the run the snapshot resolved to %s from %s", dropped.State, dropped.Source)
	}
}

// The whole log is read: an earlier decision that was closed is not superseded,
// the later one that was not is, and the newest event is a note. A reader of
// the tail finds nothing, and one that scans for any open decision finds both.
func TestSupersededComesFromTheWholeLogThroughTheStore(t *testing.T) {
	principles.Cite(t, principles.P8)
	s := openStore(t)
	task := newTask(t, s, "t1", true)
	appendEvents(t, s, task.ID, "decision-open", "decision-closed", "decision-open", "progress-note")
	finishRun(t, s, "run-1")

	got := resolveTask(t, s, task.ID)
	// 1 opening, 2 dispatched, 3 open, 4 closed, 5 open, 6 note.
	if want := []int64{5}; !slices.Equal(seqs(got), want) {
		t.Fatalf("superseded = %v, want only the decision still open, %v", seqs(got), want)
	}
}

// Deterministic means the answer follows from the records and not from when
// they were written: the same facts written in either order resolve alike.
func TestTheAnswerDoesNotDependOnWriteOrder(t *testing.T) {
	principles.Cite(t, principles.P8)
	a, b := openStore(t), openStore(t)
	newTask(t, a, "t1", true)
	appendEvents(t, a, "t1", "started", "decision-open")
	finishRun(t, a, "run-1")

	newTask(t, b, "t1", true)
	finishRun(t, b, "run-1")
	appendEvents(t, b, "t1", "started", "decision-open")

	ra, rb := resolveTask(t, a, "t1"), resolveTask(t, b, "t1")
	ra.Basis.RunUpdatedAt, rb.Basis.RunUpdatedAt = time.Time{}, time.Time{} // the runs were moved at different instants
	if !reflect.DeepEqual(ra, rb) {
		t.Fatalf("write order changed the answer:\n%+v\n%+v", ra, rb)
	}
	if ra.State != Ready {
		t.Fatalf("both resolved to %s, so agreement shows nothing", ra.State)
	}
}

// A task nobody has touched resolves from the events its intake appended: an
// independent task from its dispatch, a task queued behind another from its
// opening event. A task with no branch is not attributed another task's run,
// and neither is a task queued on the same branch as a live one: the blocker's
// run is the blocker's, and the queued task resolves Queued rather than
// inheriting the blocker's validation.
func TestAttributionAndTheOpeningEvents(t *testing.T) {
	s := openStore(t)
	newTask(t, s, "delivery", true)
	newTask(t, s, "investigation", false)
	queued := store.Task{ID: "queued", Shape: store.TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: "/w/queued",
		RepositoryID: store.Known(repo), Branch: store.Known(branch)}
	delivery, err := s.Task(context.Background(), "delivery")
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if _, err := s.CreateTask(context.Background(), queued,
		store.Disposition{Blocker: store.Known("delivery"), Observed: delivery.Row}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	finishRun(t, s, "run-1")

	if got := resolveTask(t, s, "investigation"); got.State != Dispatched || got.Source != SourceEvent {
		t.Fatalf("an investigation task resolved to %s from %s, want dispatched from its intake", got.State, got.Source)
	}
	if got := resolveTask(t, s, "delivery"); got.State != Ready || got.Source != SourceRun {
		t.Fatalf("the delivery task resolved to %s from %s", got.State, got.Source)
	}
	if got := resolveTask(t, s, "queued"); got.State != Queued || got.Source != SourceEvent {
		t.Fatalf("the task queued behind it resolved to %s from %s, want queued from its opening event", got.State, got.Source)
	}
	// Once the queued task's own attempt begins, a run newer than that is its.
	beginAttempt(t, s, "queued", 0)
	time.Sleep(2 * time.Millisecond)
	finishRun(t, s, "run-2")
	if got := resolveTask(t, s, "queued"); got.State != Ready || got.Source != SourceRun || got.Basis.RunID != "run-2" {
		t.Fatalf("after its attempt began the queued task resolved to %s from %s (run %s), want ready from run-2",
			got.State, got.Source, got.Basis.RunID)
	}
}

func TestAnUnknownTaskIsNotFound(t *testing.T) {
	s := openStore(t)
	if _, err := NewReader(s, nil).Task(context.Background(), "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Task = %v, want store.ErrNotFound", err)
	}
	all, err := NewReader(s, nil).All(context.Background())
	if err != nil || len(all) != 0 {
		t.Fatalf("All on an empty store = %v, %v", all, err)
	}
}

// The probe is asked only about tasks whose latest attempt has a session, and
// what it says reaches the tier above the log and below the run. A task with
// an attempt and no session yet is not probed either: that is the record of a
// launch that has not come up.
func TestTheReaderConsultsTheProbeForTasksWithASession(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	newTask(t, s, "withsession", false)
	newTask(t, s, "nosession", false)
	newTask(t, s, "notyet", false)
	appendEvents(t, s, "withsession", "started")
	first := beginAttempt(t, s, "withsession", 0)
	if err := s.SetAttemptSession(ctx, "withsession", first.Sequence, "pane-1"); err != nil {
		t.Fatalf("SetAttemptSession: %v", err)
	}
	beginAttempt(t, s, "notyet", 0)

	var asked []string
	live := true
	probe := func(_ context.Context, ref string) Liveness {
		asked = append(asked, ref)
		if live {
			return Live
		}
		return Dead
	}
	r := NewReader(s, probe)
	got, err := r.Task(ctx, "withsession")
	if err != nil || got.State != Running || got.Source != SourceProcess {
		t.Fatalf("live terminal: %+v, %v", got, err)
	}
	live = false
	if got, _ := r.Task(ctx, "withsession"); got.State != Unknown || got.Source != SourceProcess {
		t.Fatalf("dead terminal with a stale started event resolved to %s from %s", got.State, got.Source)
	}
	if got, _ := r.Task(ctx, "nosession"); got.Basis.Process != Unobserved {
		t.Fatalf("a task with no session was probed: %+v", got.Basis)
	}
	if got, _ := r.Task(ctx, "notyet"); got.Basis.Process != Unobserved {
		t.Fatalf("a task whose attempt has no session yet was probed: %+v", got.Basis)
	}
	all, err := r.All(ctx)
	if err != nil || len(all) != 3 {
		t.Fatalf("All = %v, %v", all, err)
	}
	if want := []string{"pane-1", "pane-1", "pane-1"}; !slices.Equal(asked, want) {
		t.Fatalf("the probe was asked about %v, want %v", asked, want)
	}
}

// fingerprint reads everything the resolver could have touched through the
// store's own accessors.
func fingerprint(t *testing.T, s *store.Store) any {
	t.Helper()
	ctx := context.Background()
	tasks, err := s.Tasks(ctx)
	if err != nil {
		t.Fatalf("Tasks: %v", err)
	}
	var events [][]store.TaskEvent
	for _, task := range tasks {
		e, err := s.TaskEvents(ctx, task.ID, 0, 0)
		if err != nil {
			t.Fatalf("TaskEvents: %v", err)
		}
		events = append(events, e)
	}
	runs, err := s.RunsForRepository(ctx, repo)
	if err != nil {
		t.Fatalf("RunsForRepository: %v", err)
	}
	holds, err := s.OpenHolds(ctx)
	if err != nil {
		t.Fatalf("OpenHolds: %v", err)
	}
	return []any{tasks, events, runs, holds}
}

// "Read-only and free of side effects" (PRD section 8) as something a test can
// fail: resolving every task changes nothing the store can report. The control
// is that the fingerprint does move when a write is made, so equality is not
// the fingerprint failing to look. Writes by a path that bypasses the store's
// accessors are outside what this can see.
func TestResolvingChangesNothing(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	newTask(t, s, "t1", true)
	appendEvents(t, s, "t1", "started", "decision-open")
	finishRun(t, s, "run-1")
	if _, err := s.RegisterHold(ctx, store.Hold{Key: "h", Subject: "q", TaskID: store.Known("t1")}); err != nil {
		t.Fatalf("RegisterHold: %v", err)
	}

	before := fingerprint(t, s)
	r := NewReader(s, func(context.Context, string) Liveness { return Live })
	for range 3 {
		if _, err := r.All(ctx); err != nil {
			t.Fatalf("All: %v", err)
		}
		if _, err := r.Task(ctx, "t1"); err != nil {
			t.Fatalf("Task: %v", err)
		}
	}
	if !reflect.DeepEqual(before, fingerprint(t, s)) {
		t.Fatal("resolving changed what the store reports")
	}

	appendEvents(t, s, "t1", "progress-note")
	if reflect.DeepEqual(before, fingerprint(t, s)) {
		t.Fatal("the fingerprint did not move on a write, so the equality above showed nothing")
	}
}

// A listing and a single read agree, and each carries the record from the very
// snapshot its state was resolved from: the state's task is the record's task.
// The two tasks resolve differently, so pairing them wrongly would be seen.
func TestARecordAndItsStateComeFromOneRead(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	newTask(t, s, "a", true)
	newTask(t, s, "b", false)
	appendEvents(t, s, "a", "started")
	finishRun(t, s, "run-1")

	r := NewReader(s, nil)
	all, err := r.All(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("All = %v, %v", all, err)
	}
	for _, listed := range all {
		if listed.Record.ID != listed.State.TaskID {
			t.Fatalf("record %s paired with the state of %s", listed.Record.ID, listed.State.TaskID)
		}
		one, err := r.Resolved(ctx, listed.Record.ID)
		if err != nil || !reflect.DeepEqual(one, listed) {
			t.Fatalf("Resolved(%s) = %+v, %v; the listing says %+v", listed.Record.ID, one, err, listed)
		}
	}
	if all[0].State.State == all[1].State.State {
		t.Fatalf("both tasks resolved to %s, so pairing them wrongly would show nothing", all[0].State.State)
	}
	if _, err := r.Resolved(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Resolved of a missing task = %v", err)
	}
}
