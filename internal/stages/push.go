package stages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/safety"
	"github.com/dayamjz/assistant/internal/vcs"
)

// Push is the push stage: it forwards the commit the run verified to the
// branch target, and only where it has established that doing so discards
// nothing the target holds.
//
// This is the stage PRD principle P6 exists for. It is the last point before
// the run's work becomes visible to anyone else, and the one place a mistake
// destroys somebody else's commits rather than costing time. So every fact the
// update rests on is either established here or the update is refused: there
// is no path through this body that pushes on an assumption, and a refusal is
// reported as a finding a person answers rather than logged and stepped past.
//
// # The anchor is read, never taken
//
// P6 names the trap outright: a lease anchored to the tip read a moment before
// pushing always succeeds and therefore protects nothing. safety.Observation
// answers it by carrying provenance, and the two constructors that produce one
// are Guard.Observe, which reads the remote, and RestoreObservedFromCheckpoint,
// which rebuilds one an earlier stage recorded.
//
// This body uses the second and never the first. The anchor comes back out of
// pipeline.KeyTargetObserved, which the rebase stage writes when it observes
// the target, and a run with nothing there is refused rather than served by a
// read taken now. That is what makes the anchor older than the work: the
// observation happened before the run rebased, reviewed, tested and fixed, and
// this stage cannot make it younger.
//
// It is a property of this code and not of the type system. safety.Guard
// offers Observe to anyone holding one, and this package can construct one, so
// nothing stops a later edit here from calling it. What stands for it is
// TestThePushStageDecidesOnTheAnchorTheRunObservedAndNotOnAFreshRead, which
// advances the remote out of band after the recorded observation and requires
// a refusal: a body that observed for itself would find the target standing
// where it just read, decide the update was an anchored force over commits it
// may drop, and push. The distinction is behavioural there because the target
// moved. Where it has not moved the two are indistinguishable by outcome,
// which is why the report names the anchor it decided on and PRD section 13
// asserts on that value.
//
// # Why the target is taken from the anchor
//
// This body never composes a target. safety.Observation carries the remote and
// the reference it was taken against, and that is the target the update is
// proposed for, so the stage has no way to update a reference the run did not
// observe. The one thing it does check about it is that the reference is the
// branch under validation, because an anchor correctly taken against some
// other reference would otherwise authorize forwarding this run's commit
// there.
//
// No URL is composed here. What is not checked either is that the recorded
// remote is a name rather than a URL: safety.Target takes either, and the
// stage that observes the target is what chooses which, so this body addresses
// whatever that stage recorded.
//
// A remote read back out of state is therefore text that may carry a
// credential, and nothing before this point removed one. internal/store runs
// the redactor over exactly repository.upstream_url and repository.fork_url
// and says so; a graph checkpoint is opaque bytes that no layer inspects, and
// pipeline.KeyTargetObserved travels in one, so what the rebase stage recorded
// is stored exactly as it recorded it. So credentials are removed on the way
// out through internal/redact, which is the one owner of that under PRD
// section 8, and this file writes no remover of its own.
//
// Two places do it, and what each covers is worth stating rather than
// summarizing, because a step error leaves this package by a different door
// than a report and internal/service writes it to the home's log.
//
//   - shownTarget renders a target for a person, and every site that puts a
//     target or a remote into text goes through it, errors included. That is
//     the display path, and it is one function so that a message added later
//     has one obvious thing to call.
//   - reportPush redacts findings.Report.Summary and Finding.Description as a
//     report leaves. It covers those two fields and no other, which is what
//     catches text this file did not compose: internal/safety's own refusal
//     sentence names the remote, and git's words arrive inside it.
//
// What neither covers is a wrapped error's own text, which stays whichever
// package produced it: internal/vcs redacts its command errors through the
// redactor the service configured, and an error from a package that does not
// would arrive here unredacted. Nor does either recognize a credential outside
// a URL's userinfo, which is internal/redact's stated scope.
//
// # The fetch before the decision
//
// internal/safety answers every reachability question from the local
// repository, so a commit only the remote holds cannot be resolved and the
// comparison that needed it becomes ReasonUnverifiable. The commit that
// matters most is exactly that kind: when somebody else has landed work on the
// target, the run has never seen it.
//
// So the target is fetched before the decision is asked for. Without it the
// remote-advanced case degrades from a refusal naming the commits that would
// be dropped to a refusal saying the comparison could not be answered, which
// refuses either way and tells the person nothing they can act on.
// TestARemoteAdvancedOutOfBandIsRefusedNamingWhatWouldBeDiscarded is what
// holds that, and it asserts the reason as well as the refusal.
//
// A reference the remote does not advertise has no history to fetch, and that
// is a state rather than a failure: it is how the remote answers about a
// branch nobody has pushed yet, whose run reaches this stage on an anchor that
// observed it absent, and about a branch deleted since the run looked. Both
// belong to the decision, which allows the first as a creation leased on the
// absence the run observed and refuses the second as a target that moved. So
// whether there is anything to fetch is read structurally first, and only a
// read or a transfer that actually failed is reported as the target being
// unreadable. fetchTarget owes that distinction, and
// TestAFirstPushCreatesTheBranchOnTheAnchorThatObservedItAbsent is what holds
// the case that used to be swallowed by it.
//
// What the fetch changes is worth stating exactly, because "it only reads" is
// the easy thing to write here and it is not true. It brings the target's
// objects into the copy's store, and git also updates the copy's own
// remote-tracking reference for that remote while it is there. Both are local
// bookkeeping: nothing on the remote changes, no branch in the copy moves, and
// HEAD stays where the run left it.
// TestTheFetchBeforeADecisionMovesNothingTheRunIsWorkingOn is what holds that,
// and it checks the remote and the copy's HEAD rather than the absence of any
// write at all.
//
// # The approval this stage requires
//
// PRD section 5 has this stage require a durable record that a completed
// review approved a commit this one descends from. The record is
// pipeline.KeyApproved, which the run carries in its checkpointed state, and
// what is checked is containment: the commit about to be forwarded has to
// contain the approved one. A run with no such record is refused, and so is
// one where containment could not be established.
//
// Nothing here writes that key. Approving is the review stage's outcome to
// record, and a push stage that recorded its own approval would be the failure
// P4 describes with the roles renamed.
//
// # What it reports
//
// A refusal is one ask finding, so P3 holds the run for a person. The stage
// takes no automatic fix rounds, and that is the PRD's row rather than this
// body's choice: there is no fix a machine applies to a target somebody else
// moved.
//
// The report names the first fact the stage could not establish rather than
// every one. The checks are ordered, and the later ones need what the earlier
// ones establish, so a report listing all of them would be listing guesses
// about the ones that never ran.
//
// # A push that failed rather than being refused
//
// A remote that refuses the update is a refusal and is reported as one. An
// invocation that failed for any other reason fails the step, because what
// happened to the reference is then unknown and this stage has nothing to
// report about it that would be true either way.
//
// Nothing records the update as done on that path, so a later attempt proposes
// it again on the same anchor. That is safe rather than a repeat: if the first
// attempt did land, the target now stands somewhere the run did not observe,
// and the same decision refuses it as a target that moved instead of pushing a
// second time.
//
// # The residual gaps
//
//   - The anchor's provenance rests on the checkpoint it came out of and not
//     on anything checkable here. internal/safety states where that guarantee
//     lives; this body inherits it whole, and a record written from a live
//     read would be indistinguishable from one the rebase stage took.
//   - The anchor's remote is not constrained, only its reference is. An
//     observation taken against refs/heads/<branch> on some other remote is
//     honoured, and both the fetch and the update then address that remote,
//     which is the reasoning behind the reference check reaching the other
//     half of safety.Target. It is a gap rather than a check because nothing
//     in a run's state names an expected remote: pipeline.KeyRepository
//     identifies a store row, not a remote, so a check written today would
//     have to invent the fact it checks against. It belongs with whichever
//     change first gives a run a named upstream remote.
//   - Nothing in this build writes pipeline.KeyTargetObserved or
//     pipeline.KeyApproved, because the rebase and review stages that record
//     them have no body yet. A run reaching this stage today is refused for
//     the absence rather than served by a default, which is the behaviour this
//     stage would want anyway.
//   - What is established about the commits an allowed force update drops is
//     that the target still stands where the run observed it. Whether the run
//     wrote them is not established, because internal/safety never learns the
//     run's base; a decision that drops commits names every one of them and
//     this stage reports that list.
//   - The advertisement read and the decision's own read are two ls-remotes,
//     and the remote can change between them. Nothing rests on the first: the
//     decision decides on its own read and the remote enforces the lease, so
//     the race costs precision rather than safety. A reference that appears
//     between the two is refused on commits the skipped fetch never brought
//     over, which is the unverifiable refusal rather than one naming them, and
//     one that disappears makes the fetch fail as unreadable rather than
//     refusing as moved.
func Push(deps StageDeps) pipeline.Implementation {
	return pipeline.Implementation{
		Reads: []pipeline.Key{
			pipeline.KeyRepository,
			pipeline.KeyRun,
			pipeline.KeyBranch,
			pipeline.KeyHead,
			pipeline.KeyTargetObserved,
			pipeline.KeyApproved,
		},
		Writes: []pipeline.Key{pipeline.KeyPushed},
		NewBody: func() pipeline.Body {
			return func(ctx context.Context, in pipeline.Input) (pipeline.Output, error) {
				return forwardVerifiedCommit(ctx, deps, in)
			}
		},
	}
}

