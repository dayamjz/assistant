package taskstate_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

const leak = "sentinel-text-only-the-open-decision-carries"

type world struct {
	t       *testing.T
	store   *store.Store
	settler *taskstate.Settler
	reader  *taskstate.Reader
}

func newWorld(t *testing.T) *world {
	t.Helper()
	s, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "state.db"), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.UpsertRepository(t.Context(), store.Repository{
		ID: "repo-1", WorkingPath: "/checkouts/one", UpstreamURL: "https://example.test/one.git", DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("recording the repository: %v", err)
	}
	return &world{t: t, store: s, settler: taskstate.NewSettler(s, nil), reader: taskstate.NewReader(s, nil)}
}

func (w *world) task(id string) {
	w.t.Helper()
	if _, err := w.settler.Create(w.t.Context(), store.Task{
		ID: id, Shape: store.TaskDelivery, Project: "p", Mode: "m", WorktreePath: "/copies/" + id,
		RepositoryID: store.Known("repo-1"), Branch: store.Known("topic-" + id),
	}); err != nil {
		w.t.Fatalf("creating task %s: %v", id, err)
	}
}

func (w *world) run(id, branch string) {
	w.t.Helper()
	if _, err := w.store.CreateRun(w.t.Context(), store.Run{
		ID: id, RepositoryID: "repo-1", Branch: branch, SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "validate", IntentSource: "push", ConfigDigest: "cfg",
		Build: store.Build{Version: "v0.1.0", Revision: "0123456789abcdef", Go: "go1.25.0"},
	}); err != nil {
		w.t.Fatalf("creating run %s: %v", id, err)
	}
}

// move changes a run through the store only, which settles nothing.
func (w *world) move(id string, from, to store.RunStatus) {
	w.t.Helper()
	if _, err := w.store.TransitionRun(w.t.Context(), id, []store.RunStatus{from}, to); err != nil {
		w.t.Fatalf("moving run %s to %s: %v", id, to, err)
	}
}

func (w *world) append(id, kind, detail string) {
	w.t.Helper()
	if _, _, err := w.settler.Append(w.t.Context(), id, kind, detail); err != nil {
		w.t.Fatalf("appending %s: %v", kind, err)
	}
}

func (w *world) current(id string) taskstate.Current {
	w.t.Helper()
	c, err := w.reader.Current(w.t.Context(), id)
	if err != nil {
		w.t.Fatalf("reading the state of %s: %v", id, err)
	}
	return c
}

// The scenario of PRD section 13's P8 test, on a real store: the log ends on an
// open decision, the run moved past it, and the settled answer is the run's.
func TestARunThatMovedPastAnOpenDecisionAnswersAndTheEventIsSuperseded(t *testing.T) {
	principles.Cite(t, principles.P8)
	w := newWorld(t)
	w.task("t1")
	for _, e := range [][2]string{{"started", "begun"}, {"handed-off", "to the gate"}, {"decision-open", leak}} {
		w.append("t1", e[0], e[1])
	}
	if got := w.current("t1"); got.State != taskstate.Held || got.Source != taskstate.SourceEvent {
		t.Fatalf("before any run the log answers: %+v", got)
	}

	w.run("run-1", "topic-t1")
	for _, m := range [][2]store.RunStatus{
		{store.RunPending, store.RunRunning}, {store.RunRunning, store.RunHeld},
		{store.RunHeld, store.RunRunning}, {store.RunRunning, store.RunPassed},
	} {
		w.move("run-1", m[0], m[1])
		if err := w.settler.SettleForRun(t.Context(), "run-1"); err != nil {
			t.Fatalf("settling after %s: %v", m[1], err)
		}
	}

	got := w.current("t1")
	if got.State != taskstate.Ready || got.Source != taskstate.SourceRun {
		t.Fatalf("the settled answer is %+v, want ready from the run", got)
	}
	if len(got.Superseded) != 1 || got.Superseded[0].Kind != "decision-open" ||
		got.Superseded[0].Claimed != taskstate.Held || got.Superseded[0].By != taskstate.SourceRun {
		t.Fatalf("superseded %+v", got.Superseded)
	}
	if detail, _ := got.Detail.Get(); strings.Contains(detail, leak) {
		t.Fatalf("the open decision's text reached the detail: %q", detail)
	}
	if got.Basis.RunID != "run-1" || got.Basis.RunStatus != "passed" {
		t.Fatalf("basis %+v", got.Basis)
	}

	// A settle that changes nothing moves nothing.
	before := got.Revision
	if _, err := w.settler.Settle(t.Context(), "t1"); err != nil {
		t.Fatalf("settling again: %v", err)
	}
	if again := w.current("t1"); again.Revision != before {
		t.Fatalf("a settle that changed nothing moved the revision from %d to %d", before, again.Revision)
	}
	// A progress note changes the basis and not the answer.
	w.append("t1", "progress-note", "still here")
	noted := w.current("t1")
	if noted.Revision != before || noted.Basis.EventSeq != 5 {
		t.Fatalf("a note moved the revision (%d to %d) or left the basis behind (%+v)", before, noted.Revision, noted.Basis)
	}
}

