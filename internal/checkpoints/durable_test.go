package checkpoints_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/dayamjz/assistant/internal/checkpoints"
	"github.com/dayamjz/assistant/internal/graph"
)

func TestARunSurvivesTheProcessThatReachedIt(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")

	// Before: a run walks to its decision and stops there.
	before := &recorder{}
	g := fixLoopGraph(t, before)
	beforeStore := checkpoints.New(openRecordsAt(t, path))
	held, err := mustExecutor(t, g, beforeStore, 20).Run(ctx, "run", mustState(t, g, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if held.Status != graph.StatusHalted || held.Position != "gate" {
		t.Fatalf("the run ended %s at %q, want halted at gate", held.Status, held.Position)
	}
	// A halt point stops the run before its node, so the gate has not started.
	if got := before.order(); !equalStrings(got, []string{"review"}) {
		t.Fatalf("bodies ran %v before the restart, want only review", got)
	}

	stoodBefore, err := beforeStore.Latest(ctx, "run")
	if err != nil {
		t.Fatalf("Latest before the restart: %v", err)
	}

	// The store the run was written through is gone, along with everything the
	// process held in memory. Only the database file is left.
	records := openRecordsAt(t, path)
	if err := records.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// After: nothing of the first executor, the first store, or the first set
	// of node bodies survives into this one.
	after := &recorder{}
	restarted := fixLoopGraph(t, after)
	s := checkpoints.New(openRecordsAt(t, path))
	exec := mustExecutor(t, restarted, s, 20)

	stood, err := s.Latest(ctx, "run")
	if err != nil {
		t.Fatalf("the run's position did not survive the restart: %v", err)
	}
	if stood.ID() != held.Checkpoint {
		t.Errorf("the run comes back at %s, want the %s it stopped at", stood.ID(), held.Checkpoint)
	}
	if stood.Position != "gate" || stood.Status != graph.StatusHalted {
		t.Errorf("the run comes back %s at %q, want halted at gate", stood.Status, stood.Position)
	}
	if !sameDecision(stood.Decision, held.Decision) {
		t.Errorf("the run comes back waiting on %v, want the %v it stopped for", stood.Decision, held.Decision)
	}
	if stood.Counters.Steps != held.Steps || stood.Counters.Budget != held.Budget {
		t.Errorf("the run comes back having spent %d of %d steps, want %d of %d",
			stood.Counters.Steps, stood.Counters.Budget, held.Steps, held.Budget)
	}
	// The counters came back indexed against the edge vector they accrued on.
	// Resuming below is the other half of that: a graph whose edges differed
	// from the digest a checkpoint carries is refused rather than resumed.
	if stood.Counters.EdgeDigest == "" || stood.Counters.EdgeDigest != stoodBefore.Counters.EdgeDigest {
		t.Errorf("the run comes back indexed against %q, want the %q it recorded",
			stood.Counters.EdgeDigest, stoodBefore.Counters.EdgeDigest)
	}
	if !equalInts(stood.Counters.Traversals, stoodBefore.Counters.Traversals) ||
		!equalStrings(stood.Counters.Fingerprints, stoodBefore.Counters.Fingerprints) {
		t.Errorf("the run comes back with counters %+v, want %+v", stood.Counters, stoodBefore.Counters)
	}

	// It is a position and not only a record: the run goes on from where it
	// stood, on the state and the bound accounting it stood there with.
	done, err := exec.Answer(ctx, "run", "fix")
	if err != nil {
		t.Fatalf("Answer after the restart: %v", err)
	}
	if done.Status != graph.StatusCompleted {
		t.Fatalf("the resumed run ended %s, want completed", done.Status)
	}
	if got := list(t, done.State, "trace"); !equalStrings(got,
		[]string{"review", "gate", "fix", "review", "done"}) {
		t.Errorf("the run traced %v, want the whole walk across the restart", got)
	}
	// The work done before the restart was not done again. The halt point's
	// node had not started when the process went away, so it runs now; the
	// review before it had, and does not.
	if got := after.order(); !equalStrings(got, []string{"gate", "fix", "review", "done"}) {
		t.Errorf("bodies ran %v after the restart, want the run to go on rather than start over", got)
	}
	if done.Steps != 5 {
		t.Errorf("the run ends having spent %d steps, want 5 counted across the restart", done.Steps)
	}
}

func TestARestartedRunIsStillAnchoredToWhatWasObservedBeforeIt(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")

	g := linearGraph(t, &recorder{})
	first := checkpoints.New(openRecordsAt(t, path))
	if _, err := mustExecutor(t, g, first, 20).Run(ctx, "run", mustState(t, g, nil)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	history, err := first.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(history) < 2 {
		t.Fatalf("the run has %d checkpoints, want enough for one of them to be stale", len(history))
	}
	stale := history[len(history)-2]
	tip := history[len(history)-1]

	records := openRecordsAt(t, path)
	if err := records.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// A caller holding a position from before the restart is held to it. The
	// anchor is decided against what the reopened store reads back, so a write
	// carrying a position the run has left is refused rather than appended
	// over the work that followed it.
	s := checkpoints.New(openRecordsAt(t, path))
	if _, err := s.Write(ctx, stale.ID(), stale); !errors.Is(err, graph.ErrStaleAnchor) {
		t.Fatalf("a write anchored to %s after the restart = %v, want ErrStaleAnchor", stale.ID(), err)
	}
	if _, err := s.Write(ctx, graph.CheckpointID{}, tip); !errors.Is(err, graph.ErrRunExists) {
		t.Fatalf("claiming the run as new after the restart = %v, want ErrRunExists", err)
	}
	after, err := s.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(after) != len(history) {
		t.Fatalf("the refused writes left %d checkpoints, want the %d the run had", len(after), len(history))
	}
	// And the run's own tip is still accepted, so what is above is a check
	// rather than a store that stopped taking writes.
	if _, err := s.Write(ctx, tip.ID(), tip); err != nil {
		t.Fatalf("a write anchored to the run's own tip after the restart: %v", err)
	}
}

func TestAForkSurvivesTheProcessThatMadeIt(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")

	rec := &recorder{}
	g := linearGraph(t, rec)
	first := checkpoints.New(openRecordsAt(t, path))
	if _, err := mustExecutor(t, g, first, 20).Run(ctx, "run", mustState(t, g, nil)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := first.Fork(ctx, graph.CheckpointID{Run: "run", Seq: 2}, "retry"); err != nil {
		t.Fatalf("Fork: %v", err)
	}
	records := openRecordsAt(t, path)
	if err := records.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	after := &recorder{}
	restarted := linearGraph(t, after)
	s := checkpoints.New(openRecordsAt(t, path))
	forked, err := s.History(ctx, "retry")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(forked) != 2 {
		t.Fatalf("the fork came back with %d checkpoints, want 2", len(forked))
	}
	for i, cp := range forked {
		want := graph.CheckpointID{Run: "run", Seq: i + 1}
		if cp.ForkedFrom == nil || *cp.ForkedFrom != want {
			t.Errorf("forked checkpoint %d came back with lineage %v, want %s", i+1, cp.ForkedFrom, want)
		}
	}
	if _, err := s.Fork(ctx, graph.CheckpointID{Run: "run", Seq: 3}, "retry"); !errors.Is(err, graph.ErrRunExists) {
		t.Errorf("forking again onto the destination after the restart = %v, want ErrRunExists", err)
	}

	result, err := mustExecutor(t, restarted, s, 20).Resume(ctx, "retry")
	if err != nil {
		t.Fatalf("Resume the fork after the restart: %v", err)
	}
	if result.Status != graph.StatusCompleted {
		t.Fatalf("the resumed fork ended %s, want completed", result.Status)
	}
	if got := after.order(); !equalStrings(got, []string{"b", "c"}) {
		t.Errorf("bodies ran %v after the restart, want the fork to go on from b", got)
	}
}