// pushFacts is what this stage reads out of the run's state.
type pushFacts struct {
	repository string
	run        string
	branch     string
	head       string
	observed   string
	approved   string
}

// readPushState reads the six facts the stage decides on. It returns the
// reader's own error unchanged, because a read this stage did not declare is a
// defect in the declaration above rather than a refusal a person can answer.
func readPushState(state pipeline.Reader) (pushFacts, error) {
	var out pushFacts
	for _, field := range []struct {
		key  pipeline.Key
		into *string
	}{
		{pipeline.KeyRepository, &out.repository},
		{pipeline.KeyRun, &out.run},
		{pipeline.KeyBranch, &out.branch},
		{pipeline.KeyHead, &out.head},
		{pipeline.KeyTargetObserved, &out.observed},
		{pipeline.KeyApproved, &out.approved},
	} {
		value, err := state.Get(field.key)
		if err != nil {
			return pushFacts{}, err
		}
		text, _ := value.Text()
		*field.into = strings.TrimSpace(text)
	}
	return out, nil
}

// forwardVerifiedCommit is the stage body.
func forwardVerifiedCommit(ctx context.Context, deps StageDeps, in pipeline.Input) (pipeline.Output, error) {
	facts, err := readPushState(in.State)
	if err != nil {
		return pipeline.Output{}, err
	}
	if facts.branch == "" || facts.head == "" {
		return refusePush("push-nothing-to-forward",
			"This run does not say which branch it validates or which commit it verified, so there is "+
				"nothing to forward and no target to forward it to. Nothing was pushed. Start the run "+
				"again against a branch and a commit."), nil
	}

	anchor, refusal := anchorFor(facts)
	if refusal != nil {
		return *refusal, nil
	}
	target := anchor.Target()

	repo, err := deps.Copy(ctx, facts.repository, facts.run)
	if err != nil {
		return pipeline.Output{}, err
	}

	if out := requireApproval(ctx, repo, facts); out != nil {
		return *out, nil
	}
	if out := fetchTarget(ctx, repo, target); out != nil {
		return *out, nil
	}

	decision, err := safety.New(repo).Decide(ctx, safety.Update{
		Target:   target,
		Proposed: facts.head,
		Anchor:   anchor,
	})
	if err != nil {
		return reportSafetyRefusal(err, facts, target)
	}
	return performUpdate(ctx, repo, decision, facts)
}

