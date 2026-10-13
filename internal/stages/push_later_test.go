package stages_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/stages"
)

// laterLandmark is a sentence only the push stage's review of later commits
// carries, which is how a script tells that review apart from the review
// stage's own. It is quoted from the prompt rather than shared with it, so a
// change to that wording fails these tests loudly instead of letting the
// script answer the wrong review.
const laterLandmark = "no review has read them"

// fixedLine is what the test stage's fix round writes into total.go: the line
// no review read when the review stage ran.
const fixedLine = "// clamp the sum so the targeted check passes"

// TestCommitsATestFixRoundWroteAfterReviewAreReviewedBeforeTheyArePushed is
// PRD principle P5 at the push stage, driven through a real pipeline: the
// review stage reads the change and passes, the test stage takes a fix round
// that commits code, and the push stage may not forward that code until a
// fresh review has read it.
//
// The review of the later commits is answered with a fix finding here, and
// what is asserted is that the run holds at push and the upstream never
// receives the branch. The positive control is the next test, which answers
// the same review cleanly over the same run and sees the fixed head arrive
// upstream: without it, a stage that never pushed anything would pass this.
func TestCommitsATestFixRoundWroteAfterReviewAreReviewedBeforeTheyArePushed(t *testing.T) {
	principles.Cite(t, principles.P5)
	principles.Cite(t, principles.P4)
	t.Parallel()

	objection := findings.Finding{
		ID:          "later-clamp-is-wrong",
		Severity:    findings.SeverityError,
		Action:      findings.ActionFix,
		Location:    findings.Location{Path: totalPath, Line: 5},
		Description: "the round clamped the sum to make the check pass instead of fixing the sum",
	}
	run := newLaterRun(t, pipeline.StageTest, standin.Reviewed("the round's clamp changes what Total returns", objection))
	result := run.execute(t)

	// The push stage takes no fix rounds, so a fix finding there holds for a
	// person exactly as an ask does: the run halts at the stage's hold.
	if result.Status != graph.StatusHalted || result.Position != pipeline.StagePush.HoldNode() {
		report, _ := pipeline.StageReport(result.State, pipeline.StagePush)
		t.Fatalf("the run is %s at %q over commits whose review objected, want halted at %q: %+v",
			result.Status, result.Position, pipeline.StagePush.HoldNode(), report)
	}
	report, err := pipeline.StageReport(result.State, pipeline.StagePush)
	if err != nil {
		t.Fatalf("reading the push stage's report: %v", err)
	}
	if !hasFinding(report, objection.ID) {
		t.Fatalf("the push stage holds without the finding the review of the later commits raised: %+v", report)
	}
	run.mustNotExistUpstream(t, "refs/heads/topic")

	calls := run.agent.Calls()
	if len(calls) != 3 {
		t.Fatalf("the agent was invoked %d time(s), want three - the review, the test stage's fix "+
			"round, and the review of what that round committed:\n%v", len(calls), calls)
	}
	later := calls[2]
	if !strings.Contains(later.Prompt, laterLandmark) {
		t.Fatalf("the last invocation is not the review of the later commits:\n%s", later.Prompt)
	}
	// P4: the review of the later commits is a review, so it carries no
	// session, and in particular not the fixer's whose round it is reading.
	if session := later.Session(); session != "" {
		t.Fatalf("the review of the later commits resumed session %q; a review is session-free", session)
	}
	// P5: what the round wrote is in front of the reviewer, measured from the
	// commit the review stage read, and the round's own account is there as a
	// claim rather than as the subject.
	if !strings.Contains(later.Prompt, fixedLine) {
		t.Fatalf("the review of the later commits was not shown the line the round wrote:\n%s", later.Prompt)
	}
	if !strings.Contains(later.Prompt, "measured against "+run.subject.head) {
		t.Fatalf("the review of the later commits is not measured from the reviewed commit %s:\n%s",
			run.subject.head, later.Prompt)
	}
	if !strings.Contains(later.Prompt, fixerAccount) {
		t.Fatalf("the round's own account did not reach the reviewer as a claim:\n%s", later.Prompt)
	}
	if strings.Contains(calls[1].Prompt, laterLandmark) || strings.Contains(calls[1].Prompt, "It must be exactly ") {
		t.Fatalf("the second invocation was expected to be the fix round:\n%s", calls[1].Prompt)
	}
}

