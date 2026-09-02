package findings_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
)

// asValidation is errors.As specialized to the one error type this package
// returns for a malformed report.
func asValidation(err error, target **findings.ValidationError) bool {
	return errors.As(err, target)
}

func validReport() findings.Report {
	return findings.Report{
		Summary: "reviewed the change",
		Findings: []findings.Finding{
			{ID: "f-1", Severity: findings.SeverityError, Action: findings.ActionFix,
				Location: findings.Location{Path: "a.go", Line: 4}, Description: "unchecked write"},
			{ID: "f-2", Severity: findings.SeverityInfo, Action: findings.ActionNote,
				Description: "reads well"},
		},
		Risk:     findings.RiskLow,
		Tested:   []string{"go test ./..."},
		Evidence: []findings.Evidence{{Label: "log", Path: "evidence/1/log.txt"}},
	}
}

func TestValidateAcceptsAWellFormedReport(t *testing.T) {
	if err := validReport().Validate(); err != nil {
		t.Fatalf("Validate refused a well-formed report: %v", err)
	}
	// The optional fields really are optional.
	minimal := findings.Report{Summary: "nothing to report"}
	if err := minimal.Validate(); err != nil {
		t.Fatalf("Validate refused a report with only a summary: %v", err)
	}
}

func TestValidateRefusesEachDefect(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*findings.Report)
		want   findings.Defect
	}{
		{"no summary", func(r *findings.Report) { r.Summary = "  " }, findings.DefectMissingSummary},
		{"no identifier", func(r *findings.Report) { r.Findings[0].ID = "" }, findings.DefectMissingID},
		{"repeated identifier", func(r *findings.Report) { r.Findings[1].ID = r.Findings[0].ID }, findings.DefectDuplicateID},
		{"no description", func(r *findings.Report) { r.Findings[0].Description = " " }, findings.DefectMissingDescription},
		{"unrecognized action", func(r *findings.Report) { r.Findings[0].Action = "whatever" }, findings.DefectUnrecognizedAction},
		{"absent action", func(r *findings.Report) { r.Findings[0].Action = findings.ActionUnset }, findings.DefectUnrecognizedAction},
		{"unrecognized severity", func(r *findings.Report) { r.Findings[0].Severity = "critical" }, findings.DefectUnrecognizedSeverity},
		{"unrecognized risk", func(r *findings.Report) { r.Risk = "critical" }, findings.DefectUnrecognizedRisk},
		{"negative line", func(r *findings.Report) { r.Findings[0].Location.Line = -1 }, findings.DefectNegativeLine},
		{"evidence with no path", func(r *findings.Report) { r.Evidence[0].Path = "" }, findings.DefectMissingEvidencePath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := validReport()
			tc.mutate(&report)
			err := report.Validate()
			var verr *findings.ValidationError
			if !asValidation(err, &verr) {
				t.Fatalf("Validate returned %v, want a *ValidationError", err)
			}
			if !verr.HasDefect(tc.want) {
				t.Fatalf("Validate reported %v, want defect %q", verr.Flaws, tc.want)
			}
		})
	}
}

func TestValidateReportsEveryDefectAtOnce(t *testing.T) {
	report := findings.Report{
		Summary: "",
		Risk:    "critical",
		Findings: []findings.Finding{
			{ID: "", Action: "junk", Severity: "loud", Description: ""},
		},
	}
	err := report.Validate()
	var verr *findings.ValidationError
	if !asValidation(err, &verr) {
		t.Fatalf("Validate returned %v, want a *ValidationError", err)
	}
	for _, want := range []findings.Defect{
		findings.DefectMissingSummary, findings.DefectUnrecognizedRisk,
		findings.DefectMissingID, findings.DefectUnrecognizedAction,
		findings.DefectUnrecognizedSeverity, findings.DefectMissingDescription,
	} {
		if !verr.HasDefect(want) {
			t.Errorf("defect %q missing from %v", want, verr.Flaws)
		}
	}
	if len(verr.Flaws) != 6 {
		t.Errorf("Validate reported %d flaws, want 6: %v", len(verr.Flaws), verr.Flaws)
	}
	if !strings.Contains(verr.Error(), "6 defects") {
		t.Errorf("Error() = %q, want it to count the defects", verr.Error())
	}
}

func TestValidationErrorNamesTheOffendingField(t *testing.T) {
	report := validReport()
	report.Findings[1].Action = "sometime"
	err := report.Validate()
	var verr *findings.ValidationError
	if !asValidation(err, &verr) {
		t.Fatalf("Validate returned %v, want a *ValidationError", err)
	}
	if len(verr.Flaws) != 1 {
		t.Fatalf("want exactly one flaw, got %v", verr.Flaws)
	}
	if verr.Flaws[0].Field != "findings[1].action" {
		t.Errorf("Field = %q, want %q", verr.Flaws[0].Field, "findings[1].action")
	}
	if !strings.Contains(verr.Error(), `"sometime"`) {
		t.Errorf("Error() = %q, want it to quote what the field held", verr.Error())
	}
	if verr.HasDefect(findings.DefectMissingSummary) {
		t.Error("HasDefect reported a defect that was not found")
	}
}

// Normalization is what stands between agent wording and these refusals, so
// nothing an agent can write in the action or severity field survives it.
func TestNormalizeSatisfiesValidateForAnyActionWording(t *testing.T) {
	for _, wording := range []string{"", "fix", "FIX", "please fix", "unknown", "ask?", "note "} {
		report := findings.Report{
			Summary:  "s",
			Findings: []findings.Finding{{Action: findings.Action(wording), Description: "d"}},
		}.Normalize()
		if err := report.Validate(); err != nil {
			t.Errorf("action %q normalized to something Validate refuses: %v", wording, err)
		}
	}
}

func TestFlawErrorRendersTheDefectAndField(t *testing.T) {
	withDetail := findings.Flaw{Defect: findings.DefectUnrecognizedAction,
		Field: "findings[0].action", Detail: `"junk"`}
	if got := withDetail.Error(); got != `unrecognized-action: findings[0].action: "junk"` {
		t.Errorf("Error() = %q", got)
	}
	bare := findings.Flaw{Defect: findings.DefectMissingSummary, Field: "summary"}
	if got := bare.Error(); got != "missing-summary: summary" {
		t.Errorf("Error() = %q", got)
	}
}

func TestValidationErrorWithOneFlawReadsAsOneLine(t *testing.T) {
	report := validReport()
	report.Summary = ""
	err := report.Validate()
	var verr *findings.ValidationError
	if !asValidation(err, &verr) {
		t.Fatalf("Validate returned %v, want a *ValidationError", err)
	}
	if got := verr.Error(); got != "findings: missing-summary: summary" {
		t.Errorf("Error() = %q", got)
	}
	if strings.Contains(verr.Error(), "\n") {
		t.Error("a single flaw must not render as a list")
	}
}