// anchorFor rebuilds the observation the run recorded of its branch target,
// and refuses when there is none, when it cannot be read, or when it was taken
// against a reference that is not the branch under validation.
//
// The last of those is the one worth being explicit about. A record naming
// another reference may be a perfectly good observation; what it is not is an
// observation of the branch this run validates, and honouring it would forward
// this run's commit to a reference nobody asked about.
func anchorFor(facts pushFacts) (safety.Observation, *pipeline.Output) {
	if facts.observed == "" {
		out := refusePush("push-no-observation",
			"This run has no record of where "+branchRef(facts.branch)+" stood before it did its work, "+
				"so there is no anchor to update it on. Nothing was pushed, and nothing here can supply "+
				"an anchor: reading the target now would give the tip read a moment before pushing that "+
				"PRD principle P6 forbids anchoring to, which always succeeds and so protects nothing. "+
				"Start the run again so its rebase stage observes the target before the work is done.")
		return safety.Observation{}, &out
	}
	anchor, err := decodeObservation(facts.observed)
	if err != nil {
		out := refusePush("push-unreadable-observation",
			"This run recorded an observation of its branch target that cannot be read back as one, so "+
				"there is no anchor to update on and nothing was pushed. The reader reported: "+
				err.Error()+". Start the run again so its rebase stage records a fresh observation.")
		return safety.Observation{}, &out
	}
	if anchor.Target().Ref != branchRef(facts.branch) {
		out := refusePush("push-observation-of-another-reference",
			"This run recorded an observation of "+shownTarget(anchor.Target()).String()+", which is not "+
				branchRef(facts.branch)+", the branch it validates. Nothing was pushed, because an "+
				"anchor taken against another reference would authorize forwarding this run's commit "+
				"to a reference nobody asked about. Start the run again so its rebase stage observes "+
				"the branch under validation.")
		return safety.Observation{}, &out
	}
	return anchor, nil
}

