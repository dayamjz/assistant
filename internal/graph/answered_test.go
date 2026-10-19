package graph_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/dayamjz/assistant/internal/graph"
)

// answeredLoop is a cycle a person drives: work runs, hold asks, an "again"
// answer goes through redo back to work, and anything else finishes. No body
// changes state, so every round is one convergence would park on. answered
// marks the back edge from redo to work.
func answeredLoop(rec *recorder, answered bool) *graph.Builder {
	return graph.NewBuilder().
		Start("work").
		Key(graph.Key{Name: "answer", Kind: graph.KindText}).
		Node(graph.Node{Name: "work", NewBody: noteOnly(rec, "work")}).
		Node(graph.Node{Name: "hold", NewBody: noteOnly(rec, "hold"), Reads: []string{"answer"},
			Halt: &graph.Halt{Question: "again?", Options: []string{"again", "done"}, Into: "answer"}}).
		Node(graph.Node{Name: "redo", NewBody: noteOnly(rec, "redo")}).
		Node(graph.Node{Name: "done", NewBody: noteOnly(rec, "done")}).
		Edge(graph.Edge{From: "work", To: "hold"}).
		Edge(graph.Edge{From: "hold", To: "redo", Rounds: 3,
			Guard: &graph.Guard{Key: "answer", Op: graph.OpEquals, Value: graph.TextValue("again")}}).
		Edge(graph.Edge{From: "hold", To: "done"}).
		Edge(graph.Edge{From: "redo", To: "work", Rounds: 3, Answered: answered})
}

