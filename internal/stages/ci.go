package stages

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// CI is the checks stage: it watches CI and mergeability.
//
// # What it establishes
//
// PRD section 5 gives this stage two jobs: watch CI and mergeability, and fix
// failures and conflicts. This implementation reads the checks on the pull
// request's head and whether the provider reports the pull request mergeable,
// and reports what it read once both are settled. A conflict it answers by
// sending the run back to rebase, and a repair its own fix round committed by
// sending the run back to review, and either reaches the pull request only
// through a second review and a second anchored push. The two sections below
// say how and why.
//
// A pass establishes that every registered check settled and passed, and that
// the provider reported the pull request mergeable, in the reads this
// invocation last made. It does not establish that either stayed so, that the
// code was reviewed by a person, or that the provider will merge it: those are
// the provider's policies and this stage reports what it reads rather than
// deciding on the provider's behalf.
//
// # Mergeability, and a conflict
//
// Mergeability is read off the same read of the pull request that comes
// before the checks, forge.PullRequest.Mergeability, which forge keeps apart
// from the checks on purpose. A pull request the provider positively reports
// as conflicted is answered at once, whatever its checks say, and its checks
// are not read: a conflict is not something waiting resolves.
//
// The answer is PRD section 14 decision 7's rebase of a conflicted pull
// request. The body asks for internal/pipeline's path back to the rebase
// stage with pipeline.Output.Rebase, over a report carrying one note that
// says why, and the run goes through rebase, review, the checks before push,
// a second push and the pull request stage before it reads here again. None
// of that is this body's: the rebase stage anchors the second push only where
// this run's first push left the branch and rebases only when the replay
// applies cleanly, holding otherwise, so this run never decides on its own
// what conflicting content should be; the review stage reviews the result with no
// session behind it; and the push stage decides through internal/safety.
// internal/pipeline bounds the path by RebaseAfterPushRounds, and a request
// past it parks the run.
//
// The body asks only where that path can do what it says. The run has to have
// pushed, its head has to be the commit it pushed, and none of the rebase,
// review and push stages may be on this run's skip list: a run that pushed
// nothing has no branch to rebase, a head a fix round of this stage moved is
// sent back to review below before the pull request is read again, and a
// skipped stage would leave the second push without the
// anchor, the review, or the push itself. Anywhere else the conflict holds
// with an ask finding, as it did before the path existed, saying why the run
// did not rebase and how a person moves the work on.
//
// Checks that passed are not a pass until the provider has also reported the
// pull request mergeable. A mergeability the provider has not reported, or
// one this build does not recognize, which forge maps to its zero value, is
// waited through on the terms below, and an elapsed wait over passed checks
// holds naming mergeability as what it was still waiting on. Checks that
// failed are settled whatever the mergeability, because their findings are
// what this stage's fix rounds are for.
//
// # A fix round's commit is reviewed again and pushed on an anchor
//
// A fix round at this stage commits in the run's isolated copy after the push
// stage forwarded the branch, so the pull request and its checks still stand
// on the pushed commit. The owner decided on #325 that such a repair goes
// back through the review stage before it is pushed, so when this stage runs
// again after one of its own rounds and the run's head is not the commit the
// push stage forwarded, it does not read the checks, which cannot have seen
// the round's commit. It observes the branch it pushed and requires the
// observation to stand at that commit, safety.RequireObservedAt, records it
// as the anchor of the next push, and asks for internal/pipeline's path back
// to the review stage with pipeline.Output.Repair, over a report carrying one
// note. The run then reviews the repaired head in a session of its own, runs
// the checks before push, pushes it on that anchor, updates the pull request,
// and stands here again, where it reads the checks on what it pushed.
// repair.go is that body; the decision is made from pipeline.KeyHead,
// pipeline.KeyPushed and this stage's own fix key, never from what the fixer
// said it did.
//
// What bounds it is this stage's fix round limit, per run and not per pass:
// every repair is one round, and a check still failing after the last round
// the limit allows parks the run in front of this stage's fixer.
// internal/pipeline builds the path only where that limit is above zero.
//
// A branch somebody else moved since this run pushed it is refused at the
// observation and holds with ci-fix-branch-moved, naming both commits, and the
// push stage refuses one that moves after it. Where the path cannot do its
// job at all - the run pushed nothing, or it skips the review or the push
// stage - the stage holds with ci-fix-not-pushed as it did before the path
// existed, naming both commits and why. A hold a round raised, such as the
// judge rule's, holds the stage whatever the body asked, because
// internal/pipeline honours the request only over a report that would
// otherwise pass. What happens to the copy holding a commit that was never
// pushed is internal/tasks' CleanUpRun's decision.
//
// # The code host and the repository
//
// This stage requires both. deps.Forge is the adapter and
// pipeline.KeyForgeRepository is the repository on that host, so a run whose
// record names no repository on a host this build talks to has nothing to open
// and reports that rather than proceeding without one. The repository is
// resolved from the run's upstream URL, and forge.RepositoryOf describes
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
// # What a fix round does here, and what it is working from
//
// A check that failed is reported as a fix finding, so it is eligible for this
// stage's automatic fix rounds. Such a finding routes into this stage's fix
// node, where the fixer asks an agent to resolve it in the run's isolated copy
// and commits what changed.
//
// What that round is working from is this stage's finding and not the check's
// own output. This stage reports the provider's verdict and the names of the
// checks that failed; it does not fetch their logs, so the agent is asked to
// resolve a named failing check rather than to read what it printed. Fetching
// that output is the work that would make this round better informed, and it
// is not done here.
//
// # Waiting, and what bounds it
//
// Checks that have not settled - some still running, or none registered and
// no no_ci declaration - are a wait rather than a hold, which is PRD section
// 5's "Waiting on checks" and section 10's checks_timeout; section 6's
// lifecycle word Paused is a worker's own declared wait and not this one. The
// body re-reads the checks every checksPollInterval until they settle or the
// timeout elapses, and only an elapsed timeout holds the run, with an ask
// finding naming what it was still waiting on. That is section 10's "on
// elapse the run holds rather than ending", and that hold is the one section
// 11's recovery step 3 re-reads the pull request before resuming, which
// internal/service does.
//
// # The pull request is read before the checks, on every read
//
// Every read of the wait, including the first after a restart, reads where
// the pull request stands before it reads the checks, through
// forge.Provider.Get by the number the pull request stage recorded. That read
// is by identifier and never through forge.Provider.Find, which is documented
// not to find a merged or closed pull request and so could not answer the
// question this read asks.
//
// Only a state the provider positively reports as merged or closed ends the
// wait. The stage then ends its wait row, records which under
// pipeline.KeyPullRequestEnd with the head it read at under
// pipeline.KeyPullRequestEndHead, and reports what it read: a merge is reported
// as complete and not newly validated, because the checks were not read to a
// verdict and the rule that only a no_ci declaration makes an empty check
// list green was never applied; a close is reported as the checks not having
// been read to a verdict. Neither report carries a finding, so in the
// pipeline's vocabulary the stage passed, which is true of the stage: nothing
// it read needs anything done about it. What the run becomes is not this
// body's to decide, and it decides nothing: internal/service reads the two
// keys and the recorded number when it settles the record, writes PRD section
// 8's merge record from them, and PRD section 5 says a merge passes the run
// and a close fails it with the reason "pull request closed without merging".
// The report says what was read, not what the record will say.
//
// Open keeps waiting, and so does a state this build does not recognize,
// which forge maps to its zero value, and so does a pull request that could
// not be read at all. The last is the one read in this body whose refusal is
// waited through rather than failing the step, and it is deliberate: PRD
// section 5 has a state that was not read leave the run waiting, and a
// provider that cannot answer for the pull request is about to be asked for
// its checks, where a refusal still fails the step as it always did. What
// that costs is a provider that answers for checks and not for the pull
// request: a run of a merged pull request then keeps waiting until the
// timeout, and so does a run whose checks passed, because a pull request that
// was not read reports no mergeability; the service's re-read at the hold that
// leaves is the next chance to notice a merge or a close. The refusal itself
// is a typed value this body reads and classifies as not read; nothing is
// logged and nothing is carried into the report, because an elapsed report
// may not vary with what one read said.
//
// What the timeout is measured from is not in the run's graph state, because
// that state is the convergence fingerprint and an instant written there
// would change it on every read. It is a row in internal/store, reached
// through StageDeps.waits, holding when the wait was armed and the base
// commit it was armed against. Every unsettled read observes the upstream's
// base branch through the run's isolated copy and arms the wait against what
// it saw, so a base that moved re-arms the timeout, and a wait read again
// against the same base keeps the instant it began. A read that finds the
// checks settled ends the wait, so the next unsettled read after a fix round
// begins a new one.
//
// The wait is inside the node rather than between nodes, and that is safe
// only because the node does nothing but read: halt-before keeps a resumed
// node from repeating side effects, and this body has none except the wait
// record, whose arming is idempotent for an unchanged base. A segment that
// ends mid-wait - a caller who walked away, a restart, an ending through the
// protocol - leaves the run standing at this stage, and whatever resumes it
// re-enters the wait against the same row, so the timeout is not reset by a
// resume.
//
// A read that does not answer is bounded too, after the first. Once a wait is
// armed, each read of the pull request, of the checks and of the base runs
// under a context that ends when what is left of the wait does, and a read
// that wait ran out on holds the run as an elapsed wait, saying the read had
// not answered. The
// last pause ends exactly on the deadline, and the read made there is the
// wait's final one: it gets checksFinalReadBound of its own, so checks that
// went green in the last interval pass rather than hold, and a base that
// advanced in it re-arms. The first read of a segment is not bounded that
// way: it comes before the segment has read the wait, so a provider that
// never answers it holds the segment until something ends its context. A base
// read that the wait ran out on holds rather than re-arming, because whether
// the base advanced is then unknown, and holding hands that to a person.
//
// An elapsed wait ends the row, as settled checks, a conflict and a fix
// round nothing pushes do. A wait whose segment ended any other way - a
// failed read, a cancellation, a supersession - leaves its row behind; internal/store's ChecksWait says what that costs.
//
// # Who is waiting, and on whose behalf
//
// While the wait lasts the run is executing and something is advancing it, so
// it reads as running rather than held. Nothing here asks whether the run is
// still wanted between reads: there is no typed route from a stage body to
// the run's record, and a body that opened one would be a second reader of
// the run's status. So a run a newer push superseded and this service never
// signalled - internal/service's claimPush says when that happens - keeps
// reading the provider once a minute until its checks settle or checks_timeout
// elapses. A run that was signalled stops waiting when the signal arrives,
// because its pauses and its reads all run under the segment's context.
//
// # What happens to a wait the service stopped, and to the hold it leaves
//
// A run waiting here when the service stops is resumed into the same wait by
// recovery, and the first read of that resumed segment reads the pull request
// before the checks like every other, so a pull request merged or closed
// while the service was down ends the run on the terms above. That is PRD
// section 11's recovery step 1 for this stage, and it is this body's rather
// than the service's because the resumed segment is this body.
//
// The hold an elapsed wait leaves is a different case, and it is the one
// section 11's step 3 names. A held run is not resumed by this body until a
// person answers, so before that answer is taken the service re-reads the
// pull request the same way, by its recorded identifier, and a positively
// read merged or closed state closes the hold by an explicit resolution the
// service records as store.ResolvedByProgram with the state it read, and ends
// the run as section 5 says for that state. ChecksTimeoutHeld is what tells
// the service that a report is that hold and not one on a person's decision
// about a finding, which is never re-read; internal/service owns the rest.
//
// # What this does not do
//
// It does not survive a checks read or a base read that fails on its own. A
// provider that refuses the checks, or an upstream that does not advertise
// the base branch, fails the step as it did before the wait existed, so a
// transient failure partway through a long wait leaves the run standing at
// this stage for something to resume rather than waiting through it. The wait
// it resumes into is the same row, so the time already spent still counts.
// The pull request read is the one exception, stated above.
//
// The timeout is compared against this process's clock, and the instant a
// wait was armed is that clock's too. A clock stepped while a run waits moves
// the elapse by the step; nothing here detects it.
func CI(deps StageDeps) pipeline.Implementation {
	return pipeline.Implementation{
		Reads: []pipeline.Key{
			pipeline.KeyForgeRepository, pipeline.KeyPullRequest,
			pipeline.KeyRepository, pipeline.KeyRun, pipeline.KeyBase,
			pipeline.KeyHead, pipeline.KeyPushed, pipeline.StageCI.FixKey(),
			pipeline.KeySkip, pipeline.KeyBranch, pipeline.KeyForkRepository,
			pipeline.KeyRepairs,
		},
		Writes: []pipeline.Key{pipeline.KeyPullRequestEnd, pipeline.KeyPullRequestEndHead, pipeline.KeyPushAnchor},
		NewBody: func() pipeline.Body {
			return func(ctx context.Context, in pipeline.Input) (pipeline.Output, error) {
				return runChecks(ctx, deps, in)
			}
		},
	}
}