// requireApproval refuses unless the commit about to be forwarded contains the
// commit a completed review approved. It returns nil when it does.
func requireApproval(ctx context.Context, repo *vcs.Repository, facts pushFacts) *pipeline.Output {
	if facts.approved == "" {
		out := refusePush("push-no-approval",
			"This run has no record that a completed review approved a commit, so there is nothing "+
				"establishing that "+facts.head+" was reviewed. Nothing was pushed. Run the review "+
				"stage to completion, which is what records the approved commit, and run this stage "+
				"again.")
		return &out
	}
	contains, err := repo.IsAncestor(ctx, facts.approved, facts.head)
	if err != nil {
		out := refusePush("push-approval-unverifiable",
			"Whether "+facts.head+" contains "+facts.approved+", the commit a completed review "+
				"approved, could not be established: "+err.Error()+". Nothing was pushed, because a "+
				"fact this update rests on is unverified rather than false. Make both commits "+
				"readable in this run's copy and run this stage again.")
		return &out
	}
	if !contains {
		out := refusePush("push-approval-not-contained",
			"The commit a completed review approved, "+facts.approved+", is not contained in "+
				facts.head+", so what would be forwarded is not a descendant of what was reviewed. "+
				"Nothing was pushed. Run the review stage again against "+facts.head+", and run this "+
				"stage again once it has approved it.")
		return &out
	}
	return nil
}

// fetchTarget brings the target's current history into the run's copy, so that
// internal/safety's reachability comparisons can be answered at all. What it
// is for is the objects; git updates the copy's remote-tracking reference for
// that remote along the way, which is local bookkeeping and moves neither the
// target nor anything the run is working on.
//
// A reference the remote does not advertise is a state, not a failure, and it
// is detected structurally: RemoteRefs is one ls-remote, an answer without the
// reference is the remote saying it does not exist, and nothing here reads
// git's prose to find that out. There is then no history to bring over, so the
// fetch is skipped rather than asked to fail, and what absence means for the
// update is internal/safety's question: its own fresh read allows a creation
// the run observed absent and refuses an anchor the remote no longer
// describes. Asking git to fetch the reference anyway is how absence used to
// arrive here dressed as a failure, which turned every branch's first push
// into a refusal whose named action could never succeed.
//
// A read or a transfer that fails is refused here rather than left for the
// decision to stumble over. The decision would refuse too, on the failure's
// behalf and in the vocabulary of a comparison it could not answer, which says
// less about what went wrong than the failure's own error does. Those two are
// the genuine failures, and only they say to restore access: the remote gave
// no answer, or advertised history it then could not deliver.
func fetchTarget(ctx context.Context, repo *vcs.Repository, target safety.Target) *pipeline.Output {
	refs, err := repo.RemoteRefs(ctx, target.Remote, target.Ref)
	if err != nil {
		out := refusePush("push-target-unreadable",
			"What "+shownTarget(target).String()+" stands at could not be read, so what that branch holds now is "+
				"unknown and no update to it can be shown to discard nothing: "+err.Error()+". Nothing "+
				"was pushed. Restore access to the remote and run this stage again.")
		return &out
	}
	if !advertises(refs, target.Ref) {
		return nil
	}
	if err := repo.Fetch(ctx, vcs.FetchSpec{Remote: target.Remote, Refspecs: []string{target.Ref}}); err != nil {
		out := refusePush("push-target-unreadable",
			"The history of "+shownTarget(target).String()+" could not be fetched, so what that branch holds now is "+
				"unknown and no update to it can be shown to discard nothing: "+err.Error()+". Nothing "+
				"was pushed. Restore access to the remote and run this stage again.")
		return &out
	}
	return nil
}

