package stages_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/forge/forgetest"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/stages"
)

// openPullRequest is gh's pr view answer for pull request 7 of the checks
// tests, open, on head, with the mergeability gh reports in its own words.
func openPullRequest(head, mergeable string) forgetest.Response {
	return forgetest.PullRequest(forgetest.PullRequestView{Number: 7, State: "OPEN", Mergeable: mergeable,
		Head: "topic", HeadCommit: head, Base: "main", Title: "sum the values",
		URL: "https://github.com/dayamjz/assistant/pull/7"})
}

// passedChecks is gh's check rollup for head with one check that passed.
func passedChecks(head string) forgetest.Response {
	return forgetest.Checks(head, forgetest.CheckRunView{Name: "build", Status: "COMPLETED", Conclusion: "SUCCESS"})
}

// failedChecks is gh's check rollup for head with one check that failed.
func failedChecks(head string) forgetest.Response {
	return forgetest.Checks(head, forgetest.CheckRunView{Name: "build", Status: "COMPLETED", Conclusion: "FAILURE"})
}

// writesTo reports the calls a stand-in code host recorded that would change
// something there. The checks stage reads and nothing else.
func writesTo(code *forgetest.Provider) []forgetest.Call {
	var writes []forgetest.Call
	for _, call := range code.Calls() {
		switch call.Op {
		case forgetest.OpView, forgetest.OpChecks:
		default:
			writes = append(writes, call)
		}
	}
	return writes
}

// A pull request whose checks have passed, and which the code host then
// reports as conflicting with its base, holds the checks stage for a person
// with ci-conflicted rather than passing, and nothing is written to the code
// host. The pull request is read through the real GitHub adapter over the
// stand-in, so the mergeability the body acts on is one gh's own words
// produce.
//
// The first read finds the checks passed and the mergeability not yet
// reported, which is a wait; the second finds the conflict. The control is
// the same run with the second read reporting the pull request mergeable,
// which passes: without it, a body that held on every passed read would pass
// the first half.
func TestAPullRequestThatConflictsAfterItsChecksPassedHoldsTheChecksStage(t *testing.T) {
	t.Parallel()
	const head = "4f1c0de4f1c0de4f1c0de4f1c0de4f1c0de4f1c0"
	for _, tc := range []struct {
		name      string
		mergeable string
		wantHold  bool
	}{
		{"conflicting", "CONFLICTING", true},
		{"control: mergeable", "MERGEABLE", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newChecksRun(t)
			code := forgetest.New(t, forgetest.Script{
				forgetest.OpView:   {openPullRequest(head, "UNKNOWN"), openPullRequest(head, tc.mergeable)},
				forgetest.OpChecks: {passedChecks(head)},
			})
			r.host = code.Host()

			report := r.mustRun(t)

			if report.HasHeld() != tc.wantHold || hasFinding(report, "ci-conflicted") != tc.wantHold {
				t.Fatalf("with the pull request %s after its checks passed, held = %v, want %v: %+v",
					tc.mergeable, report.HasHeld(), tc.wantHold, report)
			}
			if r.pauses != 1 {
				t.Fatalf("the body paused %d times, want once: the first read found the mergeability unreported", r.pauses)
			}
			if writes := writesTo(code); len(writes) != 0 {
				t.Fatalf("the checks stage wrote to the code host: %+v", writes)
			}
			if !tc.wantHold {
				return
			}
			if stages.ChecksTimeoutHeld(report) {
				t.Fatalf("a conflict is a person's decision, and it reads as the elapsed hold the service may resolve: %+v", report)
			}
			for _, want := range []string{"conflict", "does not rebase", "push it through the gate"} {
				if !strings.Contains(report.Summary+" "+report.Findings[0].Description, want) {
					t.Fatalf("the conflict hold does not say %q: %+v", want, report)
				}
			}
		})
	}
}