// runChecks is the stage body: it reads the pull request's state and then its
// checks until the checks settle, the pull request is read as merged or
// closed, or checks_timeout elapses, and turns what it last read into a
// report.
func runChecks(ctx context.Context, deps StageDeps, in pipeline.Input) (pipeline.Output, error) {
	facts, err := readCIState(in.State)
	if err != nil {
		return pipeline.Output{}, err
	}

	// A run with no forge repository or no pull request cannot query checks.
	// Both are configuration rather than a failure of the change: the
	// repository comes from the run's upstream URL, and the pull request comes
	// from whether the pull request stage ran. So both are reported and the
	// stage holds rather than failing.
	if facts.forgeRepository == "" {
		return pipeline.Output{Report: noForgeRepository()}, nil
	}
	if facts.pullRequest == 0 {
		return pipeline.Output{Report: noPullRequest()}, nil
	}

	if deps.Forge == nil {
		return pipeline.Output{}, fmt.Errorf("stages: no forge adapter, so the %s stage cannot query checks", in.Stage)
	}

	provider, err := deps.Forge.Open(facts.forgeRepository)
	if err != nil {
		// A provider that cannot be opened is an error rather than a finding,
		// because it means the adapter could not run at all and no stage
		// decision is being made. The refusal is returned as-is: it already
		// names what went wrong and redacts what it read from the provider.
		return pipeline.Output{}, fmt.Errorf("stages: opening forge provider for %s: %w", facts.forgeRepository, err)
	}

	w := checksWaiter{deps: deps, facts: facts}
	timeout := deps.Config.ChecksTimeout
	// last and wait are what the previous read saw and the wait it armed.
	// Every read after the first is bounded by what is left of that wait, so
	// a provider or an upstream that stops answering cannot outlast
	// checks_timeout; the first read of a segment has no wait to be bounded
	// by yet.
	var (
		last  forge.ChecksResult
		wait  store.ChecksWait
		armed bool
	)
	for {
		readCtx, cancel := ctx, context.CancelFunc(func() {})
		if armed {
			// What is left is measured on the stage's clock and enforced by a
			// timer on the wall clock, so with the product's clock the two are
			// one clock. A pause ends exactly on the deadline, which leaves
			// nothing; the read made there is the wait's final one, and it gets
			// a bound of its own so that checks which went green in the last
			// interval are read and passed rather than held.
			left := wait.ArmedAt.Add(timeout).Sub(deps.clock.now())
			if left <= 0 {
				left = checksFinalReadBound
			}
			readCtx, cancel = context.WithTimeout(ctx, left)
		}
		// The pull request first, by its recorded number: a merged or closed
		// one has no checks worth waiting for, and PRD section 5 has this
		// read precede the checks on every read.
		pr, end, err := readPullRequestEnd(readCtx, provider, facts.pullRequest)
		if err != nil {
			cancel()
			if elapsedDuring(ctx, readCtx) {
				return w.elapsed(ctx, last, timeout, true)
			}
			return pipeline.Output{}, fmt.Errorf("stages: reading pull request %d: %w", facts.pullRequest, err)
		}
		if end != "" {
			cancel()
			if err := w.end(ctx); err != nil {
				return pipeline.Output{}, err
			}
			return pipeline.Output{
				Report: pullRequestEndedReport(end, pr),
				Writes: map[pipeline.Key]graph.Value{
					pipeline.KeyPullRequestEnd:     graph.TextValue(string(end)),
					pipeline.KeyPullRequestEndHead: graph.TextValue(pr.HeadCommit),
				},
			}, nil
		}
		if facts.unpushedRound() {
			cancel()
			if err := w.end(ctx); err != nil {
				return pipeline.Output{}, err
			}
			if !facts.mayRepair() {
				return pipeline.Output{Report: unpushedRoundReport(facts)}, nil
			}
			already, err := pipeline.RepairAlreadySent(in.State, facts.pushed, facts.head)
			if err != nil {
				return pipeline.Output{}, fmt.Errorf("stages: reading repairs before sending one back for review: %w", err)
			}
			if already {
				return pipeline.Output{Report: repairResubmitReport(facts)}, nil
			}
			return repairAfterPush(ctx, deps, facts)
		}
		if pr.Mergeability == forge.MergeabilityConflicted {
			cancel()
			if err := w.end(ctx); err != nil {
				return pipeline.Output{}, err
			}
			if facts.mayRebase() {
				return pipeline.Output{Report: rebasingReport(pr, facts), Rebase: true}, nil
			}
			return pipeline.Output{Report: conflictedReport(pr, facts)}, nil
		}
		checksReport, err := provider.Checks(readCtx, facts.pullRequest)
		if err != nil {
			cancel()
			if elapsedDuring(ctx, readCtx) {
				return w.elapsed(ctx, last, timeout, true)
			}
			return pipeline.Output{}, fmt.Errorf("stages: reading checks for pull request %d: %w", facts.pullRequest, err)
		}
		result := checksReport.Evaluate(forge.DeclaredNoCI(deps.Config))
		if settled(result.Verdict, pr.Mergeability) {
			cancel()
			if err := w.end(ctx); err != nil {
				return pipeline.Output{}, err
			}
			return pipeline.Output{Report: checksResultReport(result, facts.pullRequest)}, nil
		}

		next, err := w.arm(readCtx)
		elapsed := elapsedDuring(ctx, readCtx)
		cancel()
		if err != nil {
			if elapsed {
				// The base could not be read before the wait ran out, so
				// whether it advanced is unknown. Holding is the direction
				// that hands the question to a person rather than waiting on.
				return w.elapsed(ctx, result, timeout, true)
			}
			return pipeline.Output{}, err
		}
		last, wait, armed = result, next, true

		deadline := wait.ArmedAt.Add(timeout)
		now := deps.clock.now()
		if !now.Before(deadline) {
			return w.elapsed(ctx, result, timeout, false)
		}
		if err := deps.clock.pause(ctx, min(checksPollInterval, deadline.Sub(now))); err != nil {
			return pipeline.Output{}, fmt.Errorf("stages: waiting on the checks for pull request %d: %w", facts.pullRequest, err)
		}
	}
}

