package stages_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/stages"
)

// git runs a git command in a directory, for building the subject repository a
// stage body reads. It shells out on the same terms internal/vcs's,
// internal/gate's and internal/service's own tests do: a working copy
// assembled with the code under test could not show that code wrong.
//
// It runs with the developer's own git configuration out of the way and with
// the author and committer identity stated, so what a test builds does not
// depend on the machine it runs on.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+filepath.Join(dir, ".gitconfig-absent"),
		"GIT_CONFIG_SYSTEM="+filepath.Join(dir, ".gitconfig-absent"),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.invalid",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newHome opens a home under a temporary root.
func newHome(t *testing.T) *home.Home {
	t.Helper()
	h, err := home.Open(t.TempDir())
	if err != nil {
		t.Fatalf("opening a home: %v", err)
	}
	return h
}

// write puts a file in a working copy, creating the directories above it.
func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatalf("making %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", full, err)
	}
}

// stateReader is the pipeline.Reader a stage body is given: it answers the
// keys the implementation declared and refuses the rest, so a test cannot read
// a key the real stage node would not have handed over. What it reads from is
// a real run state, so every key holds the typed zero the graph would give it.
//
// It has no way to fail a declared read, and that is deliberate. The real
// reader records every refusal it hands back and the node adapter takes the
// recorded error whatever the body returned, so a fake that failed a read
// without that consequence would state a shape the mechanism cannot produce
// and would let a body look like it had recovered from something no body can.
type stateReader struct {
	allowed map[pipeline.Key]bool
	state   graph.State
}

// Get implements pipeline.Reader.
func (r stateReader) Get(key pipeline.Key) (graph.Value, error) {
	if !r.allowed[key] {
		return graph.Value{}, errors.New("the stage did not declare a read of " + string(key))
	}
	value, _ := r.state.Get(string(key))
	return value, nil
}

// stageRun is one run of one stage body against a real isolated copy: a home,
// a copy where the stage looks for one, and the dependencies a body is given.
//
// It is shared by the stage bodies that run a configured command, because what
// it builds is the run rather than the stage. The stage is named at the call
// that runs a body, so one run can be put to more than one of them.
type stageRun struct {
	home         *home.Home
	deps         stages.StageDeps
	repositoryID string
	runID        string
	// copy is the isolated copy the stage runs the command in.
	copy string
	// commit is the commit at its head.
	commit string
	// subject is that commit's subject line, which the configured commands
	// print, so a test can recognize the command's own output.
	subject string
}

// newStageRun builds a home, an isolated copy at the place a stage looks for
// one, and the dependencies a body is given, with the configuration a test
// wants the run resolved against.
//
// The copy is a linked worktree at a detached head, which is what a run works
// in: a body reaches it through StageDeps.Copy, which opens and never creates.
func newStageRun(t *testing.T, cfg config.Config) *stageRun {
	t.Helper()
	run := &stageRun{
		home:         newHome(t),
		repositoryID: "repository-1",
		runID:        "run-1",
		subject:      "the commit the check prints",
	}
	source := t.TempDir()
	git(t, source, "init", "--quiet")
	write(t, source, "total.go", "package subject\n")
	git(t, source, "add", ".")
	git(t, source, "commit", "--quiet", "-m", run.subject)

	run.copy = run.home.Worktree(run.repositoryID, run.runID)
	if err := os.MkdirAll(filepath.Dir(run.copy), 0o700); err != nil {
		t.Fatalf("making %s: %v", filepath.Dir(run.copy), err)
	}
	git(t, source, "worktree", "add", "--quiet", "--detach", run.copy)
	run.commit = git(t, run.copy, "rev-parse", "HEAD")
	run.deps = stages.NewStageDeps(agents.StageAgent{}, run.home, cfg, nil)
	return run
}

// report runs one stage's body over this run and returns the report the
// pipeline would record, normalized and validated as the stage node does.
func (r *stageRun) report(t *testing.T, impl pipeline.Implementation, stage pipeline.Stage) findings.Report {
	t.Helper()
	out, err := runStageBody(t, impl, stage, r.repositoryID, r.runID)
	if err != nil {
		t.Fatalf("running the %s stage: %v", stage, err)
	}
	report := out.Report.Normalize()
	if err := report.Validate(); err != nil {
		t.Fatalf("the %s stage produced a report the pipeline refuses: %v", stage, err)
	}
	return report
}

// runStageBody runs one implementation's body through the same
// restriction the stage node applies, so a read it did not declare is refused
// here as it would be there.
//
// What it reads from is a run state the pipeline built, so every key holds
// what a run would put there rather than what this test remembered to fill in.
func runStageBody(t *testing.T, impl pipeline.Implementation, stage pipeline.Stage,
	repositoryID, runID string) (pipeline.Output, error) {
	t.Helper()
	allowed := make(map[pipeline.Key]bool, len(impl.Reads))
	for _, key := range impl.Reads {
		allowed[key] = true
	}
	return impl.NewBody()(t.Context(), pipeline.Input{
		Stage: stage,
		State: stateReader{allowed: allowed, state: newRunState(t, repositoryID, runID)},
	})
}

// newRunState is the initial state of a run of this repository, built by the
// pipeline that would run it.
func newRunState(t *testing.T, repositoryID, runID string) graph.State {
	t.Helper()
	p, err := pipeline.New(pipeline.Options{
		Stages: pipeline.ConstantStages("nothing to report"),
		Budget: config.DefaultRunBudget,
	})
	if err != nil {
		t.Fatalf("building a pipeline to take a run's initial state from: %v", err)
	}
	state, err := p.NewState(pipeline.Start{
		Repository: repositoryID,
		Run:        runID,
		Branch:     "topic",
		Base:       "main",
		Submitted:  "0000000000000000000000000000000000000000",
	})
	if err != nil {
		t.Fatalf("building the run's initial state: %v", err)
	}
	return state
}

// readRecorded reads a file a test asserts the contents of.
func readRecorded(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(content)
}
