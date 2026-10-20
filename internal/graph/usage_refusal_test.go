package graph_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dayamjz/assistant/internal/graph"
)

// The cases ErrUsageUnread's comment lists per entry point, beyond the two
// usage_test.go already covers (a Run refused before its first checkpoint, a
// Resume refused before its claim), and the halt point a failed reading can
// leave a run past its budget at.

// gateBuilder is work, which spends cost and then calls after, followed by a
// halt point and a node after it: the shape every halt point case below
// needs.
func gateBuilder(rec *recorder, l *ledger, cost int64, after func()) *graph.Builder {
	return graph.NewBuilder().
		Start("work").
		Key(graph.Key{Name: "answer", Kind: graph.KindText}).
		Node(graph.Node{Name: "work", NewBody: body(func(context.Context, graph.Reader, graph.Writer) error {
			rec.note("work")
			l.spend("run", cost)
			if after != nil {
				after()
			}
			return nil
		})}).
		Node(graph.Node{
			Name:    "gate",
			Reads:   []string{"answer"},
			Halt:    &graph.Halt{Question: "ship it?", Options: []string{"approve"}, Into: "answer"},
			NewBody: noteOnly(rec, "gate"),
		}).
		Node(graph.Node{Name: "after", NewBody: noteOnly(rec, "after")}).
		Edge(graph.Edge{From: "work", To: "gate"}).
		Edge(graph.Edge{From: "gate", To: "after"})
}

// TestARunRefusedAtALaterReadingIsContinuedWithResume is Run's second case:
// the reading before the first checkpoint succeeds and a later one fails, so
// the run has a history. It stands at the checkpoint after its last completed
// node, a second Run is refused with ErrRunExists, and Resume continues it
// with no node run twice.
func TestARunRefusedAtALaterReadingIsContinuedWithResume(t *testing.T) {
	ctx := context.Background()
	rec, l := &recorder{}, newLedger()
	g := mustBuild(t, spendingBuilder(rec, l, 10, false))
	store := graph.NewMemoryStore()
	// Readings: before the first checkpoint, before a, before b.
	l.failFromRead(3, errors.New("the record is closed"))

	if _, err := usageExecutor(t, g, store, l, 100).Run(ctx, "run", mustState(t, g, nil)); !errors.Is(err, graph.ErrUsageUnread) {
		t.Fatalf("Run = %v, want ErrUsageUnread at the reading before b", err)
	}
	latest, err := store.Latest(ctx, "run")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if latest.Status != graph.StatusRunning || latest.Position != "b" || rec.count("a") != 1 || rec.count("b") != 0 {
		t.Fatalf("the run stands %s at %q having run %v, want running in front of b with a done",
			latest.Status, latest.Position, rec.order())
	}
	l.failFromRead(0, nil)
	if _, err := usageExecutor(t, g, store, l, 100).Run(ctx, "run", mustState(t, g, nil)); !errors.Is(err, graph.ErrRunExists) {
		t.Fatalf("a second Run = %v, want ErrRunExists, since the refused Run left a history", err)
	}
	done, err := usageExecutor(t, g, store, l, 100).Resume(ctx, "run")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if done.Status != graph.StatusCompleted || rec.count("a") != 1 || rec.count("d") != 1 {
		t.Fatalf("the run ended %s having run %v, want completed with a run once", done.Status, rec.order())
	}
}

