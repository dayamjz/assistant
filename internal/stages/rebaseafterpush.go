package stages

import (
	"context"
	"errors"
	"fmt"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/safety"
	"github.com/dayamjz/assistant/internal/vcs"
)

// afterPush is what the rebase stage's second pass reads from the run's
// state: the branch and its base, where the run's head stands, and the commit
// the push stage forwarded.
type afterPush struct {
	branch string
	base   string
	head   string
	pushed string
}

// The identifiers of the holds the second pass raises, one per reason it does
// not rebase.
const (
	// findingBranchMovedAfterPush is the hold for a branch that no longer
	// stands where this run's push left it.
	findingBranchMovedAfterPush = "rebase-after-push-branch-moved"
	// findingHeadNotPushed is the hold for a run whose head does not carry the
	// commit it pushed.
	findingHeadNotPushed = "rebase-after-push-head-not-pushed"
	// findingNoBaseAfterPush is the hold for a base the upstream does not
	// advertise.
	findingNoBaseAfterPush = "rebase-after-push-no-base"
	// findingConflictAfterPush is the hold for a rebase that does not apply
	// cleanly.
	findingConflictAfterPush = "rebase-after-push-conflict"
	// findingNothingToRebase is the hold for a rebase that left the branch
	// where it was, or left nothing to change against the base.
	findingNothingToRebase = "rebase-after-push-nothing-to-rebase"
)

