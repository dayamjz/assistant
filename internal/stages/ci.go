package stages

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/pipeline"
)

// CI is the checks stage: it watches CI and mergeability.
//
// # What it establishes
//
// PRD section 5 gives this stage two jobs: watch CI and mergeability, and fix
// failures and conflicts. This implementation reads the checks on the pull
// request's head and reports what the provider says now. It does not poll:
// when checks are still running the stage holds, and how long a run waits
// before re-reading is the caller's decision through checks_timeout.
//
// A pass establishes that every registered check settled and passed at the
// moment this invocation read them. It does not establish that they stayed
// green, that the code was reviewed by a person, or that the provider will
// merge it: those are the provider's policies and this stage reports what it
// reads rather than deciding on the provider's behalf.
//
// # The code host and the repository
//
// This stage requires both. deps.Forge is the adapter and
// pipeline.KeyForgeRepository is the repository on that host, so a run whose
// record names no repository on a host this build talks to has nothing to open
// and reports that rather than proceeding without one. The repository is
// resolved from the run's upstream URL, and forge.GitHubRepository describes
// what reaches here and what does not.
//
// The adapter needs the pull request number, which is pipeline.KeyPullRequest.
// The pull request stage writes it, so this stage reads what that stage
// recorded and assumes nothing about the provider's own numbering. An empty
// value means no pull request was created, which is a run configuration rather
// than a failure of the change, so it is reported and the stage holds.
//
// # Zero checks and the no-CI declaration
//
// An empty check list means no check is registered, and that is not the same
// as passing. This stage treats the two differently, per forge's rule: zero
// checks is VerdictNoChecks unless configuration declares that this repository
// has none, and only forge.DeclaredNoCI produces a declaration that counts.
// The key is config.KeyNoCI, and internal/config's table makes it
// trusted-only, so a pushed branch cannot set it for itself.
//
// A repository that declares no_ci and then registers a check is judged on
// that check. The declaration decides nothing once checks exist, which is
// forge.ChecksReport.Evaluate's rule and not one this stage adds.
//
// # What a fix round would do, and why there is none yet
//
// A check that failed is reported as a fix finding, so it is eligible for this
// stage's automatic fix rounds. No fixer is wired: internal/cli places
// PendingFixer, which refuses, so a fix-eligible finding reaches that and the
// run fails there naming what is missing. That is the honest answer and it is
// documented on PendingFixer rather than softened here, which is the same
// shape the test stage takes.
//
// What a fixer would do is read the check's output, decide what to change, and
// apply it. All three need an agent: the output is unstructured text, the
// decision is "edit the code so this check passes", and the change is a commit
// to the isolated copy. This stage does not call an agent and has nothing to
// ask one yet, so the fixer is the work that ships after this does.
func CI(deps StageDeps) pipeline.Implementation {
	return pipeline.Implementation{
		Reads: []pipeline.Key{pipeline.KeyForgeRepository, pipeline.KeyPullRequest},
		NewBody: func() pipeline.Body {
			return func(ctx context.Context, in pipeline.Input) (pipeline.Output, error) {
				return runChecks(ctx, deps, in)
			}
		},
	}
}

// runChecks is the stage body: it queries the pull request's checks and turns
// the verdict into a report.
func runChecks(ctx context.Context, deps StageDeps, in pipeline.Input) (pipeline.Output, error) {
	repoSpec, prNumber, err := readCIState(in.State)
	if err != nil {
		return pipeline.Output{}, err
	}

	// A run with no forge repository or no pull request cannot query checks.
	// Both are configuration rather than a failure of the change: the
	// repository comes from the run's upstream URL, and the pull request comes
	// from whether the pull request stage ran. So both are reported and the
	// stage holds rather than failing.
	if repoSpec == "" {
		return pipeline.Output{Report: noForgeRepository()}, nil
	}
	if prNumber == 0 {
		return pipeline.Output{Report: noPullRequest()}, nil
	}

	if deps.Forge == nil {
		return pipeline.Output{}, fmt.Errorf("stages: no forge adapter, so the %s stage cannot query checks", in.Stage)
	}

	provider, err := deps.Forge.Open(repoSpec)
	if err != nil {
		// A provider that cannot be opened is an error rather than a finding,
		// because it means the adapter could not run at all and no stage
		// decision is being made. The refusal is returned as-is: it already
		// names what went wrong and redacts what it read from the provider.
		return pipeline.Output{}, fmt.Errorf("stages: opening forge provider for %s: %w", repoSpec, err)
	}

	checksReport, err := provider.Checks(ctx, prNumber)
	if err != nil {
		return pipeline.Output{}, fmt.Errorf("stages: reading checks for pull request %d: %w", prNumber, err)
	}

	result := checksReport.Evaluate(forge.DeclaredNoCI(deps.Config))
	return pipeline.Output{Report: checksResultReport(result, prNumber)}, nil
}

// readCIState reads the two facts this stage needs from the run's state: which
// repository on the code host and which pull request.
func readCIState(state pipeline.Reader) (repoSpec string, prNumber int, err error) {
	repoValue, err := state.Get(pipeline.KeyForgeRepository)
	if err != nil {
		return "", 0, err
	}
	prValue, err := state.Get(pipeline.KeyPullRequest)
	if err != nil {
		return "", 0, err
	}

	repoSpec, _ = repoValue.Text()

	prText, _ := prValue.Text()
	if prText != "" {
		prNumber, err = strconv.Atoi(prText)
		if err != nil {
			return "", 0, fmt.Errorf("stages: parsing pull request number %q: %w", prText, err)
		}
		if prNumber < 1 {
			return "", 0, fmt.Errorf("stages: pull request number %d is not a positive integer", prNumber)
		}
	}

	return repoSpec, prNumber, nil
}

