package standin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/findings"
)

// TestMain turns this binary into the stand-in agent when it was started as
// one. It is the wiring every package using this one needs, and having it here
// means these tests exercise the same path a caller does.
func TestMain(m *testing.M) {
	Main()
	os.Exit(m.Run())
}

// invocation is a valid Invocation in a directory the test owns.
func invocation(t *testing.T, shape agents.Shape) agents.Invocation {
	t.Helper()
	return agents.Invocation{Prompt: "review this change", Shape: shape, Dir: t.TempDir()}
}

// oneStep is a script with a single step answering everything.
func oneStep(reply Reply) Script {
	return Script{Steps: []Step{{Times: Always, Reply: reply}}}
}

// aReport is a report a stage might return, with one finding of each action so
// that what a caller reads back distinguishes them.
func aReport() findings.Report {
	return findings.Report{
		Summary: "one thing was checked",
		Findings: []findings.Finding{
			{ID: "f1", Description: "a real defect", Action: findings.ActionFix, Severity: findings.SeverityError},
			{ID: "f2", Description: "a matter of taste", Action: findings.ActionNote, Severity: findings.SeverityInfo},
		},
	}
}

// failureOf is the failure category the adapter classified err as.
func failureOf(t *testing.T, err error) agents.Failure {
	t.Helper()
	var invocationErr *agents.InvocationError
	if !errors.As(err, &invocationErr) {
		t.Fatalf("expected an *agents.InvocationError, got %v", err)
	}
	return invocationErr.Failure
}

func TestScriptedReportReachesTheCaller(t *testing.T) {
	agent := New(t, oneStep(Report(aReport())))

	result, err := agent.Runner().Run(t.Context(), agents.PurposeReview, invocation(t, agents.ShapeReport))
	if err != nil {
		t.Fatalf("running the scripted report: %v", err)
	}
	if result.Report.Summary != "one thing was checked" {
		t.Errorf("summary is %q, want the one the script described", result.Report.Summary)
	}
	if fixable := result.Report.Fixable(); len(fixable) != 1 || fixable[0].ID != "f1" {
		t.Errorf("fixable findings are %v, want only f1", fixable)
	}
}

// An action nobody recognizes and an absent one are P3's case, and this proves
// the stand-in can drive it: the report is described with the real vocabulary,
// and internal/findings resolves both to an ask on the way back.
func TestUnrecognizedAndMissingActionsBecomeAsks(t *testing.T) {
	report := findings.Report{
		Summary: "two findings arrived unclassified",
		Findings: []findings.Finding{
			{ID: "f1", Description: "carries an action nobody defines", Action: findings.Action("banana")},
			{ID: "f2", Description: "carries no action at all"},
		},
	}
	agent := New(t, oneStep(Report(report)))

	result, err := agent.Runner().Run(t.Context(), agents.PurposeReview, invocation(t, agents.ShapeReport))
	if err != nil {
		t.Fatalf("running the unclassified report: %v", err)
	}
	if held := result.Report.Held(); len(held) != 2 {
		t.Errorf("held findings are %v, want both of them", held)
	}
	if fixable := result.Report.Fixable(); len(fixable) != 0 {
		t.Errorf("fixable findings are %v, want none: an unclassified finding is never fixable", fixable)
	}
}

func TestProseAroundTheReportIsStillRead(t *testing.T) {
	agent := New(t, oneStep(Prose("I looked at the change. Here is what I found.", aReport())))

	result, err := agent.Runner().Run(t.Context(), agents.PurposeReview, invocation(t, agents.ShapeReport))
	if err != nil {
		t.Fatalf("running the prose-wrapped report: %v", err)
	}
	if result.Report.Summary != "one thing was checked" {
		t.Errorf("summary is %q, want the report out of the prose", result.Report.Summary)
	}
}

func TestMalformedOutputIsAnOutputFailure(t *testing.T) {
	agent := New(t, oneStep(Malformed("I could not finish, sorry.")))

	_, err := agent.Runner().Run(t.Context(), agents.PurposeReview, invocation(t, agents.ShapeReport))
	if got := failureOf(t, err); got != agents.FailureOutput {
		t.Errorf("failure is %q, want %q", got, agents.FailureOutput)
	}
}

