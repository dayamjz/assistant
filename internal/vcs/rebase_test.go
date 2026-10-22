package vcs_test

import (
	"errors"
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
