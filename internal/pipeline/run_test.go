package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
)

func TestARunWhereEveryStagePassesCompletes(t *testing.T) {
	c := newCalls()
	p := build(t, Options{Stages: recordingStages(c), Budget: 100})
	_, result := start(t, p, complete())

	if result.Status != graph.StatusCompleted {
		t.Fatalf("status %s, reason %q, want completed", result.Status, result.Reason)
	}
	for _, stage := range Order() {
		if got := StageOutcome(result.State, stage); got != OutcomePassed {
			t.Errorf("%s outcome %q, want %q", stage, got, OutcomePassed)
		}
		if n := c.stageCount(stage); n != 1 {
			t.Errorf("%s body ran %d times, want 1", stage, n)
		}
	}
	if Cancelled(result.State) {
		t.Error("a run nobody cancelled reports as cancelled")
	}
}

// TestAnAskFindingHoldsEvenWithRoundsRemaining is PRD section 5's rule that an
// ask finding holds immediately and never enters a fix round, whatever the
// stage's limit allows.
func TestAnAskFindingHoldsEvenWithRoundsRemaining(t *testing.T) {
	c := newCalls()
	stages := recordingStages(c)
	set(&stages, StageReview, recording(c, nil, nil, func(Input, int) (Output, error) {
		return Output{Report: reportWith(findings.ActionAsk, "is this deletion deliberate?")}, nil
	}))
	p := build(t, Options{
		Stages: stages,
		Fixer:  recordingFixer(c, nil, nil, nil),
		Rounds: rounds(3),
		Budget: 100,
	})
	exec, result := start(t, p, complete())

	if result.Status != graph.StatusHalted {
		t.Fatalf("status %s, want halted", result.Status)
	}
	if result.Position != StageReview.HoldNode() {
		t.Fatalf("halted at %q, want %q", result.Position, StageReview.HoldNode())
	}
	if n := c.fixCount(StageReview); n != 0 {
		t.Errorf("the fixer ran %d times for an ask finding, want 0", n)
	}
	// The halt stops before the hold node runs, so the outcome the stage wrote
	// is still standing: the hold node's only job is to replace it with the
	// answer, and it has not run.
	if got := StageOutcome(result.State, StageReview); got != OutcomeHeld {
		t.Errorf("review outcome %q while halted, want %q: the hold node's body ran", got, OutcomeHeld)
	}
	for _, later := range []Stage{StageTest, StageDocument, StageLint, StagePush, StagePR, StageCI} {
		if n := c.stageCount(later); n != 0 {
			t.Errorf("%s ran %d times while the run was halted at review, want 0", later, n)
		}
	}

	answered, err := exec.Answer(context.Background(), "run", string(OutcomeApproved))
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if answered.Status != graph.StatusCompleted {
		t.Fatalf("status after approving %s, want completed", answered.Status)
	}
	if got := StageOutcome(answered.State, StageReview); got != OutcomeApproved {
		t.Errorf("review outcome %q after approving, want %q", got, OutcomeApproved)
	}
	if n := c.stageCount(StageTest); n != 1 {
		t.Errorf("test ran %d times after the hold was approved, want 1", n)
	}
}

// TestAnAskFindingHoldsAReportThatAlsoHasFixableFindings is the precedence the
// same rule turns on when one report carries both: the stage holds for the ask
// finding rather than taking a round on the fixable one, so the person decides
// before a fixer touches the change. Reviewing a report of one kind cannot show
// this; only a mixed one can.
func TestAnAskFindingHoldsAReportThatAlsoHasFixableFindings(t *testing.T) {
	c := newCalls()
	stages := recordingStages(c)
	set(&stages, StageReview, recording(c, nil, nil, func(Input, int) (Output, error) {
		return Output{Report: findings.Report{
			Summary: "one to fix and one to decide",
			Findings: []findings.Finding{
				{Action: findings.ActionFix, Description: "this import is unused"},
				{Action: findings.ActionAsk, Description: "is this deletion deliberate?"},
			},
		}}, nil
	}))
	p := build(t, Options{
		Stages: stages,
		Fixer:  recordingFixer(c, nil, nil, nil),
		Rounds: rounds(3),
		Budget: 100,
	})
	_, result := start(t, p, complete())

	if result.Status != graph.StatusHalted || result.Position != StageReview.HoldNode() {
		t.Fatalf("status %s at %q, want halted at %q", result.Status, result.Position, StageReview.HoldNode())
	}
	if got := StageOutcome(result.State, StageReview); got != OutcomeHeld {
		t.Errorf("review outcome %q, want %q: the fixable finding decided the outcome", got, OutcomeHeld)
	}
	if n := c.fixCount(StageReview); n != 0 {
		t.Errorf("the fixer ran %d times on a report holding for a person, want 0", n)
	}
	if n := c.stageCount(StageReview); n != 1 {
		t.Errorf("review ran %d times, want 1: it was re-run after a fix round it should not have taken", n)
	}
}

