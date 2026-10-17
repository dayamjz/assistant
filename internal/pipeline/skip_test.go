package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/principles"
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
	principles.Cite(t, principles.P2)
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

// TestSkippedDoesNotSayWhetherTheStageRan drives all three producers of
// OutcomeSkipped through the real executor. All three carry the same outcome
// and only the third ran, which is the distinction StageRan exists to make and
// the outcome key cannot.
func TestSkippedDoesNotSayWhetherTheStageRan(t *testing.T) {
	for _, tc := range []struct {
		name string
		ran  bool
		run  func(t *testing.T) graph.State
	}{
		{"named by this run's skip list", false, func(t *testing.T) graph.State {
			t.Helper()
			c := newCalls()
			stages := recordingStages(c)
			p := build(t, Options{Stages: stages, Budget: 100})
			begin := complete()
			begin.Skip = []Stage{StageTest}
			_, result := start(t, p, begin)
			if result.Status != graph.StatusCompleted {
				t.Fatalf("status %s, reason %q, want completed", result.Status, result.Reason)
			}
			if n := c.stageCount(StageTest); n != 0 {
				t.Fatalf("test ran %d times, want 0: this case is the never-ran one", n)
			}
			return result.State
		}},
		{"passed over by the empty-diff short circuit", false, func(t *testing.T) graph.State {
			t.Helper()
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
			if n := c.stageCount(StageTest); n != 0 {
				t.Fatalf("test ran %d times, want 0: this case is the never-ran one", n)
			}
			return result.State
		}},
		{"answered skipped by a person at its hold", true, func(t *testing.T) graph.State {
			t.Helper()
			c := newCalls()
			stages := recordingStages(c)
			alwaysFixable(c, StageTest, &stages)
			p := build(t, Options{Stages: stages, Budget: 100})
			exec, result := start(t, p, complete())
			if result.Status != graph.StatusHalted {
				t.Fatalf("status %s, want halted at %q", result.Status, StageTest.HoldNode())
			}
			answered, err := exec.Answer(context.Background(), "run", string(OutcomeSkipped))
			if err != nil {
				t.Fatalf("Answer: %v", err)
			}
			if n := c.stageCount(StageTest); n == 0 {
				t.Fatal("test never ran, so this case cannot be the ran-and-was-waved-past one")
			}
			return answered.State
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := tc.run(t)
			if got := StageOutcome(state, StageTest); got != OutcomeSkipped {
				t.Fatalf("test outcome %q, want %q: all three produce the same outcome", got, OutcomeSkipped)
			}
			if got := StageRan(state, StageTest); got != tc.ran {
				t.Errorf("StageRan %v, want %v: it answers what the outcome cannot", got, tc.ran)
			}
		})
	}
}

// TestARunWithNoCodeHostPassesOverOnlyTheStagesThatNeedOne is issue #283's
// owner decision at the stage node: a run whose repository has no code host
// walks every stage up to and including the push, then passes over the pull
// request and checks stages with SkipNoCodeHost as the reason, and completes.
// It is P2's other half as well: the stages are passed over, never removed,
// and the person's skip list is left empty, so the reason is the
// repository's and not a choice made on anyone's behalf.
func TestARunWithNoCodeHostPassesOverOnlyTheStagesThatNeedOne(t *testing.T) {
	principles.Cite(t, principles.P2)
	c := newCalls()
	p := build(t, Options{Stages: recordingStages(c), Budget: 100})
	begin := complete()
	begin.NoCodeHost = true
	_, result := start(t, p, begin)

	if result.Status != graph.StatusCompleted {
		t.Fatalf("status %s, reason %q, want completed", result.Status, result.Reason)
	}
	if skip, _ := result.State.Get(string(KeySkip)); len(mustList(t, skip)) != 0 {
		t.Fatalf("the person's skip list is %v; a missing code host must not be written into it", mustList(t, skip))
	}
	for _, stage := range Order() {
		reason, skippedOver := SkipReasonOf(result.State, stage)
		if stage.NeedsCodeHost() {
			if got := StageOutcome(result.State, stage); got != OutcomeSkipped {
				t.Errorf("%s outcome %q, want %q", stage, got, OutcomeSkipped)
			}
			if n := c.stageCount(stage); n != 0 {
				t.Errorf("%s ran %d times in a run with no code host, want 0", stage, n)
			}
			if !skippedOver || reason != SkipNoCodeHost {
				t.Errorf("%s skip reason %q, %v; want %q", stage, reason, skippedOver, SkipNoCodeHost)
			}
			continue
		}
		if got := StageOutcome(result.State, stage); got != OutcomePassed {
			t.Errorf("%s outcome %q, want %q", stage, got, OutcomePassed)
		}
		if n := c.stageCount(stage); n != 1 {
			t.Errorf("%s ran %d times, want 1", stage, n)
		}
		if skippedOver {
			t.Errorf("%s reports skip reason %q and it ran", stage, reason)
		}
	}
	var needing []Stage
	for _, stage := range Order() {
		if stage.NeedsCodeHost() {
			needing = append(needing, stage)
		}
	}
	if len(needing) != 2 || needing[0] != StagePR || needing[1] != StageCI {
		t.Fatalf("the stages needing a code host are %v, want the pull request and checks stages", needing)
	}
}