// The control for the test above: the same log with no run attributed is
// answered by the log and supersedes nothing, so the resolver is not simply
// ignoring events. A run on another branch is not this task's.
func TestWithoutAnAttributedRunTheLogAnswers(t *testing.T) {
	w := newWorld(t)
	w.task("t1")
	w.append("t1", "decision-open", leak)
	w.run("run-other", "some-other-branch")
	w.move("run-other", store.RunPending, store.RunPassed)
	if err := w.settler.SettleForRun(t.Context(), "run-other"); err != nil {
		t.Fatal(err)
	}
	got := w.current("t1")
	if got.State != taskstate.Held || got.Source != taskstate.SourceEvent || len(got.Superseded) != 0 {
		t.Fatalf("resolved to %+v", got)
	}
	if err := w.reader.Audit(t.Context(), "t1"); err != nil {
		t.Fatalf("audit: %v", err)
	}
}

// The hazard of a stored answer and its detector: a run moved without settling
// leaves the record where it was, Current does not quietly resolve, and Audit
// names the run.
func TestAStaleRecordIsDetectedAndNotSilentlyRepaired(t *testing.T) {
	principles.Cite(t, principles.P8)
	w := newWorld(t)
	w.task("t1")
	w.append("t1", "decision-open", leak)
	w.run("run-1", "topic-t1")
	w.move("run-1", store.RunPending, store.RunPassed)

	if got := w.current("t1"); got.State != taskstate.Held {
		t.Fatalf("Current moved on its own to %+v; it must only read", got)
	}
	var drift *taskstate.DriftError
	err := w.reader.Audit(t.Context(), "t1")
	if !errors.As(err, &drift) || drift.Resolved.State != taskstate.Ready || drift.Resolved.Basis.RunID != "run-1" ||
		!strings.Contains(err.Error(), "run-1") {
		t.Fatalf("audit = %v, want drift naming run-1", err)
	}
	if _, err := w.settler.Settle(t.Context(), "t1"); err != nil {
		t.Fatal(err)
	}
	if err := w.reader.Audit(t.Context(), "t1"); err != nil {
		t.Fatalf("audit after settling: %v", err)
	}
}

func TestSettleAllCatchesUpEveryTask(t *testing.T) {
	w := newWorld(t)
	for _, id := range []string{"a", "b"} {
		w.task(id)
		w.run("run-"+id, "topic-"+id)
		w.move("run-"+id, store.RunPending, store.RunFailed)
	}
	if err := w.settler.SettleAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		if got := w.current(id); got.State != taskstate.Failed {
			t.Errorf("task %s is %+v", id, got)
		}
	}
}

