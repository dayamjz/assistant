package stages

import (
	"context"
	"fmt"
	"strings"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/pipeline"
)

// Lint is the lint stage: it runs the static analysis the configuration names,
// last among the local checks, and reports what that analysis answered.
//
// # What a pass establishes, and the one thing it cannot
//
// PRD section 5 puts static analysis here, last among the local checks so it
// does not churn over code that may still change, and PRD section 10 puts the
// analysis itself in commands.lint. So this stage runs what it is given rather
// than deriving a command, on the same terms as the test stage.
//
// What a passing run establishes is that the configured command exited zero
// against the commit in the run's isolated copy. The report says that and no
// more, and the wording matters here more than anywhere else in this package:
// a summary saying the change is clean would be claiming something an exit
// status cannot carry.
//
// It cannot carry it because a command that exits zero having analysed nothing
// is a command that exits zero. This module's own make target began that way:
// it printed a line saying golangci-lint was not installed and exited zero, and
// it was later rewritten to refuse instead. Nothing that ran the target could
// have told those two versions apart, and nothing in this stage can either.
// The residual gaps below say so rather than letting a pass read as more than
// it is. What this stage can refuse is the narrower case it can actually see,
// which is the next section.
//
// # The five answers
//
// A command that exits zero passes. The report carries the pass and no
// finding, and the run advances.
//
// A command the interpreter could not run establishes nothing, so it holds for
// a person rather than passing and rather than entering a fix round. That case
// is a missing or unexecutable tool - the configured command names a linter
// that is not installed - and it is the one shape of "the tool is not here"
// this stage can recognize. It is deliberately not a fix finding: a fix round
// is an agent editing the change, the change is not what is wrong, and three
// rounds of it would end where they started. interpreterCouldNotRun is the
// recognition and states what it reads and what it cannot tell apart.
//
// A command that exits non-zero otherwise fails, and that finding is a fix. It
// is objectively wrong and mechanically fixable in PRD section 5's sense, so
// it is eligible for this stage's automatic fix rounds, config.FixRounds.Lint.
// No fixer is written, and internal/cli wires PendingFixer, so such a finding
// reaches that and the run fails there naming what is missing. That is the
// honest end of this path and it is what this stage's tests hold the built
// pipeline to.
//
// A command that did not report an exit status of its own establishes nothing
// either way, and neither does a run with no command configured at all. Both
// are PRD section 5's stage that could not gather enough evidence, so both are
// ask findings and both hold the stage for a person, which is P3's fail-closed
// direction.
//
// The test stage reads the same statuses and draws the line in one place
// rather than two: every non-zero exit there is a fix. That difference is not
// one this stage is entitled to settle for it, and it is recorded here rather
// than resolved quietly in either direction.
//
// # Where the command comes from
//
// From StageDeps.Config and from nowhere else. It executes shell, so P7 puts
// it on the trusted side: it belongs to the default branch and never to the
// branch under validation. runConfiguredCheck owns that and states which half
// of P7 a stage answers for.
//
// # No agent, and therefore no evidence binding
//
// Nothing here starts an agent. The findings are built from the toolchain's
// own exit status rather than parsed out of anything an agent wrote, so there
// is no report to bind and no evidence set to bind it to: PRD section 5's
// binding rule and findings.ParseReviewReport answer for a review's claims
// about code, and this stage makes none.
//
// # Carried forward: the combined pass this build does not have
//
// PRD section 10 says that where commands.lint is empty, the document stage
// performs a combined pass and routes its lint findings here. This build has
// no document stage body, so no combined pass runs and nothing routes anything
// here; an empty commands.lint is therefore a stage that checked nothing, and
// it holds.
//
// No seam for that routing ships, on the same grounds the intent stage shipped
// none for inferred intent: the document body is not written, so nothing would
// read one, and surface that answers to no consumer is surface that grows
// whether or not the work arrives. Whoever writes the document stage adds the
// state key and the read it actually uses, and rewrites the empty-command
// answer below, which is where this build's version of it lives.
//
// # Where the line between an error and a finding falls
//
// At the command, as runConfiguredCheck describes. The run's own state, the
// isolated copy and the commit in it are this build's machinery, so a failure
// in any of them is an error; the command's own failure is something to
// report.
//
// # The residual gaps
//
// A configured command that reports success without having analysed anything
// is reported as a pass. That is the failure this stage's own documentation
// opens with, and it is stated twice on purpose: the recognition above covers
// a command the interpreter could not run, and covers nothing about a command
// that ran, decided on its own that it had nothing to do, and said so with a
// zero. A repository that wants that refused writes it into the command,
// which is where the knowledge of what the tool needs actually lives; this
// module's own make target is written that way.
//
// Whether the configured command analyses this change rather than the whole
// repository is not checked. The stage runs what it is given, so a repository
// configuring a repository-wide pass gets one.
//
// A command that leaves a process behind leaves it running. commandGrace says
// what that bounds and what it does not.
func Lint(deps StageDeps) pipeline.Implementation {
	return pipeline.Implementation{
		Reads: []pipeline.Key{pipeline.KeyRepository, pipeline.KeyRun},
		NewBody: func() pipeline.Body {
			return func(ctx context.Context, in pipeline.Input) (pipeline.Output, error) {
				return runStaticAnalysis(ctx, deps, in)
			}
		},
	}
}

