package stages_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/stages"
)

// passingTestCommand and failingTestCommand are the commands these tests
// configure. Each is one command line of the kind PRD section 10 puts in
// commands.test, each prints something the test chose by way of the commit
// subject, and both work under either interpreter this package runs a command
// through.
//
// git is what they run because these tests already require it to build the
// isolated copy, so a command that is available here is available wherever
// this package's tests run at all.
const (
	passingTestCommand = "git log -1 --oneline"
	failingTestCommand = "git log -1 --oneline && exit 3"
)

// A configured check that passes is reported as a pass, with the command it
// ran, the commit it ran against, and the evidence file naming what it did.
// The report carries no finding, so the run advances.
func TestAPassingCheckIsReportedAsAPassWithItsEvidence(t *testing.T) {
	t.Parallel()
	run := newStageRun(t, testStageConfig(passingTestCommand))

	report := run.report(t, stages.Test(run.deps), pipeline.StageTest)
	if len(report.Findings) != 0 {
		t.Fatalf("a passing check reported %d finding(s), and a pass has nothing to report: %+v",
			len(report.Findings), report.Findings)
	}
	if !strings.Contains(report.Summary, run.commit) {
		t.Fatalf("the summary %q does not name the commit %s the check ran against", report.Summary, run.commit)
	}
	if report.Revision != run.commit {
		t.Fatalf("the report says it read %q, and the check ran against %s", report.Revision, run.commit)
	}
	if len(report.Tested) != 1 || report.Tested[0] != passingTestCommand {
		t.Fatalf("the report says it tested %q, and the configured command was %q",
			report.Tested, passingTestCommand)
	}
	if len(report.Evidence) != 1 {
		t.Fatalf("the report names %d evidence artifacts, and one run of one command produces one", len(report.Evidence))
	}
	recorded := readRecorded(t, report.Evidence[0].Path)
	if !strings.Contains(recorded, run.subject) {
		t.Fatalf("the evidence at %s does not hold what the command printed; it holds:\n%s",
			report.Evidence[0].Path, recorded)
	}
	if !strings.Contains(recorded, passingTestCommand) || !strings.Contains(recorded, run.commit) {
		t.Fatalf("the evidence at %s does not say which command ran against which commit; it holds:\n%s",
			report.Evidence[0].Path, recorded)
	}
}

// A configured check that fails is a fix finding, so it is eligible for the
// stage's automatic fix rounds and never resolves itself. The finding carries
// what the command printed and the status it exited with, because a person or
// a fixer reading only the summary cannot act on it.
func TestAFailingCheckIsAFixFindingCarryingWhatTheCommandSaid(t *testing.T) {
	t.Parallel()
	run := newStageRun(t, testStageConfig(failingTestCommand))

	report := run.report(t, stages.Test(run.deps), pipeline.StageTest)
	if len(report.Findings) != 1 {
		t.Fatalf("a failing check reported %d findings, and one failure is one finding: %+v",
			len(report.Findings), report.Findings)
	}
	found := report.Findings[0]
	if !found.FixEligible() {
		t.Fatalf("the failing check reported action %q, and a failing check is fix-eligible so the "+
			"stage's automatic fix rounds apply to it", found.Action)
	}
	if found.Severity != findings.SeverityError {
		t.Fatalf("the failing check reported severity %q, and a check this change does not pass is an error", found.Severity)
	}
	if !strings.Contains(found.Description, run.subject) {
		t.Fatalf("the finding does not carry what the command printed, so nothing can act on it:\n%s", found.Description)
	}
	if !strings.Contains(found.Description, "exit status: 3") {
		t.Fatalf("the finding does not carry the status the command exited with:\n%s", found.Description)
	}
	if !strings.Contains(found.Description, report.Evidence[0].Path) {
		t.Fatalf("the finding does not name the evidence file holding the full output:\n%s", found.Description)
	}
	if len(report.Fixable()) != 1 {
		t.Fatalf("the report offers %d findings to a fixer, and the one failure is the one that "+
			"should reach it", len(report.Fixable()))
	}
}