func TestResultThatIsNotAReportIsAnOutputFailure(t *testing.T) {
	agent := New(t, oneStep(Text("I reviewed it and it looks fine to me.")))

	_, err := agent.Runner().Run(t.Context(), agents.PurposeReview, invocation(t, agents.ShapeReport))
	if got := failureOf(t, err); got != agents.FailureOutput {
		t.Errorf("failure is %q, want %q", got, agents.FailureOutput)
	}
	// The same envelope is a usable result when the invocation asked for text,
	// so the refusal above is about the shape asked for rather than about the
	// stand-in printing something the adapter cannot read.
	result, err := agent.Runner().Run(t.Context(), agents.PurposeIntent, invocation(t, agents.ShapeText))
	if err != nil {
		t.Fatalf("running the same reply as text: %v", err)
	}
	if result.Text != "I reviewed it and it looks fine to me." {
		t.Errorf("text is %q, want what the script described", result.Text)
	}
}

func TestOversizeOutputIsRefused(t *testing.T) {
	agent := New(t, oneStep(Oversize()))

	_, err := agent.Runner().Run(t.Context(), agents.PurposeReview, invocation(t, agents.ShapeText))
	if got := failureOf(t, err); got != agents.FailureOversize {
		t.Errorf("failure is %q, want %q", got, agents.FailureOversize)
	}
}

func TestAgentReportingItsOwnFailure(t *testing.T) {
	agent := New(t, oneStep(Failed("I ran out of context")))

	_, err := agent.Runner().Run(t.Context(), agents.PurposeReview, invocation(t, agents.ShapeText))
	if got := failureOf(t, err); got != agents.FailureAgent {
		t.Errorf("failure is %q, want %q", got, agents.FailureAgent)
	}
	var refusal *agents.InvocationError
	if errors.As(err, &refusal) && refusal.Message != "I ran out of context" {
		t.Errorf("the refusal says %q, want what the agent said about its own failure", refusal.Message)
	}
}

// A non-zero exit paired with a result envelope is one invocation, not two
// cases: the adapter refuses it and still records what the envelope said it
// cost, and the stand-in has to be able to produce both halves at once for
// that to be assertable at all.
func TestEnvelopeWithANonZeroExit(t *testing.T) {
	spy := &recorder{}
	agent := New(t, oneStep(Report(aReport()).WithExit(1).WithStderr("something went wrong")),
		agents.WithRecorder(spy))

	_, err := agent.Runner().Run(t.Context(), agents.PurposeReview, invocation(t, agents.ShapeReport))
	if got := failureOf(t, err); got != agents.FailureExit {
		t.Errorf("failure is %q, want %q", got, agents.FailureExit)
	}
	if len(spy.records) != 1 {
		t.Fatalf("recorded %d invocations, want one", len(spy.records))
	}
	if got := spy.records[0].Usage; got != DefaultUsage() {
		t.Errorf("recorded usage is %+v, want the envelope's %+v", got, DefaultUsage())
	}
}

func TestHangIsEndedByCancellation(t *testing.T) {
	agent := New(t, oneStep(Hang()))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// The stand-in records what it was asked before it holds, so cancelling
	// once the call is recorded cancels an invocation that has really started.
	done := make(chan error, 1)
	go func() {
		_, err := agent.Runner().Run(ctx, agents.PurposeReview, invocation(t, agents.ShapeText))
		done <- err
	}()
	waitForCalls(t, agent, 1)
	cancel()

	if got := failureOf(t, <-done); got != agents.FailureCancelled {
		t.Errorf("failure is %q, want %q", got, agents.FailureCancelled)
	}
}

