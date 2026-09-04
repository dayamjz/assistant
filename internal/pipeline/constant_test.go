package pipeline

import (
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
)

// TestConstantRefusesAReportThePipelineWouldRefuse pins the helper to the same
// answer the stage node adapter gives. A helper that could hand a test a report
// the run rejects would let the test start from a shape the real mechanism
// never accepts, and the refusal would arrive as a failed run rather than as a
// mistake at the call site.
func TestConstantRefusesAReportThePipelineWouldRefuse(t *testing.T) {
	for _, tc := range []struct {
		name    string
		summary string
		found   []findings.Finding
		defect  findings.Defect
	}{
		{"no summary", "", nil, findings.DefectMissingSummary},
		{"whitespace summary", "   ", nil, findings.DefectMissingSummary},
		{"a finding with no description", passingSummary,
			[]findings.Finding{{Action: findings.ActionNote}}, findings.DefectMissingDescription},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				raised := recover()
				if raised == nil {
					t.Fatalf("Constant returned an implementation, want a panic: the pipeline refuses this report")
				}
				message, ok := raised.(string)
				if !ok {
					t.Fatalf("panicked with %T, want a string naming the defect", raised)
				}
				if !strings.Contains(message, "Constant") {
					t.Errorf("panic %q does not name Constant", message)
				}
				if !strings.Contains(message, string(tc.defect)) {
					t.Errorf("panic %q does not quote the validation defect %q", message, tc.defect)
				}
			}()
			Constant(tc.summary, tc.found...)
		})
	}
}

// TestConstantAcceptsAReportThePipelineAccepts is the control: the panic above
// is about the report, not about Constant refusing findings at all.
func TestConstantAcceptsAReportThePipelineAccepts(t *testing.T) {
	c := newCalls()
	stages := recordingStages(c)
	set(&stages, StageReview, Constant(passingSummary,
		findings.Finding{Action: findings.ActionNote, Description: "read the diff"}))
	p := build(t, Options{Stages: stages, Budget: 100})
	_, result := start(t, p, complete())
	if got := StageOutcome(result.State, StageReview); got != OutcomePassed {
		t.Errorf("review outcome %q, want %q: a note passes", got, OutcomePassed)
	}
}
