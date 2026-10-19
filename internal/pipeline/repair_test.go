package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
)

// forwardingPush is a push stage that forwards the run's head, which is what
// the real one records under KeyPushed once it has pushed.
func forwardingPush(c *calls) Implementation {
	return recording(c, []Key{KeyHead}, []Key{KeyPushed}, func(in Input, _ int) (Output, error) {
		head, err := in.State.Get(KeyHead)
		if err != nil {
			return Output{}, err
		}
		return Output{Report: passing(), Writes: map[Key]graph.Value{KeyPushed: head}}, nil
	})
}

// repairingChecks is a checks stage that reports a failing check, as a fix
// finding, on each of its first failing reads of a head it pushed, and asks
// for the path back to review whenever it runs on a head a fix round moved
// off the commit it pushed, which is what the real body does. A failing of -1
// fails every read.
func repairingChecks(c *calls, failing int) Implementation {
	reads := []Key{KeyHead, KeyPushed, StageCI.FixKey()}
	reds := 0
	return recording(c, reads, nil, func(in Input, _ int) (Output, error) {
		text := func(k Key) string {
			v, _ := in.State.Get(k)
			s, _ := v.Text()
			return s
		}
		if text(StageCI.FixKey()) != "" && text(KeyHead) != text(KeyPushed) {
			return Output{Report: findings.Report{Summary: "a fix round repaired the failing check"}, Repair: true}, nil
		}
		if failing < 0 || reds < failing {
			reds++
			return Output{Report: reportWith(findings.ActionFix, "the build check failed")}, nil
		}
		return Output{Report: passing()}, nil
	})
}

// committingFixer is a fixer whose every round commits, moving the run's head
// to a commit no earlier round wrote.
func committingFixer(c *calls) Fixer {
	return recordingFixer(c, nil, []Key{KeyHead}, func(in FixInput, call int) (FixOutput, error) {
		return FixOutput{
			Summary: "repaired the build",
			Writes:  map[Key]graph.Value{KeyHead: graph.TextValue(fmt.Sprintf("repair-%d", call))},
		}, nil
	})
}

// repairPipeline is the nine recording stages with the forwarding push, the
// repairing checks stage, and the committing fixer, with checks rounds of
// automatic fix rounds at the checks stage and none anywhere else.
func repairPipeline(t *testing.T, c *calls, checks, failing int) *Pipeline {
	t.Helper()
	s := recordingStages(c)
	set(&s, StagePush, forwardingPush(c))
	set(&s, StageCI, repairingChecks(c, failing))
	return build(t, Options{
		Stages: s,
		Fixer:  committingFixer(c),
		Rounds: config.FixRounds{Checks: checks},
		Budget: 100,
	})
}

// A check that fails after the push is repaired by the checks stage's fix
// round, and the repair goes back to the review stage: the run reviews it,
// runs the checks before push and pushes it again, and reads the checks again,
// where they pass and the run completes with no hold. The path is a back edge
// built carrying the checks stage's round limit, and the record says one
// repair was sent back and republished.
//
// The controls are the tests below: the same repair with no fix rounds has no
// path to take, and repairs that keep failing park on the round limit.
func TestARepairAfterThePushGoesBackThroughReviewAndThePush(t *testing.T) {
	c := newCalls()
	p := repairPipeline(t, c, 2, 1)

	var path *graph.Edge
	for i, e := range p.Graph().Edges() {
		if e.From == StageCI.Node() && e.To == StageReview.Node() {
			if !p.Graph().IsBackEdge(i) {
				t.Fatalf("the path back from checks to review is not a back edge, so no bound is required of it")
			}
			path = &e
		}
	}
	if path == nil || path.Rounds != 2 {
		t.Fatalf("the path back from checks to review is %+v, want an edge bounded by the checks round limit, 2", path)
	}

	_, result := start(t, p, complete())
	if result.Status != graph.StatusCompleted {
		t.Fatalf("the run is %s at %q, want completed after one repair", result.Status, result.Position)
	}
	for stage, want := range map[Stage]int{
		StageIntent: 1, StageRebase: 1,
		StageReview: 2, StageTest: 2, StageDocument: 2, StageLint: 2, StagePush: 2, StagePR: 2,
		StageCI: 3,
	} {
		if got := c.stageCount(stage); got != want {
			t.Errorf("%s ran %d times, want %d", stage, got, want)
		}
	}
	if got := c.fixCount(StageCI); got != 1 {
		t.Errorf("the checks stage took %d fix rounds, want one", got)
	}
	if got := StageOutcome(result.State, StageCI); got != OutcomePassed {
		t.Errorf("the checks stage ended %q, want passed", got)
	}
	repairs, err := RepairsOf(result.State)
	if err != nil {
		t.Fatalf("RepairsOf: %v", err)
	}
	want := []Repair{{Pushed: "c0", Repaired: "repair-1", Republished: true}}
	if fmt.Sprint(repairs) != fmt.Sprint(want) {
		t.Fatalf("the run records repairs %+v, want %+v", repairs, want)
	}
}

