package findings_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
)

const bareObject = `{
  "summary": "reviewed the change",
  "risk": "medium",
  "risk_rationale": "touches the push path",
  "findings": [
    {"severity": "error", "action": "fix", "location": {"path": "a.go", "line": 4},
     "description": "the error is dropped"},
    {"severity": "warning", "action": "ask", "description": "was this removal deliberate?"}
  ],
  "tested": ["go test ./..."],
  "evidence": [{"label": "run log", "path": "evidence/7/log.txt"}]
}`

func mustParse(t *testing.T, raw string) findings.Report {
	t.Helper()
	report, err := findings.ParseReport(raw)
	if err != nil {
		t.Fatalf("ParseReport returned %v", err)
	}
	return report
}

func TestParseReportAcceptsTheShapesAgentsProduce(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"bare object", bareObject},
		{"leading and trailing space", "\n\n  " + bareObject + "  \n"},
		{"fenced block", "```json\n" + bareObject + "\n```"},
		{"unlabelled fence", "```\n" + bareObject + "\n```"},
		{"final object after prose", "I read the diff and here is what I found.\n\n" + bareObject},
		{"fenced block after prose", "Here you go:\n\n```json\n" + bareObject + "\n```\n\nLet me know."},
		{"trailing prose", bareObject + "\n\nThat is everything."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := mustParse(t, tc.raw)
			if got.Summary != "reviewed the change" {
				t.Errorf("Summary = %q", got.Summary)
			}
			if len(got.Findings) != 2 {
				t.Fatalf("got %d findings, want 2", len(got.Findings))
			}
			if got.Risk != findings.RiskMedium {
				t.Errorf("Risk = %q", got.Risk)
			}
			if got.Findings[0].Location != (findings.Location{Path: "a.go", Line: 4}) {
				t.Errorf("Location = %+v", got.Findings[0].Location)
			}
			if len(got.Fixable()) != 1 || got.Fixable()[0].Description != "the error is dropped" {
				t.Errorf("Fixable = %+v", got.Fixable())
			}
			if !got.HasParked() {
				t.Error("HasParked = false, but the report holds an ask")
			}
			for i, f := range got.Findings {
				if f.ID == "" {
					t.Errorf("finding %d came back without an identifier", i)
				}
			}
		})
	}
}

func TestParseReportIsDeterministic(t *testing.T) {
	first, second := mustParse(t, bareObject), mustParse(t, bareObject)
	for i := range first.Findings {
		if first.Findings[i].ID != second.Findings[i].ID {
			t.Errorf("identifier %d differs between parses: %q then %q",
				i, first.Findings[i].ID, second.Findings[i].ID)
		}
	}
}

// P3 through the parser: the four ways an action can fail to classify a
// finding, and none of them may reach the fix loop.
func TestParseReportSendsUnclassifiedFindingsToAsk(t *testing.T) {
	raw := `{"summary": "reviewed", "findings": [
	  {"severity": "error", "description": "no action field at all"},
	  {"severity": "error", "action": "", "description": "empty action"},
	  {"severity": "error", "action": "auto-fix", "description": "unrecognized action"},
	  {"severity": "error", "action": 7, "description": "action was a number"},
	  {"severity": "error", "action": {"do": "fix"}, "description": "action was an object"},
	  {"severity": "error", "action": null, "description": "action was null"},
	  {"severity": "error", "action": ["fix"], "description": "action was a list"}
	]}`
	got := mustParse(t, raw)
	if len(got.Findings) != 7 {
		t.Fatalf("got %d findings, want 7", len(got.Findings))
	}
	for _, f := range got.Findings {
		if f.Action != findings.ActionAsk {
			t.Errorf("%q came back as %q, want ask", f.Description, f.Action)
		}
		if f.FixEligible() {
			t.Errorf("%q is fix-eligible", f.Description)
		}
	}
	if len(got.Fixable()) != 0 {
		t.Fatalf("Fixable returned %+v, want nothing", got.Fixable())
	}
	if len(got.Parked()) != 7 {
		t.Fatalf("Parked returned %d findings, want all 7", len(got.Parked()))
	}
}