// TestCommitsAReviewApprovesAfterTheTestFixRoundArePushed is the positive
// control for the test above: the same run, with the review of the later
// commits answered cleanly, forwards the fixed head and records it as the
// approved commit.
func TestCommitsAReviewApprovesAfterTheTestFixRoundArePushed(t *testing.T) {
	t.Parallel()

	run := newLaterRun(t, pipeline.StageTest, standin.Reviewed("the round's change is sound"))
	result := run.execute(t)

	if got := pipeline.StageOutcome(result.State, pipeline.StagePush); got != pipeline.OutcomePassed {
		report, _ := pipeline.StageReport(result.State, pipeline.StagePush)
		t.Fatalf("the push stage came to %s over commits a review approved, want passed: %+v", got, report)
	}
	head := run.copyHead(t)
	if head == run.subject.head {
		t.Fatalf("the fix round committed nothing, so this run reached push with no later commits")
	}
	if got := git(t, run.upstream, "rev-parse", "refs/heads/topic"); got != head {
		t.Fatalf("the upstream branch stands at %s, want the fixed head %s", got, head)
	}
	approved, _ := result.State.Get(string(pipeline.KeyApproved))
	if got, _ := approved.Text(); got != head {
		t.Fatalf("the approved commit is %q, and a review read every commit up to %s", got, head)
	}
	if calls := run.agent.Calls(); len(calls) != 3 {
		t.Fatalf("the agent was invoked %d time(s), want three", len(calls))
	}
}

// TestARunWithNoCommitsAfterReviewCostsNoSecondReview is the cost half of the
// decision: a head that is the reviewed commit is pushed on the review that
// read it, and the agent is invoked once.
//
// The script would answer a review of later commits with an objection, so a
// second review that ran here would not only be counted but would hold.
func TestARunWithNoCommitsAfterReviewCostsNoSecondReview(t *testing.T) {
	t.Parallel()

	run := newLaterRun(t, pipeline.StageInvalid, standin.Reviewed("objecting", findings.Finding{
		ID: "should-never-be-asked", Severity: findings.SeverityError, Action: findings.ActionAsk,
		Description: "a review of later commits ran on a run that had none",
	}))
	result := run.execute(t)

	if got := pipeline.StageOutcome(result.State, pipeline.StagePush); got != pipeline.OutcomePassed {
		report, _ := pipeline.StageReport(result.State, pipeline.StagePush)
		t.Fatalf("the push stage came to %s, want passed: %+v", got, report)
	}
	if got := git(t, run.upstream, "rev-parse", "refs/heads/topic"); got != run.subject.head {
		t.Fatalf("the upstream branch stands at %s, want the reviewed head %s", got, run.subject.head)
	}
	if calls := run.agent.Calls(); len(calls) != 1 {
		t.Fatalf("the agent was invoked %d time(s) for a run with no commits after review, want "+
			"the one review:\n%v", len(calls), calls)
	}
}

