package agents_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/findings"
)

func TestATextInvocationReturnsTheAgentsWords(t *testing.T) {
	runner := newRunner(t)
	inv := invocation(t, agents.ShapeText, map[string]string{
		helperModeVar:   "envelope",
		helperResultVar: "the change rebases cleanly",
	})

	got, err := runner.Run(t.Context(), agents.PurposeIntent, inv)
	if err != nil {
		t.Fatalf("a well-formed invocation failed: %v", err)
	}
	if got.Text != "the change rebases cleanly" {
		t.Errorf("text is %q, want the agent's result", got.Text)
	}
	if len(got.Report.Findings) != 0 || got.Report.Summary != "" {
		t.Errorf("a text invocation returned a report: %+v", got.Report)
	}
}

func TestAReportInvocationReturnsAValidatedReport(t *testing.T) {
	runner := newRunner(t)
	inv := invocation(t, agents.ShapeReport, map[string]string{
		helperModeVar:   "envelope",
		helperResultVar: reportJSON,
	})

	got, err := runner.Run(t.Context(), agents.PurposeReview, inv)
	if err != nil {
		t.Fatalf("a well-formed report invocation failed: %v", err)
	}
	if got.Report.Summary != "one thing was checked" {
		t.Errorf("summary is %q, want the agent's summary", got.Report.Summary)
	}
	if len(got.Report.Findings) != 1 {
		t.Fatalf("report holds %d findings, want 1", len(got.Report.Findings))
	}
	// The report came back through internal/findings, so it is normalized:
	// identifiers are assigned and the action vocabulary is the one that
	// package owns.
	finding := got.Report.Findings[0]
	if finding.ID == "" {
		t.Error("the finding has no identifier, so the report was not normalized")
	}
	if finding.Action != findings.ActionFix {
		t.Errorf("action is %q, want %q", finding.Action, findings.ActionFix)
	}
}

// The fail-closed default belongs to internal/findings, and this asserts this
// package routes through it rather than reimplementing it: an unclassified
// finding arrives as an ask and is not fix-eligible.
func TestAReportInvocationInheritsTheFailClosedDefault(t *testing.T) {
	runner := newRunner(t)
	inv := invocation(t, agents.ShapeReport, map[string]string{
		helperModeVar:   "envelope",
		helperResultVar: `{"summary":"checked","findings":[{"description":"unclassified"}]}`,
	})

	got, err := runner.Run(t.Context(), agents.PurposeReview, inv)
	if err != nil {
		t.Fatalf("a report with an unclassified finding failed: %v", err)
	}
	if len(got.Report.Findings) != 1 {
		t.Fatalf("report holds %d findings, want 1", len(got.Report.Findings))
	}
	if got.Report.Findings[0].Action != findings.ActionAsk {
		t.Errorf("action is %q, want %q", got.Report.Findings[0].Action, findings.ActionAsk)
	}
	if len(got.Report.Fixable()) != 0 {
		t.Error("an unclassified finding came back fix-eligible")
	}
}

