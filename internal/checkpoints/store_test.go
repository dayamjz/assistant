package checkpoints_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/graph"
)

func TestAStoreAssignsSequencesFromOneAgainstTheAnchor(t *testing.T) {
	eachStore(t, func(t *testing.T, s graph.CheckpointStore) {
		ctx := context.Background()
		g := linearGraph(t, &recorder{})

		anchor := graph.CheckpointID{}
		for seq := 1; seq <= 3; seq++ {
			id, err := s.Write(ctx, anchor, graph.Checkpoint{
				Run: "run", Position: "a", Status: graph.StatusRunning, State: mustState(t, g, nil),
			})
			if err != nil {
				t.Fatalf("write %d: %v", seq, err)
			}
			if id != (graph.CheckpointID{Run: "run", Seq: seq}) {
				t.Fatalf("write %d was assigned %s, want run#%d", seq, id, seq)
			}
			anchor = id
		}

		history, err := s.History(ctx, "run")
		if err != nil {
			t.Fatalf("History: %v", err)
		}
		if len(history) != 3 {
			t.Fatalf("the run has %d checkpoints, want 3", len(history))
		}
		for i, cp := range history {
			if cp.ID() != (graph.CheckpointID{Run: "run", Seq: i + 1}) {
				t.Errorf("checkpoint %d identifies as %s", i+1, cp.ID())
			}
		}
		latest, err := s.Latest(ctx, "run")
		if err != nil {
			t.Fatalf("Latest: %v", err)
		}
		if latest.ID() != (graph.CheckpointID{Run: "run", Seq: 3}) {
			t.Errorf("Latest is %s, want run#3", latest.ID())
		}
	})
}

func TestAZeroAnchorIsRefusedOnceARunHasHistory(t *testing.T) {
	eachStore(t, func(t *testing.T, s graph.CheckpointStore) {
		ctx := context.Background()
		g := linearGraph(t, &recorder{})
		claim := graph.Checkpoint{
			Run: "run", Position: "a", Status: graph.StatusRunning, State: mustState(t, g, nil),
		}
		if _, err := s.Write(ctx, graph.CheckpointID{}, claim); err != nil {
			t.Fatalf("claiming a run with no history: %v", err)
		}

		_, err := s.Write(ctx, graph.CheckpointID{}, claim)
		if !errors.Is(err, graph.ErrRunExists) {
			t.Fatalf("claiming a run that already has history = %v, want ErrRunExists", err)
		}
		history, err := s.History(ctx, "run")
		if err != nil {
			t.Fatalf("History: %v", err)
		}
		if len(history) != 1 {
			t.Fatalf("the refused claim left %d checkpoints, want 1", len(history))
		}
	})
}

func TestAnAnchorTheRunHasLeftIsRefusedAndNamesWhereItStands(t *testing.T) {
	eachStore(t, func(t *testing.T, s graph.CheckpointStore) {
		ctx := context.Background()
		g := linearGraph(t, &recorder{})
		cp := graph.Checkpoint{
			Run: "run", Position: "a", Status: graph.StatusRunning, State: mustState(t, g, nil),
		}
		anchor := graph.CheckpointID{}
		for range 3 {
			id, err := s.Write(ctx, anchor, cp)
			if err != nil {
				t.Fatalf("Write: %v", err)
			}
			anchor = id
		}

		cases := map[string]graph.CheckpointID{
			"a position the run has moved past":  {Run: "run", Seq: 1},
			"a position the run has not reached": {Run: "run", Seq: 9},
			"the same position in another run":   {Run: "other", Seq: 3},
			// A negative sequence is no entry either store ever assigned, so
			// it is one more position the run does not stand at rather than a
			// refusal of its own that a caller checking for this one misses.
			"a sequence no entry can have": {Run: "run", Seq: -1},
		}
		for name, stale := range cases {
			t.Run(name, func(t *testing.T) {
				_, err := s.Write(ctx, stale, cp)
				if !errors.Is(err, graph.ErrStaleAnchor) {
					t.Fatalf("write anchored to %s = %v, want ErrStaleAnchor", stale, err)
				}
				// A caller that is told only that it was surprised cannot
				// decide again, so the refusal has to say where the run is.
				if !strings.Contains(err.Error(), stale.String()) {
					t.Errorf("the refusal does not name the anchor %s: %v", stale, err)
				}
				if !strings.Contains(err.Error(), "run#3") {
					t.Errorf("the refusal does not name where the run stands: %v", err)
				}
				history, err := s.History(ctx, "run")
				if err != nil {
					t.Fatalf("History: %v", err)
				}
				if len(history) != 3 {
					t.Errorf("the refused write left %d checkpoints, want 3", len(history))
				}
			})
		}

		// The guard is not refusing everything: the run's own tip is accepted,
		// which is what makes the refusals above evidence of a check rather
		// than of a store that cannot be written to.
		if _, err := s.Write(ctx, anchor, cp); err != nil {
			t.Fatalf("a write anchored to the run's own tip: %v, want it accepted", err)
		}
	})
}