// advertises reports whether refs carries name itself. ls-remote matches a
// pattern against the tail of a reference name, so a read for refs/heads/x
// can carry other references back; only an exact name is the target, which is
// the same rule internal/safety's own read applies.
func advertises(refs []vcs.Ref, name string) bool {
	for _, ref := range refs {
		if ref.Name == name {
			return true
		}
	}
	return false
}

// reportSafetyRefusal turns internal/safety's answer into this stage's report.
// A *Refusal is a fact about the world that a person decides what to do about,
// so it becomes an ask finding. Anything else is a defect in what this stage
// submitted, so it fails the step.
func reportSafetyRefusal(err error, facts pushFacts, target safety.Target) (pipeline.Output, error) {
	var refusal *safety.Refusal
	if !errors.As(err, &refusal) {
		return pipeline.Output{}, fmt.Errorf("stages: deciding whether %s may be updated to %s: %w",
			shownTarget(target), facts.head, err)
	}
	return refusePush("push-refused-"+string(refusal.Reason),
		refusal.Error()+".\n\n"+discardedClause(refusal)+nextStepFor(refusal, facts, target)), nil
}

// discardedClause names every commit the refused update would have dropped, or
// says nothing when the refusal identified none. It never renders an empty
// list, because an empty list of losses reads as "nothing would be lost",
// which is what a refusal that could not answer the comparison does not
// support.
func discardedClause(refusal *safety.Refusal) string {
	if len(refusal.Discarded) == 0 {
		return ""
	}
	return "The update would discard these commits, which " + refusal.Target.Ref +
		" holds and the proposed commit does not contain:\n" +
		"  " + strings.Join(refusal.Discarded, "\n  ") + "\n\n"
}

// nextStepFor names the action that resolves each refusal. A refusal that
// names none leaves a person holding a fact and no move, which is the half of
// a refusal that makes it an annoyance rather than an obstruction.
func nextStepFor(refusal *safety.Refusal, facts pushFacts, target safety.Target) string {
	switch refusal.Reason {
	case safety.ReasonWouldDiscard:
		return "Nothing was pushed and nothing was lost. Fetch " + shownTarget(target).Remote + " and rebase " +
			facts.head + " onto " + target.Ref + " so that it contains those commits, then run this " +
			"stage again: the same decision then allows a fast-forward. Deciding not to incorporate " +
			"them is a decision to discard them, and it is yours to make rather than this stage's."
	case safety.ReasonTargetMoved:
		return "Nothing was pushed. No commit was identified that this update would discard, and the " +
			"update is still refused, because an anchor that no longer describes the target protects " +
			"nothing. Start the run again so it observes " + target.Ref + " where it now stands, or " +
			"rebase onto it and run this stage again."
	case safety.ReasonUnrelatedHistories:
		return "Nothing was pushed. The two commits share no ancestor, so nothing can be said about " +
			"what either contains of the other. Check that " + shownTarget(target).String() + " is the branch this " +
			"run should be forwarding to, and start the run again against the right one."
	default:
		return "Nothing was pushed, because a fact this update rests on could not be established " +
			"rather than because it was shown to be false. Restore access to " + shownTarget(target).Remote +
			" and to the history it advertises, then run this stage again."
	}
}