func TestMalformedOutputIsATypedRefusalAndNeverAnEmptyResult(t *testing.T) {
	cases := []struct {
		name    string
		shape   agents.Shape
		env     map[string]string
		mention string
	}{
		{
			name:    "not a result envelope",
			shape:   agents.ShapeText,
			env:     map[string]string{helperModeVar: "raw", helperStdoutVar: "I am prose, not JSON"},
			mention: "result envelope",
		},
		{
			name:    "an envelope with no result in it",
			shape:   agents.ShapeText,
			env:     map[string]string{helperModeVar: "raw", helperStdoutVar: `{"session_id":"s"}`},
			mention: "carried no result",
		},
		{
			name:    "a result that is not a report",
			shape:   agents.ShapeReport,
			env:     map[string]string{helperModeVar: "envelope", helperResultVar: "I looked and it seemed fine"},
			mention: "not a stage report",
		},
		{
			name:  "a report with no summary",
			shape: agents.ShapeReport,
			env: map[string]string{
				helperModeVar:   "envelope",
				helperResultVar: `{"summary":"","findings":[]}`,
			},
			mention: "not a stage report",
		},
		{
			name:  "a report whose findings are not a list",
			shape: agents.ShapeReport,
			env: map[string]string{
				helperModeVar:   "envelope",
				helperResultVar: `{"summary":"checked","findings":"none"}`,
			},
			mention: "not a stage report",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			runner := newRunner(t, agents.WithRecorder(rec))

			got, err := runner.Run(t.Context(), agents.PurposeReview, invocation(t, tc.shape, tc.env))
			if err == nil {
				t.Fatalf("malformed output was accepted, returning %+v", got)
			}
			var refusal *agents.InvocationError
			if !errors.As(err, &refusal) {
				t.Fatalf("refusal is not an *InvocationError: %v", err)
			}
			if refusal.Failure != agents.FailureOutput {
				t.Errorf("failure category is %q, want %q", refusal.Failure, agents.FailureOutput)
			}
			if refusal.Purpose != agents.PurposeReview || refusal.Agent != agents.ClaudeName {
				t.Errorf("refusal does not name the invocation: %+v", refusal)
			}
			if !strings.Contains(refusal.Error(), tc.mention) {
				t.Errorf("refusal does not say what failed (%q): %v", tc.mention, refusal)
			}
			if got.Text != "" || got.Report.Summary != "" || len(got.Report.Findings) != 0 {
				t.Errorf("a refused invocation still returned content: %+v", got)
			}
			if len(rec.records) != 1 || rec.records[0].Failure != agents.FailureOutput {
				t.Errorf("records are %+v, want one recording the failure", rec.records)
			}
		})
	}
}

// A report the agent meant to write, in a shape internal/findings refuses, is
// still a refusal from this package rather than an empty report. This is the
// same guard from the other side: the accepting path is above.
func TestARefusedReportCarriesTheParsersOwnError(t *testing.T) {
	runner := newRunner(t)
	inv := invocation(t, agents.ShapeReport, map[string]string{
		helperModeVar:   "envelope",
		helperResultVar: "nothing that resembles an object",
	})

	_, err := runner.Run(t.Context(), agents.PurposeReview, inv)
	if !errors.Is(err, findings.ErrNoReport) {
		t.Fatalf("refusal does not carry the parser's error: %v", err)
	}
	if !errors.Is(err, agents.ErrInvocationFailed) {
		t.Errorf("refusal does not match the invocation failure class: %v", err)
	}
}

func TestANonZeroExitIsATypedRefusal(t *testing.T) {
	rec := &recorder{}
	runner := newRunner(t, agents.WithRecorder(rec))
	inv := invocation(t, agents.ShapeText, map[string]string{
		helperModeVar:   "fail",
		helperExitVar:   "7",
		helperStderrVar: "the agent could not start a session",
	})

	_, err := runner.Run(t.Context(), agents.PurposeTest, inv)
	var refusal *agents.InvocationError
	if !errors.As(err, &refusal) {
		t.Fatalf("refusal is not an *InvocationError: %v", err)
	}
	if refusal.Failure != agents.FailureExit {
		t.Errorf("failure category is %q, want %q", refusal.Failure, agents.FailureExit)
	}
	if refusal.ExitCode != 7 {
		t.Errorf("exit code is %d, want 7", refusal.ExitCode)
	}
	if !strings.Contains(refusal.Message, "could not start a session") {
		t.Errorf("the agent's own message was not carried: %q", refusal.Message)
	}
	if len(rec.records) != 1 || rec.records[0].Failure != agents.FailureExit {
		t.Errorf("records are %+v, want one recording the exit failure", rec.records)
	}
}

func TestAnAgentReportingItsOwnFailureIsATypedRefusal(t *testing.T) {
	runner := newRunner(t)
	inv := invocation(t, agents.ShapeText, map[string]string{
		helperModeVar:   "error-envelope",
		helperResultVar: "the working copy is not clean",
	})

	_, err := runner.Run(t.Context(), agents.PurposeLint, inv)
	var refusal *agents.InvocationError
	if !errors.As(err, &refusal) {
		t.Fatalf("refusal is not an *InvocationError: %v", err)
	}
	if refusal.Failure != agents.FailureAgent {
		t.Errorf("failure category is %q, want %q", refusal.Failure, agents.FailureAgent)
	}
	if !strings.Contains(refusal.Message, "working copy is not clean") {
		t.Errorf("the agent's own message was not carried: %q", refusal.Message)
	}
}