// A stage that adds a field to its own prompt must not lose its findings.
func TestParseReportIgnoresFieldsItDoesNotRecognize(t *testing.T) {
	raw := `{
	  "summary": "reviewed",
	  "confidence": 0.8,
	  "model": {"name": "some-agent", "tokens": 1200},
	  "findings": [
	    {"action": "fix", "description": "the error is dropped", "rule": "errcheck",
	     "suggested_patch": "if err != nil { return err }"}
	  ],
	  "notes_for_the_next_run": ["nothing"]
	}`
	got := mustParse(t, raw)
	if len(got.Findings) != 1 || !got.Findings[0].FixEligible() {
		t.Fatalf("report = %+v, want the one fix finding kept", got)
	}
}

func TestParseReportRefusesWhatItCannotValidate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		raw    string
		defect findings.Defect
	}{
		{"empty object", `{}`, findings.DefectMissingSummary},
		{"no summary", `{"findings": []}`, findings.DefectMissingSummary},
		{"blank summary", `{"summary": "   "}`, findings.DefectMissingSummary},
		// A top-level array is not the report shape. The object inside it is a
		// balanced span, so it is tried and refused for what it lacks.
		{"an array of findings", `[{"action": "fix", "description": "d"}]`, findings.DefectMissingSummary},
		{"unrecognized risk", `{"summary": "s", "risk": "critical"}`, findings.DefectUnrecognizedRisk},
		{"finding with no description", `{"summary": "s", "findings": [{"action": "fix"}]}`,
			findings.DefectMissingDescription},
		{"findings sharing an identifier",
			`{"summary": "s", "findings": [{"id": "x", "description": "a"}, {"id": "x", "description": "b"}]}`,
			findings.DefectDuplicateID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report, err := findings.ParseReport(tc.raw)
			var verr *findings.ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("ParseReport returned (%+v, %v), want a *ValidationError", report, err)
			}
			if !verr.HasDefect(tc.defect) {
				t.Fatalf("refusal reported %v, want defect %q", verr.Flaws, tc.defect)
			}
		})
	}
}

func TestParseReportRefusesOutputWithNoReportInIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"prose only", "I could not complete the review."},
		{"truncated object", `{"summary": "reviewed", "findings": [{"action": "fix"`},
		{"not json at all", "```json\nsummary: reviewed\nfindings: none\n```"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report, err := findings.ParseReport(tc.raw)
			if !errors.Is(err, findings.ErrNoReport) {
				t.Fatalf("ParseReport returned (%+v, %v), want ErrNoReport", report, err)
			}
		})
	}
}

// An object carrying a report's keys is the report the agent meant, so a shape
// this package cannot decode is refused by name rather than being skipped for
// whatever came before it.
func TestParseReportRefusesAReportItCannotRead(t *testing.T) {
	for _, tc := range []struct {
		name  string
		raw   string
		field string
	}{
		{"findings is not a list", `{"summary": "s", "findings": "none"}`, "findings"},
		{"a finding is not an object", `{"summary": "s", "findings": ["fix a.go"]}`, "findings"},
		{"location is a number",
			`{"summary": "s", "findings": [{"description": "d", "location": 4}]}`, "location"},
		{"risk is a number", `{"summary": "s", "risk": 3}`, "risk"},
		{"tested is not a list", `{"summary": "s", "tested": "make check"}`, "tested"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report, err := findings.ParseReport(tc.raw)
			if !errors.Is(err, findings.ErrUnreadableReport) {
				t.Fatalf("ParseReport returned (%+v, %v), want ErrUnreadableReport", report, err)
			}
			if errors.Is(err, findings.ErrNoReport) {
				t.Fatalf("refusal also reports ErrNoReport, but a report was found: %v", err)
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("refusal %q does not name the field it could not read", err)
			}
		})
	}
}