// performUpdate performs the update the decision permits, under the lease that
// decision was made on, and reports what it did.
//
// The lease is taken from the decision rather than composed here. A decision
// carries the anchor it was made against and offers no route to permission
// without it, so an update performed on any other lease is a different update
// than the one that was allowed.
func performUpdate(ctx context.Context, repo *vcs.Repository, decision safety.Decision, facts pushFacts) (pipeline.Output, error) {
	target := decision.Target()
	state := decision.Anchor().State()
	err := repo.Push(ctx, vcs.PushSpec{
		Remote: target.Remote,
		Ref:    target.Ref,
		Commit: decision.Proposed(),
		Lease:  vcs.Lease{Exists: state.Exists, Commit: state.Commit},
	})
	if err != nil {
		var rejection *vcs.PushRejection
		if !errors.As(err, &rejection) {
			return pipeline.Output{}, fmt.Errorf("stages: forwarding %s to %s: %w",
				decision.Proposed(), shownTarget(target), err)
		}
		return refusePush("push-rejected",
			"The remote refused to update "+shownTarget(target).String()+", so nothing was pushed and nothing on "+
				"that branch changed. It gave this reason: "+rejection.Reason+". The update was "+
				"proposed under a lease on "+state.String()+", which is where this run observed the "+
				"branch, so the branch may have moved since, or the remote may have declined the "+
				"update for a reason of its own; the reason above is the remote's own words and "+
				"this stage does not interpret it. Resolve what it names, or start the run again "+
				"so it observes the branch where it now stands, and run this stage again."), nil
	}
	return pipeline.Output{
		Report: reportPush(findings.Report{
			Summary: "Forwarded " + decision.Proposed() + " to " + shownTarget(target).String() + ": " +
				decision.String() + ".",
			Findings: rewrittenNote(decision),
		}),
		Writes: map[pipeline.Key]graph.Value{pipeline.KeyPushed: graph.TextValue(decision.Proposed())},
	}, nil
}

// rewrittenNote reports the commits an allowed update dropped from the branch,
// and reports nothing when it dropped none.
//
// It is a note rather than a question. The decision to drop them was already
// taken, under an anchor on a state this run observed, so holding the run to
// ask about it afterwards would be asking about something that has happened.
// What the note is for is that the commits are named somewhere a person
// reading the run can find them.
func rewrittenNote(decision safety.Decision) []findings.Finding {
	dropped := decision.Rewritten()
	if len(dropped) == 0 {
		return nil
	}
	return []findings.Finding{{
		ID:       "push-rewrote-the-branch",
		Severity: findings.SeverityWarning,
		Action:   findings.ActionNote,
		Description: "This update replaced " + decision.Target().Ref + " with a commit that does not " +
			"contain everything the branch held, so these commits are no longer on it:\n  " +
			strings.Join(dropped, "\n  ") + "\n\nIt was anchored on " + decision.Anchor().State().String() +
			", which is where this run observed the branch, so nothing that reached it after the " +
			"observation was dropped. Whether this run wrote them is not established: nothing here " +
			"learns the commit the run started from, so a commit that was on the branch before the " +
			"observation reads the same as one this run submitted.",
	}}
}

// refusePush builds the report for an update that may not proceed. Every
// refusal this stage reports goes through here, so the action is fixed rather
// than a parameter: a refusal is a question for a person, and P3 makes an ask
// finding hold the run for one.
//
// The summary is the refusal plus the finding's opening sentence rather than a
// separate account of it, so a surface showing only the summary says the push
// did not happen and why, and cannot come to disagree with the finding under
// it about which of the two it was.
func refusePush(id, description string) pipeline.Output {
	return pipeline.Output{
		Report: reportPush(findings.Report{
			Summary: "The push was refused. " + firstSentence(description),
			Findings: []findings.Finding{{
				ID:          id,
				Severity:    findings.SeverityError,
				Action:      findings.ActionAsk,
				Description: description,
			}},
		}),
	}
}

// pushRedactor is what removes a credential from a message this stage reports.
// It is internal/redact, which PRD section 8 makes the one owner of that, so
// nothing here recognizes a credential for itself.
var pushRedactor = redact.New()

// shownTarget returns target as it may be shown to a person: the same
// reference, and the remote with any credential in it removed.
//
// It returns a safety.Target rather than a string so that one function serves
// both renders this file makes, the whole target and the remote alone, and a
// site that needs either has nothing to reach for but this. The redaction is
// on the display path only and never where the anchor is decoded, because the
// remote has to stay usable for the fetch and the push that address it.
//
// Every site that puts a target or a remote into text calls it, and the error
// sites are the reason it exists rather than reportPush alone: a step error
// leaves this package as an error and internal/service writes it to the home's
// log, so the report path's redaction never sees it.
func shownTarget(target safety.Target) safety.Target {
	return safety.Target{Remote: pushRedactor.Redact(target.Remote), Ref: target.Ref}
}