// The envelope this package writes and the envelope internal/agents reads are
// two spellings of one wire contract, and nothing in Go checks them against
// each other. This does: every field the stand-in states is asserted where the
// adapter reports it, so a key renamed on one side fails here rather than
// reading back as unreported.
func TestAdapterReadsBackEveryStatedField(t *testing.T) {
	usage := agents.Usage{
		InputTokens:         agents.ReportedCount(7),
		OutputTokens:        agents.ReportedCount(8),
		CacheReadTokens:     agents.ReportedCount(9),
		CacheCreationTokens: agents.ReportedCount(10),
		Turns:               agents.ReportedCount(11),
	}
	reply := Text("the agent's final word")
	reply.Envelope.Session = SessionID("session-abc")
	reply.Envelope.Model = "model-abc"
	reply.Envelope.Usage = usage
	spy := &recorder{}
	agent := New(t, oneStep(reply), agents.WithRecorder(spy))

	result, err := agent.Runner().Run(t.Context(), agents.PurposeReview, invocation(t, agents.ShapeText))
	if err != nil {
		t.Fatalf("running the fully stated envelope: %v", err)
	}
	if result.Text != "the agent's final word" {
		t.Errorf("result is %q, want what the envelope stated", result.Text)
	}
	if result.Record.Model != "model-abc" {
		t.Errorf("recorded model is %q, want the one the envelope stated", result.Record.Model)
	}
	if result.Record.Usage != usage {
		t.Errorf("recorded usage is %+v, want %+v", result.Record.Usage, usage)
	}
	if len(spy.records) != 1 || spy.records[0] != result.Record {
		t.Errorf("the recorder was given %v, want the one record this invocation produced", spy.records)
	}
	// The session the envelope states reaches the adapter too, and a Fixer is
	// where that is observable.
	fixer, err := agents.OpenFixer(t.Context(), agent.Runner(), "")
	if err != nil {
		t.Fatalf("opening a fixer: %v", err)
	}
	if _, err := fixer.Apply(t.Context(), invocation(t, agents.ShapeText)); err != nil {
		t.Fatalf("applying a fix: %v", err)
	}
	if fixer.Reference() != "session-abc" {
		t.Errorf("the fixer holds %q, want the session the envelope stated", fixer.Reference())
	}
	// The subtype is the one key left, and the adapter reads it in one place:
	// it is what a failure the agent reported no result for says about itself.
	// It takes an invocation of its own, because an envelope that reaches it
	// is one the adapter refuses.
	subtyped := New(t, oneStep(Reply{Envelope: &Envelope{
		IsError: true,
		Subtype: "error_max_turns",
		Usage:   DefaultUsage(),
	}}))
	_, err = subtyped.Runner().Run(t.Context(), agents.PurposeReview, invocation(t, agents.ShapeText))
	var refusal *agents.InvocationError
	if !errors.As(err, &refusal) {
		t.Fatalf("expected an *agents.InvocationError, got %v", err)
	}
	if refusal.Failure != agents.FailureAgent {
		t.Errorf("failure is %q, want %q", refusal.Failure, agents.FailureAgent)
	}
	if refusal.Message != "error_max_turns" {
		t.Errorf("the refusal says %q, want the subtype the envelope stated", refusal.Message)
	}
}

// An unreported count is a shape the adapter models deliberately, so the
// stand-in has to be able to put one on the wire.
func TestAnUnreportedCountArrivesUnreported(t *testing.T) {
	reply := Text("done")
	reply.Envelope.Usage = agents.Usage{InputTokens: agents.ReportedCount(0)}
	agent := New(t, oneStep(reply))

	result, err := agent.Runner().Run(t.Context(), agents.PurposeReview, invocation(t, agents.ShapeText))
	if err != nil {
		t.Fatalf("running the envelope with unreported counts: %v", err)
	}
	if n, reported := result.Record.Usage.InputTokens.Value(); !reported || n != 0 {
		t.Errorf("input tokens read back as (%d, %t), want a reported zero", n, reported)
	}
	if _, reported := result.Record.Usage.OutputTokens.Value(); reported {
		t.Error("output tokens read back as reported, want unreported")
	}
}

// P4 asked of the wire rather than of the adapter's own account: a review
// invocation's command line carries no session, and a fixer's second round
// carries the one its first round reported.
func TestReviewCarriesNoSessionAndAFixerRoundDoes(t *testing.T) {
	agent := New(t, oneStep(Text("done")))
	runner := agent.Runner()

	if _, err := runner.Run(t.Context(), agents.PurposeReview, invocation(t, agents.ShapeText)); err != nil {
		t.Fatalf("running a review: %v", err)
	}
	fixer, err := agents.OpenFixer(t.Context(), runner, "")
	if err != nil {
		t.Fatalf("opening a fixer: %v", err)
	}
	for round := range 2 {
		if _, err := fixer.Apply(t.Context(), invocation(t, agents.ShapeText)); err != nil {
			t.Fatalf("fix round %d: %v", round, err)
		}
	}

	calls := agent.Calls()
	if len(calls) != 3 {
		t.Fatalf("the stand-in was asked %d times, want three: %s", len(calls), calls)
	}
	if got := calls[0].Session(); got != "" {
		t.Errorf("the review carried session %q, want none", got)
	}
	if got := calls[1].Session(); got != "" {
		t.Errorf("the first fix round carried session %q, want none: it opens one", got)
	}
	opened := fixer.Reference()
	if opened == "" {
		t.Fatal("the fixer holds no session reference after two rounds")
	}
	if got := calls[2].Session(); got != opened {
		t.Errorf("the second fix round carried session %q, want %q", got, opened)
	}
}

