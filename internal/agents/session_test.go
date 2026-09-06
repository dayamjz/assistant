package agents_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
)

// P4 says reviewing and fixing are separate roles with separate memory. The
// three tests below assert that structurally: the type an invocation is
// described by has nowhere to name a session, the interface method every
// review goes through has no session parameter and no way to obtain one, and
// the only route to a session produces fix invocations and nothing else.
//
// Each of them fails if the corresponding piece of the arrangement is removed,
// which is what makes them a guard rather than a description.

func TestInvocationHasNoSessionField(t *testing.T) {
	typ := reflect.TypeFor[agents.Invocation]()
	for i := range typ.NumField() {
		field := typ.Field(i)
		if strings.Contains(strings.ToLower(field.Name), "session") {
			t.Errorf("Invocation.%s would let a caller attach a session to a review", field.Name)
		}
		if strings.Contains(strings.ToLower(field.Type.String()), "session") {
			t.Errorf("Invocation.%s is of session type %s", field.Name, field.Type)
		}
	}
}

func TestTheSessionFreeAndSessionCarryingRoutesAreDifferentMethods(t *testing.T) {
	runner := reflect.TypeFor[agents.Runner]()

	run, ok := runner.MethodByName("Run")
	if !ok {
		t.Fatal("Runner has no Run method")
	}
	// Every review in the product goes through Run. Its parameters are the
	// context, the purpose, and the invocation, and nothing else: a session
	// cannot be passed to it, and it returns nothing a session could be read
	// out of and fed back in.
	wantIn := []reflect.Type{
		reflect.TypeFor[context.Context](),
		reflect.TypeFor[agents.Purpose](),
		reflect.TypeFor[agents.Invocation](),
	}
	if got := methodIn(run.Type); !reflect.DeepEqual(got, wantIn) {
		t.Errorf("Runner.Run takes %v, want exactly %v", got, wantIn)
	}
	wantOut := []reflect.Type{reflect.TypeFor[agents.Result](), reflect.TypeFor[error]()}
	if got := methodOut(run.Type); !reflect.DeepEqual(got, wantOut) {
		t.Errorf("Runner.Run returns %v, want exactly %v", got, wantOut)
	}

	// The session-carrying route is a different method with a different type,
	// and the one invocation it can make takes no purpose, so it cannot be
	// asked to review.
	apply, ok := reflect.TypeFor[agents.Fixer]().MethodByName("Apply")
	if !ok {
		t.Fatal("Fixer has no Apply method")
	}
	applyIn := methodIn(apply.Type)
	wantApply := []reflect.Type{
		reflect.TypeFor[context.Context](),
		reflect.TypeFor[agents.Invocation](),
	}
	if !reflect.DeepEqual(applyIn, wantApply) {
		t.Errorf("Fixer.Apply takes %v, want exactly %v", applyIn, wantApply)
	}
	for _, in := range applyIn {
		if in == reflect.TypeFor[agents.Purpose]() {
			t.Error("Fixer.Apply takes a purpose, so a session could be pointed at a review")
		}
	}
}

func methodIn(t reflect.Type) []reflect.Type {
	out := make([]reflect.Type, 0, t.NumIn())
	for i := range t.NumIn() {
		out = append(out, t.In(i))
	}
	return out
}

func methodOut(t reflect.Type) []reflect.Type {
	out := make([]reflect.Type, 0, t.NumOut())
	for i := range t.NumOut() {
		out = append(out, t.Out(i))
	}
	return out
}

// The same fact read off the invocation records, which is the assertion PRD
// section 13 asks for. The stand-in agent reports a session identifier on
// every invocation, so a record reading SessionNone is evidence that this
// package discarded it rather than evidence that there was nothing to discard.
func TestEveryRunInvocationIsRecognizedAndSessionFree(t *testing.T) {
	rec := &recorder{}
	runner := newRunner(t, agents.WithRecorder(rec))
	inv := invocation(t, agents.ShapeText, map[string]string{
		helperModeVar:   "envelope",
		helperResultVar: "done",
	})

	for _, p := range []agents.Purpose{
		agents.PurposeIntent, agents.PurposeRebase, agents.PurposeReview,
		agents.PurposeTest, agents.PurposeDocument, agents.PurposeLint,
		agents.PurposeChecks, agents.PurposeFix,
	} {
		// This is also the accepting side of the purpose rule: every purpose
		// this package defines runs, and only an unrecognized one is refused.
		if _, err := runner.Run(t.Context(), p, inv); err != nil {
			t.Fatalf("purpose %s failed: %v", p, err)
		}
	}
	if len(rec.records) != 8 {
		t.Fatalf("recorded %d invocations, want 8", len(rec.records))
	}
	for _, r := range rec.records {
		if r.Session != agents.SessionNone {
			t.Errorf("a %s invocation made through Run is recorded as %q", r.Purpose, r.Session)
		}
	}
}

