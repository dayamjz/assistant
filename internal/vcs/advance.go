package vcs

import (
	"context"
	"errors"
	"fmt"
)

// ErrTreeNotClean is returned by AdvanceBranch when the branch it would move
// is checked out in this working copy over uncommitted changes or untracked
// files, read immediately before the move.
var ErrTreeNotClean = errors.New("vcs: the branch is checked out over a tree that is not clean")

// ErrBranchMoved is returned by AdvanceBranch when the branch did not name
// from at the moment of the move, which is git's own compare-and-swap
// refusing the update.
var ErrBranchMoved = errors.New("vcs: the branch no longer names the commit the move was anchored to")

// AdvanceBranch moves refs/heads/<branch> in this working copy from the
// commit from to the commit to, and, when the branch is the one this working
// copy has checked out, moves the index and the working tree with it, so the
// tree still matches its branch afterwards.
//
// The move is git update-ref with from as the old value, which is git's own
// compare-and-swap: a branch that names anything other than from when the
// reference is written is not moved, and the refusal is ErrBranchMoved. Both
// commits are resolved here first, so the move is between two full
// identifiers and an abbreviation cannot name something else by the time git
// reads it. Whether moving from to to is a fast-forward is not decided here;
// that is internal/safety's question, asked before this is called.
//
// For a checked-out branch the tree is read immediately before the move and
// a tree with uncommitted changes or untracked files is ErrTreeNotClean, with
// nothing moved; after the reference moves, git read-tree -m -u from to
// brings the index and the files from from's tree to to's. If that fails the
// reference is put back to from, anchored to to, and the failure returned.
// The residual gap is the window between the read of the tree and read-tree:
// a file written there is what read-tree's own two-tree checks see, and they
// refuse to overwrite a changed tracked file but are not a lock.
func (r *Repository) AdvanceBranch(ctx context.Context, branch, from, to string) error {
	if err := checkArg("branch", branch); err != nil {
		return err
	}
	fromID, err := r.ResolveCommit(ctx, from)
	if err != nil {
		return err
	}
	toID, err := r.ResolveCommit(ctx, to)
	if err != nil {
		return err
	}
	name := "refs/heads/" + branch
	head, err := r.HeadBranch(ctx)
	checkedOut := err == nil && head == branch
	if err != nil && !errors.Is(err, ErrDetachedHead) {
		return err
	}
	if checkedOut {
		dirty, untracked, err := r.treeState(ctx)
		if err != nil {
			return err
		}
		if dirty || untracked {
			return fmt.Errorf("%w: %s is checked out in %s (uncommitted changes %t, untracked files %t)",
				ErrTreeNotClean, branch, r.path, dirty, untracked)
		}
	}
	const message = "assistant: approved local landing"
	if _, err := r.run(ctx, "update-ref-advance", "update-ref", "-m", message, "--no-deref", "--end-of-options",
		name, toID, fromID); err != nil {
		current, readErr := r.ResolveCommit(ctx, name)
		if readErr == nil && current != fromID {
			return fmt.Errorf("%w: %s names %s, not %s: %w", ErrBranchMoved, name, current, fromID, err)
		}
		return err
	}
	if !checkedOut {
		return nil
	}
	if _, err := r.run(ctx, "read-tree-advance", "read-tree", "-m", "-u", fromID, toID); err != nil {
		if _, backErr := r.run(ctx, "update-ref-restore", "update-ref", "-m", message+" undone", "--no-deref",
			"--end-of-options", name, fromID, toID); backErr != nil {
			return fmt.Errorf("vcs: %s moved to %s and its working tree could not follow (%w), and putting it back "+
				"to %s failed too: %w", name, toID, err, fromID, backErr)
		}
		return fmt.Errorf("vcs: %s could not bring the working tree from %s to %s, so the branch was put back: %w",
			name, fromID, toID, err)
	}
	return nil
}
