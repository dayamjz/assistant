package stages

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/scope"
	"github.com/dayamjz/assistant/internal/vcs"
)

// Review is the review stage: an independent reading of the change against the
// diff and the recorded intent. It is the stage that decides whether a change
// is good, and every other stage exists to make its verdict trustworthy or to
// act on it.
//
// # What it establishes
//
// It derives the change from the repository rather than from anything an agent
// says: the paths the change touched and the diff itself come from the run's
// isolated copy, and the commit under review comes from the run's state. Those
// three are what the reviewer is held to, and they are why a reviewer cannot
// move the target. It then runs one session-free agent invocation and turns
// what came back into the stage's report.
//
// # P4, and why nothing here arranges it
//
// Reviewing and fixing are separate roles with separate memory. This body does
// nothing to arrange that, because there is nothing here it could arrange: the
// agent it is handed is an agents.StageAgent, whose Run takes no session and
// which is not a route to one, and agents.Invocation carries no field a
// session could be named in. A review that resumed the session which
// prescribed a fix is unsayable at this seam rather than avoided by this code,
// which is what internal/stages' TestStageDepsIsNoRouteToAFixerSession holds
// the wiring to.
//
// The gap that leaves is agents.StageAgent's own and it is worth naming where
// a reader meets it. This package imports internal/agents, and StageDeps.Config
// carries the ordered agent list agents.Resolve takes, so a body that went
// looking for its own adapter could open a fixer session without having been
// handed a route to one. This body does not go looking, and nothing but review
// stops the next one from doing so.
//
// # P5: a fix round's work is author code
//
// The stage takes automatic fix rounds, config.FixRounds.Review, over
// fix-eligible findings only; internal/pipeline owns that eligibility rule and
// an ask finding never reaches a round. What this body owns is the re-review.
// A round leaves the previous report in this stage's report key and the
// fixer's sanitized summary in its fix key, and both are read and put in front
// of the next reviewer as claims to check rather than as evidence that
// anything was resolved. The reviewer is told in the same breath that the
// commit it is reading is author code.
//
// What crosses from the round is the commit, which is why it has to be read as
// author code, and those two texts. Nothing of the round's reasoning crosses:
// internal/pipeline builds a new body for every execution and
// agents.StageAgent.Run carries no session, so whatever the round knew and did
// not write down is gone by the time the next reviewer is asked.
//
// # The scope lens
//
// internal/scope ships on, has no off switch, and is applied here. Its
// guidance goes into the prompt, the reviewer answers it as findings.Trace
// entries on its report, and scope.Observe turns the paths those do not
// account for into notes appended to the report. The reviewer cannot make a
// note disappear by staying silent about a path, because the paths come from
// the run.
//
// A run with no recorded intent has nothing to trace to. The lens refuses that
// with scope.ErrNoIntent rather than reporting every path as unexplained, and
// this body reports the refusal as a note of its own instead of hiding it: the
// lens not having run is a fact about the review, and a silent skip would read
// exactly like a change whose every path was accounted for.
//
// # Where the line between an error and a finding falls
//
// A stage body returns an error only when the stage could not run at all, and
// that line is drawn here at the agent. Everything before it - the run's own
// state, the isolated copy, the merge base, the changed files, the diff - is
// this build's own machinery, and a failure in it means nothing was reviewed
// and nothing can be. Those are returned as errors.
//
// Size is the one thing on that side of the line that is not machinery. A read
// git refused for producing more output than internal/vcs takes in one piece
// says the change is too large to read, which is a property of the change and
// the same fact the prompt bound reports one threshold further on. Both come
// to tooLargeToReview and hold for a person, so which cap a change crossed
// decides what the person is told and never whether they are asked.
//
// The agent's own failure is the other side. The stage ran, it asked, and it
// did not get back a review it could use: an agent that crashed, timed out,
// exceeded its output limit, reported its own failure, or printed something
// findings.ParseReviewReport refused. PRD section 5 makes "a stage could not
// gather enough evidence" an ask finding, so that is what this reports, and
// the run holds for a person rather than stopping. A cancelled run is not that
// case and is returned as the error it is.
//
// # What PRD section 5 asks of review that this does not do
//
// "Recurring findings become a proposal, never a rule" is a reading across
// runs of one repository: a class of finding that keeps coming back is raised
// as a note proposing a path-scoped review rule for a person to accept. This
// body sees one run and reads no history, so it cannot see a class recurring
// and reports nothing about one. No seam for it ships either, on this
// package's rule that surface answers to a consumer that exists: whoever
// builds that reading owns deciding where the history is read from.
//
// # What it does not establish
//
// It writes no state key, and in particular it does not write
// pipeline.KeyApproved. PRD section 5 has the push stage require a durable
// record that a completed review approved a commit this one descends from, and
// a review completes two ways: this body's report can pass on its own, or a
// person can approve at the stage's hold. Only the first is visible here, so a
// key written here would record one of the two and read as though it recorded
// both. The stage's outcome and the run's head are what a later stage has to
// read, and whoever builds that stage owns turning them into the record it
// needs.
//
// # The residual gaps
//
//   - The diff handed to the reviewer is the whole diff. config.IgnorePatterns
//     excludes paths from review, and that exclusion reaches the touched set,
//     the evidence demand, and the scope lens, all of which are matched by
//     config.PatternSet, which owns what a pattern matches. It does not reach
//     the diff text, because filtering that would mean either a second matcher
//     spelled in git's pathspec language or a unified-diff parser here. The
//     reviewer is told which paths are excluded and asked not to report on
//     them, and asking is all that is: a finding located in an excluded path is
//     reported like any other.
//   - The evidence set is the reviewer's own claim, and no path here is
//     resolved against a filesystem. internal/findings owns that limit.
//   - A trace is recorded and not verified. internal/scope owns that limit.
func Review(deps StageDeps) pipeline.Implementation {
	return pipeline.Implementation{
		Reads: []pipeline.Key{
			pipeline.KeyRepository,
			pipeline.KeyRun,
			pipeline.KeyBranch,
			pipeline.KeyBase,
			pipeline.KeyHead,
			pipeline.KeyIntent,
			pipeline.KeyIntentSupplied,
			// The two a re-review is held to. PRD section 5 gives the round's
			// verifier the previous findings and the fix summary as claims,
			// and the schema admits a stage reading its own keys: it bounds
			// what a stage may write and never what it may read.
			pipeline.StageReview.ReportKey(),
			pipeline.StageReview.FixKey(),
		},
		NewBody: func() pipeline.Body {
			return func(ctx context.Context, in pipeline.Input) (pipeline.Output, error) {
				return reviewChange(ctx, deps, in)
			}
		},
	}
}