// rebaseAfterPush is the rebase stage's second pass, over a branch the push
// stage already forwarded. Rebase's doc comment says how it differs from the
// first pass and why; this is the order it does it in.
//
// The anchor comes first, before anything is fetched or rewritten, for the
// reason the first pass takes it first: it is the observation the second push
// is decided on, and it is only worth anything taken before the work. Then the
// run's head is checked to carry the pushed commit, so the commits the rebase
// replays include everything the first push put on the branch. Then the base
// is fetched and the head replayed onto it, and anything but a clean replay
// that changed something holds.
func rebaseAfterPush(ctx context.Context, copied *vcs.Repository, facts afterPush) (pipeline.Output, error) {
	target := pushTarget(facts.branch)
	anchor, err := safety.New(copied).Observe(ctx, target)
	if err == nil {
		err = safety.RequireObservedAt(anchor, facts.pushed)
	}
	if err != nil {
		var refusal *safety.Refusal
		if errors.As(err, &refusal) {
			return pipeline.Output{Report: branchMovedAfterPush(facts, refusal)}, nil
		}
		return pipeline.Output{}, fmt.Errorf("stages: observing %s before rebasing it again: %w", target, err)
	}
	anchorValue, err := encodeAnchor(anchor)
	if err != nil {
		return pipeline.Output{}, err
	}

	current, err := copied.ResolveCommit(ctx, "HEAD")
	if err != nil {
		return pipeline.Output{}, fmt.Errorf("stages: resolving HEAD before rebasing it again: %w", err)
	}
	carries := current == facts.head
	if carries {
		carries, err = copied.IsAncestor(ctx, facts.pushed, facts.head)
		if errors.Is(err, vcs.ErrRefNotFound) {
			carries, err = false, nil
		}
		if err != nil {
			return pipeline.Output{}, fmt.Errorf("stages: establishing whether %s carries the pushed commit %s: %w",
				facts.head, facts.pushed, err)
		}
	}
	if !carries {
		return pipeline.Output{Report: headNotPushed(facts, current)}, nil
	}

	present, err := remoteBranches(ctx, copied, UpstreamRemote, facts.base)
	if err != nil {
		return pipeline.Output{}, err
	}
	if facts.base == "" || !present[facts.base] {
		return pipeline.Output{Report: noBaseAfterPush(facts)}, nil
	}
	if err := copied.Fetch(ctx, vcs.FetchSpec{Remote: UpstreamRemote, Refspecs: []string{
		fmt.Sprintf("+refs/heads/%s:refs/remotes/%s/%s", facts.base, UpstreamRemote, facts.base),
	}}); err != nil {
		return pipeline.Output{}, fmt.Errorf("stages: fetching %s to rebase onto it again: %w", facts.base, err)
	}
	onto := fmt.Sprintf("refs/remotes/%s/%s", UpstreamRemote, facts.base)
	ontoCommit, err := copied.ResolveCommit(ctx, onto)
	if err != nil {
		return pipeline.Output{}, fmt.Errorf("stages: resolving %s: %w", onto, err)
	}

	if err := copied.Rebase(ctx, vcs.RebaseSpec{Onto: onto}); err != nil {
		if !errors.Is(err, vcs.ErrRebaseConflict) {
			return pipeline.Output{}, fmt.Errorf("stages: rebasing %s onto %s again: %w", facts.head, onto, err)
		}
		// The copy is returned to the commit it stood on, so nothing the run
		// holds is left half replayed and a later stage reads the branch as
		// pushed.
		if err := copied.RebaseAbort(ctx); err != nil {
			return pipeline.Output{}, fmt.Errorf("stages: abandoning the rebase of %s onto %s: %w", facts.head, onto, err)
		}
		return pipeline.Output{Report: conflictAfterPush(facts, ontoCommit)}, nil
	}
	rebased, err := copied.ResolveCommit(ctx, "HEAD")
	if err != nil {
		return pipeline.Output{}, fmt.Errorf("stages: resolving HEAD after rebasing again: %w", err)
	}
	changes, err := copied.ChangedFiles(ctx, ontoCommit, rebased)
	if err != nil {
		return pipeline.Output{}, fmt.Errorf("stages: checking for changes after rebasing again: %w", err)
	}
	if rebased == facts.head || len(changes) == 0 {
		out := pipeline.Output{Report: nothingToRebase(facts, ontoCommit, rebased, len(changes))}
		if rebased != facts.head {
			// The replay applied and the copy stands on its result, so the
			// run's head says so rather than naming a commit the copy has
			// left. No anchor is written, so nothing pushes it.
			out.Writes = map[pipeline.Key]graph.Value{pipeline.KeyHead: graph.TextValue(rebased)}
		}
		return out, nil
	}

	return pipeline.Output{
		Report: findings.Report{
			Summary: fmt.Sprintf("Rebased %s, the commit this run pushed to %s, onto %s at %s, and the rebase "+
				"applied cleanly: %s is the new head, %d file(s) changed against the base. The review, the "+
				"checks before push, and a push anchored on %s follow before the pull request is checked again.",
				facts.pushed, facts.branch, facts.base, ontoCommit, rebased, len(changes), facts.pushed),
			Revision: rebased,
		},
		Writes: map[pipeline.Key]graph.Value{
			pipeline.KeyHead:       graph.TextValue(rebased),
			pipeline.KeyPushAnchor: anchorValue,
		},
	}, nil
}

// approvingAfterPush is what every hold of the second pass says the three
// answers do. None of them records an anchor, so the push stage refuses to
// update the branch on this pass whichever is given.
const approvingAfterPush = "Approving or skipping at this hold carries the run on without rebasing: the " +
	"review and the checks before push run again over the branch as it stands, and the push stage " +
	"refuses to update the branch, because this pass recorded no anchor for it. Cancelling ends the run."

// branchMovedAfterPush is the hold for a branch that does not stand where this
// run's push left it. It relays the refusal as internal/safety rendered it.
func branchMovedAfterPush(facts afterPush, refusal *safety.Refusal) findings.Report {
	return findings.Report{
		Summary: fmt.Sprintf("%s does not stand at %s, the commit this run pushed, so it was not rebased again: %s",
			facts.branch, facts.pushed, refusal.Detail),
		Findings: []findings.Finding{{
			ID:       findingBranchMovedAfterPush,
			Severity: findings.SeverityError,
			Action:   findings.ActionAsk,
			Description: fmt.Sprintf("The code host reported the pull request conflicting with its base, and this "+
				"run rebases a branch it pushed only while the branch still holds nothing but what it pushed. "+
				"%s\n\nreason: %s\npushed: %s\n\nA second push anchored on the branch as it stands would drop "+
				"whatever reached it after this run's push, so this pass records no anchor. %s",
				refusal.Error(), refusal.Reason, facts.pushed, approvingAfterPush),
		}},
	}
}

