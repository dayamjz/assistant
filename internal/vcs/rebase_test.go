package vcs_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/vcs"
)

// branched builds a working copy with a target branch carrying one commit past
// the point a topic branch left it, and a topic commit of its own. Both are
// returned as commits, because a rebase in this package is given commits and
// never branch names.
//
// touches says whether the target's own commit edits the same file the topic
// commit does, which is what makes replaying the topic onto it conflict.
func branched(t *testing.T, touches bool) (dir, target, topic string) {
	t.Helper()
	gitEnvironment(t)
	dir = t.TempDir()
	rawGit(t, dir, "init", "--quiet", "-b", "main", ".")
	writeFile(t, filepath.Join(dir, "shared.txt"), "the original\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "--quiet", "-m", "start")
	start := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD"))

	rawGit(t, dir, "checkout", "--quiet", "-b", "topic")
	writeFile(t, filepath.Join(dir, "shared.txt"), "the topic's line\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "--quiet", "-m", "the topic's change")
	topic = strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD"))

	rawGit(t, dir, "checkout", "--quiet", "main", "--")
	name := "elsewhere.txt"
	if touches {
		name = "shared.txt"
	}
	writeFile(t, filepath.Join(dir, name), "the target's line\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "--quiet", "-m", "the target's change")
	target = strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD"))
	rawGit(t, dir, "checkout", "--quiet", "--detach", start)
	return dir, target, topic
}

// A rebase replays the commits the target does not have and leaves HEAD at the
// result, and it moves no branch: both branches in the copy still name what
// they named, which is what keeps this package's rule that no operation here
// moves a branch in a working copy.
func TestARebaseReplaysOntoTheTargetAndMovesNoBranch(t *testing.T) {
	dir, target, topic := branched(t, false)
	before := map[string]string{
		"main":  strings.TrimSpace(rawGit(t, dir, "rev-parse", "main")),
		"topic": strings.TrimSpace(rawGit(t, dir, "rev-parse", "topic")),
	}
	repo, err := vcs.OpenWorktree(t.Context(), dir)
	if err != nil {
		t.Fatalf("opening the working copy: %v", err)
	}

	rebased, err := repo.Rebase(t.Context(), vcs.RebaseSpec{Onto: target, Commit: topic})
	if err != nil {
		t.Fatalf("rebasing: %v", err)
	}
	if rebased == topic {
		t.Fatal("the rebase returned the commit it was given, so nothing was replayed")
	}
	if head := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD")); head != rebased {
		t.Fatalf("HEAD stands at %s and the rebase returned %s", head, rebased)
	}
	dropped, err := repo.CommitsNotIn(t.Context(), rebased, target)
	if err != nil {
		t.Fatalf("comparing the result against the target: %v", err)
	}
	if len(dropped) != 1 {
		t.Fatalf("the result adds %d commit(s) to the target, want the one that was replayed: %v", len(dropped), dropped)
	}
	for branch, was := range before {
		if now := strings.TrimSpace(rawGit(t, dir, "rev-parse", branch)); now != was {
			t.Fatalf("%s moved from %s to %s: a rebase here moved a branch", branch, was, now)
		}
	}
}

// A conflict is a typed result naming the paths git stopped on, and the rebase
// is aborted before it comes back, so the caller is not handed a working copy
// standing mid-rebase.
func TestAConflictingRebaseIsTypedAndLeavesNoRebaseInProgress(t *testing.T) {
	dir, target, topic := branched(t, true)
	repo, err := vcs.OpenWorktree(t.Context(), dir)
	if err != nil {
		t.Fatalf("opening the working copy: %v", err)
	}

	_, err = repo.Rebase(t.Context(), vcs.RebaseSpec{Onto: target, Commit: topic})
	if !errors.Is(err, vcs.ErrRebaseConflict) {
		t.Fatalf("rebasing over a conflict gave %v, want a conflict", err)
	}
	var conflict *vcs.RebaseConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("the conflict does not carry its detail: %v", err)
	}
	if want := []string{"shared.txt"}; len(conflict.Paths) != 1 || conflict.Paths[0] != want[0] {
		t.Fatalf("the conflict names %v, want %v", conflict.Paths, want)
	}
	if conflict.Onto != target || conflict.Commit != topic {
		t.Fatalf("the conflict names %s onto %s, want %s onto %s", conflict.Commit, conflict.Onto, topic, target)
	}
	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(filepath.Join(dir, ".git", name)); err == nil {
			t.Fatalf("the working copy was left with %s standing, so the rebase was not aborted", name)
		}
	}
}

