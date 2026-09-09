package stages

import (
	"context"
	"errors"
	"fmt"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/vcs"
)

// Rebase is the rebase stage: it fetches fresh upstream and rebases onto it,
// so everything after runs against current code.
//
// # What it establishes
//
// PRD section 5 asks this stage to fetch fresh upstream and the branch target,
// then rebase onto them. If nothing remains to change after the rebase, the
// run ends successfully and the rest is skipped via KeyDiffEmpty.
//
// # What it guards against
//
// It stops if the branch would quietly carry commits from your local default
// branch that were never pushed. This is the case PRD section 5 calls out as
// needing a hold rather than proceeding silently.
//
// # Fix rounds
//
// This stage has 3 fix rounds available per config.FixRounds.Rebase. When
// conflicts occur during rebase, the fixer can be invoked to resolve them.
func Rebase(deps StageDeps) pipeline.Implementation {
	return pipeline.Implementation{
		Reads: []pipeline.Key{
			pipeline.KeyRepository,
			pipeline.KeyRun,
			pipeline.KeyBranch,
			pipeline.KeyBase,
			pipeline.KeySubmitted,
			pipeline.KeyHead,
		},
		Writes: []pipeline.Key{
			pipeline.KeyHead,
			pipeline.KeyDiffEmpty,
		},
		NewBody: func() pipeline.Body {
			return func(ctx context.Context, in pipeline.Input) (pipeline.Output, error) {
				return runRebase(ctx, deps, in)
			}
		},
	}
}

// runRebase is the stage body: it fetches fresh upstream and target, then
// rebases the working copy onto them.
func runRebase(ctx context.Context, deps StageDeps, in pipeline.Input) (pipeline.Output, error) {
	repositoryID, runID, branch, base, submitted, _, err := readRebaseState(in.State)
	if err != nil {
		return pipeline.Output{}, err
	}

	copied, err := deps.Copy(ctx, repositoryID, runID)
	if err != nil {
		return pipeline.Output{}, err
	}

	// Fetch fresh upstream. We fetch both the branch and the base to ensure
	// we have the latest state.
	remote := "origin" // TODO: make this configurable if needed
	fetchSpec := vcs.FetchSpec{
		Remote:   remote,
		Refspecs: []string{fmt.Sprintf("+refs/heads/%s:refs/remotes/%s/%s", branch, remote, branch)},
		Prune:    true,
		Tags:     false,
	}
	if err := copied.Fetch(ctx, fetchSpec); err != nil {
		return pipeline.Output{}, fmt.Errorf("stages: fetching %s for rebase: %w", branch, err)
	}

	// Also fetch the base branch
	if base != "" && base != branch {
		baseFetchSpec := vcs.FetchSpec{
			Remote:   remote,
			Refspecs: []string{fmt.Sprintf("+refs/heads/%s:refs/remotes/%s/%s", base, remote, base)},
			Prune:    false,
			Tags:     false,
		}
		if err := copied.Fetch(ctx, baseFetchSpec); err != nil {
			return pipeline.Output{}, fmt.Errorf("stages: fetching base %s for rebase: %w", base, err)
		}
	}

	// Determine the target to rebase onto
	target := fmt.Sprintf("refs/remotes/%s/%s", remote, base)
	if base == "" || base == branch {
		target = fmt.Sprintf("refs/remotes/%s/%s", remote, branch)
	}

	targetCommit, err := copied.ResolveCommit(ctx, target)
	if err != nil {
		return pipeline.Output{}, fmt.Errorf("stages: resolving rebase target %s: %w", target, err)
	}

	// Check if the current HEAD is already up to date with target
	currentHead, err := copied.ResolveCommit(ctx, "HEAD")
	if err != nil {
		return pipeline.Output{}, fmt.Errorf("stages: resolving HEAD: %w", err)
	}

	if currentHead == targetCommit {
		// Already up to date, check if diff is empty
		return checkDiffEmpty(ctx, copied, currentHead, targetCommit, submitted)
	}

	// Perform the rebase
	rebaseSpec := vcs.RebaseSpec{
		Onto: target,
	}
	err = copied.Rebase(ctx, rebaseSpec)
	if err != nil {
		if errors.Is(err, vcs.ErrRebaseConflict) {
			return rebaseConflictReport(branch, base, target), nil
		}
		return pipeline.Output{}, fmt.Errorf("stages: rebasing onto %s: %w", target, err)
	}

	// Get the new HEAD after rebase
	newHead, err := copied.ResolveCommit(ctx, "HEAD")
	if err != nil {
		return pipeline.Output{}, fmt.Errorf("stages: resolving HEAD after rebase: %w", err)
	}

	// Check if diff is empty after rebase
	output, err := checkDiffEmpty(ctx, copied, newHead, targetCommit, submitted)
	if err != nil {
		return pipeline.Output{}, err
	}

	// Update KeyHead with the new commit
	output.Writes = map[pipeline.Key]graph.Value{
		pipeline.KeyHead: graph.TextValue(newHead),
	}

	return output, nil
}