func TestWriteRefusesACheckpointWithNoRun(t *testing.T) {
	eachStore(t, func(t *testing.T, s graph.CheckpointStore) {
		g := linearGraph(t, &recorder{})
		_, err := s.Write(context.Background(), graph.CheckpointID{}, graph.Checkpoint{
			Position: "a", Status: graph.StatusRunning, State: mustState(t, g, nil),
		})
		var ce *graph.CheckpointError
		if !errors.As(err, &ce) {
			t.Fatalf("writing a checkpoint with no run = %T %v, want a *graph.CheckpointError", err, err)
		}
	})
}

func TestAnEmptyRunHasNoLatestAndNoHistory(t *testing.T) {
	eachStore(t, func(t *testing.T, s graph.CheckpointStore) {
		ctx := context.Background()
		if _, err := s.Latest(ctx, "absent"); !errors.Is(err, graph.ErrNoSuchRun) {
			t.Errorf("Latest of an unknown run = %v, want ErrNoSuchRun", err)
		}
		history, err := s.History(ctx, "absent")
		if err != nil {
			t.Errorf("History of an unknown run = %v, want no error", err)
		}
		if len(history) != 0 {
			t.Errorf("History of an unknown run has %d entries", len(history))
		}
	})
}

func TestACheckpointReadsBackAsItWasWritten(t *testing.T) {
	eachStore(t, func(t *testing.T, s graph.CheckpointStore) {
		ctx := context.Background()
		g := everyKindGraph(t)
		origin := graph.CheckpointID{Run: "origin", Seq: 7}
		want := graph.Checkpoint{
			Run:      "run",
			Position: "only",
			Status:   graph.StatusHalted,
			Reason:   "waiting on the decision at only",
			State: mustState(t, g, map[string]graph.Value{
				"note":  graph.TextValue("a note"),
				"count": graph.IntValue(42),
				"flag":  graph.BoolValue(true),
				"items": graph.ListValue("one", "two"),
			}),
			Decision: &graph.Decision{
				Node:     "only",
				Question: "ship it?",
				Options:  []string{"approve", "cancel"},
				Into:     "note",
			},
			Counters: graph.Counters{
				Steps:        3,
				Budget:       9,
				Traversals:   []int{1, 0, 2},
				Fingerprints: []string{"one", "", "three"},
				EdgeDigest:   "a-digest",
			},
			ForkedFrom: &origin,
		}

		id, err := s.Write(ctx, graph.CheckpointID{}, want)
		if err != nil {
			t.Fatalf("Write: %v", err)
		}
		got, err := s.Latest(ctx, "run")
		if err != nil {
			t.Fatalf("Latest: %v", err)
		}
		if got.ID() != id {
			t.Errorf("the checkpoint reads back as %s, want the %s it was assigned", got.ID(), id)
		}
		if got.Position != want.Position || got.Status != want.Status || got.Reason != want.Reason {
			t.Errorf("it reads back at %q %s %q, want %q %s %q",
				got.Position, got.Status, got.Reason, want.Position, want.Status, want.Reason)
		}
		if !got.State.Equal(want.State) {
			t.Errorf("it reads back with state %v, want %v", got.State, want.State)
		}
		if !sameDecision(got.Decision, want.Decision) {
			t.Errorf("it reads back with decision %v, want %v", got.Decision, want.Decision)
		}
		if got.Counters.Steps != want.Counters.Steps || got.Counters.Budget != want.Counters.Budget ||
			got.Counters.EdgeDigest != want.Counters.EdgeDigest ||
			!equalInts(got.Counters.Traversals, want.Counters.Traversals) ||
			!equalStrings(got.Counters.Fingerprints, want.Counters.Fingerprints) {
			t.Errorf("it reads back with counters %+v, want %+v", got.Counters, want.Counters)
		}
		if got.ForkedFrom == nil || *got.ForkedFrom != origin {
			t.Errorf("it reads back with lineage %v, want %s", got.ForkedFrom, origin)
		}
	})
}

