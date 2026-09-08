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

// The commands these tests configure. Each is one command line of the kind PRD
// section 10 puts in commands.lint, and each works under either interpreter
// this package runs a command through.
//
// git is what the first two run because these tests already require it to
// build the isolated copy, so a command available here is available wherever
// this package's tests run at all. The third is a name nothing provides, which
// is what a repository configuring a linter it has not installed has.
const (
	passingLintCommand   = "git log -1 --oneline"
	violatingLintCommand = "git log -1 --oneline && exit 3"
	absentLintCommand    = "assistant-no-such-linter-a3f9c2 ./..."
)

// An analysis that reports nothing is reported as a pass, with the command it
// ran, the commit it ran against, and the evidence file naming what it did.
// The report carries no finding, so the run advances.
func TestAnAnalysisReportingNothingIsAPassWithItsEvidence(t *testing.T) {
	t.Parallel()
	run := newStageRun(t, lintStageConfig(passingLintCommand))

	report := lintReportOf(t, run)
	if len(report.Findings) != 0 {
		t.Fatalf("a clean analysis reported %d finding(s), and a pass has nothing to report: %+v",
			len(report.Findings), report.Findings)
	}
	if !strings.Contains(report.Summary, run.commit) {
		t.Fatalf("the summary %q does not name the commit %s the analysis ran against",
			report.Summary, run.commit)
	}
	if report.Revision != run.commit {
		t.Fatalf("the report says it read %q, and the analysis ran against %s", report.Revision, run.commit)
	}
	if len(report.Tested) != 1 || report.Tested[0] != passingLintCommand {
		t.Fatalf("the report says it checked %q, and the configured command was %q",
			report.Tested, passingLintCommand)
	}
	if len(report.Evidence) != 1 {
		t.Fatalf("the report names %d evidence artifacts, and one run of one command produces one",
			len(report.Evidence))
	}
	recorded := readRecorded(t, report.Evidence[0].Path)
	if !strings.Contains(recorded, passingLintCommand) || !strings.Contains(recorded, run.commit) {
		t.Fatalf("the evidence at %s does not say which command ran against which commit; it holds:\n%s",
			report.Evidence[0].Path, recorded)
	}
}

// A pass says the command exited zero and does not say the change is clean.
// The difference is the whole of what this stage can and cannot establish: a
// command that analysed nothing and exited zero produces this same report, so
// a summary asserting the change carries no violations would be asserting
// something no exit status carries.
func TestAPassClaimsTheExitStatusAndNotACleanChange(t *testing.T) {
	t.Parallel()
	run := newStageRun(t, lintStageConfig(passingLintCommand))

	report := lintReportOf(t, run)
	if !strings.Contains(report.Summary, "exited zero") {
		t.Fatalf("the summary does not say what actually happened, which is that the command "+
			"exited zero: %q", report.Summary)
	}
	for _, overclaim := range []string{
		"this change is clean",
		"no violations exist",
		"the change passes static analysis",
	} {
		if strings.Contains(strings.ToLower(report.Summary), overclaim) {
			t.Fatalf("the summary claims %q, which an exit status does not establish: %q",
				overclaim, report.Summary)
		}
	}
}

// An analysis that reports violations is a fix finding, so it is eligible for
// the stage's automatic fix rounds. The finding carries what the command
// printed and the status it exited with, because a person or a fixer reading
// only the summary cannot act on it.
func TestAReportedViolationIsAFixFindingCarryingWhatTheCommandSaid(t *testing.T) {
	t.Parallel()
	run := newStageRun(t, lintStageConfig(violatingLintCommand))

	report := lintReportOf(t, run)
	if len(report.Findings) != 1 {
		t.Fatalf("a violating analysis reported %d findings, and one run is one finding: %+v",
			len(report.Findings), report.Findings)
	}
	found := report.Findings[0]
	if !found.FixEligible() {
		t.Fatalf("the violating analysis reported action %q, and a violation is fix-eligible so the "+
			"stage's automatic fix rounds apply to it", found.Action)
	}
	if found.Severity != findings.SeverityError {
		t.Fatalf("the violating analysis reported severity %q, and static analysis this change does "+
			"not pass is an error", found.Severity)
	}
	if !strings.Contains(found.Description, run.subject) {
		t.Fatalf("the finding does not carry what the command printed, so nothing can act on it:\n%s",
			found.Description)
	}
	if !strings.Contains(found.Description, "exit status: 3") {
		t.Fatalf("the finding does not carry the status the command exited with:\n%s", found.Description)
	}
	if !strings.Contains(found.Description, report.Evidence[0].Path) {
		t.Fatalf("the finding does not name the evidence file holding the full output:\n%s",
			found.Description)
	}
}

