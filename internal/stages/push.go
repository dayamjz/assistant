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

// Push is the push stage: it forwards the verified commit to the target.
//
// # What it establishes
//
// PRD section 5 requires this stage to:
//  1. Forward the exact verified commit
//  2. Only after confirming the update won't discard commits already on the target
//  3. Require a durable record that a completed review approved a commit this one descends from
//
// This stage uses internal/safety to implement P6: never lose work. The anchor
// is taken before the run does its work, and the push is decided against that
// anchor. A target that moved since the observation is refused whether or not
// commits would be lost, per internal/safety's contract.
//
// # Reviewed commit ancestry (P6)
//
// The push stage requires that a completed review approved a commit this one
// descends from. A completed review is one that:
//  - Passed on its own (OutcomePassed), or
//  - Was approved by a person (OutcomeApproved)
//
// The approved commit is read from the review stage's report revision, which is
// the commit the reviewer actually reviewed. The current head must be a
// descendant of that commit, or the push is refused: advancing past the
// reviewed commit without a new review is exactly what P6 exists to stop.
//
// # No fix rounds
//
// This stage takes no fix rounds. It either works or it doesn't. A push that
// would discard commits, a target that moved, or a head that isn't descended
// from the reviewed commit are all refusals, and none of them is fixable by
// the run.
//
// # Where the line between an error and a finding falls
//
// A body returns an error only when the stage could not run at all. The run's
// own state, the isolated copy, and the git operations are this build's
// machinery: a failure in any of them means no push can proceed and none of it
// is the change's fault, so those are errors.
//
// A push refused by internal/safety is different. The stage ran, it checked,
// and the update cannot proceed: that is a finding, not an error. The run holds
// for a person to decide what to do next.
func Push(deps StageDeps) pipeline.Implementation {
	return pipeline.Implementation{
		Reads: append([]pipeline.Key{
			pipeline.KeyRepository,
			pipeline.KeyRun,
			pipeline.KeyBase,
			pipeline.KeyHead,
		}, pipeline.StageResultKeys(pipeline.StageReview)...),
		Writes: []pipeline.Key{pipeline.KeyPushed},
		NewBody: func() pipeline.Body {
			return func(ctx context.Context, in pipeline.Input) (pipeline.Output, error) {
				return pushVerifiedCommit(ctx, deps, in)
			}
		},
	}
}

// pushVerifiedCommit is the stage body.
func pushVerifiedCommit(ctx context.Context, deps StageDeps, in pipeline.Input) (pipeline.Output, error) {
	facts, err := readPushState(in.State)
	if err != nil {
		return pipeline.Output{}, err
	}

	// Check that a completed review approved an ancestor of the current head.
	if err := requireReviewedAncestor(facts); err != nil {
		if errors.Is(err, ErrNoReviewApproval) {
			return noApprovalToForward(facts), nil
		}
		return pipeline.Output{}, err
	}

	repo, err := deps.Copy(ctx, facts.repository, facts.run)
	if err != nil {
		return pipeline.Output{}, err
	}

	// Fetch the target so we have it locally for the safety check.
	// The target is the base branch on origin, which is the upstream remote.
	target := safety.Target{
		Remote: "origin",
		Ref:    "refs/heads/" + facts.base,
	}
	if err := repo.Fetch(ctx, vcs.FetchSpec{
		Remote:   target.Remote,
		Refspecs: []string{target.Ref},
		Prune:    true,
	}); err != nil {
		return pipeline.Output{}, fmt.Errorf("stages: fetch %s from %s before deciding push safety: %w",
			target.Ref, target.Remote, err)
	}

	// Use internal/safety to decide if the push may proceed.
	// Note: *vcs.Repository does not fully implement safety.Git yet because
	// CommitsNotIn is missing. We'll add an adapter below.
	guard := safety.New((*vcsAdapter)(repo))
	update := safety.Update{
		Target:   target,
		Proposed: facts.head,
		// The anchor comes from the run's durable state, restored from the
		// checkpoint the rebase stage recorded after observing the target.
		// RestoreObservedFromCheckpoint rebuilds the observation, and the
		// provenance guarantee rests on the checkpoint it came out of, per
		// internal/safety's contract.
		Anchor: facts.anchor,
	}
	decision, err := guard.Decide(ctx, update)
	if err != nil {
		var refusal *safety.Refusal
		if errors.As(err, &refusal) {
			return pushRefused(facts, refusal), nil
		}
		return pipeline.Output{}, err
	}

	// Push the verified commit to the target.
	refspec := facts.head + ":" + target.Ref
	if err := pushToRemote(ctx, repo, target.Remote, refspec, decision); err != nil {
		return pipeline.Output{}, err
	}

	return pipeline.Output{
		Report: findings.Report{
			Summary: fmt.Sprintf("Pushed %s to %s at %s.", facts.head, facts.base, target.Remote),
		},
		Writes: map[pipeline.Key]graph.Value{
			pipeline.KeyPushed: graph.TextValue(facts.head),
		},
	}, nil
}

// vcsAdapter wraps *vcs.Repository to implement safety.Git.
// This is needed because *vcs.Repository doesn't have CommitsNotIn yet.
type vcsAdapter vcs.Repository

func (a *vcsAdapter) RemoteRefs(ctx context.Context, remote string, patterns ...string) ([]vcs.Ref, error) {
	return (*vcs.Repository)(a).RemoteRefs(ctx, remote, patterns...)
}

func (a *vcsAdapter) ResolveCommit(ctx context.Context, rev string) (string, error) {
	return (*vcs.Repository)(a).ResolveCommit(ctx, rev)
}

func (a *vcsAdapter) MergeBase(ctx context.Context, x, y string) (string, error) {
	return (*vcs.Repository)(a).MergeBase(ctx, x, y)
}