// TestCommitsALintFixRoundWroteAfterReviewAreReviewedBeforeTheyArePushed is
// the same rule for the lint stage, which is the other stage between review
// and push that takes fix rounds. Each half is the other's control: the
// objecting review holds and pushes nothing, and the approving one forwards
// the fixed head, so neither a stage that never pushes nor one that never
// reviews passes both.
func TestCommitsALintFixRoundWroteAfterReviewAreReviewedBeforeTheyArePushed(t *testing.T) {
	principles.Cite(t, principles.P5)
	t.Parallel()

	t.Run("an objection holds", func(t *testing.T) {
		t.Parallel()
		objection := findings.Finding{
			ID: "later-lint-round-is-wrong", Severity: findings.SeverityError, Action: findings.ActionAsk,
			Location:    findings.Location{Path: totalPath, Line: 5},
			Description: "the lint round changed a line of code, not only its formatting",
		}
		run := newLaterRun(t, pipeline.StageLint, standin.Reviewed("the lint round edited code", objection))
		result := run.execute(t)

		if result.Status != graph.StatusHalted || result.Position != pipeline.StagePush.HoldNode() {
			report, _ := pipeline.StageReport(result.State, pipeline.StagePush)
			t.Fatalf("the run is %s at %q, want halted at %q: %+v",
				result.Status, result.Position, pipeline.StagePush.HoldNode(), report)
		}
		report, err := pipeline.StageReport(result.State, pipeline.StagePush)
		if err != nil {
			t.Fatalf("reading the push stage's report: %v", err)
		}
		if !hasFinding(report, objection.ID) {
			t.Fatalf("the push stage holds without the later review's finding: %+v", report)
		}
		run.mustNotExistUpstream(t, "refs/heads/topic")

		calls := run.agent.Calls()
		if len(calls) != 3 {
			t.Fatalf("the agent was invoked %d time(s), want the review, the lint round, and the "+
				"review of what it committed:\n%v", len(calls), calls)
		}
		later := calls[2]
		if !strings.Contains(later.Prompt, laterLandmark) {
			t.Fatalf("the last invocation is not the review of the later commits:\n%s", later.Prompt)
		}
		if !strings.Contains(later.Prompt, fixedLine) {
			t.Fatalf("the review of the later commits was not shown the line the lint round wrote:\n%s", later.Prompt)
		}
		if !strings.Contains(later.Prompt, "fix round of the lint stage") {
			t.Fatalf("the lint round's account did not reach the reviewer as the lint stage's:\n%s", later.Prompt)
		}
	})

	t.Run("an approval pushes", func(t *testing.T) {
		t.Parallel()
		run := newLaterRun(t, pipeline.StageLint, standin.Reviewed("the lint round's change is sound"))
		result := run.execute(t)

		if got := pipeline.StageOutcome(result.State, pipeline.StagePush); got != pipeline.OutcomePassed {
			report, _ := pipeline.StageReport(result.State, pipeline.StagePush)
			t.Fatalf("the push stage came to %s over commits a review approved, want passed: %+v", got, report)
		}
		head := run.copyHead(t)
		if head == run.subject.head {
			t.Fatalf("the lint round committed nothing, so this run reached push with no later commits")
		}
		if got := git(t, run.upstream, "rev-parse", "refs/heads/topic"); got != head {
			t.Fatalf("the upstream branch stands at %s, want the fixed head %s", got, head)
		}
	})
}

// TestALaterReviewThatDoesNotRunHoldsThePush is the failure path of the review
// of later commits: a reviewer that comes back with nothing is not a review
// that approved, so the push stage holds on the review stage's own
// review-not-established finding and the upstream never receives the branch.
//
// The positive control is TestCommitsAReviewApprovesAfterTheTestFixRoundArePushed,
// the same run with the later review answered, which does push.
func TestALaterReviewThatDoesNotRunHoldsThePush(t *testing.T) {
	principles.Cite(t, principles.P5)
	t.Parallel()

	run := newLaterRun(t, pipeline.StageTest, standin.Failed("the reviewer crashed"))
	result := run.execute(t)

	if result.Status != graph.StatusHalted || result.Position != pipeline.StagePush.HoldNode() {
		report, _ := pipeline.StageReport(result.State, pipeline.StagePush)
		t.Fatalf("the run is %s at %q after the later review failed, want halted at %q: %+v",
			result.Status, result.Position, pipeline.StagePush.HoldNode(), report)
	}
	report, err := pipeline.StageReport(result.State, pipeline.StagePush)
	if err != nil {
		t.Fatalf("reading the push stage's report: %v", err)
	}
	if !hasFinding(report, "review-not-established") {
		t.Fatalf("the push stage holds without saying the later review did not happen: %+v", report)
	}
	run.mustNotExistUpstream(t, "refs/heads/topic")
	calls := run.agent.Calls()
	if len(calls) != 3 || !strings.Contains(calls[2].Prompt, laterLandmark) {
		t.Fatalf("want the review, the round, and a failed review of the later commits, got:\n%v", calls)
	}
}

