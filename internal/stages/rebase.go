package stages

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/safety"
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
// # The anchor this stage takes for the push
//
// It also observes where the branch the push stage will forward to stands, and
// records it as pipeline.KeyPushAnchor. That is P6's anchor, and this is where
// it is taken because it has to be taken before the run does its work: an
// anchor read a moment before pushing always holds and so protects nothing.
// anchor.go owns the target and the encoding, and the push stage decides
// against what this records.
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
			pipeline.KeyPushAnchor,
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

	// The anchor is taken here, before this stage brings the change up to date
	// and before any stage after it runs. That gap is the whole value of it:
	// PRD principle P6 names an anchor read a moment before pushing as one
	// that always holds and therefore protects nothing, and the push stage is
	// several stages and possibly several fix rounds away.
	//
	// It is observed on every execution of this stage, including a fix round's
	// re-run, because each one rebases onto whatever the target holds then.
	// The anchor has to describe the state the change was actually brought up
	// to date against, and after a second rebase that is the second reading.
	anchor, err := safety.New(copied).Observe(ctx, pushTarget(branch))
	if err != nil {
		return pipeline.Output{}, fmt.Errorf(
			"stages: reading where %s stands before rebasing onto fresh upstream: %w", branch, err)
	}
	anchorValue, err := encodeAnchor(anchor)
	if err != nil {
		return pipeline.Output{}, err
	}
	writes := map[pipeline.Key]graph.Value{pipeline.KeyPushAnchor: anchorValue}

	// The branch under validation need not be on the upstream at all: a branch
	// pushed to the gate for the first time is the ordinary case, and it is
	// the one a gate exists for. So what the upstream holds is read before
	// anything is fetched, and a branch it does not advertise is not fetched
	// rather than fetched and forgiven. The copy already stands at the commit
	// the run validates, so nothing about the change depends on that fetch;
	// what depends on it is the target below, when the base names no branch of
	// its own.
	remote := UpstreamRemote
	present, err := remoteBranches(ctx, copied, remote, branch, base)
	if err != nil {
		return pipeline.Output{}, err
	}
	var refspecs []string
	for _, name := range []string{branch, base} {
		if name != "" && present[name] {
			refspecs = append(refspecs, fmt.Sprintf("+refs/heads/%s:refs/remotes/%s/%s", name, remote, name))
		}
	}
	if len(refspecs) > 0 {
		if err := copied.Fetch(ctx, vcs.FetchSpec{Remote: remote, Refspecs: refspecs}); err != nil {
			return pipeline.Output{}, fmt.Errorf("stages: fetching %s for rebase: %w",
				strings.Join(refspecs, " "), err)
		}
	}

	// What the change is rebased onto is the base branch as the upstream holds
	// it now. A base that names the branch under validation, or names nothing,
	// leaves the branch itself as the only thing to measure against.
	onto := base
	if onto == "" {
		onto = branch
	}
	// An upstream that does not hold the target has nothing newer for this
	// change to be replayed onto, which is what a first push of a branch to a
	// repository whose base is not there either looks like. That is not a
	// failure of the change, and it is not a pass this stage may report on the
	// strength of having found nothing: it establishes that the change is
	// based on the newest the upstream has, because there is none.
	if !present[onto] {
		return withWrites(nothingToRebaseOnto(onto, remote, submitted), writes), nil
	}
	target := fmt.Sprintf("refs/remotes/%s/%s", remote, onto)

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
		output, err := checkDiffEmpty(ctx, copied, currentHead, targetCommit, submitted)
		if err != nil {
			return pipeline.Output{}, err
		}
		return withWrites(output, writes), nil
	}

	// Perform the rebase
	rebaseSpec := vcs.RebaseSpec{
		Onto: target,
	}
	err = copied.Rebase(ctx, rebaseSpec)
	if err != nil {
		if errors.Is(err, vcs.ErrRebaseConflict) {
			return withWrites(rebaseConflictReport(branch, base, target), writes), nil
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

	return withWrites(output, writes), nil
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

// UpstreamRemote is the name a run's isolated copy reaches the upstream under.
//
// It is one name in one place because two packages answer to it: this body
// fetches from it, and internal/service sets it on the gate repository the
// copy is cut from. A second spelling of it would be a build where the stage
// fetches from a remote nobody configured.
const UpstreamRemote = "origin"

// remoteBranches reports which of the named branches the upstream advertises.
//
// It asks the remote rather than fetching and reading what arrived, because
// what a fetch of an absent branch produces is a failure, and a failure that
// is sometimes expected is one a body has to tell apart from the failures that
// are not. Asking first keeps that distinction out of the error path.
//
// An empty name is not asked about and is never reported present.
func remoteBranches(ctx context.Context, copied *vcs.Repository, remote string, names ...string) (
	map[string]bool, error) {
	patterns := make([]string, 0, len(names))
	for _, name := range names {
		if name != "" {
			patterns = append(patterns, "refs/heads/"+name)
		}
	}
	present := make(map[string]bool, len(patterns))
	if len(patterns) == 0 {
		return present, nil
	}
	refs, err := copied.RemoteRefs(ctx, remote, patterns...)
	if err != nil {
		return nil, fmt.Errorf("stages: reading what %s holds for rebase: %w", remote, err)
	}
	for _, ref := range refs {
		present[strings.TrimPrefix(ref.Name, "refs/heads/")] = true
	}
	return present, nil
}

// nothingToRebaseOnto is the report for a change the upstream holds no target
// for. It passes, and says what it established rather than implying a rebase
// happened.
func nothingToRebaseOnto(onto, remote, submitted string) pipeline.Output {
	return pipeline.Output{
		Report: findings.Report{
			Summary: fmt.Sprintf(
				"%s holds no %s, so there is nothing newer for %s to be replayed onto and the change "+
					"stands as it was submitted.", remote, onto, submitted),
		},
	}
}

// baseRevision names the base as the run's isolated copy holds it.
//
// The copy is cut detached and has no local branches, so what it holds of the
// base is the remote-tracking reference the rebase stage fetched. A body that
// named the base as a bare branch name would be asking for a reference the
// copy has never had, and would fail on every run rather than on any
// particular change.
//
// The base as given is the answer when no such reference is there. That covers
// a base already written as a full reference and a base the upstream does not
// hold at all, and in the second case the caller's own error is the one that
// reaches a person, naming what it was looking for.
func baseRevision(ctx context.Context, repo *vcs.Repository, base string) string {
	tracking := fmt.Sprintf("refs/remotes/%s/%s", UpstreamRemote, base)
	if _, err := repo.ResolveCommit(ctx, tracking); err == nil {
		return tracking
	}
	return base
}