// The silent pass on the decode path: a schema example quoted in the prose
// validates, so a real report the decoder cannot read must not be replaced by
// it. The caller has to see the refusal.
func TestParseReportDoesNotSubstituteAQuotedExampleForAnUnreadableReport(t *testing.T) {
	raw := `{"summary": "an example, not my report", "findings": []}` +
		"\n\nHere is my report:\n\n" +
		`{"summary": "reviewed", "findings": "none"}`
	report, err := findings.ParseReport(raw)
	if !errors.Is(err, findings.ErrUnreadableReport) {
		t.Fatalf("ParseReport returned (%+v, %v), want ErrUnreadableReport", report, err)
	}
	if report.Summary != "" || len(report.Findings) != 0 {
		t.Fatalf("ParseReport returned %+v alongside its refusal, want the zero Report", report)
	}
}

// The best-effort limit ParseReport states: a span that is not valid JSON
// exposes no keys, so it cannot be told apart from prose and an earlier object
// that validates is what comes back.
func TestParseReportCannotIdentifyAReportThatIsNotValidJSON(t *testing.T) {
	raw := `{"summary": "an example, not my report", "findings": []}` +
		"\n\nHere is my report:\n\n" +
		`{"summary": "reviewed", "findings": [{"description": "a\qb"}]}`
	got := mustParse(t, raw)
	if got.Summary != "an example, not my report" {
		t.Fatalf("Summary = %q, want the documented limit to still hold", got.Summary)
	}
}

func TestParseReportRefusalQuotesWhatItRead(t *testing.T) {
	_, err := findings.ParseReport("I could not complete the review.")
	if err == nil || !strings.Contains(err.Error(), "I could not complete the review.") {
		t.Fatalf("refusal was %v, want it to quote the output it read", err)
	}
}

func TestParseReportRefusesOutputLargerThanItAccepts(t *testing.T) {
	raw := strings.Repeat("x", findings.MaxRawBytes+1)
	report, err := findings.ParseReport(raw)
	if !errors.Is(err, findings.ErrRawTooLarge) {
		t.Fatalf("ParseReport returned (%+v, %v), want ErrRawTooLarge", report, err)
	}
	if len(err.Error()) > 200 {
		t.Errorf("the refusal is %d bytes long, so it echoes the input", len(err.Error()))
	}
}

func TestParseReportAcceptsOutputRightAtTheLimit(t *testing.T) {
	padding := findings.MaxRawBytes - len(bareObject)
	if padding < 0 {
		t.Fatal("the sample report is larger than the limit")
	}
	raw := strings.Repeat(" ", padding) + bareObject
	if len(raw) != findings.MaxRawBytes {
		t.Fatalf("built %d bytes, want exactly %d", len(raw), findings.MaxRawBytes)
	}
	if got := mustParse(t, raw); got.Summary != "reviewed the change" {
		t.Fatalf("Summary = %q", got.Summary)
	}
}

// Prose can quote an example before the real report, so the last object that
// validates is the one taken.
func TestParseReportPrefersTheLastObjectThatValidates(t *testing.T) {
	raw := "The schema looks like this:\n\n" +
		`{"summary": "an example, not my report", "findings": []}` +
		"\n\nHere is my actual report:\n\n" +
		`{"summary": "the real one", "findings": [{"action": "fix", "description": "d"}]}`
	got := mustParse(t, raw)
	if got.Summary != "the real one" {
		t.Fatalf("Summary = %q, want the last object", got.Summary)
	}
}

