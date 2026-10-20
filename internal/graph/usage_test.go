package graph_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/graph"
)

// ledger stands in for the record a run's spend is owned by: node bodies
// append to it the way a stage's agent calls append rows, and its meter
// answers from it as it stands, so nothing the executor is handed is a total
// kept anywhere but here.
type ledger struct {
	mu         sync.Mutex
	spent      map[string]int64
	unreported map[string]int
	reads      int
	fail       error
}

func newLedger() *ledger {
	return &ledger{spent: map[string]int64{}, unreported: map[string]int{}}
}

// spend records one call of the run's reporting n.
func (l *ledger) spend(run string, n int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.spent[run] += n
}

// silent records one call of the run's reporting nothing.
func (l *ledger) silent(run string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.unreported[run]++
}

func (l *ledger) meter(_ context.Context, run string) (graph.Usage, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reads++
	if l.fail != nil {
		return graph.Usage{}, l.fail
	}
	return graph.Usage{Spent: l.spent[run], Unreported: l.unreported[run]}, nil
}

// setFailure makes every later reading fail with err, or succeed again when
// err is nil.
func (l *ledger) setFailure(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fail = err
}

func (l *ledger) readCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.reads
}

// spendingBuilder is four nodes in a line, each spending cost against the
// run it is executing for, with "c" failing on its first execution when
// failC is set, after it spent, which is how a test ends a segment mid-run
// with the spend already in the record.
func spendingBuilder(rec *recorder, l *ledger, cost int64, failC bool) *graph.Builder {
	failed := false
	node := func(name string) graph.Node {
		return graph.Node{Name: name, NewBody: func() graph.Body {
			return func(_ context.Context, _ graph.Reader, _ graph.Writer) error {
				rec.note(name)
				l.spend("run", cost)
				if name == "c" && failC && !failed {
					failed = true
					return errors.New("interrupted")
				}
				return nil
			}
		}}
	}
	return graph.NewBuilder().
		Start("a").
		Key(graph.Key{Name: "unused", Kind: graph.KindInt}).
		Node(node("a")).Node(node("b")).Node(node("c")).Node(node("d")).
		Edge(graph.Edge{From: "a", To: "b"}).
		Edge(graph.Edge{From: "b", To: "c"}).
		Edge(graph.Edge{From: "c", To: "d"})
}

// usageExecutor returns an executor over g with a generous step budget and
// the given usage budget, metered by l.
func usageExecutor(t *testing.T, g *graph.Graph, store graph.CheckpointStore, l *ledger, budget int64) *graph.Executor {
	t.Helper()
	e, err := graph.NewExecutor(g, graph.Config{Store: store, Budget: 50, UsageBudget: budget, Usage: l.meter})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	return e
}

