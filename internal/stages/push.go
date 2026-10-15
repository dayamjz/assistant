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

// Push is the push stage: it forwards the verified commit to the target.
//
// # What it establishes
//
// PRD section 5 requires this stage to:
//  1. Forward the exact verified commit
//  2. Only after confirming the update won't discard commits already on the target
//  3. Require a durable record that a completed review approved a commit this one descends from
//
// # What it forwards to
//
// The branch under validation, and never the base. anchor.go's pushTarget owns
// that and says why: a run that pushed its commit to the base would have put
// the change on the base branch itself, past the pull request stage that is
// supposed to propose it.
//
// # The anchor (P6)
//
// This stage decides through internal/safety, and the anchor it decides on is
// the observation the rebase stage took before the run did its work. It is not
// read here, and that is the point: an anchor read a moment before pushing
// always holds and therefore protects nothing, which PRD principle P6 names
// outright. pipeline.KeyPushAnchor is what carries it across the stages in
// between and across a restart.
//
// A run with no anchor recorded is refused rather than pushed on a fresh read.
// So is an anchor describing a different reference. Both hold for a person.
//
// The update is performed under the lease the decision carries, which leaseFor
// derives from the decision rather than choosing beside it. A target that moved
// since the observation is refused twice over: by internal/safety against its
// fresh read, and by the remote against the lease.
//
// # A second push (P6)
//
// A run the checks stage sent back to rebase reaches this stage again with
// pipeline.KeyPushed already naming the commit it forwarded the first time,
// and the branch it now updates is one this run rewrote. The anchor for that
// update has to be an observation of the branch standing at that commit,
// which is what the rebase stage's second pass records and the only one it
// records. This stage asks safety.RequireObservedAt of the anchor before it
// decides, so an anchor from the first pass, taken before the run pushed
// anything, is refused here and held rather than reaching a decision. That
// anchor reaches here when the second pass held and a person carried the run
// on, and the decision would refuse most such anchors anyway, since the first
// push moved the branch off them. The one it would not is a branch somebody
// put back where it stood before the run, which the old anchor matches again;
// this rule is what refuses that one. The
// decision over an accepted anchor is the usual one against a fresh read, so
// a branch that moved between the rebase stage's observation and this push is
// refused as any moved target is, and the commits an anchored force reports
// dropping are ones the branch held when the first push left it.
//
// # Reviewed commit ancestry (P6)
//
// The push stage requires that a completed review approved a commit this one
// descends from. A completed review is one that:
//   - Passed on its own (OutcomePassed), or
//   - Was approved by a person (OutcomeApproved)
//
// The approved commit is the review stage's report revision, which is the
// commit the reviewer read. The head is checked to be a descendant of it with
// vcs.IsAncestor, and a head that is not is refused: forwarding it would push
// work no completed review looked at. A review that completed while naming no
// revision is refused too, because a comparison against nothing is a check
// that passes by looking at nothing.
//
// # Commits written after the review (P5)
//
// Descent alone would forward lines no review read. Review's own fix round is
// re-reviewed, because the review stage runs again after it and its report
// revision is the commit that round made; an automatic round of the test or
// lint stage, or a round a person asked for at the test, document, or lint
// stage's hold, is re-verified only by its own stage, and the head it leaves descends
// from the reviewed commit while carrying the round's work. PRD section 14
// records the decision that closes this: when the head is not the reviewed
// commit, this stage reviews the commits between the two before anything is
// pushed, in a fresh session, as author code, with each stage's last round
// summary handed over as a claim. reviewLaterCommits is that review and it is
// the review stage's own, reviewRead, measured from the reviewed commit.
//
// A review of them that is anything but notes holds the stage with the
// review's findings and pushes nothing. A clean one lets the push proceed, and
// the head is then what pipeline.KeyApproved records, because a completed
// review has now read every commit in it. A head that is the reviewed commit
// costs no second review.
//
// The decision-model checks come with reviewRead and only ever add, on the
// terms decision.go states: a check that cannot run, on a failed call or an
// unreadable answer, leaves a note that holds nothing, so a clean agent
// review still lets the push proceed, and only a flag in the enforce mode,
// which is an ask, can hold it.
//
// What this does not establish:
//   - A person approving the review stage's hold approved the commit that
//     stage read, not the later commits. Those are reviewed here like any
//     other, and a person is asked again only if that review raises something.
//   - The checks stage's fix rounds run after this stage and are not reviewed
//     here. Nothing in this build pushes the commits they make, and the
//     checks stage holds on one rather than reading checks it cannot be in.
//     A rebase the checks stage sends the run back for is different: it
//     comes back through the review stage, and through here, before it is
//     pushed.
//   - A judge path a later round deleted, which judge.go already held that
//     round's stage for, is reported again by this review's judge lens: it
//     cannot tell which edits a person already accepted, so it asks again
//     rather than assuming.
//   - The rounds' summaries reach the reviewer as the last round of each stage
//     wrote them; earlier rounds of the same stage are represented only by the
//     diff, because the fix key holds one summary.
//
// # No fix rounds
//
// This stage takes no fix rounds. It either works or it doesn't. A push that
// would discard commits, a target that moved, or a head that isn't descended
// from the reviewed commit are all refusals, and none of them is fixable by
// the run. A fix finding the review of later commits raises holds here as
// well, because internal/pipeline sends a finding at a stage with no fixer to
// a person.
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
	reads := append([]pipeline.Key{
		pipeline.KeyRepository,
		pipeline.KeyRun,
		pipeline.KeyBranch,
		pipeline.KeyBase,
		pipeline.KeyHead,
		pipeline.KeyPushAnchor,
		pipeline.KeyPushed,
		pipeline.KeyIntent,
		pipeline.KeyIntentSupplied,
	}, pipeline.StageResultKeys(pipeline.StageReview)...)
	for _, stage := range fixingAfterReview() {
		reads = append(reads, stage.FixKey())
	}
	return pipeline.Implementation{
		Reads:  reads,
		Writes: []pipeline.Key{pipeline.KeyPushed, pipeline.KeyApproved},
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

	repo, err := deps.Copy(ctx, facts.repository, facts.run)
	if err != nil {
		return pipeline.Output{}, err
	}

	// A run whose review did not complete has nothing to forward, and that is
	// read before anything else: it costs no remote read and it is the
	// condition PRD section 5 states outright.
	if !facts.reviewPassed {
		return noApprovalToForward(facts), nil
	}
	if facts.reviewedCommit == "" {
		return noReviewedCommit(facts), nil
	}
	descends, err := repo.IsAncestor(ctx, facts.reviewedCommit, facts.head)
	if errors.Is(err, vcs.ErrRefNotFound) {
		// The comparison could not be made, which P6 answers the same way it
		// answers a fact that cannot be verified: refuse and report. Treating
		// it as an error would fail the run over the one condition the
		// principle names a refusal for.
		return reviewedCommitUnreadable(facts, err), nil
	}
	if err != nil {
		return pipeline.Output{}, fmt.Errorf(
			"stages: establishing whether %s descends from the reviewed commit %s: %w",
			facts.head, facts.reviewedCommit, err)
	}
	if !descends {
		return notDescendedFromReview(facts), nil
	}

	// The anchor is the run's own observation, taken by the rebase stage
	// before the run did its work. A run that has none cannot be pushed: the
	// only anchor available here would be a read taken now, which is the one
	// P6 names as protecting nothing.
	if !facts.anchorRecorded {
		return noAnchorToPushOn(facts), nil
	}
	target := pushTarget(facts.branch)
	if facts.anchor.Target() != target {
		return anchorForAnotherTarget(facts, target), nil
	}
	// A second push is anchored only where the first one left the branch.
	// Asking it here rather than trusting the stage that recorded the anchor
	// is what refuses a first-pass anchor, which predates the first push.
	if facts.pushed != "" {
		if err := safety.RequireObservedAt(facts.anchor, facts.pushed); err != nil {
			var refusal *safety.Refusal
			if errors.As(err, &refusal) {
				return pushRefused(facts, target, refusal), nil
			}
			return pipeline.Output{}, err
		}
	}

	// Commits this run wrote after the review are reviewed before anything is
	// pushed, and only when there are some: a head that is the reviewed commit
	// costs no second review. It comes after the refusals that need no agent
	// and before the target is read, so the decision below is made on a read
	// taken after the review rather than before it.
	approved := facts.reviewedCommit
	var later []findings.Finding
	if facts.head != facts.reviewedCommit {
		out, err := reviewLaterCommits(ctx, deps, repo, facts)
		if err != nil {
			return pipeline.Output{}, err
		}
		if !out.Report.Normalize().AllNotes() {
			return laterCommitsNotApproved(facts, out.Report), nil
		}
		approved, later = facts.head, out.Report.Findings
	}

	// The target has to be in the local repository for the reachability
	// comparisons internal/safety makes, and it is fetched rather than assumed
	// because the run may never have had a reason to hold it.
	if err := fetchTargetForDecision(ctx, repo, target); err != nil {
		return pipeline.Output{}, err
	}

	decision, err := safety.New(repo).Decide(ctx, safety.Update{
		Target:   target,
		Proposed: facts.head,
		Anchor:   facts.anchor,
	})
	if err != nil {
		var refusal *safety.Refusal
		if errors.As(err, &refusal) {
			return pushRefused(facts, target, refusal), nil
		}
		return pipeline.Output{}, err
	}

	if err := repo.Push(ctx, vcs.PushSpec{
		Remote: target.Remote,
		Commit: decision.Proposed(),
		Ref:    decision.Target().Ref,
		Lease:  leaseFor(decision),
	}); err != nil {
		return pipeline.Output{}, fmt.Errorf("stages: forwarding %s to %s: %w",
			decision.Proposed(), decision.Target(), err)
	}

	report := pushedReport(decision)
	report.Findings = append(report.Findings, later...)
	return pipeline.Output{
		Report: report,
		Writes: map[pipeline.Key]graph.Value{
			pipeline.KeyPushed:   graph.TextValue(decision.Proposed()),
			pipeline.KeyApproved: graph.TextValue(approved),
		},
	}, nil
}

