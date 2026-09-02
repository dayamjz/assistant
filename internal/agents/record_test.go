package agents_test

import (
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
	want := agents.Usage{
		InputTokens:         11,
		OutputTokens:        22,
		CacheReadTokens:     33,
		CacheCreationTokens: 44,
		Turns:               3,
	}
	if got.Usage != want {
		t.Errorf("usage is %+v, want %+v", got.Usage, want)
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