// TestARunUnderItsUsageBudgetCompletes is the control for the park below: a
// budget the run's whole spend stays under parks nothing and runs every node,
// so the park is the budget's doing and not something any metered run gets.
func TestARunUnderItsUsageBudgetCompletes(t *testing.T) {
	ctx := context.Background()
	rec, l := &recorder{}, newLedger()
	g := mustBuild(t, spendingBuilder(rec, l, 10, false))

	got, err := usageExecutor(t, g, graph.NewMemoryStore(), l, 41).Run(ctx, "run", mustState(t, g, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Status != graph.StatusCompleted || len(rec.order()) != 4 {
		t.Fatalf("the run ended %s having run %v, want completed with all four nodes", got.Status, rec.order())
	}
	if got.UsageBudget != 41 {
		t.Errorf("the run reports a usage budget of %d, want the 41 it started under", got.UsageBudget)
	}
	// Once before the first checkpoint is written and once before each of
	// the four nodes.
	if n := l.readCount(); n != 5 {
		t.Errorf("the meter was read %d times, want once before the run is claimed and once before each node", n)
	}
}

// TestARunHeldToNoUsageBudgetIsNeverMetered is what makes zero mean no
// budget rather than a budget nothing reaches: the meter is not asked at all.
func TestARunHeldToNoUsageBudgetIsNeverMetered(t *testing.T) {
	ctx := context.Background()
	rec, l := &recorder{}, newLedger()
	l.silent("run")
	g := mustBuild(t, spendingBuilder(rec, l, 10, false))
	got, err := usageExecutor(t, g, graph.NewMemoryStore(), l, 0).Run(ctx, "run", mustState(t, g, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Status != graph.StatusCompleted || l.readCount() != 0 {
		t.Fatalf("the run ended %s after %d meter reads, want completed with none", got.Status, l.readCount())
	}
}

// TestARunPastItsUsageBudgetParksInFrontOfTheNextNode is the bound itself.
// The run's own calls are what the meter reads, so the run parks in front of
// the first node it reaches with its spend at or past the budget, with the
// park's status, reason and reading saying which bound and what was spent,
// and that node never runs.
func TestARunPastItsUsageBudgetParksInFrontOfTheNextNode(t *testing.T) {
	ctx := context.Background()
	rec, l := &recorder{}, newLedger()
	g := mustBuild(t, spendingBuilder(rec, l, 10, false))
	store := graph.NewMemoryStore()

	got, err := usageExecutor(t, g, store, l, 25).Run(ctx, "run", mustState(t, g, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Status != graph.StatusUsageExhausted || got.Position != "d" {
		t.Fatalf("the run ended %s at %q, want usage_exhausted standing at d", got.Status, got.Position)
	}
	if rec.count("d") != 0 {
		t.Error("the node the run parked in front of ran")
	}
	if !got.Status.Parked() || !got.Status.Stopped() {
		t.Errorf("a usage park reads Parked %v and Stopped %v, want both", got.Status.Parked(), got.Status.Stopped())
	}
	for _, want := range []string{"usage budget of 25", `"d"`, "reported 30"} {
		if !strings.Contains(got.Reason, want) {
			t.Errorf("the reason %q does not say %q", got.Reason, want)
		}
	}
	park, ok := got.Park()
	if !ok || park.Status != graph.StatusUsageExhausted || park.UsageBudget != 25 || park.Edge != nil ||
		park.Usage == nil || park.Usage.Spent != 30 || park.Usage.Unreported != 0 {
		t.Fatalf("the park reads %+v (usage %+v), want the usage budget of 25 and the 30 spent", park, park.Usage)
	}
	latest, err := store.Latest(ctx, "run")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if latest.ParkedUsage == nil || latest.ParkedUsage.Spent != 30 || latest.Counters.UsageBudget != 25 {
		t.Fatalf("the checkpoint records %+v under a usage budget of %d, want the reading and the budget it parked on",
			latest.ParkedUsage, latest.Counters.UsageBudget)
	}
	// A parked run is returned unchanged by a resume, as every park is.
	again, err := usageExecutor(t, g, store, l, 25).Resume(ctx, "run")
	if err != nil || again.Status != graph.StatusUsageExhausted || rec.count("d") != 0 {
		t.Fatalf("resuming the parked run = %s, %v; want it unchanged", again.Status, err)
	}
}

// TestAUsageTotalTheRecordDoesNotEstablishParks is the fail-closed reading:
// a call that reported no count leaves the run's spend unknown, and an
// unknown spend is never taken for room, however far under the budget the
// calls that did report are.
func TestAUsageTotalTheRecordDoesNotEstablishParks(t *testing.T) {
	ctx := context.Background()
	rec, l := &recorder{}, newLedger()
	g := mustBuild(t, spendingBuilder(rec, l, 1, false))
	l.silent("run")

	got, err := usageExecutor(t, g, graph.NewMemoryStore(), l, 1000).Run(ctx, "run", mustState(t, g, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Status != graph.StatusUsageExhausted || got.Position != "a" || len(rec.order()) != 0 {
		t.Fatalf("the run ended %s at %q having run %v, want it parked before a", got.Status, got.Position, rec.order())
	}
	if got.ParkedUsage == nil || got.ParkedUsage.Unreported != 1 || !strings.Contains(got.Reason, "could not be weighed") {
		t.Fatalf("the park records %+v with reason %q, want the unreported call named", got.ParkedUsage, got.Reason)
	}
}

// TestAUsageReadingNobodyCouldMakeIsRefusedNotParked keeps an error from the
// record from becoming a fact about the run: nothing runs, nothing parks, and
// the refusal says what could not be read. It also establishes where each
// refused call leaves the run. A Run refused at its first reading has written
// nothing, so the same Run succeeds once the record reads again rather than
// answering ErrRunExists. A Resume refused before its claim writes nothing,
// so the run still stands at the checkpoint after its last completed node,
// and the next Resume continues from there with no node run twice.
func TestAUsageReadingNobodyCouldMakeIsRefusedNotParked(t *testing.T) {
	ctx := context.Background()
	rec, l := &recorder{}, newLedger()
	g := mustBuild(t, spendingBuilder(rec, l, 10, true))
	store := graph.NewMemoryStore()
	l.setFailure(errors.New("the record is closed"))

	_, err := usageExecutor(t, g, store, l, 100).Run(ctx, "run", mustState(t, g, nil))
	if !errors.Is(err, graph.ErrUsageUnread) || !strings.Contains(err.Error(), "the record is closed") {
		t.Fatalf("Run = %v, want ErrUsageUnread carrying the meter's error", err)
	}
	if len(rec.order()) != 0 {
		t.Errorf("%v ran on a spend nobody read", rec.order())
	}
	if _, err := store.Latest(ctx, "run"); !errors.Is(err, graph.ErrNoSuchRun) {
		t.Fatalf("Latest after the refused Run = %v, want no checkpoint written", err)
	}

	// The same Run again, with the record readable: c fails on its first
	// execution, which leaves the run standing in front of c with a, b done.
	l.setFailure(nil)
	if _, err := usageExecutor(t, g, store, l, 100).Run(ctx, "run", mustState(t, g, nil)); !errors.Is(err, graph.ErrNodeFailed) {
		t.Fatalf("the retried Run = %v, want it to start and reach c's planted failure", err)
	}
	before, err := store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}

	l.setFailure(errors.New("the record is closed"))
	if _, err := usageExecutor(t, g, store, l, 100).Resume(ctx, "run"); !errors.Is(err, graph.ErrUsageUnread) {
		t.Fatalf("Resume with an unreadable record = %v, want ErrUsageUnread", err)
	}
	after, err := store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("the refused Resume wrote %d checkpoints, want none", len(after)-len(before))
	}
	if latest := after[len(after)-1]; latest.Status != graph.StatusRunning || latest.Position != "c" {
		t.Fatalf("the refused run stands %s at %q, want running in front of c", latest.Status, latest.Position)
	}

	l.setFailure(nil)
	done, err := usageExecutor(t, g, store, l, 100).Resume(ctx, "run")
	if err != nil {
		t.Fatalf("Resume with the record readable: %v", err)
	}
	if done.Status != graph.StatusCompleted || rec.count("a") != 1 || rec.count("b") != 1 || rec.count("d") != 1 {
		t.Fatalf("the run ended %s having run %v, want completed with a, b and d run once", done.Status, rec.order())
	}
}

// TestAnUnreadableUsageAtAHaltPointKeepsTheNodeBeforeIt is the reproduction
// of a lost node: work runs, the reading before the halt point that follows
// it fails, and the run must not be left standing in front of work, or a
// resume would run work a second time. The run is recorded halted at the
// gate with work's step and state kept, the refusal is still returned, and
// the budget is weighed again when the decision is answered: refused while
// the record stays unreadable, with the decision still open, and then the
// gate runs and the run completes with work run exactly once.
func TestAnUnreadableUsageAtAHaltPointKeepsTheNodeBeforeIt(t *testing.T) {
	ctx := context.Background()
	rec, l := &recorder{}, newLedger()
	g := mustBuild(t, graph.NewBuilder().
		Start("work").
		Key(graph.Key{Name: "answer", Kind: graph.KindText}).
		Key(graph.Key{Name: "done", Kind: graph.KindInt}).
		Node(graph.Node{Name: "work", Writes: []string{"done"}, NewBody: body(func(_ context.Context, _ graph.Reader, w graph.Writer) error {
			rec.note("work")
			l.spend("run", 10)
			// The reading after work is the one that fails.
			l.setFailure(errors.New("the record is closed"))
			return w.Set("done", graph.IntValue(1))
		})}).
		Node(graph.Node{
			Name:    "gate",
			Reads:   []string{"answer"},
			Halt:    &graph.Halt{Question: "ship it?", Options: []string{"approve"}, Into: "answer"},
			NewBody: noteOnly(rec, "gate"),
		}).
		Edge(graph.Edge{From: "work", To: "gate"}))
	store := graph.NewMemoryStore()
	exec := usageExecutor(t, g, store, l, 100)

	if _, err := exec.Run(ctx, "run", mustState(t, g, nil)); !errors.Is(err, graph.ErrUsageUnread) {
		t.Fatalf("Run = %v, want ErrUsageUnread from the reading before the gate", err)
	}
	latest, err := store.Latest(ctx, "run")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if latest.Status != graph.StatusHalted || latest.Position != "gate" || latest.Decision == nil ||
		latest.Counters.Steps != 1 || number(t, latest.State, "done") != 1 {
		t.Fatalf("the run stands %s at %q after %d steps (decision %v), want halted at the gate with work kept",
			latest.Status, latest.Position, latest.Counters.Steps, latest.Decision)
	}

	if _, err := exec.Answer(ctx, "run", "approve"); !errors.Is(err, graph.ErrUsageUnread) {
		t.Fatalf("Answer with the record still unreadable = %v, want ErrUsageUnread", err)
	}
	if still, _ := store.Latest(ctx, "run"); still.Status != graph.StatusHalted || still.Decision == nil {
		t.Fatalf("the refused answer left the run %s, want its decision still open", still.Status)
	}

	l.setFailure(nil)
	resumed, err := exec.Resume(ctx, "run")
	if err != nil || resumed.Status != graph.StatusHalted {
		t.Fatalf("Resume = %s, %v; want the gate's decision re-emitted", resumed.Status, err)
	}
	done, err := exec.Answer(ctx, "run", "approve")
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if done.Status != graph.StatusCompleted || rec.count("work") != 1 || rec.count("gate") != 1 {
		t.Fatalf("the run ended %s having run %v, want completed with work and gate run once each", done.Status, rec.order())
	}
}

// TestAUsageBudgetSurvivesAResumeWithWhatItIsComparedAgainst is the resume
// half: a run interrupted mid-flight and resumed under the budget it recorded
// is compared against the record as it now stands, its earlier spend
// included, so it parks where an uninterrupted run would have and does not
// start counting again from nothing.
func TestAUsageBudgetSurvivesAResumeWithWhatItIsComparedAgainst(t *testing.T) {
	ctx := context.Background()
	rec, l := &recorder{}, newLedger()
	g := mustBuild(t, spendingBuilder(rec, l, 10, true))
	store := graph.NewMemoryStore()

	if _, err := usageExecutor(t, g, store, l, 35).Run(ctx, "run", mustState(t, g, nil)); !errors.Is(err, graph.ErrNodeFailed) {
		t.Fatalf("Run = %v, want c's first execution to fail", err)
	}
	got, err := usageExecutor(t, g, store, l, 35).Resume(ctx, "run")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	// a, b and the failed c spent 30, under 35, so c runs again and brings
	// the run to 40; d is where it parks. A resume that counted from zero
	// would have run d too.
	if got.Status != graph.StatusUsageExhausted || got.Position != "d" || rec.count("d") != 0 {
		t.Fatalf("the resumed run ended %s at %q having run %v, want it parked before d", got.Status, got.Position, rec.order())
	}
	if got.ParkedUsage == nil || got.ParkedUsage.Spent != 40 {
		t.Fatalf("the park records %+v, want the 40 the run spent across both segments", got.ParkedUsage)
	}
}

// TestAResumeUnderAnotherUsageBudgetIsRefusedUnlessAdopted is ErrBudgetChanged
// for the usage budget: a run is held to the budget it recorded, a resume
// configured with another is refused with nothing written, and AdoptBudget is
// the one way the run moves onto it, which writes the change into the
// history and revives the run the old budget parked.
func TestAResumeUnderAnotherUsageBudgetIsRefusedUnlessAdopted(t *testing.T) {
	ctx := context.Background()
	rec, l := &recorder{}, newLedger()
	g := mustBuild(t, spendingBuilder(rec, l, 10, false))
	store := graph.NewMemoryStore()

	if _, err := usageExecutor(t, g, store, l, 25).Run(ctx, "run", mustState(t, g, nil)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	before, err := store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}

	roomy := usageExecutor(t, g, store, l, 100)
	_, err = roomy.Resume(ctx, "run")
	if !errors.Is(err, graph.ErrBudgetChanged) || !errors.Is(err, graph.ErrUsageBudgetChanged) {
		t.Fatalf("Resume under another usage budget = %v, want ErrUsageBudgetChanged in the ErrBudgetChanged family", err)
	}
	for _, want := range []string{"usage budget of 25", "configured with 100", "AdoptBudget"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not say %q", err, want)
		}
	}
	// The step budget is unchanged, so this is the usage budget's refusal and
	// not the step budget's: the two share a family, not a message.
	if strings.Contains(err.Error(), "step budget") {
		t.Errorf("the refusal %q names the step budget, which did not change", err)
	}
	after, err := store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("the refused resume wrote %d checkpoints", len(after)-len(before))
	}

	got, err := roomy.AdoptBudget(ctx, "run")
	if err != nil {
		t.Fatalf("AdoptBudget: %v", err)
	}
	if got.Status != graph.StatusRunning || got.UsageBudget != 100 || got.ParkedUsage != nil || got.Reason != "" {
		t.Fatalf("the adopted run is %s under %d carrying %+v and %q, want it running under 100 with the park left behind",
			got.Status, got.UsageBudget, got.ParkedUsage, got.Reason)
	}
	history, err := store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if n := len(history); history[n-1].Counters.UsageBudget != 100 || history[n-2].Counters.UsageBudget != 25 {
		t.Fatalf("the history does not record the change from 25 to 100")
	}
	done, err := roomy.Resume(ctx, "run")
	if err != nil {
		t.Fatalf("Resume after adopting: %v", err)
	}
	if done.Status != graph.StatusCompleted || rec.count("d") != 1 {
		t.Fatalf("the adopted run ended %s having run %v, want it completed with d run once", done.Status, rec.order())
	}
}

// TestAdoptingAUsageBudgetThatStillLeavesNoRoomParksAgain re-decides rather
// than releasing: the revival reads the record afresh and parks again, with
// the reason naming the budget the run now carries.
func TestAdoptingAUsageBudgetThatStillLeavesNoRoomParksAgain(t *testing.T) {
	ctx := context.Background()
	rec, l := &recorder{}, newLedger()
	g := mustBuild(t, spendingBuilder(rec, l, 10, false))
	store := graph.NewMemoryStore()
	if _, err := usageExecutor(t, g, store, l, 25).Run(ctx, "run", mustState(t, g, nil)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, err := usageExecutor(t, g, store, l, 30).AdoptBudget(ctx, "run")
	if err != nil {
		t.Fatalf("AdoptBudget: %v", err)
	}
	if got.Status != graph.StatusUsageExhausted || got.UsageBudget != 30 || !strings.Contains(got.Reason, "usage budget of 30") {
		t.Fatalf("the run is %s under %d with %q, want it parked again under 30", got.Status, got.UsageBudget, got.Reason)
	}
}

// TestARunWithNoUsageRoomParksAtTheHaltPointWithoutAsking is haltBefore's rule
// for the usage budget: a decision the run could not act on is not put to a
// person. And an answer to a decision the run can no longer afford, after its
// budget was lowered, is refused with the decision left open.
func TestARunWithNoUsageRoomParksAtTheHaltPointWithoutAsking(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	// The ledger prep spends against is chosen per run below; prep spends 10.
	var l *ledger
	g := mustBuild(t, graph.NewBuilder().
		Start("prep").
		Key(graph.Key{Name: "answer", Kind: graph.KindText}).
		Node(graph.Node{Name: "prep", NewBody: body(func(context.Context, graph.Reader, graph.Writer) error {
			l.spend("run", 10)
			return nil
		})}).
		Node(graph.Node{
			Name:    "gate",
			Reads:   []string{"answer"},
			Halt:    &graph.Halt{Question: "ship it?", Options: []string{"approve", "cancel"}, Into: "answer"},
			NewBody: noteOnly(rec, "gate"),
		}).
		Edge(graph.Edge{From: "prep", To: "gate"}))

	l = newLedger()
	parked, err := usageExecutor(t, g, graph.NewMemoryStore(), l, 10).Run(ctx, "run", mustState(t, g, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if parked.Status != graph.StatusUsageExhausted || parked.Position != "gate" || parked.Decision != nil {
		t.Fatalf("the run ended %s at %q carrying %v, want it parked at gate asking nothing",
			parked.Status, parked.Position, parked.Decision)
	}

	// A second run, halted under a budget with room, whose budget is then
	// lowered past what it spent: the answer is refused and nothing written.
	l = newLedger()
	store := graph.NewMemoryStore()
	halted, err := usageExecutor(t, g, store, l, 11).Run(ctx, "run", mustState(t, g, nil))
	if err != nil || halted.Status != graph.StatusHalted {
		t.Fatalf("Run = %s, %v; want it halted at the gate", halted.Status, err)
	}
	tight := usageExecutor(t, g, store, l, 5)
	if _, err := tight.AdoptBudget(ctx, "run"); err != nil {
		t.Fatalf("AdoptBudget: %v", err)
	}
	_, err = tight.Answer(ctx, "run", "approve")
	if !errors.Is(err, graph.ErrUsageSpent) || !errors.Is(err, graph.ErrBudgetSpent) {
		t.Fatalf("Answer with no usage room = %v, want ErrUsageSpent in the ErrBudgetSpent family", err)
	}
	latest, err := store.Latest(ctx, "run")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if latest.Status != graph.StatusHalted || latest.Decision == nil || rec.count("gate") != 0 {
		t.Fatalf("the refused answer left the run %s carrying %v, want the decision still open", latest.Status, latest.Decision)
	}
}

// TestAnExecutorWithAUsageBudgetAndNoMeterFailsToBuild is the
// construction-time half: a usage budget nothing can be compared against is
// refused before any run exists, rather than parking every run or bounding
// nothing, and a negative budget is refused too. The control is the same
// configuration with a meter.
func TestAnExecutorWithAUsageBudgetAndNoMeterFailsToBuild(t *testing.T) {
	g := mustBuild(t, spendingBuilder(&recorder{}, newLedger(), 1, false))
	store := graph.NewMemoryStore()
	if _, err := graph.NewExecutor(g, graph.Config{Store: store, Budget: 5, UsageBudget: 10}); err == nil {
		t.Error("an executor with a usage budget and no meter was built")
	}
	if _, err := graph.NewExecutor(g, graph.Config{Store: store, Budget: 5, UsageBudget: -1, Usage: newLedger().meter}); err == nil {
		t.Error("an executor with a negative usage budget was built")
	}
	if _, err := graph.NewExecutor(g, graph.Config{Store: store, Budget: 5, UsageBudget: 10, Usage: newLedger().meter}); err != nil {
		t.Errorf("the metered executor was refused: %v", err)
	}
}

// TestValidateHoldsAUsageParkToTheBudgetItNames refuses a usage reading the
// executor could not have written: on another status, missing from a usage
// park, or one that leaves room under the budget the run recorded. The
// control is the checkpoint the executor wrote, which passes and round-trips
// through the wire format with its reading intact.
func TestValidateHoldsAUsageParkToTheBudgetItNames(t *testing.T) {
	ctx := context.Background()
	l := newLedger()
	g := mustBuild(t, spendingBuilder(&recorder{}, l, 10, false))
	store := graph.NewMemoryStore()
	if _, err := usageExecutor(t, g, store, l, 25).Run(ctx, "run", mustState(t, g, nil)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	parked, err := store.Latest(ctx, "run")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if err := g.Validate(parked); err != nil {
		t.Fatalf("the executor's own usage park is refused: %v", err)
	}
	wire, err := json.Marshal(parked)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var back graph.Checkpoint
	if err := json.Unmarshal(wire, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if back.ParkedUsage == nil || *back.ParkedUsage != *parked.ParkedUsage || back.Counters.UsageBudget != 25 ||
		back.Status != graph.StatusUsageExhausted {
		t.Fatalf("the wire format carried %+v under %d (%s), want the reading and budget intact",
			back.ParkedUsage, back.Counters.UsageBudget, back.Status)
	}

	cases := map[string]func(c *graph.Checkpoint){
		"no reading on a usage park": func(c *graph.Checkpoint) { c.ParkedUsage = nil },
		"a reading with room left":   func(c *graph.Checkpoint) { c.ParkedUsage = &graph.Usage{Spent: 24} },
		"a usage park with no budget": func(c *graph.Checkpoint) {
			c.Counters.UsageBudget = 0
		},
		"a reading on a step budget park": func(c *graph.Checkpoint) { c.Status = graph.StatusBudgetExhausted },
		"a negative usage budget":         func(c *graph.Checkpoint) { c.Counters.UsageBudget = -1 },
		"a negative reading":              func(c *graph.Checkpoint) { c.ParkedUsage = &graph.Usage{Spent: 30, Unreported: -1} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			bad := back
			usage := *back.ParkedUsage
			bad.ParkedUsage = &usage
			mutate(&bad)
			var ce *graph.CheckpointError
			if err := g.Validate(bad); !errors.As(err, &ce) {
				t.Fatalf("Validate = %v, want a *CheckpointError", err)
			}
		})
	}
}
