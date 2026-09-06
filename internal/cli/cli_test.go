package cli_test

import (
	"encoding/json"
	"os"
	"slices"
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

// The two global flags mean the same thing wherever they appear around a verb.
// README.md tells the reader to add --json to any command, and a flag that is
// only accepted before the verb makes that untrue for every form a person
// would naturally type.
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

// The global flags mean the same thing after an argument that is not a flag as
// they do anywhere else. This is the form a driving agent hits: reading one run
// by name and asking for a document, which flag.FlagSet.Parse stopping at the
// first non-flag argument would otherwise turn into a refusal in prose.
func TestTheGlobalFlagsAreAcceptedAfterAnArgumentThatIsNotAFlag(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)

	for _, args := range [][]string{
		{"runs", "abc123", "--json"},
		{"tasks", "abc123", "--json"},
		{"runs", "--limit", "5", "abc123", "--json"},
	} {
		got := run(t, h, subject, args...)
		if got.code == machine.ExitUsage {
			t.Fatalf("assistant %s was refused as incorrect usage:\n%s", strings.Join(args, " "), got.stderr)
		}
		var failure machine.Failure
		if err := json.Unmarshal([]byte(got.stdout), &failure); err != nil {
			t.Fatalf("assistant %s did not write a document: %v\n%s", strings.Join(args, " "), err, got.stdout)
		}
		if failure.Error == "" {
			t.Fatalf("assistant %s wrote a document with nothing in it: %s", strings.Join(args, " "), got.stdout)
		}
	}

	// The name is still the verb's argument rather than something the
	// reordering swallowed, so a second one is still refused.
	if got := run(t, h, subject, "runs", "abc123", "def456", "--json"); got.code != machine.ExitUsage {
		t.Fatalf("naming two runs exited %s, want incorrect usage", got.code)
	}
}

// The service verb's subcommand is a word wherever it stands, so a global flag
// written between the verb and it is read as a flag rather than mistaken for a
// subcommand nobody recognizes.
func TestTheServiceSubcommandIsFoundWhereverItStands(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)

	for _, args := range [][]string{
		{"service", "status", "--json"},
		{"service", "--json", "status"},
	} {
		got := run(t, h, subject, args...)
		if got.code != machine.ExitOK {
			t.Fatalf("assistant %s exited %s:\n%s%s", strings.Join(args, " "), got.code, got.stdout, got.stderr)
		}
		var state machine.Service
		if err := json.Unmarshal([]byte(got.stdout), &state); err != nil {
			t.Fatalf("assistant %s did not write a document: %v\n%s", strings.Join(args, " "), err, got.stdout)
		}
		if state.Socket == "" {
			t.Fatalf("assistant %s reported no socket: %s", strings.Join(args, " "), got.stdout)
		}
	}

	if got := run(t, h, subject, "service", "nonsense"); got.code != machine.ExitUsage {
		t.Fatalf("a subcommand the service does not have exited %s, want incorrect usage", got.code)
	}
	if got := run(t, h, subject, "service"); got.code != machine.ExitUsage {
		t.Fatalf("assistant service with no subcommand exited %s, want incorrect usage", got.code)
	}
}

// A command line that is wrong is answered in the shape the caller asked for,
// whichever side of the verb they asked on.
func TestAUsageFailureIsStructuredWhicheverSideOfTheVerbJSONWasAskedOn(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)

	for _, args := range [][]string{
		{"--json", "status", "--bogus"},
		{"status", "--json", "--bogus"},
		{"status", "--bogus", "--json"},
	} {
		got := run(t, h, subject, args...)
		if got.code != machine.ExitUsage {
			t.Fatalf("assistant %s exited %s, want incorrect usage", strings.Join(args, " "), got.code)
		}
		var failure machine.Failure
		if err := json.Unmarshal([]byte(got.stdout), &failure); err != nil {
			t.Fatalf("assistant %s answered in prose: %v\nout: %q\nerr: %q",
				strings.Join(args, " "), err, got.stdout, got.stderr)
		}
	}
}