// fixingAfterReview is the stages that can author a commit between the review
// stage reading the change and this stage forwarding it: every stage after
// review and before push. Each is pipeline.Stage.Requestable, so a person may
// ask for a round at its hold, and some also take automatic rounds; both
// kinds write the stage's fix key, last write winning, so what this stage
// reads there is the account of that stage's last round of either kind. A
// stage that took no round leaves the key empty and contributes no claim, so
// the list is every such stage rather than a guess at which ones took rounds.
func fixingAfterReview() []pipeline.Stage {
	var out []pipeline.Stage
	for _, stage := range pipeline.Order() {
		if stage > pipeline.StageReview && stage < pipeline.StagePush {
			out = append(out, stage)
		}
	}
	return out
}

// reviewLaterCommits reviews the commits between the one the review stage read
// and the head this stage would forward, which is PRD principle P5 applied to
// the fix rounds that ran after review.
//
// It is the review stage's own review, reviewRead, measured from the reviewed
// commit rather than from where the branch left its base, so every rule that
// review is held to applies here unchanged. P4 holds for the same reason it
// holds there: the agent is the StageAgent this stage was handed, which runs
// with no session, and nothing of the rounds crosses but the commits and what
// each stage's last round wrote in its fix key, which reaches the reviewer as
// claims.
//
// The caller has established that the head descends from the reviewed commit,
// so the change measured here is exactly the commits no review has read.
func reviewLaterCommits(ctx context.Context, deps StageDeps, repo *vcs.Repository, facts pushState) (pipeline.Output, error) {
	review := reviewFacts{
		repository:  facts.repository,
		run:         facts.run,
		branch:      facts.branch,
		base:        facts.base,
		head:        facts.head,
		intent:      facts.intent,
		supplied:    facts.supplied,
		since:       facts.reviewedCommit,
		laterRounds: facts.laterRounds,
	}
	change, err := readChange(ctx, repo, review, deps.Config.IgnorePatterns)
	return reviewRead(ctx, deps, repo, review, change, err)
}