// The same writes in a different order settle to the same answer.
func TestTheOrderOfWritesDoesNotChangeTheAnswer(t *testing.T) {
	a, b := newWorld(t), newWorld(t)
	a.task("t1")
	a.append("t1", "started", "")
	a.append("t1", "decision-open", leak)
	a.run("run-1", "topic-t1")
	a.move("run-1", store.RunPending, store.RunPassed)
	if err := a.settler.SettleForRun(t.Context(), "run-1"); err != nil {
		t.Fatal(err)
	}

	b.task("t1")
	b.run("run-1", "topic-t1")
	b.move("run-1", store.RunPending, store.RunPassed)
	b.append("t1", "started", "")
	b.append("t1", "decision-open", leak)

	ca, cb := a.current("t1"), b.current("t1")
	if !reflect.DeepEqual(ca.Resolution, cb.Resolution) {
		t.Fatalf("the answers differ:\n%+v\n%+v", ca.Resolution, cb.Resolution)
	}
}

// Appending and settling are one step: an event the resolver cannot settle is
// not left in the log.
func TestAnAppendThatCannotSettleLeavesNoEvent(t *testing.T) {
	w := newWorld(t)
	w.task("t1")
	before, _ := w.store.TaskEvents(t.Context(), "t1", 0, 0)
	_, _, err := w.store.AppendTaskEventAndSettle(t.Context(), "t1", "started", "", func(store.TaskSnapshot) (store.TaskStateWrite, error) {
		return store.TaskStateWrite{}, errors.New("cannot decide")
	})
	if err == nil {
		t.Fatal("the append succeeded though the decision failed")
	}
	after, _ := w.store.TaskEvents(t.Context(), "t1", 0, 0)
	if len(after) != len(before) {
		t.Fatalf("the failed settle left %d events behind", len(after)-len(before))
	}
}