// An intent given with nothing said about its standing is acceptance criteria,
// and a caller who says otherwise is taken at their word rather than having it
// silently overridden.
func TestTheIntentSuppliedFlagIsHonouredWhenTheCallerWritesIt(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)

	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}

	sources := map[string]string{}
	for _, c := range []struct {
		branch string
		args   []string
	}{
		{"criteria", []string{"--intent", "acceptance criteria stated up front"}},
		{"hint", []string{"--intent", "a hint about what this is for", "--intent-supplied=false"}},
	} {
		git(t, subject, "checkout", "--quiet", "-b", c.branch)
		started := run(t, h, subject, append([]string{"--json"}, c.args...)...)
		if started.code != machine.ExitOK {
			t.Fatalf("starting the run for %s exited %s:\n%s", c.branch, started.code, started.stdout)
		}
		record := decodeRun(t, started.stdout).Record
		if record.Intent == "" {
			t.Fatalf("the run for %s recorded no intent", c.branch)
		}
		sources[c.branch] = record.IntentSource
		if ended := run(t, h, subject, "--json", "--cancel"); ended.code != machine.ExitOK {
			t.Fatalf("ending the run for %s exited %s:\n%s", c.branch, ended.code, ended.stdout)
		}
	}

	if sources["hint"] == sources["criteria"] {
		t.Fatalf("--intent-supplied=false was discarded: both runs record %q", sources["hint"])
	}
	// And neither says nothing was given, because both were given something.
	git(t, subject, "checkout", "--quiet", "-b", "nothing")
	started := run(t, h, subject, "--json")
	if started.code != machine.ExitOK {
		t.Fatalf("starting a run with no intent exited %s:\n%s", started.code, started.stdout)
	}
	absent := decodeRun(t, started.stdout).Record.IntentSource
	for branch, source := range sources {
		if source == absent {
			t.Fatalf("the run for %s carries intent text and records %q, the same as a run given none", branch, source)
		}
	}
}

// takesAnArgument names the commands PRD section 9's table gives an argument
// to. Every other verb takes none, and a word it does not recognize is
// incorrect usage rather than something it acts past: a mistyped subcommand
// that starts the service and exits 0 is the one answer a driving agent
// cannot recover from.
var takesAnArgument = map[string]bool{"runs": true, "tasks": true}

func TestAVerbThatTakesNoArgumentRefusesOne(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)

	for _, name := range specified {
		if takesAnArgument[name] {
			continue
		}
		args := []string{name, "nonsense"}
		if name == "service" {
			// Its subcommand is the word it does recognize; the other one is
			// still an argument it does not take.
			args = []string{name, "nonsense", "status"}
		}
		if got := run(t, h, subject, args...); got.code != machine.ExitUsage {
			t.Errorf("assistant %s exited %s, want incorrect usage:\n%s%s",
				strings.Join(args, " "), got.code, got.stdout, got.stderr)
		}
	}

	// The bare command takes none either, and it is not in the table above
	// because it has no name to type.
	if got := run(t, h, subject, "nonsense-argument"); got.code != machine.ExitUsage {
		t.Errorf("the command with no verb exited %s for an argument it does not take", got.code)
	}

	// And the two that do take one still take it.
	for _, name := range []string{"runs", "tasks"} {
		if got := run(t, h, subject, name, "abc123"); got.code == machine.ExitUsage {
			t.Errorf("assistant %s abc123 is refused as incorrect usage:\n%s", name, got.stderr)
		}
	}
}