// laterCommitsNotApproved is the report for commits this run authored after
// its review that the review of them did not approve as they stand.
//
// Every finding that review reported is carried over as it was, so a fix or
// an ask the reviewer raised is what holds this stage: it takes no fix rounds,
// and internal/pipeline holds a stage with no fixer for any finding that is
// not a note. A summary saying what was not pushed and why is the one thing
// added.
func laterCommitsNotApproved(facts pushState, review findings.Report) pipeline.Output {
	review.Summary = fmt.Sprintf(
		"%s carries commits this run's own fix rounds wrote after the review read %s, and the "+
			"review of those commits did not approve them as they stand, so nothing was pushed. "+
			"Approving at this hold advances the run without pushing; skipping records the stage "+
			"as skipped; cancelling ends the run. The review said: %s",
		facts.head, facts.reviewedCommit, review.Summary)
	return pipeline.Output{Report: review}
}

// leaseFor is the lease a decision has to be performed under.
//
// It is derived from the decision rather than chosen beside it, which is what
// keeps the update this performs the one that was allowed. A decision to
// create is leased on the target still being absent; every other decision is
// leased on the target still naming the commit the run observed.
//
// internal/safety says the same thing in its own words: the permission is
// inseparable from its anchor, and performing the update without leasing on it
// is performing a different update than the one that was allowed.
func leaseFor(decision safety.Decision) vcs.Lease {
	state := decision.Anchor().State()
	if !state.Exists {
		return vcs.LeaseAbsent()
	}
	return vcs.LeaseAt(state.Commit)
}

