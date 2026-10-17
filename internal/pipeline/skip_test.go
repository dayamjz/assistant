package pipeline

import (
	"context"
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

// TestSkippedDoesNotSayWhetherTheStageRan drives the producers of
// OutcomeSkipped through the real executor. They carry the same outcome and
// only the person-answered case ran, which is the distinction StageRan exists
// to make and the outcome key cannot.
func TestSkippedDoesNotSayWhetherTheStageRan(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stage  Stage
		ran    bool
		reason SkipReason
		run    func(t *testing.T) graph.State
	}{
		{"named by this run's skip list", StageTest, false, SkipRequested, func(t *testing.T) graph.State {
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
		{"passed over by the empty-diff short circuit", StageTest, false, SkipEmptyDiff, func(t *testing.T) graph.State {
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
		{"no code host skips the pull request stage", StagePR, false, SkipNoCodeHost, func(t *testing.T) graph.State {
			t.Helper()
			c := newCalls()
			stages := recordingStages(c)
			p := build(t, Options{Stages: stages, Budget: 100})
			begin := complete()
			begin.NoCodeHost = true
			_, result := start(t, p, begin)
			if result.Status != graph.StatusCompleted {
				t.Fatalf("status %s, reason %q, want completed", result.Status, result.Reason)
			}
			if n := c.stageCount(StagePR); n != 0 {
				t.Fatalf("pr ran %d times, want 0: this case is the never-ran one", n)
			}
			if !NoCodeHost(result.State) {
				t.Fatal("the run's state does not carry KeyNoCodeHost")
			}
			return result.State
		}},
		{"answered skipped by a person at its hold", StageTest, true, "", func(t *testing.T) graph.State {
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
			if got := StageOutcome(state, tc.stage); got != OutcomeSkipped {
				t.Fatalf("%s outcome %q, want %q: every producer yields the same outcome", tc.stage, got, OutcomeSkipped)
			}
			if got := StageRan(state, tc.stage); got != tc.ran {
				t.Errorf("StageRan %v, want %v: it answers what the outcome cannot", got, tc.ran)
			}
			if got := SkipReasonOf(state, tc.stage); got != tc.reason {
				t.Errorf("SkipReasonOf %q, want %q: the reason is the one the stage node decided with", got, tc.reason)
			}
		})
	}
}

// TestASkipReasonIsTheOneTheStageNodeDecidedWith runs the cases where two of
// the three facts hold at once, through the real executor, so the reason read
// afterwards is the one the node applied first rather than any that holds. A
// stage decided before rebase wrote KeyDiffEmpty keeps the reason it was
// passed over for, and a stage that ran has no reason.
func TestASkipReasonIsTheOneTheStageNodeDecidedWith(t *testing.T) {
	t.Run("a stage before rebase is not credited to the empty diff", func(t *testing.T) {
		c := newCalls()
		stages := recordingStages(c)
		set(&stages, StageRebase, recording(c, nil, []Key{KeyDiffEmpty}, func(Input, int) (Output, error) {
			return Output{
				Report: passing(),
				Writes: map[Key]graph.Value{KeyDiffEmpty: graph.BoolValue(true)},
			}, nil
		}))
		p := build(t, Options{Stages: stages, Budget: 100})
		begin := complete()
		begin.Skip = []Stage{StageIntent}
		_, result := start(t, p, begin)
		if result.Status != graph.StatusCompleted {
			t.Fatalf("status %s, reason %q, want completed", result.Status, result.Reason)
		}
		for stage, want := range map[Stage]SkipReason{
			StageIntent: SkipRequested,
			StageRebase: "",
			StageReview: SkipEmptyDiff,
			StageCI:     SkipEmptyDiff,
		} {
			if got := SkipReasonOf(result.State, stage); got != want {
				t.Errorf("%s: SkipReasonOf %q, want %q", stage, got, want)
			}
		}
	})
	t.Run("a requested skip of the pull request stage outranks no code host", func(t *testing.T) {
		c := newCalls()
		p := build(t, Options{Stages: recordingStages(c), Budget: 100})
		begin := complete()
		begin.NoCodeHost = true
		begin.Skip = []Stage{StagePR}
		_, result := start(t, p, begin)
		if result.Status != graph.StatusCompleted {
			t.Fatalf("status %s, reason %q, want completed", result.Status, result.Reason)
		}
		for stage, want := range map[Stage]SkipReason{
			StagePush: "",
			StagePR:   SkipRequested,
			StageCI:   SkipNoCodeHost,
		} {
			if got := SkipReasonOf(result.State, stage); got != want {
				t.Errorf("%s: SkipReasonOf %q, want %q", stage, got, want)
			}
		}
	})
}