// A configured linter this machine cannot run holds for a person rather than
// reporting a clean analysis, which is the failure this stage exists to
// refuse: a lint stage that passes because it could not run is worse than one
// that fails, because the run then reports validated over analysis that never
// happened.
//
// It also does not become a fix finding. A fix round is an agent editing this
// change, and no edit to the change installs a tool, so three rounds of it
// would end where they started and the run would park naming the bound rather
// than the missing linter.
//
// The control is in the same test and is what keeps the hold from being what
// this stage says about every failure: the same body, the same run, and the
// same isolated copy, given a command that runs and objects, produces a fix
// finding rather than a hold.
func TestALinterThatCannotRunHoldsRatherThanReportingClean(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P3)
	run := newStageRun(t, lintStageConfig(absentLintCommand))

	report := lintReportOf(t, run)
	if !report.HasHeld() {
		t.Fatalf("a lint command this machine cannot run produced %+v, which does not hold for a "+
			"person", report)
	}
	if len(report.Fixable()) != 0 {
		t.Fatalf("a lint command this machine cannot run offered %d finding(s) to a fixer, and "+
			"editing this change installs nothing: %+v", len(report.Fixable()), report.Fixable())
	}
	if len(report.Findings) != 1 {
		t.Fatalf("a lint command this machine cannot run reported %+v, and one command is one answer",
			report.Findings)
	}
	if !strings.Contains(report.Summary, "could not be run") {
		t.Fatalf("the summary does not say the command could not be run: %q", report.Summary)
	}
	if !strings.Contains(report.Findings[0].Description, absentLintCommand) {
		t.Fatalf("the finding does not name the command that could not be run:\n%s",
			report.Findings[0].Description)
	}

	// The control. Without it this test would pass just as well against a body
	// that held on every non-zero exit, which would put every real violation
	// in front of a person instead of in front of the fixer.
	run.deps = stages.NewStageDeps(agents.StageAgent{}, run.home, lintStageConfig(violatingLintCommand), nil)
	objected := lintReportOf(t, run)
	if objected.HasHeld() {
		t.Fatalf("a command that ran and objected also held, so the hold above says nothing about "+
			"a linter that could not run: %+v", objected)
	}
	if len(objected.Fixable()) != 1 {
		t.Fatalf("a command that ran and objected offered %d findings to a fixer, and the one "+
			"violation is the one that should reach it", len(objected.Fixable()))
	}
}

// PRD section 10 makes commands.lint empty by default and gives that case to
// the document stage's combined pass, which this build has no body for. So a
// run with no command configured holds for a person rather than advancing,
// which is P3's direction: the stage analysed nothing and says so instead of
// passing.
//
// It is answered without a home and without an isolated copy, which is the
// second half of the claim. A stage that had to open a copy to discover it had
// nothing to run would turn a hold a person can answer into an error they
// cannot.
func TestNoConfiguredAnalysisHoldsForAPersonWithoutTouchingAnything(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P3)

	out, err := runStageBody(t, stages.Lint(stages.StageDeps{}), pipeline.StageLint, "repository-1", "run-1")
	if err != nil {
		t.Fatalf("the lint stage failed on a run with no command configured, and it has an answer "+
			"for that run: %v", err)
	}
	report := out.Report.Normalize()
	if err := report.Validate(); err != nil {
		t.Fatalf("the lint stage produced a report the pipeline refuses: %v", err)
	}
	if !report.HasHeld() {
		t.Fatalf("a run with no configured analysis reported %+v, which does not hold for a person",
			report)
	}
	if len(report.Fixable()) != 0 {
		t.Fatalf("a run with no configured analysis offered a finding to a fixer, and there is " +
			"nothing to fix")
	}
	if len(report.Evidence) != 0 || len(report.Tested) != 0 {
		t.Fatalf("a stage that ran no analysis claimed to have checked %q with evidence %+v",
			report.Tested, report.Evidence)
	}
}

