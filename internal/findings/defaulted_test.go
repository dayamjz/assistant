package findings_test

import (
	"encoding/json"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/principles"
)

// A report read from what a stage printed records, on every finding, whether
// its action was the stage's or P3's default, and the record survives being
// stored and normalized again. Every way an action can be unreadable - absent,
// empty, unrecognized, not a string - is defaulted; every recognized action,
// in any case and spacing, is stated. A stated ask and a defaulted ask are the
// same ask to every selector, so the bit decides nothing about the fix loop.
//
// The stage's own claim about the bit is the half that must only fail toward
// a person: it is believed on a stated ask, which it can only hold for a
// person, and discarded on a fix or a note, and a defaulted action is marked
// whatever the stage claimed.
func TestNormalizeRecordsWhetherAnActionWasStatedOrDefaulted(t *testing.T) {
	principles.Cite(t, principles.P3)
	raw := `{"summary": "eleven findings", "findings": [
		{"id": "absent", "description": "d"},
		{"id": "empty", "action": "", "description": "d"},
		{"id": "unknown", "action": "autofix", "description": "d"},
		{"id": "number", "action": 7, "description": "d"},
		{"id": "claimed-false", "action": "nonsense", "action_defaulted": false, "description": "d"},
		{"id": "ask", "action": "ask", "description": "d"},
		{"id": "folded-ask", "action": " Ask ", "description": "d"},
		{"id": "fix", "action": "fix", "description": "d"},
		{"id": "note", "action": "note", "description": "d"},
		{"id": "claimed-ask", "action": "ask", "action_defaulted": true, "description": "d"},
		{"id": "claimed-fix", "action": "fix", "action_defaulted": true, "description": "d"}
	]}`
	want := map[string]struct {
		action    findings.Action
		defaulted bool
	}{
		"absent":        {findings.ActionAsk, true},
		"empty":         {findings.ActionAsk, true},
		"unknown":       {findings.ActionAsk, true},
		"number":        {findings.ActionAsk, true},
		"claimed-false": {findings.ActionAsk, true},
		"ask":           {findings.ActionAsk, false},
		"folded-ask":    {findings.ActionAsk, false},
		"fix":           {findings.ActionFix, false},
		"note":          {findings.ActionNote, false},
		"claimed-ask":   {findings.ActionAsk, true},
		"claimed-fix":   {findings.ActionFix, false},
	}
	report, err := findings.ParseReport(raw)
	if err != nil {
		t.Fatalf("ParseReport: %v", err)
	}
	check := func(when string, r findings.Report) {
		t.Helper()
		if len(r.Findings) != len(want) {
			t.Fatalf("%s: %d findings, want %d", when, len(r.Findings), len(want))
		}
		for _, f := range r.Findings {
			w, ok := want[f.ID]
			if !ok {
				t.Fatalf("%s: unexpected finding %q", when, f.ID)
			}
			if f.Action != w.action || f.ActionDefaulted != w.defaulted {
				t.Errorf("%s: finding %q is %s with ActionDefaulted %v, want %s with %v",
					when, f.ID, f.Action, f.ActionDefaulted, w.action, w.defaulted)
			}
			if f.ActionDefaulted && f.FixEligible() {
				t.Errorf("%s: finding %q is defaulted and fix-eligible", when, f.ID)
			}
			if f.ActionDefaulted && !f.Holds() {
				t.Errorf("%s: finding %q is defaulted and does not hold", when, f.ID)
			}
		}
	}
	check("as parsed", report)

	// Stored and read back, the way a stage's report travels through graph
	// state, and normalized again, the way the stage node does.
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	var stored findings.Report
	if err := json.Unmarshal(encoded, &stored); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	check("stored and normalized again", stored.Normalize())
}

// A finding the program built and marked as having its remedy outside the
// change keeps the mark through normalization and storage, and the mark moves
// none of the predicates the fix loop and the hold are built on: it is a fact
// a caller may read, not a second action.
func TestRemedyOutsideChangeSurvivesStorageAndDecidesNothingAboutTheAction(t *testing.T) {
	for _, action := range []findings.Action{findings.ActionAsk, findings.ActionFix, findings.ActionNote} {
		built := findings.Finding{ID: "x", Action: action, Description: "d", RemedyOutsideChange: true}
		plain := built
		plain.RemedyOutsideChange = false
		report := findings.Report{Summary: "s", Findings: []findings.Finding{built}}.Normalize()
		encoded, err := json.Marshal(report)
		if err != nil {
			t.Fatalf("encoding: %v", err)
		}
		var stored findings.Report
		if err := json.Unmarshal(encoded, &stored); err != nil {
			t.Fatalf("decoding: %v", err)
		}
		got := stored.Normalize().Findings[0]
		if !got.RemedyOutsideChange {
			t.Errorf("a %s finding lost RemedyOutsideChange through storage", action)
		}
		if got.FixEligible() != plain.FixEligible() || got.Holds() != plain.Holds() {
			t.Errorf("RemedyOutsideChange moved a %s finding's predicates", action)
		}
	}
}
