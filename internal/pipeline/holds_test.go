package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
)

// recordedReport reads a stage's report out of a run's state the way the fix
// node does, so what a test inspects is what the stage node encoded.
func recordedReport(t *testing.T, s graph.State, stage Stage) findings.Report {
	t.Helper()
	v, ok := s.Get(string(stage.ReportKey()))
	if !ok {
		t.Fatalf("the state holds no %s report", stage)
	}
	text, _ := v.Text()
	r, err := decodeReport(text, stage)
	if err != nil {
		t.Fatalf("decoding the %s report: %v", stage, err)
	}
	return r
}

// fixOnceThenPass is a stage that reports one fix finding and then, after a
// round, reports nothing: the stage's own verdict on the second execution is a
// pass, so anything holding it there came from somewhere else.
func fixOnceThenPass(c *calls) Implementation {
	return recording(c, nil, nil, func(in Input, call int) (Output, error) {
		if call == 1 {
			return Output{Report: reportWith(findings.ActionFix, "the check fails")}, nil
		}
		return Output{Report: passing()}, nil
	})
}

// A hold a fix round raises holds the stage on the execution that verifies
// the round, even though the stage's own report on that execution passes, and
// the hold arrives in the stage's recorded report with an action P3 settled:
// the round stated none, and it is an ask. The same round raising nothing does
// not hold, which is what keeps the first half from passing because something
// else held the stage.
func TestAHoldAFixRoundRaisesHoldsTheStageItFixed(t *testing.T) {
	for _, raise := range []bool{true, false} {
		c := newCalls()
		all := recordingStages(c)
		set(&all, StageTest, fixOnceThenPass(c))
		p := build(t, Options{
			Stages: all,
			Fixer: recordingFixer(c, nil, []Key{KeyHead}, func(in FixInput, _ int) (FixOutput, error) {
				out := FixOutput{Summary: "fixed it", Writes: map[Key]graph.Value{KeyHead: graph.TextValue("c1")}}
				if raise {
					out.Holds = []findings.Finding{{ID: "mine", Description: "the round edited a_test.go"}}
				}
				return out, nil
			}),
			Rounds: rounds(2),
			Budget: 200,
		})
		exec, result := start(t, p, complete())

		if !raise {
			if result.Status != graph.StatusCompleted {
				t.Fatalf("a round that raised nothing left the run %s at %q", result.Status, result.Position)
			}
			continue
		}
		if result.Position != StageTest.HoldNode() {
			t.Fatalf("the run stopped at %q (%s), want the test stage's hold", result.Position, result.Status)
		}
		if n := c.stageCount(StageTest); n != 2 {
			t.Fatalf("the test stage ran %d times, want 2: the hold belongs on the verifying execution", n)
		}
		report := recordedReport(t, result.State, StageTest)
		if len(report.Findings) != 1 {
			t.Fatalf("the test report carries %d findings, want the one hold: %+v", len(report.Findings), report.Findings)
		}
		held := report.Findings[0]
		if held.Action != findings.ActionAsk || !strings.Contains(held.Description, "a_test.go") {
			t.Fatalf("the recorded hold is %+v, want an ask carrying the round's description", held)
		}
		if held.ID == "mine" || held.ID == "" {
			t.Fatalf("the hold carries the identifier %q, want one the stage's report assigned", held.ID)
		}

		// Approving accepts it and the run goes on: the later stages do not
		// see the test round's hold, because it is the test stage's alone.
		answered, err := exec.Answer(context.Background(), "run", string(OutcomeApproved))
		if err != nil {
			t.Fatalf("Answer: %v", err)
		}
		if answered.Status != graph.StatusCompleted {
			t.Fatalf("after approving, the run is %s at %q, want completed", answered.Status, answered.Position)
		}
	}
}

// The fix node writes a stage's held key on every round, so a round that
// raised nothing leaves it empty rather than leaving an earlier value
// standing. That is what FixOutput.Holds' claim that a hold applies to the
// execution verifying its own round, and to no later one, rests on.
func TestARoundRaisingNoHoldClearsTheLastRoundsHold(t *testing.T) {
	c := newCalls()
	all := recordingStages(c)
	set(&all, StageLint, recording(c, nil, nil, func(in Input, call int) (Output, error) {
		if call < 3 {
			return Output{Report: reportWith(findings.ActionFix, "the check fails")}, nil
		}
		return Output{Report: passing()}, nil
	}))
	var heldSeen []string
	set(&all, StagePush, recording(c, []Key{StageLint.HeldKey()}, nil, func(in Input, _ int) (Output, error) {
		v, err := in.State.Get(StageLint.HeldKey())
		if err != nil {
			return Output{}, err
		}
		text, _ := v.Text()
		heldSeen = append(heldSeen, text)
		return Output{Report: passing()}, nil
	}))
	p := build(t, Options{
		Stages: all,
		Fixer: recordingFixer(c, nil, []Key{KeyHead}, func(in FixInput, call int) (FixOutput, error) {
			return FixOutput{Summary: "round", Writes: map[Key]graph.Value{
				KeyHead: graph.TextValue("c" + string(rune('0'+call))),
			}}, nil
		}),
		Rounds: rounds(3),
		Budget: 200,
	})
	_, result := start(t, p, complete())
	if result.Status != graph.StatusCompleted {
		t.Fatalf("the run is %s at %q, want completed", result.Status, result.Position)
	}
	if len(heldSeen) != 1 || heldSeen[0] != "" {
		t.Fatalf("after two rounds that raised nothing, the lint stage's held key reads %q, want empty", heldSeen)
	}
}

// A fixer may raise a hold and nothing else. A note or a fix finding from the
// role that made the change would be the fixer reporting on the stage it just
// changed, so the step fails, naming the refusal; so does a hold with nothing
// in it for a person to read.
func TestAFixRoundRaisingAnythingButAnAskIsRefused(t *testing.T) {
	for _, raised := range []findings.Finding{
		{Action: findings.ActionNote, Description: "looks fine to me"},
		{Action: findings.ActionFix, Description: "fix this too"},
		{Action: findings.ActionAsk, Description: "   "},
	} {
		c := newCalls()
		all := recordingStages(c)
		set(&all, StageTest, fixOnceThenPass(c))
		p := build(t, Options{
			Stages: all,
			Fixer: recordingFixer(c, nil, nil, func(FixInput, int) (FixOutput, error) {
				return FixOutput{Summary: "fixed it", Holds: []findings.Finding{raised}}, nil
			}),
			Rounds: rounds(1),
			Budget: 200,
		})
		exec, err := p.Executor(graph.NewMemoryStore())
		if err != nil {
			t.Fatal(err)
		}
		state, err := p.NewState(complete())
		if err != nil {
			t.Fatal(err)
		}
		_, err = exec.Run(context.Background(), "run", state)
		if !errors.Is(err, ErrFixerFinding) {
			t.Errorf("a round raising %+v: err = %v, want ErrFixerFinding", raised, err)
		}
		if n := c.stageCount(StageTest); n != 1 {
			t.Errorf("a round raising %+v: the test stage ran %d times, want the step refused before it re-ran",
				raised, n)
		}
	}
}
