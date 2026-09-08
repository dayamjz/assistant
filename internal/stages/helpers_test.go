package stages_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/stages"
)

// git runs a git command in a directory, for building the repositories a stage
// body works in. It shells out on the same terms internal/vcs's, internal/gate's
// and internal/service's own tests do, and for the reason internal/fixture's
// package comment gives: a subject assembled with the code under test could not
// show that code wrong.
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

// write puts a file in a working copy.
func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
		t.Fatalf("making the directory for %s: %v", name, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

// commit stages everything in a working copy and records it, returning the
// commit it made.
func commit(t *testing.T, dir, message string) string {
	t.Helper()
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "--quiet", "-m", message)
	return git(t, dir, "rev-parse", "HEAD")
}

// The names every subject in this file shares.
const (
	subjectBranch     = "topic"
	subjectBase       = "main"
	subjectRepository = "repo-1"
	subjectRun        = "run-1"
)

// subject is a built repository a run's rebase stage can be pointed at: a
// remote standing in for the change's destination, a copy the run works in at
// the path StageDeps.Copy derives, and a place to make further commits on
// either.
type subject struct {
	t *testing.T
	// origin is the bare repository the copy's origin remote names.
	origin string
	// author is a working copy of origin, for planting commits the way
	// somebody with a checkout makes them.
	author string
	// copy is the run's isolated copy, at home.Worktree(repository, run).
	copy string
	// deps are the dependencies a stage body is given for this subject.
	deps stages.StageDeps
	// head is the commit the run is validating.
	head string
}

// newSubject builds a remote holding one commit on the target branch and one
// commit on the branch under validation, and a copy of it standing detached at
// the branch's commit, which is where a run's isolated copy starts.
func newSubject(t *testing.T) *subject {
	t.Helper()
	root := t.TempDir()
	s := &subject{
		t:      t,
		origin: filepath.Join(root, "origin.git"),
		author: filepath.Join(root, "author"),
	}
	if err := os.MkdirAll(s.origin, 0o755); err != nil {
		t.Fatalf("making the origin directory: %v", err)
	}
	git(t, s.origin, "init", "--quiet", "--bare", "-b", subjectBase, ".")

	git(t, root, "clone", "--quiet", s.origin, s.author)
	write(t, s.author, "base.txt", "one\n")
	commit(t, s.author, "start the project")
	git(t, s.author, "push", "--quiet", "origin", subjectBase)

	git(t, s.author, "checkout", "--quiet", "-b", subjectBranch)
	write(t, s.author, "change.txt", "the change\n")
	s.head = commit(t, s.author, "make the change")
	git(t, s.author, "push", "--quiet", "origin", subjectBranch)
	git(t, s.author, "checkout", "--quiet", subjectBase)

	h, err := home.Open(filepath.Join(root, "home"))
	if err != nil {
		t.Fatalf("opening the home: %v", err)
	}
	if err := h.Create(); err != nil {
		t.Fatalf("creating the home: %v", err)
	}
	s.copy = h.Worktree(subjectRepository, subjectRun)
	if err := os.MkdirAll(filepath.Dir(s.copy), 0o755); err != nil {
		t.Fatalf("making the worktree directory: %v", err)
	}
	git(t, root, "clone", "--quiet", s.origin, s.copy)
	git(t, s.copy, "checkout", "--quiet", "--detach", s.head)
	s.deps = stages.NewStageDeps(agents.StageAgent{}, h, config.Config{}, nil)
	return s
}

// state is the run state a rebase stage body reads, with any key the caller
// wants to differ from this subject's.
func (s *subject) state(overrides map[pipeline.Key]graph.Value) map[pipeline.Key]graph.Value {
	state := map[pipeline.Key]graph.Value{
		pipeline.KeyRepository: graph.TextValue(subjectRepository),
		pipeline.KeyRun:        graph.TextValue(subjectRun),
		pipeline.KeyBranch:     graph.TextValue(subjectBranch),
		pipeline.KeyBase:       graph.TextValue(subjectBase),
		pipeline.KeyHead:       graph.TextValue(s.head),
	}
	for key, value := range overrides {
		state[key] = value
	}
	return state
}

// runRebase runs the rebase stage's body over this subject.
func (s *subject) runRebase(overrides map[pipeline.Key]graph.Value) (pipeline.Output, error) {
	s.t.Helper()
	impl := stages.Rebase(s.deps)
	allowed := make(map[pipeline.Key]bool, len(impl.Reads))
	for _, key := range impl.Reads {
		allowed[key] = true
	}
	return impl.NewBody()(s.t.Context(), pipeline.Input{
		Stage: pipeline.StageRebase,
		State: declaredReader{allowed: allowed, state: s.state(overrides)},
	})
}