// Repairs that keep failing are bounded by the checks stage's fix round
// limit, which the owner decided bounds them: with a limit of two the run
// repairs twice, takes the path back twice, and on the third failing read
// parks rounds-exhausted in front of the checks stage's fixer, naming the
// edge into it, rather than on the path back. The step budget is generous, so
// it is the round limit and not the budget that stops it.
func TestRepairsThatKeepFailingParkOnTheChecksRoundLimit(t *testing.T) {
	c := newCalls()
	_, result := start(t, repairPipeline(t, c, 2, -1), complete())

	if result.Status != graph.StatusRoundsExhausted || result.Position != StageCI.FixNode() {
		t.Fatalf("the run is %s at %q, want parked rounds-exhausted in front of %q",
			result.Status, result.Position, StageCI.FixNode())
	}
	if result.ParkedAt == nil || result.ParkedAt.From != StageCI.Node() || result.ParkedAt.To != StageCI.FixNode() {
		t.Fatalf("the park names edge %+v, want the edge into the checks stage's fixer", result.ParkedAt)
	}
	if got := c.fixCount(StageCI); got != 2 {
		t.Errorf("the checks stage took %d fix rounds, want its limit, 2", got)
	}
	if got := c.stageCount(StageReview); got != 3 {
		t.Errorf("review ran %d times, want 3: the first pass and once per repair", got)
	}
	repairs, err := RepairsOf(result.State)
	if err != nil {
		t.Fatalf("RepairsOf: %v", err)
	}
	if len(repairs) != 2 || !repairs[0].Republished || !repairs[1].Republished {
		t.Fatalf("the run records repairs %+v, want two, each pushed again", repairs)
	}
}

// With no fix round at the checks stage there is no repair, so the pipeline
// builds no path back to review and a body asking for one fails the step with
// ErrRepairNotOffered rather than having the outcome fall through to the
// stage's way forward, where a commit nothing reviewed would read as a pass.
// Every other stage asking fails the same way, with or without rounds. The
// control is the first test above, where the checks stage with rounds takes
// the path.
func TestOnlyTheChecksStageWithFixRoundsMayAskToGoBackToReview(t *testing.T) {
	for _, stage := range Order() {
		for _, checks := range []int{0, 1} {
			if stage.RepairsAfterPush() && checks > 0 {
				continue
			}
			t.Run(fmt.Sprintf("%s with %d checks rounds", stage, checks), func(t *testing.T) {
				c := newCalls()
				s := recordingStages(c)
				set(&s, stage, recording(c, nil, nil, func(Input, int) (Output, error) {
					return Output{Report: passing(), Repair: true}, nil
				}))
				p := build(t, Options{Stages: s, Fixer: committingFixer(c), Rounds: config.FixRounds{Checks: checks}, Budget: 100})
				for _, e := range p.Graph().Edges() {
					if checks == 0 && e.From == StageCI.Node() && e.To == StageReview.Node() {
						t.Fatalf("a pipeline whose checks stage takes no rounds built the path back to review")
					}
				}
				if err := runErr(t, p); !errors.Is(err, ErrRepairNotOffered) {
					t.Fatalf("%s asking for the path back to review: %v, want ErrRepairNotOffered", stage, err)
				}
			})
		}
	}
	if !StageCI.RepairsAfterPush() {
		t.Fatalf("the checks stage is not given the path back to review, so nothing can take it")
	}
}

