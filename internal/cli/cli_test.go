package cli_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/store"
)

// specified is PRD section 9's command table, written out. Every one of these
// has to be a command this surface serves, and a name outside the list has to
// be refused as incorrect usage, which is the acceptance criterion that the
// surface is the specification's and nothing else.
//
// The bare command is the empty name and is covered by the tests below rather
// than here, because a command with no verb cannot be told from no arguments
// at all.
var specified = []string{"init", "status", "runs", "rerun", "sync", "tasks", "watch", "doctor", "service", "eject"}

// unspecified are commands this product could plausibly have and that PRD
// section 9 does not describe. A verb this surface needs and that section does
// not name is a finding to raise against the specification rather than a row
// to add quietly, so each of these has to be refused as incorrect usage.
var unspecified = []string{
	"start", "attach", "respond", "answer", "cancel", "stop", "restart",
	"machine", "agent", "push", "version", "help", "config", "logs", "stage",
}

func TestEveryCommandTheSpecificationNamesIsServed(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)
	for _, name := range specified {
		args := []string{name}
		if name == "service" {
			// The one command with subcommands. Its bare form names none, and
			// naming none is itself incorrect usage.
			args = append(args, "status")
		}
		if got := run(t, h, subject, args...); got.code == machine.ExitUsage {
			t.Fatalf("assistant %s is refused as incorrect usage:\n%s", name, got.stderr)
		}
	}
}

func TestACommandTheSpecificationDoesNotNameIsRefused(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)
	for _, name := range unspecified {
		if got := run(t, h, subject, name); got.code != machine.ExitUsage {
			t.Fatalf("assistant %s exited %s, want incorrect usage; it is not a command PRD section 9 describes", name, got.code)
		}
	}
}

// The three exit codes are never overloaded: incorrect usage is not an
// operational failure, and a run stopping for a decision is neither.
func TestTheThreeExitCodesMeanThreeDifferentThings(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)

	if got := run(t, h, subject, "nonsense"); got.code != machine.ExitUsage {
		t.Fatalf("an unknown command exited %s, want usage", got.code)
	}
	if got := run(t, h, subject, "runs"); got.code != machine.ExitFailure {
		t.Fatalf("a command that needs a service it cannot reach exited %s, want failure", got.code)
	}
	if got := run(t, h, subject, "--version"); got.code != machine.ExitOK {
		t.Fatalf("--version exited %s, want ok", got.code)
	}
}

// Structured output is one document on standard output, and progress is on
// standard error, so a caller redirecting standard output gets documents and
// nothing else.
func TestStructuredOutputIsOneDocumentAndProgressIsSeparate(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)

	got := run(t, h, subject, "--json", "runs")
	lines := strings.Split(strings.TrimRight(got.stdout, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("one invocation wrote %d documents:\n%s", len(lines), got.stdout)
	}
	var failure machine.Failure
	if err := json.Unmarshal([]byte(lines[0]), &failure); err != nil {
		t.Fatalf("the document does not decode: %v\n%s", err, lines[0])
	}
	if failure.Error == "" {
		t.Fatalf("a failure was written with nothing in it: %s", lines[0])
	}
	if failure.NextAction == "" {
		t.Fatal("a failure a caller can act on was written with no next action")
	}
}

// A command line that is wrong still answers in the shape the caller asked
// for. An agent that has to read prose to learn that its arguments were wrong
// is one that will read it wrong.
func TestAUsageFailureIsStructuredWhenStructuredOutputWasAskedFor(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)

	got := run(t, h, subject, "--json", "nonsense")
	if got.code != machine.ExitUsage {
		t.Fatalf("exited %s, want usage", got.code)
	}
	var failure machine.Failure
	if err := json.Unmarshal([]byte(got.stdout), &failure); err != nil {
		t.Fatalf("the answer does not decode: %v\n%s", err, got.stdout)
	}
}

