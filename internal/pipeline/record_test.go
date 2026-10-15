package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/redact"
)

// The two credential shapes internal/redact recognizes, and the parts of each
// that must not survive being recorded.
const (
	urlSecret    = "hunter2-url-password"
	bearerSecret = "s3cretBearerToken0123456789"
)

// credentialed returns text carrying both shapes around a word that is not a
// credential, so a test can show the credential gone and the word kept.
func credentialed(word string) string {
	return fmt.Sprintf("%s cloned https://deploy:%s@example.com/o/r.git with Authorization: Bearer %s",
		word, urlSecret, bearerSecret)
}

// requireNoCredential fails the test when text carries either secret, and when
// it does not carry every word the caller expects to have survived.
func requireNoCredential(t *testing.T, what, text string, kept ...string) {
	t.Helper()
	for _, secret := range []string{urlSecret, bearerSecret} {
		if strings.Contains(text, secret) {
			t.Fatalf("%s carries the credential %q:\n%s", what, secret, text)
		}
	}
	for _, word := range kept {
		if !strings.Contains(text, word) {
			t.Fatalf("%s lost %q, which is not a credential:\n%s", what, word, text)
		}
	}
}

// fillStrings sets every string reachable from v, through struct fields and
// slices, to credentialed text, giving each slice one element. It returns how
// many strings it set, so a walk that reached nothing cannot read as a pass.
func fillStrings(v reflect.Value) int {
	switch v.Kind() {
	case reflect.String:
		v.SetString(credentialed("field"))
		return 1
	case reflect.Struct:
		n := 0
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				n += fillStrings(v.Field(i))
			}
		}
		return n
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		return fillStrings(v.Index(0))
	default:
		return 0
	}
}

// Every string a report carries is recorded without the credentials in it,
// including a field nobody has listed yet: the report is filled by reflection
// rather than by naming fields, so a field findings.Report or findings.Finding
// gains later is filled here too, and redactedReport leaving it out fails this.
// The filled report encoded before redaction is the positive control, so a
// walk that set nothing, or a fill the encoding drops, cannot pass.
func TestARecordedReportCarriesNoCredentialInAnyField(t *testing.T) {
	var report findings.Report
	if n := fillStrings(reflect.ValueOf(&report).Elem()); n < 10 {
		t.Fatalf("filling the report reached %d strings, which is fewer than the report carries", n)
	}
	before, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("encoding the filled report: %v", err)
	}
	if strings.Count(string(before), urlSecret) < 10 {
		t.Fatalf("the filled report does not carry the credential in every field:\n%s", before)
	}

	after, err := json.Marshal(redactedReport(report))
	if err != nil {
		t.Fatalf("encoding the redacted report: %v", err)
	}
	requireNoCredential(t, "the redacted report", string(after), "field", redact.Marker)

	// The body's own report is left as it was, because its slices are the
	// body's and not this package's to write through.
	unchanged, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("encoding the filled report again: %v", err)
	}
	if string(unchanged) != string(before) {
		t.Fatal("redacting the report wrote through to the report the stage body returned")
	}
}

