package graph_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/graph"
)

// threeStepGraph is a linear graph whose nodes append their names to "trace".
func threeStepGraph(t *testing.T, rec *recorder) *graph.Graph {
	t.Helper()
	return mustBuild(t, graph.NewBuilder().
		Start("a").
		Key(graph.Key{Name: "trace", Kind: graph.KindList, Merge: graph.MergeAppend}).
		Node(graph.Node{Name: "a", Writes: []string{"trace"}, NewBody: appendTrace(rec, "a")}).
		Node(graph.Node{Name: "b", Writes: []string{"trace"}, NewBody: appendTrace(rec, "b")}).
		Node(graph.Node{Name: "c", Writes: []string{"trace"}, NewBody: appendTrace(rec, "c")}).
		Edge(graph.Edge{From: "a", To: "b"}).
		Edge(graph.Edge{From: "b", To: "c"}))
}

func TestForkLeavesTheOriginalHistoryIntact(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	g := threeStepGraph(t, rec)
	store := graph.NewMemoryStore()
	exec := mustExecutor(t, g, store, 20)

	original, err := exec.Run(ctx, "run", mustState(t, g, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	before, err := store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(before) != 4 {
		t.Fatalf("the original run has %d checkpoints, want 4", len(before))
	}

	// Fork from the checkpoint written after "a", which is positioned at "b".
	forkPoint := before[1].ID()
	if before[1].Position != "b" {
		t.Fatalf("checkpoint 2 is at %q, want b", before[1].Position)
	}
	id, err := store.Fork(ctx, forkPoint, "retry")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if id.Run != "retry" || id.Seq != forkPoint.Seq {
		t.Errorf("Fork returned %s, want retry#%d", id, forkPoint.Seq)
	}

	forked, err := exec.Resume(ctx, "retry")
	if err != nil {
		t.Fatalf("Resume the fork: %v", err)
	}
	if forked.Status != graph.StatusCompleted {
		t.Fatalf("the fork ended %s, want completed", forked.Status)
	}

	after, err := store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("forking changed the original history from %d to %d checkpoints", len(before), len(after))
	}
	for i := range before {
		if before[i].Run != after[i].Run || before[i].Seq != after[i].Seq ||
			before[i].Position != after[i].Position || before[i].Status != after[i].Status ||
			!before[i].State.Equal(after[i].State) {
			t.Errorf("forking changed the original checkpoint %d", i)
		}
		if after[i].ForkedFrom != nil {
			t.Errorf("the original checkpoint %d was marked as forked", i)
		}
	}
	latest, err := store.Latest(ctx, "run")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if latest.ID() != original.Checkpoint {
		t.Errorf("the original run's latest checkpoint moved to %s", latest.ID())
	}

	// Lineage stays on the checkpoints the fork copied. The ones the resumed
	// run wrote afterwards were copied from nothing and say so.
	forkedHistory, err := store.History(ctx, "retry")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(forkedHistory) <= forkPoint.Seq {
		t.Fatalf("the fork has %d checkpoints, want more than the %d it copied",
			len(forkedHistory), forkPoint.Seq)
	}
	for i, cp := range forkedHistory {
		copied := i < forkPoint.Seq
		if copied && cp.ForkedFrom == nil {
			t.Errorf("copied checkpoint %s records no lineage", cp.ID())
		}
		if !copied && cp.ForkedFrom != nil {
			t.Errorf("checkpoint %s, written after the fork resumed, claims to be copied from %s",
				cp.ID(), cp.ForkedFrom)
		}
	}

	// The fork replayed only the work after the fork point.
	if !equalStrings(rec.order(), []string{"a", "b", "c", "b", "c"}) {
		t.Errorf("bodies ran %v, want the fork to resume from b", rec.order())
	}
	if got := list(t, forked.State, "trace"); !equalStrings(got, []string{"a", "b", "c"}) {
		t.Errorf("the fork's trace = %v, want a b c", got)
	}
}

func TestForkCopiesTheHistoryUpToItsPointAndRecordsTheLineage(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	g := threeStepGraph(t, rec)
	store := graph.NewMemoryStore()
	exec := mustExecutor(t, g, store, 20)
	if _, err := exec.Run(ctx, "run", mustState(t, g, nil)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if _, err := store.Fork(ctx, graph.CheckpointID{Run: "run", Seq: 2}, "retry"); err != nil {
		t.Fatalf("Fork: %v", err)
	}
	forked, err := store.History(ctx, "retry")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(forked) != 2 {
		t.Fatalf("the fork has %d checkpoints, want 2", len(forked))
	}
	for i, cp := range forked {
		if cp.Run != "retry" || cp.Seq != i+1 {
			t.Errorf("forked checkpoint %d is %s", i, cp.ID())
		}
		if cp.ForkedFrom == nil || *cp.ForkedFrom != (graph.CheckpointID{Run: "run", Seq: i + 1}) {
			t.Errorf("forked checkpoint %d records lineage %v", i, cp.ForkedFrom)
		}
		if err := g.Validate(cp); err != nil {
			t.Errorf("forked checkpoint %d does not validate: %v", i, err)
		}
	}
}

func TestForkRefusesAPointOrDestinationItCannotHonour(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	g := threeStepGraph(t, rec)
	store := graph.NewMemoryStore()
	exec := mustExecutor(t, g, store, 20)
	if _, err := exec.Run(ctx, "run", mustState(t, g, nil)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if _, err := store.Fork(ctx, graph.CheckpointID{Run: "run", Seq: 99}, "retry"); !errors.Is(err, graph.ErrNoSuchCheckpoint) {
		t.Errorf("forking past the end of a run: %v, want ErrNoSuchCheckpoint", err)
	}
	if _, err := store.Fork(ctx, graph.CheckpointID{Run: "absent", Seq: 1}, "retry"); !errors.Is(err, graph.ErrNoSuchCheckpoint) {
		t.Errorf("forking an unknown run: %v, want ErrNoSuchCheckpoint", err)
	}
	if _, err := store.Fork(ctx, graph.CheckpointID{Run: "run", Seq: 1}, "run"); !errors.Is(err, graph.ErrRunExists) {
		t.Errorf("forking a run onto itself: %v, want ErrRunExists", err)
	}
	if _, err := store.Fork(ctx, graph.CheckpointID{Run: "run", Seq: 1}, "retry"); err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if _, err := store.Fork(ctx, graph.CheckpointID{Run: "run", Seq: 2}, "retry"); !errors.Is(err, graph.ErrRunExists) {
		t.Errorf("forking onto a run that already has history: %v, want ErrRunExists", err)
	}
	if history, err := store.History(ctx, "retry"); err != nil || len(history) != 1 {
		t.Errorf("the refused fork changed the destination: %d checkpoints, %v", len(history), err)
	}
}

func TestConcurrentRunsUnderOneNameClaimItExactlyOnce(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	g := threeStepGraph(t, rec)
	store := graph.NewMemoryStore()
	exec := mustExecutor(t, g, store, 20)

	const attempts = 8
	errs := make([]error, attempts)
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-release
			_, errs[i] = exec.Run(ctx, "run", mustStateNoHelper(g))
		}(i)
	}
	close(release)
	wg.Wait()

	started := 0
	for i, err := range errs {
		switch {
		case err == nil:
			started++
		case errors.Is(err, graph.ErrRunExists):
		default:
			t.Fatalf("attempt %d: %v, want either success or ErrRunExists", i, err)
		}
	}
	if started != 1 {
		t.Fatalf("%d of %d concurrent Run calls started the run, want exactly 1", started, attempts)
	}

	history, err := store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	wantPositions := []string{"a", "b", "c", ""}
	if len(history) != len(wantPositions) {
		t.Fatalf("the run has %d checkpoints, want %d: the losing calls wrote into it",
			len(history), len(wantPositions))
	}
	for i, cp := range history {
		if cp.Seq != i+1 || cp.Position != wantPositions[i] {
			t.Fatalf("checkpoint %d is %s at %q, want %d at %q: two runs interleaved",
				i+1, cp.ID(), cp.Position, i+1, wantPositions[i])
		}
	}
	if got := list(t, history[len(history)-1].State, "trace"); !equalStrings(got, []string{"a", "b", "c"}) {
		t.Errorf("trace = %v, want one pass through the graph", got)
	}
	if got := rec.order(); !equalStrings(got, []string{"a", "b", "c"}) {
		t.Errorf("bodies ran %v, want one pass: a refused Run must execute nothing", got)
	}
}

// appendOnlyStore satisfies the letter of "append and assign Seq" while
// ignoring the claim a zero Seq makes on a new run: it forwards every
// operation to a MemoryStore, and the only thing it declines to do is refuse a
// checkpoint that claims a run which already has history. It stands in for a
// substrate written against that half of the contract alone.
type appendOnlyStore struct {
	inner *graph.MemoryStore
}

func (s appendOnlyStore) Write(ctx context.Context, c graph.Checkpoint) (graph.CheckpointID, error) {
	if c.Seq == 0 {
		c.Seq = 1
	}
	return s.inner.Write(ctx, c)
}

func (s appendOnlyStore) Latest(ctx context.Context, run string) (graph.Checkpoint, error) {
	return s.inner.Latest(ctx, run)
}

func (s appendOnlyStore) History(ctx context.Context, run string) ([]graph.Checkpoint, error) {
	return s.inner.History(ctx, run)
}

func (s appendOnlyStore) Fork(ctx context.Context, from graph.CheckpointID, into string) (graph.CheckpointID, error) {
	return s.inner.Fork(ctx, from, into)
}

func TestRunRefusesAStoreThatDoesNotHonourTheRunClaim(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	g := threeStepGraph(t, rec)
	store := appendOnlyStore{inner: graph.NewMemoryStore()}
	exec := mustExecutor(t, g, store, 20)

	if _, err := exec.Run(ctx, "run", mustState(t, g, nil)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	before, err := store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(before) != 4 {
		t.Fatalf("the first run wrote %d checkpoints, want 4", len(before))
	}

	if _, err := exec.Run(ctx, "run", mustState(t, g, nil)); !errors.Is(err, graph.ErrRunExists) {
		t.Fatalf("starting over a run on a store that ignores the claim = %v, want ErrRunExists", err)
	}

	after, err := store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(after) != len(before)+1 {
		t.Errorf("the refused run added %d checkpoints, want only the one that revealed the store",
			len(after)-len(before))
	}
	for i := range before {
		if before[i].Position != after[i].Position || !before[i].State.Equal(after[i].State) {
			t.Errorf("the refused run changed the original checkpoint %d", i+1)
		}
	}
	if got := rec.order(); !equalStrings(got, []string{"a", "b", "c"}) {
		t.Errorf("bodies ran %v, want one pass: a refused Run must not walk the graph", got)
	}
}

// retainingStore keeps the Checkpoint value it was handed rather than encoding
// or copying it, which the Write contract permits. It stands in for a
// substrate that holds checkpoints in memory as values, and it forwards
// everything to a MemoryStore so the run itself behaves normally.
type retainingStore struct {
	inner *graph.MemoryStore
	mu    sync.Mutex
	held  []graph.Checkpoint
}

func (s *retainingStore) Write(ctx context.Context, c graph.Checkpoint) (graph.CheckpointID, error) {
	s.mu.Lock()
	s.held = append(s.held, c)
	s.mu.Unlock()
	return s.inner.Write(ctx, c)
}

func (s *retainingStore) Latest(ctx context.Context, run string) (graph.Checkpoint, error) {
	return s.inner.Latest(ctx, run)
}

func (s *retainingStore) History(ctx context.Context, run string) ([]graph.Checkpoint, error) {
	return s.inner.History(ctx, run)
}

func (s *retainingStore) Fork(ctx context.Context, from graph.CheckpointID, into string) (graph.CheckpointID, error) {
	return s.inner.Fork(ctx, from, into)
}

func (s *retainingStore) retained() []graph.Checkpoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]graph.Checkpoint(nil), s.held...)
}