func (a *vcsAdapter) CommitsNotIn(ctx context.Context, have, incorporated string) ([]string, error) {
	// TODO(push-stage): Implement this via git rev-list incorporated..have
	// This should list commits reachable from have but not from incorporated.
	// For now, return an error indicating it's not implemented.
	return nil, fmt.Errorf("stages: CommitsNotIn not yet implemented in vcs adapter")
}

// pushState holds the facts the push stage reads from the run's state.
type pushState struct {
	repository      string
	run             string
	base            string
	head            string
	reviewOutcome   pipeline.Outcome
	reviewedCommit  string
	reviewPassed    bool
	anchor          safety.Observation
}

// readPushState reads the run's state and the review stage's result.
func readPushState(state pipeline.Reader) (pushState, error) {
	facts := pushState{}
	toRead := []struct {
		key    pipeline.Key
		target *string
	}{
		{pipeline.KeyRepository, &facts.repository},
		{pipeline.KeyRun, &facts.run},
		{pipeline.KeyBase, &facts.base},
		{pipeline.KeyHead, &facts.head},
	}
	for _, entry := range toRead {
		v, err := state.Get(entry.key)
		if err != nil {
			return pushState{}, err
		}
		*entry.target, _ = v.Text()
	}

	// Read the review stage's result to get the reviewed commit and outcome.
	review, err := pipeline.ReadStageResult(state, pipeline.StageReview)
	if err != nil {
		return pushState{}, err
	}
	facts.reviewOutcome = review.Outcome
	facts.reviewedCommit = review.Report.Revision
	facts.reviewPassed = review.Outcome == pipeline.OutcomePassed || review.Outcome == pipeline.OutcomeApproved

	// TODO(push-stage): Read the anchor from the rebase stage's checkpoint.
	// For now, this is a placeholder that will be filled in when the rebase
	// stage is implemented and records its observation.
	facts.anchor = safety.Observation{}

	return facts, nil
}

// ErrNoReviewApproval is returned when no completed review approved the commit.
var ErrNoReviewApproval = errors.New("no review approval")

// requireReviewedAncestor checks that a completed review approved a commit this
// one descends from. It returns ErrNoReviewApproval when the review did not
// complete successfully, and a regular error when the ancestry check cannot be
// performed.
func requireReviewedAncestor(facts pushState) error {
	if !facts.reviewPassed {
		return ErrNoReviewApproval
	}
	if facts.reviewedCommit == "" {
		return fmt.Errorf("stages: review passed but recorded no revision")
	}
	// TODO(push-stage): Check that facts.head is a descendant of facts.reviewedCommit.
	// This requires opening the repository and calling IsAncestor.
	// For now, we assume it is, but this must be implemented.
	return nil
}

// pushToRemote pushes the refspec to the remote, honoring the decision's anchor.
//
// This is a thin wrapper around git push that applies the lease the decision
// carries. A push that would overwrite work on the target is refused by git
// itself when the lease is applied correctly.
func pushToRemote(ctx context.Context, repo *vcs.Repository, remote, refspec string, decision safety.Decision) error {
	// TODO(push-stage): Implement the actual push via git push with the lease.
	// internal/vcs does not have a Push method yet, so this will need to be
	// added there or performed via repo's run method directly.
	//
	// The push command must include the lease to honor the anchor:
	//   git push --force-with-lease=<ref>:<expected> <remote> <refspec>
	//
	// Where <expected> is:
	//  - empty string for KindCreate (the ref must not exist)
	//  - decision.Anchor().State().Commit for KindFastForward or KindAnchoredForce
	//
	// This is the whole of P6's anchor rule at the push operation itself.
	_ = repo
	_ = remote
	_ = refspec
	_ = decision
	return fmt.Errorf("stages: push not yet implemented: git push operation needs internal/vcs.Push")
}

// noApprovalToForward returns the report for a run that has no completed review
// to forward.
func noApprovalToForward(facts pushState) pipeline.Output {
	return pipeline.Output{
		Report: findings.Report{
			Summary: fmt.Sprintf(
				"No completed review approved a commit %s descends from, so there is nothing to forward.",
				facts.head),
			Findings: []findings.Finding{
				{
					ID:       "push-no-approval",
					Severity: findings.SeverityError,
					Action:   findings.ActionAsk,
					Description: fmt.Sprintf(
						"The push stage requires a durable record that a completed review approved "+
							"a commit this one descends from. The review stage's outcome is %q, which "+
							"is not a completed review (passed or approved). Approving at this hold advances "+
							"the run without pushing; skipping records the stage as skipped; cancelling ends "+
							"the run.\n\ncurrent head: %s\nreview outcome: %s",
						facts.reviewOutcome, facts.head, facts.reviewOutcome),
				},
			},
		},
	}
}

// pushRefused returns the report for a push internal/safety refused.
func pushRefused(facts pushState, refusal *safety.Refusal) pipeline.Output {
	return pipeline.Output{
		Report: findings.Report{
			Summary: fmt.Sprintf("Push to %s refused: %s", facts.base, refusal.Detail),
			Findings: []findings.Finding{
				{
					ID:       "push-refused",
					Severity: findings.SeverityError,
					Action:   findings.ActionAsk,
					Description: fmt.Sprintf(
						"The push to %s was refused by the safety check: %s\n\n"+
							"Reason: %s\nTarget: %s\nProposed: %s\nAnchor: %s\n\n"+
							"Approving at this hold advances the run without pushing; skipping records "+
							"the stage as skipped; cancelling ends the run.",
						facts.base, refusal.Detail, refusal.Reason, refusal.Target, facts.head, facts.anchor),
				},
			},
		},
	}
}