// A stored state this package cannot interpret is refused, not passed along.
func TestAnUninterpretableRecordIsRefused(t *testing.T) {
	w := newWorld(t)
	w.task("t1")
	if _, err := w.store.SettleTaskState(t.Context(), "t1", func(store.TaskSnapshot) (store.TaskStateWrite, error) {
		return store.TaskStateWrite{State: "validated", Source: "run"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.reader.Current(t.Context(), "t1"); !errors.Is(err, taskstate.ErrUnreadable) {
		t.Fatalf("a state outside the vocabulary was read as %v", err)
	}
	if _, err := w.store.SettleTaskState(t.Context(), "t1", func(store.TaskSnapshot) (store.TaskStateWrite, error) {
		return store.TaskStateWrite{State: "ready", Source: "run", Superseded: store.Known(`[{"bogus":1}]`), Basis: store.Known(`{}`)}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.reader.Current(t.Context(), "t1"); !errors.Is(err, taskstate.ErrUnreadable) {
		t.Fatalf("an undecodable superseded list was read as %v", err)
	}
}

// A row written without the resolver's fields says so, and an audit does not
// pass it as matching.
func TestARowWithoutABasisIsUnrecorded(t *testing.T) {
	w := newWorld(t)
	w.task("t1")
	if _, err := w.store.SettleTaskState(t.Context(), "t1", func(store.TaskSnapshot) (store.TaskStateWrite, error) {
		return store.TaskStateWrite{State: "queued", Source: "event", Detail: store.Unknown[string]()}, nil
	}); err != nil {
		t.Fatal(err)
	}
	got := w.current("t1")
	if !got.Unrecorded {
		t.Fatalf("a row with no basis was read as recorded: %+v", got)
	}
	var drift *taskstate.DriftError
	if err := w.reader.Audit(t.Context(), "t1"); !errors.As(err, &drift) {
		t.Fatalf("audit = %v", err)
	}
}

func TestAProbeFindingTheTerminalDeadMakesTheTaskUnknown(t *testing.T) {
	w := newWorld(t)
	probe := func(context.Context, string) taskstate.Liveness { return taskstate.Dead }
	w.settler, w.reader = taskstate.NewSettler(w.store, probe), taskstate.NewReader(w.store, probe)
	w.task("t1")
	w.append("t1", "started", "")
	if got := w.current("t1"); got.State != taskstate.Running {
		t.Fatalf("a task with no session was probed: %+v", got)
	}
	if err := w.store.SetTaskSession(t.Context(), "t1", "pane-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.settler.Settle(t.Context(), "t1"); err != nil {
		t.Fatal(err)
	}
	got := w.current("t1")
	if got.State != taskstate.Unknown || got.Source != taskstate.SourceProcess ||
		len(got.Superseded) != 1 || got.Superseded[0].Kind != "started" {
		t.Fatalf("resolved to %+v", got)
	}
	if err := w.reader.Audit(t.Context(), "t1"); err != nil {
		t.Fatalf("audit: %v", err)
	}
}

// Appenders and run movers race; afterward nothing is stale. Run under -race.
func TestConcurrentAppendersAndRunMoversLeaveNothingStale(t *testing.T) {
	w := newWorld(t)
	const tasks = 4
	for i := 0; i < tasks; i++ {
		id := fmt.Sprintf("t%d", i)
		w.task(id)
		w.run("run-"+id, "topic-"+id)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 256)
	for i := 0; i < tasks; i++ {
		id := fmt.Sprintf("t%d", i)
		wg.Add(2)
		go func() {
			defer wg.Done()
			for _, kind := range []string{"started", "decision-open", "progress-note", "decision-closed", "report-ready"} {
				if _, _, err := w.settler.Append(t.Context(), id, kind, ""); err != nil {
					errs <- err
				}
			}
		}()
		go func() {
			defer wg.Done()
			for _, to := range []store.RunStatus{store.RunRunning, store.RunHeld, store.RunRunning, store.RunPassed} {
				if _, err := w.store.TransitionRun(t.Context(), "run-"+id, []store.RunStatus{store.RunPending, store.RunRunning, store.RunHeld}, to); err != nil {
					errs <- err
					continue
				}
				if err := w.settler.SettleForRun(t.Context(), "run-"+id); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("a concurrent write failed: %v", err)
	}
	for i := 0; i < tasks; i++ {
		id := fmt.Sprintf("t%d", i)
		if err := w.reader.Audit(t.Context(), id); err != nil {
			t.Errorf("task %s: %v", id, err)
		}
		if got := w.current(id); got.State != taskstate.Ready {
			t.Errorf("task %s is %+v", id, got)
		}
	}
}

// The store methods that take a decision are the three that write a task's
// state record. Asked of the method set by parameter type, with the walk shown
// to find something so it cannot pass by inspecting nothing.
func TestOnlyDecisionMethodsWriteTaskState(t *testing.T) {
	decisionType := reflect.TypeOf(store.TaskDecision(nil))
	var found []string
	storeType := reflect.TypeOf(&store.Store{})
	for i := 0; i < storeType.NumMethod(); i++ {
		m := storeType.Method(i)
		for p := 1; p < m.Type.NumIn(); p++ {
			if m.Type.In(p) == decisionType {
				found = append(found, m.Name)
			}
		}
	}
	slices.Sort(found)
	if want := []string{"AppendTaskEventAndSettle", "CreateTask", "SettleTaskState"}; !slices.Equal(found, want) {
		t.Fatalf("store methods taking a decision: %v, want %v", found, want)
	}
	// No exported method is named for writing a state directly.
	for i := 0; i < storeType.NumMethod(); i++ {
		if name := storeType.Method(i).Name; strings.HasPrefix(name, "SetTaskState") || strings.HasPrefix(name, "WriteTaskState") {
			t.Errorf("store has %s, a second way to write a task's state", name)
		}
	}
}
