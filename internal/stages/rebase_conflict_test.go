package stages_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/vcs"
)

// TestAConflictingRebaseLeavesTheCopyReadyForItsFixRound runs a submitted
// change against a newer, conflicting base through the real rebase stage and
// scripted fixer. The fixer changes nothing, so the rebase stage must be able
// to retry and the run must stop on its convergence bound. P6 requires the
// submitted commit to remain available when the replay cannot apply.
func TestAConflictingRebaseLeavesTheCopyReadyForItsFixRound(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newSubject(t)
	git(t, s.dir, "config", "user.name", "test")
	git(t, s.dir, "config", "user.email", "test@example.invalid")
	upstream := filepath.Join(t.TempDir(), "upstream.git")
	git(t, filepath.Dir(upstream), "init", "--quiet", "--bare", "-b", "main", upstream)
	git(t, s.dir, "remote", "add", stages.UpstreamRemote, upstream)
	git(t, s.dir, "push", "--quiet", stages.UpstreamRemote, s.base+":refs/heads/main")
	other := t.TempDir()
	git(t, other, "clone", "--quiet", upstream, other)
	write(t, other, totalPath, strings.Replace(baseTotal, "sum := 0", "sum := 0 // newer base", 1))
	git(t, other, "add", "-A")
	git(t, other, "commit", "--quiet", "-m", "change the same line on main")
	git(t, other, "push", "--quiet", "origin", "main")

	agent := standin.New(t, script(standin.Text("I could not resolve the conflicting change")))
	cfg := config.Defaults()
	deps := stages.NewStageDeps(agents.NewStageAgent(agent.Runner()), s.home, cfg, nil, redact.New())
	fixDeps := stages.NewFixDeps(func(ctx context.Context, _ string) (agents.Fixer, error) {
		return agents.OpenFixer(ctx, agent.Runner(), "")
	}, s.home, cfg, nil, redact.New())
	all := pipeline.ConstantStages("nothing to report")
	rebase := stages.Rebase(deps)
	rebaseBody := rebase.NewBody()
	var rebaseRuns int
	rebase.NewBody = func() pipeline.Body {
		return func(ctx context.Context, in pipeline.Input) (pipeline.Output, error) {
			rebaseRuns++
			return rebaseBody(ctx, in)
		}
	}
	all.Rebase = rebase
	fixer := stages.Fix(fixDeps)
	fixBody := fixer.NewBody()
	var fixRuns int
	fixer.NewBody = func() pipeline.FixBody {
		return func(ctx context.Context, in pipeline.FixInput) (pipeline.FixOutput, error) {
			fixRuns++
			copy, err := vcs.OpenWorktree(ctx, s.dir)
			if err != nil {
				t.Fatalf("opening the copy before the fix round: %v", err)
			}
			inProgress, err := copy.RebaseStatus(ctx)
			if err != nil {
				t.Fatalf("checking the copy before the fix round: %v", err)
			}
			if inProgress {
				t.Fatal("the fixer was sent into a rebase left in progress by the stage")
			}
			return fixBody(ctx, in)
		}
	}
	p, err := pipeline.New(pipeline.Options{
		Stages: all, Rounds: config.FixRounds{Rebase: 3},
		Budget: config.DefaultRunBudget, Fixer: fixer,
	})
	if err != nil {
		t.Fatalf("building the pipeline: %v", err)
	}
	exec, err := p.Executor(graph.NewMemoryStore())
	if err != nil {
		t.Fatalf("building the executor: %v", err)
	}
	state, err := p.NewState(s.start())
	if err != nil {
		t.Fatalf("building the run state: %v", err)
	}
	result, err := exec.Run(t.Context(), s.run, state)
	if err != nil {
		t.Fatalf("running the rebase and fix loop: %v", err)
	}
	if rebaseRuns < 2 {
		t.Fatalf("the rebase stage ran %d time(s), want a retry after the fix round", rebaseRuns)
	}
	if fixRuns == 0 || len(agent.Calls()) != fixRuns {
		t.Fatalf("the fixer ran %d time(s), and the scripted agent received %d calls", fixRuns, len(agent.Calls()))
	}
	if result.Status != graph.StatusConverged {
		t.Fatalf("the run stopped with %s, want convergence after the scripted fixer changed nothing", result.Status)
	}
	if got := git(t, s.dir, "rev-parse", "HEAD"); got != s.head {
		t.Fatalf("the copy stands at %s, want the submitted commit %s", got, s.head)
	}
	if status := git(t, s.dir, "status", "--porcelain"); status != "" {
		t.Fatalf("the copy is not clean after the run stopped:\n%s", status)
	}
	t.Logf("rebase stage ran %d times; the scripted fixer ran %d times", rebaseRuns, fixRuns)
}