// When the last object cannot be validated, whether an earlier one is taken
// instead turns on whether that last object was a report at all. An object
// carrying none of "summary", "findings", or "risk" is something else the agent
// printed, so the report before it stands; one carrying any of those keys is
// the report the agent meant, so its refusal is what the caller sees rather
// than a different object from earlier in the text.
func TestParseReportFallsBackToAnEarlierValidObject(t *testing.T) {
	t.Run("trailing object is not a report", func(t *testing.T) {
		raw := `{"summary": "the real one", "findings": []}` + "\n\nThoughts: {\"note\": \"no summary here\"}"
		got := mustParse(t, raw)
		if got.Summary != "the real one" {
			t.Fatalf("Summary = %q", got.Summary)
		}
	})

	t.Run("trailing object is not a report and does not decode", func(t *testing.T) {
		raw := `{"summary": "the real one", "findings": []}` +
			"\n\nFor the record: " + `{"tested": "make check"}`
		got := mustParse(t, raw)
		if got.Summary != "the real one" {
			t.Fatalf("Summary = %q", got.Summary)
		}
	})

	t.Run("trailing object is a report that does not validate", func(t *testing.T) {
		raw := `{"summary": "the real one", "findings": []}` +
			"\n\nAnd a correction: " + `{"findings": [{"action": "fix"}]}`
		report, err := findings.ParseReport(raw)
		var verr *findings.ValidationError
		if !errors.As(err, &verr) {
			t.Fatalf("ParseReport returned (%+v, %v), want a *ValidationError", report, err)
		}
		if !verr.HasDefect(findings.DefectMissingDescription) {
			t.Fatalf("refusal reported %v, want the trailing report's own defects", verr.Flaws)
		}
	})
}

// The silent pass this fallback used to allow: a schema example quoted in the
// prose validates, so a real report that fails validation after it must not be
// replaced by the example. The caller has to see the refusal.
func TestParseReportDoesNotSubstituteAQuotedExampleForARefusedReport(t *testing.T) {
	raw := "The schema looks like this:\n\n" +
		`{"summary": "an example, not my report", "findings": []}` +
		"\n\nHere is my report:\n\n" +
		`{"summary": "reviewed", "risk": "critical", "findings": [
		  {"severity": "error", "action": "fix", "description": "the error is dropped"},
		  {"severity": "error", "action": "fix", "description": "the lock is not released"},
		  {"severity": "error", "action": "ask", "description": "was this removal deliberate?"}
		]}`
	report, err := findings.ParseReport(raw)
	var verr *findings.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("ParseReport returned (%+v, %v), want a *ValidationError", report, err)
	}
	if !verr.HasDefect(findings.DefectUnrecognizedRisk) {
		t.Fatalf("refusal reported %v, want the unrecognized risk", verr.Flaws)
	}
	if report.Summary != "" || len(report.Findings) != 0 {
		t.Fatalf("ParseReport returned %+v alongside its refusal, want the zero Report", report)
	}
}

// An empty summary and an absent summary are different: the first is a report
// with the defect the summary guard exists to catch, so its refusal is what a
// caller sees rather than an earlier object.
func TestParseReportRefusesATrailingReportWithAnEmptySummary(t *testing.T) {
	raw := `{"summary": "the real one", "findings": []}` + "\n\nOn reflection: " + `{"summary": ""}`
	report, err := findings.ParseReport(raw)
	var verr *findings.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("ParseReport returned (%+v, %v), want a *ValidationError", report, err)
	}
	if !verr.HasDefect(findings.DefectMissingSummary) {
		t.Fatalf("refusal reported %v, want the missing summary", verr.Flaws)
	}
}

// A location this package cannot read fully is not a reason to discard the
// findings around it, so a wrong-typed line drops to zero and everything else
// survives.
func TestParseReportKeepsFindingsWhenALocationLineIsWrongTyped(t *testing.T) {
	got := mustParse(t, `{"summary": "reviewed", "findings": [
	  {"severity": "error", "action": "fix", "description": "the error is dropped",
	   "location": {"path": "a.go", "line": "42"}},
	  {"severity": "error", "action": "fix", "description": "the lock is not released",
	   "location": {"path": "b.go", "line": 7}}
	]}`)
	if len(got.Findings) != 2 {
		t.Fatalf("got %d findings, want both", len(got.Findings))
	}
	if got.Findings[0].Location != (findings.Location{Path: "a.go"}) {
		t.Errorf("Location = %+v, want the path with no line", got.Findings[0].Location)
	}
	if got.Findings[1].Location != (findings.Location{Path: "b.go", Line: 7}) {
		t.Errorf("Location = %+v", got.Findings[1].Location)
	}
	if len(got.Fixable()) != 2 {
		t.Errorf("Fixable = %+v, want both findings", got.Fixable())
	}
}

