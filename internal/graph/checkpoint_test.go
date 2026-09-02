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
	// A loop with a decision in it: every round parks, so the round counter
	// only survives if the checkpoint carries it.
	g := mustBuild(t, graph.NewBuilder().
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
		Edge(graph.Edge{From: "fix", To: "check", Rounds: 2}))

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

	cases := map[string]func(c *graph.Checkpoint){
		"no run name":              func(c *graph.Checkpoint) { c.Run = "" },
		"no sequence number":       func(c *graph.Checkpoint) { c.Seq = 0 },
		"position is not a node":   func(c *graph.Checkpoint) { c.Position = "elsewhere" },
		"completed but positioned": func(c *graph.Checkpoint) { c.Status = graph.StatusCompleted },
		"halted with no decision":  func(c *graph.Checkpoint) { c.Decision = nil },
		"decision moved elsewhere": func(c *graph.Checkpoint) { c.Decision.Node = "prep" },
		"decision rewritten": func(c *graph.Checkpoint) {
			c.Decision.Options = []string{"approve", "cancel", "ship anyway"}
		},
		"decision on a running checkpoint": func(c *graph.Checkpoint) {
			c.Status = graph.StatusRunning
		},
		"state from another graph": func(c *graph.Checkpoint) {
			c.State = mustStateNoHelper(other)
		},
		"counters sized for another graph": func(c *graph.Checkpoint) {
			c.Counters = graph.Counters{Traversals: []int{0}, Fingerprints: []string{""}}
		},
		"negative step count": func(c *graph.Checkpoint) { c.Counters.Steps = -1 },
		"negative traversals": func(c *graph.Checkpoint) { c.Counters.Traversals[0] = -1 },
	}
	for name, tamper := range cases {
		t.Run(name, func(t *testing.T) {
			cp := sound
			cp.State = sound.State.Clone()
			cp.Counters = graph.Counters{
				Steps:        sound.Counters.Steps,
				Traversals:   append([]int(nil), sound.Counters.Traversals...),
				Fingerprints: append([]string(nil), sound.Counters.Fingerprints...),
			}
			decision := *sound.Decision
			decision.Options = append([]string(nil), sound.Decision.Options...)
			cp.Decision = &decision

			tamper(&cp)
			err := g.Validate(cp)
			if err == nil {
				t.Fatalf("Validate accepted a tampered checkpoint: %+v", cp)
			}
			var ce *graph.CheckpointError
			if !errors.As(err, &ce) {
				t.Fatalf("Validate error = %T %v, want a *graph.CheckpointError", err, err)
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
	if _, err := store.Write(context.Background(), cp); err != nil {
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