// headNotPushed is the hold for a run whose head is not the commit its copy
// stands on, or does not carry the commit it pushed, so a rebase of it would
// not replay what the branch holds.
func headNotPushed(facts afterPush, current string) findings.Report {
	return findings.Report{
		Summary: fmt.Sprintf("The run's head %s does not carry %s, the commit this run pushed, so it was not "+
			"rebased again.", facts.head, facts.pushed),
		Findings: []findings.Finding{{
			ID:       findingHeadNotPushed,
			Severity: findings.SeverityError,
			Action:   findings.ActionAsk,
			Description: fmt.Sprintf("Rebasing a branch this run pushed replays the run's head onto the base, and "+
				"that is only the branch's own history when the head descends from the commit the push forwarded "+
				"and is what the run's copy stands on.\n\nrun head: %s\ncopy stands on: %s\npushed: %s\n\n%s",
				facts.head, current, facts.pushed, approvingAfterPush),
		}},
	}
}

// noBaseAfterPush is the hold for a base the upstream does not advertise,
// which leaves nothing to rebase onto.
func noBaseAfterPush(facts afterPush) findings.Report {
	return findings.Report{
		Summary: fmt.Sprintf("%s does not advertise the base %q, so %s was not rebased again.",
			UpstreamRemote, facts.base, facts.branch),
		Findings: []findings.Finding{{
			ID:       findingNoBaseAfterPush,
			Severity: findings.SeverityError,
			Action:   findings.ActionAsk,
			Description: fmt.Sprintf("The code host reported the pull request conflicting with its base %q, and "+
				"the upstream does not advertise that branch, so there is nothing for this run to rebase onto. %s",
				facts.base, approvingAfterPush),
		}},
	}
}

// conflictAfterPush is the hold for a rebase that did not apply cleanly. The
// run does not resolve it, and the report says so and how a person moves the
// work on.
func conflictAfterPush(facts afterPush, onto string) findings.Report {
	return findings.Report{
		Summary: fmt.Sprintf("Rebasing %s onto %s at %s stopped on a conflict, so it was abandoned and the branch "+
			"stays at %s. This run does not resolve conflicting content after the push.",
			facts.head, facts.base, onto, facts.pushed),
		Findings: []findings.Finding{{
			ID:       findingConflictAfterPush,
			Severity: findings.SeverityError,
			Action:   findings.ActionAsk,
			Description: fmt.Sprintf("The code host reported the pull request conflicting with %s, and replaying "+
				"%s onto %s does not apply cleanly. Resolving it means deciding what the conflicting content "+
				"should be, which this run never decides for you after the push, so the rebase was abandoned and "+
				"nothing was rewritten. Rebase the branch onto %s yourself and push it through the gate: that "+
				"starts a new run, which reviews the result before it reaches the pull request. %s",
				facts.base, facts.head, onto, facts.base, approvingAfterPush),
		}},
	}
}

// nothingToRebase is the hold for a rebase that left the branch where it
// stood, or left no difference from the base. Either way a pull request the
// code host calls conflicting is still open, and this run has nothing to push
// that would change that.
func nothingToRebase(facts afterPush, onto, rebased string, changed int) findings.Report {
	return findings.Report{
		Summary: fmt.Sprintf("Rebasing %s onto %s at %s left %s with %d file(s) changed against the base, so there "+
			"is nothing for a second push to send.", facts.head, facts.base, onto, rebased, changed),
		Findings: []findings.Finding{{
			ID:       findingNothingToRebase,
			Severity: findings.SeverityWarning,
			Action:   findings.ActionAsk,
			Description: fmt.Sprintf("The code host reported the pull request conflicting with %s, and rebasing "+
				"onto it either left the branch where it was or left nothing the base does not already hold. A "+
				"second push would not change what the code host reads, so the run stops here rather than "+
				"reporting a pass past an open pull request.\n\nbefore: %s\nafter: %s\n\n%s",
				facts.base, facts.head, rebased, approvingAfterPush),
		}},
	}
}