// PRD section 10 makes commands.test empty by default, and PRD section 5 makes
// a stage that could not gather enough evidence an ask. So a run with no
// command configured holds for a person rather than advancing, which is P3's
// direction: the stage checked nothing and says so instead of passing.
//
// It is answered without a home and without an isolated copy, which is the
// second half of the claim. A stage that had to open a copy to discover it had
// nothing to run would turn a hold a person can answer into an error they
// cannot.
func TestNoConfiguredCheckHoldsForAPersonWithoutTouchingAnything(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P3)

	out, err := runTestStage(t, stages.StageDeps{}, "repository-1", "run-1")
	if err != nil {
		t.Fatalf("the test stage failed on a run with no command configured, and it has an answer "+
			"for that run: %v", err)
	}
	report := out.Report.Normalize()
	if err := report.Validate(); err != nil {
		t.Fatalf("the test stage produced a report the pipeline refuses: %v", err)
	}
	if !report.HasHeld() {
		t.Fatalf("a run with no configured check reported %+v, which does not hold for a person", report)
	}
	if len(report.Fixable()) != 0 {
		t.Fatalf("a run with no configured check offered a finding to a fixer, and there is nothing to fix")
	}
	if len(report.Evidence) != 0 || len(report.Tested) != 0 {
		t.Fatalf("a stage that ran no check claimed to have tested %q with evidence %+v",
			report.Tested, report.Evidence)
	}
}

// The command comes from the configuration the stage was handed and never from
// the branch under validation. P7 puts a command on the trusted side because it
// executes shell, and a stage that read one out of the isolated copy would be a
// second way in whatever the trusted copy said.
//
// The copy here carries both a configuration file naming a different command
// and the command that file names, so a stage that read either would run it and
// leave the tripwire behind. What this claims is that the stage runs what it
// was given; that what it was given came from a trusted commit is
// internal/service's, and this test says nothing about it.
func TestTheCheckComesFromConfigurationAndNotFromTheBranch(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)

	run := newStageRun(t, testStageConfig(passingTestCommand))
	tripwire := filepath.Join(t.TempDir(), "the-branch-chose-this")
	planted := "git log -1 --oneline > " + filepath.ToSlash(tripwire)
	write(t, run.copy, ".assistant.yaml", "commands:\n  test: "+planted+"\n")
	write(t, run.copy, "Makefile", "test:\n\t"+planted+"\n")

	report := run.report(t, stages.Test(run.deps), pipeline.StageTest)
	if len(report.Tested) != 1 || report.Tested[0] != passingTestCommand {
		t.Fatalf("the stage says it ran %q, and the configuration it was handed named %q",
			report.Tested, passingTestCommand)
	}
	if _, err := os.Stat(tripwire); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the command the branch named ran: %s exists, and stat reported %v", tripwire, err)
	}

	// The tripwire has to be able to fire, or its absence above says nothing.
	// The same command reaches the same interpreter through the same stage,
	// this time by being the configured one.
	armed := stages.NewStageDeps(agents.StageAgent{}, run.home, testStageConfig(planted), nil)
	if _, err := runTestStage(t, armed, run.repositoryID, run.runID); err != nil {
		t.Fatalf("running the planted command as the configured one: %v", err)
	}
	if _, err := os.Stat(tripwire); err != nil {
		t.Fatalf("the planted command does not create %s even when it is the one that runs, so its "+
			"absence above establishes nothing: %v", tripwire, err)
	}
}