// reviewChange is the stage body.
func reviewChange(ctx context.Context, deps StageDeps, in pipeline.Input) (pipeline.Output, error) {
	facts, err := readReviewState(in.State)
	if err != nil {
		return pipeline.Output{}, err
	}
	repo, err := deps.Copy(ctx, facts.repository, facts.run)
	if err != nil {
		return pipeline.Output{}, err
	}
	change, err := readChange(ctx, repo, facts, deps.Config.IgnorePatterns)
	if err != nil {
		if errors.Is(err, vcs.ErrOutputTooLarge) {
			return tooLargeToReview(change, "The change at "+facts.head+" could not be read "+
				"from git in one piece: "+err.Error()+"."), nil
		}
		return pipeline.Output{}, err
	}
	if len(change.touched) == 0 {
		return nothingToReview(change), nil
	}

	demand := findings.Demand{Revision: facts.head, Touched: change.touched}
	evidence, err := demand.Guidance()
	if err != nil {
		// The demand is built from the run's own facts, so a refusal here says
		// the run reached this stage without a commit to review, which is a
		// state no run presents and nothing this body can review around.
		return pipeline.Output{}, fmt.Errorf("stages: the review stage cannot bind a report of run %s: %w",
			facts.run, err)
	}
	scoped := scope.Change{Intent: facts.intent, Supplied: facts.supplied, Touched: change.touched}
	scopeGuidance, scopeErr := scope.Guidance(scoped)

	prompt := reviewPrompt(deps.Config, facts, change, evidence, scopeGuidance)
	if len(prompt) > agents.MaxPromptBytes {
		return tooLargeToReview(change, "Reviewing "+facts.head+" would take a prompt of "+
			strconv.Itoa(len(prompt))+" bytes, and one invocation may carry "+
			strconv.Itoa(agents.MaxPromptBytes)+"."), nil
	}

	result, err := deps.Agent.Run(ctx, agents.PurposeReview, agents.Invocation{
		Prompt: prompt,
		Shape:  agents.ShapeReview,
		Review: demand,
		Dir:    repo.Path(),
	})
	if err != nil {
		return unreviewed(ctx, facts, err)
	}

	report := result.Report
	report.Findings = append(report.Findings, observeScope(scoped, report.Traced, scopeErr)...)
	return pipeline.Output{Report: report}, nil
}

