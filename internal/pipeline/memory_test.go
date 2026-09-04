package pipeline

import (
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
)

// TestTheStageBodyIsRebuiltEveryRoundAndTheFixerIsNot drives one stage through
// two fix rounds inside a single advance segment and counts constructions
// rather than executions, because that is the only thing that tells a body
// that ran three times apart from three bodies that ran once each.
//
// P4's asymmetry is the claim under test. A review body may carry nothing from
// the round before it, so the stage's constructor must run once per execution;
// a fixer may keep a session across the rounds it is fixing, so its
// constructor must run once for the whole segment. Both halves are asserted
// here, since making either one match the other would break the split.
func TestTheStageBodyIsRebuiltEveryRoundAndTheFixerIsNot(t *testing.T) {
	const rounds = 2
	c := newCalls()
	stages := recordingStages(c)
	review := recording(c, nil, nil, func(_ Input, call int) (Output, error) {
		if call > rounds {
			return Output{Report: passing()}, nil
		}
		return Output{Report: reportWith(findings.ActionFix, "still wrong")}, nil
	})
	set(&stages, StageReview, constructing(c, StageReview, review))
	p := build(t, Options{
		Stages: stages,
		Fixer:  constructingFixer(c, movingFixer(c)),
		Rounds: config.FixRounds{Review: rounds},
		Budget: 200,
	})
	_, result := start(t, p, complete())

	if result.Status != graph.StatusCompleted {
		t.Fatalf("status %s, reason %q, want completed: the run has to reach the end in one segment for the counts to mean anything", result.Status, result.Reason)
	}
	if n := c.fixCount(StageReview); n != rounds {
		t.Fatalf("the fixer ran %d times, want %d: the run did not take the rounds this test counts constructions over", n, rounds)
	}

	executions := c.stageCount(StageReview)
	if executions != rounds+1 {
		t.Fatalf("review ran %d times, want %d: one first look plus one verification per round", executions, rounds+1)
	}
	if n := c.stageNewCount(StageReview); n != executions {
		t.Errorf("review's body was constructed %d times for %d executions, want one per execution: a body built fewer times than it ran is the same Go value in two rounds and can carry anything between them", n, executions)
	}
	if n := c.fixNewCount(); n != 1 {
		t.Errorf("the fixer was constructed %d times across %d rounds, want 1: only the fixer keeps a session across rounds, and rebuilding it takes that away", n, rounds)
	}
}

// TestASkippedStageConstructsNoBody holds the other half of the construction
// rule: the stage node reads whether it runs at all before it builds anything,
// so a stage this run skips costs no body.
func TestASkippedStageConstructsNoBody(t *testing.T) {
	c := newCalls()
	stages := recordingStages(c)
	set(&stages, StageReview, constructing(c, StageReview, recording(c, nil, nil, nil)))
	p := build(t, Options{Stages: stages, Rounds: rounds(0), Budget: 200})

	s := complete()
	s.Skip = []Stage{StageReview}
	_, result := start(t, p, s)

	if result.Status != graph.StatusCompleted {
		t.Fatalf("status %s, reason %q, want completed", result.Status, result.Reason)
	}
	if n := c.stageNewCount(StageReview); n != 0 {
		t.Errorf("a skipped review constructed %d bodies, want 0", n)
	}
}
