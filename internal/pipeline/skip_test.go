package pipeline

import (
	"context"
	"testing"

	"github.com/dayamjz/assistant/internal/graph"
)

// TestAnEmptyDiffAfterRebaseCompletesTheRunAndSkipsTheRest is PRD section 5's
// short circuit: when nothing remains to change the run ends successfully with
// the remaining stages skipped, not failed.
func TestAnEmptyDiffAfterRebaseCompletesTheRunAndSkipsTheRest(t *testing.T) {
	c := newCalls()
	stages := recordingStages(c)
	set(&stages, StageRebase, recording(c, nil, []Key{KeyDiffEmpty}, func(Input, int) (Output, error) {
		return Output{
			Report: passing(),
			Writes: map[Key]graph.Value{KeyDiffEmpty: graph.BoolValue(true)},
		}, nil
	}))
	p := build(t, Options{Stages: stages, Budget: 100})
	_, result := start(t, p, complete())

	if result.Status != graph.StatusCompleted {
		t.Fatalf("status %s, reason %q, want completed", result.Status, result.Reason)
	}
	ran := []Stage{StageIntent, StageRebase}
	rest := []Stage{StageReview, StageTest, StageDocument, StageLint, StagePush, StagePR, StageCI}
	for _, stage := range ran {
		if got := StageOutcome(result.State, stage); got != OutcomePassed {
			t.Errorf("%s outcome %q, want %q", stage, got, OutcomePassed)
		}
	}
	for _, stage := range rest {
		if got := StageOutcome(result.State, stage); got != OutcomeSkipped {
			t.Errorf("%s outcome %q, want %q", stage, got, OutcomeSkipped)
		}
		if n := c.stageCount(stage); n != 0 {
			t.Errorf("%s ran %d times after an empty diff, want 0", stage, n)
		}
	}
}

// TestARunMaySkipAStageOnPurpose is the half of P2 that says a person may skip
// a stage for one run. The other half, that no standing configuration may,
// is TestConfigurationCannotChangeWhichStagesRun: Options has no way to say
// this, and Start is the only place that does.
func TestARunMaySkipAStageOnPurpose(t *testing.T) {
	c := newCalls()
	p := build(t, Options{Stages: recordingStages(c), Budget: 100})
	begin := complete()
	begin.Skip = []Stage{StageLint, StageCI}
	_, result := start(t, p, begin)

	if result.Status != graph.StatusCompleted {
		t.Fatalf("status %s, reason %q, want completed", result.Status, result.Reason)
	}
	for _, stage := range Order() {
		want := OutcomePassed
		wantRuns := 1
		if stage == StageLint || stage == StageCI {
			want, wantRuns = OutcomeSkipped, 0
		}
		if got := StageOutcome(result.State, stage); got != want {
			t.Errorf("%s outcome %q, want %q", stage, got, want)
		}
		if n := c.stageCount(stage); n != wantRuns {
			t.Errorf("%s ran %d times, want %d", stage, n, wantRuns)
		}
	}
}

// TestSkippingAtAHoldSkipsOnlyThatStage checks the second way a stage is
// skipped: a person answering its hold.
func TestSkippingAtAHoldSkipsOnlyThatStage(t *testing.T) {
	c := newCalls()
	stages := recordingStages(c)
	alwaysFixable(c, StageTest, &stages)
	p := build(t, Options{Stages: stages, Budget: 100})
	exec, result := start(t, p, complete())

	if result.Status != graph.StatusHalted || result.Position != StageTest.HoldNode() {
		t.Fatalf("status %s at %q, want halted at %q", result.Status, result.Position, StageTest.HoldNode())
	}
	answered, err := exec.Answer(context.Background(), "run", string(OutcomeSkipped))
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if answered.Status != graph.StatusCompleted {
		t.Fatalf("status %s, reason %q, want completed", answered.Status, answered.Reason)
	}
	if got := StageOutcome(answered.State, StageTest); got != OutcomeSkipped {
		t.Errorf("test outcome %q, want %q", got, OutcomeSkipped)
	}
	if got := StageOutcome(answered.State, StageDocument); got != OutcomePassed {
		t.Errorf("document outcome %q, want %q: only the held stage was skipped", got, OutcomePassed)
	}
}

// TestCancellingAtAHoldEndsTheRun checks the third hold answer. The graph
// completes, because the node the run ends at is terminal, and the state is
// what says which of the two ends it reached.
func TestCancellingAtAHoldEndsTheRun(t *testing.T) {
	c := newCalls()
	stages := recordingStages(c)
	alwaysFixable(c, StageReview, &stages)
	p := build(t, Options{Stages: stages, Budget: 100})
	exec, result := start(t, p, complete())

	if result.Status != graph.StatusHalted {
		t.Fatalf("status %s, want halted", result.Status)
	}
	answered, err := exec.Answer(context.Background(), "run", string(OutcomeCancelled))
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if answered.Status != graph.StatusCompleted {
		t.Fatalf("status %s, want completed", answered.Status)
	}
	if !Cancelled(answered.State) {
		t.Error("a cancelled run does not report as cancelled")
	}
	for _, later := range []Stage{StageTest, StageDocument, StageLint, StagePush, StagePR, StageCI} {
		if n := c.stageCount(later); n != 0 {
			t.Errorf("%s ran %d times after the run was cancelled, want 0", later, n)
		}
	}
}

// TestAHoldAcceptsOnlyTheOutcomesAPersonMayGive checks the halt point refuses
// an answer outside its options, which is what lets the hold node record the
// answer as the outcome without translating it.
func TestAHoldAcceptsOnlyTheOutcomesAPersonMayGive(t *testing.T) {
	c := newCalls()
	stages := recordingStages(c)
	alwaysFixable(c, StageReview, &stages)
	p := build(t, Options{Stages: stages, Budget: 100})
	exec, result := start(t, p, complete())
	if result.Status != graph.StatusHalted {
		t.Fatalf("status %s, want halted", result.Status)
	}
	for _, answer := range []string{"", "passed", "held", "fixable", "yes"} {
		if _, err := exec.Answer(context.Background(), "run", answer); err == nil {
			t.Errorf("answering %q was accepted", answer)
		}
	}
}
