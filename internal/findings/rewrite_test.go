package findings_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/redact"
)

// secret is what the rewrites in these tests remove, and gone is what they
// leave in its place.
const (
	secret = "SECRETVALUE"
	gone   = "GONE"
)

func dropSecret(s string) string { return strings.ReplaceAll(s, secret, gone) }

// fillEveryString sets every string reachable from v, through struct fields
// and slices, to text carrying the secret, giving each slice one element. It
// returns how many strings it set, so a walk that reached nothing cannot read
// as a pass.
func fillEveryString(v reflect.Value) int {
	switch v.Kind() {
	case reflect.String:
		v.SetString("field " + secret)
		return 1
	case reflect.Struct:
		n := 0
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				n += fillEveryString(v.Field(i))
			}
		}
		return n
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		return fillEveryString(v.Index(0))
	default:
		return 0
	}
}

// Rewritten reaches every string a report carries, including a field nobody
// has listed yet: the report is filled by reflection rather than by naming
// fields, so a field Report or anything it holds gains later is filled here
// too, and Rewritten leaving it out fails this. The filled report encoded
// before the rewrite is the positive control, so a fill that set nothing, or
// one the encoding drops, cannot pass. The report rewritten from is left as
// it was, because its slices are the caller's.
func TestRewrittenReachesEveryStringField(t *testing.T) {
	var report findings.Report
	if n := fillEveryString(reflect.ValueOf(&report).Elem()); n < 10 {
		t.Fatalf("filling the report reached %d strings, which is fewer than the report carries", n)
	}
	before, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(before), secret) < 10 {
		t.Fatalf("the filled report does not carry the secret in every field:\n%s", before)
	}
	after, err := json.Marshal(report.Rewritten(dropSecret))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(after), secret) || !strings.Contains(string(after), "field "+gone) {
		t.Fatalf("the rewritten report kept the secret or lost the rest of the text:\n%s", after)
	}
	unchanged, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if string(unchanged) != string(before) {
		t.Fatal("rewriting the report wrote through to the report it was rewritten from")
	}
	if empty := (findings.Report{}).Rewritten(dropSecret); empty.Findings != nil || empty.Read != nil ||
		empty.Traced != nil || empty.Tested != nil || empty.Evidence != nil || empty.Uncertainties != nil || empty.FollowUps != nil {
		t.Fatalf("rewriting an empty report made its lists non-nil: %+v", empty)
	}
}

// ParseReportThrough rewrites the decoded report before Normalize, so a
// finding identifier derived there is the digest of the rewritten text: it is
// the identifier the same report written without the secret in the first
// place gets, and not the one ParseReport derives from the report as written,
// which is the control showing the secret would otherwise have reached it.
func TestParseReportThroughRewritesBeforeTheIdentifierIsDerived(t *testing.T) {
	const raw = `{"summary":"looked ` + secret + `","findings":[{"action":"ask","description":"is ` + secret + ` meant to be here?"}]}`

	through, err := findings.ParseReportThrough(raw, dropSecret)
	if err != nil {
		t.Fatalf("ParseReportThrough: %v", err)
	}
	clean, err := findings.ParseReport(dropSecret(raw))
	if err != nil {
		t.Fatalf("ParseReport of the clean report: %v", err)
	}
	asWritten, err := findings.ParseReport(raw)
	if err != nil {
		t.Fatalf("ParseReport of the report as written: %v", err)
	}
	if !reflect.DeepEqual(through, clean) {
		t.Fatalf("the report read through the rewrite is %+v, want the report written without the secret %+v", through, clean)
	}
	if through.Findings[0].ID == asWritten.Findings[0].ID {
		t.Fatalf("the identifier %s is the one derived from the text as written", through.Findings[0].ID)
	}
	if through.Summary != "looked "+gone || through.Findings[0].Description != "is "+gone+" meant to be here?" {
		t.Fatalf("the report read through the rewrite is %+v", through)
	}
}

// The candidate scan reads raw as it arrived, so a rewrite that would break
// JSON syntax if it ran over the text cannot make the report unreadable.
// internal/redact is such a rewrite for an Authorization value, which it
// removes to the end of its line, quote and closing braces included: running
// it over a one-line report and then parsing is refused, which is the control,
// and passing it to ParseReportThrough reads the report with the value gone.
func TestParseReportThroughReadsTheStructureFromRawAsItArrived(t *testing.T) {
	const raw = `{"summary":"called the API","findings":[{"action":"ask","description":"sent Authorization: Bearer abcdefgh12345678 to the wrong host"}]}`
	remover := redact.New()

	if _, err := findings.ParseReport(remover.Redact(raw)); err == nil {
		t.Fatal("parsing the redacted text read a report; the control needs the redaction to break it")
	}
	report, err := findings.ParseReportThrough(raw, remover.Redact)
	if err != nil {
		t.Fatalf("ParseReportThrough: %v", err)
	}
	if len(report.Findings) != 1 || strings.Contains(report.Findings[0].Description, "abcdefgh12345678") ||
		!strings.Contains(report.Findings[0].Description, "Authorization: Bearer "+redact.Marker) {
		t.Fatalf("the report read through the remover is %+v", report)
	}
}

// A bare token a reviewer quoted in a report reaches the parsed report with
// the value cut when the report is read through internal/redact, the
// remover internal/tasks and internal/pipeline hand ParseReportThrough. The
// value is built at run time in the shape of a GitHub classic token, so no
// literal here is a credential. Reading the same report through the identity
// is the control: the value survives there, so the cut is the remover's.
func TestParseReportThroughCutsABareTokenWithTheRemover(t *testing.T) {
	value := "ghp_" + strings.Repeat("aB3dE6gH9k", 4)[:36]
	raw := `{"summary":"found a key","findings":[{"action":"ask","description":"the log prints ` + value + ` in clear"}]}`

	plain, err := findings.ParseReportThrough(raw, func(s string) string { return s })
	if err != nil {
		t.Fatalf("ParseReportThrough with the identity: %v", err)
	}
	if !strings.Contains(plain.Findings[0].Description, value) {
		t.Fatalf("the report read through the identity is %+v, want the value standing", plain)
	}
	report, err := findings.ParseReportThrough(raw, redact.New().Redact)
	if err != nil {
		t.Fatalf("ParseReportThrough: %v", err)
	}
	if got, want := report.Findings[0].Description, "the log prints ghp_"+redact.Marker+" in clear"; got != want {
		t.Fatalf("the finding reads %q, want %q", got, want)
	}
}