// The whole point of the surface: a run started, reported on, answered, and
// carried on, each through a separate invocation of the command line.
func TestARunIsStartedReportedAndAnsweredThroughSeparateInvocations(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)

	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}

	started := run(t, h, subject, "--json", "--intent", "add a greeting, with the tradeoffs stated")
	if started.code != machine.ExitOK {
		t.Fatalf("starting a run exited %s:\n%s", started.code, started.stdout)
	}
	first := decodeRun(t, started.stdout)
	if first.Outcome != machine.OutcomeDecision {
		t.Fatalf("a started run reports %s, want a decision", first.Outcome)
	}
	if first.Decision == nil || first.Decision.Stage != pipeline.StageIntent.String() {
		t.Fatalf("the run is not holding at the first stage: %+v", first.Decision)
	}

	// A separate invocation reports the same decision without advancing it.
	reported := run(t, h, subject, "--json", "status")
	if reported.code != machine.ExitOK {
		t.Fatalf("status exited %s:\n%s", reported.code, reported.stdout)
	}
	var status machine.Status
	if err := json.Unmarshal([]byte(reported.stdout), &status); err != nil {
		t.Fatalf("status does not decode: %v\n%s", err, reported.stdout)
	}
	if status.ActiveRun == nil || status.ActiveRun.Record.ID != first.Record.ID {
		t.Fatalf("status reports %+v, want the run that was started", status.ActiveRun)
	}
	if status.ActiveRun.Decision == nil {
		t.Fatal("status reports a held run with no decision")
	}

	// A third invocation answers it, and the run moves on.
	answered := run(t, h, subject, "--json", "--answer", string(pipeline.OutcomeApproved))
	if answered.code != machine.ExitOK {
		t.Fatalf("answering exited %s:\n%s", answered.code, answered.stdout)
	}
	second := decodeRun(t, answered.stdout)
	if second.Record.ID != first.Record.ID {
		t.Fatalf("answering moved a different run: %s, want %s", second.Record.ID, first.Record.ID)
	}
	if second.Decision == nil || second.Decision.Stage != pipeline.StageRebase.String() {
		t.Fatalf("the run did not advance past the first stage: %+v", second.Decision)
	}

	// And the run is one run, not one per invocation.
	listed := run(t, h, subject, "--json", "runs")
	var runs machine.Runs
	if err := json.Unmarshal([]byte(listed.stdout), &runs); err != nil {
		t.Fatalf("runs does not decode: %v\n%s", err, listed.stdout)
	}
	if len(runs.Runs) != 1 {
		t.Fatalf("three invocations left %d runs, want 1", len(runs.Runs))
	}
}

// A person reading the terminal is shown the decision and the findings behind
// it in full, and told how to answer.
func TestTheHumanRenderingShowsTheDecisionAndHowToAnswerIt(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)

	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	got := run(t, h, subject, "--intent", "a change with its tradeoffs stated")
	if got.code != machine.ExitOK {
		t.Fatalf("starting a run exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	for _, want := range []string{
		"Waiting on you",
		"Options: approved, skipped, cancelled",
		"assistant --answer approved",
		"not implemented in this build",
	} {
		if !strings.Contains(got.stdout, want) {
			t.Fatalf("the rendering does not say %q:\n%s", want, got.stdout)
		}
	}
}

// status is one of the two commands whose job includes reporting that the
// service is down, so it answers when it is.
func TestStatusAnswersWhenTheServiceIsNotRunning(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)

	got := run(t, h, subject, "--json", "status")
	if got.code != machine.ExitOK {
		t.Fatalf("status exited %s with no service running:\n%s", got.code, got.stdout)
	}
	var status machine.Status
	if err := json.Unmarshal([]byte(got.stdout), &status); err != nil {
		t.Fatalf("status does not decode: %v\n%s", err, got.stdout)
	}
	if status.Service.Running {
		t.Fatal("status reports a service that is not running as running")
	}
	if status.Service.Detail == "" {
		t.Fatal("status reports the service as down and does not say why")
	}
}

// doctor decides rather than leaving a reader to infer, and it decides against
// starting a run when the service that would own it is not there.
func TestDoctorDecidesWhetherARunCanStart(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)

	got := run(t, h, subject, "--json", "doctor")
	if got.code != machine.ExitFailure {
		t.Fatalf("doctor exited %s with no service and no gate, want failure", got.code)
	}
	var report machine.Doctor
	if err := json.Unmarshal([]byte(got.stdout), &report); err != nil {
		t.Fatalf("doctor does not decode: %v\n%s", err, got.stdout)
	}
	if report.CanStartRun {
		t.Fatal("doctor says a run can start with no service and no gate")
	}
	if report.Detail == "" {
		t.Fatal("doctor says a run cannot start and does not say what stops one")
	}
	if !namesCheck(report, "stages") {
		t.Fatal("doctor does not report how much of the gate this build implements")
	}
}

