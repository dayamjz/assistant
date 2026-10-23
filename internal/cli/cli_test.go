package cli_test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/cli"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/custody"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/procgroup"
	"github.com/dayamjz/assistant/internal/stages"
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
//
// "gate", "host", "coordinator" and "worker" are deliberately not among
// them. None is a candidate for that table: "gate" is the interface
// internal/gate requires of this surface, invoked by the hooks it installs,
// "host" is the interface the coordinator's host requires, invoked by the
// hook it fires at turn end, "coordinator" is the client for the
// coordinator's service methods, which the owner decided on #227 is
// dispatched beside the table, and "worker" is a task's worker's route back
// to the program, which #226 asked for on the same terms. PRD section 9
// names all four outside its table, the fourth by the owner's decision, so
// none of them is a row here.
// TestTheGateVerbServesTheHooksAndNothingElse, host_test.go's
// TestTheHostVerbServesTheHookAndNothingElse, coordinator_test.go's
// TestTheCoordinatorVerbServesItsTableAndNothingElse and worker_test.go's
// TestTheWorkerVerbServesItsTableAndNothingElse hold what each may be.
// The coordinator's subcommands, and the worker's acknowledge, are among
// the names refused here, so none of them reaches the surface as a verb of
// its own.
var unspecified = []string{
	"start", "attach", "respond", "answer", "cancel", "stop", "restart",
	"machine", "agent", "push", "version", "help", "config", "logs", "stage",
	"task", "fleet", "create", "message", "relaunch", "promote", "wakes", "await", "listen", "acknowledge", "supervision",
	"peek", "report", "event",
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

// TestTheGateVerbServesTheHooksAndNothingElse is the boundary around the one
// command this surface answers to that PRD section 9 does not name.
//
// Both halves matter. The two subcommands internal/gate's hooks invoke reach a
// verb rather than being refused, which is what a gate's installed hooks need
// of this binary and what nineteen merged changes went without. And nothing
// else under that verb does, so "gate" is not a door the specification's
// surface can grow through.
//
// What it does not establish is that these are the subcommands internal/gate
// actually writes; a list here would be a second statement of that. The push
// tests establish it structurally, by pushing through hooks that package wrote
// and requiring a run to start.
func TestTheGateVerbServesTheHooksAndNothingElse(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)

	for _, hook := range []string{"admit", "notify"} {
		// With --gate, so what is checked is the subcommand reaching a verb
		// rather than the refusal that a missing identifier produces.
		got := run(t, h, subject, "gate", hook, "--gate", "0123456789abcdef")
		if got.code == machine.ExitUsage {
			t.Fatalf("assistant gate %s is refused as incorrect usage, and internal/gate's hooks "+
				"invoke it:\n%s", hook, got.stderr)
		}
	}
	for _, args := range [][]string{
		{"gate"},
		{"gate", "start"},
		{"gate", "admit", "--gate", "0123456789abcdef", "notify"},
		// The subcommand comes first after the verb. A scan that took it from
		// wherever it stood would read these two as naming one.
		{"gate", "--gate", "0123456789abcdef", "admit"},
		{"gate", "--gate", "notify"},
	} {
		if got := run(t, h, subject, args...); got.code != machine.ExitUsage {
			t.Fatalf("assistant %s exited %s, want incorrect usage", strings.Join(args, " "), got.code)
		}
	}
	// The identifier is required rather than defaulted: a hook always writes
	// one, and a default would mean acting on a gate nobody named.
	for _, hook := range []string{"admit", "notify"} {
		if got := run(t, h, subject, "gate", hook); got.code != machine.ExitUsage {
			t.Fatalf("assistant gate %s with no --gate exited %s, want incorrect usage", hook, got.code)
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
	t.Parallel()
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)

	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	standingOnChange(t, subject, "a-change")

	started := run(t, h, subject, "--json",
		"--intent", "add a greeting, with the tradeoffs stated")
	if started.code != machine.ExitOK {
		t.Fatalf("starting a run exited %s:\n%s", started.code, started.stdout)
	}
	first := decodeRun(t, started.stdout)
	if first.Outcome != machine.OutcomeDecision {
		t.Fatalf("a started run reports %s, want a decision", first.Outcome)
	}
	holds, next := firstHoldingStages(t)
	if first.Decision == nil || first.Decision.Stage != holds.String() {
		t.Fatalf("the run is not holding at %s, the first stage that holds it: %+v", holds, first.Decision)
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
	if second.Decision == nil || second.Decision.Stage != next.String() {
		t.Fatalf("the run did not advance from %s to %s: %+v", holds, next, second.Decision)
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
	t.Parallel()
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)

	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	standingOnChange(t, subject, "a-change")
	got := run(t, h, subject, "--intent", "a change with its tradeoffs stated")
	if got.code != machine.ExitOK {
		t.Fatalf("starting a run exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	for _, want := range []string{
		"Waiting on you",
		"assistant --answer followed by one of: approved, skipped, cancelled",
		// The finding behind the hold, in full: the run stops at the test
		// stage, which holds wherever the configuration names no test
		// command, and these homes configure none.
		"No test command is configured",
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
	if !namesCheck(report, "code host") {
		t.Fatal("doctor does not report whether the code-host provider command line is runnable")
	}
}

// doctor reports the agent the operator's file names, not the agent auto
// would choose when that file names another. Claude and Codex are both on
// PATH, and a repository document naming Claude is present in the working
// copy; the home names Codex. The agent row is Codex.
func TestDoctorReportsTheConfiguredAgentRatherThanAuto(t *testing.T) {
	principles.Cite(t, principles.P7)
	h := newHome(t)
	subject := newSubject(t)
	if err := os.WriteFile(h.ConfigFile(), []byte("agent: codex\n"), 0o600); err != nil {
		t.Fatalf("writing the home configuration: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subject, "assistant.json"), []byte("{\"agent\":\"claude\"}\n"), 0o644); err != nil {
		t.Fatalf("writing the repository document: %v", err)
	}
	bin := agentPATH(t, "claude", "codex")
	t.Setenv("PATH", bin)

	report := decodeDoctor(t, run(t, h, subject, "--json", "doctor").stdout)
	row, found := findCheck(report, machine.CheckAgent)
	if !found {
		t.Fatal("doctor made no agent check")
	}
	if !row.OK || row.Detail != "codex" {
		t.Fatalf("doctor agent row = ok:%v %q, want ok and codex", row.OK, row.Detail)
	}
}

// A configured Codex stays available when Claude is not on PATH. auto would
// fail closed there, and that failure must not block a run whose operator
// file names Codex. The reverse, Claude configured and Codex absent, reports
// Claude.
func TestDoctorFollowsTheConfiguredAgentWhenTheOtherIsAbsent(t *testing.T) {
	h := newHome(t)
	subject := newSubject(t)
	if err := os.WriteFile(h.ConfigFile(), []byte("agent: codex\n"), 0o600); err != nil {
		t.Fatalf("writing the home configuration: %v", err)
	}
	t.Setenv("PATH", agentPATH(t, "codex"))

	report := decodeDoctor(t, run(t, h, subject, "--json", "doctor").stdout)
	row, found := findCheck(report, machine.CheckAgent)
	if !found || !row.OK || row.Detail != "codex" {
		t.Fatalf("doctor agent row = found:%v ok:%v %q, want codex", found, row.OK, row.Detail)
	}
	if blocksOn(report, machine.CheckAgent) {
		t.Fatalf("doctor blocks on the agent check for a configured Codex: %s", report.Detail)
	}

	if err := os.WriteFile(h.ConfigFile(), []byte("agent: claude\n"), 0o600); err != nil {
		t.Fatalf("writing the home configuration: %v", err)
	}
	t.Setenv("PATH", agentPATH(t, "claude"))
	report = decodeDoctor(t, run(t, h, subject, "--json", "doctor").stdout)
	row, found = findCheck(report, machine.CheckAgent)
	if !found || !row.OK || row.Detail != "claude" {
		t.Fatalf("doctor agent row = found:%v ok:%v %q, want claude", found, row.OK, row.Detail)
	}
}

// A home configuration that cannot be read is a refusal. Doctor does not
// substitute auto and then report whichever agent that resolves.
func TestDoctorRefusesAnUnreadableAgentConfiguration(t *testing.T) {
	h := newHome(t)
	subject := newSubject(t)
	if err := os.WriteFile(h.ConfigFile(), []byte("agent: codex\n"), 0o600); err != nil {
		t.Fatalf("writing the home configuration: %v", err)
	}
	if err := os.Chmod(h.ConfigFile(), 0); err != nil {
		t.Fatalf("removing permission to read the home configuration: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(h.ConfigFile(), 0o600) })
	if _, err := os.ReadFile(h.ConfigFile()); err == nil {
		t.Skip("this process can still read a mode 0 file, so the refusal cannot be shown")
	}
	t.Setenv("PATH", agentPATH(t, "claude", "codex"))

	report := decodeDoctor(t, run(t, h, subject, "--json", "doctor").stdout)
	row, found := findCheck(report, machine.CheckAgent)
	if !found || row.OK || !row.Blocking {
		t.Fatalf("doctor agent row = found:%v %+v, want a blocking refusal", found, row)
	}
	if row.Detail == "claude" || row.Detail == "codex" || row.Detail == "auto" {
		t.Fatalf("doctor named an agent for a configuration it could not read: %q", row.Detail)
	}
	if !blocksOn(report, machine.CheckAgent) {
		t.Fatalf("doctor does not count the unreadable configuration as stopping a run: %s", report.Detail)
	}
}

// doctor reports whether the code-host provider command line resolves, and
// the row does not decide: a run can start without a code host and the stages
// that talk to one refuse there, so the row informs before a stage does. The
// test builds a PATH holding git and no provider, so what the row reports is
// established rather than being whatever this machine has installed; it stays
// serial because PATH is process state that every command run here reads.
func TestDoctorReportsTheCodeHostAndDoesNotBlockOnIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("building a PATH that holds git and nothing else needs a symlink this test cannot portably make on windows")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("resolving git: %v", err)
	}
	bin := t.TempDir()
	if err := os.Symlink(realGit, filepath.Join(bin, "git")); err != nil {
		t.Fatalf("planting git on the probe PATH: %v", err)
	}
	t.Setenv("PATH", bin)
	h := newHome(t)
	subject := newSubject(t)

	report := decodeDoctor(t, run(t, h, subject, "--json", "doctor").stdout)
	row, found := findCheck(report, "code host")
	if !found {
		t.Fatal("doctor does not report the code host")
	}
	if row.OK {
		t.Fatalf("doctor reports a provider command line that is not on PATH as runnable: %s", row.Detail)
	}
	if row.Blocking {
		t.Fatal("the code host row blocks, and a run can start without a code host")
	}
	if row.Detail == "" {
		t.Fatal("the code host row is not OK and does not say what was found")
	}
	// The decision does not count it: what stops a run from starting names the
	// blocking rows that failed and the code host is not among them.
	if report.CanStartRun {
		t.Fatal("doctor says a run can start with no gate and no service, so the decision below is not being made")
	}
	if strings.Contains(report.Detail, "code host") {
		t.Fatalf("the code host is named among what stops a run from starting: %q", report.Detail)
	}

	// With a provider command line standing where PATH points, the same row
	// reports it, path and all. The file only has to resolve: the probe's
	// contract is that the command resolves to something this process may
	// execute, not that it answers as a provider.
	planted := filepath.Join(bin, "gh")
	if err := procgroup.WriteProgram(planted, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("planting a provider command line: %v", err)
	}
	report = decodeDoctor(t, run(t, h, subject, "--json", "doctor").stdout)
	row, found = findCheck(report, "code host")
	if !found {
		t.Fatal("doctor does not report the code host")
	}
	if !row.OK {
		t.Fatalf("doctor reports a resolvable provider command line as not runnable: %s", row.Detail)
	}
	if row.Blocking {
		t.Fatal("the code host row blocks")
	}
	if row.Detail != planted {
		t.Fatalf("the code host row reports %q, want the resolved command line %q", row.Detail, planted)
	}
}

// sync acts on a person's branch through the service, which holds the home,
// so with no service it fails saying so and nothing is changed. With one, a
// branch no run has checked is classified as a refusal the person can read:
// the structured answer carries custody's reason and its sentence, the human
// one the phrase-table row and nothing else, and the exit code says the
// person was stopped.
func TestSyncAsksTheServiceAndReportsWhatCustodyFound(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)

	down := run(t, h, subject, "--json", "sync")
	var failure machine.Failure
	if down.code != machine.ExitFailure || json.Unmarshal([]byte(down.stdout), &failure) != nil ||
		!strings.Contains(failure.Error, "the background service is not running") {
		t.Fatalf("sync with no service exited %s with %q, want a failure saying the service is not running", down.code, down.stdout)
	}

	serve(t, h)
	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	got := run(t, h, subject, "--json", "sync")
	if got.code != machine.ExitFailure {
		t.Fatalf("sync of a branch no run checked exited %s, want failure", got.code)
	}
	var plan machine.Plan
	if err := json.Unmarshal([]byte(got.stdout), &plan); err != nil {
		t.Fatalf("sync does not decode: %v\n%s", err, got.stdout)
	}
	if plan.Applied || plan.Refusal != custody.ReasonNoRun || plan.Detail == "" || len(plan.Steps) != 0 {
		t.Fatalf("sync of a branch no run checked answered %+v", plan)
	}
	human := run(t, h, subject, "sync")
	if want := machine.PlanPhrase(plan).String(); strings.TrimSpace(human.stdout) != want {
		t.Fatalf("sync printed %q, want the row %q", human.stdout, want)
	}
	confirmed := run(t, h, subject, "--json", "sync", "--confirm")
	if err := json.Unmarshal([]byte(confirmed.stdout), &plan); err != nil || plan.Applied || plan.Refusal != custody.ReasonNoRun {
		t.Fatalf("a confirmed sync of a branch no run checked answered %+v (%v)", plan, err)
	}
}

// A command that can destroy work does nothing on its first invocation and
// says what it would do.
func TestEjectPrintsWhatItWouldRemoveBeforeRemovingAnything(t *testing.T) {
	t.Parallel()
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
	_, found := findCheck(report, name)
	return found
}

// findCheck returns what a doctor report says about one named check.
func findCheck(report machine.Doctor, name string) (machine.Check, bool) {
	for _, check := range report.Checks {
		if check.Name == name {
			return check, true
		}
	}
	return machine.Check{}, false
}

// blocksOn reports whether doctor's verdict names one blocking check.
func blocksOn(report machine.Doctor, name string) bool {
	const prefix = "a run cannot start: "
	rest, ok := strings.CutPrefix(report.Detail, prefix)
	if !ok {
		return false
	}
	return slices.Contains(strings.Split(rest, ", "), name)
}

// agentPATH is a PATH whose only agent binaries are the names given, each a
// script LookPath can execute, plus git so the working-copy check can run.
// The caller sets PATH to the returned directory.
func agentPATH(t *testing.T, names ...string) string {
	t.Helper()
	bin := t.TempDir()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("resolving git: %v", err)
	}
	if err := os.Symlink(realGit, filepath.Join(bin, "git")); err != nil {
		t.Fatalf("planting git: %v", err)
	}
	script := []byte("#!/bin/sh\nexit 0\n")
	for _, name := range names {
		if err := procgroup.WriteProgram(filepath.Join(bin, name), script, 0o755); err != nil {
			t.Fatalf("planting %s: %v", name, err)
		}
	}
	return bin
}

// Every method the protocol serves has to be reachable from this surface, or
// it is a method nothing can call. The two exceptions are named: the event
// stream, which assistant watch opens and which cannot be driven from a
// one-shot invocation, and returning a stage's result, which PRD section 9
// names no command for and which no stage in this build would use.
func TestARunCanBeReadAndEndedThroughTheSurface(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)

	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	standingOnChange(t, subject, "a-change")
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
	t.Parallel()
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
	standingOnChange(t, subject, "a-change")

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
	// was refused was the removal and not the command. The change is on the
	// upstream first, so the cancel's cleanup removes the run's copy on the
	// remote-branch proof rather than keeping it for eject to refuse over.
	git(t, subject, "push", "--quiet", "origin", "a-change")
	if got := run(t, h, subject, "--json", "--cancel"); got.code != machine.ExitOK {
		t.Fatalf("ending the run exited %s:\n%s", got.code, got.stdout)
	}
	if got := run(t, h, subject, "eject", "--confirm"); got.code != machine.ExitOK {
		t.Fatalf("eject --confirm exited %s once the run was over:\n%s%s", got.code, got.stdout, got.stderr)
	}
}

// Eject either removes everything the gate recorded or refuses before it
// touches anything, and never fails partway (#177). A finished run that has a
// cleanup record is forgotten with it, whatever else the service recorded about
// the run meanwhile; a finished run with a process record, an agent group whose
// end nothing recorded, is refused before the gate is touched, and the same
// command completes once that record is cleared.
func TestEjectRemovesEveryRecordOfAFinishedRunOrRefusesFirst(t *testing.T) {
	t.Parallel()
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
	standingOnChange(t, subject, "a-change")
	started := run(t, h, subject, "--json", "--intent", "a change held at its first stage")
	if started.code != machine.ExitOK {
		t.Fatalf("starting a run exited %s:\n%s", started.code, started.stdout)
	}
	id := decodeRun(t, started.stdout).Record.ID
	// The change is on the upstream, so the cancel's cleanup removes the
	// run's copy on the remote-branch proof rather than keeping it.
	git(t, subject, "push", "--quiet", "origin", "a-change")
	if got := run(t, h, subject, "--json", "--cancel"); got.code != machine.ExitOK {
		t.Fatalf("ending the run exited %s:\n%s", got.code, got.stdout)
	}

	records := openStore(t, h)
	defer func() { _ = records.Close() }()
	ctx := t.Context()
	// The service may already have recorded its own decision about the copy;
	// either way the run ends up with a cleanup record, which is the row a
	// forget used to fail on.
	if _, err := records.RecordRunCleanupRefusal(ctx, id, "unproven", "kept for the test", nil); err != nil &&
		!errors.Is(err, store.ErrCleanupRecorded) {
		t.Fatalf("recording a cleanup refusal: %v", err)
	}
	if _, err := records.RunCleanup(ctx, id); err != nil {
		t.Fatalf("the run has no cleanup record: %v", err)
	}
	if _, err := records.RecordProcess(ctx, store.ProcessRecord{RunID: id, Stage: "review", Group: 424242}); err != nil {
		t.Fatalf("RecordProcess: %v", err)
	}

	refused := run(t, h, subject, "eject", "--confirm")
	if refused.code != machine.ExitFailure {
		t.Fatalf("eject --confirm exited %s over a recorded agent process, want failure:\n%s%s",
			refused.code, refused.stdout, refused.stderr)
	}
	if _, err := os.Stat(built.Gate.Repository); err != nil {
		t.Fatalf("the refused eject removed the gate at %s: %v", built.Gate.Repository, err)
	}
	if _, err := records.Run(ctx, id); err != nil {
		t.Fatalf("the refused eject removed the run record: %v", err)
	}

	if err := records.ClearProcess(ctx, id, 424242); err != nil {
		t.Fatalf("ClearProcess: %v", err)
	}
	if got := run(t, h, subject, "eject", "--confirm"); got.code != machine.ExitOK {
		t.Fatalf("eject --confirm exited %s once the process record was cleared:\n%s%s", got.code, got.stdout, got.stderr)
	}
	if _, err := os.Stat(built.Gate.Repository); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the gate at %s survived the eject: %v", built.Gate.Repository, err)
	}
	if _, err := records.Run(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the run record survived the eject: %v", err)
	}
	if _, err := records.RunCleanup(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the run's cleanup record survived the eject: %v", err)
	}
}

// PRD principle P15's "the program never discards it on its own", at eject,
// through the real cleanup. A cancelled run's copy holds a commit on no
// remote, so the cancel's cleanup keeps it and records a kept work entry.
// Eject is then refused before the gate is touched, naming the entry's copy,
// its run, the commit and the way past. The supervisor loop raises the
// entry, which no task names, as a kept-work wake, and the coordinator
// drains it without acknowledging it. Once the commit is on the upstream
// branch, the next service start removes the copy on the remote-branch
// proof and closes the entry, and eject is still refused, naming the wake,
// because PRD section 8 keeps a wake actionable until it is acknowledged and
// forgetting the repository would delete it. Once the coordinator
// acknowledges it, the same command completes, taking the closed entry and
// its wake with the run.
func TestEjectIsRefusedWhileTheRepositoryHoldsOpenKeptWork(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	h := newHome(t)
	subject := newSubject(t)
	if err := os.WriteFile(h.ConfigFile(), []byte(`{"supervision_beacon_interval": "1s"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stop := serveUntilStopped(t, h)

	created := run(t, h, subject, "--json", "init")
	if created.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", created.code, created.stdout, created.stderr)
	}
	var built machine.Init
	if err := json.Unmarshal([]byte(created.stdout), &built); err != nil {
		t.Fatalf("init does not decode: %v\n%s", err, created.stdout)
	}
	standingOnChange(t, subject, "a-change")
	started := run(t, h, subject, "--json", "--intent", "a change held at its first stage")
	if started.code != machine.ExitOK {
		t.Fatalf("starting a run exited %s:\n%s", started.code, started.stdout)
	}
	record := decodeRun(t, started.stdout).Record
	if got := run(t, h, subject, "--json", "--cancel"); got.code != machine.ExitOK {
		t.Fatalf("ending the run exited %s:\n%s", got.code, got.stdout)
	}
	var status machine.Status
	if got := run(t, h, subject, "--json", "status"); json.Unmarshal([]byte(got.stdout), &status) != nil || len(status.KeptWork) != 1 {
		t.Fatalf("status lists kept work %+v, want the cancelled run's one entry:\n%s", status.KeptWork, got.stdout)
	}
	entry := status.KeptWork[0]
	var drained machine.Wakes
	if got := run(t, h, subject, "--json", "coordinator", "await", "--timeout", "30s"); got.code != machine.ExitOK ||
		json.Unmarshal([]byte(got.stdout), &drained) != nil || len(drained.KeptWorkWakes) != 1 || drained.KeptWorkWakes[0].KeptWork != entry.ID {
		t.Fatalf("the wait exited %s handing out kept-work wakes %+v, want entry %d's one:\n%s%s",
			got.code, drained.KeptWorkWakes, entry.ID, got.stdout, got.stderr)
	}
	wake := drained.KeptWorkWakes[0].ID

	refused := run(t, h, subject, "eject", "--confirm")
	if refused.code != machine.ExitFailure {
		t.Fatalf("eject --confirm exited %s over open kept work, want failure:\n%s%s", refused.code, refused.stdout, refused.stderr)
	}
	said := refused.stdout + refused.stderr
	for _, want := range []string{entry.CopyPath, "run " + record.ID, record.SubmittedHead, "#411 part 2", "landing"} {
		if !strings.Contains(said, want) {
			t.Fatalf("the refusal does not name %q:\n%s", want, said)
		}
	}
	if _, err := os.Stat(built.Gate.Repository); err != nil {
		t.Fatalf("the refused eject removed the gate at %s: %v", built.Gate.Repository, err)
	}

	git(t, subject, "push", "--quiet", "origin", "a-change")
	stop()
	serve(t, h)
	deadline := time.Now().Add(30 * time.Second)
	for {
		var now machine.Status
		got := run(t, h, subject, "--json", "status")
		if err := json.Unmarshal([]byte(got.stdout), &now); err == nil && len(now.KeptWork) == 0 {
			if _, err := os.Stat(entry.CopyPath); errors.Is(err, os.ErrNotExist) {
				break
			}
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the next start did not close the entry over a copy whose work landed:\n%s", got.stdout)
		}
		time.Sleep(50 * time.Millisecond)
	}
	queued := run(t, h, subject, "eject", "--confirm")
	if queued.code != machine.ExitFailure || !strings.Contains(queued.stderr, "kept-work wake "+strconv.FormatInt(wake, 10)+",") {
		t.Fatalf("eject --confirm exited %s with kept-work wake %d queued, want it refused naming the wake:\n%s%s",
			queued.code, wake, queued.stdout, queued.stderr)
	}
	if _, err := os.Stat(built.Gate.Repository); err != nil {
		t.Fatalf("the eject refused over a queued wake removed the gate at %s: %v", built.Gate.Repository, err)
	}
	if got := run(t, h, subject, "coordinator", "acknowledge", machine.KeptWorkWakePrefix+strconv.FormatInt(wake, 10)); got.code != machine.ExitOK {
		t.Fatalf("acknowledging kept-work wake %d exited %s:\n%s%s", wake, got.code, got.stdout, got.stderr)
	}
	if got := run(t, h, subject, "eject", "--confirm"); got.code != machine.ExitOK {
		t.Fatalf("eject --confirm exited %s once the entry closed and its wake was acknowledged:\n%s%s", got.code, got.stdout, got.stderr)
	}
	records := openStore(t, h)
	defer func() { _ = records.Close() }()
	if _, err := records.KeptWorkEntry(t.Context(), entry.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the closed entry survived the eject: %v", err)
	}
}

// A finished run whose cleanup has not yet decided its copy keeps eject from
// removing the gate. The run is recorded failed, as a service that stopped
// before its cleanup leaves it, and its copy, a linked worktree of the gate
// at the run's commit, stands with no entry. The preflight's reservation
// succeeds and the eject then refuses naming the copy, leaving the gate, the
// copy's commit readable, and no reservation. Once the copy is gone, the same
// command completes, and inside its window, the seam between the reserved
// preflight and the gate's removal, a new run of the repository is refused,
// so no copy can appear under the gate. It changes a package variable, so it
// does not run in parallel.
func TestEjectIsRefusedWhileAFinishedRunsCopyStands(t *testing.T) {
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
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
	standingOnChange(t, subject, "a-change")
	head := git(t, subject, "rev-parse", "HEAD")

	records := openStore(t, h)
	defer func() { _ = records.Close() }()
	ctx := t.Context()
	binding, err := records.GateBinding(ctx, subject)
	if err != nil {
		t.Fatal(err)
	}
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatal(err)
	}
	newRun := func(id string) store.Run {
		return store.Run{ID: id, RepositoryID: binding.GateID, Branch: "a-change", SubmittedHead: head, Base: head,
			Intent: "validate", IntentSource: "push", Build: build, ConfigDigest: "cfg"}
	}
	const runID = "run-left-undecided"
	if _, err := records.CreateRun(ctx, newRun(runID)); err != nil {
		t.Fatal(err)
	}
	if _, err := records.TransitionRun(ctx, runID, []store.RunStatus{store.RunPending}, store.RunRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := records.TransitionRun(ctx, runID, []store.RunStatus{store.RunRunning}, store.RunFailed); err != nil {
		t.Fatal(err)
	}
	copyPath := h.Worktree(binding.GateID, runID)
	git(t, built.Gate.Repository, "fetch", "--quiet", subject, "+refs/heads/a-change:refs/assistant/runs/"+runID)
	git(t, built.Gate.Repository, "worktree", "add", "--quiet", "--detach", copyPath, "refs/assistant/runs/"+runID)

	refused := run(t, h, subject, "eject", "--confirm")
	if refused.code != machine.ExitFailure {
		t.Fatalf("eject --confirm exited %s while a run's copy stood, want failure:\n%s%s", refused.code, refused.stdout, refused.stderr)
	}
	if said := refused.stdout + refused.stderr; !strings.Contains(said, copyPath) {
		t.Fatalf("the refusal does not name the copy %s:\n%s", copyPath, said)
	}
	if _, err := os.Stat(built.Gate.Repository); err != nil {
		t.Fatalf("the refused eject removed the gate at %s: %v", built.Gate.Repository, err)
	}
	if got := git(t, copyPath, "rev-parse", "HEAD"); got != head {
		t.Fatalf("the standing copy reads %q after the refused eject, want its commit %s", got, head)
	}
	if reserved, err := records.RepositoryRemovals(ctx); err != nil || len(reserved) != 0 {
		t.Fatalf("the refused eject left its reservation: %+v, %v", reserved, err)
	}

	git(t, built.Gate.Repository, "worktree", "remove", copyPath)
	var during error
	cli.BetweenEjectPreflightAndRemoval(t, func() {
		_, during = records.CreateRun(ctx, newRun("run-during-eject"))
	})
	if got := run(t, h, subject, "eject", "--confirm"); got.code != machine.ExitOK {
		t.Fatalf("eject --confirm exited %s once the copy was gone:\n%s%s", got.code, got.stdout, got.stderr)
	}
	if !errors.Is(during, store.ErrRepositoryBeingRemoved) {
		t.Fatalf("a run started inside the eject's window = %v, want ErrRepositoryBeingRemoved", during)
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
	t.Parallel()
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
		standingOnChange(t, subject, c.branch)
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
	standingOnChange(t, subject, "nothing")
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
	t.Parallel()
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)

	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	standingOnChange(t, subject, "a-change")
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
	t.Parallel()
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)

	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	standingOnChange(t, subject, "a-change")
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
	if !strings.Contains(plain.stdout, machine.PhraseInputsNotApplied.String()) {
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

	// The refusal is incorrect usage, because its remedy is --upstream, a flag
	// the caller supplies; what it says is checked beside the init tests.
	refused := run(t, h, subject, "init")
	if refused.code != machine.ExitUsage {
		t.Fatalf("init with no origin and no --upstream exited %s, want incorrect usage:\n%s%s",
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
	t.Parallel()
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
	t.Parallel()
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)

	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	standingOnChange(t, subject, "a-change")
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

// firstHoldingStages returns the first two stages a run these tests start
// stops at, which are the two holds a walk meets. It derives them from
// stages.Holding rather than from Implemented's complement, because a run
// stops at the stages that hold and not at the stages without a body: the
// test stage holds with a body wherever the configuration names no test
// command. Deriving rather than naming is what keeps this from failing the
// day a body lands for a reason that has nothing to do with what a test
// checks.
//
// The configuration passed is the resolved default, because the homes these
// tests build write no commands.* into their configuration documents, so the
// runs resolve none either; a test that starts configuring one breaks that
// premise loudly, since its run then stops somewhere this did not derive. The
// review stage never appears here: it holds under no configuration, and these
// walks skip it besides, so a stage both holding and skipped would need this
// helper taught about skips before it could stay right.
//
// It needs two stages to be holding. A build with fewer has stopped being one
// where answering a hold is what carries a run to the next one, and this test
// says so rather than reporting a decision that did not arrive.
func firstHoldingStages(t *testing.T) (holds, next pipeline.Stage) {
	t.Helper()
	holding := stages.Holding(config.Defaults())
	if len(holding) < 2 {
		t.Fatalf("this build holds a default run at %d stage(s), so no run walks from one hold to the "+
			"next; this test needs rewriting against whatever now holds a run", len(holding))
	}
	return holding[0], holding[1]
}

// doctor reports an adapter that resolves on PATH but that auto never chooses, against its
// real CLI on its own row, apart from the agent row that names what a run
// would use. The PATH holds git, claude and cursor and nothing else, so the
// rows are established rather than being whatever this machine has installed.
// It stays serial because PATH is process state.
func TestDoctorSeparatesANamedOnlyRunnableAgentFromTheAutoEligibleOne(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("building a PATH that holds named executables needs a script this test cannot portably make on windows")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("resolving git: %v", err)
	}
	bin := t.TempDir()
	if err := os.Symlink(realGit, filepath.Join(bin, "git")); err != nil {
		t.Fatalf("planting git on the probe PATH: %v", err)
	}
	t.Setenv("PATH", bin)
	h := newHome(t)
	subject := newSubject(t)

	plant := func(name string) {
		t.Helper()
		if err := procgroup.WriteProgram(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("planting %s: %v", name, err)
		}
	}

	// Only a named-only adapter is runnable: no agent can be chosen, and the
	// named-only row says what is there instead of the agent row claiming it.
	plant("cursor")
	report := decodeDoctor(t, run(t, h, subject, "--json", "doctor").stdout)
	agentRow, found := findCheck(report, "agent")
	if !found || agentRow.OK {
		t.Fatalf("doctor accepts an agent when only a named-only adapter is on PATH: %+v", agentRow)
	}
	namedOnly, found := findCheck(report, "named-only agents")
	if !found || namedOnly.OK || namedOnly.Blocking || !strings.Contains(namedOnly.Detail, "cursor") {
		t.Fatalf("doctor does not report cursor as a named-only runnable adapter: %+v", namedOnly)
	}

	// With claude present too, the agent row names claude and cursor stays on
	// its own row.
	plant("claude")
	report = decodeDoctor(t, run(t, h, subject, "--json", "doctor").stdout)
	agentRow, _ = findCheck(report, "agent")
	if !agentRow.OK || agentRow.Detail != "claude" {
		t.Fatalf("doctor's agent row is %+v, want claude", agentRow)
	}
	namedOnly, _ = findCheck(report, "named-only agents")
	if namedOnly.OK || !strings.Contains(namedOnly.Detail, "cursor") || strings.Contains(namedOnly.Detail, "claude") {
		t.Fatalf("named-only row is %+v, want cursor and not claude", namedOnly)
	}
}

// doctor reports the operator's session_provider before a task stalls
// Dispatched: the default pipe is fine; tmux with a binary and an agent that
// declares terminal_workers is fine; tmux without the binary, or with an
// agent that has not declared the capability, is not OK and names the stall.
// It stays serial because PATH is process state.
func TestDoctorReportsWhetherTmuxWorkersCanLaunch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("building a PATH that holds named executables needs a script this test cannot portably make on windows")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("resolving git: %v", err)
	}
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skipf("tmux is not on PATH: %v", err)
	}
	bin := t.TempDir()
	for name, target := range map[string]string{"git": realGit, "tmux": realTmux} {
		if err := os.Symlink(target, filepath.Join(bin, name)); err != nil {
			t.Fatalf("planting %s on the probe PATH: %v", name, err)
		}
	}
	plant := func(name string) {
		t.Helper()
		if err := procgroup.WriteProgram(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("planting %s: %v", name, err)
		}
	}
	plant("claude")
	t.Setenv("PATH", bin)

	h := newHome(t)
	subject := newSubject(t)

	report := decodeDoctor(t, run(t, h, subject, "--json", "doctor").stdout)
	row, found := findCheck(report, machine.CheckSessionProvider)
	if !found || !row.OK || row.Blocking || !strings.Contains(row.Detail, "pipes") {
		t.Fatalf("doctor's session provider row with the default is %+v, want OK naming pipes", row)
	}

	if err := os.WriteFile(h.ConfigFile(), []byte("session_provider: tmux\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report = decodeDoctor(t, run(t, h, subject, "--json", "doctor").stdout)
	row, found = findCheck(report, machine.CheckSessionProvider)
	if !found || !row.OK || row.Blocking ||
		!strings.Contains(row.Detail, "tmux") || !strings.Contains(row.Detail, string(agents.CapabilityTerminalWorkers)) {
		t.Fatalf("doctor's session provider row with tmux and claude is %+v, want OK naming tmux and %s",
			row, agents.CapabilityTerminalWorkers)
	}

	// Same file, no tmux on PATH: the row fails closed and names the stall.
	noTmux := t.TempDir()
	if err := os.Symlink(realGit, filepath.Join(noTmux, "git")); err != nil {
		t.Fatal(err)
	}
	if err := procgroup.WriteProgram(filepath.Join(noTmux, "claude"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", noTmux)
	report = decodeDoctor(t, run(t, h, subject, "--json", "doctor").stdout)
	row, found = findCheck(report, machine.CheckSessionProvider)
	if !found || row.OK || row.Blocking || !strings.Contains(row.Detail, "stays dispatched") ||
		!strings.Contains(row.Detail, "tmux") {
		t.Fatalf("doctor's session provider row without tmux is %+v, want not OK naming the dispatched stall", row)
	}

	// tmux present again, but the configured agent has no terminal_workers.
	t.Setenv("PATH", bin)
	plant(agents.CursorName)
	if err := os.WriteFile(h.ConfigFile(), []byte("session_provider: tmux\nagent: "+agents.CursorName+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report = decodeDoctor(t, run(t, h, subject, "--json", "doctor").stdout)
	row, found = findCheck(report, machine.CheckSessionProvider)
	if !found || row.OK || row.Blocking ||
		!strings.Contains(row.Detail, agents.CursorName) ||
		!strings.Contains(row.Detail, string(agents.CapabilityTerminalWorkers)) ||
		!strings.Contains(row.Detail, "stays dispatched") {
		t.Fatalf("doctor's session provider row with cursor under tmux is %+v, want not OK naming %s and %s",
			row, agents.CursorName, agents.CapabilityTerminalWorkers)
	}
}

// The default branch init records is what origin's own HEAD names, read from
// the working copy, and never the branch the checkout stands on. P7 reads the
// trusted configuration from that branch, so recording a feature branch would
// let the branch under validation be its own trusted copy. The subject is
// standing on a feature branch when init runs, which is the case a fallback to
// the checked-out branch would answer wrongly.
func TestInitRecordsOriginsDefaultBranchFromAFeatureBranch(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)
	h := newHome(t)
	subject := newSubject(t)
	git(t, subject, "checkout", "--quiet", "-b", "a-feature")

	created := run(t, h, subject, "--json", "init")
	if created.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", created.code, created.stdout, created.stderr)
	}
	var built machine.Init
	if err := json.Unmarshal([]byte(created.stdout), &built); err != nil {
		t.Fatalf("init does not decode: %v\n%s", err, created.stdout)
	}
	if built.Repository.DefaultBranch != "main" {
		t.Errorf("init recorded default branch %q from a checkout on a-feature, want origin's main",
			built.Repository.DefaultBranch)
	}
	// And it says where that answer came from, in both renderings, so a
	// wrong one is visible at the moment it is written.
	if built.DefaultBranchSource != machine.DefaultBranchFromOrigin {
		t.Errorf("init says its default branch came from %q, want %q", built.DefaultBranchSource, machine.DefaultBranchFromOrigin)
	}
	plain := run(t, h, subject, "init")
	if plain.code != machine.ExitOK {
		t.Fatalf("assistant init again exited %s:\n%s%s", plain.code, plain.stdout, plain.stderr)
	}
	if !strings.Contains(plain.stdout, machine.DefaultBranchPhrase(machine.DefaultBranchFromOrigin).String()) ||
		!strings.Contains(plain.stdout, "Default branch main") {
		t.Errorf("init does not say it recorded main from origin:\n%s", plain.stdout)
	}
}