// A request for both paths back in one execution fails the step: the two
// answer different findings and the topology takes one edge.
func TestAStageMayNotAskForBothPathsBack(t *testing.T) {
	c := newCalls()
	s := recordingStages(c)
	set(&s, StageCI, recording(c, nil, nil, func(Input, int) (Output, error) {
		return Output{Report: passing(), Rebase: true, Repair: true}, nil
	}))
	p := build(t, Options{Stages: s, Fixer: committingFixer(c), Rounds: config.FixRounds{Checks: 1}, Budget: 100})
	if err := runErr(t, p); !errors.Is(err, ErrTwoPathsBack) {
		t.Fatalf("asking for both paths back: %v, want ErrTwoPathsBack", err)
	}
}

// A hold is never traded for a repair: a repair whose report holds, as one
// carrying the judge rule's hold does, stays at the checks stage's hold, and
// records no repair. The control is the first test above, where the same
// request over a passing report takes the path.
func TestAHoldWinsOverARequestToGoBackToReview(t *testing.T) {
	c := newCalls()
	s := recordingStages(c)
	set(&s, StagePush, forwardingPush(c))
	reads := []Key{StageCI.FixKey()}
	set(&s, StageCI, recording(c, reads, nil, func(in Input, _ int) (Output, error) {
		v, _ := in.State.Get(StageCI.FixKey())
		if text, _ := v.Text(); text != "" {
			return Output{Report: reportWith(findings.ActionAsk, "the round edited a judge path"), Repair: true}, nil
		}
		return Output{Report: reportWith(findings.ActionFix, "the build check failed")}, nil
	}))
	p := build(t, Options{Stages: s, Fixer: committingFixer(c), Rounds: config.FixRounds{Checks: 1}, Budget: 100})
	_, result := start(t, p, complete())
	if result.Status != graph.StatusHalted || result.Position != StageCI.HoldNode() {
		t.Fatalf("the run is %s at %q, want halted at %q", result.Status, result.Position, StageCI.HoldNode())
	}
	if got := c.stageCount(StageReview); got != 1 {
		t.Fatalf("review ran %d times, want once: the request should not have been taken", got)
	}
	if repairs, err := RepairsOf(result.State); err != nil || len(repairs) != 0 {
		t.Fatalf("a held repair recorded %+v (%v), want nothing", repairs, err)
	}
}

// The path back to review closes a cycle, so the graph refuses it without a
// bound: rebuilding the pipeline's own topology with that one edge's bound
// removed fails to build under the bounded-cycle rule. The control is the
// same rebuild with the bound left in place, which builds, so the refusal is
// the bound's and not the rebuild's.
func TestThePathBackToReviewWithoutABoundFailsToBuild(t *testing.T) {
	p := repairPipeline(t, newCalls(), 1, 1)
	rebuild := func(strip bool) error {
		g := p.Graph()
		b := graph.NewBuilder().Start(g.Start())
		for _, k := range g.Keys() {
			b.Key(k)
		}
		for _, n := range g.Nodes() {
			b.Node(n)
		}
		stripped := false
		for _, e := range g.Edges() {
			if strip && e.From == StageCI.Node() && e.To == StageReview.Node() {
				e.Rounds = 0
				stripped = true
			}
			b.Edge(e)
		}
		if strip && !stripped {
			t.Fatalf("the topology has no path back to review to strip")
		}
		_, err := b.Build()
		return err
	}
	if err := rebuild(false); err != nil {
		t.Fatalf("the pipeline's own topology does not rebuild: %v", err)
	}
	err := rebuild(true)
	var build *graph.BuildError
	if !errors.As(err, &build) || !build.HasRule(graph.RuleBoundedCycle) {
		t.Fatalf("the path back to review with no bound built: %v, want a %s refusal", err, graph.RuleBoundedCycle)
	}
}

