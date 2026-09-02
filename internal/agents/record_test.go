package agents_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
)

// PRD section 8 fixes what an agent invocation record holds: purpose, agent,
// model, timing, failure category, and token usage, and never prompts,
// outputs, diffs, or credentials. These two tests assert that on the recorded
// shape rather than on a call site: the first fails if a field is added that
// content could be written into, and the second fails if content reaches one
// of the fields that are there.

func TestARecordHasNoFieldContentCouldBeWrittenInto(t *testing.T) {
	allowed := map[string]reflect.Type{
		"Purpose":  reflect.TypeFor[agents.Purpose](),
		"Agent":    reflect.TypeFor[string](),
		"Model":    reflect.TypeFor[string](),
		"Session":  reflect.TypeFor[agents.SessionUse](),
		"Started":  reflect.TypeFor[time.Time](),
		"Duration": reflect.TypeFor[time.Duration](),
		"Failure":  reflect.TypeFor[agents.Failure](),
		"Usage":    reflect.TypeFor[agents.Usage](),
	}
	typ := reflect.TypeFor[agents.Record]()
	seen := make(map[string]struct{}, typ.NumField())
	for i := range typ.NumField() {
		field := typ.Field(i)
		want, ok := allowed[field.Name]
		if !ok {
			t.Errorf("Record.%s is a field PRD section 8 does not list; a record holds cost, not content", field.Name)
			continue
		}
		if field.Type != want {
			t.Errorf("Record.%s is %s, want %s", field.Name, field.Type, want)
		}
		seen[field.Name] = struct{}{}
	}
	for name := range allowed {
		if _, ok := seen[name]; !ok {
			t.Errorf("Record.%s is missing", name)
		}
	}
}

func TestNothingAboutAnInvocationIsRecordedExceptItsCost(t *testing.T) {
	const (
		secretPrompt      = "the-prompt-nobody-should-record"
		secretCredential  = "the-credential-nobody-should-record"
		secretAgentOutput = "the-output-nobody-should-record"
	)
	rec := &recorder{}
	runner := newRunner(t, agents.WithRecorder(rec))

	inv := invocation(t, agents.ShapeText, map[string]string{
		helperModeVar:       "envelope",
		helperResultVar:     secretAgentOutput,
		"ANTHROPIC_API_KEY": secretCredential,
	})
	inv.Prompt = secretPrompt

	got, err := runner.Run(t.Context(), agents.PurposeReview, inv)
	if err != nil {
		t.Fatalf("invocation failed: %v", err)
	}
	// The stand-in agent really did see all three, so their absence below is
	// this package discarding them rather than them never existing.
	if got.Text != secretAgentOutput {
		t.Fatalf("the agent's result did not come back: %q", got.Text)
	}

	if len(rec.records) != 1 {
		t.Fatalf("recorded %d invocations, want 1", len(rec.records))
	}
	rendered := fmt.Sprintf("%+v", rec.records[0])
	for _, secret := range []string{secretPrompt, secretCredential, secretAgentOutput, inv.Dir} {
		if strings.Contains(rendered, secret) {
			t.Errorf("the record carries %q: %s", secret, rendered)
		}
	}
	if !reflect.DeepEqual(rec.records[0], got.Record) {
		t.Errorf("Result.Record is %+v, want the record that was reported: %+v", got.Record, rec.records[0])
	}
}

func TestASuccessfulInvocationRecordsWhatItCost(t *testing.T) {
	rec := &recorder{}
	runner := newRunner(t, agents.WithRecorder(rec))
	inv := invocation(t, agents.ShapeText, map[string]string{
		helperModeVar:   "envelope",
		helperResultVar: "done",
	})
	inv.Model = "asked-for-model"

	before := time.Now()
	if _, err := runner.Run(t.Context(), agents.PurposeDocument, inv); err != nil {
		t.Fatalf("invocation failed: %v", err)
	}
	if len(rec.records) != 1 {
		t.Fatalf("recorded %d invocations, want 1", len(rec.records))
	}
	got := rec.records[0]
	if got.Purpose != agents.PurposeDocument {
		t.Errorf("purpose is %q, want %q", got.Purpose, agents.PurposeDocument)
	}
	if got.Agent != agents.ClaudeName {
		t.Errorf("agent is %q, want %q", got.Agent, agents.ClaudeName)
	}
	if got.Model != "stand-in-model" {
		t.Errorf("model is %q, want the one the agent reported", got.Model)
	}
	if got.Failure != agents.FailureNone {
		t.Errorf("failure is %q, want none", got.Failure)
	}
	if got.Started.Before(before) {
		t.Errorf("started at %v, before the call was made at %v", got.Started, before)
	}
	if got.Duration < 0 {
		t.Errorf("duration is %v", got.Duration)
	}
	if got.Usage != helperUsage() {
		t.Errorf("usage is %+v, want %+v", got.Usage, helperUsage())
	}
}