// The machine interface is one document per invocation on standard output.
// Version and help are answers rather than failures, so they write one too
// when --json was read before them: an agent that decodes standard output
// every time must not get a decode error from the two commands it tries first.
// The other order is the gap internal/cli/doc.go records, and nothing here
// pins it either way.
func TestVersionAndHelpAnswerAsDocumentsUnderJSON(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)

	got := run(t, h, subject, "--json", "--version")
	if got.code != machine.ExitOK {
		t.Fatalf("--json --version exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	var version machine.Version
	if err := json.Unmarshal([]byte(got.stdout), &version); err != nil {
		t.Fatalf("--json --version did not write a document: %v\n%s", err, got.stdout)
	}
	if version.Version == "" {
		t.Fatalf("--json --version wrote a document naming no build: %s", got.stdout)
	}

	for _, args := range [][]string{{"--json", "--help"}, {"--json", "-h"}, {"runs", "-h", "--json"}} {
		got := run(t, h, subject, args...)
		if got.code != machine.ExitOK {
			t.Fatalf("assistant %s exited %s:\n%s%s", strings.Join(args, " "), got.code, got.stdout, got.stderr)
		}
		var help machine.Help
		if err := json.Unmarshal([]byte(got.stdout), &help); err != nil {
			t.Fatalf("assistant %s did not write a document: %v\n%s", strings.Join(args, " "), err, got.stdout)
		}
		if help.Usage == "" {
			t.Fatalf("assistant %s wrote a document with no usage in it: %s", strings.Join(args, " "), got.stdout)
		}
	}

	// The rendering a person reads is unchanged: still the text, on standard
	// output, exiting successfully.
	plain := run(t, h, subject, "--help")
	if plain.code != machine.ExitOK || !strings.Contains(plain.stdout, "Commands:") {
		t.Fatalf("assistant --help exited %s:\n%s", plain.code, plain.stdout)
	}
}

// A refusal the service answered is proof the service is up. Reporting it as a
// service that is not running inverts the one fact the command was asked for,
// and it is the verb whose job is to say what is wrong.
func TestStatusReportsARefusalRatherThanAServiceThatIsNotRunning(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)

	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	started := run(t, h, subject, "--json", "--intent", "a change held at its first stage")
	if started.code != machine.ExitOK {
		t.Fatalf("starting a run exited %s:\n%s", started.code, started.stdout)
	}
	runID := decodeRun(t, started.stdout).Record.ID

	// Make the run's position unreadable, which is a refusal the service
	// raises while it is running and answering.
	records := openStore(t, h)
	latest, err := records.LatestGraphCheckpoint(t.Context(), runID)
	if err != nil {
		t.Fatalf("reading the run's tip: %v", err)
	}
	if _, err := records.AppendGraphCheckpoint(t.Context(), runID, runID, latest.Seq, []byte("not a checkpoint")); err != nil {
		t.Fatalf("appending an undecodable checkpoint: %v", err)
	}
	if err := records.Close(); err != nil {
		t.Fatalf("closing the store: %v", err)
	}

	// The service is up, and says so.
	if got := run(t, h, subject, "--json", "service", "status"); got.code != machine.ExitOK {
		t.Fatalf("service status exited %s:\n%s", got.code, got.stdout)
	}

	got := run(t, h, subject, "--json", "status")
	if got.code != machine.ExitFailure {
		t.Fatalf("status exited %s for a refusal the service answered, want failure:\n%s%s",
			got.code, got.stdout, got.stderr)
	}
	var failure machine.Failure
	if err := json.Unmarshal([]byte(got.stdout), &failure); err != nil {
		t.Fatalf("status did not write a document: %v\n%s", err, got.stdout)
	}
	if !strings.Contains(failure.Error, runID) {
		t.Fatalf("the refusal does not name the run whose position could not be read: %s", failure.Error)
	}
	if strings.Contains(failure.Error, "not running") {
		t.Fatalf("status reports a running service as not running: %s", failure.Error)
	}
}

