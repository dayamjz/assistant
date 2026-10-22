package vcs_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/vcs"
)

// TestRebaseStatusDistinguishesAnActiveRebaseFromAnAbortedOne checks the
// status operation against a real conflicting replay and its abort. A clean
// copy must return false without treating the missing REBASE_HEAD as failure.
func TestRebaseStatusDistinguishesAnActiveRebaseFromAnAbortedOne(t *testing.T) {
	gitEnvironment(t)
	dir := t.TempDir()
	rawGit(t, dir, "init", "--quiet", "-b", "main", ".")
	base := commitFile(t, dir, "clash.txt", "base\n", "base")
	rawGit(t, dir, "checkout", "--quiet", "-b", "topic")
	topic := commitFile(t, dir, "clash.txt", "topic\n", "topic")
	rawGit(t, dir, "checkout", "--quiet", "main")
	main := commitFile(t, dir, "clash.txt", "main\n", "main")
	rawGit(t, dir, "checkout", "--quiet", "topic")
	repo, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	if active, err := repo.RebaseStatus(ctx(t)); err != nil || active {
		t.Fatalf("before rebase: active %v, error %v; want false without error", active, err)
	}
	if err := repo.Rebase(ctx(t), vcs.RebaseSpec{Onto: main}); !errors.Is(err, vcs.ErrRebaseConflict) {
		t.Fatalf("rebasing %s onto %s from %s: %v, want a conflict", topic, main, base, err)
	}
	if active, err := repo.RebaseStatus(ctx(t)); err != nil || !active {
		t.Fatalf("during rebase: active %v, error %v; want true without error", active, err)
	}
	if err := repo.RebaseAbort(ctx(t)); err != nil {
		t.Fatalf("aborting the rebase: %v", err)
	}
	if active, err := repo.RebaseStatus(ctx(t)); err != nil || active {
		t.Fatalf("after abort: active %v, error %v; want false without error", active, err)
	}
	if head, err := repo.ResolveCommit(ctx(t), "HEAD"); err != nil || head != topic {
		t.Fatalf("after abort HEAD = %s, %v; want the original %s", head, err, topic)
	}
}

// TestRebaseStatusSeesARebasePausedBeforeAnyCommitIsPicked is the case
// REBASE_HEAD does not cover. An interactive rebase stopped at an initial
// break has no such name, the rebase-merge directory is present, and abort
// succeeds. Status has to report that rebase as in progress.
func TestRebaseStatusSeesARebasePausedBeforeAnyCommitIsPicked(t *testing.T) {
	gitEnvironment(t)
	dir := t.TempDir()
	rawGit(t, dir, "init", "--quiet", "-b", "main", ".")
	base := commitFile(t, dir, "note.txt", "base\n", "base")
	rawGit(t, dir, "checkout", "--quiet", "-b", "topic")
	topic := commitFile(t, dir, "note.txt", "topic\n", "topic")
	script := filepath.Join(t.TempDir(), "edit.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'break\\n' > \"$1\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "rebase", "-i", base)
	cmd.Dir = dir
	env := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "GIT_SEQUENCE_EDITOR=") {
			continue
		}
		env = append(env, entry)
	}
	cmd.Env = append(env, "GIT_SEQUENCE_EDITOR="+script)
	out, err := cmd.CombinedOutput()
	repo, openErr := vcs.OpenWorktree(ctx(t), dir)
	if openErr != nil {
		t.Fatal(openErr)
	}
	active, statusErr := repo.RebaseStatus(ctx(t))
	if statusErr != nil || !active {
		t.Fatalf("paused rebase: active %v, status error %v, command error %v\n%s", active, statusErr, err, out)
	}
	if _, missing := tryRawGit(dir, "rev-parse", "--verify", "--quiet", "REBASE_HEAD"); missing == nil {
		t.Fatal("REBASE_HEAD resolved during a break before any commit was picked")
	}
	if err := repo.RebaseAbort(ctx(t)); err != nil {
		t.Fatalf("aborting the paused rebase: %v", err)
	}
	if active, err := repo.RebaseStatus(ctx(t)); err != nil || active {
		t.Fatalf("after abort: active %v, error %v; want false without error", active, err)
	}
	if head, err := repo.ResolveCommit(ctx(t), "HEAD"); err != nil || head != topic {
		t.Fatalf("after abort HEAD = %s, %v; want the original %s", head, err, topic)
	}
}

// TestRebaseContinueCommitsTheResolutionOntoTheNewBase checks that a
// conflicted replay, once its resolution is committed, continues onto the
// commit the rebase had checked out. Continuing while the conflict is still
// unmerged must fail and leave the rebase in progress.
func TestRebaseContinueCommitsTheResolutionOntoTheNewBase(t *testing.T) {
	gitEnvironment(t)
	dir := t.TempDir()
	rawGit(t, dir, "init", "--quiet", "-b", "main", ".")
	commitFile(t, dir, "clash.txt", "base\n", "base")
	rawGit(t, dir, "checkout", "--quiet", "-b", "topic")
	commitFile(t, dir, "clash.txt", "topic\n", "topic")
	rawGit(t, dir, "checkout", "--quiet", "main")
	main := commitFile(t, dir, "clash.txt", "main\n", "main")
	rawGit(t, dir, "checkout", "--quiet", "topic")
	repo, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Rebase(ctx(t), vcs.RebaseSpec{Onto: main}); !errors.Is(err, vcs.ErrRebaseConflict) {
		t.Fatalf("rebasing onto %s: %v, want a conflict", main, err)
	}
	first, err := repo.UncommittedDigest(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.UncommittedDigest(ctx(t))
	if err != nil || second != first {
		t.Fatalf("digest changed with no edit: %q then %q, error %v", first, second, err)
	}
	if err := repo.RebaseContinue(ctx(t)); err == nil {
		t.Fatal("continuing with the conflict still unmerged succeeded")
	}
	if active, err := repo.RebaseStatus(ctx(t)); err != nil || !active {
		t.Fatalf("after a refused continue: active %v, error %v; want the rebase still in progress", active, err)
	}
	writeFile(t, filepath.Join(dir, "clash.txt"), "topic and main\n")
	edited, err := repo.UncommittedDigest(ctx(t))
	if err != nil || edited == first {
		t.Fatalf("digest after the edit = %q, before %q, error %v; want a different digest", edited, first, err)
	}
	committed, err := repo.CommitAll(ctx(t), "resolve the conflict")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.RebaseContinue(ctx(t)); err != nil {
		t.Fatalf("continuing after the resolution was committed: %v", err)
	}
	if active, err := repo.RebaseStatus(ctx(t)); err != nil || active {
		t.Fatalf("after continue: active %v, error %v; want false without error", active, err)
	}
	head, err := repo.ResolveCommit(ctx(t), "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if head != committed {
		t.Fatalf("HEAD = %s, want the resolution %s", head, committed)
	}
	ancestor, err := repo.IsAncestor(ctx(t), main, head)
	if err != nil || !ancestor {
		t.Fatalf("main %s ancestor of %s: %v, %v", main, head, ancestor, err)
	}
}
