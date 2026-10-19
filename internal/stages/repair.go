package stages

import (
	"context"
	"errors"
	"fmt"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/safety"
)

// The identifiers of what the checks stage reports about a repair: the note
// a repair sent back for review carries, and the hold for a branch that moved
// before the repair could be anchored on it.
const (
	// findingRepairing is the note a fix round's commit that takes the path
	// back to review carries.
	findingRepairing = "ci-fix-repairing"
	// findingRepairBranchMoved is the hold for a branch that no longer stands
	// where this run's last push left it, so the repair was not sent back.
	findingRepairBranchMoved = "ci-fix-branch-moved"
)

// repairAfterPush is the checks stage's answer to a fix round of its own that
// committed after the push: PRD section 5's repair of a red CI run, which the
// owner decided on #325 goes back through the review stage before a second
// push. It runs on the stage's next execution after the round, when the run's
// head is the round's commit and the pull request still stands on the commit
// the push stage forwarded.
//
// It observes the branch the push stage forwards to and requires the
// observation to name that commit, safety.RequireObservedAt, before anything
// else, which is the anchor rule of #262's second push. A branch anybody else
// moved since this run pushed it is refused here and held, so a repair is
// never sent on to a push that would have to adopt or drop what they pushed.
// An observation that names the commit is recorded under
// pipeline.KeyPushAnchor, and the run goes back to review with the round's
// commit through pipeline.Output.Repair. The push stage asks RequireObservedAt
// of that anchor again before it decides, and decides against a fresh read
// under a lease at the anchor, so a branch that moves between this
// observation and the second push is refused there.
//
// The observation is taken after the round rather than before it. The round
// writes only in the run's isolated copy, so nothing it does can move the
// branch, and what the rule needs is an observation at the pushed commit taken
// before the review and the push that follow it, which this is.
//
// What this does not establish. A branch that moved away and back to the
// pushed commit between the push and this observation is not seen, which is
// RequireObservedAt's own residual gap. And the repair is a commit on top of
// the pushed one only because a fix round commits on the copy's head; this
// does not check its ancestry, and the push stage's decision refuses one that
// would drop the pushed commit as it refuses any update that would discard.
func repairAfterPush(ctx context.Context, deps StageDeps, facts ciFacts) (pipeline.Output, error) {
	copied, err := deps.Copy(ctx, facts.repositoryID, facts.runID)
	if err != nil {
		return pipeline.Output{}, err
	}
	target := pushTarget(facts.branch, facts.fork)
	anchor, err := safety.New(copied).Observe(ctx, target)
	if err == nil {
		err = safety.RequireObservedAt(anchor, facts.pushed)
	}
	if err != nil {
		var refusal *safety.Refusal
		if errors.As(err, &refusal) {
			return pipeline.Output{Report: repairBranchMoved(facts, refusal)}, nil
		}
		return pipeline.Output{}, fmt.Errorf("stages: observing %s before sending a repair back for review: %w", target, err)
	}
	anchorValue, err := encodeAnchor(anchor)
	if err != nil {
		return pipeline.Output{}, err
	}
	return pipeline.Output{
		Report: repairingReport(facts),
		Repair: true,
		Writes: map[pipeline.Key]graph.Value{pipeline.KeyPushAnchor: anchorValue},
	}, nil
}

// repairingReport is the report for a fix round's commit the run sends back
// for review. It passes on its own, so internal/pipeline records
// OutcomeRepair for it and takes the path; its one finding is a note saying
// what happens next, for the pull request stage's account of the run.
func repairingReport(facts ciFacts) findings.Report {
	return findings.Report{
		Revision: facts.head,
		Summary: fmt.Sprintf("A fix round at the checks stage committed %s on top of %s, the commit pull request %d "+
			"stands on, and the run takes it back to review before it is pushed.", facts.head, facts.pushed,
			facts.pullRequest),
		Findings: []findings.Finding{{
			ID:       findingRepairing,
			Severity: findings.SeverityInfo,
			Action:   findings.ActionNote,
			Description: fmt.Sprintf("The checks failed on %s and this stage's fix round committed %s to repair them. "+
				"A commit written after the push reaches the pull request only once it is reviewed, so the run goes "+
				"back to the review stage, which reviews it in a session of its own, then through the checks before "+
				"push, and the push stage forwards it on an anchor at %s, where this stage observed %s standing. "+
				"The checks are read again on what that push forwarded.", facts.pushed, facts.head, facts.pushed,
				facts.branch),
		}},
	}
}

// repairBranchMoved is the hold for a repair whose branch does not stand where
// this run's push left it. It relays the refusal as internal/safety rendered
// it and names both commits, so the round's work has a path back to a person.
func repairBranchMoved(facts ciFacts, refusal *safety.Refusal) findings.Report {
	return findings.Report{
		Revision: facts.head,
		Summary: fmt.Sprintf("A fix round at the checks stage committed %s, and %s does not stand at %s, the commit "+
			"this run pushed, so the repair was not sent back for review: %s",
			facts.head, facts.branch, facts.pushed, refusal.Detail),
		Findings: []findings.Finding{{
			ID:       findingRepairBranchMoved,
			Severity: findings.SeverityError,
			Action:   findings.ActionAsk,
			Description: fmt.Sprintf("This stage repairs a failing check only on a branch that still holds nothing but "+
				"what this run pushed. %s\n\nreason: %s\npushed: %s\nrepair: %s\n\nA second push anchored on the "+
				"branch as it stands would drop whatever reached it after this run's push, so nothing is pushed. "+
				"To use the repair, take %s from this run's isolated copy and push it through the gate, which "+
				"starts a run that reviews it. Approving carries this run past the checks that failed; skipping "+
				"records this stage as skipped; cancelling ends the run.",
				refusal.Error(), refusal.Reason, facts.pushed, facts.head, facts.head),
		}},
	}
}