// A hold on the path back to review is left once a pass, and the path makes a
// pass per repair, so the bound the graph requires on the hold's exit has
// room for every repair the checks round limit allows as well as the passes
// back to rebase. Here the lint stage holds on every pass and is approved,
// and the checks stage repairs three times: the run makes four passes, one
// more than the first pass and the passes back to rebase account for, and
// completes rather than parking on the hold's exit.
func TestAHoldOnThePathBackToReviewIsLeftOncePerRepair(t *testing.T) {
	c := newCalls()
	s := recordingStages(c)
	set(&s, StagePush, forwardingPush(c))
	set(&s, StageCI, repairingChecks(c, 3))
	set(&s, StageLint, recording(c, nil, nil, func(Input, int) (Output, error) {
		return Output{Report: reportWith(findings.ActionAsk, "a lint warning for a person")}, nil
	}))
	p := build(t, Options{Stages: s, Fixer: committingFixer(c), Rounds: config.FixRounds{Checks: 3}, Budget: 100})
	exec, result := start(t, p, complete())
	for answers := 0; result.Status == graph.StatusHalted; answers++ {
		if result.Position != StageLint.HoldNode() || answers > 4 {
			t.Fatalf("the run is halted at %q after %d answers, want the lint hold at most four times", result.Position, answers)
		}
		var err error
		if result, err = exec.Answer(context.Background(), "run", string(OutcomeApproved)); err != nil {
			t.Fatalf("Answer: %v", err)
		}
	}
	if result.Status != graph.StatusCompleted {
		t.Fatalf("the run is %s at %q on edge %+v, want completed after three repairs",
			result.Status, result.Position, result.ParkedAt)
	}
	if got := c.stageCount(StageLint); got != 4 {
		t.Fatalf("lint ran %d times, want four: the first pass and one per repair", got)
	}
}

// RepairAlreadySent reports whether the same repair was recorded earlier.
func TestRepairAlreadySent(t *testing.T) {
	t.Parallel()
	p := repairPipeline(t, newCalls(), 1, 1)
	state, err := p.NewState(complete())
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	reader := stateReader{state}
	if got, err := RepairAlreadySent(reader, "p0", "r1"); err != nil || got {
		t.Fatalf("empty state: got %v, %v, want false", got, err)
	}
	repairs, err := RepairsOf(state)
	if err != nil || len(repairs) != 0 {
		t.Fatalf("RepairsOf empty: %+v, %v", repairs, err)
	}
	entry, err := repairValue("p0", "r1")
	if err != nil {
		t.Fatalf("repairValue: %v", err)
	}
	encoded, err := json.Marshal(map[string]graph.Value{string(KeyRepairs): entry})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := json.Unmarshal(encoded, &state); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	reader = stateReader{state}
	if got, err := RepairAlreadySent(reader, "p0", "r1"); err != nil || !got {
		t.Fatalf("recorded repair: got %v, %v, want true", got, err)
	}
	if got, err := RepairAlreadySent(reader, "p0", "r2"); err != nil || got {
		t.Fatalf("different repair: got %v, %v, want false", got, err)
	}
}

type stateReader struct{ graph.State }

func (s stateReader) Get(k Key) (graph.Value, error) {
	v, ok := s.State.Get(string(k))
	if !ok {
		return graph.Value{}, nil
	}
	return v, nil
}

// runErr runs a pipeline from a complete Start and returns the error the run
// stopped on.
func runErr(t *testing.T, p *Pipeline) error {
	t.Helper()
	exec, err := p.Executor(graph.NewMemoryStore())
	if err != nil {
		t.Fatalf("Executor: %v", err)
	}
	state, err := p.NewState(complete())
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	_, err = exec.Run(context.Background(), "run", state)
	return err
}