func TestParseReportReportsTheLastObjectsRefusal(t *testing.T) {
	raw := `{"summary": "reviewed", "risk": "critical"}` + "\n\nand also {\"risk\": \"critical\"}"
	report, err := findings.ParseReport(raw)
	var verr *findings.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("ParseReport returned (%+v, %v), want a *ValidationError", report, err)
	}
	if !verr.HasDefect(findings.DefectUnrecognizedRisk) {
		t.Fatalf("refusal reported %v, want the unrecognized risk", verr.Flaws)
	}
}

// Braces and quotes inside strings must not be read as structure.
func TestParseReportHandlesBracesInsideStrings(t *testing.T) {
	raw := `Here: {"summary": "the fix is if err != nil { return err }",
	  "findings": [{"action": "fix", "description": "a \" quote and a } brace"}]}`
	got := mustParse(t, raw)
	if got.Summary != `the fix is if err != nil { return err }` {
		t.Fatalf("Summary = %q", got.Summary)
	}
	if got.Findings[0].Description != `a " quote and a } brace` {
		t.Fatalf("Description = %q", got.Findings[0].Description)
	}
}

// The residual gap the package documents: an unmatched brace in prose absorbs
// the report, and the result is a refusal rather than a different report.
func TestParseReportRefusesWhenProseAbsorbsTheObject(t *testing.T) {
	raw := `consider the set {a, b ` + bareObject
	report, err := findings.ParseReport(raw)
	if err == nil {
		t.Fatalf("ParseReport returned %+v, want a refusal", report)
	}
	if !errors.Is(err, findings.ErrNoReport) {
		t.Fatalf("ParseReport returned %v, want ErrNoReport", err)
	}
}

func TestParseReportReadsTheLocationTextForm(t *testing.T) {
	got := mustParse(t, `{"summary": "s", "findings": [
	  {"action": "fix", "description": "d", "location": "internal/graph/node.go:42"},
	  {"action": "fix", "description": "d", "location": "README.md"}
	]}`)
	if got.Findings[0].Location != (findings.Location{Path: "internal/graph/node.go", Line: 42}) {
		t.Errorf("Location = %+v", got.Findings[0].Location)
	}
	if got.Findings[1].Location != (findings.Location{Path: "README.md"}) {
		t.Errorf("Location = %+v", got.Findings[1].Location)
	}
}

func TestParseReportNormalizesSeverityAndKeepsSuppliedIdentifiers(t *testing.T) {
	got := mustParse(t, `{"summary": "s", "findings": [
	  {"id": "review-3", "severity": "blocker", "action": "fix", "description": "d"}
	]}`)
	if got.Findings[0].ID != "review-3" {
		t.Errorf("ID = %q, want the supplied one", got.Findings[0].ID)
	}
	if got.Findings[0].Severity != findings.SeverityWarning {
		t.Errorf("Severity = %q, want warning", got.Findings[0].Severity)
	}
	if !got.Findings[0].FixEligible() {
		t.Error("an unreadable severity must not change the action")
	}
}

func TestParseReportIgnoresStrayClosingBraces(t *testing.T) {
	got := mustParse(t, "the diff closes a block with } here, then:\n"+bareObject)
	if got.Summary != "reviewed the change" {
		t.Fatalf("Summary = %q", got.Summary)
	}
}

func TestParseReportRefusesAnUnreadableStringInAField(t *testing.T) {
	// An invalid escape makes the whole candidate undecodable, which is a
	// refusal rather than a partially read report.
	raw := `{"summary": "s", "findings": [{"description": "d", "location": "a\qb"}]}`
	report, err := findings.ParseReport(raw)
	if !errors.Is(err, findings.ErrNoReport) {
		t.Fatalf("ParseReport returned (%+v, %v), want ErrNoReport", report, err)
	}
}