// readPullRequestEnd reads where the pull request stands and reports which
// end, if either, the provider positively reported, with the pull request as
// read. A state that is open, that this build does not recognize, or that
// could not be read is no end and keeps the caller waiting, which is PRD
// section 5's rule; the doc comment on CI says what the last of those costs.
//
// The error it returns is the context's and only the context's: a read the
// segment ended, or the wait ran out on, is reported so the caller can tell
// those two apart the way it does for a checks read. A refusal the provider
// made on its own is classified here as not read and does not reach the
// caller as an error, because it is a typed answer this stage has a rule for.
func readPullRequestEnd(ctx context.Context, provider forge.Provider, number int) (forge.PullRequest, store.PullRequestEnd, error) {
	pr, err := provider.Get(ctx, number)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return forge.PullRequest{}, "", ctxErr
		}
		return forge.PullRequest{}, "", nil
	}
	switch pr.State {
	case forge.PullRequestStateMerged:
		return pr, store.PullRequestMerged, nil
	case forge.PullRequestStateClosed:
		return pr, store.PullRequestClosed, nil
	default:
		// Open, the zero state forge maps an unrecognized word onto, and any
		// state added to forge without being classified here are all waited
		// through, on the same terms as an unrecognized verdict: only a
		// positively read merged or closed state ends a wait.
		return pr, "", nil
	}
}