// fetchTargetForDecision brings the target into the local repository, so the
// reachability questions internal/safety asks about it can be answered.
//
// A target the remote does not hold is not an error here. internal/safety
// reads the remote itself and decides what an absent target means; what this
// has to avoid is failing the stage over a fetch of something that was never
// there, which is the ordinary case for a branch this run is the first to
// push.
func fetchTargetForDecision(ctx context.Context, repo *vcs.Repository, target safety.Target) error {
	refs, err := repo.RemoteRefs(ctx, target.Remote, target.Ref)
	if err != nil {
		return fmt.Errorf("stages: reading %s before deciding whether the push may proceed: %w", target, err)
	}
	if len(refs) == 0 {
		return nil
	}
	if err := repo.Fetch(ctx, vcs.FetchSpec{
		Remote:   target.Remote,
		Refspecs: []string{"+" + target.Ref + ":refs/remotes/" + target.Remote + "/" + strings.TrimPrefix(target.Ref, "refs/heads/")},
	}); err != nil {
		return fmt.Errorf("stages: fetching %s before deciding whether the push may proceed: %w", target, err)
	}
	return nil
}

// pushedReport says what was forwarded and what it cost the target.
//
// A push that dropped commits says so and names them. internal/safety allowed
// it because the target still stood where this run observed it, which is what
// makes the drop anchored rather than blind; it is not a reason to report the
// push as though nothing happened to the branch.
func pushedReport(decision safety.Decision) findings.Report {
	dropped := decision.Rewritten()
	if len(dropped) == 0 {
		return findings.Report{
			Summary:  fmt.Sprintf("Forwarded %s to %s.", decision.Proposed(), decision.Target()),
			Revision: decision.Proposed(),
		}
	}
	return findings.Report{
		Summary: fmt.Sprintf("Forwarded %s to %s, which dropped %d commit(s) the branch held.",
			decision.Proposed(), decision.Target(), len(dropped)),
		Revision: decision.Proposed(),
		Findings: []findings.Finding{
			{
				ID:       "push-rewrote-branch",
				Severity: findings.SeverityWarning,
				Action:   findings.ActionNote,
				Description: fmt.Sprintf(
					"The push replaced %s, which named %s when this run observed it, and the commit "+
						"forwarded does not contain %d commit(s) that branch held: %s.\n\nThe update was "+
						"anchored to the state this run observed, so nothing that arrived after the "+
						"observation was overwritten without the anchor failing first. Who wrote the "+
						"dropped commits is not established here: a commit that reached the branch "+
						"before the observation looks the same as one this run submitted.",
					decision.Target(), decision.Anchor().State(), len(dropped), strings.Join(dropped, ", ")),
			},
		},
	}
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
//
// The refusal is a finding rather than an error because the stage ran and
// checked: what it establishes is that the update may not proceed, which is a
// fact about the world and a decision for a person.
//
// The description relays the refusal rather than interpreting it: its message
// as Refusal.Error renders it, its reason, the commits Refusal.Discarded names
// as Refusal.DiscardedText renders them, which keeps the field's order and
// bounds the list at safety.MaxDiscardedNamed, and the remedy Refusal.Remedy
// states. The list appears twice, in the message and on its own line, so that
// line can be read without parsing the message; the bound applies to both. A
// person at the hold is deciding whether to incorporate what somebody else
// landed, so the commits are the fact the hold exists to show. Nothing here
// sorts, filters, or chooses a remedy by reason, so a line is present exactly
// when the refusal carries it. Every value is a
// function of the refusal, the target, the head and the anchor, with no time
// or counter in it, because a report is part of the state the convergence
// fingerprint reads.
func pushRefused(facts pushState, target safety.Target, refusal *safety.Refusal) pipeline.Output {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\nreason: %s\ntarget: %s\nproposed: %s\nanchor: %s\n",
		refusal.Error(), refusal.Reason, target, facts.head, facts.anchor)
	if discarded := refusal.DiscardedText(); discarded != "" {
		fmt.Fprintf(&b, "discarded: %s\n", discarded)
	}
	if remedy := refusal.Remedy(); remedy != "" {
		fmt.Fprintf(&b, "\n%s\n", remedy)
	}
	b.WriteString("\nNothing was pushed. Approving at this hold advances the run without pushing, " +
		"so the branch on the code host stays where it is; skipping records the stage " +
		"as skipped; cancelling ends the run.")
	return pipeline.Output{
		Report: findings.Report{
			Summary: fmt.Sprintf("The push of %s to %s was refused: %s",
				facts.head, target, refusal.Detail),
			Findings: []findings.Finding{
				{
					ID:          "push-refused",
					Severity:    findings.SeverityError,
					Action:      findings.ActionAsk,
					Description: b.String(),
				},
			},
		},
	}
}