func TestACheckpointThatCannotBeEncodedIsRefusedAndWritesNothing(t *testing.T) {
	eachStore(t, func(t *testing.T, s graph.CheckpointStore) {
		ctx := context.Background()
		g := linearGraph(t, &recorder{})
		// StatusInvalid is the zero status and refuses to encode, which is
		// what a store must do with it rather than store a checkpoint nothing
		// can read back.
		_, err := s.Write(ctx, graph.CheckpointID{}, graph.Checkpoint{
			Run: "run", Position: "a", Status: graph.StatusInvalid, State: mustState(t, g, nil),
		})
		if err == nil {
			t.Fatal("a checkpoint with an unencodable status was accepted")
		}
		history, err := s.History(ctx, "run")
		if err != nil {
			t.Fatalf("History: %v", err)
		}
		if len(history) != 0 {
			t.Errorf("the refused write left %d checkpoints, want none", len(history))
		}
	})
}

func TestAnUnencodableCheckpointAgainstAStaleAnchorIsRefusedAndWritesNothing(t *testing.T) {
	eachStore(t, func(t *testing.T, s graph.CheckpointStore) {
		ctx := context.Background()
		g := linearGraph(t, &recorder{})
		if _, err := s.Write(ctx, graph.CheckpointID{}, graph.Checkpoint{
			Run: "run", Position: "a", Status: graph.StatusRunning, State: mustState(t, g, nil),
		}); err != nil {
			t.Fatalf("claiming the run: %v", err)
		}

		// Both things are wrong with this write at once. What the two stores
		// report about it is the one difference between them doc.go names; that
		// it is refused and leaves the run where it was is not a difference, and
		// is what this holds.
		_, err := s.Write(ctx, graph.CheckpointID{Run: "run", Seq: 9}, graph.Checkpoint{
			Run: "run", Position: "a", Status: graph.StatusInvalid, State: mustState(t, g, nil),
		})
		if err == nil {
			t.Fatal("a checkpoint that can neither be encoded nor anchored was accepted")
		}
		history, err := s.History(ctx, "run")
		if err != nil {
			t.Fatalf("History: %v", err)
		}
		if len(history) != 1 {
			t.Errorf("the refused write left %d checkpoints, want 1", len(history))
		}
	})
}