// TestSkipReasonOfNamesEachRoute is the positive control for SkipReasonOf: a
// stage the person named reads as requested, a stage after an empty diff as
// nothing to change, and a stage a person answered skipped at its hold, whose
// body ran, reads as no reason at all.
func TestSkipReasonOfNamesEachRoute(t *testing.T) {
	c := newCalls()
	p := build(t, Options{Stages: recordingStages(c), Budget: 100})
	begin := complete()
	begin.Skip = []Stage{StageLint, StageCI}
	begin.NoCodeHost = true
	_, result := start(t, p, begin)
	if reason, ok := SkipReasonOf(result.State, StageLint); !ok || reason != SkipRequested {
		t.Errorf("lint skip reason %q, %v; want %q", reason, ok, SkipRequested)
	}
	// Both apply to the checks stage, and the repository's fact is the
	// reason, because it holds whatever the person asked.
	if reason, ok := SkipReasonOf(result.State, StageCI); !ok || reason != SkipNoCodeHost {
		t.Errorf("ci skip reason %q, %v; want %q", reason, ok, SkipNoCodeHost)
	}

	c = newCalls()
	stages := recordingStages(c)
	set(&stages, StageRebase, recording(c, nil, []Key{KeyDiffEmpty}, func(Input, int) (Output, error) {
		return Output{Report: passing(), Writes: map[Key]graph.Value{KeyDiffEmpty: graph.BoolValue(true)}}, nil
	}))
	_, result = start(t, build(t, Options{Stages: stages, Budget: 100}), complete())
	if reason, ok := SkipReasonOf(result.State, StagePR); !ok || reason != SkipNothingToChange {
		t.Errorf("pr skip reason %q, %v; want %q", reason, ok, SkipNothingToChange)
	}

	c = newCalls()
	stages = recordingStages(c)
	alwaysFixable(c, StageTest, &stages)
	exec, _ := start(t, build(t, Options{Stages: stages, Budget: 100}), complete())
	answered, err := exec.Answer(context.Background(), "run", string(OutcomeSkipped))
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if reason, ok := SkipReasonOf(answered.State, StageTest); ok {
		t.Errorf("a stage answered skipped at its hold reports skip reason %q; its body ran", reason)
	}
}

// TestAStartCannotBothNameACodeHostRepositoryAndHaveNone checks NewState
// refuses the contradiction rather than choosing one reading of it.
func TestAStartCannotBothNameACodeHostRepositoryAndHaveNone(t *testing.T) {
	p := build(t, Options{Stages: recordingStages(newCalls()), Budget: 100})
	begin := complete()
	begin.NoCodeHost = true
	begin.ForgeRepository = "owner/name"
	if _, err := p.NewState(begin); !errors.Is(err, ErrCodeHostContradiction) {
		t.Fatalf("NewState = %v, want %v", err, ErrCodeHostContradiction)
	}
}

func mustList(t *testing.T, v graph.Value) []string {
	t.Helper()
	list, ok := v.List()
	if !ok {
		t.Fatalf("value %v is not a list", v)
	}
	return list
}