func TestAnEnvelopeReportingNoSessionLeavesTheFixerNothing(t *testing.T) {
	reply := Text("done")
	reply.Envelope.Session = NoSession()
	agent := New(t, oneStep(reply))

	fixer, err := agents.OpenFixer(t.Context(), agent.Runner(), "")
	if err != nil {
		t.Fatalf("opening a fixer: %v", err)
	}
	if _, err := fixer.Apply(t.Context(), invocation(t, agents.ShapeText)); err != nil {
		t.Fatalf("applying a fix: %v", err)
	}
	if got := fixer.Reference(); got != "" {
		t.Errorf("the fixer holds %q, want nothing to continue", got)
	}
}

// A Reply derived with a With method owns its envelope, so a script that
// adjusts the derived one still answers the base with what the base states.
func TestADerivedReplyDoesNotShareTheBaseEnvelope(t *testing.T) {
	base := Text("answered by the base")
	derived := base.WithExit(1)
	derived.Envelope.Result = "answered by the derived reply"
	derived.Envelope.Model = "model-derived"
	agent := New(t, Script{Steps: []Step{
		{Match: Match{PromptContains: "derived"}, Times: Always, Reply: derived},
		{Match: Match{PromptContains: "base"}, Times: Always, Reply: base},
	}})

	ask := invocation(t, agents.ShapeText)
	ask.Prompt = "answer from the base step"
	result, err := agent.Runner().Run(t.Context(), agents.PurposeReview, ask)
	if err != nil {
		t.Fatalf("running the base invocation: %v", err)
	}
	if result.Text != "answered by the base" {
		t.Errorf("the base step answered %q, want what the base envelope states", result.Text)
	}
	if result.Record.Model != DefaultModel {
		t.Errorf("the base step reported model %q, want %q", result.Record.Model, DefaultModel)
	}
}

func TestCallReportsWhatTheAgentWasAsked(t *testing.T) {
	agent := New(t, oneStep(Text("done")))
	inv := invocation(t, agents.ShapeText)
	inv.Prompt = "a prompt only this test writes"
	inv.Model = "some-model"

	if _, err := agent.Runner().Run(t.Context(), agents.PurposeReview, inv); err != nil {
		t.Fatalf("running the invocation: %v", err)
	}

	call := agent.Call()
	if call.Prompt != inv.Prompt {
		t.Errorf("the prompt on standard input was %q, want %q", call.Prompt, inv.Prompt)
	}
	if call.Model() != "some-model" {
		t.Errorf("the command line asked for model %q, want %q", call.Model(), "some-model")
	}
	if !call.Answered() || call.Step != 0 {
		t.Errorf("the call was answered by step %d, want step 0", call.Step)
	}
	want, err := filepath.EvalSymlinks(inv.Dir)
	if err != nil {
		t.Fatalf("resolving the invocation's directory: %v", err)
	}
	got, err := filepath.EvalSymlinks(call.Dir)
	if err != nil {
		t.Fatalf("resolving the directory the stand-in reported: %v", err)
	}
	if got != want {
		t.Errorf("the stand-in ran in %q, want %q", got, want)
	}
}

func TestStepsAnswerInTheOrderTheyAreWritten(t *testing.T) {
	agent := New(t, Script{Steps: []Step{
		{Reply: Text("first")},
		{Reply: Text("second")},
	}})

	for _, want := range []string{"first", "second"} {
		result, err := agent.Runner().Run(t.Context(), agents.PurposeReview, invocation(t, agents.ShapeText))
		if err != nil {
			t.Fatalf("running for %q: %v", want, err)
		}
		if result.Text != want {
			t.Errorf("the agent answered %q, want %q", result.Text, want)
		}
	}
}

func TestTimesBoundsHowManyInvocationsAStepAnswers(t *testing.T) {
	agent := New(t, Script{Steps: []Step{
		{Times: 2, Reply: Text("bounded")},
		{Times: Always, Reply: Text("the rest")},
	}})

	for _, want := range []string{"bounded", "bounded", "the rest", "the rest"} {
		result, err := agent.Runner().Run(t.Context(), agents.PurposeReview, invocation(t, agents.ShapeText))
		if err != nil {
			t.Fatalf("running for %q: %v", want, err)
		}
		if result.Text != want {
			t.Errorf("the agent answered %q, want %q", result.Text, want)
		}
	}
}