// The command comes from the configuration the stage was handed and never from
// the branch under validation. P7 puts a command on the trusted side because
// it executes shell, and a stage that read one out of the isolated copy would
// be a second way in whatever the trusted copy said.
//
// The copy here carries both a configuration document naming a different
// command and the command that document names, so a stage that read either
// would run it and leave the tripwire behind. What this claims is that the
// stage runs what it was given; that what it was given came from a trusted
// commit is internal/service's, and this test says nothing about it.
func TestTheAnalysisComesFromConfigurationAndNotFromTheBranch(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)

	run := newStageRun(t, lintStageConfig(passingLintCommand))
	tripwire := filepath.Join(t.TempDir(), "the-branch-chose-this")
	planted := "git log -1 --oneline > " + filepath.ToSlash(tripwire)
	write(t, run.copy, ".assistant.yaml", "commands:\n  lint: "+planted+"\n")
	write(t, run.copy, "Makefile", "lint:\n\t"+planted+"\n")

	report := lintReportOf(t, run)
	if len(report.Tested) != 1 || report.Tested[0] != passingLintCommand {
		t.Fatalf("the stage says it ran %q, and the configuration it was handed named %q",
			report.Tested, passingLintCommand)
	}
	if _, err := os.Stat(tripwire); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the command the branch named ran: %s exists, and stat reported %v", tripwire, err)
	}

	// The tripwire has to be able to fire, or its absence above says nothing.
	// The same command reaches the same interpreter through the same stage,
	// this time by being the configured one.
	run.deps = stages.NewStageDeps(agents.StageAgent{}, run.home, lintStageConfig(planted), nil)
	lintReportOf(t, run)
	if _, err := os.Stat(tripwire); err != nil {
		t.Fatalf("the planted command does not create %s even when it is the one that runs, so its "+
			"absence above establishes nothing: %v", tripwire, err)
	}
}

// PRD section 8 puts a stage's evidence outside the isolated copy on purpose,
// so an artifact of checking never becomes part of the change being validated.
// The evidence is under the home's evidence directory for this run, and the
// copy is left as the run found it.
//
// The record is also this stage's own rather than shared with the stage that
// ran before it. Both are named after the stage that wrote them, and a run
// whose test and lint stages wrote to one file would offer each stage's report
// a path holding the other's output as well.
func TestLintEvidenceIsItsOwnFileOutsideTheIsolatedCopy(t *testing.T) {
	t.Parallel()
	run := newStageRun(t, lintStageConfig(passingLintCommand))

	report := lintReportOf(t, run)
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

	run.deps = stages.NewStageDeps(agents.StageAgent{}, run.home, testStageConfig(passingTestCommand), nil)
	tested := run.report(t, stages.Test(run.deps), pipeline.StageTest)
	if tested.Evidence[0].Path == at {
		t.Fatalf("the test and lint stages of one run both recorded to %s, so each stage's report "+
			"points at the other's output too", at)
	}
}

// A record that cannot be written does not decide the verdict. The analysis
// answered, so the stage reports what it answered, adds a note saying the
// output could not be filed, and names no evidence rather than pointing at a
// file that does not hold what it claims.
//
// The record is made unwritable by putting a file where the run's evidence
// directory belongs, which is a state any platform can reach.
func TestAnUnwritableLintRecordIsANoteAndNotAVerdict(t *testing.T) {
	t.Parallel()
	run := newStageRun(t, lintStageConfig(passingLintCommand))
	occupied := run.home.Evidence(run.runID)
	if err := os.MkdirAll(filepath.Dir(occupied), 0o700); err != nil {
		t.Fatalf("making %s: %v", filepath.Dir(occupied), err)
	}
	if err := os.WriteFile(occupied, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("occupying %s: %v", occupied, err)
	}

	report := lintReportOf(t, run)
	if !strings.Contains(report.Summary, "exited zero") {
		t.Fatalf("an unwritable record changed the verdict: %q", report.Summary)
	}
	if len(report.Evidence) != 0 {
		t.Fatalf("the report names evidence at %+v, and nothing was recorded", report.Evidence)
	}
	if len(report.Findings) != 1 {
		t.Fatalf("a clean analysis with no record reported %+v, and it owes exactly the one note",
			report.Findings)
	}
	if !report.AllNotes() {
		t.Fatalf("failing to file the output held or fixed the run: %+v", report.Findings)
	}
	if report.Findings[0].ID != "lint-evidence-unrecorded" {
		t.Fatalf("the note is filed as %q, and this stage's findings are filed under its own name",
			report.Findings[0].ID)
	}
}

// A run whose isolated copy is not there is a stage that could not run at all,
// so it is an error rather than a finding: nothing about the change is known
// and nothing a person answers would make it known.
func TestAMissingIsolatedCopyIsAnErrorForTheLintStageToo(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	deps := stages.NewStageDeps(agents.StageAgent{}, h, lintStageConfig(passingLintCommand), nil)

	_, err := runStageBody(t, stages.Lint(deps), pipeline.StageLint, "repository-1", "run-1")
	if err == nil {
		t.Fatal("the lint stage reported on a run whose isolated copy does not exist")
	}
	if at := h.Worktree("repository-1", "run-1"); !strings.Contains(err.Error(), at) {
		t.Fatalf("the refusal %q does not name the copy %s it looked for", err, at)
	}
}