// noReviewedCommit is the report for a review that completed without recording
// which commit it read.
//
// It is separate from having no completed review because the two are different
// situations with the same remedy. A review that passed and named no revision
// leaves nothing to measure the head against, and measuring against nothing is
// the check passing because it looked at nothing.
func noReviewedCommit(facts pushState) pipeline.Output {
	return pipeline.Output{
		Report: findings.Report{
			Summary: fmt.Sprintf(
				"The review of this run completed as %s but recorded no commit, so there is nothing "+
					"to establish that %s descends from a reviewed commit.", facts.reviewOutcome, facts.head),
			Findings: []findings.Finding{
				{
					ID:       "push-review-named-no-commit",
					Severity: findings.SeverityError,
					Action:   findings.ActionAsk,
					Description: fmt.Sprintf(
						"PRD section 5 requires a durable record that a completed review approved a "+
							"commit this one descends from. The review stage ended as %s and its report "+
							"names no revision, so what was reviewed is not established and the ancestry "+
							"check has nothing to compare against.\n\ncurrent head: %s\n\nApproving at this "+
							"hold advances the run without pushing; skipping records the stage as skipped; "+
							"cancelling ends the run.",
						facts.reviewOutcome, facts.head),
				},
			},
		},
	}
}

// notDescendedFromReview is the report for a head that has moved off the
// reviewed commit rather than past it.
//
// This is the check P6 asks for, and it fails closed: a head that does not
// contain the reviewed commit is carrying work no completed review looked at,
// whatever else is true of it.
func notDescendedFromReview(facts pushState) pipeline.Output {
	return pipeline.Output{
		Report: findings.Report{
			Summary: fmt.Sprintf("%s does not descend from the reviewed commit %s, so it is not "+
				"the change a review approved.", facts.head, facts.reviewedCommit),
			Findings: []findings.Finding{
				{
					ID:       "push-not-descended-from-review",
					Severity: findings.SeverityError,
					Action:   findings.ActionAsk,
					Description: fmt.Sprintf(
						"The review of this run read %s, and the commit this stage would forward, %s, "+
							"does not contain it. Forwarding it would push work no completed review "+
							"looked at.\n\nA history rewritten after the review is what this usually "+
							"means.\n\nApproving at this hold advances the run without pushing; skipping "+
							"records the stage as skipped; cancelling ends the run.",
						facts.reviewedCommit, facts.head),
				},
			},
		},
	}
}

