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
// request's head and reports what the provider says once they settle. It does
// not judge mergeability.
//
// A pass establishes that every registered check settled and passed at the
// moment this invocation last read them. It does not establish that they stayed
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
// timeout, and the service's re-read at the hold that leaves is the next
// chance to notice. The refusal itself is a typed value this body reads and
// classifies as not read; nothing is logged and nothing is carried into the
// report, because an elapsed report may not vary with what one read said.
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
// An elapsed wait ends the row, as settled checks do. A wait whose segment
// ended any other way - a failed read, a cancellation, a supersession - leaves
// its row behind; internal/store's ChecksWait says what that costs.
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
		},
		Writes: []pipeline.Key{pipeline.KeyPullRequestEnd, pipeline.KeyPullRequestEndHead},
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
		checksReport, err := provider.Checks(readCtx, facts.pullRequest)
		if err != nil {
			cancel()
			if elapsedDuring(ctx, readCtx) {
				return w.elapsed(ctx, last, timeout, true)
			}
			return pipeline.Output{}, fmt.Errorf("stages: reading checks for pull request %d: %w", facts.pullRequest, err)
		}
		result := checksReport.Evaluate(forge.DeclaredNoCI(deps.Config))
		if settled(result.Verdict) {
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

// settled reports whether a verdict ends the wait. Passed and failed do; no
// checks and running are what the stage waits through, per forge's own
// statement of each.
func settled(v forge.Verdict) bool {
	switch v {
	case forge.VerdictPassed, forge.VerdictFailed:
		return true
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
	}
	for _, t := range texts {
		value, err := state.Get(t.key)
		if err != nil {
			return ciFacts{}, err
		}
		*t.into, _ = value.Text()
	}

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

// The identifiers of the two findings an elapsed wait holds on. They are
// what ChecksTimeoutHeld reads, so a report built elsewhere cannot pass as
// that hold by carrying the words.
const (
	// findingChecksRunning is the hold for checks that had not settled when
	// checks_timeout elapsed.
	findingChecksRunning = "ci-running"
	// findingNoChecks is the hold for no checks registered and no no_ci
	// declaration when checks_timeout elapsed.
	findingNoChecks = "ci-no-checks"
)

// ChecksTimeoutHeld reports whether report is the checks stage holding
// because checks_timeout elapsed, and nothing else: the hold PRD section 11's
// recovery step 3 has the service re-read the pull request before resuming,
// and the only hold it may.
//
// It is true exactly when the report holds and every finding it holds on is
// one of the two an elapsed wait raises. A report holding on anything else -
// the stage's own hold on a run with no pull request, or a hold a fix round
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
		if f.ID != findingChecksRunning && f.ID != findingNoChecks {
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