// TestAnUnclassifiedFindingHolds is P3 reaching this package: a finding with no
// action is not fix-eligible and is not a note, so the stage holds for a
// person rather than advancing.
func TestAnUnclassifiedFindingHolds(t *testing.T) {
	for _, action := range []findings.Action{findings.ActionUnset, findings.Action("resolve-it")} {
		t.Run(string(action)+"/", func(t *testing.T) {
			c := newCalls()
			stages := recordingStages(c)
			set(&stages, StageLint, recording(c, nil, nil, func(Input, int) (Output, error) {
				return Output{Report: reportWith(action, "unreadable action")}, nil
			}))
			p := build(t, Options{
				Stages: stages,
				Fixer:  recordingFixer(c, nil, nil, nil),
				Rounds: rounds(3),
				Budget: 100,
			})
			_, result := start(t, p, complete())

			if result.Status != graph.StatusHalted || result.Position != StageLint.HoldNode() {
				t.Fatalf("status %s at %q, want halted at %q", result.Status, result.Position, StageLint.HoldNode())
			}
			if n := c.fixCount(StageLint); n != 0 {
				t.Errorf("the fixer ran %d times for an unclassified finding, want 0", n)
			}
			recorded, err := StageReport(result.State, StageLint)
			if err != nil {
				t.Fatalf("StageReport: %v", err)
			}
			if len(recorded.Findings) != 1 || recorded.Findings[0].Action != findings.ActionAsk {
				t.Fatalf("recorded findings %+v, want one ask finding", recorded.Findings)
			}
		})
	}
}

// TestANoteDoesNotHold is the other side of the same rule: a report whose
// findings are all notes is approved as it stands.
func TestANoteDoesNotHold(t *testing.T) {
	c := newCalls()
	stages := recordingStages(c)
	set(&stages, StageReview, recording(c, nil, nil, func(Input, int) (Output, error) {
		return Output{Report: reportWith(findings.ActionNote, "worth knowing")}, nil
	}))
	p := build(t, Options{Stages: stages, Budget: 100})
	_, result := start(t, p, complete())

	if result.Status != graph.StatusCompleted {
		t.Fatalf("status %s, want completed", result.Status)
	}
	if got := StageOutcome(result.State, StageReview); got != OutcomePassed {
		t.Errorf("review outcome %q, want %q", got, OutcomePassed)
	}
}

// TestAFixRoundReRunsTheStage is the loop working: the fixer receives the
// stage's fix-eligible findings, the stage runs again, and the run advances
// once the second run reports nothing.
func TestAFixRoundReRunsTheStage(t *testing.T) {
	c := newCalls()
	stages := recordingStages(c)
	set(&stages, StageTest, recording(c, []Key{KeyHead}, nil, func(in Input, call int) (Output, error) {
		if call == 1 {
			return Output{Report: reportWith(findings.ActionFix, "the assertion is inverted")}, nil
		}
		summary, err := in.State.Get(KeyHead)
		if err != nil {
			return Output{}, err
		}
		head, _ := summary.Text()
		if head != "c1" {
			return Output{}, errors.New("the stage re-ran without the fixer's commit: head is " + head)
		}
		return Output{Report: passing()}, nil
	}))
	var given []findings.Finding
	fixer := recordingFixer(c, nil, []Key{KeyHead}, func(in FixInput, call int) (FixOutput, error) {
		given = in.Findings
		return FixOutput{
			Summary: "inverted the assertion back",
			Writes:  map[Key]graph.Value{KeyHead: graph.TextValue("c1")},
		}, nil
	})
	p := build(t, Options{
		Stages: stages,
		Fixer:  fixer,
		Rounds: config.FixRounds{Test: 1},
		Budget: 100,
	})
	_, result := start(t, p, complete())

	if result.Status != graph.StatusCompleted {
		t.Fatalf("status %s, reason %q, want completed", result.Status, result.Reason)
	}
	if n := c.stageCount(StageTest); n != 2 {
		t.Errorf("test ran %d times, want 2: one run and one verification", n)
	}
	if n := c.fixCount(StageTest); n != 1 {
		t.Errorf("the fixer ran %d times, want 1", n)
	}
	if len(given) != 1 || given[0].Action != findings.ActionFix {
		t.Errorf("the fixer was given %+v, want the one fix-eligible finding", given)
	}
	if got := FixSummary(result.State, StageTest); got != "inverted the assertion back" {
		t.Errorf("fix summary %q, want the fixer's own words", got)
	}
	if got := StageOutcome(result.State, StageTest); got != OutcomePassed {
		t.Errorf("test outcome %q, want %q", got, OutcomePassed)
	}
}

// TestTheFixerSeesTheLastRoundsSummary is P4's one channel between rounds: the
// sanitized summary, and nothing else.
func TestTheFixerSeesTheLastRoundsSummary(t *testing.T) {
	c := newCalls()
	stages := recordingStages(c)
	set(&stages, StageLint, recording(c, nil, nil, func(Input, int) (Output, error) {
		return Output{Report: reportWith(findings.ActionFix, "unused import")}, nil
	}))
	var seen []string
	fixer := recordingFixer(c, nil, []Key{KeyHead}, func(in FixInput, call int) (FixOutput, error) {
		seen = append(seen, in.Previous)
		return FixOutput{
			Summary: "round " + string(rune('0'+call)),
			Writes:  map[Key]graph.Value{KeyHead: graph.TextValue("c" + string(rune('0'+call)))},
		}, nil
	})
	p := build(t, Options{
		Stages: stages,
		Fixer:  fixer,
		Rounds: config.FixRounds{Lint: 2},
		Budget: 100,
	})
	_, result := start(t, p, complete())

	if result.Status != graph.StatusRoundsExhausted {
		t.Fatalf("status %s, want rounds-exhausted", result.Status)
	}
	want := []string{"", "round 1"}
	if len(seen) != len(want) {
		t.Fatalf("the fixer saw %q, want %q", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("the fixer saw %q, want %q", seen, want)
		}
	}
}
