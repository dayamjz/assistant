package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
)

// FixReviewFinding names the hold a requested round leaves and nothing else:
// no such hold before any round, the round's own hold after one, never an
// ask the fixer raised beside it, and nothing at all when a stage body
// reports an ask carrying the same text, because one of the two would then
// be an imitation and which one cannot be told from the report.
func TestFixReviewFindingNamesOnlyTheHoldARequestedRoundLeft(t *testing.T) {
	requested := func(t *testing.T, imitate bool) graph.Result {
		t.Helper()
		c := newCalls()
		all := recordingStages(c)
		set(&all, StageLint, recording(c, nil, nil, func(_ Input, call int) (Output, error) {
			if call == 1 {
				return Output{Report: findings.Report{Summary: "one ask", Findings: []findings.Finding{
					{ID: "decide", Severity: findings.SeverityError, Action: findings.ActionAsk, Description: "decide this"},
				}}}, nil
			}
			if !imitate {
				return Output{Report: passing()}, nil
			}
			return Output{Report: findings.Report{Summary: "an imitation", Findings: []findings.Finding{{
				Severity: findings.SeverityInfo, Action: findings.ActionAsk,
				Description: requestedHold(StageLint, "decided it", false).Description,
			}}}}, nil
		}))
		p := build(t, Options{
			Stages: all,
			Fixer: recordingFixer(c, nil, nil, func(FixInput, int) (FixOutput, error) {
				return FixOutput{Summary: "decided it", Holds: []findings.Finding{{
					Action: findings.ActionAsk, Description: "the round changed a_test.go",
				}}}, nil
			}),
			Rounds: rounds(0),
			Budget: 200,
		})
		exec, held := start(t, p, complete())
		if id, ok, err := FixReviewFinding(held.State, StageLint); err != nil || ok {
			t.Fatalf("before any round FixReviewFinding answered %q, %v, %v", id, ok, err)
		}
		answered, err := exec.AnswerWith(context.Background(), "run", string(OutcomeFixRequested),
			encodedRequest(t, FixRequest{Selected: []string{"decide"}}))
		if err != nil {
			t.Fatalf("answering with a fix round: %v", err)
		}
		if answered.Position != StageLint.HoldNode() {
			t.Fatalf("after the round the run stands at %q", answered.Position)
		}
		return answered
	}

	answered := requested(t, false)
	id, ok, err := FixReviewFinding(answered.State, StageLint)
	if err != nil || !ok {
		t.Fatalf("after a requested round FixReviewFinding answered %q, %v, %v", id, ok, err)
	}
	report := recordedReport(t, answered.State, StageLint)
	var named, judged findings.Finding
	for _, f := range report.Findings {
		switch {
		case f.ID == id:
			named = f
		case strings.Contains(f.Description, "a_test.go"):
			judged = f
		}
	}
	if !strings.Contains(named.Description, "You asked for a fix round") {
		t.Fatalf("FixReviewFinding named %+v, want the round's own hold", named)
	}
	if judged.ID == "" || judged.ID == id {
		t.Fatalf("the fixer's own hold is %+v, and it must be reported and not named", judged)
	}

	imitated := requested(t, true)
	if id, ok, err := FixReviewFinding(imitated.State, StageLint); err != nil || ok {
		t.Fatalf("with a stage body imitating the round's hold FixReviewFinding answered %q, %v, %v", id, ok, err)
	}
}