// A back edge marked Answered is exempt from convergence: a round there that
// changed nothing returns to the halt point instead of parking. Its round
// limit still holds. The same graph without the mark parks on the second
// round, which is the positive control that the rounds really change nothing.
func TestAnAnsweredBackEdgeIsExemptFromConvergenceAndNothingElse(t *testing.T) {
	ctx := context.Background()

	rec := &recorder{}
	g := mustBuild(t, answeredLoop(rec, true))
	exec := mustExecutor(t, g, graph.NewMemoryStore(), 100)
	if _, err := exec.Run(ctx, "run", mustState(t, g, nil)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for i := range 3 {
		got, err := exec.Answer(ctx, "run", "again")
		if err != nil {
			t.Fatalf("answer %d: %v", i+1, err)
		}
		if got.Status != graph.StatusHalted || got.Position != "hold" {
			t.Fatalf("after answered round %d the run is %s at %q (%s), want halted at the hold",
				i+1, got.Status, got.Position, got.Reason)
		}
	}
	// The round limit is untouched by the exemption, and the answer past it
	// is refused with the decision still open: the same run then takes a
	// different answer.
	if _, err := exec.Answer(ctx, "run", "again"); !errors.Is(err, graph.ErrRoundsSpent) {
		t.Fatalf("a fourth round past a bound of three came to %v, want graph.ErrRoundsSpent", err)
	}
	done, err := exec.Answer(ctx, "run", "done")
	if err != nil || done.Status != graph.StatusCompleted {
		t.Fatalf("finishing the same run after the refused round came to %v, %v", done.Status, err)
	}

	control := &recorder{}
	cg := mustBuild(t, answeredLoop(control, false))
	cexec := mustExecutor(t, cg, graph.NewMemoryStore(), 100)
	if _, err := cexec.Run(ctx, "run", mustState(t, cg, nil)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got, err := cexec.Answer(ctx, "run", "again"); err != nil || got.Status != graph.StatusHalted {
		t.Fatalf("the unmarked graph's first round came to %v, %v", got.Status, err)
	}
	got, err := cexec.Answer(ctx, "run", "again")
	if err != nil {
		t.Fatalf("the unmarked graph's second round: %v", err)
	}
	if got.Status != graph.StatusConverged {
		t.Fatalf("the unmarked graph's second no-op round came to %s, want converged", got.Status)
	}
}

// The mark is admitted only where every traversal of the edge follows an
// answer at a halt point, so it cannot be used to switch convergence off for
// a loop nobody drives. answeredLoop with the mark builds, which is the
// control for the refusals.
func TestTheBuildAdmitsAnsweredOnlyAfterAnAnswer(t *testing.T) {
	rec := &recorder{}
	mustBuild(t, answeredLoop(rec, true))

	for name, b := range map[string]*graph.Builder{
		"a forward edge": graph.NewBuilder().Start("a").
			Node(graph.Node{Name: "a", NewBody: noteOnly(rec, "a")}).
			Node(graph.Node{Name: "b", NewBody: noteOnly(rec, "b")}).
			Edge(graph.Edge{From: "a", To: "b", Answered: true}),
		"a loop on the start node": graph.NewBuilder().Start("a").
			Key(graph.Key{Name: "n", Kind: graph.KindInt}).
			Node(graph.Node{Name: "a", Reads: []string{"n"}, NewBody: noteOnly(rec, "a")}).
			Node(graph.Node{Name: "b", NewBody: noteOnly(rec, "b")}).
			Edge(graph.Edge{From: "a", To: "a", Rounds: 2, Answered: true,
				Guard: &graph.Guard{Key: "n", Op: graph.OpEquals, Value: graph.IntValue(0)}}).
			Edge(graph.Edge{From: "a", To: "b"}),
		"a loop entered from no halt point": graph.NewBuilder().Start("a").
			Node(graph.Node{Name: "a", NewBody: noteOnly(rec, "a")}).
			Node(graph.Node{Name: "b", NewBody: noteOnly(rec, "b")}).
			Edge(graph.Edge{From: "a", To: "b"}).
			Edge(graph.Edge{From: "b", To: "a", Rounds: 2, Answered: true}),
		// The way in is guarded, but on a key other than the halt point's
		// answer, so the guard says nothing about what was answered.
		"a way in guarded on another key": graph.NewBuilder().Start("work").
			Key(graph.Key{Name: "answer", Kind: graph.KindText}).
			Key(graph.Key{Name: "other", Kind: graph.KindText}).
			Node(graph.Node{Name: "work", NewBody: noteOnly(rec, "work")}).
			Node(graph.Node{Name: "hold", NewBody: noteOnly(rec, "hold"), Reads: []string{"answer"},
				Halt: &graph.Halt{Question: "again?", Options: []string{"again", "done"}, Into: "answer"}}).
			Node(graph.Node{Name: "redo", NewBody: noteOnly(rec, "redo")}).
			Node(graph.Node{Name: "done", NewBody: noteOnly(rec, "done")}).
			Edge(graph.Edge{From: "work", To: "hold"}).
			Edge(graph.Edge{From: "hold", To: "redo", Rounds: 3,
				Guard: &graph.Guard{Key: "other", Op: graph.OpEquals, Value: graph.TextValue("again")}}).
			Edge(graph.Edge{From: "hold", To: "done"}).
			Edge(graph.Edge{From: "redo", To: "work", Rounds: 3, Answered: true}),
		// One way in is the valid one and a second comes from a node that is
		// no halt point, so the check has to look at every way in rather
		// than the first.
		"a second way in from no halt point": graph.NewBuilder().Start("work").
			Key(graph.Key{Name: "answer", Kind: graph.KindText}).
			Node(graph.Node{Name: "work", NewBody: noteOnly(rec, "work")}).
			Node(graph.Node{Name: "hold", NewBody: noteOnly(rec, "hold"), Reads: []string{"answer"},
				Halt: &graph.Halt{Question: "again?", Options: []string{"again", "side", "done"}, Into: "answer"}}).
			Node(graph.Node{Name: "side", NewBody: noteOnly(rec, "side")}).
			Node(graph.Node{Name: "redo", NewBody: noteOnly(rec, "redo")}).
			Node(graph.Node{Name: "done", NewBody: noteOnly(rec, "done")}).
			Edge(graph.Edge{From: "work", To: "hold"}).
			Edge(graph.Edge{From: "hold", To: "redo", Rounds: 3,
				Guard: &graph.Guard{Key: "answer", Op: graph.OpEquals, Value: graph.TextValue("again")}}).
			Edge(graph.Edge{From: "hold", To: "side",
				Guard: &graph.Guard{Key: "answer", Op: graph.OpEquals, Value: graph.TextValue("side")}}).
			Edge(graph.Edge{From: "hold", To: "done"}).
			Edge(graph.Edge{From: "side", To: "redo"}).
			Edge(graph.Edge{From: "redo", To: "work", Rounds: 3, Answered: true}),
		"a halt point's unguarded way in": graph.NewBuilder().Start("work").
			Key(graph.Key{Name: "answer", Kind: graph.KindText}).
			Node(graph.Node{Name: "work", NewBody: noteOnly(rec, "work")}).
			Node(graph.Node{Name: "hold", NewBody: noteOnly(rec, "hold"),
				Halt: &graph.Halt{Question: "?", Into: "answer"}}).
			Node(graph.Node{Name: "redo", NewBody: noteOnly(rec, "redo")}).
			Edge(graph.Edge{From: "work", To: "hold"}).
			Edge(graph.Edge{From: "hold", To: "redo"}).
			Edge(graph.Edge{From: "redo", To: "work", Rounds: 2, Answered: true}),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := b.Build()
			requireBuildError(t, err, graph.RuleBoundedCycle)
		})
	}
}