// TestAnAnswerRefusedAfterItsClaimIsContinuedWithResume is Answer's second
// case: the reading before the claim succeeds and the one after it fails, so
// the answer is recorded and the decision closed. The run stands running at
// the halt point with the answer kept, a second Answer is refused with
// ErrNoOpenDecision, and Resume runs the halt node once from the recorded
// answer.
func TestAnAnswerRefusedAfterItsClaimIsContinuedWithResume(t *testing.T) {
	ctx := context.Background()
	rec, l := &recorder{}, newLedger()
	g := mustBuild(t, gateBuilder(rec, l, 10, nil))
	store := graph.NewMemoryStore()
	exec := usageExecutor(t, g, store, l, 100)
	if halted, err := exec.Run(ctx, "run", mustState(t, g, nil)); err != nil || halted.Status != graph.StatusHalted {
		t.Fatalf("Run = %s, %v; want it halted at the gate", halted.Status, err)
	}
	// Readings: before the claim, then before the gate's node runs.
	l.failFromRead(2, errors.New("the record is closed"))
	if _, err := exec.Answer(ctx, "run", "approve"); !errors.Is(err, graph.ErrUsageUnread) {
		t.Fatalf("Answer = %v, want ErrUsageUnread at the reading after the claim", err)
	}
	latest, err := store.Latest(ctx, "run")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if latest.Status != graph.StatusRunning || latest.Position != "gate" || latest.Decision != nil ||
		text(t, latest.State, "answer") != "approve" || rec.count("gate") != 0 {
		t.Fatalf("the run stands %s at %q with decision %v, want running at the gate with the answer recorded",
			latest.Status, latest.Position, latest.Decision)
	}
	l.failFromRead(0, nil)
	if _, err := exec.Answer(ctx, "run", "approve"); !errors.Is(err, graph.ErrNoOpenDecision) {
		t.Fatalf("a second Answer = %v, want ErrNoOpenDecision, since the first was claimed", err)
	}
	done, err := exec.Resume(ctx, "run")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if done.Status != graph.StatusCompleted || rec.count("gate") != 1 || rec.count("work") != 1 {
		t.Fatalf("the run ended %s having run %v, want completed with gate and work run once", done.Status, rec.order())
	}
}

// TestARunPastItsBudgetAtAnOpenDecisionIsParkedOnResume is the stranding a
// failed reading before a halt point could cause: work spends the whole
// budget of 10 and the reading before the gate fails, so the run is recorded
// halted at the gate with work kept. Answering it is refused with
// ErrUsageSpent once the record reads again, and Resume, rather than refusing
// forever, finishes the comparison: the run parks on its usage budget in
// front of the gate with the decision withdrawn and the reading recorded,
// and work ran exactly once.
func TestARunPastItsBudgetAtAnOpenDecisionIsParkedOnResume(t *testing.T) {
	ctx := context.Background()
	rec, l := &recorder{}, newLedger()
	g := mustBuild(t, gateBuilder(rec, l, 10, func() { l.setFailure(errors.New("the record is closed")) }))
	store := graph.NewMemoryStore()
	exec := usageExecutor(t, g, store, l, 10)

	if _, err := exec.Run(ctx, "run", mustState(t, g, nil)); !errors.Is(err, graph.ErrUsageUnread) {
		t.Fatalf("Run = %v, want ErrUsageUnread at the reading before the gate", err)
	}
	if _, err := exec.Resume(ctx, "run"); !errors.Is(err, graph.ErrUsageUnread) {
		t.Fatalf("Resume with the record still unreadable = %v, want ErrUsageUnread", err)
	}
	l.setFailure(nil)
	if _, err := exec.Answer(ctx, "run", "approve"); !errors.Is(err, graph.ErrUsageSpent) {
		t.Fatalf("Answer past the budget = %v, want ErrUsageSpent", err)
	}
	parked, err := exec.Resume(ctx, "run")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if parked.Status != graph.StatusUsageExhausted || parked.Position != "gate" || parked.Decision != nil ||
		parked.ParkedUsage == nil || parked.ParkedUsage.Spent != 10 {
		t.Fatalf("the run is %s at %q with decision %v and reading %+v, want the usage park in front of the gate",
			parked.Status, parked.Position, parked.Decision, parked.ParkedUsage)
	}
	latest, err := store.Latest(ctx, "run")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if err := g.Validate(latest); err != nil || latest.Status != graph.StatusUsageExhausted {
		t.Fatalf("the recorded park is %s (%v), want a valid usage park", latest.Status, err)
	}
	if rec.count("work") != 1 || rec.count("gate") != 0 {
		t.Fatalf("ran %v, want work once and the gate never", rec.order())
	}
	if _, err := exec.Answer(ctx, "run", "approve"); !errors.Is(err, graph.ErrNoOpenDecision) {
		t.Fatalf("answering the parked run = %v, want ErrNoOpenDecision", err)
	}
}