// A flag that starts a run and a flag that acts on the run already in flight
// are two different things to ask for. Taking one and discarding the other
// with nothing said is what this refuses.
func TestAnsweringOrEndingARunRefusesTheFlagsThatStartOne(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)

	for _, args := range [][]string{
		{"--answer", "approved", "--intent", "a change with acceptance criteria"},
		{"--answer", "approved", "--intent-supplied=false"},
		{"--answer", "approved", "--skip", "test,lint"},
		{"--cancel", "--intent", "a change with acceptance criteria"},
		{"--cancel", "--skip", "test,lint"},
	} {
		got := run(t, h, subject, args...)
		if got.code != machine.ExitUsage {
			t.Errorf("assistant %s exited %s, want incorrect usage:\n%s%s",
				strings.Join(args, " "), got.code, got.stdout, got.stderr)
		}
	}

	// An answer the caller wrote and left empty is refused for being empty. It
	// is not a request to start a run: routing it there would create a run
	// nobody asked for and throw the flag away with nothing said.
	for _, args := range [][]string{{"--answer="}, {"--answer", ""}, {"--answer", "   "}} {
		got := run(t, h, subject, args...)
		if got.code != machine.ExitUsage {
			t.Errorf("assistant --answer with an empty answer exited %s, want incorrect usage:\n%s%s",
				got.code, got.stdout, got.stderr)
		}
		if !strings.Contains(got.stderr, "empty") {
			t.Errorf("the refusal does not say the answer was empty:\n%s", got.stderr)
		}
	}

	// Each of them on its own is still accepted, so what was refused is the
	// combination and not the flag.
	for _, args := range [][]string{
		{"--answer", "approved"},
		{"--cancel"},
		{"--intent", "a change with acceptance criteria"},
	} {
		if got := run(t, h, subject, args...); got.code == machine.ExitUsage {
			t.Errorf("assistant %s is refused as incorrect usage:\n%s", strings.Join(args, " "), got.stderr)
		}
	}
}

// The bare command is attach-or-start, so a second call carrying an intent is
// answered rather than refused - and it says the intent was not applied to the
// run it answered with, in both renderings. Explicit input is never taken and
// thrown away in silence.
func TestAttachingSaysWhichStartingInputsWereNotApplied(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)

	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	started := run(t, h, subject, "--json", "--intent", "the intent the run was built from")
	if started.code != machine.ExitOK {
		t.Fatalf("starting a run exited %s:\n%s", started.code, started.stdout)
	}
	first := decodeRun(t, started.stdout)
	if len(first.NotApplied) != 0 {
		t.Fatalf("the call that created the run reports %v as not applied", first.NotApplied)
	}

	// A second call on the same branch attaches. It succeeds, answers about
	// the same run, and names what it could not apply to it.
	again := run(t, h, subject, "--json", "--intent", "a different intent", "--skip", "test,lint")
	if again.code != machine.ExitOK {
		t.Fatalf("attaching with an intent exited %s:\n%s%s", again.code, again.stdout, again.stderr)
	}
	attached := decodeRun(t, again.stdout)
	if attached.Record.ID != first.Record.ID {
		t.Fatalf("the second call answered about run %s, want %s", attached.Record.ID, first.Record.ID)
	}
	if attached.Record.Intent != first.Record.Intent {
		t.Fatalf("the attach rewrote the run's intent to %q", attached.Record.Intent)
	}
	for _, want := range []string{"intent", "skip"} {
		if !slices.Contains(attached.NotApplied, want) {
			t.Fatalf("the attach does not report %q as not applied: %v", want, attached.NotApplied)
		}
	}

	// And the person reading the terminal is told the same thing.
	plain := run(t, h, subject, "--intent", "a third intent")
	if plain.code != machine.ExitOK {
		t.Fatalf("attaching exited %s:\n%s%s", plain.code, plain.stdout, plain.stderr)
	}
	if !strings.Contains(plain.stdout, "Not applied") || !strings.Contains(plain.stdout, "intent") {
		t.Fatalf("the rendering a person reads does not say the intent was not applied:\n%s", plain.stdout)
	}
}

// An init that cannot complete creates nothing. internal/gate's invariant is
// that a refusal leaves the working copy as it was, and the repository record
// a run needs is established from the working copy before anything is built,
// so a repository with no origin and no --upstream is refused whole rather
// than left with a gate and no record.
func TestAnInitThatCannotCompleteCreatesNothing(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubjectWithoutOrigin(t)

	refused := run(t, h, subject, "init")
	if refused.code != machine.ExitFailure {
		t.Fatalf("init with no origin and no --upstream exited %s, want failure:\n%s%s",
			refused.code, refused.stdout, refused.stderr)
	}

	// The working copy is as it was: no gate remote was added to it.
	if remotes := git(t, subject, "remote"); strings.Contains(remotes, "assistant") {
		t.Fatalf("the refused init left an assistant remote behind: %q", remotes)
	}
	// And the home holds no gate for it.
	records := openStore(t, h)
	defer func() { _ = records.Close() }()
	if binding, err := records.GateBinding(t.Context(), subject); err == nil {
		t.Fatalf("the refused init left the working copy bound to gate %s", binding.GateID)
	}

	// So the surface that decides whether a run can start says it cannot.
	report := decodeDoctor(t, run(t, h, subject, "--json", "doctor").stdout)
	if report.CanStartRun {
		t.Fatal("doctor says a run can start after an init that refused")
	}
}