// A stage's report reaches state, and so the checkpoint and every answer read
// off it, without the credentials its body wrote in it; a fix round's summary
// and the holds it raises reach state on the same terms. The finding itself,
// its non-secret text and its action, still arrives, which is the control: a
// report dropped whole would carry no credential either.
func TestAReportAndAFixRoundAreRecordedWithoutTheirCredentials(t *testing.T) {
	c := newCalls()
	all := recordingStages(c)
	set(&all, StageLint, recording(c, nil, nil, func(_ Input, call int) (Output, error) {
		if call > 1 {
			return Output{Report: passing()}, nil
		}
		return Output{Report: findings.Report{
			Summary: credentialed("summarized"),
			Findings: []findings.Finding{{
				ID: "decide", Severity: findings.SeverityError, Action: findings.ActionAsk,
				Description: credentialed("the client logs its remote"),
			}},
		}}, nil
	}))
	p := build(t, Options{
		Stages: all,
		Fixer: recordingFixer(c, nil, nil, func(FixInput, int) (FixOutput, error) {
			return FixOutput{Summary: credentialed("decided it"), Holds: []findings.Finding{{
				Action: findings.ActionAsk, Description: credentialed("the round changed a_test.go"),
			}}}, nil
		}),
		Rounds: rounds(0),
		Budget: 200,
	})
	exec, held := start(t, p, complete())
	if held.Position != StageLint.HoldNode() {
		t.Fatalf("the run stopped at %q, want the lint stage's hold", held.Position)
	}
	text, _ := mustGet(t, held.State, StageLint.ReportKey()).Text()
	requireNoCredential(t, "the recorded lint report", text, "summarized", "the client logs its remote", redact.Marker)
	report := recordedReport(t, held.State, StageLint)
	if len(report.Findings) != 1 || report.Findings[0].ID != "decide" || !report.Findings[0].Holds() {
		t.Fatalf("the recorded report carries %+v, want the one ask the stage reported", report.Findings)
	}

	answered, err := exec.AnswerWith(context.Background(), "run", string(OutcomeFixRequested),
		encodedRequest(t, FixRequest{Selected: []string{"decide"}}))
	if err != nil {
		t.Fatalf("answering with a fix round: %v", err)
	}
	summary := FixSummary(answered.State, StageLint)
	requireNoCredential(t, "the round's recorded summary", summary, "decided it")
	heldText, _ := mustGet(t, answered.State, StageLint.HeldKey()).Text()
	requireNoCredential(t, "the round's recorded holds", heldText, "a_test.go", "decided it")
	after, _ := mustGet(t, answered.State, StageLint.ReportKey()).Text()
	requireNoCredential(t, "the lint report after the round", after, "a_test.go", "decided it")

	// The round's own hold quotes the summary as it was recorded, and is
	// still the one FixReviewFinding names.
	id, ok, err := FixReviewFinding(answered.State, StageLint)
	if err != nil || !ok {
		t.Fatalf("after a round whose summary carried a credential FixReviewFinding answered %q, %v, %v", id, ok, err)
	}
}

// mustGet reads a key the state declares, or fails the test.
func mustGet(t *testing.T, s graph.State, key Key) graph.Value {
	t.Helper()
	v, ok := s.Get(string(key))
	if !ok {
		t.Fatalf("the state holds no %s", key)
	}
	return v
}

// Redaction is applied to state, and state is what the convergence bound
// compares, so this states what that does to the bound. Two reports that
// differ only inside a credential are recorded as the same text, and a round
// that changed nothing else changed no state: the run converges. The control
// is two reports that differ outside the credential, which the bound must
// still tell apart, so the run spends its rounds instead.
func TestReportsThatDifferOnlyInACredentialAreOneRecordedState(t *testing.T) {
	const limit = 6
	for _, outside := range []bool{false, true} {
		c := newCalls()
		stages := recordingStages(c)
		set(&stages, StageCI, recording(c, nil, nil, func(_ Input, call int) (Output, error) {
			word := "the check fails"
			if outside {
				word = fmt.Sprintf("the check fails on attempt %d", call)
			}
			return Output{Report: findings.Report{
				Summary: "one finding",
				Findings: []findings.Finding{{Action: findings.ActionFix, Description: fmt.Sprintf(
					"%s at https://deploy:%s-%d@example.com/o/r.git", word, urlSecret, call)}},
			}}, nil
		}))
		p := build(t, Options{
			Stages: stages,
			Fixer: recordingFixer(c, nil, nil, func(FixInput, int) (FixOutput, error) {
				return FixOutput{Summary: "nothing needed changing"}, nil
			}),
			Rounds: config.FixRounds{Checks: limit},
			Budget: 500,
		})
		_, result := start(t, p, complete())
		want := graph.StatusConverged
		if outside {
			want = graph.StatusRoundsExhausted
		}
		if result.Status != want {
			t.Fatalf("with the reports differing outside the credential %v, the run is %s (%q), want %s",
				outside, result.Status, result.Reason, want)
		}
	}
}