// reviewedCommitUnreadable is the report for a reviewed commit the run's copy
// does not hold, so whether the head descends from it cannot be established.
//
// PRD principle P6 asks for a refusal when a safety fact cannot be verified,
// and this is one: the alternative is pushing on the strength of a comparison
// nobody could make.
func reviewedCommitUnreadable(facts pushState, cause error) pipeline.Output {
	return pipeline.Output{
		Report: findings.Report{
			Summary: fmt.Sprintf("The reviewed commit %s is not in this run's copy, so whether %s "+
				"descends from it cannot be established.", facts.reviewedCommit, facts.head),
			Findings: []findings.Finding{
				{
					ID:       "push-reviewed-commit-unreadable",
					Severity: findings.SeverityError,
					Action:   findings.ActionAsk,
					Description: fmt.Sprintf(
						"The review of this run recorded %s, and the run's isolated copy does not "+
							"hold it, so the ancestry check PRD section 5 requires cannot be made: "+
							"%v.\n\nA safety fact that cannot be verified is refused rather than "+
							"assumed, per PRD principle P6.\n\ncurrent head: %s\n\nApproving at this "+
							"hold advances the run without pushing; skipping records the stage as "+
							"skipped; cancelling ends the run.",
						facts.reviewedCommit, cause, facts.head),
				},
			},
		},
	}
}

// noAnchorToPushOn is the report for a run that never observed its target.
//
// The alternative is the one PRD principle P6 names outright: read the target
// now and push against that. It would always succeed, and it would protect
// nothing, so the stage refuses instead.
func noAnchorToPushOn(facts pushState) pipeline.Output {
	return pipeline.Output{
		Report: findings.Report{
			Summary: fmt.Sprintf(
				"This run recorded no observation of %s, so there is no anchor to push %s on.",
				facts.branch, facts.head),
			Findings: []findings.Finding{
				{
					ID:       "push-no-anchor",
					Severity: findings.SeverityError,
					Action:   findings.ActionAsk,
					Description: fmt.Sprintf(
						"Every history-rewriting update has to be anchored to a commit the run actually "+
							"observed, per PRD principle P6. The rebase stage takes that observation "+
							"before the run does its work, and this run has none recorded - a run that "+
							"skipped that stage is what this usually means.\n\nThe only anchor available "+
							"now would be a fresh read of %s, which is the tip read a moment before "+
							"pushing that P6 names: it always holds and so protects nothing. This stage "+
							"will not push on one.\n\nRe-running without skipping the rebase stage is what "+
							"produces an anchor. Approving at this hold advances the run without pushing; "+
							"skipping records the stage as skipped; cancelling ends the run.",
						facts.branch),
				},
			},
		},
	}
}