// runStaticAnalysis is the stage body: it runs the configured analysis in the
// run's isolated copy and turns what it answered into the stage's report.
func runStaticAnalysis(ctx context.Context, deps StageDeps, in pipeline.Input) (pipeline.Output, error) {
	command := strings.TrimSpace(deps.Config.Commands.Lint)
	if command == "" {
		return pipeline.Output{Report: noLintCommandConfigured()}, nil
	}
	check, err := runConfiguredCheck(ctx, deps, in, command)
	if err != nil {
		return pipeline.Output{}, err
	}
	return pipeline.Output{Report: lintReport(in.Stage, command, check)}, nil
}

// lintReport is what the stage found.
//
// The order the cases are asked in is what keeps each one answerable. Whether
// the command reported a status at all comes first, because a status is what
// the rest read and a command that gave none carries the same -1 as one this
// build could not start. Whether the interpreter could run it comes next,
// because that status is non-zero and would otherwise be read as an analysis
// that objected to the change.
func lintReport(stage pipeline.Stage, command string, check configuredCheck) findings.Report {
	result, record := check.result, check.record
	report := findings.Report{
		Revision: check.commit,
		Tested:   []string{command},
	}
	if record.recorded() {
		report.Evidence = []findings.Evidence{{
			Label: "the full output of every attempt this run made at the configured lint command",
			Path:  record.path,
		}}
	}

	sense, couldNotRun := "", false
	if result.exited {
		sense, couldNotRun = interpreterCouldNotRun(result.code)
	}
	switch {
	case !result.exited:
		report.Summary = fmt.Sprintf(
			"The configured lint command did not report an exit status against %s, so no static "+
				"analysis was established about this change either way.", check.commit)
		report.Findings = append(report.Findings, findings.Finding{
			ID:       "lint-not-settled",
			Severity: findings.SeverityWarning,
			Action:   findings.ActionAsk,
			Description: fmt.Sprintf(
				"The configured lint command did not report an exit status of its own, so it "+
					"established neither a clean analysis nor a violation. Approving carries the run "+
					"past a check that answered nothing; skipping records the stage as skipped; "+
					"cancelling ends the run.\n\ncommand: %s\ncommit: %s\n%s\n\n%s",
				command, check.commit, notSettledReason(result), checkOutputSection(result, record)),
		})

	case couldNotRun:
		report.Summary = fmt.Sprintf(
			"The configured lint command could not be run against %s, so no static analysis ran and "+
				"this stage reports none: %q exited %d, and %s.",
			check.commit, command, result.code, sense)
		report.Findings = append(report.Findings, findings.Finding{
			ID:       "lint-could-not-run",
			Severity: findings.SeverityError,
			Action:   findings.ActionAsk,
			Description: fmt.Sprintf(
				"The configured lint command could not be run, so this change has not been analysed. "+
					"It is reported as a question rather than as something to fix because the change "+
					"is not what is wrong: a fix round is an agent editing this change, and editing it "+
					"would not install a missing tool. Installing what the command names, or "+
					"configuring a command this machine can run, is what lets the stage answer.\n\n"+
					"Approving carries the run past a check that never ran; skipping records the stage "+
					"as skipped; cancelling ends the run.\n\ncommand: %s\nexit status: %d\ncommit: %s\n"+
					"what that status means here: %s\n\n%s",
				command, result.code, check.commit, sense, checkOutputSection(result, record)),
		})

	case result.code == 0:
		report.Summary = fmt.Sprintf(
			"The configured lint command reported no violations against %s: %q exited zero in the "+
				"run's isolated copy.", check.commit, command)

	default:
		report.Summary = fmt.Sprintf(
			"The configured lint command reported violations against %s: %q exited %d in the run's "+
				"isolated copy.", check.commit, command, result.code)
		report.Findings = append(report.Findings, findings.Finding{
			ID:       "lint-violation",
			Severity: findings.SeverityError,
			Action:   findings.ActionFix,
			Description: fmt.Sprintf(
				"The configured lint command reported violations, so this change does not pass the "+
					"static analysis this repository holds it to.\n\ncommand: %s\nexit status: %d\n"+
					"commit: %s\n\n%s",
				command, result.code, check.commit, checkOutputSection(result, record)),
		})
	}
	if !record.recorded() {
		report.Findings = append(report.Findings, record.unrecordedNote(stage))
	}
	return report
}

// noLintCommandConfigured is the report for a run whose configuration names no
// lint command.
//
// PRD section 10 makes commands.lint empty by default and gives that case to
// the document stage's combined pass. This build has no document stage body,
// so nothing performs that pass and nothing routes findings here, which leaves
// a stage that analysed nothing. The finding is an ask for that reason: a note
// would let a run advance past a stage that never ran, which is the one thing
// this stage may not do.
//
// It reads no state and touches no filesystem, so a run with no command
// configured holds here whatever else is wrong with the machine. The answer
// does not depend on the isolated copy, so failing to open one would replace a
// hold a person can act on with an error they cannot.
func noLintCommandConfigured() findings.Report {
	return findings.Report{
		Summary: "No lint command is configured and this build performs no combined pass in its " +
			"place, so this stage ran no static analysis and established nothing about this change.",
		Findings: []findings.Finding{{
			ID:       "lint-no-command",
			Severity: findings.SeverityWarning,
			Action:   findings.ActionAsk,
			Description: "This repository configures no lint command. Where commands.lint is empty " +
				"the specification has the document stage perform a combined pass and route its lint " +
				"findings here, and this build has no document stage body, so that pass does not run " +
				"and nothing reaches this stage from it. Nothing analysed this change.\n\n" +
				"Setting commands.lint on the default branch is what gives this stage something to " +
				"run; it executes shell, so it belongs to that branch and never to the branch under " +
				"validation. Approving carries the run past a stage that checked nothing; skipping " +
				"records it as skipped; cancelling ends the run.",
		}},
	}
}