// Checks that passed on a pull request the code host never reports as
// mergeable are a wait, and an elapsed one holds with ci-mergeability-unknown,
// which is the elapsed hold the service may re-read before resuming. The
// control is the conflict test above, whose second read reports MERGEABLE and
// passes after one pause.
func TestPassedChecksWaitForTheCodeHostToReportMergeability(t *testing.T) {
	t.Parallel()
	const head = "4f1c0de4f1c0de4f1c0de4f1c0de4f1c0de4f1c0"
	r := newChecksRun(t)
	code := forgetest.New(t, forgetest.Script{
		forgetest.OpView:   {openPullRequest(head, "UNKNOWN")},
		forgetest.OpChecks: {passedChecks(head)},
	})
	r.host = code.Host()

	report := r.mustRun(t)

	if !report.HasHeld() || !hasFinding(report, "ci-mergeability-unknown") {
		t.Fatalf("passed checks on a pull request never reported mergeable did not hold on its mergeability: %+v", report)
	}
	if waited := r.now.Sub(r.start); waited != checksTimeout {
		t.Fatalf("the stage held after waiting %s, want exactly the %s checks_timeout", waited, checksTimeout)
	}
	if !stages.ChecksTimeoutHeld(report) {
		t.Fatalf("an elapsed wait on mergeability is not recognized as the elapsed hold: %+v", report)
	}
}

// TestAPullRequestThatConflictsAfterThePushIsHeldAndNothingIsRewritten is
// the conflict through a real pipeline: the review passes, the push stage
// forwards the branch, and then another commit lands on the base and the code
// host reports the pull request as conflicting. The run holds at the checks
// stage, and neither branch on the upstream is touched: the pushed branch
// still stands on the commit the push stage forwarded, and the commit that
// caused the conflict is still on the base.
//
// This is P6 for a conflict after the push: the run rewrites no history it
// cannot anchor and resolves no conflicting content, so nothing on the
// upstream can be lost to it; it hands the conflict to a person instead.
func TestAPullRequestThatConflictsAfterThePushIsHeldAndNothingIsRewritten(t *testing.T) {
	principles.Cite(t, principles.P6)
	t.Parallel()

	run := newChecksPipeline(t, 0, func(head string) forgetest.Script {
		return forgetest.Script{
			forgetest.OpView:   {openPullRequest(head, "CONFLICTING")},
			forgetest.OpChecks: {passedChecks(head)},
		}
	})
	// The base moves on the upstream once the push has happened, which is the
	// other pull request that merged first.
	run.afterPush = func(t *testing.T) { run.conflicting = run.landOnBase(t) }
	result := run.execute(t)

	if result.Status != graph.StatusHalted || result.Position != pipeline.StageCI.HoldNode() {
		report, _ := pipeline.StageReport(result.State, pipeline.StageCI)
		t.Fatalf("the run is %s at %q over a conflicting pull request, want halted at %q: %+v",
			result.Status, result.Position, pipeline.StageCI.HoldNode(), report)
	}
	report, err := pipeline.StageReport(result.State, pipeline.StageCI)
	if err != nil {
		t.Fatalf("reading the checks stage's report: %v", err)
	}
	if !hasFinding(report, "ci-conflicted") {
		t.Fatalf("the checks stage holds without saying the pull request conflicts: %+v", report)
	}
	if run.conflicting == "" {
		t.Fatalf("nothing landed on the base after the push, so this run had nothing to conflict with")
	}
	if got := git(t, run.upstream, "rev-parse", "refs/heads/main"); got != run.conflicting {
		t.Fatalf("the base stands at %s, want the conflicting commit %s left where it landed", got, run.conflicting)
	}
	if got := git(t, run.upstream, "rev-parse", "refs/heads/topic"); got != run.subject.head {
		t.Fatalf("the pushed branch stands at %s, want the commit the push stage forwarded, %s", got, run.subject.head)
	}
	if writes := writesTo(run.code); len(writes) != 0 {
		t.Fatalf("the run wrote to the code host after the push: %+v", writes)
	}
}