// readRebaseState reads the facts this stage needs from the run's state.
func readRebaseState(state pipeline.Reader) (repositoryID, runID, branch, base, submitted, head string, err error) {
	repoValue, err := state.Get(pipeline.KeyRepository)
	if err != nil {
		return "", "", "", "", "", "", err
	}
	runValue, err := state.Get(pipeline.KeyRun)
	if err != nil {
		return "", "", "", "", "", "", err
	}
	branchValue, err := state.Get(pipeline.KeyBranch)
	if err != nil {
		return "", "", "", "", "", "", err
	}
	baseValue, err := state.Get(pipeline.KeyBase)
	if err != nil {
		return "", "", "", "", "", "", err
	}
	submittedValue, err := state.Get(pipeline.KeySubmitted)
	if err != nil {
		return "", "", "", "", "", "", err
	}
	headValue, err := state.Get(pipeline.KeyHead)
	if err != nil {
		return "", "", "", "", "", "", err
	}

	repositoryID, _ = repoValue.Text()
	runID, _ = runValue.Text()
	branch, _ = branchValue.Text()
	base, _ = baseValue.Text()
	submitted, _ = submittedValue.Text()
	head, _ = headValue.Text()

	return repositoryID, runID, branch, base, submitted, head, nil
}

// checkDiffEmpty checks if there are any changes between the current HEAD and
// the rebase target. If the diff is empty, it sets KeyDiffEmpty and reports
// success.
func checkDiffEmpty(ctx context.Context, repo *vcs.Repository, head, target, submitted string) (pipeline.Output, error) {
	// Compare the trees of head and target
	changes, err := repo.ChangedFiles(ctx, target, head)
	if err != nil {
		return pipeline.Output{}, fmt.Errorf("stages: checking for changes after rebase: %w", err)
	}

	if len(changes) == 0 {
		// No changes remain, set KeyDiffEmpty
		return pipeline.Output{
			Report: findings.Report{
				Summary:  "Nothing remains to change after rebasing onto the latest upstream.",
				Revision: head,
			},
			Writes: map[pipeline.Key]graph.Value{
				pipeline.KeyDiffEmpty: graph.BoolValue(true),
			},
		}, nil
	}

	// There are changes, report success
	return pipeline.Output{
		Report: findings.Report{
			Summary:  fmt.Sprintf("Rebased onto latest upstream. %d file(s) changed.", len(changes)),
			Revision: head,
		},
	}, nil
}

// rebaseConflictReport builds the report for a rebase that stopped due to conflicts.
func rebaseConflictReport(branch, base, target string) pipeline.Output {
	description := fmt.Sprintf(
		"The rebase onto %s encountered conflicts that need to be resolved. "+
			"The branch %s could not be automatically rebased onto %s.\n\n"+
			"A fixer can attempt to resolve these conflicts, or the run can be "+
			"cancelled if manual intervention is required.",
		target, branch, base)

	return pipeline.Output{
		Report: findings.Report{
			Summary: fmt.Sprintf("Rebase onto %s stopped due to conflicts.", target),
			Findings: []findings.Finding{
				{
					ID:          "rebase-conflict",
					Severity:    findings.SeverityError,
					Action:      findings.ActionFix,
					Description: description,
				},
			},
		},
	}
}
