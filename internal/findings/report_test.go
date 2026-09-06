package findings_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
)

func TestReportNormalizeResolvesEveryField(t *testing.T) {
	in := findings.Report{
		Summary: "  reviewed the change  ",
		Findings: []findings.Finding{
			{Severity: "CRITICAL", Action: "please fix", Description: " a bug ",
				Location: findings.Location{Path: " a.go ", Line: -3}},
		},
		Risk:          " HIGH ",
		RiskRationale: "  touches the push path\n",
		Tested:        []string{" go test ./... ", "   ", ""},
		Evidence:      []findings.Evidence{{Label: " run log ", Path: " evidence/1/log.txt "}},
	}
	got := in.Normalize()

	if got.Summary != "reviewed the change" {
		t.Errorf("Summary = %q", got.Summary)
	}
	if got.Risk != findings.RiskHigh {
		t.Errorf("Risk = %q, want %q", got.Risk, findings.RiskHigh)
	}
	if got.RiskRationale != "touches the push path" {
		t.Errorf("RiskRationale = %q", got.RiskRationale)
	}
	if len(got.Tested) != 1 || got.Tested[0] != "go test ./..." {
		t.Errorf("Tested = %q, want the one non-empty entry", got.Tested)
	}
	if got.Evidence[0] != (findings.Evidence{Label: "run log", Path: "evidence/1/log.txt"}) {
		t.Errorf("Evidence[0] = %+v", got.Evidence[0])
	}
	f := got.Findings[0]
	if f.Action != findings.ActionAsk {
		t.Errorf("Action = %q, want ask: an unrecognized action is P3's case", f.Action)
	}
	if f.FixEligible() {
		t.Error("a finding with an unrecognized action came out fix-eligible")
	}
	if f.Severity != findings.SeverityWarning {
		t.Errorf("Severity = %q, want warning", f.Severity)
	}
	if f.Description != "a bug" || f.Location.Path != "a.go" || f.Location.Line != 0 {
		t.Errorf("finding = %+v", f)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("a normalized report does not validate: %v", err)
	}
}

// An unrecognized risk survives normalization so Validate can refuse it by
// name. Reading it as a level either way would misinform the person.
func TestReportNormalizeLeavesAnUnrecognizedRiskInPlace(t *testing.T) {
	got := findings.Report{Summary: "s", Risk: " Critical "}.Normalize()
	if got.Risk != findings.Risk("Critical") {
		t.Fatalf("Risk = %q, want the trimmed original", got.Risk)
	}
	err := got.Validate()
	var verr *findings.ValidationError
	if !asValidation(err, &verr) || !verr.HasDefect(findings.DefectUnrecognizedRisk) {
		t.Fatalf("Validate returned %v, want an unrecognized-risk refusal", err)
	}
}

func TestReportNormalizeDoesNotModifyItsReceiver(t *testing.T) {
	in := findings.Report{
		Summary:  " s ",
		Findings: []findings.Finding{{Action: "junk", Description: "d"}},
		Tested:   []string{" t "},
		Evidence: []findings.Evidence{{Label: " l ", Path: " p "}},
	}
	_ = in.Normalize()
	if in.Summary != " s " || in.Findings[0].Action != "junk" ||
		in.Tested[0] != " t " || in.Evidence[0].Path != " p " {
		t.Fatalf("Normalize modified the report it was called on: %+v", in)
	}
}

func TestReportSelectorsAndPredicates(t *testing.T) {
	report := findings.Report{
		Summary: "s",
		Findings: []findings.Finding{
			{ID: "a", Action: findings.ActionFix, Description: "d"},
			{ID: "b", Action: findings.ActionAsk, Description: "d"},
			{ID: "c", Action: findings.ActionNote, Description: "d"},
		},
	}
	if got := report.Fixable(); len(got) != 1 || got[0].ID != "a" {
		t.Errorf("Fixable = %+v, want just the fix finding", got)
	}
	if got := report.Held(); len(got) != 1 || got[0].ID != "b" {
		t.Errorf("Held = %+v, want just the ask finding", got)
	}
	if !report.HasHeld() {
		t.Error("HasHeld = false with an ask finding present")
	}
	if report.AllNotes() {
		t.Error("AllNotes = true with a fix and an ask present")
	}
}

func TestAllNotesAndHasHeldOnTheApprovingCases(t *testing.T) {
	notes := findings.Report{Summary: "s", Findings: []findings.Finding{
		{ID: "a", Action: findings.ActionNote, Description: "d"},
		{ID: "b", Action: findings.ActionNote, Description: "d"},
	}}
	if !notes.AllNotes() {
		t.Error("a report of only notes must report AllNotes")
	}
	if notes.HasHeld() {
		t.Error("a report of only notes must not report HasHeld")
	}
	empty := findings.Report{Summary: "s"}
	if !empty.AllNotes() || empty.HasHeld() {
		t.Error("a report with no findings is approved as it stands")
	}
	// An unclassified finding is not a note, so it cannot approve a stage.
	unclassified := findings.Report{Summary: "s", Findings: []findings.Finding{
		{ID: "a", Action: findings.ActionUnset, Description: "d"},
	}}
	if unclassified.AllNotes() {
		t.Error("a finding with no action must not count as a note")
	}
	if len(unclassified.Fixable()) != 0 {
		t.Error("a finding with no action must not be fix-eligible")
	}
}

// A report is written to a record and read back, so what this package encodes
// has to be something ParseReport accepts.
func TestReportSurvivesAJSONRoundTrip(t *testing.T) {
	original := mustParse(t, bareObject)
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal returned %v", err)
	}
	got, err := findings.ParseReport(string(encoded))
	if err != nil {
		t.Fatalf("ParseReport refused this package's own encoding: %v\n%s", err, encoded)
	}
	if !reflect.DeepEqual(got, original) {
		t.Errorf("round trip changed the report:\n got %+v\nwant %+v", got, original)
	}
}