// sync is present and refuses, because the module that owns reconciling a
// local branch does not exist. A verb that is absent leaves a caller guessing
// whether it was ever specified.
func TestSyncRefusesAndSaysWhatIsMissing(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)

	got := run(t, h, subject, "--json", "sync")
	if got.code != machine.ExitFailure {
		t.Fatalf("sync exited %s, want failure", got.code)
	}
	var plan machine.Plan
	if err := json.Unmarshal([]byte(got.stdout), &plan); err != nil {
		t.Fatalf("sync does not decode: %v\n%s", err, got.stdout)
	}
	if plan.Applied {
		t.Fatal("sync reports that it changed something")
	}
	if plan.Detail == "" {
		t.Fatal("sync changed nothing and does not say why")
	}
}

// A command that can destroy work does nothing on its first invocation and
// says what it would do.
func TestEjectPrintsWhatItWouldRemoveBeforeRemovingAnything(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)

	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	unconfirmed := run(t, h, subject, "eject")
	if unconfirmed.code != machine.ExitFailure {
		t.Fatalf("eject without confirmation exited %s, want failure", unconfirmed.code)
	}
	if !strings.Contains(unconfirmed.stderr, "--confirm") {
		t.Fatalf("eject does not say how to carry it out:\n%s", unconfirmed.stderr)
	}
	// It did nothing, so the gate is still there.
	if got := run(t, h, subject, "--json", "status"); !strings.Contains(got.stdout, `"present":true`) {
		t.Fatalf("the unconfirmed eject removed the gate:\n%s", got.stdout)
	}
	confirmed := run(t, h, subject, "eject", "--confirm")
	if confirmed.code != machine.ExitOK {
		t.Fatalf("eject --confirm exited %s:\n%s%s", confirmed.code, confirmed.stdout, confirmed.stderr)
	}
	if got := run(t, h, subject, "--json", "status"); strings.Contains(got.stdout, `"present":true`) {
		t.Fatalf("the gate survived a confirmed eject:\n%s", got.stdout)
	}
}

// decodeRun reads a run out of one structured answer.
func decodeRun(t *testing.T, document string) machine.Run {
	t.Helper()
	var run machine.Run
	if err := json.Unmarshal([]byte(document), &run); err != nil {
		t.Fatalf("a run does not decode: %v\n%s", err, document)
	}
	return run
}

// namesCheck reports whether a doctor report looked at something.
func namesCheck(report machine.Doctor, name string) bool {
	for _, check := range report.Checks {
		if check.Name == name {
			return true
		}
	}
	return false
}

// Every method the protocol serves has to be reachable from this surface, or
// it is a method nothing can call. The two exceptions are named: the event
// stream, which assistant watch opens and which cannot be driven from a
// one-shot invocation, and returning a stage's result, which PRD section 9
// names no command for and which no stage in this build would use.
func TestARunCanBeReadAndEndedThroughTheSurface(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)

	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	started := decodeRun(t, run(t, h, subject, "--json", "--intent", "a change").stdout)

	// One run in full, by name.
	named := run(t, h, subject, "--json", "runs", started.Record.ID)
	if named.code != machine.ExitOK {
		t.Fatalf("reading one run exited %s:\n%s", named.code, named.stdout)
	}
	if got := decodeRun(t, named.stdout); got.Record.ID != started.Record.ID {
		t.Fatalf("reading run %s reported %s", started.Record.ID, got.Record.ID)
	}

	// And ended, wherever it stands.
	ended := run(t, h, subject, "--json", "--cancel")
	if ended.code != machine.ExitOK {
		t.Fatalf("ending the run exited %s:\n%s", ended.code, ended.stdout)
	}
	if got := decodeRun(t, ended.stdout); got.Record.Status != store.RunTerminated {
		t.Fatalf("the ended run is recorded as %s, want terminated", got.Record.Status)
	}
	// With no run in flight, ending one is incorrect usage rather than a
	// failure: there is nothing to end.
	if got := run(t, h, subject, "--cancel"); got.code != machine.ExitUsage {
		t.Fatalf("ending a run that is not there exited %s, want usage", got.code)
	}
}