// The payload has to exist before the accessor that decides the anchor is
// called, so this store finds an encoding failure where graph.MemoryStore, which
// decides the anchor first, finds a stale one. doc.go names that difference, and
// this is what holds it where it is named rather than letting it widen or
// disappear unnoticed. A store that started reading the run's tip to answer the
// anchor sooner would be deciding it outside the write, which is the defect the
// anchor exists to prevent.
func TestTheDurableStoreFindsAnEncodingFailureWhereTheInMemoryOneFindsTheAnchor(t *testing.T) {
	ctx := context.Background()
	g := linearGraph(t, &recorder{})
	claim := graph.Checkpoint{
		Run: "run", Position: "a", Status: graph.StatusRunning, State: mustState(t, g, nil),
	}
	unencodable := graph.Checkpoint{
		Run: "run", Position: "a", Status: graph.StatusInvalid, State: mustState(t, g, nil),
	}
	stale := graph.CheckpointID{Run: "run", Seq: 9}

	memory := graph.NewMemoryStore()
	if _, err := memory.Write(ctx, graph.CheckpointID{}, claim); err != nil {
		t.Fatalf("claiming the run in memory: %v", err)
	}
	if _, err := memory.Write(ctx, stale, unencodable); !errors.Is(err, graph.ErrStaleAnchor) {
		t.Errorf("the in-memory store = %v, want ErrStaleAnchor", err)
	}

	durable := durableStore(t)
	if _, err := durable.Write(ctx, graph.CheckpointID{}, claim); err != nil {
		t.Fatalf("claiming the run durably: %v", err)
	}
	switch _, err := durable.Write(ctx, stale, unencodable); {
	case err == nil:
		t.Error("the durable store accepted a checkpoint it cannot encode")
	case errors.Is(err, graph.ErrStaleAnchor):
		t.Errorf("the durable store = %v, want the encoding failure it reaches first", err)
	case !strings.Contains(err.Error(), "encoding checkpoint"):
		t.Errorf("the durable store = %v, want it to say what it could not encode", err)
	}
}

// internal/store refuses a blank name everywhere it takes one, and nothing here
// relaxes that, so a run graph.MemoryStore is willing to hold under a name that
// is only whitespace has no durable history. graph.Executor admits the name, so
// this is reachable rather than theoretical; doc.go names it, and this is what
// holds it to a refusal that writes nothing rather than a partly claimed run.
func TestTheDurableStoreRefusesARunNamedOnlyWhitespace(t *testing.T) {
	ctx := context.Background()
	g := linearGraph(t, &recorder{})
	blank := " "
	cp := graph.Checkpoint{
		Run: blank, Position: "a", Status: graph.StatusRunning, State: mustState(t, g, nil),
	}

	if _, err := graph.NewMemoryStore().Write(ctx, graph.CheckpointID{}, cp); err != nil {
		t.Fatalf("the in-memory store refused a run named %q: %v", blank, err)
	}

	durable := durableStore(t)
	if _, err := durable.Write(ctx, graph.CheckpointID{}, cp); err == nil {
		t.Fatal("the durable store accepted a run named only whitespace")
	}
	history, err := durable.History(ctx, blank)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(history) != 0 {
		t.Errorf("the refused write left %d checkpoints, want none", len(history))
	}
}

func TestForkCopiesThePrefixAndRecordsItsLineage(t *testing.T) {
	eachStore(t, func(t *testing.T, s graph.CheckpointStore) {
		ctx := context.Background()
		rec := &recorder{}
		g := linearGraph(t, rec)
		exec := mustExecutor(t, g, s, 20)
		if _, err := exec.Run(ctx, "run", mustState(t, g, nil)); err != nil {
			t.Fatalf("Run: %v", err)
		}
		before, err := s.History(ctx, "run")
		if err != nil {
			t.Fatalf("History: %v", err)
		}
		if len(before) != 4 {
			t.Fatalf("the run has %d checkpoints, want 4", len(before))
		}

		id, err := s.Fork(ctx, graph.CheckpointID{Run: "run", Seq: 2}, "retry")
		if err != nil {
			t.Fatalf("Fork: %v", err)
		}
		if id != (graph.CheckpointID{Run: "retry", Seq: 2}) {
			t.Errorf("Fork returned %s, want retry#2", id)
		}

		forked, err := s.History(ctx, "retry")
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
			if cp.Position != before[i].Position || !cp.State.Equal(before[i].State) {
				t.Errorf("forked checkpoint %d is not the one it copied", i)
			}
			want := graph.CheckpointID{Run: "run", Seq: i + 1}
			if cp.ForkedFrom == nil || *cp.ForkedFrom != want {
				t.Errorf("forked checkpoint %d records lineage %v, want %s", i, cp.ForkedFrom, want)
			}
			if err := g.Validate(cp); err != nil {
				t.Errorf("forked checkpoint %d does not validate against the graph: %v", i, err)
			}
		}

		after, err := s.History(ctx, "run")
		if err != nil {
			t.Fatalf("History: %v", err)
		}
		if len(after) != len(before) {
			t.Fatalf("forking changed the source from %d to %d checkpoints", len(before), len(after))
		}
		for i := range before {
			if after[i].ForkedFrom != nil {
				t.Errorf("the source checkpoint %d was marked as forked", i+1)
			}
		}

		// The fork is a run, so it resumes and finishes the work its source
		// had left.
		result, err := exec.Resume(ctx, "retry")
		if err != nil {
			t.Fatalf("Resume the fork: %v", err)
		}
		if result.Status != graph.StatusCompleted {
			t.Fatalf("the fork ended %s, want completed", result.Status)
		}
		if got := rec.order(); !equalStrings(got, []string{"a", "b", "c", "b", "c"}) {
			t.Errorf("bodies ran %v, want the fork to resume from b", got)
		}
	})
}