// A failure that is not a stopped rebase comes back as git reported it, and
// not dressed as a conflict.
//
// The two cases are different failures and both matter. A commit that does not
// resolve fails before any rebase is attempted, so nothing here reaches the
// abort at all; a working tree with changes in it fails inside git rebase,
// which is where a failure could be mistaken for a stopped one, and the abort
// attempt is the only thing that tells them apart.
func TestARebaseThatNeverStartedIsNotAConflict(t *testing.T) {
	dir, target, topic := branched(t, false)
	repo, err := vcs.OpenWorktree(t.Context(), dir)
	if err != nil {
		t.Fatalf("opening the working copy: %v", err)
	}

	_, err = repo.Rebase(t.Context(), vcs.RebaseSpec{Onto: target, Commit: "0000000000000000000000000000000000000000"})
	if err == nil {
		t.Fatal("rebasing an unresolvable commit succeeded")
	}
	if errors.Is(err, vcs.ErrRebaseConflict) {
		t.Fatalf("a rebase that never started was reported as a conflict: %v", err)
	}
	if !errors.Is(err, vcs.ErrRefNotFound) {
		t.Fatalf("rebasing an unresolvable commit gave %v, want ErrRefNotFound", err)
	}

	// A tracked file with uncommitted changes is what git rebase refuses to
	// start over, and refusing to start is not stopping part way through.
	writeFile(t, filepath.Join(dir, "shared.txt"), "an edit nobody committed\n")
	_, err = repo.Rebase(t.Context(), vcs.RebaseSpec{Onto: target, Commit: topic})
	if err == nil {
		t.Fatal("rebasing over uncommitted changes succeeded")
	}
	if errors.Is(err, vcs.ErrRebaseConflict) {
		t.Fatalf("a rebase git refused to start was reported as a conflict: %v", err)
	}
}

// A bare repository has no working tree to replay in, and the refusal says so
// rather than letting git fail somewhere less legible.
func TestARebaseNeedsAWorkingCopy(t *testing.T) {
	gitEnvironment(t)
	bare := t.TempDir()
	repo, err := vcs.InitBare(t.Context(), filepath.Join(bare, "repo.git"))
	if err != nil {
		t.Fatalf("creating a bare repository: %v", err)
	}
	_, err = repo.Rebase(t.Context(), vcs.RebaseSpec{Onto: "HEAD", Commit: "HEAD"})
	if !errors.Is(err, vcs.ErrInvalidArgument) {
		t.Fatalf("rebasing in a bare repository gave %v, want ErrInvalidArgument", err)
	}
	if !strings.Contains(err.Error(), "working copy") {
		t.Fatalf("the refusal does not say what it needed: %v", err)
	}
}

// CommitsNotIn answers the question internal/safety decides on: what one
// revision holds that another does not, most recent first, and nothing when
// the second already holds everything the first does.
func TestCommitsNotInNamesWhatWouldBeLost(t *testing.T) {
	dir, target, topic := branched(t, false)
	repo, err := vcs.OpenWorktree(t.Context(), dir)
	if err != nil {
		t.Fatalf("opening the working copy: %v", err)
	}

	lost, err := repo.CommitsNotIn(t.Context(), target, topic)
	if err != nil {
		t.Fatalf("comparing the target against the topic: %v", err)
	}
	if len(lost) != 1 || lost[0] != target {
		t.Fatalf("replacing the target with the topic loses %v, want just %s", lost, target)
	}
	none, err := repo.CommitsNotIn(t.Context(), target, target)
	if err != nil {
		t.Fatalf("comparing the target against itself: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("a revision holds %v that it does not hold itself", none)
	}
	if _, err := repo.CommitsNotIn(t.Context(), target, "0000000000000000000000000000000000000000"); !errors.Is(err, vcs.ErrRefNotFound) {
		t.Fatalf("comparing against a commit this repository does not hold gave %v, want ErrRefNotFound", err)
	}
}
