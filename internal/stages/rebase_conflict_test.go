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

// upstreamReadme is the newer base's copy of the file a later submitted
// commit also edits. The first submitted commit conflicts on total.go; this
// one makes the commit after it conflict too.
const upstreamReadme = "Total adds up numbers on the newer base.\n"

// TestAnUnchangedLaterRoundKeepsAnEarlierResolution runs two commits that
// each conflict. The first round resolves the first conflict and the rebase
// advances to the second. The second round edits nothing. Aborting that
// round would restore the submitted tip, drop the resolution from the
// working history, and leave the recorded head naming the commit abort
// removed. P6 keeps the resolution, and the recorded head is the commit the
// copy stands on.
func TestAnUnchangedLaterRoundKeepsAnEarlierResolution(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newSubject(t)
	git(t, s.dir, "checkout", "--quiet", "-B", "topic", s.base)
	write(t, s.dir, totalPath, strings.Replace(baseTotal, "sum := 0", "sum := 0 // first edit", 1))
	s.commit(t, "edit the function")
	write(t, s.dir, readmePath, "Total sums every value it is given.\n")
	s.head = s.commit(t, "edit the readme")
	git(t, s.dir, "config", "user.name", "test")
	git(t, s.dir, "config", "user.email", "test@example.invalid")
	upstream := filepath.Join(t.TempDir(), "upstream.git")
	git(t, filepath.Dir(upstream), "init", "--quiet", "--bare", "-b", "main", upstream)
	git(t, s.dir, "remote", "add", stages.UpstreamRemote, upstream)
	git(t, s.dir, "push", "--quiet", stages.UpstreamRemote, s.base+":refs/heads/main")
	other := t.TempDir()
	git(t, other, "clone", "--quiet", upstream, other)
	write(t, other, totalPath, upstreamTotal)
	write(t, other, readmePath, upstreamReadme)
	git(t, other, "add", "-A")
	git(t, other, "commit", "--quiet", "-m", "change both files on main")
	git(t, other, "push", "--quiet", "origin", "main")

	agent := standin.New(t, script(standin.Text("resolved the first conflict and left the second")))
	var calls int
	cfg := config.Defaults()
	deps := stages.NewStageDeps(agents.NewStageAgent(agent.Runner()), s.home, cfg, nil, redact.New())
	fixDeps := stages.NewFixDeps(func(ctx context.Context, _ string) (agents.Fixer, error) {
		inner, err := agents.OpenFixer(ctx, agent.Runner(), "")
		if err != nil {
			return nil, err
		}
		return &firstOnlyFixer{inner: inner, dir: s.dir, t: t, calls: &calls}, nil
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
	if calls < 2 {
		t.Fatalf("the fixer ran %d time(s), want a second round on the later conflict", calls)
	}
	if result.Status != graph.StatusConverged {
		t.Fatalf("the run stopped with %s, want convergence after the later round changed nothing", result.Status)
	}
	head := git(t, s.dir, "rev-parse", "HEAD")
	if recorded := textAt(t, result.State, pipeline.KeyHead); recorded != head {
		t.Fatalf("recorded head %s, working copy %s", recorded, head)
	}
	if got := git(t, s.dir, "show", "HEAD:"+totalPath); got != strings.TrimSpace(resolvedTotal) {
		t.Fatalf("HEAD's %s =\n%s\nwant the first round's resolution", totalPath, got)
	}
	if git(t, s.dir, "cat-file", "-t", s.head) != "commit" {
		t.Fatalf("the submitted commit %s is gone", s.head)
	}
}

// firstOnlyFixer resolves the first conflict it is shown and leaves every
// later round untouched, which is the unchanged round after a resolution
// this run already committed.
type firstOnlyFixer struct {
	inner agents.Fixer
	dir   string
	t     *testing.T
	calls *int
}

func (f *firstOnlyFixer) Reference() string { return f.inner.Reference() }

func (f *firstOnlyFixer) Apply(ctx context.Context, inv agents.Invocation) (agents.Result, error) {
	*f.calls++
	if *f.calls == 1 {
		write(f.t, f.dir, totalPath, resolvedTotal)
	}
	return f.inner.Apply(ctx, inv)
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

// submoduleCase is a submitted branch that conflicts with upstream on a
// submodule gitlink, plus an earlier commit the rebase can apply. fix is a
// commit in that submodule, reachable from the checkout, that neither side of
// the conflict names.
type submoduleCase struct {
	s   *subject
	fix string
}

// prepareSubmoduleConflict builds submoduleCase and leaves the checkout on
// the submitted branch, with the submodule attached to its own main at the
// submitted gitlink. arrange adjusts that checkout before the run, which is
// where a symbolic HEAD or a reftable store is installed.
func prepareSubmoduleConflict(t *testing.T, arrange func(t *testing.T, dir string)) submoduleCase {
	t.Helper()
	sub := t.TempDir()
	git(t, sub, "init", "--quiet", "-b", "main", ".")
	write(t, sub, "f.txt", "base\n")
	git(t, sub, "add", "f.txt")
	git(t, sub, "commit", "--quiet", "-m", "base")
	subBase := git(t, sub, "rev-parse", "HEAD")
	git(t, sub, "checkout", "--quiet", "-b", "topic")
	write(t, sub, "f.txt", "topic\n")
	git(t, sub, "commit", "--quiet", "-am", "topic")
	subTopic := git(t, sub, "rev-parse", "HEAD")
	git(t, sub, "checkout", "--quiet", "main")
	write(t, sub, "f.txt", "upstream\n")
	git(t, sub, "commit", "--quiet", "-am", "upstream")
	subUpstream := git(t, sub, "rev-parse", "HEAD")
	git(t, sub, "checkout", "--quiet", "-b", "fix")
	write(t, sub, "f.txt", "fix\n")
	git(t, sub, "commit", "--quiet", "-am", "fix")
	subFix := git(t, sub, "rev-parse", "HEAD")

	s := newSubject(t)
	git(t, s.dir, "config", "user.name", "test")
	git(t, s.dir, "config", "user.email", "test@example.invalid")
	git(t, s.dir, "checkout", "--quiet", "main")
	git(t, s.dir, "-c", "protocol.file.allow=always", "submodule", "add", "--quiet", sub, "lib")
	git(t, s.dir, "-C", "lib", "checkout", "--quiet", subBase)
	git(t, s.dir, "add", "lib")
	git(t, s.dir, "commit", "--quiet", "-m", "add the submodule")

	git(t, s.dir, "checkout", "--quiet", "-B", "topic")
	write(t, s.dir, "internal/note/note.go", keptNote)
	s.commit(t, "note a fact the rebase can apply")
	git(t, s.dir, "-C", "lib", "checkout", "--quiet", subTopic)
	git(t, s.dir, "add", "lib")
	s.head = s.commit(t, "point the submodule at a diverging commit")

	git(t, s.dir, "checkout", "--quiet", "main")
	git(t, s.dir, "-C", "lib", "checkout", "--quiet", subUpstream)
	git(t, s.dir, "add", "lib")
	git(t, s.dir, "commit", "--quiet", "-m", "point the submodule at the other side")
	remote := filepath.Join(t.TempDir(), "upstream.git")
	git(t, filepath.Dir(remote), "init", "--quiet", "--bare", "-b", "main", remote)
	git(t, s.dir, "remote", "add", stages.UpstreamRemote, remote)
	git(t, s.dir, "push", "--quiet", stages.UpstreamRemote, "HEAD:refs/heads/main")
	git(t, s.dir, "checkout", "--quiet", "topic")
	git(t, s.dir, "-C", "lib", "checkout", "--quiet", "-B", "main", subTopic)
	arrange(t, s.dir)
	return submoduleCase{s: s, fix: subFix}
}

// runSubmoduleResolution runs the rebase stage and a fixer that moves the
// submodule's main branch to fix. That is the whole resolution: the program
// stages and continues.
func runSubmoduleResolution(t *testing.T, c submoduleCase) graph.Result {
	t.Helper()
	s := c.s
	agent := standin.New(t, script(standin.Text("moved the submodule checkout")))
	cfg := config.Defaults()
	deps := stages.NewStageDeps(agents.NewStageAgent(agent.Runner()), s.home, cfg, nil, redact.New())
	fixDeps := stages.NewFixDeps(func(ctx context.Context, _ string) (agents.Fixer, error) {
		inner, err := agents.OpenFixer(ctx, agent.Runner(), "")
		if err != nil {
			return nil, err
		}
		return submoduleMoveFixer{inner: inner, dir: s.dir, commit: c.fix, t: t}, nil
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
	if len(agent.Calls()) == 0 {
		t.Fatal("the fixer was not launched")
	}
	return result
}

func expectSubmoduleResolved(t *testing.T, c submoduleCase, result graph.Result) {
	t.Helper()
	s := c.s
	head := git(t, s.dir, "rev-parse", "HEAD")
	base := git(t, s.dir, "rev-parse", "refs/remotes/"+stages.UpstreamRemote+"/main")
	if result.Status != graph.StatusCompleted || !isAncestor(t, s.dir, base, head) {
		t.Fatalf("status %s, head %s, upstream %s is ancestor %v; the submodule resolution has to finish the rebase",
			result.Status, head, base, isAncestor(t, s.dir, base, head))
	}
	got, err := os.ReadFile(filepath.Join(s.dir, "internal/note/note.go"))
	if err != nil || string(got) != keptNote {
		t.Fatalf("the earlier commit's file = %q, error %v", got, err)
	}
	if git(t, s.dir, "-C", "lib", "rev-parse", "HEAD") != c.fix {
		t.Fatalf("submodule HEAD = %s, want %s", git(t, s.dir, "-C", "lib", "rev-parse", "HEAD"), c.fix)
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

// TestASymbolicSubmoduleResolutionKeepsAnEarlierCommit is a conflicted
// submodule whose HEAD points through refs/heads/alias at refs/heads/main.
// The fixer only moves main, between two commits that both differ from the
// index. That has to change the digest, be staged, and finish the rebase, so
// the earlier commit stays. A digest that records the alias text aborts the
// round and drops that commit. P6 keeps the submitted commit.
func TestASymbolicSubmoduleResolutionKeepsAnEarlierCommit(t *testing.T) {
	principles.Cite(t, principles.P6)
	c := prepareSubmoduleConflict(t, func(t *testing.T, dir string) {
		t.Helper()
		git(t, dir, "-C", "lib", "symbolic-ref", "refs/heads/alias", "refs/heads/main")
		git(t, dir, "-C", "lib", "symbolic-ref", "HEAD", "refs/heads/alias")
	})
	expectSubmoduleResolved(t, c, runSubmoduleResolution(t, c))
}

// TestAReftableSubmoduleResolutionKeepsAnEarlierCommit is a conflicted
// submodule whose checkout stores refs in a reftable and whose HEAD is
// attached. The fixer moves that branch. The round has to read the digest,
// launch the fixer, and finish the rebase. A digest that opens the sentinel
// path reftable writes fails before the fixer runs. P6 keeps the submitted
// commit.
func TestAReftableSubmoduleResolutionKeepsAnEarlierCommit(t *testing.T) {
	principles.Cite(t, principles.P6)
	c := prepareSubmoduleConflict(t, func(t *testing.T, dir string) {
		t.Helper()
		git(t, dir, "-C", "lib", "refs", "migrate", "--ref-format=reftable")
	})
	expectSubmoduleResolved(t, c, runSubmoduleResolution(t, c))
}

// submoduleMoveFixer moves the submodule's main branch and does not stage
// the superproject. The program records the checkout that move resolves to.
type submoduleMoveFixer struct {
	inner  agents.Fixer
	dir    string
	commit string
	t      *testing.T
}

func (f submoduleMoveFixer) Reference() string { return f.inner.Reference() }

func (f submoduleMoveFixer) Apply(ctx context.Context, inv agents.Invocation) (agents.Result, error) {
	git(f.t, f.dir, "-C", "lib", "update-ref", "refs/heads/main", f.commit)
	return f.inner.Apply(ctx, inv)
}

// conflictOnUpstream gives s an upstream whose main branch edits the named
// files of the subject's base, so the submitted commit conflicts on each of
// them when it is replayed there.
func conflictOnUpstream(t *testing.T, s *subject, files map[string]string) {
	t.Helper()
	git(t, s.dir, "config", "user.name", "test")
	git(t, s.dir, "config", "user.email", "test@example.invalid")
	upstream := filepath.Join(t.TempDir(), "upstream.git")
	git(t, filepath.Dir(upstream), "init", "--quiet", "--bare", "-b", "main", upstream)
	git(t, s.dir, "remote", "add", stages.UpstreamRemote, upstream)
	git(t, s.dir, "push", "--quiet", stages.UpstreamRemote, s.base+":refs/heads/main")
	other := t.TempDir()
	git(t, other, "clone", "--quiet", upstream, other)
	for path, content := range files {
		write(t, other, path, content)
	}
	git(t, other, "add", "-A")
	git(t, other, "commit", "--quiet", "-m", "edit the same lines on main")
	git(t, other, "push", "--quiet", "origin", "main")
}

// runRebaseRounds drives s through the real rebase stage and fix body, with
// wrap given the fixer the round opens and inspect called after each round.
// It returns what the run returned.
func runRebaseRounds(t *testing.T, s *subject, reply string,
	wrap func(agents.Fixer) agents.Fixer,
	inspect func(round int, out pipeline.FixOutput, err error)) (graph.Result, error) {
	t.Helper()
	agent := standin.New(t, script(standin.Text(reply)))
	cfg := config.Defaults()
	deps := stages.NewStageDeps(agents.NewStageAgent(agent.Runner()), s.home, cfg, nil, redact.New())
	fixDeps := stages.NewFixDeps(func(ctx context.Context, _ string) (agents.Fixer, error) {
		inner, err := agents.OpenFixer(ctx, agent.Runner(), "")
		if err != nil {
			return nil, err
		}
		return wrap(inner), nil
	}, s.home, cfg, nil, redact.New())
	all := pipeline.ConstantStages("nothing to report")
	all.Rebase = stages.Rebase(deps)
	fixer := stages.Fix(fixDeps)
	fixBody := fixer.NewBody()
	var round int
	fixer.NewBody = func() pipeline.FixBody {
		return func(ctx context.Context, in pipeline.FixInput) (pipeline.FixOutput, error) {
			round++
			out, err := fixBody(ctx, in)
			inspect(round, out, err)
			return out, err
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
	return exec.Run(t.Context(), s.run, state)
}

// TestARebaseResolutionFinishesUnderTheApplyBackendSetting is the resolved
// conflict with rebase.backend=apply in the copy's configuration. The round
// commits the resolution and continues, and that continue has to finish the
// rebase. One that stops with nothing to apply leaves the run without the
// resolution on the new base. P6 keeps the submitted commit.
func TestARebaseResolutionFinishesUnderTheApplyBackendSetting(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newSubject(t)
	git(t, s.dir, "config", "rebase.backend", "apply")
	conflictOnUpstream(t, s, map[string]string{totalPath: upstreamTotal})
	result, err := runRebaseRounds(t, s, "resolved the conflict on the new base",
		func(inner agents.Fixer) agents.Fixer { return resolvingFixer{inner: inner, dir: s.dir, t: t} },
		func(round int, _ pipeline.FixOutput, err error) {
			if err != nil {
				t.Errorf("fix round %d: %v", round, err)
			}
		})
	if err != nil {
		t.Fatalf("running the rebase and fix loop: %v", err)
	}
	head := git(t, s.dir, "rev-parse", "HEAD")
	base := git(t, s.dir, "rev-parse", "refs/remotes/"+stages.UpstreamRemote+"/main")
	if result.Status != graph.StatusCompleted || !isAncestor(t, s.dir, base, head) {
		t.Fatalf("status %s, head %s, upstream %s is ancestor %v; the resolution has to land on the new base",
			result.Status, head, base, isAncestor(t, s.dir, base, head))
	}
	if got := git(t, s.dir, "show", "HEAD:"+totalPath); got != strings.TrimSpace(resolvedTotal) {
		t.Fatalf("HEAD's %s =\n%s\nwant the resolution", totalPath, got)
	}
	if git(t, s.dir, "cat-file", "-t", s.head) != "commit" {
		t.Fatalf("the submitted commit %s is gone", s.head)
	}
}

// TestAConflictLeftUntouchedIsNotCommittedAsResolved is one commit that
// conflicts on two files. The fixer resolves one and leaves the other as the
// conflict left it, which the conflict finding asks of a file it cannot
// resolve. Committing every path would stage that file's conflict markers as
// its resolution and finish the rebase with them. The round has to leave the
// rebase stopped on the conflict and report no head, and no commit the run
// records may carry the markers.
func TestAConflictLeftUntouchedIsNotCommittedAsResolved(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newSubject(t)
	conflictOnUpstream(t, s, map[string]string{totalPath: upstreamTotal, readmePath: upstreamReadme})
	result, err := runRebaseRounds(t, s, "resolved the code and left the README",
		func(inner agents.Fixer) agents.Fixer { return resolvingFixer{inner: inner, dir: s.dir, t: t} },
		func(round int, out pipeline.FixOutput, err error) {
			if err != nil {
				t.Errorf("fix round %d: %v", round, err)
				return
			}
			if round != 1 {
				return
			}
			if head, ok := out.Writes[pipeline.KeyHead]; ok {
				t.Errorf("the first round reported head %v with %s still conflicted", head, readmePath)
			}
			if unmerged := git(t, s.dir, "ls-files", "-u", "--", readmePath); unmerged == "" {
				t.Errorf("after the first round %s is no longer conflicted in the index", readmePath)
			}
			repo, err := vcs.OpenWorktree(t.Context(), s.dir)
			if err != nil {
				t.Fatalf("opening the copy after the first round: %v", err)
			}
			if active, err := repo.RebaseStatus(t.Context()); err != nil || !active {
				t.Errorf("after the first round, rebase in progress %v, error %v; want it stopped on the conflict", active, err)
			}
		})
	if err != nil {
		t.Fatalf("running the rebase and fix loop: %v", err)
	}
	if result.Status == graph.StatusCompleted {
		t.Fatalf("the run completed with %s never resolved", readmePath)
	}
	recorded := textAt(t, result.State, pipeline.KeyHead)
	for _, commit := range []string{recorded, git(t, s.dir, "rev-parse", "HEAD")} {
		if readme := git(t, s.dir, "show", commit+":"+readmePath); strings.Contains(readme, "<<<<<<<") {
			t.Fatalf("commit %s records conflict markers in %s:\n%s", commit, readmePath, readme)
		}
	}
	if git(t, s.dir, "cat-file", "-t", s.head) != "commit" {
		t.Fatalf("the submitted commit %s is gone", s.head)
	}
	t.Logf("the run stopped with %s at recorded head %s", result.Status, recorded)
}

// TestEveryConflictResolvedIsCommitted is the positive control for the
// untouched-conflict guard: the same two conflicted files, both resolved by
// the fixer, commit and finish the rebase on the new base.
func TestEveryConflictResolvedIsCommitted(t *testing.T) {
	s := newSubject(t)
	conflictOnUpstream(t, s, map[string]string{totalPath: upstreamTotal, readmePath: upstreamReadme})
	const resolvedReadme = "Total sums every value it is given, on the newer base.\n"
	result, err := runRebaseRounds(t, s, "resolved both conflicted files",
		func(inner agents.Fixer) agents.Fixer {
			return editingFixer{inner: inner, edit: func() {
				write(t, s.dir, totalPath, resolvedTotal)
				write(t, s.dir, readmePath, resolvedReadme)
			}}
		},
		func(round int, _ pipeline.FixOutput, err error) {
			if err != nil {
				t.Errorf("fix round %d: %v", round, err)
			}
		})
	if err != nil {
		t.Fatalf("running the rebase and fix loop: %v", err)
	}
	head := git(t, s.dir, "rev-parse", "HEAD")
	base := git(t, s.dir, "rev-parse", "refs/remotes/"+stages.UpstreamRemote+"/main")
	if result.Status != graph.StatusCompleted || !isAncestor(t, s.dir, base, head) {
		t.Fatalf("status %s, head %s, upstream %s is ancestor %v; both resolutions have to land on the new base",
			result.Status, head, base, isAncestor(t, s.dir, base, head))
	}
	if got := git(t, s.dir, "show", "HEAD:"+readmePath); got != strings.TrimSpace(resolvedReadme) {
		t.Fatalf("HEAD's %s = %q, want the resolution", readmePath, got)
	}
}

// TestConflictsResolvedOverTwoRoundsAreCommitted is the same two conflicted
// files resolved one per round. The first round leaves the README untouched,
// so it commits nothing. The second resolves the README and leaves the code
// as the first round wrote it. That earlier resolution is not an untouched
// conflict, so the second round commits both and finishes the rebase.
func TestConflictsResolvedOverTwoRoundsAreCommitted(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newSubject(t)
	conflictOnUpstream(t, s, map[string]string{totalPath: upstreamTotal, readmePath: upstreamReadme})
	const resolvedReadme = "Total sums every value it is given, on the newer base.\n"
	var calls int
	result, err := runRebaseRounds(t, s, "resolved one conflicted file",
		func(inner agents.Fixer) agents.Fixer {
			return editingFixer{inner: inner, edit: func() {
				calls++
				if calls == 1 {
					write(t, s.dir, totalPath, resolvedTotal)
					return
				}
				write(t, s.dir, readmePath, resolvedReadme)
			}}
		},
		func(round int, _ pipeline.FixOutput, err error) {
			if err != nil {
				t.Errorf("fix round %d: %v", round, err)
			}
		})
	if err != nil {
		t.Fatalf("running the rebase and fix loop: %v", err)
	}
	head := git(t, s.dir, "rev-parse", "HEAD")
	base := git(t, s.dir, "rev-parse", "refs/remotes/"+stages.UpstreamRemote+"/main")
	if result.Status != graph.StatusCompleted || !isAncestor(t, s.dir, base, head) {
		t.Fatalf("status %s after %d round(s), head %s, upstream %s is ancestor %v; the two rounds' resolutions have to land on the new base",
			result.Status, calls, head, base, isAncestor(t, s.dir, base, head))
	}
	if got := git(t, s.dir, "show", "HEAD:"+totalPath); got != strings.TrimSpace(resolvedTotal) {
		t.Fatalf("HEAD's %s =\n%s\nwant the first round's resolution", totalPath, got)
	}
	if got := git(t, s.dir, "show", "HEAD:"+readmePath); got != strings.TrimSpace(resolvedReadme) {
		t.Fatalf("HEAD's %s = %q, want the second round's resolution", readmePath, got)
	}
}

// TestARebaseTheFixerEndedIsNotCommitted is a fixer that aborts the rebase
// it was given and then edits a file. The round has to fail before it
// commits, so nothing the fixer left is recorded on the submitted commit
// and the run reports no head for it. P6 keeps the submitted commit.
func TestARebaseTheFixerEndedIsNotCommitted(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newSubject(t)
	conflictOnUpstream(t, s, map[string]string{totalPath: upstreamTotal})
	var roundErr error
	_, err := runRebaseRounds(t, s, "aborted the rebase and edited the code",
		func(inner agents.Fixer) agents.Fixer {
			return editingFixer{inner: inner, edit: func() {
				git(t, s.dir, "rebase", "--abort")
				write(t, s.dir, totalPath, resolvedTotal)
			}}
		},
		func(_ int, _ pipeline.FixOutput, err error) { roundErr = err })
	if err == nil || roundErr == nil || !strings.Contains(roundErr.Error(), "no longer in progress") {
		t.Fatalf("run error %v, round error %v; want the round to refuse the rebase the fixer ended", err, roundErr)
	}
	if head := git(t, s.dir, "rev-parse", "HEAD"); head != s.head {
		t.Fatalf("HEAD = %s, want the submitted commit %s with nothing committed onto it", head, s.head)
	}
}

// editingFixer runs edit while the real fixer invocation runs.
type editingFixer struct {
	inner agents.Fixer
	edit  func()
}

func (f editingFixer) Reference() string { return f.inner.Reference() }

func (f editingFixer) Apply(ctx context.Context, inv agents.Invocation) (agents.Result, error) {
	f.edit()
	return f.inner.Apply(ctx, inv)
}

// TestAConflictEditedWithItsMarkersLeftIsNotCommitted is one commit that
// conflicts on two files. The fixer resolves the code and edits the README
// without removing the conflict markers git wrote into it. The README was
// changed, so it is not an untouched conflict, but committing it would
// record the markers as its resolution. The round has to leave the rebase
// stopped on the README's conflict and report no head, and no commit the
// run records may carry the markers.
func TestAConflictEditedWithItsMarkersLeftIsNotCommitted(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newSubject(t)
	conflictOnUpstream(t, s, map[string]string{totalPath: upstreamTotal, readmePath: upstreamReadme})
	result, err := runRebaseRounds(t, s, "resolved the code and edited the README",
		func(inner agents.Fixer) agents.Fixer {
			return editingFixer{inner: inner, edit: func() {
				write(t, s.dir, totalPath, resolvedTotal)
				write(t, s.dir, readmePath, "Edited above the conflict.\n"+read(t, s.dir, readmePath))
			}}
		},
		expectReadmeStillConflicted(t, s))
	if err != nil {
		t.Fatalf("running the rebase and fix loop: %v", err)
	}
	expectMarkersNeverRecorded(t, s, result)
}

// TestAConflictStagedWithItsMarkersIsNotCommitted is the same two files. The
// fixer resolves the code and stages the README as the conflict left it,
// which takes it out of the index's unmerged entries. Committing would
// record the markers, and so would a later continue that found nothing
// unmerged. The round has to put the README's conflict back in the index,
// leave the rebase stopped on it, and report no head.
func TestAConflictStagedWithItsMarkersIsNotCommitted(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newSubject(t)
	conflictOnUpstream(t, s, map[string]string{totalPath: upstreamTotal, readmePath: upstreamReadme})
	result, err := runRebaseRounds(t, s, "resolved the code and staged the README",
		func(inner agents.Fixer) agents.Fixer {
			return editingFixer{inner: inner, edit: func() {
				write(t, s.dir, totalPath, resolvedTotal)
				git(t, s.dir, "add", "--", readmePath)
			}}
		},
		expectReadmeStillConflicted(t, s))
	if err != nil {
		t.Fatalf("running the rebase and fix loop: %v", err)
	}
	expectMarkersNeverRecorded(t, s, result)
}

// markerReadme is a newer base's README that documents conflict markers, so
// lines shaped like git's markers are part of one side of the conflict.
const markerReadme = "Total adds up numbers on the newer base.\n\n" +
	"A conflict looks like this:\n\n" +
	"<<<<<<< HEAD\nours\n=======\ntheirs\n>>>>>>> topic\n"

// TestMarkerShapedLinesOfAResolutionAreCommitted is the positive control for
// the leftover-marker guard. The README's newer base holds lines shaped like
// conflict markers, and the fixer's resolution keeps them and adds a
// heading underlined with exactly seven equals signs. None of those is a
// marker git wrote for this conflict, so the resolution is committed and the
// rebase finishes on the new base.
func TestMarkerShapedLinesOfAResolutionAreCommitted(t *testing.T) {
	s := newSubject(t)
	conflictOnUpstream(t, s, map[string]string{totalPath: upstreamTotal, readmePath: markerReadme})
	const resolvedReadme = "Total sums every value it is given, on the newer base.\n\n" +
		"Example\n=======\n\n" +
		"A conflict looks like this:\n\n" +
		"<<<<<<< HEAD\nours\n=======\ntheirs\n>>>>>>> topic\n"
	result, err := runRebaseRounds(t, s, "resolved both conflicted files",
		func(inner agents.Fixer) agents.Fixer {
			return editingFixer{inner: inner, edit: func() {
				if !strings.Contains(read(t, s.dir, readmePath), ">>>>>>> "+s.head[:7]) {
					t.Errorf("the README holds no marker git wrote for the conflict:\n%s", read(t, s.dir, readmePath))
				}
				write(t, s.dir, totalPath, resolvedTotal)
				write(t, s.dir, readmePath, resolvedReadme)
			}}
		},
		func(round int, _ pipeline.FixOutput, err error) {
			if err != nil {
				t.Errorf("fix round %d: %v", round, err)
			}
		})
	if err != nil {
		t.Fatalf("running the rebase and fix loop: %v", err)
	}
	head := git(t, s.dir, "rev-parse", "HEAD")
	base := git(t, s.dir, "rev-parse", "refs/remotes/"+stages.UpstreamRemote+"/main")
	if result.Status != graph.StatusCompleted || !isAncestor(t, s.dir, base, head) {
		t.Fatalf("status %s, head %s, upstream %s is ancestor %v; the resolution has to land on the new base",
			result.Status, head, base, isAncestor(t, s.dir, base, head))
	}
	if got := git(t, s.dir, "show", "HEAD:"+readmePath); got != strings.TrimSpace(resolvedReadme) {
		t.Fatalf("HEAD's %s =\n%s\nwant the resolution", readmePath, got)
	}
}

// expectReadmeStillConflicted checks the first round of a run whose fixer
// left the README holding the markers git wrote: the round reports no head,
// names the README, and leaves it conflicted in the index with the rebase
// stopped on it.
func expectReadmeStillConflicted(t *testing.T, s *subject) func(int, pipeline.FixOutput, error) {
	return func(round int, out pipeline.FixOutput, err error) {
		if err != nil {
			t.Errorf("fix round %d: %v", round, err)
			return
		}
		if round != 1 {
			return
		}
		if head, ok := out.Writes[pipeline.KeyHead]; ok {
			t.Errorf("the first round reported head %v with %s still holding its markers", head, readmePath)
		}
		if !strings.Contains(out.Summary, readmePath) {
			t.Errorf("the first round's summary does not name %s: %s", readmePath, out.Summary)
		}
		if unmerged := git(t, s.dir, "ls-files", "-u", "--", readmePath); unmerged == "" {
			t.Errorf("after the first round %s is no longer conflicted in the index", readmePath)
		}
		repo, err := vcs.OpenWorktree(t.Context(), s.dir)
		if err != nil {
			t.Fatalf("opening the copy after the first round: %v", err)
		}
		if active, err := repo.RebaseStatus(t.Context()); err != nil || !active {
			t.Errorf("after the first round, rebase in progress %v, error %v; want it stopped on the conflict", active, err)
		}
	}
}

// expectMarkersNeverRecorded checks the end of a run whose fixer never
// removed the README's markers: the run did not complete, neither the head
// it records nor the copy's HEAD carries them, and the submitted commit is
// kept.
func expectMarkersNeverRecorded(t *testing.T, s *subject, result graph.Result) {
	t.Helper()
	if result.Status == graph.StatusCompleted {
		t.Fatalf("the run completed with %s never resolved", readmePath)
	}
	recorded := textAt(t, result.State, pipeline.KeyHead)
	for _, commit := range []string{recorded, git(t, s.dir, "rev-parse", "HEAD")} {
		if readme := git(t, s.dir, "show", commit+":"+readmePath); strings.Contains(readme, "<<<<<<<") {
			t.Fatalf("commit %s records conflict markers in %s:\n%s", commit, readmePath, readme)
		}
	}
	if git(t, s.dir, "cat-file", "-t", s.head) != "commit" {
		t.Fatalf("the submitted commit %s is gone", s.head)
	}
	t.Logf("the run stopped with %s at recorded head %s", result.Status, recorded)
}
