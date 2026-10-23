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
// # A branch that already holds commits the run was not given (P6)
//
// PRD section 5 has this stage rebase onto the branch target as well as the
// base. Where the branch the push will update holds commits that are not in
// the commit submitted to the run, somebody else pushed them, and the run's
// own commits are replayed onto the commit this pass observed rather than onto
// the base, so the push is a fast-forward from the anchor and their commits
// survive it (#396). That happens on its own only where their commits and the
// run's change touch different paths: a colleague's edit to a file the run
// also changed always asks a person, before anything is replayed.
// incorporateBranch owns that and its other holds: a replay that conflicts is
// abandoned and asks a person, a replay whose tree is not exactly the branch's
// tree with the run's entries is discarded and asks too, and so does a branch
// that does not contain the current base, because bringing the base in would
// rewrite those commits. The push stage's
// would-discard refusal stays the backstop for a run carried past any of them.
//
// # Fix rounds
//
// This stage has 3 fix rounds available per config.FixRounds.Rebase. When
// the first pass stops on conflicts, the rebase stays in progress and the
// copy stands on the new base with those conflicts in the working tree. The
// fix round resolves them there. It commits a resolution that differs from
// the commit the rebase is stopped on and continues, so the conflicting
// commit is not replayed again ahead of the fix. A resolution that matches
// that commit skips the step, and a step already replayed stays. A round that
// leaves any conflicted file as the conflict left it commits nothing and
// leaves the rebase stopped, so the next execution reports the conflict
// again. A round that
// leaves the index and the worktree unchanged aborts the rebase when the
// copy's HEAD is not the head the run already records, and the next
// execution starts a new one. When HEAD is that recorded head, the round
// leaves the rebase in progress so the commit stays on the working copy.
// An execution that finds a rebase this
// stage already started does not start a second one. The section below says
// what a conflict does after the push.
//
// # After the push: a second pass that rebases a branch the run pushed
//
// The checks stage sends a run back here when the code host reports its pull
// request conflicting with its base, PRD section 14 decision 7's rebase of a
// conflicted pull request, and internal/pipeline's path back from that stage
// is how. pipeline.KeyPushed is what tells this pass from the first: it names
// the commit the push stage forwarded, and it is empty until a push happened.
// rebaseAfterPush is the pass, and it differs from the first in three ways.
//
// The anchor it records is not whatever the branch holds now. The branch is
// observed, and the observation is accepted only where it finds the branch at
// the commit this run pushed, which safety.RequireObservedAt decides; a branch
// anybody else moved since the push holds for a person and records no anchor,
// so the second push cannot drop what they added. A fresh observation is still
// taken here rather than reused, so the gap P6 asks for between observing and
// pushing is a real one on this pass too.
//
// It only rebases where the rebase applies cleanly. A conflict is aborted,
// which returns the run's copy to the commit it stood on, and holds with an
// ask: this run never decides on its own what conflicting content should be
// after the push, and no automatic fix round is offered there, because the
// finding is an ask and an ask never enters one. A fix round a person asks
// for at that hold is the one way an agent changes anything here: it works
// on the branch as it stands, since the abandoned rebase leaves nothing in
// progress, and this pass then rebases its result if that applies cleanly.
// What that round does with the content is not established here; it is the
// person's request, held to the judge rule like any requested round.
//
// And it never ends the run as having nothing left to change. A rebase that
// leaves no difference from the base, or that leaves the branch where it was,
// holds instead, because a pull request still stands open on the code host
// and the run reporting success past it would say something it did not
// establish.
func Rebase(deps StageDeps) pipeline.Implementation {
	return pipeline.Implementation{
		Reads: []pipeline.Key{
			pipeline.KeyRepository,
			pipeline.KeyRun,
			pipeline.KeyBranch,
			pipeline.KeyBase,
			pipeline.KeySubmitted,
			pipeline.KeyHead,
			pipeline.KeyPushed,
			pipeline.KeyForkRepository,
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
	repositoryID, runID, branch, base, submitted, head, err := readRebaseState(in.State)
	if err != nil {
		return pipeline.Output{}, err
	}
	pushedValue, err := in.State.Get(pipeline.KeyPushed)
	if err != nil {
		return pipeline.Output{}, err
	}
	pushed, _ := pushedValue.Text()
	forkValue, err := in.State.Get(pipeline.KeyForkRepository)
	if err != nil {
		return pipeline.Output{}, err
	}
	fork, _ := forkValue.Text()

	copied, err := deps.Copy(ctx, repositoryID, runID)
	if err != nil {
		return pipeline.Output{}, err
	}
	recorder := recorderOf(deps, copied, runID, submitted)
	if pushed != "" {
		return rebaseAfterPush(ctx, copied, afterPush{branch: branch, base: base, head: head, pushed: pushed, fork: fork,
			recorder: recorder})
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
	anchor, err := safety.New(copied).Observe(ctx, pushTarget(branch, fork))
	if err != nil {
		return pipeline.Output{}, fmt.Errorf(
			"stages: reading where %s stands before rebasing onto fresh upstream: %w", branch, err)
	}
	anchorValue, err := encodeAnchor(anchor)
	if err != nil {
		return pipeline.Output{}, err
	}
	writes := map[pipeline.Key]graph.Value{pipeline.KeyPushAnchor: anchorValue}

	// A rebase left in progress is one this stage started and a fix round has
	// not finished. Starting another would stop on the directory that is
	// already there. A conflict still open is the same fix finding. One whose
	// current step is already committed is continued, and the ordinary replay
	// below then checks the result.
	if out, handled, err := settleOpenRebase(ctx, copied, branch, base); err != nil {
		return pipeline.Output{}, err
	} else if handled {
		return withWrites(out, writes), nil
	}

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
	baseCommit := ""
	if present[onto] {
		resolved, err := copied.ResolveCommit(ctx, fmt.Sprintf("refs/remotes/%s/%s", remote, onto))
		if err != nil {
			return pipeline.Output{}, fmt.Errorf("stages: resolving rebase target %s: %w", onto, err)
		}
		baseCommit = resolved
	}

	// A branch holding commits nobody gave this run is brought into the head
	// before anything is replayed onto the base, and is then the whole of
	// what this pass does: incorporateBranch says why the base cannot be
	// rebased onto past it.
	incorporated, handled, err := incorporateBranch(ctx, copied, incorporating{
		branch: branch, base: onto, submitted: submitted, anchor: anchor, baseCommit: baseCommit, recorder: recorder,
	})
	if err != nil {
		return pipeline.Output{}, err
	}
	if handled {
		return withWrites(incorporated, writes), nil
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
	targetCommit := baseCommit

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
			// The copy stays on the new base with the conflicts in the tree.
			// The fix round resolves them there; aborting first would put a
			// later fix commit after the conflicting one, and the next replay
			// would stop on that commit before it reached the fix.
			return withWrites(rebaseConflictReport(branch, base, target), writes), nil
		}
		return pipeline.Output{}, fmt.Errorf("stages: rebasing onto %s: %w", target, err)
	}

	// Get the new HEAD after rebase
	newHead, err := copied.ResolveCommit(ctx, "HEAD")
	if err != nil {
		return pipeline.Output{}, fmt.Errorf("stages: resolving HEAD after rebase: %w", err)
	}
	if err := recorder.record(ctx, currentHead, targetCommit, newHead); err != nil {
		return pipeline.Output{}, err
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

// settleOpenRebase reports a conflict this stage's rebase has already stopped
// on, or continues a rebase whose current step is already committed.
//
// handled is false when the copy has no rebase in progress, so the caller
// starts the ordinary replay. A conflicted tree is not aborted here: the fix
// round is what resolves it, on this base.
func settleOpenRebase(ctx context.Context, copied *vcs.Repository, branch, base string) (pipeline.Output, bool, error) {
	active, err := copied.RebaseStatus(ctx)
	if err != nil {
		return pipeline.Output{}, false, fmt.Errorf("stages: reading whether a rebase is in progress: %w", err)
	}
	if !active {
		return pipeline.Output{}, false, nil
	}
	onto := base
	if onto == "" {
		onto = branch
	}
	target := fmt.Sprintf("refs/remotes/%s/%s", UpstreamRemote, onto)
	changes, err := copied.Changes(ctx)
	if err != nil {
		return pipeline.Output{}, false, fmt.Errorf("stages: reading the in-progress rebase onto %s: %w", target, err)
	}
	if changes.Unmerged {
		return rebaseConflictReport(branch, base, target), true, nil
	}
	if err := copied.RebaseContinue(ctx); err != nil {
		if errors.Is(err, vcs.ErrRebaseConflict) {
			return rebaseConflictReport(branch, base, target), true, nil
		}
		return pipeline.Output{}, false, fmt.Errorf("stages: continuing the rebase onto %s: %w", target, err)
	}
	return pipeline.Output{}, false, nil
}

// rebaseConflictReport builds the fix finding for a rebase that stopped with
// conflicts while the copy stands on the new base. The fix round resolves
// those files in place.
func rebaseConflictReport(branch, base, target string) pipeline.Output {
	description := fmt.Sprintf(
		"The rebase onto %s stopped with conflicts. The copy is on that base "+
			"with the conflicts in the working tree, and the rebase is still in progress. "+
			"The branch %s could not be replayed onto %s as it stands.\n\n"+
			"Resolve the conflicted files in place. The resolution is recorded on this "+
			"base. Leave a file unchanged if its conflict cannot be resolved: while any "+
			"conflicted file is left as the conflict left it, nothing is recorded and the "+
			"rebase stays stopped. If a conflicted file's current content already is its "+
			"resolution, stage it with git add.",
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

// ForkRemote is the name a run's isolated copy reaches the fork under, when
// the run's repository record names one. It is one name in one place for
// UpstreamRemote's reason: pushTarget pushes to it and internal/service sets
// it on the gate repository the copy is cut from.
const ForkRemote = "fork"

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