func TestMatchSelectsByPromptAndBySession(t *testing.T) {
	agent := New(t, Script{Steps: []Step{
		{Match: Match{PromptContains: "lint"}, Times: Always, Reply: Text("linted")},
		{Match: Match{Resumed: Resumed()}, Times: Always, Reply: Text("resumed")},
		{Match: Match{Resumed: Fresh()}, Times: Always, Reply: Text("anything else")},
	}})
	runner := agent.Runner()

	lint := invocation(t, agents.ShapeText)
	lint.Prompt = "run the lint stage"
	result, err := runner.Run(t.Context(), agents.PurposeLint, lint)
	if err != nil {
		t.Fatalf("running the lint invocation: %v", err)
	}
	if result.Text != "linted" {
		t.Errorf("the lint invocation was answered %q, want %q", result.Text, "linted")
	}

	result, err = runner.Run(t.Context(), agents.PurposeReview, invocation(t, agents.ShapeText))
	if err != nil {
		t.Fatalf("running the review invocation: %v", err)
	}
	if result.Text != "anything else" {
		t.Errorf("the review invocation was answered %q, want %q", result.Text, "anything else")
	}

	fixer, err := agents.OpenFixer(t.Context(), runner, "an-earlier-session")
	if err != nil {
		t.Fatalf("opening a fixer: %v", err)
	}
	result, err = fixer.Apply(t.Context(), invocation(t, agents.ShapeText))
	if err != nil {
		t.Fatalf("applying a fix: %v", err)
	}
	if result.Text != "resumed" {
		t.Errorf("the resumed fix round was answered %q, want %q", result.Text, "resumed")
	}
}

// An invocation the script does not cover fails loudly and is recorded as
// unanswered, rather than being given a default nobody wrote.
func TestAnUnscriptedInvocationFails(t *testing.T) {
	agent := New(t, Script{Steps: []Step{{Match: Match{PromptContains: "never appears"}, Reply: Text("unreachable")}}})

	_, err := agent.Runner().Run(t.Context(), agents.PurposeReview, invocation(t, agents.ShapeText))
	if got := failureOf(t, err); got != agents.FailureExit {
		t.Fatalf("failure is %q, want %q", got, agents.FailureExit)
	}
	var invocationErr *agents.InvocationError
	if errors.As(err, &invocationErr) && invocationErr.ExitCode != ExitUnscripted {
		t.Errorf("the stand-in exited %d, want ExitUnscripted (%d)", invocationErr.ExitCode, ExitUnscripted)
	}
	if call := agent.Call(); call.Answered() {
		t.Errorf("the call reports step %d, want it recorded as unanswered", call.Step)
	}
}

// Overlapping invocations still take one use each, which is what makes the
// stand-in usable under a graph that runs stages concurrently. The race
// detector runs this alongside the assertion.
func TestConcurrentInvocationsEachTakeOneUse(t *testing.T) {
	const invocations = 4
	steps := make([]Step, invocations)
	for i := range steps {
		steps[i] = Step{Reply: Text("answer")}
	}
	agent := New(t, Script{Steps: steps})

	errs := make(chan error, invocations)
	for range invocations {
		go func() {
			_, err := agent.Runner().Run(t.Context(), agents.PurposeReview, invocation(t, agents.ShapeText))
			errs <- err
		}()
	}
	for range invocations {
		if err := <-errs; err != nil {
			t.Errorf("a concurrent invocation failed: %v", err)
		}
	}

	seen := map[int]bool{}
	for _, call := range agent.Calls() {
		if seen[call.Step] {
			t.Errorf("step %d answered more than one invocation, and it has one use", call.Step)
		}
		seen[call.Step] = true
	}
	if len(seen) != invocations {
		t.Errorf("%d steps answered %d invocations, want one each", len(seen), invocations)
	}
}

// recorder collects every Record an invocation produced.
type recorder struct{ records []agents.Record }

func (r *recorder) RecordInvocation(rec agents.Record) { r.records = append(r.records, rec) }

// waitForCalls blocks until the stand-in has recorded at least n calls. It is
// bounded so that a stand-in which never records anything fails here, saying
// what it was waiting for, rather than hanging until the whole test binary is
// killed for taking too long.
func waitForCalls(t *testing.T, agent *Agent, n int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for len(agent.Calls()) < n {
		if !time.Now().Before(deadline) {
			t.Fatalf("the stand-in recorded %d calls, waited for %d", len(agent.Calls()), n)
		}
		time.Sleep(time.Millisecond)
	}
}