func TestForkRefusesAPointOrDestinationItCannotHonour(t *testing.T) {
	eachStore(t, func(t *testing.T, s graph.CheckpointStore) {
		ctx := context.Background()
		g := linearGraph(t, &recorder{})
		exec := mustExecutor(t, g, s, 20)
		if _, err := exec.Run(ctx, "run", mustState(t, g, nil)); err != nil {
			t.Fatalf("Run: %v", err)
		}

		if _, err := s.Fork(ctx, graph.CheckpointID{Run: "run", Seq: 99}, "retry"); !errors.Is(err, graph.ErrNoSuchCheckpoint) {
			t.Errorf("forking past the end of a run = %v, want ErrNoSuchCheckpoint", err)
		}
		if _, err := s.Fork(ctx, graph.CheckpointID{Run: "absent", Seq: 1}, "retry"); !errors.Is(err, graph.ErrNoSuchCheckpoint) {
			t.Errorf("forking an unknown run = %v, want ErrNoSuchCheckpoint", err)
		}
		if _, err := s.Fork(ctx, graph.CheckpointID{Run: "run", Seq: 0}, "retry"); !errors.Is(err, graph.ErrNoSuchCheckpoint) {
			t.Errorf("forking from sequence zero = %v, want ErrNoSuchCheckpoint", err)
		}
		if _, err := s.Fork(ctx, graph.CheckpointID{Run: "run", Seq: 1}, "run"); !errors.Is(err, graph.ErrRunExists) {
			t.Errorf("forking a run onto itself = %v, want ErrRunExists", err)
		}
		var ce *graph.CheckpointError
		if _, err := s.Fork(ctx, graph.CheckpointID{Run: "run", Seq: 1}, ""); !errors.As(err, &ce) {
			t.Errorf("forking into an unnamed run = %T %v, want a *graph.CheckpointError", err, err)
		}
		if history, err := s.History(ctx, "retry"); err != nil || len(history) != 0 {
			t.Fatalf("a refused fork wrote %d checkpoints, %v", len(history), err)
		}

		if _, err := s.Fork(ctx, graph.CheckpointID{Run: "run", Seq: 1}, "retry"); err != nil {
			t.Fatalf("Fork: %v", err)
		}
		if _, err := s.Fork(ctx, graph.CheckpointID{Run: "run", Seq: 2}, "retry"); !errors.Is(err, graph.ErrRunExists) {
			t.Errorf("forking onto a run that already has history = %v, want ErrRunExists", err)
		}
		if history, err := s.History(ctx, "retry"); err != nil || len(history) != 1 {
			t.Errorf("the refused fork changed the destination: %d checkpoints, %v", len(history), err)
		}
	})
}