// anchorForAnotherTarget is the report for an anchor that does not describe the
// reference this stage would move.
//
// internal/safety refuses this too, as a caller bug rather than a refusal, so
// catching it here is what turns it into something a person is told about
// rather than an error naming no cause.
func anchorForAnotherTarget(facts pushState, target safety.Target) pipeline.Output {
	return pipeline.Output{
		Report: findings.Report{
			Summary: fmt.Sprintf("The observation this run recorded is of %s, and the push would "+
				"move %s.", facts.anchor.Target(), target),
			Findings: []findings.Finding{
				{
					ID:       "push-anchor-names-another-target",
					Severity: findings.SeverityError,
					Action:   findings.ActionAsk,
					Description: fmt.Sprintf(
						"An anchor that does not describe the reference being moved protects nothing: "+
							"it would be checked against a branch this push does not touch.\n\nrecorded "+
							"observation: %s\npush target: %s\n\nThis is a disagreement inside the run "+
							"rather than a state of the world, so re-running is what settles it. "+
							"Approving at this hold advances the run without pushing; skipping records "+
							"the stage as skipped; cancelling ends the run.",
						facts.anchor.Target(), target),
				},
			},
		},
	}
}

// pushState holds the facts the push stage reads from the run's state.
type pushState struct {
	repository string
	run        string
	branch     string
	base       string
	head       string
	// pushed is the commit an earlier execution of this stage forwarded, and
	// empty until one did. It is what makes this execution a second push.
	pushed string
	// reviewOutcome is how the review stage ended, which is what says whether
	// a review completed at all.
	reviewOutcome pipeline.Outcome
	// reviewedCommit is the commit the reviewer read, as the review stage
	// reported it.
	reviewedCommit string
	reviewPassed   bool
	// anchor is where the push target stood when this run observed it, and
	// anchorRecorded says whether the run recorded one at all.
	anchor         safety.Observation
	anchorRecorded bool
	// intent and supplied are what the review of later commits frames the
	// change with, on the same terms the review stage does.
	intent   string
	supplied bool
	// laterRounds is what the last fix round of each stage between review and
	// push said it changed, for the stages whose round left an account.
	laterRounds []laterRound
}

// readPushState reads the run's state, the review stage's result, and the
// anchor the run recorded.
func readPushState(state pipeline.Reader) (pushState, error) {
	facts := pushState{}
	toRead := []struct {
		key    pipeline.Key
		target *string
	}{
		{pipeline.KeyRepository, &facts.repository},
		{pipeline.KeyRun, &facts.run},
		{pipeline.KeyBranch, &facts.branch},
		{pipeline.KeyBase, &facts.base},
		{pipeline.KeyHead, &facts.head},
		{pipeline.KeyPushed, &facts.pushed},
		{pipeline.KeyIntent, &facts.intent},
	}
	for _, entry := range toRead {
		v, err := state.Get(entry.key)
		if err != nil {
			return pushState{}, err
		}
		*entry.target, _ = v.Text()
	}
	facts.intent = strings.TrimSpace(facts.intent)
	supplied, err := state.Get(pipeline.KeyIntentSupplied)
	if err != nil {
		return pushState{}, err
	}
	facts.supplied, _ = supplied.Bool()
	for _, stage := range fixingAfterReview() {
		v, err := state.Get(stage.FixKey())
		if err != nil {
			return pushState{}, err
		}
		if summary, _ := v.Text(); strings.TrimSpace(summary) != "" {
			facts.laterRounds = append(facts.laterRounds, laterRound{stage: stage, summary: strings.TrimSpace(summary)})
		}
	}

	// A review completes two ways, and this is where the two become one fact.
	// The review stage's own documentation declines to write that record,
	// because its body sees only the first: a report that passed on its own.
	// The second is a person approving at the stage's hold, which is the
	// stage's outcome and not anything in its report. Reading both here is
	// what that stage means by a later stage owning the record it needs.
	review, err := pipeline.ReadStageResult(state, pipeline.StageReview)
	if err != nil {
		return pushState{}, err
	}
	facts.reviewOutcome = review.Outcome
	facts.reviewedCommit = review.Report.Revision
	facts.reviewPassed = review.Outcome == pipeline.OutcomePassed || review.Outcome == pipeline.OutcomeApproved

	anchor, recorded, err := readAnchor(state)
	if err != nil {
		return pushState{}, err
	}
	facts.anchor = anchor
	facts.anchorRecorded = recorded

	return facts, nil
}