// checksFinalReadBound is how long the read made once the wait has run out may
// take. It is the poll interval, so the final read is given what any other
// read in the last minute of a wait would have had.
const checksFinalReadBound = checksPollInterval

// elapsedDuring reports whether a read failed because what was left of the
// wait ran out while it was in flight, rather than because the segment ended
// or the read failed on its own.
func elapsedDuring(segment, read context.Context) bool {
	return segment.Err() == nil && errors.Is(read.Err(), context.DeadlineExceeded)
}

// settled reports whether a verdict, read beside the pull request's
// mergeability, ends the wait. Failed does whatever the mergeability, because
// its findings are what the fix rounds are for. Passed does only once the
// provider has reported the pull request mergeable: a conflicted one never
// reaches here, and any other mergeability is one the provider has not
// settled, which is waited through on the same terms as a running check. No
// checks and running are what the stage waits through, per forge's own
// statement of each.
func settled(v forge.Verdict, m forge.Mergeability) bool {
	switch v {
	case forge.VerdictFailed:
		return true
	case forge.VerdictPassed:
		return m == forge.MergeabilityMergeable
	case forge.VerdictNoChecks, forge.VerdictRunning:
		return false
	default:
		// A verdict added to forge without being classified here is waited
		// through, for the reason forge gives for an unrecognized check state:
		// waiting is the error that can still be corrected, and the timeout
		// bounds it.
		return false
	}
}

// checksPollInterval is how long the checks body waits between reads of a
// pull request's checks that had not settled. It is a minute so that one
// waiting run costs the provider one checks request and one read of the
// upstream's base branch a minute.
const checksPollInterval = time.Minute