func TestARunWalkedByTheExecutorLandsInTheSamePlace(t *testing.T) {
	// The whole of a run over the fixture that moves state, the decision and
	// the counters, checked against both implementations. A store that lost
	// any part of a checkpoint would either fail to resume or resume somewhere
	// else, and either one shows up as a difference here.
	eachStore(t, func(t *testing.T, s graph.CheckpointStore) {
		ctx := context.Background()
		rec := &recorder{}
		g := fixLoopGraph(t, rec)
		exec := mustExecutor(t, g, s, 20)

		held, err := exec.Run(ctx, "run", mustState(t, g, nil))
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if held.Status != graph.StatusHalted || held.Position != "gate" {
			t.Fatalf("the run ended %s at %q, want halted at gate", held.Status, held.Position)
		}
		if held.Decision == nil || held.Decision.Question != "apply the fix?" {
			t.Fatalf("the halted run re-emits %v, want the gate's question", held.Decision)
		}

		done, err := exec.Answer(ctx, "run", "fix")
		if err != nil {
			t.Fatalf("Answer: %v", err)
		}
		if done.Status != graph.StatusCompleted {
			t.Fatalf("the answered run ended %s, want completed", done.Status)
		}
		if got := list(t, done.State, "trace"); !equalStrings(got,
			[]string{"review", "gate", "fix", "review", "done"}) {
			t.Errorf("the run traced %v", got)
		}
		if got := rec.order(); !equalStrings(got,
			[]string{"review", "gate", "fix", "review", "done"}) {
			t.Errorf("bodies ran %v", got)
		}

		history, err := s.History(ctx, "run")
		if err != nil {
			t.Fatalf("History: %v", err)
		}
		want := []string{"review", "gate", "gate", "fix", "review", "done", ""}
		if got := positions(history); !equalStrings(got, want) {
			t.Fatalf("the run's checkpoints stand at %v, want %v", got, want)
		}
		// The bound accounting travelled with the run rather than restarting
		// when the answer resumed it.
		last := history[len(history)-1]
		if last.Counters.Steps != 5 || last.Counters.Budget != 20 {
			t.Errorf("the run ends having spent %d of %d steps, want 5 of 20",
				last.Counters.Steps, last.Counters.Budget)
		}
		if last.Counters.EdgeDigest == "" {
			t.Error("the run's counters name no edge vector")
		}
		back := len(g.Edges()) - 1
		if last.Counters.Traversals[back] != 1 {
			t.Errorf("the back edge records %d traversals, want 1", last.Counters.Traversals[back])
		}
		if last.Counters.Fingerprints[back] == "" {
			t.Error("the back edge records no state fingerprint")
		}
	})
}

func TestConcurrentRunsUnderOneNameClaimItExactlyOnce(t *testing.T) {
	eachStore(t, func(t *testing.T, s graph.CheckpointStore) {
		ctx := context.Background()
		rec := &recorder{}
		g := linearGraph(t, rec)
		exec := mustExecutor(t, g, s, 20)

		const attempts = 8
		states := make([]graph.State, attempts)
		for i := range states {
			states[i] = mustState(t, g, nil)
		}
		errs := make([]error, attempts)
		release := make(chan struct{})
		var wg sync.WaitGroup
		for i := range attempts {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-release
				_, errs[i] = exec.Run(ctx, "run", states[i])
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
			t.Fatalf("%d of %d concurrent runs started, want exactly 1", started, attempts)
		}
		history, err := s.History(ctx, "run")
		if err != nil {
			t.Fatalf("History: %v", err)
		}
		want := []string{"a", "b", "c", ""}
		if got := positions(history); !equalStrings(got, want) {
			t.Fatalf("the run's checkpoints stand at %v, want %v: the losing calls wrote into it", got, want)
		}
		if got := rec.order(); !equalStrings(got, []string{"a", "b", "c"}) {
			t.Errorf("bodies ran %v, want one pass: a refused run must execute nothing", got)
		}
	})
}

// racingStore holds a run's latest checkpoint from every reader until a fixed
// number of them have asked, so two callers are guaranteed to decide against
// the same tip and then race to write it. It is a scheduler rather than a
// double: the store underneath does all the deciding, and holding a read
// changes when the writes arrive and nothing about what they are.
//
// The barrier only applies once arm is called, so the sequential setup a test
// does first is not held.
type racingStore struct {
	inner   graph.CheckpointStore
	readers int

	mu      sync.Mutex
	armed   bool
	arrived int
	ready   chan struct{}
}

func newRacingStore(readers int, inner graph.CheckpointStore) *racingStore {
	return &racingStore{inner: inner, readers: readers, ready: make(chan struct{})}
}

func (s *racingStore) arm() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.armed = true
}