func TestOnlyTheFixerCarriesASessionAcrossRounds(t *testing.T) {
	rec := &recorder{}
	runner := newRunner(t, agents.WithRecorder(rec))
	fixer, err := agents.OpenFixer(t.Context(), runner, "")
	if err != nil {
		t.Fatalf("opening a fixer session: %v", err)
	}
	if fixer.Reference() != "" {
		t.Errorf("a fixer that has run nothing already holds %q", fixer.Reference())
	}

	inv := invocation(t, agents.ShapeText, map[string]string{helperModeVar: "call"})
	first, err := fixer.Apply(t.Context(), inv)
	if err != nil {
		t.Fatalf("the first fix round failed: %v", err)
	}
	if resumed := decodeCall(t, first.Text).resumedSession(); resumed != "" {
		t.Errorf("the first round resumed %q", resumed)
	}
	if fixer.Reference() != "session-opened" {
		t.Errorf("the fixer holds %q after its first round, want the agent's session", fixer.Reference())
	}

	second, err := fixer.Apply(t.Context(), inv)
	if err != nil {
		t.Fatalf("the second fix round failed: %v", err)
	}
	if resumed := decodeCall(t, second.Text).resumedSession(); resumed != "session-opened" {
		t.Errorf("the second round resumed %q, want the session the first round opened", resumed)
	}

	if len(rec.records) != 2 {
		t.Fatalf("recorded %d invocations, want 2", len(rec.records))
	}
	if rec.records[0].Purpose != agents.PurposeFix || rec.records[0].Session != agents.SessionOpened {
		t.Errorf("first record is %+v, want a fix that opened the session", rec.records[0])
	}
	if rec.records[1].Purpose != agents.PurposeFix || rec.records[1].Session != agents.SessionResumed {
		t.Errorf("second record is %+v, want a fix that resumed the session", rec.records[1])
	}
}

// A fixer session survives a restart: the reference is handed back and the
// next round continues the same conversation rather than opening a new one.
func TestAFixerSessionCanBeResumedFromItsReference(t *testing.T) {
	rec := &recorder{}
	runner := newRunner(t, agents.WithRecorder(rec))
	fixer, err := agents.OpenFixer(t.Context(), runner, "session-from-an-earlier-service")
	if err != nil {
		t.Fatalf("resuming a fixer session: %v", err)
	}

	got, err := fixer.Apply(t.Context(), invocation(t, agents.ShapeText, map[string]string{helperModeVar: "call"}))
	if err != nil {
		t.Fatalf("the resumed round failed: %v", err)
	}
	if resumed := decodeCall(t, got.Text).resumedSession(); resumed != "session-from-an-earlier-service" {
		t.Errorf("the round resumed %q, want the session it was given", resumed)
	}
	if len(rec.records) != 1 || rec.records[0].Session != agents.SessionResumed {
		t.Errorf("records are %+v, want one resumed fix", rec.records)
	}
}

// A round whose agent reports no session reference leaves the fixer with
// nothing to continue. The fix itself stands, and the loss shows up in what
// was recorded rather than being silent.
func TestAFixRoundWithNoSessionReferenceIsRecordedAsSessionFree(t *testing.T) {
	rec := &recorder{}
	runner := newRunner(t, agents.WithRecorder(rec))
	fixer, err := agents.OpenFixer(t.Context(), runner, "")
	if err != nil {
		t.Fatalf("opening a fixer session: %v", err)
	}

	got, err := fixer.Apply(t.Context(), invocation(t, agents.ShapeText, map[string]string{
		helperModeVar:   "raw",
		helperStdoutVar: `{"result":"the fix was applied"}`,
	}))
	if err != nil {
		t.Fatalf("the fix round failed: %v", err)
	}
	if got.Text != "the fix was applied" {
		t.Errorf("the round's result was discarded: %q", got.Text)
	}
	if fixer.Reference() != "" {
		t.Errorf("the fixer holds %q after a round that reported no session", fixer.Reference())
	}
	if len(rec.records) != 1 || rec.records[0].Session != agents.SessionNone {
		t.Errorf("records are %+v, want one fix recorded as session-free", rec.records)
	}
}

// A fix round that opened a session and then failed keeps the session. The
// round may already have edited files, so the next round continues the
// conversation those edits were made in rather than starting blind, and the
// record says what actually happened: a session was opened, and the round
// failed on its output.
//
// The two assertions are one fact read from two places, which is the point. If
// the reference is dropped on the failure path the first fails; if the record
// claims an opened session the fixer does not hold, the pair disagree.
func TestAFailedFixRoundKeepsTheSessionItOpened(t *testing.T) {
	rec := &recorder{}
	runner := newRunner(t, agents.WithRecorder(rec))
	fixer, err := agents.OpenFixer(t.Context(), runner, "")
	if err != nil {
		t.Fatalf("opening a fixer session: %v", err)
	}

	// The agent opens a session and prints a well-formed envelope whose result
	// is not a stage report, so the round fails after the session exists.
	failing := invocation(t, agents.ShapeReport, map[string]string{
		helperModeVar:   "envelope",
		helperResultVar: "I edited some files and then said something unparseable",
	})
	_, err = fixer.Apply(t.Context(), failing)
	var refusal *agents.InvocationError
	if !errors.As(err, &refusal) {
		t.Fatalf("the round is not an *InvocationError: %v", err)
	}
	if refusal.Failure != agents.FailureOutput {
		t.Fatalf("failure category is %q, want %q", refusal.Failure, agents.FailureOutput)
	}

	if fixer.Reference() != "session-opened" {
		t.Errorf("the fixer holds %q after a failed round that opened a session, want it kept", fixer.Reference())
	}
	if len(rec.records) != 1 {
		t.Fatalf("recorded %d invocations, want 1", len(rec.records))
	}
	if got := rec.records[0]; got.Session != agents.SessionOpened || got.Failure != agents.FailureOutput {
		t.Errorf("the record is %+v, want an opened session and a failure on the output", got)
	}

	// The next round continues that conversation rather than opening another.
	next, err := fixer.Apply(t.Context(), invocation(t, agents.ShapeText, map[string]string{helperModeVar: "call"}))
	if err != nil {
		t.Fatalf("the round after the failed one failed: %v", err)
	}
	if resumed := decodeCall(t, next.Text).resumedSession(); resumed != "session-opened" {
		t.Errorf("the next round resumed %q, want the session the failed round opened", resumed)
	}
	if len(rec.records) != 2 || rec.records[1].Session != agents.SessionResumed {
		t.Errorf("records are %+v, want the second recorded as a resumed fix", rec.records)
	}
}
