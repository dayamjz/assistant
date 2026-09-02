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