// The two global flags mean the same thing wherever they appear on the command
// line. README.md tells the reader to add --json to any command, and a flag
// that is only accepted before the verb makes that untrue for every form a
// person would naturally type.
func TestTheGlobalFlagsAreAcceptedAfterTheCommandAsWellAsBeforeIt(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)

	got := run(t, h, subject, "status", "--json")
	if got.code != machine.ExitOK {
		t.Fatalf("assistant status --json exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	var status machine.Status
	if err := json.Unmarshal([]byte(got.stdout), &status); err != nil {
		t.Fatalf("status --json did not write a document: %v\n%s", err, got.stdout)
	}

	// And after a flag of the verb's own, which is where a naive scan of the
	// command line would take the value for the flag.
	got = run(t, h, subject, "runs", "--limit", "5", "--json")
	var failure machine.Failure
	if err := json.Unmarshal([]byte(got.stdout), &failure); err != nil {
		t.Fatalf("runs --limit 5 --json did not write a document: %v\n%s", err, got.stdout)
	}

	// --home settles which home the command acts on wherever it appears, so
	// the answer has to be about the home it named.
	got = runArgs(t, subject, "status", "--home", h.Root(), "--json")
	if got.code != machine.ExitOK {
		t.Fatalf("assistant status --home ... --json exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	if err := json.Unmarshal([]byte(got.stdout), &status); err != nil {
		t.Fatalf("the answer does not decode: %v\n%s", err, got.stdout)
	}
	if status.Home != h.Root() {
		t.Fatalf("the command acted on %s, want the home named after the verb %s", status.Home, h.Root())
	}
}

// Asking what the commands are is not incorrect usage. PRD section 9 gives
// machine.ExitUsage that meaning, and a driving agent that read exit 2 as "my
// arguments were wrong" would be told the wrong thing.
func TestHelpIsAnsweredRatherThanRefused(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)

	for _, c := range []struct {
		args []string
		says string
	}{
		{[]string{"-h"}, "Commands:"},
		{[]string{"--help"}, "Commands:"},
		{[]string{"runs", "-h"}, "-limit"},
		{[]string{"runs", "--help"}, "-limit"},
	} {
		got := run(t, h, subject, c.args...)
		if got.code != machine.ExitOK {
			t.Fatalf("assistant %s exited %s, want ok", strings.Join(c.args, " "), got.code)
		}
		if !strings.Contains(got.stdout, c.says) {
			t.Fatalf("assistant %s does not answer with %q on standard output:\n%s",
				strings.Join(c.args, " "), c.says, got.stdout)
		}
		if got.stderr != "" {
			t.Fatalf("assistant %s wrote a failure to standard error:\n%s",
				strings.Join(c.args, " "), got.stderr)
		}
	}
}

// Nothing is destroyed unless the whole removal can complete. The refusal that
// stops a repository being forgotten while a run has not finished is asked for
// before the gate is touched, so an eject that is refused leaves the gate and
// the assistant remote exactly as they were.
func TestEjectRefusedByAnActiveRunRemovesNothing(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)

	created := run(t, h, subject, "--json", "init")
	if created.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", created.code, created.stdout, created.stderr)
	}
	var built machine.Init
	if err := json.Unmarshal([]byte(created.stdout), &built); err != nil {
		t.Fatalf("init does not decode: %v\n%s", err, created.stdout)
	}

	started := run(t, h, subject, "--json", "--intent", "a change held at its first stage")
	if started.code != machine.ExitOK {
		t.Fatalf("starting a run exited %s:\n%s", started.code, started.stdout)
	}
	held := decodeRun(t, started.stdout)
	if held.Outcome != machine.OutcomeDecision {
		t.Fatalf("the run reports %s, want a run waiting on a decision", held.Outcome)
	}

	ejected := run(t, h, subject, "eject", "--confirm")
	if ejected.code != machine.ExitFailure {
		t.Fatalf("eject --confirm exited %s while a run was in flight, want failure:\n%s%s",
			ejected.code, ejected.stdout, ejected.stderr)
	}

	// The gate it refused to remove is still on disk.
	if _, err := os.Stat(built.Gate.Repository); err != nil {
		t.Fatalf("the refused eject removed the gate at %s: %v", built.Gate.Repository, err)
	}
	// So is the remote a push is validated through.
	if remotes := git(t, subject, "remote"); !strings.Contains(remotes, gate.RemoteName) {
		t.Fatalf("the refused eject removed the %s remote; the working copy has %q", gate.RemoteName, remotes)
	}
	// And the records are whole, so the working copy is still bound.
	if got := run(t, h, subject, "--json", "status"); !strings.Contains(got.stdout, `"present":true`) {
		t.Fatalf("the refused eject unbound the working copy:\n%s", got.stdout)
	}

	// Once the run is over the same command carries the removal out, so what
	// was refused was the removal and not the command.
	if got := run(t, h, subject, "--json", "--cancel"); got.code != machine.ExitOK {
		t.Fatalf("ending the run exited %s:\n%s", got.code, got.stdout)
	}
	if got := run(t, h, subject, "eject", "--confirm"); got.code != machine.ExitOK {
		t.Fatalf("eject --confirm exited %s once the run was over:\n%s%s", got.code, got.stdout, got.stderr)
	}
}
