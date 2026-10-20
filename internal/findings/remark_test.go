package findings_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/redact"
)

// A report's uncertainties and follow-ups are read off the wire beside its
// findings, normalized (trimmed, the wholly blank dropped), and hold nothing:
// Held reads findings alone, so a report whose only content beside a note is
// remarks holds nothing, and the same report with an ask finding added holds
// exactly that finding, which is the control showing Held was asked.
func TestARemarkIsReadBesideTheFindingsAndHoldsNothing(t *testing.T) {
	const remarks = `"uncertainties": [
	  {"text": "  whether the cache is ever cold  ", "location": "pkg/cache.go:12"},
	  "which callers rely on the old order",
	  {"text": "   "},
	  ""],
	 "follow_ups": [{"text": "measure the retry budget under load"}]`
	report, err := findings.ParseReport(`{"summary": "looked", "findings": [{"action": "note", "description": "fine"}], ` + remarks + `}`)
	if err != nil {
		t.Fatalf("ParseReport: %v", err)
	}
	wantUncertain := findings.RemarkList{
		{Text: "whether the cache is ever cold", Location: findings.Location{Path: "pkg/cache.go", Line: 12}},
		{Text: "which callers rely on the old order"},
	}
	if !reflect.DeepEqual(report.Uncertainties, wantUncertain) {
		t.Fatalf("uncertainties = %#v, want %#v", report.Uncertainties, wantUncertain)
	}
	if want := (findings.RemarkList{{Text: "measure the retry budget under load"}}); !reflect.DeepEqual(report.FollowUps, want) {
		t.Fatalf("follow-ups = %#v, want %#v", report.FollowUps, want)
	}
	if held := report.Held(); len(held) != 0 || report.HasHeld() || !report.AllNotes() {
		t.Fatalf("a report whose only content beside a note is remarks holds %+v", held)
	}

	control, err := findings.ParseReport(`{"summary": "looked", "findings": [{"action": "note", "description": "fine"}, ` +
		`{"action": "ask", "description": "keep the old order?"}], ` + remarks + `}`)
	if err != nil {
		t.Fatalf("ParseReport of the control: %v", err)
	}
	if held := control.Held(); len(held) != 1 || held[0].Description != "keep the old order?" {
		t.Fatalf("the control holds %+v, want exactly its ask finding", held)
	}
	if len(control.Uncertainties) != 2 || len(control.FollowUps) != 1 {
		t.Fatalf("the control carries %d uncertainties and %d follow-ups, want 2 and 1", len(control.Uncertainties), len(control.FollowUps))
	}

	// What was stored reads back the same, and a report stored before the
	// fields existed reads back with them empty rather than refused.
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var again findings.Report
	if err := json.Unmarshal(encoded, &again); err != nil || !reflect.DeepEqual(again, report) {
		t.Fatalf("the encoded report reads back as %+v (%v), want %+v", again, err, report)
	}
	var older findings.Report
	if err := json.Unmarshal([]byte(`{"summary":"looked","findings":[{"id":"f-1","severity":"info","action":"note","description":"fine"}]}`), &older); err != nil ||
		older.Uncertainties != nil || older.FollowUps != nil {
		t.Fatalf("a report stored without remarks reads as %+v (%v)", older, err)
	}
}

