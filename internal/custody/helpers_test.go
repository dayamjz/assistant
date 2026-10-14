package custody_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/custody"
	"github.com/dayamjz/assistant/internal/safety"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// The scenario every test here builds is the shape a run leaves behind, out
// of real repositories: an upstream, the person's working copy cloned from
// it with a feature branch, a bare gate repository the branch was pushed to,
// and the run's isolated copy, a linked worktree of the gate detached at the
// submitted commit. What the run did is then real commits in that copy, and
// what it pushed is a real push from the copy to the upstream.
//
// The repositories are built by running git directly rather than through
// internal/vcs, for the reason internal/vcs' own helpers give: a fixture
// built with the code under test cannot show that code wrong.

// gitEnvironment points git at a configuration file this test owns.
func gitEnvironment(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "gitconfig")
	content := "[user]\n\tname = Test\n\temail = test@example.invalid\n[init]\n\tdefaultBranch = main\n" +
		"[gc]\n\tauto = 0\n[maintenance]\n\tauto = false\n[advice]\n\tdetachedHead = false\n"
	if err := os.WriteFile(cfg, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

// git runs git in dir and returns its trimmed output, failing the test when
// it does not succeed.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// gitWithInput runs git in dir with input on its standard input.
func gitWithInput(t *testing.T, dir, input string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// write writes a file under dir.
func write(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// commit writes the files and commits them on whatever dir has checked out.
func commit(t *testing.T, dir, message string, files map[string]string) string {
	t.Helper()
	for name, content := range files {
		write(t, dir, name, content)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "--quiet", "-m", message)
	return git(t, dir, "rev-parse", "HEAD")
}

// twin is a file with two identical blocks, so an edit to one is textually
// the same edit as one to the other, context lines and all.
const twin = "a\na\na\nk = 0\na\na\na\na\na\na\nk = 0\na\na\na\n"

// firstEdited and secondEdited are twin with the first or the second block
// changed the same way.
const (
	firstEdited  = "a\na\na\nk = 1\na\na\na\na\na\na\nk = 0\na\na\na\n"
	secondEdited = "a\na\na\nk = 0\na\na\na\na\na\na\nk = 1\na\na\na\n"
)

// scenario is one built run.
type scenario struct {
	upstream, working, gate, copy string
	// base is main where the branch forked; submitted is the commit the
	// run was submitted, which the working copy's feature names.
	base, submitted string
	run             store.Run
}

const runID = "abcdefghij23"

// newScenario builds the upstream, the working copy on feature at the
// submitted commit, the gate holding it, and the copy detached there.
func newScenario(t *testing.T) *scenario {
	t.Helper()
	gitEnvironment(t)
	root := t.TempDir()
	s := &scenario{
		upstream: filepath.Join(root, "upstream.git"),
		working:  filepath.Join(root, "working"),
		gate:     filepath.Join(root, "gate.git"),
		copy:     filepath.Join(root, "copy"),
	}
	git(t, root, "init", "--quiet", "--bare", s.upstream)
	git(t, root, "clone", "--quiet", s.upstream, s.working)
	s.base = commit(t, s.working, "base", map[string]string{"twin.txt": twin, "readme.txt": "read me\n"})
	git(t, s.working, "push", "--quiet", "origin", "main")
	git(t, s.working, "switch", "--quiet", "-c", "feature")
	s.submitted = commit(t, s.working, "the person's change", map[string]string{"twin.txt": firstEdited})
	git(t, root, "init", "--quiet", "--bare", s.gate)
	git(t, s.working, "push", "--quiet", s.gate, "feature")
	git(t, s.gate, "worktree", "add", "--quiet", "--detach", s.copy, s.submitted)
	s.run = store.Run{ID: runID, Branch: "feature", Status: store.RunFailed, SubmittedHead: s.submitted}
	return s
}

// upstreamMoves puts an unrelated commit on the upstream's main and returns
// it, which is what a rebase in the copy then rebases onto.
func (s *scenario) upstreamMoves(t *testing.T) string {
	t.Helper()
	other := filepath.Join(t.TempDir(), "other")
	git(t, filepath.Dir(other), "clone", "--quiet", s.upstream, other)
	moved := commit(t, other, "upstream moves on", map[string]string{"upstream.txt": "from elsewhere\n"})
	git(t, other, "push", "--quiet", "origin", "main")
	return moved
}

// rebuild makes the copy's head a commit on onto whose twin.txt is content,
// the way a rebase of the person's change, or a lookalike of it, leaves it.
func (s *scenario) rebuild(t *testing.T, onto, content string) string {
	t.Helper()
	git(t, s.copy, "fetch", "--quiet", s.upstream, "main")
	git(t, s.copy, "switch", "--quiet", "--detach", onto)
	return commit(t, s.copy, "the person's change, rebased", map[string]string{"twin.txt": content})
}

// push forwards commit to the upstream's feature, as the push stage does.
func (s *scenario) push(t *testing.T, commit string) {
	t.Helper()
	git(t, s.copy, "push", "--quiet", "--force", s.upstream, commit+":refs/heads/feature")
}

// facts is what the service would read for this scenario: the run, the
// position its checkpoint records, and the copy and the working copy as they
// stand.
func (s *scenario) facts(t *testing.T, position custody.Position) custody.Facts {
	t.Helper()
	run := s.run
	f := custody.Facts{
		Run:      &run,
		Position: position,
		Branch:   git(t, s.working, "branch", "--show-current"),
		Head:     git(t, s.working, "rev-parse", "HEAD"),
		Target:   custody.TargetOf(s.working, "feature"),
	}
	if _, err := os.Stat(s.copy); err == nil {
		f.Copy = custody.Copy{Standing: true, Path: s.copy, Head: git(t, s.copy, "rev-parse", "HEAD"),
			Dirty:     git(t, s.copy, "status", "--porcelain", "--untracked-files=no") != "",
			Untracked: git(t, s.copy, "ls-files", "--others", "--exclude-standard") != ""}
	}
	return f
}

// open opens the working copy and the gate through internal/vcs.
func (s *scenario) open(t *testing.T) (working, gate *vcs.Repository) {
	t.Helper()
	w, err := vcs.OpenWorktree(context.Background(), s.working)
	if err != nil {
		t.Fatal(err)
	}
	g, err := vcs.OpenBare(context.Background(), s.gate)
	if err != nil {
		t.Fatal(err)
	}
	return w, g
}

// classify classifies the scenario as the service would.
func (s *scenario) classify(t *testing.T, position custody.Position) (custody.Classification, error) {
	t.Helper()
	w, g := s.open(t)
	return custody.Classify(context.Background(), s.facts(t, position), g, w)
}

// mustClassify classifies and fails the test on a refusal.
func (s *scenario) mustClassify(t *testing.T, position custody.Position) custody.Classification {
	t.Helper()
	c, err := s.classify(t, position)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	return c
}

// apply carries the classification out in the working copy.
func (s *scenario) apply(t *testing.T, c custody.Classification) (custody.Result, error) {
	t.Helper()
	w, _ := s.open(t)
	return custody.Apply(context.Background(), w, c, s.gate, s.upstream)
}

// snapshot is everything about a repository a mutation would show in: every
// reference and what it names, HEAD, the tree's state, its configuration,
// and the markers a merge, a rebase or a cherry-pick in progress leave.
func snapshot(t *testing.T, dir string, bare bool) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(git(t, dir, "for-each-ref", "--format=%(refname) %(objectname)"))
	b.WriteString("\nHEAD " + git(t, dir, "symbolic-ref", "--quiet", "HEAD"))
	b.WriteString("\nconfig " + git(t, dir, "config", "--local", "--list"))
	if !bare {
		b.WriteString("\nstatus " + git(t, dir, "status", "--porcelain=v2", "--untracked-files=all", "--branch"))
		gitDir := git(t, dir, "rev-parse", "--absolute-git-dir")
		for _, marker := range []string{"MERGE_HEAD", "REBASE_HEAD", "CHERRY_PICK_HEAD", "rebase-merge", "rebase-apply", "refs/stash"} {
			if _, err := os.Stat(filepath.Join(gitDir, marker)); err == nil {
				b.WriteString("\nmarker " + marker)
			}
		}
	}
	return b.String()
}

// refusal asserts err is a custody refusal for reason and returns it.
func refusal(t *testing.T, err error, reason custody.Reason) *custody.Refusal {
	t.Helper()
	var r *custody.Refusal
	if !errors.As(err, &r) {
		t.Fatalf("got %v, want a custody refusal (%s)", err, reason)
	}
	if !errors.Is(err, custody.ErrRefused) {
		t.Fatalf("the refusal %v does not match ErrRefused", err)
	}
	if r.Reason != reason {
		t.Fatalf("refused as %s (%v), want %s", r.Reason, err, reason)
	}
	return r
}

// safetyReason is the reason of the *safety.Refusal err carries, empty when
// it carries none.
func safetyReason(err error) safety.Reason {
	var r *safety.Refusal
	if errors.As(err, &r) {
		return r.Reason
	}
	return ""
}

var _ custody.Working = (*vcs.Repository)(nil)
var _ custody.Pipeline = (*vcs.Repository)(nil)
var _ custody.Branch = (*vcs.Repository)(nil)
var _ safety.Merger = (*vcs.Repository)(nil)