// TestAHeadAReviewFixRoundWroteCostsNoSecondReview is the cost half of the
// decision for review's own fix round: the review stage runs again after that
// round and its report revision is the commit the round made, so the head the
// push stage forwards is the reviewed commit and no review of later commits
// runs.
//
// The script would answer a review of later commits with an objection, so one
// that ran here would hold the push as well as being counted. That the head
// moved off the submitted commit is asserted too, so the test cannot pass on
// a run whose round committed nothing.
func TestAHeadAReviewFixRoundWroteCostsNoSecondReview(t *testing.T) {
	principles.Cite(t, principles.P5)
	t.Parallel()

	run := newLaterRun(t, pipeline.StageReview, standin.Reviewed("objecting", findings.Finding{
		ID: "should-never-be-asked", Severity: findings.SeverityError, Action: findings.ActionAsk,
		Description: "a review of later commits ran over a head the review stage had read",
	}))
	result := run.execute(t)

	if got := pipeline.StageOutcome(result.State, pipeline.StagePush); got != pipeline.OutcomePassed {
		report, _ := pipeline.StageReport(result.State, pipeline.StagePush)
		t.Fatalf("the push stage came to %s, want passed: %+v", got, report)
	}
	head := run.copyHead(t)
	if head == run.subject.head {
		t.Fatalf("the review's fix round committed nothing, so this run never moved its head")
	}
	review, err := pipeline.StageReport(result.State, pipeline.StageReview)
	if err != nil {
		t.Fatalf("reading the review stage's report: %v", err)
	}
	if review.Revision != head {
		t.Fatalf("the review stage's report names %s, want the round's commit %s", review.Revision, head)
	}
	if got := git(t, run.upstream, "rev-parse", "refs/heads/topic"); got != head {
		t.Fatalf("the upstream branch stands at %s, want the re-reviewed head %s", got, head)
	}
	calls := run.agent.Calls()
	for _, call := range calls {
		if strings.Contains(call.Prompt, laterLandmark) {
			t.Fatalf("a review of later commits ran over a head the review stage had read:\n%s", call.Prompt)
		}
	}
	if len(calls) != 3 {
		t.Fatalf("the agent was invoked %d time(s), want the review, its fix round, and the "+
			"re-review:\n%v", len(calls), calls)
	}
}

// fixerAccount is what the scripted fixer says it did.
const fixerAccount = "clamped the sum in total.go so the targeted check passes"

// laterRun is a pipeline whose review, fix path, and push stage are the real
// ones, over a subject with an upstream to push to.
type laterRun struct {
	subject  *subject
	upstream string
	agent    *standin.Agent
	exec     *graph.Executor
	state    graph.State
}

// newLaterRun builds the run. fixAt is the stage that takes one fix round,
// or pipeline.StageInvalid for none. A test or lint stage named there reports
// a fix finding on its first execution and passes once a round has run; the
// review stage named there is answered with a fix finding by its first review
// and cleanly after. Either way the round writes fixedLine into total.go
// before the real fix body commits it: the stand-in prints and cannot edit a
// working copy, so what an agent would have changed is put there the way
// fix_test.go puts it, and everything after the edit is the product's.
//
// later is how the review of later commits is answered. The review stage's
// own review is otherwise answered cleanly, so the only thing that can hold
// the push is the review this decision added.
func newLaterRun(t *testing.T, fixAt pipeline.Stage, later standin.Reply) *laterRun {
	t.Helper()
	s := newSubject(t)
	git(t, s.dir, "config", "user.name", "test")
	git(t, s.dir, "config", "user.email", "test@example.invalid")
	upstream := filepath.Join(t.TempDir(), "upstream.git")
	git(t, filepath.Dir(upstream), "init", "--quiet", "--bare", "-b", "main", upstream)
	git(t, s.dir, "remote", "add", stages.UpstreamRemote, upstream)
	git(t, s.dir, "push", "--quiet", stages.UpstreamRemote, "main")

	steps := []standin.Step{
		{Match: standin.Match{PromptContains: laterLandmark}, Times: standin.Always, Reply: later},
	}
	if fixAt == pipeline.StageReview {
		steps = append(steps, standin.Step{Match: standin.MatchReview(), Reply: standin.Reviewed(
			"the sum starts from the wrong place", findings.Finding{
				ID: "review-wants-a-fix", Severity: findings.SeverityError, Action: findings.ActionFix,
				Location:    findings.Location{Path: totalPath, Line: 5},
				Description: "the sum's starting comment does not say what the loop does",
			})})
	}
	steps = append(steps,
		standin.Step{Match: standin.MatchReview(), Times: standin.Always, Reply: standin.Reviewed("nothing to report")},
		standin.Step{Times: standin.Always, Reply: standin.Text(fixerAccount)},
	)
	agent := standin.New(t, standin.Script{Steps: steps})

	deps := stages.NewStageDeps(agents.NewStageAgent(agent.Runner()), s.home, config.Defaults(), nil, redact.New())
	fixDeps := stages.NewFixDeps(
		func(ctx context.Context, _ string) (agents.Fixer, error) {
			return agents.OpenFixer(ctx, agent.Runner(), "")
		},
		s.home, config.Defaults(), nil, redact.New(),
	)

	all := pipeline.ConstantStages("nothing to report")
	all.Rebase = anchoringRebase(t)
	all.Review = stages.Review(deps)
	all.Test = failingUntilFixed(pipeline.StageTest, fixAt == pipeline.StageTest)
	all.Lint = failingUntilFixed(pipeline.StageLint, fixAt == pipeline.StageLint)
	all.Push = stages.Push(deps)

	real := stages.Fix(fixDeps)
	fixer := real
	fixer.NewBody = func() pipeline.FixBody {
		body := real.NewBody()
		return func(ctx context.Context, in pipeline.FixInput) (pipeline.FixOutput, error) {
			write(t, s.dir, totalPath, strings.Replace(baseTotal, "sum := 0", "sum := 0 "+fixedLine, 1))
			return body(ctx, in)
		}
	}

	p, err := pipeline.New(pipeline.Options{
		Stages: all,
		Rounds: config.FixRounds{Review: 1, Test: 1, Lint: 1},
		Budget: config.DefaultRunBudget,
		Fixer:  fixer,
	})
	if err != nil {
		t.Fatalf("building the pipeline: %v", err)
	}
	exec, err := p.Executor(graph.NewMemoryStore())
	if err != nil {
		t.Fatalf("building an executor: %v", err)
	}
	state, err := p.NewState(s.start())
	if err != nil {
		t.Fatalf("building the run's initial state: %v", err)
	}
	return &laterRun{subject: s, upstream: upstream, agent: agent, exec: exec, state: state}
}