// doctor asks the question it claims to answer. A run needs the gate binding
// and the repository record the service resolves, so a half-state with one and
// not the other is reported as a run that cannot start, however it was reached.
func TestDoctorSaysARunCannotStartWithNoRepositoryRecord(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)

	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	// Reach the half-state the other way: the gate stands, and the repository
	// record a run resolves is gone.
	records := openStore(t, h)
	binding, err := records.GateBinding(t.Context(), subject)
	if err != nil {
		t.Fatalf("reading the gate binding: %v", err)
	}
	if _, err := records.ForgetRepository(t.Context(), binding.GateID); err != nil {
		t.Fatalf("forgetting the repository record: %v", err)
	}
	if err := records.Close(); err != nil {
		t.Fatalf("closing the store: %v", err)
	}

	report := decodeDoctor(t, run(t, h, subject, "--json", "doctor").stdout)
	if report.CanStartRun {
		t.Fatal("doctor says a run can start with no repository record")
	}

	// And the refusal a start produces names what is actually missing rather
	// than the gate, which is standing.
	started := run(t, h, subject, "--intent", "a change")
	if started.code == machine.ExitOK {
		t.Fatalf("a run started with no repository record:\n%s", started.stdout)
	}
	said := started.stdout + started.stderr
	if !strings.Contains(said, "repository record") {
		t.Fatalf("the refusal does not name the missing repository record:\n%s", said)
	}
	if !strings.Contains(said, "assistant init") {
		t.Fatalf("the refusal does not name the command that repairs it:\n%s", said)
	}
}

// A run that has ended is not offered as answerable. The record decides before
// the checkpoint does, so a terminated run carries no decision on either
// surface and neither invites an answer the surface would refuse.
func TestARunThatHasEndedIsNotOfferedAsAnswerable(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)

	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	started := decodeRun(t, run(t, h, subject, "--json", "--intent", "a change").stdout)
	if started.Decision == nil {
		t.Fatal("the run did not stop at a decision, so there is nothing to end at a hold")
	}

	ended := run(t, h, subject, "--cancel")
	if ended.code != machine.ExitOK {
		t.Fatalf("ending the run exited %s:\n%s%s", ended.code, ended.stdout, ended.stderr)
	}
	if strings.Contains(ended.stdout, "Waiting on you") || strings.Contains(ended.stdout, "--answer") {
		t.Fatalf("ending the run offered an answer to a run that is over:\n%s", ended.stdout)
	}

	// And a later read of the same run says the same thing, on both surfaces.
	read := run(t, h, subject, "--json", "runs", started.Record.ID)
	over := decodeRun(t, read.stdout)
	if over.Record.Status != store.RunTerminated {
		t.Fatalf("the run is recorded as %s, want terminated", over.Record.Status)
	}
	if over.Decision != nil {
		t.Fatalf("the wire shape offers an answer to a run that has ended: %+v", over.Decision)
	}
	plain := run(t, h, subject, "runs", started.Record.ID)
	if strings.Contains(plain.stdout, "Waiting on you") || strings.Contains(plain.stdout, "--answer") {
		t.Fatalf("reading the ended run offered an answer to it:\n%s", plain.stdout)
	}

	// The stage reports are still there: what goes is the invitation, not the
	// record of what each stage found.
	if len(over.Stages) == 0 {
		t.Fatal("the ended run reports no stages, so dropping the decision lost the reports")
	}
}

// decodeDoctor reads a doctor report out of one structured answer.
func decodeDoctor(t *testing.T, document string) machine.Doctor {
	t.Helper()
	var report machine.Doctor
	if err := json.Unmarshal([]byte(document), &report); err != nil {
		t.Fatalf("a doctor report does not decode: %v\n%s", err, document)
	}
	return report
}