// noForgeRepository is the report for a run whose record does not name a
// repository on a host this build talks to.
func noForgeRepository() findings.Report {
	return findings.Report{
		Summary: "No repository on a supported code host is configured, so the checks stage cannot query CI status.",
		Findings: []findings.Finding{{
			ID:       "ci-no-repository",
			Severity: findings.SeverityWarning,
			Action:   findings.ActionAsk,
			Description: "This run's record does not name a repository on a code host this build can talk to, " +
				"so the checks stage has no provider to query and cannot establish whether CI passed. " +
				"Approving carries the run past a stage that checked nothing; skipping records it as skipped; " +
				"cancelling ends the run.",
		}},
	}
}

// noPullRequest is the report for a run that has no pull request.
func noPullRequest() findings.Report {
	return findings.Report{
		Summary: "No pull request exists, so the checks stage cannot query CI status.",
		Findings: []findings.Finding{{
			ID:       "ci-no-pull-request",
			Severity: findings.SeverityWarning,
			Action:   findings.ActionAsk,
			Description: "This run has no pull request recorded, so the checks stage has no pull request to " +
				"query checks against and cannot establish whether CI passed. This happens when the pull request " +
				"stage was skipped or when it did not create one. Approving carries the run past a stage that " +
				"checked nothing; skipping records it as skipped; cancelling ends the run.",
		}},
	}
}

// checksResultReport turns a checks result into the stage's report.
func checksResultReport(result forge.ChecksResult, prNumber int) findings.Report {
	report := findings.Report{
		Revision: result.HeadCommit,
	}

	switch result.Verdict {
	case forge.VerdictPassed:
		// Checks passed: report the good news and no findings.
		summary := fmt.Sprintf("All checks passed on pull request %d", prNumber)
		if result.Declaration.Declared() {
			summary = fmt.Sprintf("No checks are registered on pull request %d and %s says this repository has none",
				prNumber, result.Declaration)
		} else if len(result.Runs) > 0 {
			summary = fmt.Sprintf("All %d check(s) passed on pull request %d: %s",
				len(result.Runs), prNumber, checksNames(result.Runs))
		}
		report.Summary = summary

	case forge.VerdictRunning:
		// Checks are still running: hold for them to complete.
		waiting := result.Waiting()
		report.Summary = fmt.Sprintf(
			"Checks are still running on pull request %d. Waiting for %d check(s) to complete: %s",
			prNumber, len(waiting), checksNames(waiting))
		report.Findings = append(report.Findings, findings.Finding{
			ID:       "ci-running",
			Severity: findings.SeverityInfo,
			Action:   findings.ActionAsk,
			Description: fmt.Sprintf(
				"Checks are still running and have not finished. The checks stage is waiting for them to complete. "+
					"Waiting on: %s\n\nApproving carries the run forward before the checks finish; "+
					"skipping records this stage as skipped; cancelling ends the run.",
				checksNames(waiting)),
		})

	case forge.VerdictNoChecks:
		// No checks registered and no declaration: hold.
		report.Summary = fmt.Sprintf(
			"No checks are registered on pull request %d and nothing declares that this repository has none",
			prNumber)
		report.Findings = append(report.Findings, findings.Finding{
			ID:       "ci-no-checks",
			Severity: findings.SeverityWarning,
			Action:   findings.ActionAsk,
			Description: "No checks are registered on this pull request's head and configuration does not declare " +
				"that this repository has none, so the checks stage cannot establish whether CI would pass. " +
				"Checks may still register; the stage waits, bounded by checks_timeout. Approving carries the run " +
				"past unregistered checks; skipping records the stage as skipped; cancelling ends the run.",
		})

	case forge.VerdictFailed:
		// At least one check failed: report fix findings for each blocking check.
		blocking := result.Blocking()
		report.Summary = fmt.Sprintf(
			"Checks failed on pull request %d. %d check(s) did not pass: %s",
			prNumber, len(blocking), checksNames(blocking))

		for _, check := range blocking {
			name := check.Name
			if name == "" {
				name = "(unnamed check)"
			}
			description := fmt.Sprintf(
				"Check %q is %s and blocks this run. The check must pass for the stage to advance.\n\n"+
					"Check URL: %s",
				name, check.State, check.URL)
			if check.URL == "" {
				description = fmt.Sprintf(
					"Check %q is %s and blocks this run. The check must pass for the stage to advance.",
					name, check.State)
			}

			report.Findings = append(report.Findings, findings.Finding{
				ID:          "ci-check-failed",
				Severity:    findings.SeverityError,
				Action:      findings.ActionFix,
				Description: description,
			})
		}
	}

	return report
}

// checksNames renders a list of checks as a comma-separated string for a
// summary or finding.
func checksNames(runs []forge.CheckRun) string {
	if len(runs) == 0 {
		return "(none)"
	}
	parts := make([]string, 0, len(runs))
	for _, run := range runs {
		parts = append(parts, run.String())
	}
	return strings.Join(parts, ", ")
}
