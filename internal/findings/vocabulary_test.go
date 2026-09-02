package findings_test

import (
	"encoding/json"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
)

func TestParseActionRecognizesTheThreeActions(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want findings.Action
	}{
		{"fix", findings.ActionFix},
		{"ask", findings.ActionAsk},
		{"note", findings.ActionNote},
		{"FIX", findings.ActionFix},
		{"Ask", findings.ActionAsk},
		{"  note\n", findings.ActionNote},
	} {
		if got := findings.ParseAction(tc.in); got != tc.want {
			t.Errorf("ParseAction(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// P3: no action, an empty action, and an action the system does not recognize
// all become ask.
func TestParseActionFailsClosedToAsk(t *testing.T) {
	for _, in := range []string{
		"", "   ", "unknown", "fixme", "fix it", "FIX_ME", "auto", "resolve",
		"none", "null", "note.", "fix-it", "ﬁx",
	} {
		if got := findings.ParseAction(in); got != findings.ActionAsk {
			t.Errorf("ParseAction(%q) = %q, want %q", in, got, findings.ActionAsk)
		}
	}
}

func TestActionRecognized(t *testing.T) {
	for _, a := range []findings.Action{findings.ActionFix, findings.ActionAsk, findings.ActionNote} {
		if !a.Recognized() {
			t.Errorf("Action(%q).Recognized() = false, want true", a)
		}
	}
	for _, a := range []findings.Action{findings.ActionUnset, "Fix", " fix", "whatever"} {
		if a.Recognized() {
			t.Errorf("Action(%q).Recognized() = true, want false", a)
		}
	}
}

// A wrong-typed action must not fail the decode and must not survive as
// anything a machine may act on.
func TestActionUnmarshalJSONTolerAtesWrongTypes(t *testing.T) {
	for _, raw := range []string{`12`, `true`, `null`, `{"action":"fix"}`, `["fix"]`} {
		var a findings.Action
		if err := json.Unmarshal([]byte(raw), &a); err != nil {
			t.Fatalf("Unmarshal(%s) returned %v, want no error", raw, err)
		}
		if a != findings.ActionUnset {
			t.Errorf("Unmarshal(%s) gave action %q, want the unset action", raw, a)
		}
		if (findings.Finding{Action: a}).FixEligible() {
			t.Errorf("Unmarshal(%s) produced a fix-eligible action", raw)
		}
		if findings.ParseAction(string(a)) != findings.ActionAsk {
			t.Errorf("Unmarshal(%s) does not normalize to ask", raw)
		}
	}
}

func TestActionUnmarshalJSONKeepsTheStringItWasGiven(t *testing.T) {
	var a findings.Action
	if err := json.Unmarshal([]byte(`"FiX-later"`), &a); err != nil {
		t.Fatalf("Unmarshal returned %v", err)
	}
	if string(a) != "FiX-later" {
		t.Fatalf("Unmarshal kept %q, want the string verbatim so a refusal can quote it", a)
	}
	if (findings.Finding{Action: a}).FixEligible() {
		t.Fatal("an unrecognized action decoded verbatim is fix-eligible")
	}
}

func TestParseSeverityRecognizesAndDefaultsToWarning(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want findings.Severity
	}{
		{"error", findings.SeverityError},
		{"WARNING", findings.SeverityWarning},
		{" info ", findings.SeverityInfo},
		{"", findings.SeverityWarning},
		{"critical", findings.SeverityWarning},
		{"blocker", findings.SeverityWarning},
		{"trace", findings.SeverityWarning},
	} {
		if got := findings.ParseSeverity(tc.in); got != tc.want {
			t.Errorf("ParseSeverity(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSeverityUnmarshalJSONTolerAtesWrongTypes(t *testing.T) {
	for _, raw := range []string{`3`, `false`, `null`, `{}`} {
		var s findings.Severity
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			t.Fatalf("Unmarshal(%s) returned %v, want no error", raw, err)
		}
		if s != findings.SeverityUnset {
			t.Errorf("Unmarshal(%s) gave severity %q, want the unset severity", raw, s)
		}
	}
}

func TestParseRiskReportsWhatItCannotRead(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want findings.Risk
		ok   bool
	}{
		{"", findings.RiskUnstated, true},
		{"  ", findings.RiskUnstated, true},
		{"low", findings.RiskLow, true},
		{"Medium", findings.RiskMedium, true},
		{" HIGH ", findings.RiskHigh, true},
		{"critical", "critical", false},
		{"unknown", "unknown", false},
	} {
		got, ok := findings.ParseRisk(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("ParseRisk(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestRiskRecognizedAllowsUnstated(t *testing.T) {
	if !findings.RiskUnstated.Recognized() {
		t.Error("RiskUnstated must be recognized, since risk is optional")
	}
	if findings.Risk("critical").Recognized() {
		t.Error(`Risk("critical") must not be recognized`)
	}
}

func TestStringRendersWhatIsStored(t *testing.T) {
	if got := findings.ActionFix.String(); got != "fix" {
		t.Errorf("Action.String() = %q", got)
	}
	if got := findings.Action("please fix").String(); got != "please fix" {
		t.Errorf("Action.String() = %q, want the stored text so a refusal can quote it", got)
	}
	if got := findings.SeverityError.String(); got != "error" {
		t.Errorf("Severity.String() = %q", got)
	}
	if got := findings.RiskUnstated.String(); got != "" {
		t.Errorf("Risk.String() = %q", got)
	}
	if got := findings.RiskHigh.String(); got != "high" {
		t.Errorf("Risk.String() = %q", got)
	}
}