// PRD section 8 puts test evidence outside the isolated copy on purpose, so a
// test artifact never becomes part of the change being validated. The evidence
// is under the home's evidence directory for this run, and the copy is left as
// the run found it.
func TestEvidenceIsWrittenOutsideTheIsolatedCopy(t *testing.T) {
	t.Parallel()
	run := newStageRun(t, testStageConfig(passingTestCommand))

	report := run.report(t, stages.Test(run.deps), pipeline.StageTest)
	at := report.Evidence[0].Path
	if want := run.home.Evidence(run.runID); !strings.HasPrefix(at, want) {
		t.Fatalf("the evidence is at %s, and PRD section 8 puts it under %s", at, want)
	}
	if strings.HasPrefix(at, run.copy) {
		t.Fatalf("the evidence is at %s, which is inside the isolated copy %s", at, run.copy)
	}
	if dirty := git(t, run.copy, "status", "--porcelain"); dirty != "" {
		t.Fatalf("the stage left the isolated copy changed:\n%s", dirty)
	}
}

// A record that cannot be written does not decide the verdict. The check
// answered, so the stage reports what it answered, adds a note saying the
// output could not be filed, and names no evidence rather than pointing at a
// file that does not hold what it claims.
//
// The record is made unwritable by putting a file where the run's evidence
// directory belongs, which is a state any platform can reach.
func TestAnUnwritableRecordIsANoteAndNotAVerdict(t *testing.T) {
	t.Parallel()
	run := newStageRun(t, testStageConfig(passingTestCommand))
	occupied := run.home.Evidence(run.runID)
	if err := os.MkdirAll(filepath.Dir(occupied), 0o700); err != nil {
		t.Fatalf("making %s: %v", filepath.Dir(occupied), err)
	}
	if err := os.WriteFile(occupied, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("occupying %s: %v", occupied, err)
	}

	report := run.report(t, stages.Test(run.deps), pipeline.StageTest)
	if !strings.Contains(report.Summary, "passed") {
		t.Fatalf("an unwritable record changed the verdict: %q", report.Summary)
	}
	if len(report.Evidence) != 0 {
		t.Fatalf("the report names evidence at %+v, and nothing was recorded", report.Evidence)
	}
	if len(report.Findings) != 1 {
		t.Fatalf("a passing check with no record reported %+v, and it owes exactly the one note", report.Findings)
	}
	if !report.AllNotes() {
		t.Fatalf("failing to file the output held or fixed the run: %+v", report.Findings)
	}
}

// A stage that takes fix rounds runs more than once, and the record keeps
// every attempt rather than letting a later one replace an earlier one. What
// is left of a run whose first attempt failed and whose second passed is
// otherwise only the second, which is the half that says least.
func TestASecondAttemptIsAddedToTheRecordRatherThanReplacingIt(t *testing.T) {
	t.Parallel()
	run := newStageRun(t, testStageConfig(failingTestCommand))
	first := run.report(t, stages.Test(run.deps), pipeline.StageTest)

	run.deps = stages.NewStageDeps(agents.StageAgent{}, run.home, testStageConfig(passingTestCommand), nil)
	second := run.report(t, stages.Test(run.deps), pipeline.StageTest)
	if first.Evidence[0].Path != second.Evidence[0].Path {
		t.Fatalf("the two attempts recorded to %s and %s, and one run has one record",
			first.Evidence[0].Path, second.Evidence[0].Path)
	}
	recorded := readRecorded(t, second.Evidence[0].Path)
	if strings.Count(recorded, "=== test stage") != 2 {
		t.Fatalf("the record holds %d attempts after two, so one replaced the other:\n%s",
			strings.Count(recorded, "=== test stage"), recorded)
	}
	if !strings.Contains(recorded, failingTestCommand) || !strings.Contains(recorded, "=== exit status: 3") {
		t.Fatalf("the record does not hold the first attempt after the second:\n%s", recorded)
	}
}

// A run whose isolated copy is not there is a stage that could not run at all,
// so it is an error rather than a finding: nothing about the change is known
// and nothing a person answers would make it known.
func TestAMissingIsolatedCopyIsAnErrorAndNotAFinding(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	deps := stages.NewStageDeps(agents.StageAgent{}, h, testStageConfig(passingTestCommand), nil)

	_, err := runTestStage(t, deps, "repository-1", "run-1")
	if err == nil {
		t.Fatal("the test stage reported on a run whose isolated copy does not exist")
	}
	if at := h.Worktree("repository-1", "run-1"); !strings.Contains(err.Error(), at) {
		t.Fatalf("the refusal %q does not name the copy %s it looked for", err, at)
	}
}