func TestOversizeOutputIsDiscardedRatherThanTruncated(t *testing.T) {
	runner := newRunner(t, agents.WithMaxOutput(64))
	inv := invocation(t, agents.ShapeText, map[string]string{
		helperModeVar:  "oversize",
		helperBytesVar: "4096",
	})

	got, err := runner.Run(t.Context(), agents.PurposeChecks, inv)
	var refusal *agents.InvocationError
	if !errors.As(err, &refusal) {
		t.Fatalf("refusal is not an *InvocationError: %v", err)
	}
	if refusal.Failure != agents.FailureOversize {
		t.Errorf("failure category is %q, want %q", refusal.Failure, agents.FailureOversize)
	}
	if got.Text != "" {
		t.Errorf("a fragment of the over-limit output was returned: %q", got.Text)
	}
}

// The run manages the flags that decide the output format, the model, and the
// session. The prompt is not one of them: it reaches the agent on standard
// input, so nothing about it is decided by an argument list, and a prompt
// beginning with a dash cannot be read as an option.
func TestTheRunManagesItsFlagsAndDeliversThePromptOnStandardInput(t *testing.T) {
	runner := newRunner(t)
	inv := invocation(t, agents.ShapeText, map[string]string{helperModeVar: "call"})
	inv.Prompt = "--not-a-flag but a prompt"
	inv.Model = "a-model"

	got, err := runner.Run(t.Context(), agents.PurposeReview, inv)
	if err != nil {
		t.Fatalf("invocation failed: %v", err)
	}
	call := decodeCall(t, got.Text)
	joined := strings.Join(call.Args, " ")
	for _, want := range []string{"--print", "--output-format json", "--model a-model"} {
		if !strings.Contains(joined, want) {
			t.Errorf("command line %q does not carry %q", joined, want)
		}
	}
	for _, arg := range call.Args {
		if strings.Contains(arg, inv.Prompt) {
			t.Errorf("the prompt is on the command line: %q", call.Args)
			break
		}
	}
	if call.Stdin != inv.Prompt {
		t.Errorf("the agent read %q on standard input, want the prompt %q", call.Stdin, inv.Prompt)
	}
	if call.resumedSession() != "" {
		t.Errorf("a session-free invocation asked to resume %q", call.resumedSession())
	}
}

// A prompt of the largest size Validate accepts reaches the agent whole. This
// is what delivering it on standard input buys: the same prompt in an argument
// list meets a ceiling the operating system sets, which on the platforms this
// module targets is below MaxPromptBytes, so the bound Validate applies would
// have been unreachable and the refusal it promises pre-empted.
func TestThePromptSizeThisPackageAcceptsIsThePromptSizeItCanDeliver(t *testing.T) {
	runner := newRunner(t)
	inv := invocation(t, agents.ShapeText, map[string]string{helperModeVar: "call"})
	inv.Prompt = strings.Repeat("p", agents.MaxPromptBytes)

	got, err := runner.Run(t.Context(), agents.PurposeReview, inv)
	if err != nil {
		t.Fatalf("a prompt of %d bytes, the largest Validate accepts, did not run: %v", len(inv.Prompt), err)
	}
	if call := decodeCall(t, got.Text); call.Stdin != inv.Prompt {
		t.Errorf("the agent read %d bytes on standard input, want the whole %d byte prompt",
			len(call.Stdin), len(inv.Prompt))
	}
}