func TestARetainedCheckpointKeepsTheCountersItWasWrittenWith(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	g := mustBuild(t, fixLoopBuilder(rec, 2, func(_ context.Context, _ graph.Reader, w graph.Writer) error {
		rec.note("fix")
		return w.Set("log", graph.ListValue("fix"))
	}))
	store := &retainingStore{inner: graph.NewMemoryStore()}
	exec := mustExecutor(t, g, store, 20)

	got, err := exec.Run(ctx, "run", mustState(t, g, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Status != graph.StatusRoundsExhausted {
		t.Fatalf("the run ended %s, want rounds_exhausted: the fixture must spend its bound", got.Status)
	}

	// The history MemoryStore decoded is what each checkpoint held at the
	// moment it was written, because encoding snapshots it.
	history, err := store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	retained := store.retained()
	if len(retained) != len(history) {
		t.Fatalf("the store retained %d checkpoints and holds %d", len(retained), len(history))
	}
	if !countersMoved(history) {
		t.Fatal("the fixture never changed the counters, so this proves nothing")
	}
	for i := range history {
		if retained[i].Counters.Steps != history[i].Counters.Steps {
			t.Errorf("retained checkpoint %d records %d steps, want the %d it was written with",
				i+1, retained[i].Counters.Steps, history[i].Counters.Steps)
		}
		if !equalInts(retained[i].Counters.Traversals, history[i].Counters.Traversals) {
			t.Errorf("retained checkpoint %d records traversals %v, want the %v it was written with",
				i+1, retained[i].Counters.Traversals, history[i].Counters.Traversals)
		}
		if !equalStrings(retained[i].Counters.Fingerprints, history[i].Counters.Fingerprints) {
			t.Errorf("retained checkpoint %d records fingerprints that are not the ones it was written with", i+1)
		}
	}
}

// countersMoved reports whether the traversal counts differ across the
// history, which is what makes retaining an aliased slice observable.
func countersMoved(history []graph.Checkpoint) bool {
	for i := 1; i < len(history); i++ {
		if !equalInts(history[i].Counters.Traversals, history[0].Counters.Traversals) {
			return true
		}
	}
	return false
}

func equalInts(got, want []int) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// misroutingStore answers Latest with a checkpoint belonging to another run,
// the way a substrate with a key-prefix bug or a query missing its run filter
// would. Everything else it forwards untouched.
type misroutingStore struct {
	inner *graph.MemoryStore
	at    graph.CheckpointID
}

func (s misroutingStore) Write(ctx context.Context, c graph.Checkpoint) (graph.CheckpointID, error) {
	return s.inner.Write(ctx, c)
}

func (s misroutingStore) Latest(ctx context.Context, _ string) (graph.Checkpoint, error) {
	history, err := s.inner.History(ctx, s.at.Run)
	if err != nil {
		return graph.Checkpoint{}, err
	}
	if s.at.Seq < 1 || s.at.Seq > len(history) {
		return graph.Checkpoint{}, fmt.Errorf("no checkpoint %s", s.at)
	}
	return history[s.at.Seq-1], nil
}

func (s misroutingStore) History(ctx context.Context, run string) ([]graph.Checkpoint, error) {
	return s.inner.History(ctx, run)
}

func (s misroutingStore) Fork(ctx context.Context, from graph.CheckpointID, into string) (graph.CheckpointID, error) {
	return s.inner.Fork(ctx, from, into)
}

func TestResumeAndAnswerRefuseACheckpointFromAnotherRun(t *testing.T) {
	ctx := context.Background()
	cases := map[string]struct {
		seq  int
		call func(*graph.Executor) error
	}{
		"resume a checkpoint the other run is still running": {
			seq: 1,
			call: func(e *graph.Executor) error {
				_, err := e.Resume(ctx, "wanted")
				return err
			},
		},
		"answer a decision the other run is halted on": {
			seq: 2,
			call: func(e *graph.Executor) error {
				_, err := e.Answer(ctx, "wanted", "approve")
				return err
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := &recorder{}
			g := mustBuild(t, haltingBuilder(rec))
			inner := graph.NewMemoryStore()
			if _, err := mustExecutor(t, g, inner, 10).Run(ctx, "other", mustState(t, g, nil)); err != nil {
				t.Fatalf("Run: %v", err)
			}
			before, err := inner.History(ctx, "other")
			if err != nil {
				t.Fatalf("History: %v", err)
			}
			ranDuringSetup := rec.order()

			exec := mustExecutor(t, g, misroutingStore{
				inner: inner,
				at:    graph.CheckpointID{Run: "other", Seq: tc.seq},
			}, 10)

			var ce *graph.CheckpointError
			if err := tc.call(exec); !errors.As(err, &ce) {
				t.Fatalf("error = %T %v, want a *graph.CheckpointError", err, err)
			}

			after, err := inner.History(ctx, "other")
			if err != nil {
				t.Fatalf("History: %v", err)
			}
			if len(after) != len(before) {
				t.Errorf("the other run's history grew from %d to %d checkpoints", len(before), len(after))
			}
			if history, err := inner.History(ctx, "wanted"); err != nil || len(history) != 0 {
				t.Errorf("the requested run has %d checkpoints, %v", len(history), err)
			}
			if got := rec.order(); !equalStrings(got, ranDuringSetup) {
				t.Errorf("bodies ran %v, want nothing past the %v of the setup run", got, ranDuringSetup)
			}
		})
	}
}

func TestAnEmptyRunHasNoLatestAndNoHistory(t *testing.T) {
	ctx := context.Background()
	store := graph.NewMemoryStore()

	if _, err := store.Latest(ctx, "absent"); !errors.Is(err, graph.ErrNoSuchRun) {
		t.Errorf("Latest of an unknown run = %v, want ErrNoSuchRun", err)
	}
	history, err := store.History(ctx, "absent")
	if err != nil {
		t.Errorf("History of an unknown run = %v, want no error", err)
	}
	if len(history) != 0 {
		t.Errorf("History of an unknown run has %d entries", len(history))
	}
}

func TestMemoryStoreIsSafeForConcurrentUse(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	g := threeStepGraph(t, rec)
	store := graph.NewMemoryStore()
	exec := mustExecutor(t, g, store, 20)

	const runs = 8
	var wg sync.WaitGroup
	errs := make([]error, runs)
	for i := 0; i < runs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("run-%d", i)
			if _, err := exec.Run(ctx, name, mustStateNoHelper(g)); err != nil {
				errs[i] = err
				return
			}
			if _, err := store.Fork(ctx, graph.CheckpointID{Run: name, Seq: 2}, name+"-retry"); err != nil {
				errs[i] = err
				return
			}
			if _, err := store.History(ctx, name); err != nil {
				errs[i] = err
				return
			}
			if _, err := exec.Resume(ctx, name+"-retry"); err != nil {
				errs[i] = err
			}
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	for i := 0; i < runs; i++ {
		name := fmt.Sprintf("run-%d", i)
		history, err := store.History(ctx, name)
		if err != nil {
			t.Fatalf("History(%q): %v", name, err)
		}
		if len(history) != 4 {
			t.Errorf("%s has %d checkpoints, want 4", name, len(history))
		}
		forked, err := store.History(ctx, name+"-retry")
		if err != nil {
			t.Fatalf("History(%q): %v", name+"-retry", err)
		}
		if len(forked) != 4 {
			t.Errorf("%s-retry has %d checkpoints, want 4", name, len(forked))
		}
	}
}

func TestStoredCheckpointsDoNotAliasWhatTheCallerHolds(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	g := threeStepGraph(t, rec)
	store := graph.NewMemoryStore()
	exec := mustExecutor(t, g, store, 20)
	if _, err := exec.Run(ctx, "run", mustState(t, g, nil)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	first, err := store.Latest(ctx, "run")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	first.Position = "tampered"
	first.Counters.Steps = 99

	second, err := store.Latest(ctx, "run")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if second.Position != "" || second.Counters.Steps != 3 {
		t.Errorf("mutating a checkpoint the store returned changed what it holds: %+v", second)
	}
}