// All has to place this body at the test stage, and the body has to advance a
// run whose configured check passes. It is the property the shared assertion in
// stages_test.go cannot make, because that one builds every body from an empty
// StageDeps and this stage's answer to that is a hold.
func TestAllPlacesTheTestBodyAndItAdvancesAPassingRun(t *testing.T) {
	t.Parallel()
	run := newStageRun(t, testStageConfig(passingTestCommand))
	placed := stages.All(run.deps).Test

	out, err := runStageBody(t, placed, pipeline.StageTest, run.repositoryID, run.runID)
	if err != nil {
		t.Fatalf("running the body All places at the test stage: %v", err)
	}
	report := out.Report.Normalize()
	if err := report.Validate(); err != nil {
		t.Fatalf("the body All places at the test stage produced a report the pipeline refuses: %v", err)
	}
	pending, err := stages.Pending(pipeline.StageTest.String()).NewBody()(
		t.Context(), pipeline.Input{Stage: pipeline.StageTest})
	if err != nil {
		t.Fatalf("running Pending for the test stage: %v", err)
	}
	if report.HasHeld() || len(report.Findings) != 0 {
		t.Fatalf("the body All places at the test stage did not advance a run whose check passed: %+v", report)
	}
	if strings.Contains(pending.Report.Summary, report.Summary) {
		t.Fatalf("All places Pending at the test stage, which this build reports a body for")
	}
}

// A failing check is fix-eligible, and this build has no fixer, so a run that
// reaches one ends at PendingFixer naming what is missing. That is the
// documented end of this path, and it is checked rather than described so that
// a build which later reports a pass here is caught.
func TestAFailingCheckReachesTheFixerThisBuildDoesNotHave(t *testing.T) {
	t.Parallel()
	run := newStageRun(t, testStageConfig(failingTestCommand))

	all := pipeline.ConstantStages("nothing to report")
	all.Test = stages.All(run.deps).Test
	rounds := config.FixRounds{Test: 3}
	p, err := pipeline.New(pipeline.Options{
		Stages: all,
		Fixer:  stages.PendingFixer(nil),
		Rounds: rounds,
		Budget: config.DefaultRunBudget,
	})
	if err != nil {
		t.Fatalf("building a pipeline around the test stage: %v", err)
	}
	executor, err := p.Executor(graph.NewMemoryStore())
	if err != nil {
		t.Fatalf("building an executor: %v", err)
	}
	state, err := p.NewState(pipeline.Start{
		Repository: run.repositoryID,
		Run:        run.runID,
		Branch:     "topic",
		Base:       "main",
		Submitted:  run.commit,
	})
	if err != nil {
		t.Fatalf("building the run's initial state: %v", err)
	}
	if _, err := executor.Run(t.Context(), run.runID, state); err == nil {
		t.Fatal("a run whose check failed reached no fixer and reported no failure, so it passed a " +
			"stage this build cannot fix")
	} else if !strings.Contains(err.Error(), "no fixer is implemented") {
		t.Fatalf("a run whose check failed ended with %v, and this build ends it at the missing fixer", err)
	}
}

// runTestStage runs the test stage's body over a run's identity.
func runTestStage(t *testing.T, deps stages.StageDeps, repositoryID, runID string) (pipeline.Output, error) {
	t.Helper()
	return runStageBody(t, stages.Test(deps), pipeline.StageTest, repositoryID, runID)
}

// testStageConfig is the resolved configuration a run is given, with one command
// set. It starts from the schema defaults so nothing else is invented here.
func testStageConfig(command string) config.Config {
	cfg := config.Defaults()
	cfg.Commands.Test = command
	return cfg
}