// An agent that printed what it spent has that recorded whichever way it then
// signalled failure. A record that says an invocation cost nothing, when the
// agent said otherwise, understates spend on exactly the invocations worth
// understanding.
func TestANonZeroExitStillRecordsWhatTheAgentReported(t *testing.T) {
	rec := &recorder{}
	runner := newRunner(t, agents.WithRecorder(rec))
	inv := invocation(t, agents.ShapeText, map[string]string{
		helperModeVar:   "raw",
		helperStdoutVar: helperEnvelope("the work was done", "a-session"),
		helperExitVar:   "4",
		helperStderrVar: "the agent could not close cleanly",
	})

	_, err := runner.Run(t.Context(), agents.PurposeFix, inv)
	var refusal *agents.InvocationError
	if !errors.As(err, &refusal) {
		t.Fatalf("refusal is not an *InvocationError: %v", err)
	}
	// The envelope carried no failure of the agent's own, so the status is
	// what failed and stderr is what a person has to go on.
	if refusal.Failure != agents.FailureExit {
		t.Errorf("failure category is %q, want %q", refusal.Failure, agents.FailureExit)
	}
	if refusal.ExitCode != 4 {
		t.Errorf("exit code is %d, want 4", refusal.ExitCode)
	}
	if !strings.Contains(refusal.Message, "could not close cleanly") {
		t.Errorf("the agent's own message was not carried: %q", refusal.Message)
	}
	if len(rec.records) != 1 {
		t.Fatalf("recorded %d invocations, want 1", len(rec.records))
	}
	got := rec.records[0]
	if got.Usage != helperUsage() {
		t.Errorf("the failed invocation recorded %+v, want the usage the agent reported %+v",
			got.Usage, helperUsage())
	}
	if got.Model != "stand-in-model" {
		t.Errorf("model is %q, want the one the agent reported", got.Model)
	}
}

// An agent that says it failed is an agent failure whatever status came with
// it, and what it said is the message, because it is better evidence of what
// went wrong than the shell's status.
func TestAnAgentsOwnFailureOutranksTheExitStatusThatCameWithIt(t *testing.T) {
	rec := &recorder{}
	runner := newRunner(t, agents.WithRecorder(rec))
	inv := invocation(t, agents.ShapeText, map[string]string{
		helperModeVar:   "raw",
		helperStdoutVar: helperErrorEnvelope("the working copy is not clean"),
		helperExitVar:   "5",
		helperStderrVar: "a stack trace nobody needs",
	})

	_, err := runner.Run(t.Context(), agents.PurposeLint, inv)
	var refusal *agents.InvocationError
	if !errors.As(err, &refusal) {
		t.Fatalf("refusal is not an *InvocationError: %v", err)
	}
	if refusal.Failure != agents.FailureAgent {
		t.Errorf("failure category is %q, want %q", refusal.Failure, agents.FailureAgent)
	}
	if !strings.Contains(refusal.Message, "working copy is not clean") {
		t.Errorf("the message is %q, want what the agent said about its own failure", refusal.Message)
	}
	if refusal.ExitCode != 5 {
		t.Errorf("exit code is %d, want 5", refusal.ExitCode)
	}
	if len(rec.records) != 1 {
		t.Fatalf("recorded %d invocations, want 1", len(rec.records))
	}
	if got := rec.records[0]; got.Failure != agents.FailureAgent || got.Usage != helperUsage() {
		t.Errorf("recorded %+v, want an agent failure carrying the usage the agent reported", got)
	}
}

func TestEnvironmentIsPerInvocation(t *testing.T) {
	runner := newRunner(t, agents.WithBaseEnvironment([]string{"SHARED=from-base", "KEPT=base-only"}))
	read := func(t *testing.T, name, value, want string) {
		t.Helper()
		inv := invocation(t, agents.ShapeText, map[string]string{
			helperModeVar: "env",
			helperEchoVar: name,
		})
		if value != "" {
			inv.Env[name] = value
		}
		got, err := runner.Run(t.Context(), agents.PurposeReview, inv)
		if err != nil {
			t.Fatalf("invocation failed: %v", err)
		}
		if got.Text != want {
			t.Errorf("the agent read %s=%q, want %q", name, got.Text, want)
		}
	}

	read(t, "KEPT", "", "base-only")
	read(t, "SHARED", "from-invocation", "from-invocation")
	// The override was for one invocation only, so the base is back.
	read(t, "SHARED", "", "from-base")
}
