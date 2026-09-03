package pipeline

import (
	"strconv"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
)

// alwaysFixable is a stage that never stops reporting a fix-eligible finding,
// which is what a loop with no bound would run forever on.
func alwaysFixable(c *calls, stage Stage, stages *Stages) {
	set(stages, stage, recording(c, nil, nil, func(Input, int) (Output, error) {
		return Output{Report: reportWith(findings.ActionFix, "still wrong")}, nil
	}))
}

// movingFixer writes a different head every round, so the run's state is never
// twice the same and convergence can never be what stops it.
func movingFixer(c *calls) Fixer {
	return recordingFixer(c, nil, []Key{KeyHead}, func(in FixInput, call int) (FixOutput, error) {
		return FixOutput{
			Summary: "round " + strconv.Itoa(call),
			Writes:  map[Key]graph.Value{KeyHead: graph.TextValue("c" + strconv.Itoa(call))},
		}, nil
	})
}

// TestTheRoundLimitStopsARunTheOtherTwoBoundsWouldNot gives the run a budget it
// cannot spend and a fixer that changes state every round, so neither the
// budget nor convergence can stop it. Only the stage's own round limit can.
func TestTheRoundLimitStopsARunTheOtherTwoBoundsWouldNot(t *testing.T) {
	const limit = 2
	c := newCalls()
	stages := recordingStages(c)
	alwaysFixable(c, StageReview, &stages)
	p := build(t, Options{
		Stages: stages,
		Fixer:  movingFixer(c),
		Rounds: config.FixRounds{Review: limit},
		Budget: 500,
	})
	_, result := start(t, p, complete())

	if result.Status != graph.StatusRoundsExhausted {
		t.Fatalf("status %s, reason %q, want rounds-exhausted", result.Status, result.Reason)
	}
	if result.Steps >= 500 {
		t.Fatalf("the run spent %d of its 500 steps, so the budget could have stopped it", result.Steps)
	}
	if n := c.fixCount(StageReview); n != limit {
		t.Errorf("the fixer ran %d times, want %d: the limit is the number of rounds, not of re-runs", n, limit)
	}
	if n := c.stageCount(StageReview); n != limit+1 {
		t.Errorf("review ran %d times, want %d: every round is verified", n, limit+1)
	}
}

// TestTheRunBudgetStopsARunTheOtherTwoBoundsWouldNot gives two stages generous
// round limits and a fixer that changes state every round, so neither limit
// nor convergence can stop it. Only the run-wide budget can.
func TestTheRunBudgetStopsARunTheOtherTwoBoundsWouldNot(t *testing.T) {
	const budget = 12
	c := newCalls()
	stages := recordingStages(c)
	alwaysFixable(c, StageRebase, &stages)
	alwaysFixable(c, StageReview, &stages)
	p := build(t, Options{
		Stages: stages,
		Fixer:  movingFixer(c),
		Rounds: config.FixRounds{Rebase: 50, Review: 50},
		Budget: budget,
	})
	_, result := start(t, p, complete())

	if result.Status != graph.StatusBudgetExhausted {
		t.Fatalf("status %s, reason %q, want budget-exhausted", result.Status, result.Reason)
	}
	if result.Steps != budget {
		t.Errorf("the run spent %d steps, want its whole budget of %d", result.Steps, budget)
	}
	if n := c.fixCount(StageRebase); n >= 50 {
		t.Errorf("the rebase fixer ran %d times, so its own limit of 50 could have stopped the run", n)
	}
}

// TestConvergenceStopsARunTheOtherTwoBoundsWouldNot gives the run a generous
// budget and a generous round limit, and a fixer that reports success without
// changing anything. Neither counter would notice before exhausting itself.
func TestConvergenceStopsARunTheOtherTwoBoundsWouldNot(t *testing.T) {
	const limit = 20
	c := newCalls()
	stages := recordingStages(c)
	alwaysFixable(c, StageCI, &stages)
	idle := recordingFixer(c, nil, nil, func(FixInput, int) (FixOutput, error) {
		return FixOutput{Summary: "nothing needed changing"}, nil
	})
	p := build(t, Options{
		Stages: stages,
		Fixer:  idle,
		Rounds: config.FixRounds{Checks: limit},
		Budget: 500,
	})
	_, result := start(t, p, complete())

	if result.Status != graph.StatusConverged {
		t.Fatalf("status %s, reason %q, want converged", result.Status, result.Reason)
	}
	if n := c.fixCount(StageCI); n >= limit {
		t.Errorf("the fixer ran %d times, so the round limit of %d could have stopped the run", n, limit)
	}
	if result.Steps >= 500 {
		t.Errorf("the run spent %d of its 500 steps, so the budget could have stopped it", result.Steps)
	}
}

// TestAFixRoundLimitOfZeroSendsEveryFindingToThePerson is what a limit of zero
// means: the stage has no fixer at all, so a fix-eligible finding holds.
func TestAFixRoundLimitOfZeroSendsEveryFindingToThePerson(t *testing.T) {
	c := newCalls()
	stages := recordingStages(c)
	alwaysFixable(c, StageReview, &stages)
	p := build(t, Options{Stages: stages, Rounds: config.FixRounds{}, Budget: 100})
	_, result := start(t, p, complete())

	if result.Status != graph.StatusHalted || result.Position != StageReview.HoldNode() {
		t.Fatalf("status %s at %q, want halted at %q", result.Status, result.Position, StageReview.HoldNode())
	}
	if n := c.stageCount(StageReview); n != 1 {
		t.Errorf("review ran %d times with no fix rounds, want 1", n)
	}
	if _, ok := p.Graph().Node(StageReview.FixNode()); ok {
		t.Error("a stage with no fix rounds still has a fixer node")
	}
}