// execute runs the pipeline until it completes or halts.
func (r *laterRun) execute(t *testing.T) graph.Result {
	t.Helper()
	result, err := r.exec.Run(t.Context(), r.subject.run, r.state)
	if err != nil {
		t.Fatalf("running the pipeline: %v", err)
	}
	return result
}

// copyHead is where the run's isolated copy stands now.
func (r *laterRun) copyHead(t *testing.T) string {
	t.Helper()
	return git(t, r.subject.dir, "rev-parse", "HEAD")
}

// mustNotExistUpstream fails when the upstream holds the reference.
func (r *laterRun) mustNotExistUpstream(t *testing.T, ref string) {
	t.Helper()
	if out := git(t, r.upstream, "for-each-ref", "--format=%(refname)", ref); strings.TrimSpace(out) != "" {
		t.Fatalf("%s exists on the upstream, standing at %s, and nothing was to be pushed",
			ref, git(t, r.upstream, "rev-parse", ref))
	}
}

// anchoringRebase stands in for the rebase stage's one write the push stage
// needs: the observation of the branch target, recorded absent because the
// upstream has never held the topic branch. It is encoded the way the rebase
// stage encodes it.
func anchoringRebase(t *testing.T) pipeline.Implementation {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"remote": stages.UpstreamRemote, "ref": "refs/heads/topic", "exists": false, "commit": "",
	})
	if err != nil {
		t.Fatalf("encoding the anchor: %v", err)
	}
	return pipeline.Implementation{
		Writes: []pipeline.Key{pipeline.KeyPushAnchor},
		NewBody: func() pipeline.Body {
			return func(context.Context, pipeline.Input) (pipeline.Output, error) {
				return pipeline.Output{
					Report: findings.Report{Summary: "observed the branch target"},
					Writes: map[pipeline.Key]graph.Value{pipeline.KeyPushAnchor: graph.TextValue(string(encoded))},
				}, nil
			}
		},
	}
}

// failingUntilFixed is a check stage that reports one fix finding until a fix
// round has left a summary, and passes after. It reads its own fix key for
// that, which is the state a round leaves behind, so it is the round that
// moves it rather than a counter in the test.
func failingUntilFixed(stage pipeline.Stage, fails bool) pipeline.Implementation {
	return pipeline.Implementation{
		Reads: []pipeline.Key{stage.FixKey()},
		NewBody: func() pipeline.Body {
			return func(_ context.Context, in pipeline.Input) (pipeline.Output, error) {
				summary, err := in.State.Get(stage.FixKey())
				if err != nil {
					return pipeline.Output{}, err
				}
				if text, _ := summary.Text(); !fails || text != "" {
					return pipeline.Output{Report: findings.Report{Summary: "the targeted check passed"}}, nil
				}
				return pipeline.Output{Report: findings.Report{
					Summary: "the targeted check failed",
					Findings: []findings.Finding{{
						ID: stage.String() + "-failed", Severity: findings.SeverityError, Action: findings.ActionFix,
						Description: "the targeted check over total.go failed",
					}},
				}}, nil
			}
		},
	}
}