// reviewFacts is what the run's own state says about the change under review.
// Every field comes from the run and none from an agent, which is what makes
// the evidence demand and the scope lens able to fail.
type reviewFacts struct {
	repository string
	run        string
	branch     string
	base       string
	head       string
	intent     string
	supplied   bool
	// previous is the report this stage recorded last time it ran, which is
	// empty on the first round. Its findings are the claims a re-review checks.
	previous findings.Report
	// fixSummary is the sanitized account the last fix round wrote, empty when
	// no round has run.
	fixSummary string
}

// rereview reports whether this execution is verifying a fix round's work.
//
// Either half of what a round leaves behind is enough. A round whose fixer
// reported no summary still ran, and it is the report of the review before it
// that says what it was sent to do; a round that reported one still leaves
// that report behind. Only a first execution has neither, because this stage
// re-runs for no other reason than a round having happened.
func (f reviewFacts) rereview() bool {
	return f.fixSummary != "" || len(f.previous.Findings) > 0
}

// readReviewState reads everything the stage takes from the run.
func readReviewState(state pipeline.Reader) (reviewFacts, error) {
	var f reviewFacts
	for _, field := range []struct {
		key  pipeline.Key
		into *string
	}{
		{pipeline.KeyRepository, &f.repository},
		{pipeline.KeyRun, &f.run},
		{pipeline.KeyBranch, &f.branch},
		{pipeline.KeyBase, &f.base},
		{pipeline.KeyHead, &f.head},
		{pipeline.KeyIntent, &f.intent},
		{pipeline.StageReview.FixKey(), &f.fixSummary},
	} {
		value, err := state.Get(field.key)
		if err != nil {
			return reviewFacts{}, err
		}
		text, _ := value.Text()
		*field.into = strings.TrimSpace(text)
	}
	supplied, err := state.Get(pipeline.KeyIntentSupplied)
	if err != nil {
		return reviewFacts{}, err
	}
	f.supplied, _ = supplied.Bool()
	f.previous, err = pipeline.ReadStageReport(state, pipeline.StageReview)
	if err != nil {
		return reviewFacts{}, err
	}
	return f, nil
}

// reviewChangeSet is the change as the repository describes it: what the
// reviewer is asked about, what was excluded from that, and the diff itself.
type reviewChangeSet struct {
	// from is the commit the change is measured against. It is the merge base
	// of the run's target and its head rather than the target itself, so the
	// set is the change's own work whether or not the rebase stage has already
	// put the head on top of the target.
	from string
	// touched is the repository-relative paths the change touched and the
	// review is asked about, in the order git reported them.
	touched []string
	// excluded is the paths the change touched that config.IgnorePatterns
	// keeps out of review. It is reported rather than dropped, so a review
	// covering less than the change is a fact a person can see.
	excluded []string
	// diff is the unified diff of the whole change, excluded paths included.
	// Review's documentation states why.
	diff string
}

// readChange derives the change from the run's isolated copy.
func readChange(ctx context.Context, repo *vcs.Repository, f reviewFacts, ignore config.PatternSet) (reviewChangeSet, error) {
	from, err := repo.MergeBase(ctx, f.base, f.head)
	if err != nil {
		return reviewChangeSet{}, fmt.Errorf("stages: the review stage cannot locate where %s left %s: %w",
			f.head, f.base, err)
	}
	changed, err := repo.ChangedFiles(ctx, from, f.head)
	if err != nil {
		return reviewChangeSet{}, fmt.Errorf("stages: the review stage cannot list what %s changed: %w", f.head, err)
	}
	set := reviewChangeSet{from: from}
	for _, path := range changedPaths(changed) {
		if ignore.Matches(path) {
			set.excluded = append(set.excluded, path)
			continue
		}
		set.touched = append(set.touched, path)
	}
	if len(set.touched) == 0 {
		return set, nil
	}
	if set.diff, err = repo.Diff(ctx, from, f.head); err != nil {
		// The set travels with the refusal, so a caller that turns a diff too
		// large to read into a report can still say how much the change touches.
		return set, fmt.Errorf("stages: the review stage cannot read the diff of %s: %w", f.head, err)
	}
	return set, nil
}

