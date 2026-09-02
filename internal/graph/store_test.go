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

// appendOnlyStore satisfies the letter of "append and assign Seq" and nothing
// more: it discards the anchor it is handed and appends against whatever the
// run's tip happens to be, so it neither refuses a claim on a run that already
// has history nor refuses a write whose run has moved. It stands in for a
// substrate written before the anchor was part of the contract, and it is
// what makes the executor's post-condition on the store's answer observable.
type appendOnlyStore struct {
	inner *graph.MemoryStore
}

func (s appendOnlyStore) Write(ctx context.Context, _ graph.CheckpointID, c graph.Checkpoint) (graph.CheckpointID, error) {
	history, err := s.inner.History(ctx, c.Run)
	if err != nil {
		return graph.CheckpointID{}, err
	}
	return s.inner.Write(ctx, graph.CheckpointID{Run: c.Run, Seq: len(history)}, c)
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

// retainingStore keeps the Checkpoint value it was handed and answers Latest
// with what it kept, which the Write, Latest and History contracts all permit.
// It stands in for a substrate that holds checkpoints in memory as values
// rather than encoding them, and it forwards to a MemoryStore so the run
// itself behaves normally.
type retainingStore struct {
	inner *graph.MemoryStore
	mu    sync.Mutex
	held  []graph.Checkpoint
}

func (s *retainingStore) Write(ctx context.Context, anchor graph.CheckpointID, c graph.Checkpoint) (graph.CheckpointID, error) {
	id, err := s.inner.Write(ctx, anchor, c)
	if err != nil {
		return graph.CheckpointID{}, err
	}
	c.Seq = id.Seq
	s.mu.Lock()
	s.held = append(s.held, c)
	s.mu.Unlock()
	return id, nil
}

func (s *retainingStore) Latest(_ context.Context, run string) (graph.Checkpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.held) - 1; i >= 0; i-- {
		if s.held[i].Run == run {
			return s.held[i], nil
		}
	}
	return graph.Checkpoint{}, fmt.Errorf("%w: %q", graph.ErrNoSuchRun, run)
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

// haltingLoopBuilder declares a decision inside a bounded cycle: review routes
// to the gate while there is anything to fix, the gate halts for an answer,
// the fixer records a fix, and the bounded back edge sends the run around
// again. Resuming it therefore crosses a halt point and a back edge, which is
// what moves the state, the decision and the counters a checkpoint carries.
func haltingLoopBuilder(rec *recorder) *graph.Builder {
	return graph.NewBuilder().
		Start("review").
		Key(graph.Key{Name: "findings", Kind: graph.KindInt}).
		Key(graph.Key{Name: "fixes", Kind: graph.KindInt, Merge: graph.MergeSum}).
		Key(graph.Key{Name: "answer", Kind: graph.KindText}).
		Node(graph.Node{
			Name:   "review",
			Reads:  []string{"fixes"},
			Writes: []string{"findings"},
			NewBody: body(func(_ context.Context, r graph.Reader, w graph.Writer) error {
				rec.note("review")
				fixes, err := r.Get("fixes")
				if err != nil {
					return err
				}
				if applied, _ := fixes.Int(); applied > 0 {
					return w.Set("findings", graph.IntValue(0))
				}
				return w.Set("findings", graph.IntValue(1))
			}),
		}).
		Node(graph.Node{
			Name:    "gate",
			Reads:   []string{"answer"},
			Halt:    &graph.Halt{Question: "apply the fix?", Options: []string{"fix", "stop"}, Into: "answer"},
			NewBody: noteOnly(rec, "gate"),
		}).
		Node(graph.Node{
			Name:   "fix",
			Writes: []string{"fixes"},
			NewBody: body(func(_ context.Context, _ graph.Reader, w graph.Writer) error {
				rec.note("fix")
				return w.Set("fixes", graph.IntValue(1))
			}),
		}).
		Node(graph.Node{Name: "done", NewBody: noteOnly(rec, "done")}).
		Edge(graph.Edge{From: "review", To: "gate", Guard: &graph.Guard{
			Key: "findings", Op: graph.OpGreaterThan, Value: graph.IntValue(0),
		}}).
		Edge(graph.Edge{From: "review", To: "done"}).
		Edge(graph.Edge{From: "gate", To: "fix", Guard: &graph.Guard{
			Key: "answer", Op: graph.OpEquals, Value: graph.TextValue("fix"),
		}}).
		Edge(graph.Edge{From: "gate", To: "done"}).
		Edge(graph.Edge{From: "fix", To: "review", Rounds: 2})
}

func TestARetainedCheckpointKeepsWhatItWasWrittenWith(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	g := mustBuild(t, haltingLoopBuilder(rec))
	store := &retainingStore{inner: graph.NewMemoryStore()}
	exec := mustExecutor(t, g, store, 20)

	held, err := exec.Run(ctx, "run", mustState(t, g, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if held.Status != graph.StatusHalted {
		t.Fatalf("the run ended %s, want halted: the fixture must reach its decision", held.Status)
	}
	done, err := exec.Answer(ctx, "run", "fix")
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if done.Status != graph.StatusCompleted {
		t.Fatalf("the run ended %s, want completed", done.Status)
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
	if !countersMoved(history) || !statesMoved(history) || !decisionsMoved(history) {
		t.Fatal("the fixture left the counters, the state or the decision unchanged, so this proves nothing")
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
		if !retained[i].State.Equal(history[i].State) {
			t.Errorf("retained checkpoint %d records state %v, want the %v it was written with",
				i+1, retained[i].State, history[i].State)
		}
		if !sameDecision(retained[i].Decision, history[i].Decision) {
			t.Errorf("retained checkpoint %d records decision %v, want the %v it was written with",
				i+1, retained[i].Decision, history[i].Decision)
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

// statesMoved reports whether the state differs across the history, which is
// what makes retaining an aliased state map observable.
func statesMoved(history []graph.Checkpoint) bool {
	for i := 1; i < len(history); i++ {
		if !history[i].State.Equal(history[0].State) {
			return true
		}
	}
	return false
}

// decisionsMoved reports whether the history holds a checkpoint carrying a
// decision and another carrying none, which is what makes retaining an
// aliased decision observable.
func decisionsMoved(history []graph.Checkpoint) bool {
	var open, closed bool
	for _, cp := range history {
		if cp.Decision != nil {
			open = true
		} else {
			closed = true
		}
	}
	return open && closed
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

func (s misroutingStore) Write(ctx context.Context, anchor graph.CheckpointID, c graph.Checkpoint) (graph.CheckpointID, error) {
	return s.inner.Write(ctx, anchor, c)
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

// racingStore holds every Latest until a fixed number of callers have asked
// for one, so two operations on the same run are guaranteed to read the same
// tip and then race to write it. Without the barrier the first caller could
// finish before the second reads, and the interleaving under test would never
// be attempted.
type racingStore struct {
	inner   graph.CheckpointStore
	readers int

	mu      sync.Mutex
	arrived int
	ready   chan struct{}
}

func newRacingStore(readers int, inner graph.CheckpointStore) *racingStore {
	return &racingStore{inner: inner, readers: readers, ready: make(chan struct{})}
}

func (s *racingStore) Write(ctx context.Context, anchor graph.CheckpointID, c graph.Checkpoint) (graph.CheckpointID, error) {
	return s.inner.Write(ctx, anchor, c)
}

func (s *racingStore) Latest(ctx context.Context, run string) (graph.Checkpoint, error) {
	cp, err := s.inner.Latest(ctx, run)
	s.mu.Lock()
	s.arrived++
	if s.arrived == s.readers {
		close(s.ready)
	}
	s.mu.Unlock()
	<-s.ready
	return cp, err
}

func (s *racingStore) History(ctx context.Context, run string) ([]graph.Checkpoint, error) {
	return s.inner.History(ctx, run)
}

func (s *racingStore) Fork(ctx context.Context, from graph.CheckpointID, into string) (graph.CheckpointID, error) {
	return s.inner.Fork(ctx, from, into)
}

func TestConcurrentAnswersToOneRunDoNotInterleave(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	g := mustBuild(t, haltingBuilder(rec))
	store := newRacingStore(2, graph.NewMemoryStore())
	exec := mustExecutor(t, g, store, 20)

	held, err := exec.Run(ctx, "run", mustState(t, g, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if held.Status != graph.StatusHalted {
		t.Fatalf("the run ended %s, want halted at its decision", held.Status)
	}
	before, err := store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}

	const answers = 2
	errs := make([]error, answers)
	var wg sync.WaitGroup
	for i := 0; i < answers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = exec.Answer(ctx, "run", "approve")
		}(i)
	}
	wg.Wait()

	answered := 0
	for i, err := range errs {
		switch {
		case err == nil:
			answered++
		case errors.Is(err, graph.ErrStaleAnchor):
		default:
			t.Fatalf("answer %d: %v, want either success or ErrStaleAnchor", i, err)
		}
	}
	if answered != 1 {
		t.Fatalf("%d of %d concurrent answers were accepted, want exactly 1", answered, answers)
	}

	after, err := store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	for i := range before {
		if before[i].Position != after[i].Position || before[i].Status != after[i].Status {
			t.Fatalf("checkpoint %d changed under the answers", i+1)
		}
	}
	// One answer's walk, appended once: the gate runs and the run stands at
	// act, then act runs and the run completes.
	wantPositions := []string{"act", ""}
	if len(after) != len(before)+len(wantPositions) {
		t.Fatalf("the run has %d checkpoints, want the %d it had plus one walk of %d: the answers interleaved",
			len(after), len(before), len(wantPositions))
	}
	for i, want := range wantPositions {
		cp := after[len(before)+i]
		if cp.Seq != len(before)+i+1 || cp.Position != want {
			t.Fatalf("checkpoint %d is %s at %q, want %d at %q", i+1, cp.ID(), cp.Position, len(before)+i+1, want)
		}
	}
}

func TestConcurrentAnswersAreRefusedWhenTheStoreIgnoresTheAnchor(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	g := mustBuild(t, haltingBuilder(rec))
	store := newRacingStore(2, appendOnlyStore{inner: graph.NewMemoryStore()})
	exec := mustExecutor(t, g, store, 20)

	held, err := exec.Run(ctx, "run", mustState(t, g, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if held.Status != graph.StatusHalted {
		t.Fatalf("the run ended %s, want halted at its decision", held.Status)
	}

	const answers = 2
	errs := make([]error, answers)
	var wg sync.WaitGroup
	for i := 0; i < answers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = exec.Answer(ctx, "run", "approve")
		}(i)
	}
	wg.Wait()

	accepted := 0
	for i, err := range errs {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, graph.ErrStaleAnchor):
		default:
			t.Fatalf("answer %d: %v, want either success or ErrStaleAnchor", i, err)
		}
	}
	if accepted > 1 {
		t.Fatalf("%d of %d concurrent answers were accepted over a store that ignores the anchor, want at most 1",
			accepted, answers)
	}

	// The store appended what the executor refused, because a substrate that
	// ignores the anchor cannot be stopped from writing. What must not happen
	// is a caller being told both walks landed.
	history, err := store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	completed := 0
	for _, cp := range history {
		if cp.Status == graph.StatusCompleted {
			completed++
		}
	}
	if completed > 1 {
		t.Errorf("the run records %d completed walks, want at most 1: the answers interleaved", completed)
	}
}

func TestWriteRefusesACheckpointAnchoredToARunThatHasMoved(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	g := threeStepGraph(t, rec)
	store := graph.NewMemoryStore()
	exec := mustExecutor(t, g, store, 20)
	if _, err := exec.Run(ctx, "run", mustState(t, g, nil)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	before, err := store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(before) < 2 {
		t.Fatalf("the run has %d checkpoints, want enough for one of them to be stale", len(before))
	}

	stale := before[len(before)-2]
	if _, err := store.Write(ctx, stale.ID(), stale); !errors.Is(err, graph.ErrStaleAnchor) {
		t.Fatalf("writing against %s when the run stands at %s = %v, want ErrStaleAnchor",
			stale.ID(), before[len(before)-1].ID(), err)
	}

	tip := before[len(before)-1]
	if _, err := store.Write(ctx, tip.ID(), tip); err != nil {
		t.Fatalf("writing against the run's own tip: %v, want it accepted", err)
	}

	after, err := store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(after) != len(before)+1 {
		t.Errorf("the run has %d checkpoints, want %d: the refused write was appended",
			len(after), len(before)+1)
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
