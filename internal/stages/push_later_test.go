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
	t.Parallel()

	objection := findings.Finding{
		ID:          "later-clamp-is-wrong",
		Severity:    findings.SeverityError,
		Action:      findings.ActionFix,
		Location:    findings.Location{Path: totalPath, Line: 5},
		Description: "the round clamped the sum to make the check pass instead of fixing the sum",
	}
	run := newLaterRun(t, true, standin.Reviewed("the round's clamp changes what Total returns", objection))
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

	run := newLaterRun(t, true, standin.Reviewed("the round's change is sound"))
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

	run := newLaterRun(t, false, standin.Reviewed("objecting", findings.Finding{
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

// newLaterRun builds the run. When fixRound is true the test stage reports a
// fix finding on its first execution and passes once a round has run, and the
// round writes fixedLine into total.go before the real fix body commits it:
// the stand-in prints and cannot edit a working copy, so what an agent would
// have changed is put there the way fix_test.go puts it, and everything after
// the edit is the product's. When it is false the test stage passes at once.
//
// later is how the review of later commits is answered. The review stage's
// own review is always answered cleanly, so the only thing that can hold the
// push is the review this decision added.
func newLaterRun(t *testing.T, fixRound bool, later standin.Reply) *laterRun {
	t.Helper()
	s := newSubject(t)
	git(t, s.dir, "config", "user.name", "test")
	git(t, s.dir, "config", "user.email", "test@example.invalid")
	upstream := filepath.Join(t.TempDir(), "upstream.git")
	git(t, filepath.Dir(upstream), "init", "--quiet", "--bare", "-b", "main", upstream)
	git(t, s.dir, "remote", "add", stages.UpstreamRemote, upstream)
	git(t, s.dir, "push", "--quiet", stages.UpstreamRemote, "main")

	agent := standin.New(t, standin.Script{Steps: []standin.Step{
		{Match: standin.Match{PromptContains: laterLandmark}, Times: standin.Always, Reply: later},
		{Match: standin.MatchReview(), Times: standin.Always, Reply: standin.Reviewed("nothing to report")},
		{Times: standin.Always, Reply: standin.Text(fixerAccount)},
	}})

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
	all.Test = failingUntilFixed(fixRound)
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
		Rounds: config.FixRounds{Test: 1},
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

// failingUntilFixed is a test stage that reports one fix finding until a fix
// round has left a summary, and passes after. It reads its own fix key for
// that, which is the state a round leaves behind, so it is the round that
// moves it rather than a counter in the test.
func failingUntilFixed(fails bool) pipeline.Implementation {
	return pipeline.Implementation{
		Reads: []pipeline.Key{pipeline.StageTest.FixKey()},
		NewBody: func() pipeline.Body {
			return func(_ context.Context, in pipeline.Input) (pipeline.Output, error) {
				summary, err := in.State.Get(pipeline.StageTest.FixKey())
				if err != nil {
					return pipeline.Output{}, err
				}
				if text, _ := summary.Text(); !fails || text != "" {
					return pipeline.Output{Report: findings.Report{Summary: "the targeted check passed"}}, nil
				}
				return pipeline.Output{Report: findings.Report{
					Summary: "the targeted check failed",
					Findings: []findings.Finding{{
						ID: "test-failed", Severity: findings.SeverityError, Action: findings.ActionFix,
						Description: "the targeted check over total.go failed",
					}},
				}}, nil
			}
		},
	}
}
