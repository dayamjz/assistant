package graph_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/graph"
)

func TestACheckpointIsWrittenAfterEveryNode(t *testing.T) {
	rec := &recorder{}
	g := mustBuild(t, graph.NewBuilder().
		Start("first").
		Key(graph.Key{Name: "trace", Kind: graph.KindList, Merge: graph.MergeAppend}).
		Node(graph.Node{Name: "first", Writes: []string{"trace"}, NewBody: appendTrace(rec, "first")}).
		Node(graph.Node{Name: "second", Writes: []string{"trace"}, NewBody: appendTrace(rec, "second")}).
		Node(graph.Node{Name: "third", Writes: []string{"trace"}, NewBody: appendTrace(rec, "third")}).
		Edge(graph.Edge{From: "first", To: "second"}).
		Edge(graph.Edge{From: "second", To: "third"}))

	store := graph.NewMemoryStore()
	exec := mustExecutor(t, g, store, 10)
	if _, err := exec.Run(context.Background(), "run", mustState(t, g, nil)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	history, err := store.History(context.Background(), "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	// One before the first node, then one after each of the three.
	if len(history) != 4 {
		t.Fatalf("history has %d checkpoints, want 4", len(history))
	}
	wantPositions := []string{"first", "second", "third", ""}
	wantTraceLen := []int{0, 1, 2, 3}
	for i, cp := range history {
		if cp.Seq != i+1 {
			t.Errorf("checkpoint %d has Seq %d", i, cp.Seq)
		}
		if cp.Position != wantPositions[i] {
			t.Errorf("checkpoint %d has Position %q, want %q", i, cp.Position, wantPositions[i])
		}
		if got := len(list(t, cp.State, "trace")); got != wantTraceLen[i] {
			t.Errorf("checkpoint %d records %d trace entries, want %d", i, got, wantTraceLen[i])
		}
		if err := g.Validate(cp); err != nil {
			t.Errorf("checkpoint %d does not validate against its own graph: %v", i, err)
		}
	}
}

func TestResumeReEmitsTheOpenDecisionAndRunsNothing(t *testing.T) {
	rec := &recorder{}
	g := mustBuild(t, haltingBuilder(rec))
	store := graph.NewMemoryStore()
	exec := mustExecutor(t, g, store, 10)

	first, err := exec.Run(context.Background(), "run", mustState(t, g, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	before, err := store.History(context.Background(), "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}

	// A fresh executor stands in for a restarted process.
	restarted := mustExecutor(t, g, store, 10)
	again, err := restarted.Resume(context.Background(), "run")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}

	if again.Status != graph.StatusHalted {
		t.Fatalf("Status = %s, want halted", again.Status)
	}
	if again.Decision == nil {
		t.Fatal("Resume emitted no decision")
	}
	if !sameDecision(again.Decision, first.Decision) {
		t.Errorf("Resume emitted %+v, want the decision the run stopped for, %+v", again.Decision, first.Decision)
	}
	if rec.count("gate") != 0 {
		t.Error("Resume ran the halted node")
	}
	after, err := store.History(context.Background(), "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("Resume wrote %d new checkpoints, want none", len(after)-len(before))
	}
}

func TestRestoreThenAnswerReachesTheSameStateAsAnsweringOutright(t *testing.T) {
	const answer = "approve"

	inOneCall := func(t *testing.T) (graph.Result, []graph.Checkpoint) {
		t.Helper()
		rec := &recorder{}
		g := mustBuild(t, haltingBuilder(rec))
		store := graph.NewMemoryStore()
		exec := mustExecutor(t, g, store, 10)
		if _, err := exec.Run(context.Background(), "run", mustState(t, g, nil)); err != nil {
			t.Fatalf("Run: %v", err)
		}
		got, err := exec.Answer(context.Background(), "run", answer)
		if err != nil {
			t.Fatalf("Answer: %v", err)
		}
		history, err := store.History(context.Background(), "run")
		if err != nil {
			t.Fatalf("History: %v", err)
		}
		return got, history
	}

	inTwoCalls := func(t *testing.T) (graph.Result, []graph.Checkpoint) {
		t.Helper()
		rec := &recorder{}
		g := mustBuild(t, haltingBuilder(rec))
		store := graph.NewMemoryStore()
		exec := mustExecutor(t, g, store, 10)
		if _, err := exec.Run(context.Background(), "run", mustState(t, g, nil)); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if _, err := exec.Resume(context.Background(), "run"); err != nil {
			t.Fatalf("Resume: %v", err)
		}
		got, err := exec.Answer(context.Background(), "run", answer)
		if err != nil {
			t.Fatalf("Answer: %v", err)
		}
		history, err := store.History(context.Background(), "run")
		if err != nil {
			t.Fatalf("History: %v", err)
		}
		return got, history
	}

	one, oneHistory := inOneCall(t)
	two, twoHistory := inTwoCalls(t)

	if one.Status != two.Status || one.Position != two.Position || one.Steps != two.Steps {
		t.Errorf("one call ended %s at %q after %d steps; two calls ended %s at %q after %d steps",
			one.Status, one.Position, one.Steps, two.Status, two.Position, two.Steps)
	}
	if !one.State.Equal(two.State) {
		t.Errorf("state after one call = %v, after two calls = %v",
			list(t, one.State, "trace"), list(t, two.State, "trace"))
	}
	if one.Checkpoint != two.Checkpoint {
		t.Errorf("last checkpoint = %s and %s", one.Checkpoint, two.Checkpoint)
	}
	if len(oneHistory) != len(twoHistory) {
		t.Fatalf("histories have %d and %d checkpoints", len(oneHistory), len(twoHistory))
	}
	for i := range oneHistory {
		if oneHistory[i].Position != twoHistory[i].Position || !oneHistory[i].State.Equal(twoHistory[i].State) {
			t.Errorf("checkpoint %d differs between the two paths", i)
		}
	}
	if got := list(t, one.State, "trace"); !equalStrings(got, []string{"prep", "gate:approve", "act"}) {
		t.Errorf("trace = %v, want the answer to have reached the halted node", got)
	}
}

func TestAnsweringRunsTheHaltedNodeExactlyOnce(t *testing.T) {
	rec := &recorder{}
	g := mustBuild(t, haltingBuilder(rec))
	store := graph.NewMemoryStore()
	exec := mustExecutor(t, g, store, 10)

	if _, err := exec.Run(context.Background(), "run", mustState(t, g, nil)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, err := exec.Answer(context.Background(), "run", "cancel")
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if got.Status != graph.StatusCompleted {
		t.Fatalf("Status = %s, want completed", got.Status)
	}
	if rec.count("gate") != 1 {
		t.Errorf("the halted node ran %d times, want exactly 1", rec.count("gate"))
	}
	if rec.count("prep") != 1 {
		t.Errorf("the node before the halt point ran %d times; a resume must re-execute nothing", rec.count("prep"))
	}
	if !equalStrings(rec.order(), []string{"prep", "gate", "stop"}) {
		t.Errorf("bodies ran %v, want the answer to have routed to stop", rec.order())
	}
	if text(t, got.State, "answer") != "cancel" {
		t.Errorf("answer = %q, want the answer written into the declared key", text(t, got.State, "answer"))
	}
}

func TestAnswerRefusesWhatTheDecisionDoesNotAllow(t *testing.T) {
	rec := &recorder{}
	g := mustBuild(t, haltingBuilder(rec))
	store := graph.NewMemoryStore()
	exec := mustExecutor(t, g, store, 10)
	if _, err := exec.Run(context.Background(), "run", mustState(t, g, nil)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, answer := range []string{"", "maybe", "APPROVE"} {
		if _, err := exec.Answer(context.Background(), "run", answer); !errors.Is(err, graph.ErrAnswerNotAllowed) {
			t.Errorf("Answer(%q) error = %v, want ErrAnswerNotAllowed", answer, err)
		}
	}
	if rec.count("gate") != 0 {
		t.Error("a refused answer ran the halted node")
	}
	// The run is still waiting, and still says so.
	again, err := exec.Resume(context.Background(), "run")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if again.Status != graph.StatusHalted || again.Decision == nil {
		t.Errorf("after a refused answer the run is %s with decision %v", again.Status, again.Decision)
	}
}

func TestAnswerRefusesARunWithNoOpenDecision(t *testing.T) {
	rec := &recorder{}
	g := mustBuild(t, graph.NewBuilder().
		Start("only").
		Node(graph.Node{Name: "only", NewBody: noteOnly(rec, "only")}))
	store := graph.NewMemoryStore()
	exec := mustExecutor(t, g, store, 10)
	if _, err := exec.Run(context.Background(), "run", mustState(t, g, nil)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if _, err := exec.Answer(context.Background(), "run", "yes"); !errors.Is(err, graph.ErrNoOpenDecision) {
		t.Errorf("Answer error = %v, want ErrNoOpenDecision", err)
	}
	if _, err := exec.Resume(context.Background(), "nothing"); !errors.Is(err, graph.ErrNoSuchRun) {
		t.Errorf("Resume of an unknown run error = %v, want ErrNoSuchRun", err)
	}
}

func TestBoundAccountingSurvivesAHaltAndResume(t *testing.T) {
	rec := &recorder{}
	// A loop with a decision in it: every round holds, so the round counter
	// only survives if the checkpoint carries it.
	g := mustBuild(t, haltLoopBuilder(rec))

	store := graph.NewMemoryStore()
	if _, err := mustExecutor(t, g, store, 50).Run(context.Background(), "run", mustState(t, g, nil)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Each answer uses a brand new executor, as a restarted process would.
	for round := 1; round <= 2; round++ {
		got, err := mustExecutor(t, g, store, 50).Answer(context.Background(), "run", "fix")
		if err != nil {
			t.Fatalf("round %d: Answer: %v", round, err)
		}
		if got.Status != graph.StatusHalted {
			t.Fatalf("round %d: Status = %s (%s), want halted again", round, got.Status, got.Reason)
		}
		if n := traversals(t, g, store, "run")["fix->check"]; n != round {
			t.Fatalf("round %d: back edge traversals = %d, want %d", round, n, round)
		}
	}

	// The third answer takes the back edge past its bound of two.
	got, err := mustExecutor(t, g, store, 50).Answer(context.Background(), "run", "fix")
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if got.Status != graph.StatusRoundsExhausted {
		t.Fatalf("Status = %s (%s), want rounds_exhausted", got.Status, got.Reason)
	}
	// A parked run stays parked however many times it is resumed.
	parked, err := mustExecutor(t, g, store, 50).Resume(context.Background(), "run")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if parked.Status != graph.StatusRoundsExhausted || parked.Checkpoint != got.Checkpoint {
		t.Errorf("resuming a parked run produced %s at %s", parked.Status, parked.Checkpoint)
	}
}

func TestAHaltPointReEnteredInALoopAsksAgainRatherThanInheritingTheAnswer(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	g := mustBuild(t, haltLoopBuilder(rec))
	store := graph.NewMemoryStore()

	held, err := mustExecutor(t, g, store, 50).Run(ctx, "run", mustState(t, g, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if held.Status != graph.StatusHalted {
		t.Fatalf("the run ended %s, want halted at its decision", held.Status)
	}
	if answer := text(t, held.State, "answer"); answer != "" {
		t.Errorf("the first halt already holds the answer %q", answer)
	}

	// Answering sends the run round the loop and back to the same halt point.
	again, err := mustExecutor(t, g, store, 50).Answer(ctx, "run", "fix")
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if again.Status != graph.StatusHalted {
		t.Fatalf("the run ended %s, want halted at the same decision again", again.Status)
	}
	if again.Decision == nil || again.Decision.Node != "gate" {
		t.Fatalf("the re-entered halt point emitted %v, want the gate's decision again", again.Decision)
	}
	// An answer is consent to one decision. Carrying it round the loop would
	// be standing consent nobody gave.
	if answer := text(t, again.State, "answer"); answer != "" {
		t.Errorf("the re-entered halt point still holds %q, want the previous answer cleared", answer)
	}

	// What the run was asked to resume from says the same thing.
	latest, err := store.Latest(ctx, "run")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if answer := text(t, latest.State, "answer"); answer != "" {
		t.Errorf("the checkpoint written at the re-entered halt holds %q, want it cleared", answer)
	}

	// And the forgery the cleared key exists to keep refusable stays refused:
	// the same record with its status flipped and its decision dropped.
	latest.Status = graph.StatusRunning
	latest.Decision = nil
	if _, err := store.Write(ctx, latest.ID(), latest); err != nil {
		t.Fatalf("Write: %v", err)
	}
	ranBefore := rec.count("gate")
	var ce *graph.CheckpointError
	if _, err := mustExecutor(t, g, store, 50).Resume(ctx, "run"); !errors.As(err, &ce) {
		t.Fatalf("Resume of a forged running checkpoint at the re-entered halt = %T %v, want a *graph.CheckpointError", err, err)
	}
	if rec.count("gate") != ranBefore {
		t.Error("the halt point's body ran from a forged checkpoint at a re-entered halt")
	}
}

func TestABoundParkedRunStandingAtAHaltPointHoldsNoAnswer(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	// The bounded back edge re-enters the halt point itself, so spending the
	// bound parks the run standing in front of a decision it was not asked.
	g := mustBuild(t, graph.NewBuilder().
		Start("gate").
		Key(graph.Key{Name: "answer", Kind: graph.KindText}).
		Key(graph.Key{Name: "log", Kind: graph.KindList, Merge: graph.MergeAppend}).
		Node(graph.Node{
			Name:    "gate",
			Reads:   []string{"answer"},
			Writes:  []string{"log"},
			Halt:    &graph.Halt{Question: "go on?", Options: []string{"go", "stop"}, Into: "answer"},
			NewBody: appendLog(rec, "gate"),
		}).
		Node(graph.Node{Name: "work", Writes: []string{"log"}, NewBody: appendLog(rec, "work")}).
		Node(graph.Node{Name: "done", Writes: []string{"log"}, NewBody: appendLog(rec, "done")}).
		Edge(graph.Edge{From: "gate", To: "work", Guard: &graph.Guard{
			Key: "answer", Op: graph.OpEquals, Value: graph.TextValue("go"),
		}}).
		Edge(graph.Edge{From: "gate", To: "done"}).
		Edge(graph.Edge{From: "work", To: "gate", Rounds: 1}))

	store := graph.NewMemoryStore()
	exec := mustExecutor(t, g, store, 50)
	if _, err := exec.Run(ctx, "run", mustState(t, g, nil)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := exec.Answer(ctx, "run", "go"); err != nil {
		t.Fatalf("first Answer: %v", err)
	}
	parked, err := exec.Answer(ctx, "run", "go")
	if err != nil {
		t.Fatalf("second Answer: %v", err)
	}
	if parked.Status != graph.StatusRoundsExhausted || parked.Position != "gate" {
		t.Fatalf("the run ended %s at %q, want rounds_exhausted standing at gate", parked.Status, parked.Position)
	}
	// The bound stopped the run in front of the gate without asking it, so the
	// answer the last round was given must not be sitting there.
	if answer := text(t, parked.State, "answer"); answer != "" {
		t.Errorf("the parked run still holds the answer %q", answer)
	}

	forged, err := store.Latest(ctx, "run")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if answer := text(t, forged.State, "answer"); answer != "" {
		t.Errorf("the checkpoint written when the bound parked the run holds %q, want it cleared", answer)
	}

	// A substrate hands that record back as a running one. With the answer
	// cleared it is the halt-bypass forgery again and is refused; with a stale
	// answer left in it, it would walk straight through the gate.
	forged.Status = graph.StatusRunning
	forged.Reason = ""
	if _, err := store.Write(ctx, forged.ID(), forged); err != nil {
		t.Fatalf("Write: %v", err)
	}
	ranBefore := rec.count("gate")
	_, err = exec.Resume(ctx, "run")
	var ce *graph.CheckpointError
	if !errors.As(err, &ce) {
		t.Fatalf("Resume of a forged running checkpoint at the parked halt = %T %v, want a *graph.CheckpointError", err, err)
	}
	if ce.Field != "status" {
		t.Errorf("the refusal blames %q (%s), want the halt-point rule to name %q", ce.Field, ce.Detail, "status")
	}
	if rec.count("gate") != ranBefore {
		t.Error("the halt point's body ran from a forged checkpoint built on a bound-parked answer")
	}
}

// stateHolding returns s with key holding answer. It goes through the state's
// own wire format because the exported constructor deliberately refuses to
// seed a halt point's answer key, and what this stands for is a record a
// substrate hands back rather than a state a caller could build.
func stateHolding(t *testing.T, s graph.State, key, answer string) graph.State {
	t.Helper()
	encoded, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal state: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatalf("Unmarshal state: %v", err)
	}
	value, err := json.Marshal(graph.TextValue(answer))
	if err != nil {
		t.Fatalf("Marshal value: %v", err)
	}
	raw[key] = value
	patched, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("Marshal patched state: %v", err)
	}
	var out graph.State
	if err := json.Unmarshal(patched, &out); err != nil {
		t.Fatalf("Unmarshal patched state: %v", err)
	}
	return out
}

func TestARunCannotStartHoldingAnAnswerNobodyWasAskedFor(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	g := mustBuild(t, haltingBuilder(rec))

	// The constructor refuses it, naming the key, rather than dropping what
	// the caller passed.
	_, err := g.NewState(map[string]graph.Value{"answer": graph.TextValue("approve")})
	if !errors.Is(err, graph.ErrAnswerPreseeded) {
		t.Fatalf("NewState with an answer pre-seeded = %v, want ErrAnswerPreseeded", err)
	}
	if !strings.Contains(err.Error(), `"answer"`) {
		t.Errorf("the refusal does not name the key: %v", err)
	}

	// And so does the run, for a state that never came through NewState - a
	// Result's state carries whatever the run it came from was holding.
	store := graph.NewMemoryStore()
	seeded := stateHolding(t, mustState(t, g, nil), "answer", "approve")
	if _, err := mustExecutor(t, g, store, 20).Run(ctx, "run", seeded); !errors.Is(err, graph.ErrAnswerPreseeded) {
		t.Fatalf("Run from a state holding an answer = %v, want ErrAnswerPreseeded", err)
	}
	if history, err := store.History(ctx, "run"); err != nil || len(history) != 0 {
		t.Errorf("the refused run wrote %d checkpoints, %v", len(history), err)
	}
	if got := rec.order(); len(got) != 0 {
		t.Errorf("bodies ran %v, want none: the run was refused before it started", got)
	}
}

func TestARunWithNoStepLeftParksAtTheHaltPointWithoutAsking(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	g := mustBuild(t, haltingBuilder(rec))
	store := graph.NewMemoryStore()

	// A budget of one is spent by "prep", so the run reaches the gate with
	// nothing left to run it with.
	got, err := mustExecutor(t, g, store, 1).Run(ctx, "run", mustState(t, g, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Status != graph.StatusBudgetExhausted || got.Position != "gate" {
		t.Fatalf("the run ended %s at %q, want budget_exhausted standing at gate", got.Status, got.Position)
	}
	// Asking for a decision the run could not act on is the thing this
	// prevents: consent that cannot be used is not requested.
	if got.Decision != nil {
		t.Errorf("the run emitted the decision %v it had no step left to act on", got.Decision)
	}
	if got.Reason == "" {
		t.Error("the parked run reports no reason")
	}
	if rec.count("gate") != 0 {
		t.Error("the halt point's body ran")
	}

	latest, err := store.Latest(ctx, "run")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if latest.Status != graph.StatusBudgetExhausted || latest.Decision != nil {
		t.Errorf("the checkpoint written is %s carrying %v, want budget_exhausted with no decision",
			latest.Status, latest.Decision)
	}
	// Nothing is open to answer, and the refusal says so.
	if _, err := mustExecutor(t, g, store, 1).Answer(ctx, "run", "approve"); !errors.Is(err, graph.ErrNoOpenDecision) {
		t.Errorf("answering a run parked before its halt point = %v, want ErrNoOpenDecision", err)
	}
}

func TestAnsweringIsRefusedWhenNoStepIsLeftForTheHaltedNode(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	g := mustBuild(t, haltingBuilder(rec))
	store := graph.NewMemoryStore()

	// Halted legitimately: this executor could afford the gate.
	held, err := mustExecutor(t, g, store, 20).Run(ctx, "run", mustState(t, g, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if held.Status != graph.StatusHalted {
		t.Fatalf("the run ended %s, want halted at its decision", held.Status)
	}

	// The budget travels with the run, so reaching a halt point the run
	// cannot afford takes a deliberate lowering. Adopting one below what the
	// run has already spent is that act, and it leaves the decision open.
	lean := mustExecutor(t, g, store, 1)
	if _, err := lean.AdoptBudget(ctx, "run"); err != nil {
		t.Fatalf("AdoptBudget: %v", err)
	}
	before, err := store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	_, err = lean.Answer(ctx, "run", "approve")
	if !errors.Is(err, graph.ErrBudgetSpent) {
		t.Fatalf("answering with no step left = %v, want ErrBudgetSpent", err)
	}
	if errors.Is(err, graph.ErrNoOpenDecision) {
		t.Error("the refusal reads as no open decision; the decision is open and unanswerable, which is a different thing")
	}
	after, err := store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("the refused answer wrote %d checkpoints, want none", len(after)-len(before))
	}
	// The decision is still open and the answer was not consumed, so the same
	// run answers normally once it can afford the node.
	latest := after[len(after)-1]
	if latest.Status != graph.StatusHalted || latest.Decision == nil {
		t.Fatalf("the run is %s carrying %v, want its decision still open", latest.Status, latest.Decision)
	}
	if answer := text(t, latest.State, "answer"); answer != "" {
		t.Errorf("the refused answer was recorded as %q", answer)
	}
	if rec.count("gate") != 0 {
		t.Error("the halt point's body ran under a budget that could not afford it")
	}
	roomy := mustExecutor(t, g, store, 20)
	if _, err := roomy.AdoptBudget(ctx, "run"); err != nil {
		t.Fatalf("AdoptBudget back to a budget the run can afford: %v", err)
	}
	done, err := roomy.Answer(ctx, "run", "approve")
	if err != nil {
		t.Fatalf("Answer once the run can afford the node: %v", err)
	}
	if done.Status != graph.StatusCompleted {
		t.Fatalf("the answered run ended %s, want completed", done.Status)
	}
}

func TestResumingIsRefusedWhenNoStepIsLeftForTheClaimedHaltedNode(t *testing.T) {
	ctx := context.Background()
	store := graph.NewMemoryStore()

	// An answered segment that died before the halt node finished leaves the
	// run standing at the gate with the answer recorded.
	died := &recorder{}
	crashing := mustBuild(t, haltingBuilderWithGate(died, func(_ context.Context, _ graph.Reader, _ graph.Writer) error {
		return errors.New("the process died")
	}))
	if _, err := mustExecutor(t, crashing, store, 20).Run(ctx, "run", mustState(t, crashing, nil)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := mustExecutor(t, crashing, store, 20).Answer(ctx, "run", "approve"); !errors.Is(err, graph.ErrNodeFailed) {
		t.Fatalf("Answer = %v, want the halt node's failure", err)
	}

	// Lowering the budget below what the run has spent and then resuming is
	// refused rather than claimed and parked, which would discard the answer
	// the claim carries.
	rec := &recorder{}
	g := mustBuild(t, haltingBuilder(rec))
	lean := mustExecutor(t, g, store, 1)
	if _, err := lean.AdoptBudget(ctx, "run"); err != nil {
		t.Fatalf("AdoptBudget: %v", err)
	}
	before, err := store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if _, err := lean.Resume(ctx, "run"); !errors.Is(err, graph.ErrBudgetSpent) {
		t.Fatalf("resuming with no step left = %v, want ErrBudgetSpent", err)
	}
	after, err := store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("the refused resume wrote %d checkpoints, want none", len(after)-len(before))
	}
	if answer := text(t, after[len(after)-1].State, "answer"); answer != "approve" {
		t.Errorf("the claim now holds %q, want the answer it was given to be kept", answer)
	}
	if rec.count("gate") != 0 {
		t.Error("the halt point's body ran under a budget that could not afford it")
	}
}

func TestResumingAHaltedRunIsRefusedWhenItsBudgetCannotAffordTheHaltNode(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	g := mustBuild(t, haltingBuilder(rec))
	store := graph.NewMemoryStore()

	held, err := mustExecutor(t, g, store, 20).Run(ctx, "run", mustState(t, g, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if held.Status != graph.StatusHalted || held.Decision == nil {
		t.Fatalf("the run ended %s carrying %v, want halted at its decision", held.Status, held.Decision)
	}

	// Restoring the run once its budget leaves no step for the gate must not
	// put the decision to a caller who could never act on it.
	lean := mustExecutor(t, g, store, 1)
	if _, err := lean.AdoptBudget(ctx, "run"); err != nil {
		t.Fatalf("AdoptBudget: %v", err)
	}
	before, err := store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	got, err := lean.Resume(ctx, "run")
	if !errors.Is(err, graph.ErrBudgetSpent) {
		t.Fatalf("resuming a halted run with no step left = %v, want ErrBudgetSpent", err)
	}
	if got.Decision != nil {
		t.Errorf("the refused resume re-emitted the decision %v", got.Decision)
	}
	after, err := store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("the refused resume wrote %d checkpoints, want none: refusing is not parking", len(after)-len(before))
	}
	if rec.count("gate") != 0 {
		t.Error("the halt point's body ran")
	}

	// The decision is untouched, so a caller with room for the gate still gets
	// it back and Resume still writes nothing.
	roomy := mustExecutor(t, g, store, 20)
	if _, err := roomy.AdoptBudget(ctx, "run"); err != nil {
		t.Fatalf("AdoptBudget back to a budget that affords the gate: %v", err)
	}
	before, err = store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	again, err := roomy.Resume(ctx, "run")
	if err != nil {
		t.Fatalf("Resume under a budget that affords the gate: %v", err)
	}
	if again.Status != graph.StatusHalted || again.Decision == nil {
		t.Fatalf("the restored run is %s carrying %v, want its decision re-emitted", again.Status, again.Decision)
	}
	restored, err := store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(restored) != len(before) {
		t.Errorf("re-emitting the decision wrote %d checkpoints, want none", len(restored)-len(before))
	}
	if rec.count("gate") != 0 {
		t.Error("re-emitting the decision ran the halt point's body")
	}
}

func TestABoundParkedRunAtAHaltPointStillResumesUnchanged(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	g := mustBuild(t, haltingBuilder(rec))
	store := graph.NewMemoryStore()

	// A budget of one is spent by "prep", so the run parks in front of the
	// gate: standing at a halt point, with the budget as spent as the run
	// refused above, but parked by a bound rather than waiting on anything.
	parked, err := mustExecutor(t, g, store, 1).Run(ctx, "run", mustState(t, g, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if parked.Status != graph.StatusBudgetExhausted || parked.Position != "gate" {
		t.Fatalf("the run ended %s at %q, want budget_exhausted standing at gate", parked.Status, parked.Position)
	}
	before, err := store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}

	got, err := mustExecutor(t, g, store, 1).Resume(ctx, "run")
	if err != nil {
		t.Fatalf("resuming a bound-parked run = %v, want it returned unchanged", err)
	}
	if got.Status != parked.Status || got.Position != parked.Position {
		t.Errorf("the resumed run is %s at %q, want %s at %q", got.Status, got.Position, parked.Status, parked.Position)
	}
	after, err := store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("resuming a parked run wrote %d checkpoints, want none", len(after)-len(before))
	}
	if rec.count("gate") != 0 {
		t.Error("the halt point's body ran")
	}
}

func TestARunStartingAtAHaltPointStopsBeforeItsFirstNode(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	g := mustBuild(t, graph.NewBuilder().
		Start("gate").
		Key(graph.Key{Name: "answer", Kind: graph.KindText}).
		Key(graph.Key{Name: "log", Kind: graph.KindList, Merge: graph.MergeAppend}).
		Node(graph.Node{
			Name:    "gate",
			Reads:   []string{"answer"},
			Writes:  []string{"log"},
			Halt:    &graph.Halt{Question: "go on?", Options: []string{"go", "stop"}, Into: "answer"},
			NewBody: appendLog(rec, "gate"),
		}).
		Node(graph.Node{Name: "done", Writes: []string{"log"}, NewBody: appendLog(rec, "done")}).
		Edge(graph.Edge{From: "gate", To: "done"}))

	store := graph.NewMemoryStore()
	got, err := mustExecutor(t, g, store, 50).Run(ctx, "run", mustState(t, g, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Run hands back whatever the first checkpoint stopped as rather than
	// walking on from it, so the node it stands in front of never starts.
	if got.Status != graph.StatusHalted || got.Position != "gate" {
		t.Fatalf("the run ended %s at %q, want halted standing at gate", got.Status, got.Position)
	}
	if got.Decision == nil || got.Decision.Node != "gate" {
		t.Fatalf("the run emitted %v, want the gate's decision", got.Decision)
	}
	if ran := rec.order(); len(ran) != 0 {
		t.Errorf("bodies ran %v, want none: the run stops before its start node", ran)
	}
	history, err := store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(history) != 1 {
		t.Errorf("the run wrote %d checkpoints, want the one it halted with", len(history))
	}
}

func TestResumeAcceptsTheClaimAnAnsweredSegmentLeftBehind(t *testing.T) {
	ctx := context.Background()
	store := graph.NewMemoryStore()

	// The answer is claimed and then the segment dies before the halt node can
	// finish, so the run's tip is the claim: running at the halt point, no
	// decision, carrying the answer that was given.
	died := &recorder{}
	crashing := mustBuild(t, haltingBuilderWithGate(died, func(_ context.Context, _ graph.Reader, _ graph.Writer) error {
		return errors.New("the process died")
	}))
	if _, err := mustExecutor(t, crashing, store, 20).Run(ctx, "run", mustState(t, crashing, nil)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := mustExecutor(t, crashing, store, 20).Answer(ctx, "run", "approve"); !errors.Is(err, graph.ErrNodeFailed) {
		t.Fatalf("Answer = %v, want the halt node's failure", err)
	}
	claim, err := store.Latest(ctx, "run")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if claim.Position != "gate" || claim.Status != graph.StatusRunning || claim.Decision != nil {
		t.Fatalf("the tip is %s at %q with decision %v, want a running claim at the gate",
			claim.Status, claim.Position, claim.Decision)
	}
	if answer := text(t, claim.State, "answer"); answer != "approve" {
		t.Fatalf("the claim carries the answer %q, want the one that was given", answer)
	}

	// A restarted process resumes from that claim. Validate must accept it -
	// the answer is there, so this decision was answered - and the halt node
	// runs for the first time. Refusing it instead would strand the run for
	// good: forking copies the same record.
	rec := &recorder{}
	g := mustBuild(t, haltingBuilder(rec))
	done, err := mustExecutor(t, g, store, 20).Resume(ctx, "run")
	if err != nil {
		t.Fatalf("Resume from the claim an answered segment left behind: %v", err)
	}
	if done.Status != graph.StatusCompleted {
		t.Fatalf("the resumed run ended %s, want completed", done.Status)
	}
	if !equalStrings(rec.order(), []string{"gate", "act"}) {
		t.Errorf("bodies ran %v, want the halt node once and then the node it routed to", rec.order())
	}
	if trace := list(t, done.State, "trace"); !equalStrings(trace, []string{"prep", "gate:approve", "act"}) {
		t.Errorf("trace = %v, want the run to have routed on the answer it was resumed with", trace)
	}
}

func TestBuildRefusesASecondWriterOfAHaltAnswerKey(t *testing.T) {
	rec := &recorder{}
	cases := map[string]struct {
		build *graph.Builder
		names []string
	}{
		"another node writes the answer key": {
			build: graph.NewBuilder().
				Start("prep").
				Key(graph.Key{Name: "answer", Kind: graph.KindText, Merge: graph.MergeLastWriteWins}).
				Node(graph.Node{Name: "prep", Writes: []string{"answer"}, NewBody: noteOnly(rec, "prep")}).
				Node(graph.Node{
					Name:    "gate",
					Halt:    &graph.Halt{Question: "ship it?", Into: "answer"},
					NewBody: noteOnly(rec, "gate"),
				}).
				Edge(graph.Edge{From: "prep", To: "gate"}),
			names: []string{`"answer"`, `"prep"`, `"gate"`},
		},
		"a second halt point asks into the same key": {
			build: graph.NewBuilder().
				Start("first").
				Key(graph.Key{Name: "answer", Kind: graph.KindText}).
				Node(graph.Node{
					Name:    "first",
					Halt:    &graph.Halt{Question: "ship it?", Into: "answer"},
					NewBody: noteOnly(rec, "first"),
				}).
				Node(graph.Node{
					Name:    "second",
					Halt:    &graph.Halt{Question: "really?", Into: "answer"},
					NewBody: noteOnly(rec, "second"),
				}).
				Edge(graph.Edge{From: "first", To: "second"}),
			names: []string{`"answer"`, "first", "second"},
		},
		"the answer key declares a merge rule": {
			build: graph.NewBuilder().
				Start("gate").
				Key(graph.Key{Name: "answer", Kind: graph.KindText, Merge: graph.MergeLastWriteWins}).
				Node(graph.Node{
					Name:    "gate",
					Halt:    &graph.Halt{Question: "ship it?", Into: "answer"},
					NewBody: noteOnly(rec, "gate"),
				}),
			names: []string{`"answer"`, `"gate"`, "last-write-wins"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := tc.build.Build()
			be := requireBuildError(t, err, graph.RuleSingleWriter)
			for _, want := range tc.names {
				if !strings.Contains(be.Error(), want) {
					t.Errorf("the refusal does not name %s: %v", want, be)
				}
			}
		})
	}
}

// haltLoopBuilder declares a decision inside a bounded loop: the gate halts,
// answering it routes to the fixer, and the back edge brings the run round to
// the same gate again.
func haltLoopBuilder(rec *recorder) *graph.Builder {
	return graph.NewBuilder().
		Start("check").
		Key(graph.Key{Name: "answer", Kind: graph.KindText}).
		Key(graph.Key{Name: "log", Kind: graph.KindList, Merge: graph.MergeAppend}).
		Node(graph.Node{Name: "check", Writes: []string{"log"}, NewBody: appendLog(rec, "check")}).
		Node(graph.Node{
			Name:    "gate",
			Reads:   []string{"answer"},
			Writes:  []string{"log"},
			Halt:    &graph.Halt{Question: "fix it?", Options: []string{"fix", "stop"}, Into: "answer"},
			NewBody: appendLog(rec, "gate"),
		}).
		Node(graph.Node{Name: "fix", Writes: []string{"log"}, NewBody: appendLog(rec, "fix")}).
		Node(graph.Node{Name: "done", Writes: []string{"log"}, NewBody: appendLog(rec, "done")}).
		Edge(graph.Edge{From: "check", To: "gate"}).
		Edge(graph.Edge{From: "gate", To: "fix", Guard: &graph.Guard{
			Key: "answer", Op: graph.OpEquals, Value: graph.TextValue("fix"),
		}}).
		Edge(graph.Edge{From: "gate", To: "done"}).
		Edge(graph.Edge{From: "fix", To: "check", Rounds: 2})
}

// appendLog returns a body that appends its node's name to the "log" key.
func appendLog(rec *recorder, name string) func() graph.Body {
	return body(func(_ context.Context, _ graph.Reader, w graph.Writer) error {
		rec.note(name)
		return w.Set("log", graph.ListValue(name))
	})
}

func TestValidateRefusesACheckpointThatDoesNotMatchTheGraph(t *testing.T) {
	rec := &recorder{}
	g := mustBuild(t, haltingBuilder(rec))
	store := graph.NewMemoryStore()
	exec := mustExecutor(t, g, store, 10)
	if _, err := exec.Run(context.Background(), "run", mustState(t, g, nil)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	sound, err := store.Latest(context.Background(), "run")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if err := g.Validate(sound); err != nil {
		t.Fatalf("a checkpoint the executor wrote does not validate: %v", err)
	}

	// A graph that declares different keys, so its states are the wrong shape.
	other := mustBuild(t, graph.NewBuilder().
		Start("only").
		Key(graph.Key{Name: "answer", Kind: graph.KindInt}).
		Node(graph.Node{Name: "only", NewBody: noteOnly(rec, "only")}))

	// An answer the gate's declared options do not permit, which is no answer
	// at all as far as the halt point is concerned.
	unacceptable := stateHolding(t, sound.State, "answer", "ship anyway")

	// Each case names the field the refusal must blame, so a case cannot start
	// passing because some other rule caught it first.
	cases := map[string]struct {
		tamper func(c *graph.Checkpoint)
		field  string
	}{
		"no run name": {
			tamper: func(c *graph.Checkpoint) { c.Run = "" },
			field:  "run",
		},
		"no sequence number": {
			tamper: func(c *graph.Checkpoint) { c.Seq = 0 },
			field:  "seq",
		},
		"position is not a node": {
			tamper: func(c *graph.Checkpoint) { c.Position = "elsewhere" },
			field:  "position",
		},
		"completed but positioned": {
			tamper: func(c *graph.Checkpoint) { c.Status = graph.StatusCompleted },
			field:  "position",
		},
		// A run may stand at a halt point with StatusRunning only once it has
		// been answered, which is what the claim an Answer writes looks like.
		// Without an answer, and with one the halt point does not accept, the
		// checkpoint is a forgery that would walk through the node.
		"running at a halt point with no answer": {
			tamper: func(c *graph.Checkpoint) {
				c.Status = graph.StatusRunning
				c.Decision = nil
			},
			field: "status",
		},
		"running at a halt point on an answer it does not accept": {
			tamper: func(c *graph.Checkpoint) {
				c.Status = graph.StatusRunning
				c.Decision = nil
				c.State = unacceptable
			},
			field: "status",
		},
		"reason on a run that is still running": {
			tamper: func(c *graph.Checkpoint) {
				c.Status = graph.StatusRunning
				c.Position = "prep"
				c.Decision = nil
				c.Reason = "the run-wide step budget of 8 was spent"
			},
			field: "reason",
		},
		"halted with no decision": {
			tamper: func(c *graph.Checkpoint) { c.Decision = nil },
			field:  "decision",
		},
		"decision moved elsewhere": {
			tamper: func(c *graph.Checkpoint) { c.Decision.Node = "prep" },
			field:  "decision",
		},
		"decision rewritten": {
			tamper: func(c *graph.Checkpoint) {
				c.Decision.Options = []string{"approve", "cancel", "ship anyway"}
			},
			field: "decision",
		},
		// Positioned away from the halt point, so the run is legitimately
		// running and the decision it still carries is what is wrong.
		"decision on a running checkpoint": {
			tamper: func(c *graph.Checkpoint) {
				c.Status = graph.StatusRunning
				c.Position = "prep"
			},
			field: "decision",
		},
		"state from another graph": {
			tamper: func(c *graph.Checkpoint) {
				c.State = mustStateNoHelper(other)
			},
			field: "state",
		},
		"counters sized for another graph": {
			tamper: func(c *graph.Checkpoint) {
				c.Counters.Traversals = []int{0}
				c.Counters.Fingerprints = []string{""}
			},
			field: "counters.traversals",
		},
		"negative step count": {
			tamper: func(c *graph.Checkpoint) { c.Counters.Steps = -1 },
			field:  "counters.steps",
		},
		"negative traversals": {
			tamper: func(c *graph.Checkpoint) { c.Counters.Traversals[0] = -1 },
			field:  "counters.traversals",
		},
		"no run-wide budget": {
			tamper: func(c *graph.Checkpoint) { c.Counters.Budget = 0 },
			field:  "counters.budget",
		},
		"counters accrued against other edges": {
			tamper: func(c *graph.Checkpoint) { c.Counters.EdgeDigest = "not this graph's edges" },
			field:  "counters.edge_digest",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cp := sound
			cp.State = sound.State.Clone()
			// Copied whole and then detached, so a field added to Counters
			// later is carried here rather than silently arriving zeroed and
			// refused for a reason no case in this table asked about.
			counters := sound.Counters
			counters.Traversals = append([]int(nil), sound.Counters.Traversals...)
			counters.Fingerprints = append([]string(nil), sound.Counters.Fingerprints...)
			cp.Counters = counters
			decision := *sound.Decision
			decision.Options = append([]string(nil), sound.Decision.Options...)
			cp.Decision = &decision

			tc.tamper(&cp)
			err := g.Validate(cp)
			if err == nil {
				t.Fatalf("Validate accepted a tampered checkpoint: %+v", cp)
			}
			var ce *graph.CheckpointError
			if !errors.As(err, &ce) {
				t.Fatalf("Validate error = %T %v, want a *graph.CheckpointError", err, err)
			}
			if ce.Field != tc.field {
				t.Fatalf("Validate blamed %q (%s), want the refusal to name %q",
					ce.Field, ce.Detail, tc.field)
			}
		})
	}
}

func TestValidateRefusesTraversalsPastAnEdgeBound(t *testing.T) {
	rec := &recorder{}
	g := mustBuild(t, fixLoopBuilder(rec, 2, func(_ context.Context, _ graph.Reader, _ graph.Writer) error {
		return nil
	}))
	store := graph.NewMemoryStore()
	exec := mustExecutor(t, g, store, 50)
	if _, err := exec.Run(context.Background(), "run", mustState(t, g, nil)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	cp, err := store.Latest(context.Background(), "run")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}

	for i, e := range g.Edges() {
		if e.Rounds == 0 {
			continue
		}
		cp.Counters.Traversals[i] = e.Rounds + 1
	}
	err = g.Validate(cp)
	var ce *graph.CheckpointError
	if !errors.As(err, &ce) {
		t.Fatalf("Validate error = %T %v, want a *graph.CheckpointError", err, err)
	}
	if ce.Field != "counters.traversals" {
		t.Fatalf("Validate blamed %q (%s), want the count named: these are this graph's own edges",
			ce.Field, ce.Detail)
	}
}

func TestResumeRefusesATamperedCheckpoint(t *testing.T) {
	rec := &recorder{}
	g := mustBuild(t, haltingBuilder(rec))
	store := graph.NewMemoryStore()
	exec := mustExecutor(t, g, store, 10)
	if _, err := exec.Run(context.Background(), "run", mustState(t, g, nil)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	cp, err := store.Latest(context.Background(), "run")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	cp.Position = "act"
	cp.Decision.Node = "act"
	if _, err := store.Write(context.Background(), cp.ID(), cp); err != nil {
		t.Fatalf("Write: %v", err)
	}

	_, err = exec.Resume(context.Background(), "run")
	var ce *graph.CheckpointError
	if !errors.As(err, &ce) {
		t.Fatalf("Resume error = %T %v, want a *graph.CheckpointError", err, err)
	}
	if _, err := exec.Answer(context.Background(), "run", "approve"); !errors.As(err, &ce) {
		t.Fatalf("Answer error = %T %v, want a *graph.CheckpointError", err, err)
	}
	if rec.count("gate") != 0 || rec.count("act") != 0 {
		t.Error("a tampered checkpoint ran a node")
	}
}

func TestResumeRefusesARunningCheckpointStandingAtAHaltPoint(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	g := mustBuild(t, haltingBuilder(rec))
	store := graph.NewMemoryStore()
	exec := mustExecutor(t, g, store, 10)
	if _, err := exec.Run(ctx, "run", mustState(t, g, nil)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	cp, err := store.Latest(ctx, "run")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if cp.Status != graph.StatusHalted || cp.Position != "gate" {
		t.Fatalf("the run is %s at %q, want halted at gate", cp.Status, cp.Position)
	}
	// A substrate hands back the halt point with the decision dropped and the
	// status rewritten, which would walk the run straight through the node it
	// stopped before.
	cp.Status = graph.StatusRunning
	cp.Decision = nil
	if _, err := store.Write(ctx, cp.ID(), cp); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var ce *graph.CheckpointError
	if _, err := exec.Resume(ctx, "run"); !errors.As(err, &ce) {
		t.Fatalf("Resume error = %T %v, want a *graph.CheckpointError", err, err)
	}
	if _, err := exec.Answer(ctx, "run", "approve"); !errors.As(err, &ce) {
		t.Fatalf("Answer error = %T %v, want a *graph.CheckpointError", err, err)
	}
	if rec.count("gate") != 0 {
		t.Error("the halt point's body ran from a forged running checkpoint")
	}
	if rec.count("act") != 0 {
		t.Error("a node past the halt point ran from a forged running checkpoint")
	}
}

func TestCheckpointRoundTripsThroughItsWireFormat(t *testing.T) {
	rec := &recorder{}
	g := mustBuild(t, haltingBuilder(rec))
	store := graph.NewMemoryStore()
	exec := mustExecutor(t, g, store, 10)
	if _, err := exec.Run(context.Background(), "run", mustState(t, g, nil)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	cp, err := store.Latest(context.Background(), "run")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}

	encoded, err := json.Marshal(cp)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded graph.Checkpoint
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.Run != cp.Run || decoded.Seq != cp.Seq || decoded.Position != cp.Position ||
		decoded.Status != cp.Status || !decoded.State.Equal(cp.State) {
		t.Errorf("round trip changed the checkpoint:\n got %+v\nwant %+v", decoded, cp)
	}
	if !sameDecision(decoded.Decision, cp.Decision) {
		t.Errorf("round trip changed the decision: %+v", decoded.Decision)
	}
	if err := g.Validate(decoded); err != nil {
		t.Errorf("a round-tripped checkpoint no longer validates: %v", err)
	}
}

func TestCheckpointDecodingRefusesWhatItDoesNotRecognize(t *testing.T) {
	sound := `{"run":"r","seq":1,"position":"prep","status":"running",` +
		`"state":{"answer":{"kind":"text","value":"x"}},` +
		`"counters":{"steps":0,"traversals":[],"fingerprints":[]}}`
	var cp graph.Checkpoint
	if err := json.Unmarshal([]byte(sound), &cp); err != nil {
		t.Fatalf("a well-formed checkpoint was refused: %v", err)
	}

	cases := map[string]string{
		"an unknown field":       strings.Replace(sound, `"seq":1`, `"seq":1,"exec":"rm -rf /"`, 1),
		"an unknown status":      strings.Replace(sound, `"status":"running"`, `"status":"privileged"`, 1),
		"an unknown value kind":  strings.Replace(sound, `"kind":"text"`, `"kind":"function"`, 1),
		"a mistyped value":       strings.Replace(sound, `"value":"x"`, `"value":{"nested":true}`, 1),
		"an unknown value field": strings.Replace(sound, `"kind":"text"`, `"kind":"text","extra":1`, 1),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			var got graph.Checkpoint
			if err := json.Unmarshal([]byte(raw), &got); err == nil {
				t.Fatalf("decoding accepted %s: %s", name, raw)
			}
		})
	}
}
