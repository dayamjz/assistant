package vcs

import (
	"context"
	"errors"
	"strings"
)

// RebaseSpec describes a rebase operation.
type RebaseSpec struct {
	// Onto is the new base for the current branch.
	Onto string
	// Upstream is the old base, which determines which commits to reapply.
	// When empty, it defaults to the merge base of HEAD and Onto.
	Upstream string
}

// Rebase replays commits from Upstream..HEAD onto Onto. The working copy HEAD
// is updated to the result.
//
// It returns ErrRebaseConflict when the rebase stops due to conflicts.
// The caller must resolve conflicts or abort the rebase.
func (r *Repository) Rebase(ctx context.Context, spec RebaseSpec) error {
	if r.kind != KindWorktree {
		return errors.New("vcs: rebase requires a working copy")
	}
	onto, err := r.ResolveCommit(ctx, spec.Onto)
	if err != nil {
		return err
	}
	args := []string{"rebase", "--no-autostash"}
	if spec.Upstream != "" {
		upstream, err := r.ResolveCommit(ctx, spec.Upstream)
		if err != nil {
			return err
		}
		args = append(args, upstream)
	}
	args = append(args, onto)
	
	_, code, err := r.runExpecting(ctx, "rebase", []int{1}, args...)
	if err != nil {
		return err
	}
	if code != 0 {
		return ErrRebaseConflict
	}
	return nil
}

// RebaseAbort aborts an in-progress rebase and returns the working copy to
// its pre-rebase state.
func (r *Repository) RebaseAbort(ctx context.Context) error {
	if r.kind != KindWorktree {
		return errors.New("vcs: rebase requires a working copy")
	}
	_, err := r.run(ctx, "rebase-abort", "rebase", "--abort")
	return err
}

// RebaseStatus reports whether a rebase is in progress.
func (r *Repository) RebaseStatus(ctx context.Context) (inProgress bool, err error) {
	if r.kind != KindWorktree {
		return false, errors.New("vcs: rebase requires a working copy")
	}
	// Check if .git/rebase-merge or .git/rebase-apply exists
	_, code, err := r.runExpecting(ctx, "rebase-status", []int{1}, "rev-parse", "--verify", "REBASE_HEAD")
	if err != nil {
		return false, err
	}
	return code == 0, nil
}

// conflictError reports a rebase that stopped due to conflicts.
type conflictError struct {
	detail string
}

func (e *conflictError) Error() string {
	msg := "vcs: rebase stopped due to conflicts"
	if e.detail != "" {
		msg += ": " + e.detail
	}
	return msg
}

// Unwrap makes every rebase conflict match ErrRebaseConflict.
func (e *conflictError) Unwrap() error { return ErrRebaseConflict }

// parseRebaseError examines git's stderr to determine if a rebase failure
// was due to conflicts.
func parseRebaseError(stderr string) error {
	lower := strings.ToLower(stderr)
	if strings.Contains(lower, "conflict") || strings.Contains(lower, "could not apply") {
		return &conflictError{detail: strings.TrimSpace(stderr)}
	}
	return nil
}