// TestACommitTheChecksFixRoundWroteIsHeldAndNeverPushed is the checks stage's
// fix round through a real pipeline. The review passes, the push stage
// forwards the reviewed head, the checks fail, and the stage's one round
// commits a change in the run's copy. That commit reaches neither the
// upstream nor a reviewer: the stage runs again, sees the run's head is not
// the commit the push forwarded, and holds with ci-fix-not-pushed naming both
// commits.
//
// P5 is why it holds rather than pushing: no review read the round's commit,
// and the agent's invocations show none was asked to. P6 is why it names the
// commit: the round's work has a path back to a person, the hold.
//
// The controls are in the same run: the push stage did forward the reviewed
// head, so a pipeline that pushed nothing would fail the upstream assertion,
// and the round did move the copy's head, so a round that committed nothing
// cannot pass.
func TestACommitTheChecksFixRoundWroteIsHeldAndNeverPushed(t *testing.T) {
	principles.Cite(t, principles.P5)
	principles.Cite(t, principles.P6)
	t.Parallel()

	run := newChecksPipeline(t, 1, func(head string) forgetest.Script {
		return forgetest.Script{
			forgetest.OpView:   {openPullRequest(head, "MERGEABLE")},
			forgetest.OpChecks: {failedChecks(head)},
		}
	})
	result := run.execute(t)

	if result.Status != graph.StatusHalted || result.Position != pipeline.StageCI.HoldNode() {
		report, _ := pipeline.StageReport(result.State, pipeline.StageCI)
		t.Fatalf("the run is %s at %q after the checks round, want halted at %q: %+v",
			result.Status, result.Position, pipeline.StageCI.HoldNode(), report)
	}
	report, err := pipeline.StageReport(result.State, pipeline.StageCI)
	if err != nil {
		t.Fatalf("reading the checks stage's report: %v", err)
	}
	round := run.copyHead(t)
	if round == run.subject.head {
		t.Fatalf("the checks round committed nothing, so this run has no commit to hold")
	}
	if !hasFinding(report, "ci-fix-not-pushed") {
		t.Fatalf("the checks stage holds without saying the round's commit is not pushed: %+v", report)
	}
	for _, want := range []string{round, run.subject.head} {
		if !strings.Contains(report.Summary, want) || !strings.Contains(report.Findings[0].Description, want) {
			t.Fatalf("the hold does not name %s: %+v", want, report)
		}
	}
	if got := git(t, run.upstream, "rev-parse", "refs/heads/topic"); got != run.subject.head {
		t.Fatalf("the pushed branch stands at %s, want the reviewed commit %s and not the round's %s",
			got, run.subject.head, round)
	}
	calls := run.agent.Calls()
	if len(calls) != 2 {
		t.Fatalf("the agent was invoked %d time(s), want the review and the checks round:\n%v", len(calls), calls)
	}
	for _, call := range calls {
		if strings.Contains(call.Prompt, laterLandmark) {
			t.Fatalf("a review of the round's commit ran, and this build does not push what it would approve:\n%s",
				call.Prompt)
		}
	}
	if writes := writesTo(run.code); len(writes) != 0 {
		t.Fatalf("the run wrote to the code host after the push: %+v", writes)
	}
}

// checksPipeline is a pipeline whose review, push, checks and fix path are
// the real ones, over a subject with an upstream to push to and a stand-in
// code host. The pull request stage is a constant that records pull request
// 7, which is all the checks stage reads of it.
type checksPipeline struct {
	*laterRun
	code *forgetest.Provider
	// afterPush runs once the push stage has run, and conflicting is the
	// commit a test landed on the base there.
	afterPush   func(t *testing.T)
	conflicting string
}

