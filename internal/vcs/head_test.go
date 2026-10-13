package vcs_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/vcs"
)

// A delivery task's clone is checked out on whatever branch the source's HEAD
// named, and SwitchToNewBranch is what puts it on the task's own branch at the
// commit the task starts from. The branch must be new: a second switch to the
// same name is git's refusal, not a silent move.
func TestSwitchToNewBranchCreatesTheBranchAtTheCommit(t *testing.T) {
	gitEnvironment(t)
	src, first, second := sourceRepo(t)
	to := filepath.Join(t.TempDir(), "copy")
	cloned, err := vcs.Clone(ctx(t), src, to, "")
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if err := cloned.SwitchToNewBranch(ctx(t), "task/one", first); err != nil {
		t.Fatalf("SwitchToNewBranch: %v", err)
	}
	if branch := strings.TrimSpace(rawGit(t, to, "symbolic-ref", "--short", "HEAD")); branch != "task/one" {
		t.Errorf("HEAD is on %q, want task/one", branch)
	}
	if head := strings.TrimSpace(rawGit(t, to, "rev-parse", "HEAD")); head != first {
		t.Errorf("HEAD is %s, want %s", head, first)
	}
	if tip := strings.TrimSpace(rawGit(t, to, "rev-parse", "main")); tip != second {
		t.Errorf("the branch the clone was made on moved to %s, want it untouched at %s", tip, second)
	}
	if err := cloned.SwitchToNewBranch(ctx(t), "task/one", second); err == nil {
		t.Error("switching to a branch that already exists was not refused")
	}
	if err := cloned.SwitchToNewBranch(ctx(t), "task/two", "0000000000000000000000000000000000000000"); !errors.Is(err, vcs.ErrRefNotFound) {
		t.Errorf("switching at a commit that is not here answered %v, want ErrRefNotFound", err)
	}
}

// An investigation task's clone creates no branch, so Detach is what leaves
// its HEAD on the commit and on no branch.
func TestDetachLeavesHeadOnNoBranch(t *testing.T) {
	gitEnvironment(t)
	src, first, _ := sourceRepo(t)
	to := filepath.Join(t.TempDir(), "copy")
	cloned, err := vcs.Clone(ctx(t), src, to, "")
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if err := cloned.Detach(ctx(t), first); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	if _, err := cloned.HeadBranch(ctx(t)); !errors.Is(err, vcs.ErrDetachedHead) {
		t.Errorf("HeadBranch answered %v, want ErrDetachedHead", err)
	}
	if head := strings.TrimSpace(rawGit(t, to, "rev-parse", "HEAD")); head != first {
		t.Errorf("HEAD is %s, want %s", head, first)
	}
}

// UpdateRef writes one reference in a bare repository at an object already
// there, and refuses a commit the repository does not hold rather than
// fetching it from anywhere.
func TestUpdateRefWritesOneReferenceAtAPresentCommit(t *testing.T) {
	gitEnvironment(t)
	src, first, second := sourceRepo(t)
	bare, err := vcs.InitBare(ctx(t), filepath.Join(t.TempDir(), "gate.git"))
	if err != nil {
		t.Fatalf("InitBare: %v", err)
	}
	if err := bare.Fetch(ctx(t), vcs.FetchSpec{Remote: src, Refspecs: []string{"+refs/heads/main:refs/heads/main"}}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	const name = "refs/assistant/runs/one"
	if err := bare.UpdateRef(ctx(t), name, first); err != nil {
		t.Fatalf("UpdateRef: %v", err)
	}
	if got := strings.TrimSpace(rawGit(t, bare.Path(), "rev-parse", name)); got != first {
		t.Errorf("%s is %s, want %s", name, got, first)
	}
	if err := bare.UpdateRef(ctx(t), name, second); err != nil {
		t.Fatalf("UpdateRef moving the reference: %v", err)
	}
	if got := strings.TrimSpace(rawGit(t, bare.Path(), "rev-parse", name)); got != second {
		t.Errorf("%s is %s after the move, want %s", name, got, second)
	}
	if err := bare.UpdateRef(ctx(t), name, "0000000000000000000000000000000000000000"); !errors.Is(err, vcs.ErrRefNotFound) {
		t.Errorf("writing a reference at a commit that is not here answered %v, want ErrRefNotFound", err)
	}
	if err := bare.UpdateRef(ctx(t), "runs/one", first); !errors.Is(err, vcs.ErrInvalidArgument) {
		t.Errorf("a short name answered %v, want ErrInvalidArgument", err)
	}
}
