package pipeline

import (
	"strconv"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
)

// TestTheStageBodyIsRebuiltEveryRoundAndTheFixerIsNot drives two stages through
// two fix rounds each inside a single advance segment and counts constructions
// rather than executions, because that is the only thing that tells a body
// which ran three times apart from three bodies that ran once each.
//
// The asymmetry is the claim under test. This package hands no stage body to
// the round after it, so a stage's constructor must run once per execution; a
// fixer may keep a session across the rounds of the loop it serves, so its
// constructor must run once per fix node rather than once per round. Two fix
// nodes are what pins the second half: with one stage taking rounds, once per
// node and once per segment predict the same number and neither can fail.
func TestTheStageBodyIsRebuiltEveryRoundAndTheFixerIsNot(t *testing.T) {
	const rounds = 2
	looping := []Stage{StageReview, StageLint}
	c := newCalls()
	stages := recordingStages(c)
	for _, stage := range looping {
		impl := recording(c, nil, nil, func(_ Input, call int) (Output, error) {
			if call > rounds {
				return Output{Report: passing()}, nil
			}
			return Output{Report: reportWith(findings.ActionFix, "still wrong")}, nil
		})
		set(&stages, stage, constructing(c, stage, impl))
	}
	// A head nobody repeats, so convergence can never be what ends a loop and
	// the round limit is left as the only thing that does.
	fixer := recordingFixer(c, nil, []Key{KeyHead}, func(in FixInput, call int) (FixOutput, error) {
		mark := in.Stage.String() + strconv.Itoa(call)
		return FixOutput{
			Summary: "round " + mark,
			Writes:  map[Key]graph.Value{KeyHead: graph.TextValue("c" + mark)},
		}, nil
	})
	p := build(t, Options{
		Stages: stages,
		Fixer:  constructingFixer(c, fixer),
		Rounds: config.FixRounds{Review: rounds, Lint: rounds},
		Budget: 200,
	})
	_, result := start(t, p, complete())

	if result.Status != graph.StatusCompleted {
		t.Fatalf("status %s, reason %q, want completed: the run has to reach the end in one segment for the counts to mean anything", result.Status, result.Reason)
	}
	for _, stage := range looping {
		if n := c.fixCount(stage); n != rounds {
			t.Fatalf("%s's fixer ran %d times, want %d: the run did not take the rounds this test counts constructions over", stage, n, rounds)
		}
		executions := c.stageCount(stage)
		if executions != rounds+1 {
			t.Fatalf("%s ran %d times, want %d: one first look plus one verification per round", stage, executions, rounds+1)
		}
		if n := c.stageNewCount(stage); n != executions {
			t.Errorf("%s's body was constructed %d times for %d executions, want one per execution: a body built fewer times than it ran is the same Go value in two rounds", stage, n, executions)
		}
	}
	if n := c.fixNewCount(); n != len(looping) {
		t.Errorf("the fixer was constructed %d times for %d fix nodes over %d rounds each, want %d: one per fix node, spanning that node's rounds",
			n, len(looping), rounds, len(looping))
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