// checksClock is how the checks body tells the time and waits. Its zero value
// is the wall clock.
type checksClock struct {
	nowFunc   func() time.Time
	pauseFunc func(context.Context, time.Duration) error
}

func (c checksClock) now() time.Time {
	if c.nowFunc != nil {
		return c.nowFunc()
	}
	return time.Now()
}

// pause waits for d or until ctx ends, and reports the context's error in the
// second case so a segment ended mid-wait fails the step rather than reading
// the checks once more.
func (c checksClock) pause(ctx context.Context, d time.Duration) error {
	if c.pauseFunc != nil {
		return c.pauseFunc(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// checksWaiter arms and ends one run's wait, observing the base branch through
// the run's isolated copy, which it opens once and only when a wait needs it.
type checksWaiter struct {
	deps   StageDeps
	facts  ciFacts
	copied *vcs.Repository
}

// arm observes where the base branch stands on the upstream now and arms the
// run's wait against it.
//
// The base is read from the upstream rather than from anything this run
// fetched earlier, because what re-arms the timeout is the base advancing
// while the run waits, and a copy fetched before the wait began cannot show
// that.
func (w *checksWaiter) arm(ctx context.Context) (store.ChecksWait, error) {
	if w.deps.waits == nil {
		return store.ChecksWait{}, fmt.Errorf("stages: no checks wait record, so the checks stage cannot bound its wait on pull request %d by checks_timeout",
			w.facts.pullRequest)
	}
	if w.facts.base == "" {
		return store.ChecksWait{}, fmt.Errorf("stages: the run names no base branch, so the checks stage cannot tell whether it advanced")
	}
	if w.copied == nil {
		copied, err := w.deps.Copy(ctx, w.facts.repositoryID, w.facts.runID)
		if err != nil {
			return store.ChecksWait{}, err
		}
		w.copied = copied
	}
	ref := "refs/heads/" + w.facts.base
	refs, err := w.copied.RemoteRefs(ctx, UpstreamRemote, ref)
	if err != nil {
		return store.ChecksWait{}, fmt.Errorf("stages: reading where %s stands on %s: %w", ref, UpstreamRemote, err)
	}
	base := ""
	for _, r := range refs {
		if r.Name == ref {
			base = r.Commit
		}
	}
	if base == "" {
		return store.ChecksWait{}, fmt.Errorf("stages: %s does not advertise %s, so the checks stage cannot tell whether the base advanced",
			UpstreamRemote, ref)
	}
	wait, err := w.deps.waits.ArmChecksWait(ctx, w.facts.runID, base, w.deps.clock.now())
	if err != nil {
		return store.ChecksWait{}, fmt.Errorf("stages: arming the checks wait: %w", err)
	}
	return wait, nil
}

// end removes the run's wait once its checks settled. With no record wired
// there is nothing this body could have armed, so there is nothing to end.
func (w *checksWaiter) end(ctx context.Context) error {
	if w.deps.waits == nil {
		return nil
	}
	if err := w.deps.waits.EndChecksWait(ctx, w.facts.runID); err != nil {
		return fmt.Errorf("stages: ending the checks wait: %w", err)
	}
	return nil
}

// elapsed ends the run's wait and reports that it ran out. The wait is ended
// here rather than left for a later read, because the hold this reports is
// answered by a person and not by this stage running again, so a row left
// behind would only be found by a wait that should have begun afresh.
func (w *checksWaiter) elapsed(ctx context.Context, result forge.ChecksResult, timeout time.Duration,
	unanswered bool) (pipeline.Output, error) {
	if err := w.end(ctx); err != nil {
		return pipeline.Output{}, err
	}
	return pipeline.Output{Report: checksElapsedReport(result, w.facts.pullRequest, timeout, unanswered)}, nil
}

// ciFacts are what this stage reads from the run's state.
type ciFacts struct {
	forgeRepository string
	pullRequest     int
	repositoryID    string
	runID           string
	base            string
	// head is the commit the run stands on now, pushed is the commit the
	// push stage forwarded, and roundRan says a fix round of this stage has
	// left its summary, which is the state a round leaves behind.
	head     string
	pushed   string
	roundRan bool
	// branch and fork locate the branch the push stage forwards to, which a
	// repair observes before it is sent back for a second push.
	branch string
	fork   string
	// skipped names the stages this run's skip list passes over, which is
	// what says whether the path back to rebase would run the stages it is
	// for.
	skipped map[pipeline.Stage]bool
}

// rebasePath is the stages the path back to rebase needs to run for a second
// push to be anchored, reviewed and made.
var rebasePath = []pipeline.Stage{pipeline.StageRebase, pipeline.StageReview, pipeline.StagePush}

// mayRebase reports whether a conflict is answered by the path back to
// rebase: the run pushed, its head is the commit it pushed, and the stages the
// path needs are not skipped in this run.
func (f ciFacts) mayRebase() bool {
	if f.pushed == "" || f.head != f.pushed {
		return false
	}
	for _, stage := range rebasePath {
		if f.skipped[stage] {
			return false
		}
	}
	return true
}

// whyNotRebased says why a conflict holds rather than taking the path back,
// in a sentence a hold can carry. It is asked only where mayRebase is false.
func (f ciFacts) whyNotRebased() string {
	if f.pushed == "" {
		return "This run pushed no commit, so it has no branch of its own to rebase."
	}
	if f.head != f.pushed {
		return fmt.Sprintf("This run's head %s is not %s, the commit it pushed, so a rebase of it would not be "+
			"the branch the pull request stands on.", f.head, f.pushed)
	}
	var names []string
	for _, stage := range rebasePath {
		if f.skipped[stage] {
			names = append(names, stage.String())
		}
	}
	return fmt.Sprintf("This run skips the %s stage(s), which a rebase after the push has to pass through "+
		"before a second push.", strings.Join(names, ", "))
}

// unpushedRound reports whether a fix round of this stage moved the run's
// head off the commit the push stage forwarded, so the head is a commit the
// pull request does not carry. Only this stage's own round is asked about:
// the pull request stage writes no head, and the stages before push are
// behind the push stage's own review of later commits.
func (f ciFacts) unpushedRound() bool {
	return f.roundRan && f.head != "" && f.head != f.pushed
}

// repairPath is the stages the path back to review needs to run for a
// repair to be reviewed again and pushed a second time.
var repairPath = []pipeline.Stage{pipeline.StageReview, pipeline.StagePush}

// mayRepair reports whether a fix round's commit is answered by the path
// back to review: the run pushed, so there is a branch the repair goes on top
// of and an anchor to observe, and neither the review stage nor the push
// stage is on this run's skip list, since a skipped review would send a
// repair to the pull request that nothing read, and a skipped push would
// leave it where it is.
func (f ciFacts) mayRepair() bool {
	if f.pushed == "" {
		return false
	}
	for _, stage := range repairPath {
		if f.skipped[stage] {
			return false
		}
	}
	return true
}

// whyNotRepaired says why a fix round's commit holds rather than taking the
// path back to review, in a sentence a hold can carry. It is asked only where
// mayRepair is false.
func (f ciFacts) whyNotRepaired() string {
	if f.pushed == "" {
		return "This run pushed no commit, so it has no branch of its own for the repair to go on."
	}
	var names []string
	for _, stage := range repairPath {
		if f.skipped[stage] {
			names = append(names, stage.String())
		}
	}
	return fmt.Sprintf("This run skips the %s stage(s), which a repair has to pass through before it is "+
		"pushed.", strings.Join(names, ", "))
}

// readCIState reads the facts this stage needs from the run's state: which
// repository on the code host and which pull request, and the run, its
// repository and its base branch, which locate the isolated copy the base is
// observed through and key the wait.
func readCIState(state pipeline.Reader) (ciFacts, error) {
	var facts ciFacts
	texts := []struct {
		key  pipeline.Key
		into *string
	}{
		{pipeline.KeyForgeRepository, &facts.forgeRepository},
		{pipeline.KeyRepository, &facts.repositoryID},
		{pipeline.KeyRun, &facts.runID},
		{pipeline.KeyBase, &facts.base},
		{pipeline.KeyHead, &facts.head},
		{pipeline.KeyPushed, &facts.pushed},
		{pipeline.KeyBranch, &facts.branch},
		{pipeline.KeyForkRepository, &facts.fork},
	}
	for _, t := range texts {
		value, err := state.Get(t.key)
		if err != nil {
			return ciFacts{}, err
		}
		*t.into, _ = value.Text()
	}

	skip, err := state.Get(pipeline.KeySkip)
	if err != nil {
		return ciFacts{}, err
	}
	names, _ := skip.List()
	facts.skipped = make(map[pipeline.Stage]bool, len(names))
	for _, name := range names {
		if stage, ok := pipeline.ParseStage(name); ok {
			facts.skipped[stage] = true
		}
	}

	summary, err := state.Get(pipeline.StageCI.FixKey())
	if err != nil {
		return ciFacts{}, err
	}
	text, _ := summary.Text()
	facts.roundRan = text != ""

	prValue, err := state.Get(pipeline.KeyPullRequest)
	if err != nil {
		return ciFacts{}, err
	}
	prText, _ := prValue.Text()
	if prText != "" {
		facts.pullRequest, err = strconv.Atoi(prText)
		if err != nil {
			return ciFacts{}, fmt.Errorf("stages: parsing pull request number %q: %w", prText, err)
		}
		if facts.pullRequest < 1 {
			return ciFacts{}, fmt.Errorf("stages: pull request number %d is not a positive integer", facts.pullRequest)
		}
	}
	return facts, nil
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

// pullRequestEndedReport is the report for a wait the pull request's own
// state ended. It carries no finding, because nothing read needs anything
// done about it, and it says what was read rather than what the run's record
// will say: a merge is complete and not newly validated, with the two things
// that did not happen named, and a close is the checks not read to a verdict.
// What the run becomes is internal/service's, from pipeline.KeyPullRequestEnd.
func pullRequestEndedReport(end store.PullRequestEnd, pr forge.PullRequest) findings.Report {
	report := findings.Report{Revision: pr.HeadCommit}
	switch end {
	case store.PullRequestMerged:
		report.Summary = fmt.Sprintf(
			"Pull request %d was merged while the checks stage waited on its checks. The run is complete "+
				"and not newly validated: the checks were not read to a verdict, and the rule that only a "+
				"no_ci declaration makes an empty check list green was not applied.", pr.Number)
	case store.PullRequestClosed:
		report.Summary = fmt.Sprintf(
			"Pull request %d was closed without being merged while the checks stage waited on its checks. "+
				"The checks were not read to a verdict.", pr.Number)
	default:
		report.Summary = fmt.Sprintf("Pull request %d was read as %s while the checks stage waited on its checks.",
			pr.Number, end)
	}
	return report
}

// The identifiers of the findings an elapsed wait holds on. They are
// what ChecksTimeoutHeld reads, so a report built elsewhere cannot pass as
// that hold by carrying the words.
const (
	// findingChecksRunning is the hold for checks that had not settled when
	// checks_timeout elapsed.
	findingChecksRunning = "ci-running"
	// findingNoChecks is the hold for no checks registered and no no_ci
	// declaration when checks_timeout elapsed.
	findingNoChecks = "ci-no-checks"
	// findingMergeabilityUnknown is the hold for checks that passed on a pull
	// request the provider had not reported mergeable when checks_timeout
	// elapsed.
	findingMergeabilityUnknown = "ci-mergeability-unknown"
)

// The identifiers of the two holds that are a person's decision from the
// start rather than a wait that ran out, so ChecksTimeoutHeld never reads
// either as the elapsed hold.
const (
	// findingConflicted is the hold for a pull request the provider reports
	// as conflicting with its base.
	findingConflicted = "ci-conflicted"
	// findingUnpushedRound is the hold for a commit this stage's own fix
	// round made where the path back to review cannot take it, so nothing in
	// this run pushes it.
	findingUnpushedRound = "ci-fix-not-pushed"
)

// ChecksTimeoutHeld reports whether report is the checks stage holding
// because checks_timeout elapsed, and nothing else: the hold PRD section 11's
// recovery step 3 has the service re-read the pull request before resuming,
// and the only hold it may.
//
// It is true exactly when the report holds and every finding it holds on is
// one of those an elapsed wait raises. A report holding on anything else -
// the stage's own hold on a run with no pull request, on a conflict, on a fix
// round's commit nothing pushes, or a hold a fix round
// raised that the stage node appended, such as the judge rule's - is a hold
// on a person's decision, and a pull request's state must not answer a
// question a person was asked, so a report carrying both is not this hold
// either. A report that holds on nothing is not a hold at all.
func ChecksTimeoutHeld(report findings.Report) bool {
	held := report.Held()
	if len(held) == 0 {
		return false
	}
	for _, f := range held {
		if f.ID != findingChecksRunning && f.ID != findingNoChecks && f.ID != findingMergeabilityUnknown {
			return false
		}
	}
	return true
}

// checksResultReport turns a settled checks result into the stage's report.
// The two verdicts that are not settled never reach it: runChecks waits
// through them and reports an elapsed wait with checksElapsedReport.
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

// checksElapsedReport is the report for checks that had not settled when
// checks_timeout elapsed. It holds, which is PRD section 10's rule: the run
// holds rather than ending, and a person decides what a wait that ran out
// means.
//
// It names the configured timeout and never when the wait was armed or the
// base commit it was armed against. The report is recorded in the run's
// graph state, which is the convergence fingerprint, and an instant there
// would make two otherwise identical elapses differ; when a wait began is the
// store's row, and only that.
//
// unanswered says a read made once the wait had run out did not answer within
// its own bound, so result is what the read before it saw rather than what the
// provider says now.
func checksElapsedReport(result forge.ChecksResult, prNumber int, timeout time.Duration, unanswered bool) findings.Report {
	report := findings.Report{Revision: result.HeadCommit}
	elapsed := fmt.Sprintf("checks_timeout (%s) elapsed", timeout)
	if unanswered {
		elapsed += ", and a read of the pull request, of the checks or of the base branch made as it ran out " +
			"did not answer in time, so what follows is what the read before it saw"
	}
	answers := "Approving carries the run forward before the checks settle; skipping records this stage as " +
		"skipped; cancelling ends the run."

	if result.Verdict == forge.VerdictPassed {
		// Passed checks reach an elapsed wait only when the provider never
		// reported the pull request mergeable in the reads the wait made.
		report.Summary = fmt.Sprintf(
			"Checks passed on pull request %d, but the code host had not reported whether it can be merged; %s",
			prNumber, elapsed)
		report.Findings = append(report.Findings, findings.Finding{
			ID:       findingMergeabilityUnknown,
			Severity: findings.SeverityWarning,
			Action:   findings.ActionAsk,
			Description: "Every registered check passed, but the code host did not report this pull request " +
				"as mergeable into its base, so the checks stage cannot establish that it merges. It waited for " +
				"that answer until " + elapsed + ". Approving carries the run forward without it; skipping " +
				"records this stage as skipped; cancelling ends the run.",
		})
		return report
	}
	if result.Verdict == forge.VerdictNoChecks {
		report.Summary = fmt.Sprintf(
			"No checks registered on pull request %d and nothing declares that this repository has none; %s",
			prNumber, elapsed)
		report.Findings = append(report.Findings, findings.Finding{
			ID:       findingNoChecks,
			Severity: findings.SeverityWarning,
			Action:   findings.ActionAsk,
			Description: "No checks are registered on this pull request's head and configuration does not declare " +
				"that this repository has none, so the checks stage cannot establish whether CI would pass. " +
				"It waited for checks to register until " + elapsed + ". " + answers,
		})
		return report
	}

	waiting := result.Waiting()
	report.Summary = fmt.Sprintf(
		"Checks on pull request %d did not settle; %s. Still waiting on %d check(s): %s",
		prNumber, elapsed, len(waiting), checksNames(waiting))
	report.Findings = append(report.Findings, findings.Finding{
		ID:       findingChecksRunning,
		Severity: findings.SeverityInfo,
		Action:   findings.ActionAsk,
		Description: fmt.Sprintf(
			"Checks have not settled and the checks stage stopped waiting for them: %s. Still waiting on: %s\n\n%s",
			elapsed, checksNames(waiting), answers),
	})
	return report
}

// findingRebasing is the note a conflict that takes the path back to rebase
// carries, saying why the run went back.
const findingRebasing = "ci-conflicted-rebasing"

// rebasingReport is the report for a pull request the provider reports as
// conflicting with its base, when the run answers it by going back to rebase.
// It passes on its own, so internal/pipeline records OutcomeRebase for it and
// takes the path; its one finding is a note saying what was read and what
// happens next, for the pull request stage's account of the run.
//
// Like conflictedReport it names nothing that varies between two reads of the
// same conflict, because the report is recorded in the run's graph state.
func rebasingReport(pr forge.PullRequest, facts ciFacts) findings.Report {
	base := pr.Base
	if base == "" {
		base = facts.base
	}
	return findings.Report{
		Revision: pr.HeadCommit,
		Summary: fmt.Sprintf("Pull request %d conflicts with its base branch %s, as the code host reports it, "+
			"so its checks were not read and the run goes back to rebase %s onto %s.", pr.Number, base, facts.pushed, base),
		Findings: []findings.Finding{{
			ID:       findingRebasing,
			Severity: findings.SeverityInfo,
			Action:   findings.ActionNote,
			Description: fmt.Sprintf("The code host reports that pull request %d cannot be merged into %s because "+
				"the two conflict. The run goes back to the rebase stage, which rebases %s, the commit this run "+
				"pushed, onto %s only if the rebase applies cleanly and only while the branch still stands at "+
				"that commit; this run never decides what conflicting content should be, and holds for you "+
				"instead. A clean rebase is reviewed again, passes the checks before push again, and is pushed "+
				"on an anchor at the commit this run pushed before this stage reads the pull request again.",
				pr.Number, base, facts.pushed, base),
		}},
	}
}

// conflictedReport is the report for a pull request the provider reports as
// conflicting with its base, when the run cannot take the path back to
// rebase. It holds, and it says why the run did not rebase, that this run
// will not resolve the conflict, and what a person does instead.
//
// It names the pull request's head as the provider read it and nothing that
// varies between two reads of the same conflict, because the report is
// recorded in the run's graph state.
func conflictedReport(pr forge.PullRequest, facts ciFacts) findings.Report {
	base := pr.Base
	if base == "" {
		base = facts.base
	}
	why := facts.whyNotRebased()
	return findings.Report{
		Revision: pr.HeadCommit,
		Summary: fmt.Sprintf("Pull request %d conflicts with its base branch %s, as the code host reports it, "+
			"so its checks were not read. This run does not rebase it: %s", pr.Number, base, why),
		Findings: []findings.Finding{{
			ID:       findingConflicted,
			Severity: findings.SeverityError,
			Action:   findings.ActionAsk,
			Description: fmt.Sprintf("The code host reports that pull request %d cannot be merged into %s because "+
				"the two conflict. %s Resolving a conflict means deciding what the conflicting content should "+
				"be, which this run never decides for you. Rebase the branch onto %s yourself and push it "+
				"through the gate: that starts a new run, which reviews the result before it reaches the pull "+
				"request. Approving carries this run past a pull request that cannot merge; skipping records "+
				"this stage as skipped; cancelling ends the run.",
				pr.Number, base, why, base),
		}},
	}
}

// unpushedRoundReport is the report for a run whose head a fix round of this
// stage moved off the pushed commit, when the path back to review cannot take
// it. It holds, names both commits, says why the repair was not sent back,
// and says the round's commit will not reach the pull request: nothing in
// this run reviews or pushes it, so reading the checks again could only read
// the pushed commit's.
func unpushedRoundReport(facts ciFacts) findings.Report {
	pushed := facts.pushed
	if pushed == "" {
		pushed = "(nothing: the push stage forwarded no commit in this run)"
	}
	why := facts.whyNotRepaired()
	return findings.Report{
		Revision: facts.head,
		Summary: fmt.Sprintf("A fix round at the checks stage committed %s in this run's isolated copy. It will not "+
			"be reviewed or pushed by this run, so pull request %d still stands on %s. %s",
			facts.head, facts.pullRequest, pushed, why),
		Findings: []findings.Finding{{
			ID:       findingUnpushedRound,
			Severity: findings.SeverityWarning,
			Action:   findings.ActionAsk,
			Description: fmt.Sprintf("This stage's fix round committed %s on top of %s, the commit the push stage "+
				"forwarded. A commit a fix round writes after the push reaches the pull request only once the review "+
				"stage has reviewed it again and the push stage has pushed it, and this run cannot take it there: %s "+
				"The pull request and its checks still stand on %s. To use the round's change, take %s from this "+
				"run's isolated copy and push it through the gate, which starts a run that reviews it. Approving "+
				"carries this run past the checks that failed; skipping records this stage as skipped; cancelling "+
				"ends the run.", facts.head, pushed, why, pushed, facts.head),
		}},
	}
}

// repairResubmitReport is the report for a repair the run already sent back
// to review without pushing. It carries a fix finding so the resubmission
// counts toward the checks stage's fix round limit rather than taking the path
// back to review again without another fix round.
func repairResubmitReport(facts ciFacts) findings.Report {
	return findings.Report{
		Revision: facts.head,
		Summary: fmt.Sprintf("Pull request %d still stands on %s, and the repair %s was already sent back for "+
			"review without being pushed, so the checks still fail.", facts.pullRequest, facts.pushed, facts.head),
		Findings: []findings.Finding{{
			ID:       "ci-check-failed",
			Severity: findings.SeverityError,
			Action:   findings.ActionFix,
			Description: fmt.Sprintf("The repair %s is still not on the pull request, which stands on %s. The run "+
				"already sent that repair back for review once without a second push, so asking again counts as "+
				"another fix round at this stage rather than a second trip through review without one.",
				facts.head, facts.pushed),
		}},
	}
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