// newChecksPipeline builds the run with rounds automatic fix rounds at the
// checks stage. script is what the code host answers, given the head the
// push stage will forward.
func newChecksPipeline(t *testing.T, rounds int, script func(head string) forgetest.Script) *checksPipeline {
	t.Helper()
	s := newSubject(t)
	git(t, s.dir, "config", "user.name", "test")
	git(t, s.dir, "config", "user.email", "test@example.invalid")
	upstream := filepath.Join(t.TempDir(), "upstream.git")
	git(t, filepath.Dir(upstream), "init", "--quiet", "--bare", "-b", "main", upstream)
	git(t, s.dir, "remote", "add", stages.UpstreamRemote, upstream)
	git(t, s.dir, "push", "--quiet", stages.UpstreamRemote, "main")

	code := forgetest.New(t, script(s.head))
	agent := standin.New(t, standin.Script{Steps: []standin.Step{
		{Match: standin.Match{PromptContains: laterLandmark}, Times: standin.Always, Reply: standin.Reviewed(
			"objecting", findings.Finding{ID: "should-never-be-asked", Severity: findings.SeverityError,
				Action: findings.ActionAsk, Description: "a review of later commits ran"})},
		{Match: standin.MatchReview(), Times: standin.Always, Reply: standin.Reviewed("nothing to report")},
		{Times: standin.Always, Reply: standin.Text(fixerAccount)},
	}})

	cfg := config.Defaults()
	cfg.ChecksTimeout = checksTimeout
	deps := stages.NewStageDeps(agents.NewStageAgent(agent.Runner()), s.home, cfg, code.Host(), redact.New())
	fixDeps := stages.NewFixDeps(
		func(ctx context.Context, _ string) (agents.Fixer, error) {
			return agents.OpenFixer(ctx, agent.Runner(), "")
		},
		s.home, cfg, nil, redact.New(),
	)

	run := &checksPipeline{code: code}
	all := pipeline.ConstantStages("nothing to report")
	all.Rebase = anchoringRebase(t)
	all.Review = stages.Review(deps)
	all.Push = stages.Push(deps)
	all.PR = recordingPullRequest(func() {
		if run.afterPush != nil {
			run.afterPush(t)
		}
	})
	all.CI = stages.CI(deps)

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
		Rounds: config.FixRounds{Checks: rounds},
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
	start := s.start()
	start.ForgeRepository = "dayamjz/assistant"
	state, err := p.NewState(start)
	if err != nil {
		t.Fatalf("building the run's initial state: %v", err)
	}
	run.laterRun = &laterRun{subject: s, upstream: upstream, agent: agent, exec: exec, state: state}
	return run
}

// landOnBase puts a commit on the upstream's base branch from a checkout of
// its own, touching the line the change under validation touched, and
// returns it.
func (r *checksPipeline) landOnBase(t *testing.T) string {
	t.Helper()
	other := t.TempDir()
	git(t, other, "clone", "--quiet", r.upstream, other)
	git(t, other, "config", "user.name", "elsewhere")
	git(t, other, "config", "user.email", "elsewhere@example.invalid")
	write(t, other, totalPath, strings.Replace(baseTotal, "sum := 0", "sum := 0 // landed first", 1))
	git(t, other, "add", "-A")
	git(t, other, "commit", "--quiet", "-m", "another change lands first")
	git(t, other, "push", "--quiet", "origin", "main")
	return git(t, other, "rev-parse", "HEAD")
}

// recordingPullRequest stands in for the pull request stage: it records pull
// request 7 and calls then, which is where a test changes the world after
// the push.
func recordingPullRequest(then func()) pipeline.Implementation {
	return pipeline.Implementation{
		Writes: []pipeline.Key{pipeline.KeyPullRequest},
		NewBody: func() pipeline.Body {
			return func(context.Context, pipeline.Input) (pipeline.Output, error) {
				then()
				return pipeline.Output{
					Report: findings.Report{Summary: "recorded pull request 7"},
					Writes: map[pipeline.Key]graph.Value{pipeline.KeyPullRequest: graph.TextValue("7")},
				}, nil
			}
		},
	}
}