// changedPaths is the repository-relative paths a change touched, in the order
// git reported them and with repeats collapsed.
//
// A rename contributes both of its paths, because both locations changed and a
// finding may need to name either. A deletion contributes the one path it has,
// which vcs.FileChange already reports as the path in the earlier commit.
//
// A copy contributes only the new one. The property that holds whatever git's
// copy detection does is the one this is written for: nothing enters the set
// on the strength of having been copied from, so a file this change left alone
// is never admitted as though it sat inside the change, where a claim about it
// would pass the evidence rule for free. Whether internal/vcs's diff options
// can produce such a source at all is that package's business and this does
// not depend on the answer.
func changedPaths(changed []vcs.FileChange) []string {
	seen := make(map[string]struct{}, len(changed))
	out := make([]string, 0, len(changed))
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		if _, dup := seen[path]; dup {
			return
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	for _, c := range changed {
		add(c.Path)
		if c.Status == vcs.StatusRenamed {
			add(c.OldPath)
		}
	}
	return out
}

// observeScope is what the lens comes to. It is one function so that the
// answer and the refusal have one owner: a lens that did not run is reported
// as a note saying so, because a silent skip reads exactly like a change every
// path of which was accounted for.
//
// refused is the refusal scope.Guidance already gave for this change, so the
// prompt the reviewer saw and the notes reported here agree about whether the
// lens applied. Observe is asked again rather than assumed, and its own
// refusal takes the same path, so neither call can quietly become the one that
// decides.
//
// The note carries no identifier of its own. It is appended to a report an
// agent wrote, an identifier a finding arrived with survives
// findings.Normalize verbatim, and a literal here would collide with a
// reviewer that happened to write the same string. Validate refuses a set with
// duplicates whole, so that collision would fail the run over agent output
// that everywhere else in this stage holds it for a person.
// findings.NormalizeFindings derives one instead, checked against every
// identifier the report already carries.
func observeScope(c scope.Change, traced []findings.Trace, refused error) []findings.Finding {
	if refused == nil {
		notes, err := scope.Observe(c, traced)
		if err == nil {
			return notes
		}
		refused = err
	}
	return []findings.Finding{{
		Severity: findings.SeverityInfo,
		Action:   findings.ActionNote,
		Description: "The scope lens did not run over this change, so nothing here says whether " +
			"what the change touches was asked for: " + refused.Error() + ". This is " +
			"informational and blocks nothing. A run started without an intent reaches this " +
			"honestly rather than through a defect, and supplying an intent is what gives the " +
			"lens something to measure against.",
	}}
}

// nothingToReview is the report for a change with no path left to review. It
// is a pass, because there is nothing here to establish rather than something
// established without looking: the run's own diff decides which of the two
// sentences below applies, and both are reported as notes so the run advances.
//
// A change that touched nothing at all does not normally reach this stage, as
// PRD section 5 has the rebase stage end such a run at the empty-diff short
// circuit. It is reported apart from an excluded change all the same, because
// the two are different facts and the one that is not supposed to happen is
// the one worth being able to see.
func nothingToReview(change reviewChangeSet) pipeline.Output {
	if len(change.excluded) == 0 {
		return pipeline.Output{Report: findings.Report{
			Summary: "The change touches no path, so there was nothing to review.",
			Findings: []findings.Finding{{
				ID:       "review-nothing-changed",
				Severity: findings.SeverityWarning,
				Action:   findings.ActionNote,
				Description: "This change touches no path at all, so the review read nothing and " +
					"established nothing about it. A run whose change is empty is normally ended " +
					"before this stage, so reaching it this way is worth knowing about.",
			}},
		}}
	}
	return pipeline.Output{Report: findings.Report{
		Summary: "Every path this change touches is excluded from review by the repository's " +
			"ignore patterns, so there was nothing to review.",
		Findings: []findings.Finding{{
			ID:       "review-everything-excluded",
			Severity: findings.SeverityWarning,
			Action:   findings.ActionNote,
			Description: "The configured ignore patterns exclude every one of the " +
				strconv.Itoa(len(change.excluded)) + " path(s) this change touches, so the review " +
				"read nothing and established nothing about it. The excluded paths are: " +
				strings.Join(change.excluded, ", ") + ".",
		}},
	}}
}

// tooLargeToReview is what a change too large to review in one piece comes to.
//
// Two bounds produce it and it is one report because it is one fact. A diff
// git produced more of than internal/vcs reads in one invocation is refused
// before the prompt is assembled, and agents.MaxPromptBytes bounds what one
// invocation may hand an agent, so a change that got past the first is
// measured against the second before it is sent rather than refused at the
// call. Which cap a change crossed decides only what the person is told, which
// is what crossed carries: a sentence about this change rather than about the
// threshold it met.
//
// It holds for a person rather than failing the run. Nothing was reviewed and
// nothing here can review it, which PRD section 5 makes an ask finding: a
// stage that could not gather enough evidence is the person's to resolve, and
// splitting a change that large is a decision rather than a repair.
//
// The reviewable path count is stated only when the change is known, because a
// read that failed before the paths were listed knows nothing about how many
// there are and would otherwise report none.
func tooLargeToReview(change reviewChangeSet, crossed string) pipeline.Output {
	scale := ""
	if len(change.touched) > 0 {
		scale = "The change touches " + strconv.Itoa(len(change.touched)) + " reviewable path(s). "
	}
	return pipeline.Output{Report: findings.Report{
		Summary: "This change is too large to review in one piece, so it was not reviewed.",
		Findings: []findings.Finding{{
			ID:       "review-change-too-large",
			Severity: findings.SeverityError,
			Action:   findings.ActionAsk,
			Description: crossed + " " + scale +
				"Nothing was reviewed, so nothing here says whether this change is good. " +
				"Splitting it into changes that can each be reviewed is the usual answer; " +
				"approving here records that the run advanced past a review that did not happen.",
		}},
	}}
}

// unreviewed is what an agent that did not come back with a usable review
// comes to.
//
// A cancelled run is returned as the error it is: the run is being stopped, so
// a finding asking a person to decide about it would be answered by nobody.
// That is what the guard below is for, and it asks for context.Canceled alone
// rather than reading ctx.Err() whole. A deadline that elapsed is the agent
// not coming back rather than the run being stopped, so this returns the ask
// for it; a guard reading ctx.Err() whole would take that case too, and
// agents.FailureTimeout is produced under exactly the condition such a guard
// tests, so the ask below would be unreachable for the failure most likely to
// need it.
//
// What the run then does with that ask is not what this body returns, and on
// the only path this build produces agents.FailureTimeout on the two do not
// agree. The deadline that elapsed is the run's own context, so the write that
// would record the hold is refused by the same expiry: no checkpoint carries
// the ask and it is discarded with the segment, which
// TestAnAgentWhoseDeadlineElapsedProducesAnAskTheRunCannotRecord drives and
// asserts. Making that ask durable means the deadline sitting on a context of
// internal/agents' own so the body's stays live, which is work in that package
// and is not done: nothing here or there arranges it today.
//
// Anything else that is an *agents.InvocationError is the agent's own failure -
// it crashed, timed out, overran its output limit, reported its own failure,
// or printed something findings.ParseReviewReport refused - and PRD section 5
// makes a stage that could not gather enough evidence an ask finding, so the
// run holds rather than stopping.
//
// Everything else is returned. agents.ErrNoStageAgent is a body that was wired
// with no agent and agents.ErrInvalidInvocation is a prompt this stage
// assembled wrongly; both are defects in this build rather than a review that
// came back empty, and dressing one as a question for a person would put the
// person in front of a decision they cannot make.
func unreviewed(ctx context.Context, f reviewFacts, err error) (pipeline.Output, error) {
	if errors.Is(ctx.Err(), context.Canceled) {
		return pipeline.Output{}, err
	}
	var invocation *agents.InvocationError
	if !errors.As(err, &invocation) {
		return pipeline.Output{}, fmt.Errorf("stages: the review stage could not run for run %s: %w", f.run, err)
	}
	return pipeline.Output{Report: findings.Report{
		Summary: "The reviewer did not come back with a review of this change, so nothing was established about it.",
		Findings: []findings.Finding{{
			ID:       "review-not-established",
			Severity: findings.SeverityError,
			Action:   findings.ActionAsk,
			Description: "The review of " + f.head + " produced no usable report (" +
				string(invocation.Failure) + "): " + invocation.Error() + ". Nothing was reviewed, " +
				"so nothing here says whether this change is good. Approving records that the run " +
				"advanced past a review that did not happen.",
		}},
	}}, nil
}