// A count the agent reported as zero and one it never reported are different
// facts, and only the second may be stored as an unknown. The type is what
// keeps them apart: there is no exported field holding a bare number, so a
// caller reading either one is handed whether it was reported.
func TestAReportedZeroCountIsNotAnUnreportedOne(t *testing.T) {
	usageOf := func(t *testing.T, envelope string) agents.Usage {
		t.Helper()
		rec := &recorder{}
		runner := newRunner(t, agents.WithRecorder(rec))
		inv := invocation(t, agents.ShapeText, map[string]string{
			helperModeVar:   "raw",
			helperStdoutVar: envelope,
		})
		if _, err := runner.Run(t.Context(), agents.PurposeReview, inv); err != nil {
			t.Fatalf("invocation failed: %v", err)
		}
		if len(rec.records) != 1 {
			t.Fatalf("recorded %d invocations, want 1", len(rec.records))
		}
		return rec.records[0].Usage
	}

	reported := usageOf(t, `{"result":"done","num_turns":0,"usage":{"input_tokens":0}}`)
	if n, ok := reported.InputTokens.Value(); !ok || n != 0 {
		t.Errorf("a reported zero reads (%d, %v), want (0, true)", n, ok)
	}
	if n, ok := reported.Turns.Value(); !ok || n != 0 {
		t.Errorf("a reported zero turn count reads (%d, %v), want (0, true)", n, ok)
	}
	// The same envelope said nothing about the rest, and silence is not zero.
	if n, ok := reported.OutputTokens.Value(); ok || n != 0 {
		t.Errorf("an omitted count reads (%d, %v), want (0, false)", n, ok)
	}

	silent := usageOf(t, `{"result":"done"}`)
	if n, ok := silent.InputTokens.Value(); ok || n != 0 {
		t.Errorf("an unreported count reads (%d, %v), want (0, false)", n, ok)
	}
	if reported == silent {
		t.Error("an agent that reported zeros and one that reported nothing recorded the same usage")
	}
}

// The distinction above cannot be kept by a caller that is handed a bare
// number, so it is kept by the type: every count in a Usage carries whether it
// was reported, and none of them can be read without it.
func TestEveryCountInAUsageCarriesWhetherItWasReported(t *testing.T) {
	usage := reflect.TypeFor[agents.Usage]()
	if usage.NumField() == 0 {
		t.Fatal("Usage holds no counts")
	}
	for i := range usage.NumField() {
		field := usage.Field(i)
		if field.Type != reflect.TypeFor[agents.Count]() {
			t.Errorf("Usage.%s is %s, want %s, which a caller cannot read without its reported flag",
				field.Name, field.Type, reflect.TypeFor[agents.Count]())
		}
	}
	count := reflect.TypeFor[agents.Count]()
	for i := range count.NumField() {
		if field := count.Field(i); field.IsExported() {
			t.Errorf("Count.%s is exported, so a count can be read as a bare number", field.Name)
		}
	}
}

// The distinction has to survive the route a record actually takes to a store,
// and JSON is that route. A Count whose fields are unexported and which
// defines neither method encodes to an empty object with no error, which
// destroys both the number and the fact that there was one, so this fails if
// either method is removed.
func TestAUsageSurvivesEncodingAndDecodingWithItsCountsIntact(t *testing.T) {
	original := agents.Usage{
		InputTokens:         agents.ReportedCount(0),
		OutputTokens:        agents.ReportedCount(22),
		CacheReadTokens:     agents.Count{},
		CacheCreationTokens: agents.ReportedCount(-1),
		Turns:               agents.Count{},
	}

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("encoding a usage: %v", err)
	}
	// A reported zero is written as the number it is, and an unreported count
	// as null, so a reader of the stored form can tell them apart too.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("the encoded usage is not an object: %v (%s)", err, encoded)
	}
	if got := string(fields["InputTokens"]); got != "0" {
		t.Errorf("a reported zero encoded as %s, want the number 0", got)
	}
	if got := string(fields["CacheReadTokens"]); got != "null" {
		t.Errorf("an unreported count encoded as %s, want null", got)
	}

	var decoded agents.Usage
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decoding a usage: %v (%s)", err, encoded)
	}
	if decoded != original {
		t.Fatalf("the usage came back as %+v, want %+v", decoded, original)
	}
	// Stated as the two facts the round trip is for, so a failure names which
	// one was lost rather than only that something was.
	if n, ok := decoded.InputTokens.Value(); !ok || n != 0 {
		t.Errorf("a reported zero came back as (%d, %v), want (0, true)", n, ok)
	}
	if n, ok := decoded.CacheReadTokens.Value(); ok || n != 0 {
		t.Errorf("an unreported count came back as (%d, %v), want (0, false)", n, ok)
	}
}

// Decoding refuses what it cannot read rather than quietly calling it
// unreported, because a count that failed to decode and a count nothing was
// reported for are different facts and the second is the one this type stores.
func TestACountThatCannotBeDecodedIsRefused(t *testing.T) {
	var c agents.Count
	if err := json.Unmarshal([]byte(`"eleven"`), &c); err == nil {
		t.Fatalf("a string decoded into a count as %v", c)
	}
	if n, ok := c.Value(); ok || n != 0 {
		t.Errorf("the refused count left (%d, %v), want the zero count", n, ok)
	}
}

// The model a record carries is the one the agent reported when it reported
// one, and the one the invocation asked for when it did not. The second half
// is what keeps a record from saying nothing about which model ran.
func TestTheRecordedModelFallsBackToTheOneAskedFor(t *testing.T) {
	rec := &recorder{}
	runner := newRunner(t, agents.WithRecorder(rec))
	inv := invocation(t, agents.ShapeText, map[string]string{
		helperModeVar:   "raw",
		helperStdoutVar: `{"result":"done","session_id":"s"}`,
	})
	inv.Model = "asked-for-model"

	if _, err := runner.Run(t.Context(), agents.PurposeTest, inv); err != nil {
		t.Fatalf("invocation failed: %v", err)
	}
	if rec.records[0].Model != "asked-for-model" {
		t.Errorf("model is %q, want the one the invocation asked for", rec.records[0].Model)
	}
}