// A remark written with an action is refused, whatever the action's value
// and however its key is cased, with the typed refusal naming the remark; a
// finding written beside it is no excuse. The same report with the action
// moved onto a finding is accepted, which is the control showing the refusal
// is about where the action sits.
func TestARemarkCarryingAnActionIsRefused(t *testing.T) {
	cases := map[string]struct {
		raw   string
		field string
	}{
		"an ask on an uncertainty": {`{"summary": "s", "uncertainties": [{"text": "keep it?", "action": "ask"}]}`, "uncertainties[0].action"},
		"an empty action":          {`{"summary": "s", "uncertainties": ["fine", {"text": "keep it?", "action": ""}]}`, "uncertainties[1].action"},
		"a cased key":              {`{"summary": "s", "follow_ups": [{"text": "do it", "Action": "fix"}]}`, "follow_ups[0].action"},
		"an action and nothing":    {`{"summary": "s", "follow_ups": [{"action": "note"}]}`, "follow_ups[0].action"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := findings.ParseReport(c.raw)
			var verr *findings.ValidationError
			if !errors.As(err, &verr) || !verr.HasDefect(findings.DefectActionOutsideFinding) {
				t.Fatalf("ParseReport = %v, want a ValidationError with %s", err, findings.DefectActionOutsideFinding)
			}
			if !strings.Contains(err.Error(), c.field) {
				t.Fatalf("the refusal %q does not name %s", err, c.field)
			}
		})
	}
	control, err := findings.ParseReport(`{"summary": "s", "findings": [{"action": "ask", "description": "keep it?"}], "uncertainties": [{"text": "keep it?"}]}`)
	if err != nil || len(control.Held()) != 1 || len(control.Uncertainties) != 1 {
		t.Fatalf("the control = %+v, %v, want one held finding and one uncertainty", control, err)
	}
}

// A remark this package cannot read as one is kept as the JSON it was
// written as, so a stage's words never vanish into a gap, and it costs the
// findings around it nothing.
func TestARemarkInAnUnreadableShapeIsKeptAsWritten(t *testing.T) {
	report, err := findings.ParseReport(`{"summary": "s", "findings": [{"action": "ask", "description": "q"}], ` +
		`"uncertainties": "one bare remark", "follow_ups": [42, {"why": "unasked key"}, {"text": 7, "location": "a.go"}, ` +
		`{"text": null, "question": "Whether retries preserve order"}]}`)
	if err != nil {
		t.Fatalf("ParseReport: %v", err)
	}
	if want := (findings.RemarkList{{Text: "one bare remark"}}); !reflect.DeepEqual(report.Uncertainties, want) {
		t.Fatalf("uncertainties = %#v, want %#v", report.Uncertainties, want)
	}
	want := findings.RemarkList{
		{Text: "42"},
		{Text: `{"why": "unasked key"}`},
		{Location: findings.Location{Path: "a.go"}},
		{Text: `{"text": null, "question": "Whether retries preserve order"}`},
	}
	if !reflect.DeepEqual(report.FollowUps, want) {
		t.Fatalf("follow-ups = %#v, want %#v", report.FollowUps, want)
	}
	if len(report.Held()) != 1 {
		t.Fatalf("the findings beside unreadable remarks were lost: %+v", report.Findings)
	}
}

// A credential a worker wrote into an uncertainty or a follow-up is gone
// before anything reads the report, because ParseReportThrough reaches the
// remarks' text and location; the text around it is the control showing the
// remark was read at all.
func TestParseReportThroughRedactsARemark(t *testing.T) {
	const password = "hunter2-remark-password"
	raw := `{"summary": "s", "uncertainties": [{"text": "whether the mirror still works", "location": "https://deploy:` + password + `@example.com/o/r.git"}], ` +
		`"follow_ups": ["rotate https://deploy:` + password + `@example.com/o/r.git"]}`
	report, err := findings.ParseReportThrough(raw, redact.New().Redact)
	if err != nil {
		t.Fatalf("ParseReportThrough: %v", err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), password) {
		t.Fatalf("the parsed report keeps the credential:\n%s", encoded)
	}
	if !strings.HasPrefix(report.FollowUps[0].Text, "rotate https://") || report.Uncertainties[0].Text != "whether the mirror still works" ||
		!strings.Contains(report.Uncertainties[0].Location.Path, "example.com") {
		t.Fatalf("the remarks read through the remover are %+v and %+v", report.Uncertainties, report.FollowUps)
	}
	if asWritten, err := findings.ParseReport(raw); err != nil || !strings.Contains(asWritten.FollowUps[0].Text, password) {
		t.Fatalf("the control read without the remover is %+v, %v, want the credential as written", asWritten.FollowUps, err)
	}
}
