package stages

import (
	"context"
	"fmt"
	"strings"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/pipeline"
)

// Test is the test stage: it validates this change with the check the
// configuration names, and reports what that check answered.
//
// # What it establishes, and what it does not
//
// PRD section 5 asks this stage for targeted validation of this change and
// this intent rather than a full suite, and PRD section 10 puts the check
// itself in commands.test, described there in those same words. So the
// targeting lives in the configured command, and this stage runs that command
// rather than deriving one: a stage that appended paths to a command line it
// cannot parse would be inventing a second targeting rule, quietly, out of a
// value whose whole shape is one line of shell.
//
// What a passing run therefore establishes is that the configured command
// exited zero against the commit in the run's isolated copy. The report says
// that and no more. Whether that command covers the intent is a judgement this
// build does not make, and the residual gaps below say so rather than letting
// a pass read as more than it is.
//
// # Where the command comes from, and where it must not come from
//
// The command is read from StageDeps.Config and from nothing else. It executes
// shell, so P7 puts it on the trusted side: it belongs to the default branch
// and never to the branch under validation, and this body opens no
// configuration file in the isolated copy at all. A pushed branch setting a
// commands.test of its own therefore changes nothing about what runs here.
//
// That is the half of P7 this stage owns. The other half is that what it was
// handed was itself resolved from a trusted commit, and that half belongs to
// whoever resolves the configuration; StageDeps.Config says where this build's
// value comes from and what section 10 it is short of. This stage cannot
// narrow that gap and does not claim to. What it can do is refuse to be a
// second way in, which is what reading only what it was given amounts to.
//
// # The three answers, and which of them holds
//
// A command that exits zero passes. The report carries the pass and no
// finding, and the run advances.
//
// A command that exits non-zero fails, and the finding is a fix. It is
// objectively wrong and mechanically fixable in PRD section 5's sense, so it
// is eligible for this stage's automatic fix rounds, config.FixRounds.Test.
// No fixer is written, and internal/cli wires PendingFixer, so such a finding
// reaches that and the run fails there naming what is missing. That is the
// honest end of this path, it is documented on PendingFixer rather than
// softened here, and it is what this stage's tests hold the built pipeline to.
//
// A command that did not report an exit status of its own establishes nothing
// either way, and neither does a run with no command configured at all. Both
// are the case PRD section 5 calls a stage that could not gather enough
// evidence, so both are ask findings and both hold the stage for a person,
// which is P3's fail-closed direction. PRD section 5 offers three answers when
// no targeted check can establish the intent - write one, verify manually with
// recorded evidence, or report that sufficient evidence is not possible - and
// this build always takes the third. The first two need an agent authoring a
// check and a person recording a verification, and neither is built; a stage
// that took either silently would be reporting a pass it did not establish.
//
// # Evidence
//
// PRD section 8 puts test evidence at evidence/<run>, deliberately outside the
// isolated copy, so that a test artifact never becomes part of the change
// being validated. This body writes there and never into the copy, and it
// writes while the command runs, so the record survives a command this build
// gives up waiting on.
//
// The report names that file, and the finding carries a bounded tail of the
// output with a marker saying how much earlier output it leaves out and where
// the whole of it is. That is PRD section 8's rule that the full log is the
// authority and what travels is a bounded projection of it.
//
// Filing that record is never allowed to decide the verdict. A command that
// answered has answered, so an evidence file that could not be opened or
// written is reported as a note, the report then names no evidence and claims
// none, and the stage still reports what the check said. The alternative is a
// gate that stops validating when a disk fills, and a report that points at a
// file not holding what it says it does.
//
// # No agent, and therefore no evidence binding
//
// Nothing here starts an agent. The findings are built from the toolchain's
// own exit status rather than parsed out of anything an agent wrote, so there
// is no report to bind and no evidence set to bind it to: PRD section 5's
// binding rule and findings.ParseReviewReport answer for a review's claims
// about code, and this stage makes none. A later body that did ask an agent
// whether a check establishes the intent would be making such a claim and
// would owe that rule; it would not inherit an exemption from here.
//
// # Where the line between an error and a finding falls
//
// A body returns an error only when the stage could not run at all, and the
// line is drawn at the command. The run's own state, the isolated copy and the
// commit in it are this build's machinery: a failure in any of them means no
// check can run and none of it is the change's fault, so those are errors. A
// cancelled run is an error for the same reason and is returned as the
// context's own, so a caller can recognize it.
//
// The command's own failure is the other side of that line. It ran, or it
// could not be started, and either way the stage ran and has something to
// report, so it reports rather than stopping the run.
//
// # The residual gaps
//
// Whether the configured command is targeted at this change is not checked.
// The key is documented as a targeted check and this stage runs what it is
// given, so a repository configuring a full suite gets a full suite, slowly,
// and this stage does not notice.
//
// Whether the configured command covers the intent is not checked either. The
// run's intent is not read here, because nothing here could compare a line of
// shell against it; a build that judges that needs an agent, and this one does
// not have that stage.
//
// A command that leaves a process behind leaves it running. commandGrace says
// what that bounds and what it does not.
func Test(deps StageDeps) pipeline.Implementation {
	return pipeline.Implementation{
		Reads: []pipeline.Key{pipeline.KeyRepository, pipeline.KeyRun},
		NewBody: func() pipeline.Body {
			return func(ctx context.Context, in pipeline.Input) (pipeline.Output, error) {
				return runTargetedCheck(ctx, deps, in)
			}
		},
	}
}

