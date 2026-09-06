package standin_test

import (
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/principles"
)

// This is the shape a stage's own tests and the end-to-end harness use the
// stand-in in, written from outside the package so what it proves is the
// exported surface rather than anything reachable only from within.
//
// It is also P4 asked of the wire across a whole loop: a review, the fix
// rounds it prescribed, and the review that checks them. The second review
// runs after a fixer session exists and still carries no session, which is the
// thing the type split in internal/agents is arranged to make impossible and
// this is the evidence for.
func TestAReviewFixReviewLoopKeepsItsSessionsApart(t *testing.T) {
	principles.Cite(t, principles.P4)
	found := findings.Report{
		Summary: "one thing needs fixing",
		Findings: []findings.Finding{{
			ID:          "f1",
			Description: "the doc comment claims more than the code does",
			Action:      findings.ActionFix,
			Severity:    findings.SeverityError,
		}},
	}
	clean := findings.Report{Summary: "the fix holds"}

	agent := standin.New(t, standin.Script{Steps: []standin.Step{
		{Match: standin.Match{PromptContains: "review"}, Reply: standin.Report(found)},
		{Match: standin.Match{PromptContains: "fix"}, Times: standin.Always, Reply: standin.Text("applied")},
		{Match: standin.Match{PromptContains: "review"}, Times: standin.Always, Reply: standin.Report(clean)},
	}})
	runner := agent.Runner()

	review := func() findings.Report {
		t.Helper()
		result, err := runner.Run(t.Context(), agents.PurposeReview, agents.Invocation{
			Prompt: "review this change", Shape: agents.ShapeReport, Dir: t.TempDir(),
		})
		if err != nil {
			t.Fatalf("running the review: %v", err)
		}
		return result.Report
	}

	first := review()
	if len(first.Fixable()) != 1 {
		t.Fatalf("the first review reported %d fixable findings, want one", len(first.Fixable()))
	}

	fixer, err := agents.OpenFixer(t.Context(), runner, "")
	if err != nil {
		t.Fatalf("opening the fixer: %v", err)
	}
	for round := range 2 {
		if _, err := fixer.Apply(t.Context(), agents.Invocation{
			Prompt: "fix " + first.Fixable()[0].ID, Shape: agents.ShapeText, Dir: t.TempDir(),
		}); err != nil {
			t.Fatalf("fix round %d: %v", round, err)
		}
	}

	if second := review(); len(second.Findings) != 0 {
		t.Errorf("the second review reported %v, want nothing", second.Findings)
	}

	calls := agent.Calls()
	if len(calls) != 4 {
		t.Fatalf("the stand-in was asked %d times, want four: %s", len(calls), calls)
	}
	for _, i := range []int{0, 1, 3} {
		if got := calls[i].Session(); got != "" {
			t.Errorf("%s carried session %q, want none", calls[i], got)
		}
	}
	if got := calls[2].Session(); got != fixer.Reference() {
		t.Errorf("the second fix round carried session %q, want the one the fixer holds, %q",
			got, fixer.Reference())
	}
}