// haltedAt runs g on store to its first halt and returns the executor.
func haltedAt(t *testing.T, g *graph.Graph, store graph.CheckpointStore) *graph.Executor {
	t.Helper()
	exec := mustExecutor(t, g, store, 100)
	got, err := exec.Run(context.Background(), "run", mustState(t, g, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Status != graph.StatusHalted {
		t.Fatalf("the run is %s, want halted", got.Status)
	}
	return exec
}

// latest is the run's latest checkpoint in store.
func latest(t *testing.T, store graph.CheckpointStore) graph.Checkpoint {
	t.Helper()
	cp, err := store.Latest(context.Background(), "run")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	return cp
}

// The mark is part of the edge digest in both directions, because it decides
// whether a fingerprint is compared at all: a run recorded under the marked
// loop is refused by the unmarked one and the other way round, with the
// refusal the digest already gives for edges that differ.
func TestTheAnsweredMarkIsPartOfTheEdgeDigest(t *testing.T) {
	for name, tc := range map[string]struct{ recorded, resumed bool }{
		"marked then unmarked": {true, false},
		"unmarked then marked": {false, true},
	} {
		t.Run(name, func(t *testing.T) {
			rec := &recorder{}
			store := graph.NewMemoryStore()
			haltedAt(t, mustBuild(t, answeredLoop(rec, tc.recorded)), store)
			other := mustExecutor(t, mustBuild(t, answeredLoop(rec, tc.resumed)), store, 100)
			_, err := other.Answer(context.Background(), "run", "again")
			var ce *graph.CheckpointError
			if !errors.As(err, &ce) || ce.Field != "counters.edge_digest" {
				t.Fatalf("answering under the other marking came to %v, want the edge digest refused", err)
			}
		})
	}
}

// The digest of a graph that marks no edge is the one the same edges had
// before Edge.Answered existed, so runs recorded before it resume. The
// unmarked literal was taken from the build before the field was added, over
// these same edges; the marked one pins that the mark changes it.
func TestTheEdgeDigestOfAnUnmarkedGraphIsUnchanged(t *testing.T) {
	const (
		unmarked = "2976e4953d07afaae89f12c7ba0537c96ce698fcd53025c997360b63a42fb184"
		marked   = "ef6fdafddaa8d5e8e3c26646d47eb73ac1dafadab39365bafcae48ffbaffcabd"
	)
	for _, tc := range []struct {
		answered bool
		want     string
	}{{false, unmarked}, {true, marked}} {
		rec := &recorder{}
		store := graph.NewMemoryStore()
		haltedAt(t, mustBuild(t, answeredLoop(rec, tc.answered)), store)
		if got := latest(t, store).Counters.EdgeDigest; got != tc.want {
			t.Errorf("answered=%v: the edge digest is %s, want %s", tc.answered, got, tc.want)
		}
	}
}

// The exemption and the counts it sits beside survive a restart: an
// executor built afresh over the same store carries the run on, a no-op
// round there still returns to the hold, and the round bound still counts the
// round taken before the restart.
func TestAnAnsweredLoopContinuesUnderAFreshExecutor(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	store := graph.NewMemoryStore()
	first := haltedAt(t, mustBuild(t, answeredLoop(rec, true)), store)
	if got, err := first.Answer(ctx, "run", "again"); err != nil || got.Status != graph.StatusHalted {
		t.Fatalf("the first round came to %v, %v", got.Status, err)
	}

	fresh := mustExecutor(t, mustBuild(t, answeredLoop(rec, true)), store, 100)
	for i := range 2 {
		got, err := fresh.Answer(ctx, "run", "again")
		if err != nil || got.Status != graph.StatusHalted || got.Position != "hold" {
			t.Fatalf("round %d after the restart came to %v at %q (%s), %v", i+2, got.Status, got.Position, got.Reason, err)
		}
	}
	if _, err := fresh.Answer(ctx, "run", "again"); !errors.Is(err, graph.ErrRoundsSpent) {
		t.Fatalf("a fourth round across the restart came to %v, want graph.ErrRoundsSpent", err)
	}
	if got, err := fresh.Answer(ctx, "run", "done"); err != nil || got.Status != graph.StatusCompleted {
		t.Fatalf("finishing came to %v, %v", got.Status, err)
	}
}

// AnswerWith refuses a detail the halt point does not take with that answer,
// and an answer that needs one arriving without it, and in both cases writes
// nothing: the latest checkpoint is exactly what it was. The accepted answer
// at the end is the control that the refusals were about the detail.
func TestAnAnswerWithAMisplacedDetailWritesNothing(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	g := mustBuild(t, graph.NewBuilder().
		Start("work").
		Key(graph.Key{Name: "answer", Kind: graph.KindText}).
		Key(graph.Key{Name: "detail", Kind: graph.KindText}).
		Node(graph.Node{Name: "work", NewBody: noteOnly(rec, "work")}).
		Node(graph.Node{Name: "hold", NewBody: noteOnly(rec, "hold"),
			Halt: &graph.Halt{Question: "how?", Options: []string{"fix", "done"}, Into: "answer",
				Detail: "detail", Detailed: []string{"fix"}}}).
		Node(graph.Node{Name: "done", NewBody: noteOnly(rec, "done")}).
		Edge(graph.Edge{From: "work", To: "hold"}).
		Edge(graph.Edge{From: "hold", To: "done"}))
	store := graph.NewMemoryStore()
	exec := haltedAt(t, g, store)
	before := latest(t, store)

	for name, tc := range map[string]struct {
		answer, detail string
		want           error
	}{
		"a detail with an answer that takes none":   {"done", "and this", graph.ErrDetailNotAccepted},
		"an answer that needs a detail without one": {"fix", "", graph.ErrDetailRequired},
	} {
		if _, err := exec.AnswerWith(ctx, "run", tc.answer, tc.detail); !errors.Is(err, tc.want) {
			t.Fatalf("%s came to %v, want %v", name, err, tc.want)
		}
		if after := latest(t, store); !reflect.DeepEqual(after, before) {
			t.Fatalf("%s changed the checkpoint:\nbefore %+v\nafter  %+v", name, before, after)
		}
	}
	if got, err := exec.AnswerWith(ctx, "run", "fix", "the detail"); err != nil || got.Status != graph.StatusCompleted {
		t.Fatalf("a well-formed answer came to %v, %v", got.Status, err)
	}
}

// An answer the caller declares nobody attended does not carry the Answered
// exemption: on answeredLoop, whose rounds change nothing, the second
// unattended answer parks the run converged as it leaves the hold, before
// the round it asks for runs, with the run's round limit untouched, and the
// parked run still reads back. The positive control is the same run shape answered by a
// person, which TestAnAnsweredBackEdgeIsExemptFromConvergenceAndNothingElse
// already walks three rounds without parking, and again here: a run that
// alternates is measured against the round before it whoever asked for that
// one, so an unattended round after a person's that changed nothing parks
// too, while the person's own round never does.
func TestAnUnattendedAnswerIsHeldToConvergenceOnAnAnsweredEdge(t *testing.T) {
	ctx := context.Background()

	start := func(t *testing.T) *graph.Executor {
		t.Helper()
		g := mustBuild(t, answeredLoop(&recorder{}, true))
		exec := mustExecutor(t, g, graph.NewMemoryStore(), 100)
		if _, err := exec.Run(ctx, "run", mustState(t, g, nil)); err != nil {
			t.Fatalf("Run: %v", err)
		}
		return exec
	}

	t.Run("two unattended rounds", func(t *testing.T) {
		exec := start(t)
		var claims []graph.CheckpointID
		claimed := func(id graph.CheckpointID) { claims = append(claims, id) }
		first, err := exec.AnswerUnattended(ctx, "run", "again", "", claimed)
		if err != nil || first.Status != graph.StatusHalted {
			t.Fatalf("the first unattended round came to %v, %v; want it to return to the hold", first.Status, err)
		}
		second, err := exec.AnswerUnattended(ctx, "run", "again", "", claimed)
		if err != nil {
			t.Fatalf("the second unattended round: %v", err)
		}
		if second.Status != graph.StatusConverged {
			t.Fatalf("the second unattended no-op round came to %s at %q, want converged", second.Status, second.Position)
		}
		park, ok := second.Park()
		if !ok || park.Edge == nil || park.Edge.From != "hold" || park.Edge.To != "redo" {
			t.Fatalf("the park names %+v, want the edge the answer took out of the hold", park)
		}
		// A park on an edge only an unattended answer holds to convergence
		// is one validation accepts, so the run reads back where it parked
		// rather than being refused as a checkpoint no run could produce.
		again, err := exec.Resume(ctx, "run")
		if err != nil || again.Status != graph.StatusConverged {
			t.Fatalf("resuming the parked run came to %v, %v; want it read back converged", again.Status, err)
		}
		if len(claims) != 2 || claims[0].Seq == 0 || claims[1].Seq <= claims[0].Seq {
			t.Fatalf("the claim was reported as %v, want once per answer, in order", claims)
		}
	})

	t.Run("a person's round then an unattended one", func(t *testing.T) {
		exec := start(t)
		if got, err := exec.Answer(ctx, "run", "again"); err != nil || got.Status != graph.StatusHalted {
			t.Fatalf("the person's round came to %v, %v", got.Status, err)
		}
		got, err := exec.AnswerUnattended(ctx, "run", "again", "", nil)
		if err != nil {
			t.Fatalf("the unattended round: %v", err)
		}
		if got.Status != graph.StatusConverged {
			t.Fatalf("an unattended round that changed nothing since the person's came to %s, want converged", got.Status)
		}
	})

	t.Run("an unattended round then a person's", func(t *testing.T) {
		exec := start(t)
		if got, err := exec.AnswerUnattended(ctx, "run", "again", "", nil); err != nil || got.Status != graph.StatusHalted {
			t.Fatalf("the unattended round came to %v, %v", got.Status, err)
		}
		// The unattended fact lapsed at the hold, so the person's round
		// keeps the exemption.
		for i := range 2 {
			got, err := exec.Answer(ctx, "run", "again")
			if err != nil || got.Status != graph.StatusHalted {
				t.Fatalf("the person's round %d came to %v, %v; want it to return to the hold", i+1, got.Status, err)
			}
		}
	})

	t.Run("a refused answer reports no claim", func(t *testing.T) {
		exec := start(t)
		called := false
		if _, err := exec.AnswerUnattended(ctx, "run", "never offered", "", func(graph.CheckpointID) { called = true }); !errors.Is(err, graph.ErrAnswerNotAllowed) {
			t.Fatalf("an answer the halt point does not offer came to %v, want graph.ErrAnswerNotAllowed", err)
		}
		if called {
			t.Fatal("a refused answer reported a claim")
		}
	})
}

// reportingLoop is answeredLoop's shape with the one thing the requested
// round in internal/pipeline has that answeredLoop lacks: the node a round
// returns to reports differently once any round has run, because a round
// leaves something behind for it, the way a requested round leaves the hold
// that has the stage hold again. So the report the first round reads is the
// one report no round left anything in, and a back edge closing a round, which
// is taken before that node re-runs, sees it differ between the first two
// rounds even when no round changes anything.
//
// edits says the round changes the state too: it counts itself, so no two
// rounds leave the same state.
func reportingLoop(rec *recorder, edits bool) *graph.Builder {
	redo := graph.Stateless(func(_ context.Context, r graph.Reader, w graph.Writer) error {
		rec.note("redo")
		if err := w.Set("redone", graph.BoolValue(true)); err != nil {
			return err
		}
		if !edits {
			return nil
		}
		v, err := r.Get("edits")
		if err != nil {
			return err
		}
		n, _ := v.Int()
		return w.Set("edits", graph.IntValue(n+1))
	})
	work := graph.Stateless(func(_ context.Context, r graph.Reader, w graph.Writer) error {
		v, err := r.Get("redone")
		if err != nil {
			return err
		}
		report := "the first report"
		if redone, _ := v.Bool(); redone {
			report = "a report after a round"
		}
		return w.Set("report", graph.TextValue(report))
	})
	return graph.NewBuilder().
		Start("work").
		Key(graph.Key{Name: "answer", Kind: graph.KindText}).
		Key(graph.Key{Name: "report", Kind: graph.KindText}).
		Key(graph.Key{Name: "redone", Kind: graph.KindBool}).
		Key(graph.Key{Name: "edits", Kind: graph.KindInt}).
		Node(graph.Node{Name: "work", NewBody: work, Reads: []string{"redone"}, Writes: []string{"report"}}).
		Node(graph.Node{Name: "hold", NewBody: noteOnly(rec, "hold"), Reads: []string{"answer"},
			Halt: &graph.Halt{Question: "again?", Options: []string{"again", "done"}, Into: "answer"}}).
		Node(graph.Node{Name: "redo", NewBody: redo, Reads: []string{"edits"}, Writes: []string{"redone", "edits"}}).
		Node(graph.Node{Name: "done", NewBody: noteOnly(rec, "done")}).
		Edge(graph.Edge{From: "work", To: "hold"}).
		Edge(graph.Edge{From: "hold", To: "redo", Rounds: 3,
			Guard: &graph.Guard{Key: "answer", Op: graph.OpEquals, Value: graph.TextValue("again")}}).
		Edge(graph.Edge{From: "hold", To: "done"}).
		Edge(graph.Edge{From: "redo", To: "work", Rounds: 3, Answered: true})
}

// An unattended answer is measured where it leaves the halt point, so a loop
// whose rounds change nothing parks before it spends its round limit even
// when the first round's report differs from every later one (#320).
//
// On reportingLoop the back edge closing a round compares the first two
// rounds unequal, because the first round read the one report no round left
// anything in, so that edge alone would let the third round run and park
// only after it, which is the round the limit of three refuses the next one
// after. The edge out of the hold compares the run as each answer sends it on,
// which is after the re-run: the third unattended answer leaves in exactly the
// state the second did and parks there, so two rounds run, not three.
//
// The run is walked three ways, and only the first parks. The second is the
// same loop answered by a person, which keeps Edge.Answered's exemption and
// spends the limit. The third is the positive control for the comparison
// itself: the same unattended answers on a loop whose rounds do change the
// state, which spends the limit too rather than parking.
func TestAnUnattendedAnswerIsMeasuredWhereItLeavesTheHalt(t *testing.T) {
	ctx := context.Background()
	const limit = 3
	for _, tc := range []struct {
		name       string
		edits      bool
		unattended bool
		rounds     int
		parked     bool
	}{
		{"unattended rounds that change nothing", false, true, 2, true},
		{"a person's rounds that change nothing", false, false, limit, false},
		{"unattended rounds that change the state", true, true, limit, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			g := mustBuild(t, reportingLoop(rec, tc.edits))
			exec := mustExecutor(t, g, graph.NewMemoryStore(), 100)
			if _, err := exec.Run(ctx, "run", mustState(t, g, nil)); err != nil {
				t.Fatalf("Run: %v", err)
			}
			answer := func() (graph.Result, error) {
				if tc.unattended {
					return exec.AnswerUnattended(ctx, "run", "again", "", nil)
				}
				return exec.Answer(ctx, "run", "again")
			}
			var last graph.Result
			for range limit + 1 {
				got, err := answer()
				if errors.Is(err, graph.ErrRoundsSpent) {
					break
				}
				if err != nil {
					t.Fatalf("answering: %v", err)
				}
				last = got
				if got.Status != graph.StatusHalted {
					break
				}
			}
			if got := rec.count("redo"); got != tc.rounds {
				t.Errorf("%d rounds ran, want %d", got, tc.rounds)
			}
			if !tc.parked {
				if last.Status != graph.StatusHalted || last.Position != "hold" {
					t.Fatalf("the run is %s at %q (%s), want it back at the hold with the limit spent",
						last.Status, last.Position, last.Reason)
				}
				return
			}
			park, ok := last.Park()
			if last.Status != graph.StatusConverged || !ok || park.Edge == nil ||
				park.Edge.From != "hold" || park.Edge.To != "redo" {
				t.Fatalf("the run is %s with park %+v (%s), want converged on the edge out of the hold",
					last.Status, park, last.Reason)
			}
		})
	}
}

// A convergence park on an edge only an unattended answer holds to the check
// is accepted on a checkpoint that records the answer as unattended and
// refused on one that does not, on the edge out of the halt point and on the
// Answered back edge alike, because without the record nothing could have
// compared either.
func TestValidateHoldsAConvergenceParkToTheAnswerThatAllowsIt(t *testing.T) {
	ctx := context.Background()
	g := mustBuild(t, answeredLoop(&recorder{}, true))
	store := graph.NewMemoryStore()
	exec := mustExecutor(t, g, store, 100)
	if _, err := exec.Run(ctx, "run", mustState(t, g, nil)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for range 2 {
		if _, err := exec.AnswerUnattended(ctx, "run", "again", "", nil); err != nil {
			t.Fatalf("AnswerUnattended: %v", err)
		}
	}
	parked, err := store.Latest(ctx, "run")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if parked.Status != graph.StatusConverged || !parked.Counters.Unattended {
		t.Fatalf("the run is %s, unattended %v; want an unattended convergence park", parked.Status, parked.Counters.Unattended)
	}
	answeredBack := func(c graph.Checkpoint) graph.Checkpoint {
		c.ParkedAt = &graph.EdgeRef{From: "redo", To: "work", Rounds: 3}
		c.Position = "work"
		return c
	}
	attended := func(c graph.Checkpoint) graph.Checkpoint {
		c.Counters.Unattended = false
		return c
	}
	for _, c := range []struct {
		name   string
		mutate func(graph.Checkpoint) graph.Checkpoint
		ok     bool
	}{
		{"the edge out of the hold, unattended", func(c graph.Checkpoint) graph.Checkpoint { return c }, true},
		{"the edge out of the hold, attended", attended, false},
		{"the answered back edge, unattended", answeredBack, true},
		{"the answered back edge, attended", func(c graph.Checkpoint) graph.Checkpoint { return attended(answeredBack(c)) }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := g.Validate(c.mutate(parked))
			if c.ok {
				if err != nil {
					t.Fatalf("Validate refused a park the executor writes: %v", err)
				}
				return
			}
			var refused *graph.CheckpointError
			if !errors.As(err, &refused) || refused.Field != "parked_at" {
				t.Fatalf("Validate = %v, want a refusal naming parked_at", err)
			}
		})
	}
}