func (s *racingStore) Write(ctx context.Context, anchor graph.CheckpointID, c graph.Checkpoint) (graph.CheckpointID, error) {
	return s.inner.Write(ctx, anchor, c)
}

func (s *racingStore) Latest(ctx context.Context, run string) (graph.Checkpoint, error) {
	cp, err := s.inner.Latest(ctx, run)
	s.mu.Lock()
	hold := s.armed && s.arrived < s.readers
	if hold {
		s.arrived++
		if s.arrived == s.readers {
			close(s.ready)
		}
	}
	s.mu.Unlock()
	if hold {
		<-s.ready
	}
	return cp, err
}

func (s *racingStore) History(ctx context.Context, run string) ([]graph.Checkpoint, error) {
	return s.inner.History(ctx, run)
}

func (s *racingStore) Fork(ctx context.Context, from graph.CheckpointID, into string) (graph.CheckpointID, error) {
	return s.inner.Fork(ctx, from, into)
}

func TestConcurrentAnswersToOneRunDoNotInterleave(t *testing.T) {
	eachStore(t, func(t *testing.T, inner graph.CheckpointStore) {
		ctx := context.Background()
		rec := &recorder{}
		g := fixLoopGraph(t, rec)
		s := newRacingStore(2, inner)
		exec := mustExecutor(t, g, s, 20)

		held, err := exec.Run(ctx, "run", mustState(t, g, nil))
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if held.Status != graph.StatusHalted {
			t.Fatalf("the run ended %s, want halted at its decision", held.Status)
		}
		before, err := s.History(ctx, "run")
		if err != nil {
			t.Fatalf("History: %v", err)
		}
		s.arm()

		const answers = 2
		errs := make([]error, answers)
		var wg sync.WaitGroup
		for i := range answers {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, errs[i] = exec.Answer(ctx, "run", "fix")
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
		// The refusal lands before the node runs, not after it: what a node
		// body does is the caller's business and may not be repeatable.
		if n := rec.count("gate"); n != 1 {
			t.Fatalf("the halt point's body ran %d times, want 1", n)
		}

		after, err := s.History(ctx, "run")
		if err != nil {
			t.Fatalf("History: %v", err)
		}
		want := append(positions(before), "gate", "fix", "review", "done", "")
		if got := positions(after); !equalStrings(got, want) {
			t.Fatalf("the run's checkpoints stand at %v, want %v: the answers interleaved", got, want)
		}
	})
}

func TestManyRunsAdvanceConcurrentlyWithoutCrossingHistories(t *testing.T) {
	eachStore(t, func(t *testing.T, s graph.CheckpointStore) {
		ctx := context.Background()
		rec := &recorder{}
		g := linearGraph(t, rec)
		exec := mustExecutor(t, g, s, 20)

		const runs = 8
		states := make([]graph.State, runs)
		for i := range states {
			states[i] = mustState(t, g, nil)
		}
		errs := make([]error, runs)
		var wg sync.WaitGroup
		for i := range runs {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				name := fmt.Sprintf("run-%d", i)
				if _, err := exec.Run(ctx, name, states[i]); err != nil {
					errs[i] = err
					return
				}
				if _, err := s.Fork(ctx, graph.CheckpointID{Run: name, Seq: 2}, name+"-retry"); err != nil {
					errs[i] = err
					return
				}
				_, errs[i] = exec.Resume(ctx, name+"-retry")
			}(i)
		}
		wg.Wait()

		for i, err := range errs {
			if err != nil {
				t.Fatalf("run %d: %v", i, err)
			}
		}
		for i := range runs {
			name := fmt.Sprintf("run-%d", i)
			if got, err := s.History(ctx, name); err != nil || len(got) != 4 {
				t.Errorf("%s has %d checkpoints, want 4: %v", name, len(got), err)
			}
			// Two copied, then the claim the resuming segment wrote before it
			// ran anything, then one for each of the two nodes it had left.
			if got, err := s.History(ctx, name+"-retry"); err != nil || len(got) != 5 {
				t.Errorf("%s-retry has %d checkpoints, want 5: %v", name, len(got), err)
			}
		}
	})
}