// All has to place this body at the lint stage, and the body has to advance a
// run whose analysis reports nothing. It is the property the shared assertion
// in stages_test.go cannot make, because that one builds every body from an
// empty StageDeps and this stage's answer to that is a hold.
func TestAllPlacesTheLintBodyAndItAdvancesACleanRun(t *testing.T) {
	t.Parallel()
	run := newStageRun(t, lintStageConfig(passingLintCommand))
	placed := stages.All(run.deps).Lint

	out, err := runStageBody(t, placed, pipeline.StageLint, run.repositoryID, run.runID)
	if err != nil {
		t.Fatalf("running the body All places at the lint stage: %v", err)
	}
	report := out.Report.Normalize()
	if err := report.Validate(); err != nil {
		t.Fatalf("the body All places at the lint stage produced a report the pipeline refuses: %v", err)
	}
	pending, err := stages.Pending(pipeline.StageLint.String()).NewBody()(
		t.Context(), pipeline.Input{Stage: pipeline.StageLint})
	if err != nil {
		t.Fatalf("running Pending for the lint stage: %v", err)
	}
	if report.HasHeld() || len(report.Findings) != 0 {
		t.Fatalf("the body All places at the lint stage did not advance a run whose analysis "+
			"reported nothing: %+v", report)
	}
	if strings.Contains(pending.Report.Summary, report.Summary) {
		t.Fatalf("All places Pending at the lint stage, which this build reports a body for")
	}
}

// A reported violation is fix-eligible, and this build has no fixer, so a run
// that reaches one ends at PendingFixer naming what is missing. That is the
// documented end of this path, and it is checked rather than described so that
// a build which later reports a pass here is caught.
func TestAViolationReachesTheFixerThisBuildDoesNotHave(t *testing.T) {
	t.Parallel()
	run := newStageRun(t, lintStageConfig(violatingLintCommand))

	all := pipeline.ConstantStages("nothing to report")
	all.Lint = stages.All(run.deps).Lint
	p, err := pipeline.New(pipeline.Options{
		Stages: all,
		Fixer:  stages.PendingFixer(nil),
		Rounds: config.FixRounds{Lint: 3},
		Budget: config.DefaultRunBudget,
	})
	if err != nil {
		t.Fatalf("building a pipeline around the lint stage: %v", err)
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
		t.Fatal("a run whose analysis reported violations reached no fixer and reported no failure, " +
			"so it passed a stage this build cannot fix")
	} else if !strings.Contains(err.Error(), "no fixer is implemented") {
		t.Fatalf("a run whose analysis reported violations ended with %v, and this build ends it at "+
			"the missing fixer", err)
	}
}

// A linter that could not run never reaches a fix round, whatever the
// configured limit is. The finding above is an ask, and internal/pipeline
// never lets an ask into a fix round, so the run holds for a person rather
// than ending at the fixer this build does not have.
//
// It is the same pipeline the violation test drives, with the same fix round
// limit, so what differs between the two ends is the finding's action and not
// the configuration.
func TestALinterThatCannotRunHoldsRatherThanEnteringAFixRound(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P3)
	run := newStageRun(t, lintStageConfig(absentLintCommand))

	all := pipeline.ConstantStages("nothing to report")
	all.Lint = stages.All(run.deps).Lint
	p, err := pipeline.New(pipeline.Options{
		Stages: all,
		Fixer:  stages.PendingFixer(nil),
		Rounds: config.FixRounds{Lint: 3},
		Budget: config.DefaultRunBudget,
	})
	if err != nil {
		t.Fatalf("building a pipeline around the lint stage: %v", err)
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
	status, err := executor.Run(t.Context(), run.runID, state)
	if err != nil {
		t.Fatalf("a linter that could not run reached the fixer this build does not have, so the "+
			"stage sent a question into a fix round: %v", err)
	}
	if status.Status != graph.StatusHalted || status.Decision == nil {
		t.Fatalf("a run whose linter could not run ended as %v with decision %+v, and an ask holds "+
			"the run at a decision a person answers", status.Status, status.Decision)
	}
}

// lintReportOf runs the lint stage over a run and returns the report the
// pipeline would record.
func lintReportOf(t *testing.T, run *stageRun) findings.Report {
	t.Helper()
	return run.report(t, stages.Lint(run.deps), pipeline.StageLint)
}

// lintStageConfig is the resolved configuration a run is given, with the lint
// command set. It starts from the schema defaults so nothing else is invented
// here.
func lintStageConfig(command string) config.Config {
	cfg := config.Defaults()
	cfg.Commands.Lint = command
	return cfg
}
