package stages_test

import (
	"context"
	"os"
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
// scripted fixer. The fixer is shown the conflict on the new base and edits
// nothing, so the round aborts that rebase and the stage can retry. The run
// stops on its convergence bound. P6 requires the submitted commit to remain
// available when the replay cannot apply.
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
			if !inProgress {
				t.Fatal("the fixer was not shown the conflict the rebase stopped on")
			}
			out, err := fixBody(ctx, in)
			if err != nil {
				return out, err
			}
			inProgress, err = copy.RebaseStatus(ctx)
			if err != nil || inProgress {
				t.Fatalf("after the fix round, rebase in progress %v, error %v", inProgress, err)
			}
			return out, nil
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

// resolvedTotal is the submitted change and the newer base, in one file. It is
// what a fixer writes over the conflict while the copy stands on the new base.
const resolvedTotal = `package total

// Total sums the values.
func Total(values []int) int {
	sum := 0 // start empty, on the newer base
	for _, v := range values {
		sum += v
	}
	return sum
}
`

// TestARebaseConflictIsResolvedOnTheRebasedBase runs the same conflicting
// replay as the no-edit case, with a fixer that writes the combined file.
// The resolution has to be committed on the new base. A fix commit appended
// after the copy is returned to the submitted branch does not resolve the
// replay: the next rebase stops on the original commit, before it reaches
// the fix. P6 keeps the submitted commit, and the resolved head has to carry
// its change on top of the upstream the stage fetched.
func TestARebaseConflictIsResolvedOnTheRebasedBase(t *testing.T) {
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

	agent := standin.New(t, script(standin.Text("resolved the conflict on the new base")))
	cfg := config.Defaults()
	deps := stages.NewStageDeps(agents.NewStageAgent(agent.Runner()), s.home, cfg, nil, redact.New())
	fixDeps := stages.NewFixDeps(func(ctx context.Context, _ string) (agents.Fixer, error) {
		inner, err := agents.OpenFixer(ctx, agent.Runner(), "")
		if err != nil {
			return nil, err
		}
		return resolvingFixer{inner: inner, dir: s.dir, t: t}, nil
	}, s.home, cfg, nil, redact.New())
	all := pipeline.ConstantStages("nothing to report")
	rebase := stages.Rebase(deps)
	all.Rebase = rebase
	fixer := stages.Fix(fixDeps)
	fixBody := fixer.NewBody()
	var fixRuns int
	fixer.NewBody = func() pipeline.FixBody {
		return func(ctx context.Context, in pipeline.FixInput) (pipeline.FixOutput, error) {
			fixRuns++
			repo, err := vcs.OpenWorktree(ctx, s.dir)
			if err != nil {
				t.Fatalf("opening the copy before the fix round: %v", err)
			}
			inProgress, err := repo.RebaseStatus(ctx)
			if err != nil {
				t.Fatalf("checking the copy before the fix round: %v", err)
			}
			if !inProgress {
				t.Fatal("the fixer was not given the conflict on the rebased base")
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
	head := git(t, s.dir, "rev-parse", "HEAD")
	base := git(t, s.dir, "rev-parse", "refs/remotes/"+stages.UpstreamRemote+"/main")
	if result.Status != graph.StatusCompleted || !isAncestor(t, s.dir, base, head) {
		t.Fatalf("status %s, head %s, upstream %s is ancestor %v; the resolution has to land on the new base and the run has to finish",
			result.Status, head, base, isAncestor(t, s.dir, base, head))
	}
	if fixRuns == 0 || len(agent.Calls()) != fixRuns {
		t.Fatalf("the fixer ran %d time(s), and the scripted agent received %d calls", fixRuns, len(agent.Calls()))
	}
	if git(t, s.dir, "cat-file", "-t", s.head) != "commit" {
		t.Fatalf("the submitted commit %s is gone", s.head)
	}
	got, err := os.ReadFile(filepath.Join(s.dir, filepath.FromSlash(totalPath)))
	if err != nil {
		t.Fatalf("reading the resolved file: %v", err)
	}
	if string(got) != resolvedTotal {
		t.Fatalf("the resolved head has:\n%s\nwant:\n%s", got, resolvedTotal)
	}
	repo, err := vcs.OpenWorktree(t.Context(), s.dir)
	if err != nil {
		t.Fatalf("opening the copy after the run: %v", err)
	}
	if inProgress, err := repo.RebaseStatus(t.Context()); err != nil || inProgress {
		t.Fatalf("after the run, rebase in progress %v, error %v", inProgress, err)
	}
}

// resolvingFixer edits the conflicted file while the real fixer invocation
// runs, which is when a round's digest of the working copy is taken.
type resolvingFixer struct {
	inner agents.Fixer
	dir   string
	t     *testing.T
}

func (f resolvingFixer) Reference() string { return f.inner.Reference() }

func (f resolvingFixer) Apply(ctx context.Context, inv agents.Invocation) (agents.Result, error) {
	write(f.t, f.dir, totalPath, resolvedTotal)
	return f.inner.Apply(ctx, inv)
}

const keptNote = `package note

// Kept is a change an earlier commit made, on a file the conflict does not touch.
const Kept = true
`

// upstreamTotal is the newer base's copy of the file the submitted commit also
// edits. A fixer that keeps this text is resolving onto the commit the rebase
// stopped on.
var upstreamTotal = strings.Replace(baseTotal, "sum := 0", "sum := 0 // newer base", 1)

// TestARebaseResolutionMatchingTheBaseKeepsAnEarlierCommit runs a branch whose
// first commit applies and whose second conflicts. The fixer writes the
// upstream side of the conflict, which is the tree the rebase is already
// stopped on. That resolution has to finish the rebase and keep the first
// commit. Aborting it drops the first commit and the next rebase stops on the
// original conflicting commit again. P6 keeps the submitted commit.
func TestARebaseResolutionMatchingTheBaseKeepsAnEarlierCommit(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newSubject(t)
	git(t, s.dir, "checkout", "--quiet", "-B", "topic", s.base)
	write(t, s.dir, "internal/note/note.go", keptNote)
	s.commit(t, "note a fact the rebase can apply")
	write(t, s.dir, totalPath, strings.Replace(baseTotal, "sum := 0", "sum := 0 // start empty", 1))
	s.head = s.commit(t, "change the same line")
	git(t, s.dir, "config", "user.name", "test")
	git(t, s.dir, "config", "user.email", "test@example.invalid")
	upstream := filepath.Join(t.TempDir(), "upstream.git")
	git(t, filepath.Dir(upstream), "init", "--quiet", "--bare", "-b", "main", upstream)
	git(t, s.dir, "remote", "add", stages.UpstreamRemote, upstream)
	git(t, s.dir, "push", "--quiet", stages.UpstreamRemote, s.base+":refs/heads/main")
	other := t.TempDir()
	git(t, other, "clone", "--quiet", upstream, other)
	write(t, other, totalPath, upstreamTotal)
	git(t, other, "add", "-A")
	git(t, other, "commit", "--quiet", "-m", "change the same line on main")
	git(t, other, "push", "--quiet", "origin", "main")

	agent := standin.New(t, script(standin.Text("kept the upstream side of the conflict")))
	cfg := config.Defaults()
	deps := stages.NewStageDeps(agents.NewStageAgent(agent.Runner()), s.home, cfg, nil, redact.New())
	fixDeps := stages.NewFixDeps(func(ctx context.Context, _ string) (agents.Fixer, error) {
		inner, err := agents.OpenFixer(ctx, agent.Runner(), "")
		if err != nil {
			return nil, err
		}
		return upstreamFixer{inner: inner, dir: s.dir, t: t}, nil
	}, s.home, cfg, nil, redact.New())
	all := pipeline.ConstantStages("nothing to report")
	all.Rebase = stages.Rebase(deps)
	fixer := stages.Fix(fixDeps)
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
	head := git(t, s.dir, "rev-parse", "HEAD")
	base := git(t, s.dir, "rev-parse", "refs/remotes/"+stages.UpstreamRemote+"/main")
	if result.Status != graph.StatusCompleted || !isAncestor(t, s.dir, base, head) {
		t.Fatalf("status %s, head %s, upstream %s is ancestor %v; keeping the upstream side has to finish the rebase on that base",
			result.Status, head, base, isAncestor(t, s.dir, base, head))
	}
	got, err := os.ReadFile(filepath.Join(s.dir, "internal/note/note.go"))
	if err != nil || string(got) != keptNote {
		t.Fatalf("the earlier commit's file = %q, error %v", got, err)
	}
	total, err := os.ReadFile(filepath.Join(s.dir, filepath.FromSlash(totalPath)))
	if err != nil || string(total) != upstreamTotal {
		t.Fatalf("the resolved file =\n%s\nerror %v", total, err)
	}
	if git(t, s.dir, "cat-file", "-t", s.head) != "commit" {
		t.Fatalf("the submitted commit %s is gone", s.head)
	}
	repo, err := vcs.OpenWorktree(t.Context(), s.dir)
	if err != nil {
		t.Fatalf("opening the copy after the run: %v", err)
	}
	if inProgress, err := repo.RebaseStatus(t.Context()); err != nil || inProgress {
		t.Fatalf("after the run, rebase in progress %v, error %v", inProgress, err)
	}
}

// upstreamFixer writes the upstream side of the conflicted file while the real
// fixer invocation runs.
type upstreamFixer struct {
	inner agents.Fixer
	dir   string
	t     *testing.T
}

func (f upstreamFixer) Reference() string { return f.inner.Reference() }

func (f upstreamFixer) Apply(ctx context.Context, inv agents.Invocation) (agents.Result, error) {
	write(f.t, f.dir, totalPath, upstreamTotal)
	return f.inner.Apply(ctx, inv)
}

// TestAModeOnlyResolutionKeepsAnEarlierCommit is a modify/delete conflict
// whose worktree already holds the upstream bytes. The fixer only sets the
// executable bit. That has to count as a change, be committed, and finish
// the rebase, so the earlier commit stays. A digest that records bytes and
// not mode aborts the round and drops that commit. P6 keeps the submitted
// commit.
func TestAModeOnlyResolutionKeepsAnEarlierCommit(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newSubject(t)
	git(t, s.dir, "checkout", "--quiet", "-B", "topic", s.base)
	write(t, s.dir, "internal/note/note.go", keptNote)
	s.commit(t, "note a fact the rebase can apply")
	git(t, s.dir, "rm", "--quiet", totalPath)
	s.head = s.commit(t, "delete the file upstream edits")
	git(t, s.dir, "config", "user.name", "test")
	git(t, s.dir, "config", "user.email", "test@example.invalid")
	upstream := filepath.Join(t.TempDir(), "upstream.git")
	git(t, filepath.Dir(upstream), "init", "--quiet", "--bare", "-b", "main", upstream)
	git(t, s.dir, "remote", "add", stages.UpstreamRemote, upstream)
	git(t, s.dir, "push", "--quiet", stages.UpstreamRemote, s.base+":refs/heads/main")
	other := t.TempDir()
	git(t, other, "clone", "--quiet", upstream, other)
	write(t, other, totalPath, upstreamTotal)
	git(t, other, "add", "-A")
	git(t, other, "commit", "--quiet", "-m", "change the same line on main")
	git(t, other, "push", "--quiet", "origin", "main")

	agent := standin.New(t, script(standin.Text("marked the resolved file executable")))
	cfg := config.Defaults()
	deps := stages.NewStageDeps(agents.NewStageAgent(agent.Runner()), s.home, cfg, nil, redact.New())
	fixDeps := stages.NewFixDeps(func(ctx context.Context, _ string) (agents.Fixer, error) {
		inner, err := agents.OpenFixer(ctx, agent.Runner(), "")
		if err != nil {
			return nil, err
		}
		return modeFixer{inner: inner, dir: s.dir, t: t}, nil
	}, s.home, cfg, nil, redact.New())
	all := pipeline.ConstantStages("nothing to report")
	all.Rebase = stages.Rebase(deps)
	fixer := stages.Fix(fixDeps)
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
	head := git(t, s.dir, "rev-parse", "HEAD")
	base := git(t, s.dir, "rev-parse", "refs/remotes/"+stages.UpstreamRemote+"/main")
	if result.Status != graph.StatusCompleted || !isAncestor(t, s.dir, base, head) {
		t.Fatalf("status %s, head %s, upstream %s is ancestor %v; a mode-only resolution has to finish the rebase",
			result.Status, head, base, isAncestor(t, s.dir, base, head))
	}
	got, err := os.ReadFile(filepath.Join(s.dir, "internal/note/note.go"))
	if err != nil || string(got) != keptNote {
		t.Fatalf("the earlier commit's file = %q, error %v", got, err)
	}
	full := filepath.Join(s.dir, filepath.FromSlash(totalPath))
	info, err := os.Stat(full)
	if err != nil {
		t.Fatalf("the resolved file: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("the resolved file mode is %o, want it executable", info.Mode().Perm())
	}
	body, err := os.ReadFile(full)
	if err != nil || string(body) != upstreamTotal {
		t.Fatalf("the resolved file =\n%s\nerror %v", body, err)
	}
	if git(t, s.dir, "cat-file", "-t", s.head) != "commit" {
		t.Fatalf("the submitted commit %s is gone", s.head)
	}
	repo, err := vcs.OpenWorktree(t.Context(), s.dir)
	if err != nil {
		t.Fatalf("opening the copy after the run: %v", err)
	}
	if inProgress, err := repo.RebaseStatus(t.Context()); err != nil || inProgress {
		t.Fatalf("after the run, rebase in progress %v, error %v", inProgress, err)
	}
}

// modeFixer sets the executable bit on the conflicted file and does not
// change its bytes. That is the whole resolution.
type modeFixer struct {
	inner agents.Fixer
	dir   string
	t     *testing.T
}

func (f modeFixer) Reference() string { return f.inner.Reference() }

func (f modeFixer) Apply(ctx context.Context, inv agents.Invocation) (agents.Result, error) {
	full := filepath.Join(f.dir, filepath.FromSlash(totalPath))
	if err := os.Chmod(full, 0o755); err != nil {
		f.t.Fatalf("marking %s executable: %v", full, err)
	}
	return f.inner.Apply(ctx, inv)
}