// runTargetedCheck is the stage body: it runs the configured check in the run's
// isolated copy and turns what the check answered into the stage's report.
func runTargetedCheck(ctx context.Context, deps StageDeps, in pipeline.Input) (pipeline.Output, error) {
	command := strings.TrimSpace(deps.Config.Commands.Test)
	if command == "" {
		return pipeline.Output{Report: noTestCommandConfigured()}, nil
	}
	check, err := runConfiguredCheck(ctx, deps, in, command)
	if err != nil {
		return pipeline.Output{}, err
	}
	return pipeline.Output{Report: testReport(in.Stage, command, check)}, nil
}

// testReport is what the stage found. The three cases are what a command can
// answer: it did not report a status at all, it exited zero, or it exited
// something else. Whether it reported a status is asked first, because a
// status is what the other two read and a command that gave none carries the
// same -1 as one this build could not start.
func testReport(stage pipeline.Stage, command string, check configuredCheck) findings.Report {
	result, record := check.result, check.record
	report := findings.Report{
		Revision: check.commit,
		Tested:   []string{command},
	}
	if record.recorded() {
		report.Evidence = []findings.Evidence{{
			Label: "the full output of every attempt this run made at the configured test command",
			Path:  record.path,
		}}
	}
	switch {
	case !result.exited:
		report.Summary = fmt.Sprintf(
			"The configured test command did not report an exit status against %s, so nothing was "+
				"established about this change either way.", check.commit)
		report.Findings = append(report.Findings, findings.Finding{
			ID:       "test-not-settled",
			Severity: findings.SeverityWarning,
			Action:   findings.ActionAsk,
			Description: fmt.Sprintf(
				"The configured test command did not report an exit status of its own, so it "+
					"established neither a pass nor a failure. Approving carries the run past a check "+
					"that answered nothing; skipping records the stage as skipped; cancelling ends the "+
					"run.\n\ncommand: %s\ncommit: %s\n%s\n\n%s",
				command, check.commit, notSettledReason(result), checkOutputSection(result, record)),
		})

	case result.code == 0:
		report.Summary = fmt.Sprintf(
			"The configured test command passed against %s: %q exited zero in the run's isolated copy.",
			check.commit, command)

	default:
		report.Summary = fmt.Sprintf(
			"The configured test command failed against %s: %q exited %d in the run's isolated copy.",
			check.commit, command, result.code)
		report.Findings = append(report.Findings, findings.Finding{
			ID:       "test-failed",
			Severity: findings.SeverityError,
			Action:   findings.ActionFix,
			Description: fmt.Sprintf(
				"The configured test command failed, so this change does not pass the check this "+
					"repository holds it to.\n\ncommand: %s\nexit status: %d\ncommit: %s\n\n%s",
				command, result.code, check.commit, checkOutputSection(result, record)),
		})
	}
	if !record.recorded() {
		report.Findings = append(report.Findings, record.unrecordedNote(stage))
	}
	return report
}

// noTestCommandConfigured is the report for a run whose configuration names no test
// command.
//
// PRD section 10 makes commands.test empty by default, so this is the ordinary
// state of a repository that has not configured one rather than a malfunction.
// It is still a stage that established nothing, and the finding is an ask
// rather than a note for that reason: a note would let a run advance past a
// stage that never checked anything, which is the one thing this stage may not
// do.
//
// It reads no state and touches no filesystem, so a run with no command
// configured holds here whatever else is wrong with the machine. That is
// deliberate: the answer does not depend on the isolated copy, so failing to
// open one would replace a hold a person can act on with an error they cannot.
func noTestCommandConfigured() findings.Report {
	return findings.Report{
		Summary: "No test command is configured, so this stage ran no check and established nothing " +
			"about this change.",
		Findings: []findings.Finding{{
			ID:       "test-no-command",
			Severity: findings.SeverityWarning,
			Action:   findings.ActionAsk,
			Description: "This repository configures no test command, so the test stage had nothing " +
				"targeted to run and could not gather evidence that this change does what it set out " +
				"to do. Setting commands.test on the default branch is what gives this stage " +
				"something to run; it executes shell, so it belongs to that branch and never to the " +
				"branch under validation. Approving carries the run past a stage that checked " +
				"nothing; skipping records it as skipped; cancelling ends the run.",
		}},
	}
}