// reportPush is the exit every report this stage produces takes, and it
// removes credentials from the two fields of one that carry prose.
//
// It reads text rather than a target, which is what shownTarget does not
// reach: a refusal carries internal/safety's own sentence and git's own words,
// both of which name the remote and neither of which is this stage's to
// reformat. The two overlap on what this file composes itself, and that is
// deliberate rather than redundant, because neither alone covers the other's
// path.
//
// What it covers is Summary and Finding.Description. Every other field of a
// report is untouched, and an error is not a report at all, so it goes out
// through shownTarget instead.
func reportPush(report findings.Report) findings.Report {
	report.Summary = pushRedactor.Redact(report.Summary)
	for i := range report.Findings {
		report.Findings[i].Description = pushRedactor.Redact(report.Findings[i].Description)
	}
	return report
}

// firstSentence returns the opening sentence of a description: everything up
// to and including the first full stop that ends one, which is one followed by
// a space, by a line break, or by nothing.
//
// It reads the text rather than taking a summary beside it because a summary
// stated separately is a second account of the same refusal, and the two go
// out of step the first time one of them is edited.
func firstSentence(text string) string {
	for i, r := range text {
		if r != '.' {
			continue
		}
		rest := text[i+1:]
		if rest == "" || strings.HasPrefix(rest, " ") || strings.HasPrefix(rest, "\n") {
			return text[:i+1]
		}
	}
	return text
}

// branchRef renders a branch name as the full reference a remote advertises
// it under. It is the one place this stage spells that prefix, so the check
// that an anchor names the branch under validation and the messages that
// report it cannot come to disagree about what the branch's reference is.
func branchRef(branch string) string { return "refs/heads/" + branch }

// observationRecord is the notation this package writes a run's observation of
// its branch target in, and the one owner of it: the rebase stage encodes an
// observation with it and the push stage decodes one, and both are bodies in
// this package, so there is no second reader of this shape anywhere.
//
// It is safety.ObservationRecord's fields and nothing else. That type is
// already plain scalars, so this adds a wire form rather than a second model
// of the fact; the fields are named here because the JSON names are the
// durable part and a rename in the Go type must not silently change what an
// interrupted run reads back.
type observationRecord struct {
	Remote string `json:"remote"`
	Ref    string `json:"ref"`
	Exists bool   `json:"exists"`
	Commit string `json:"commit,omitempty"`
}

// encodeObservation renders an observation for pipeline.KeyTargetObserved.
//
// Nothing in this build calls it: the rebase stage, which observes the target,
// has no body yet. It is here rather than with that stage because the decode
// below is what this stage depends on, and a pair whose halves live apart is a
// pair that drifts. The round trip is tested, which is the only check either
// half can be given before both have callers.
func encodeObservation(o safety.Observation) (string, error) {
	rec := o.Record()
	out, err := json.Marshal(observationRecord{
		Remote: rec.Remote,
		Ref:    rec.Ref,
		Exists: rec.Exists,
		Commit: rec.Commit,
	})
	if err != nil {
		return "", fmt.Errorf("stages: recording the observation of %s: %w", shownTarget(o.Target()), err)
	}
	return string(out), nil
}

// decodeObservation rebuilds the observation an earlier stage of this run
// recorded. It refuses text it cannot read and a record internal/safety
// refuses, so a value that reaches a decision is one that package accepted.
//
// What this cannot check is that the record is the one this run wrote. That
// rests on the checkpoint it came out of, which internal/safety's
// documentation is explicit about; decoding establishes the shape and nothing
// about the provenance.
func decodeObservation(text string) (safety.Observation, error) {
	var rec observationRecord
	if err := json.Unmarshal([]byte(text), &rec); err != nil {
		return safety.Observation{}, fmt.Errorf("stages: reading the recorded observation: %w", err)
	}
	return safety.RestoreObservedFromCheckpoint(safety.ObservationRecord{
		Remote: rec.Remote,
		Ref:    rec.Ref,
		Exists: rec.Exists,
		Commit: rec.Commit,
	})
}
