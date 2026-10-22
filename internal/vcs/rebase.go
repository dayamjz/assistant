package vcs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

// RebaseContinue continues an in-progress rebase whose current step is
// already committed.
//
// A later step that conflicts returns ErrRebaseConflict and leaves the rebase
// in progress. A later step that applies cleanly is replayed. When nothing
// remains, the rebase is finished. A continue that is not ready, including
// one whose conflicts are still unmerged, returns an error and leaves the
// rebase in progress. The editor every invocation runs exits non-zero, so a
// continue that still has to ask for a commit message fails rather than waits.
func (r *Repository) RebaseContinue(ctx context.Context) error {
	if r.kind != KindWorktree {
		return errors.New("vcs: rebase requires a working copy")
	}
	_, code, err := r.runExpecting(ctx, "rebase-continue", []int{1}, "rebase", "--continue")
	if err != nil {
		return err
	}
	if code != 0 {
		return ErrRebaseConflict
	}
	return nil
}

// RebaseSkip skips the current patch and replays what remains.
//
// A caller uses it when that patch has nothing to commit. The operation does
// not check that: the current patch is dropped either way. A later step that
// conflicts returns ErrRebaseConflict and leaves the rebase in progress.
// When nothing remains, the rebase is finished. The editor every invocation
// runs exits non-zero, so a skip that still has to ask for a commit message
// fails rather than waits.
func (r *Repository) RebaseSkip(ctx context.Context) error {
	if r.kind != KindWorktree {
		return errors.New("vcs: rebase requires a working copy")
	}
	_, code, err := r.runExpecting(ctx, "rebase-skip", []int{1}, "rebase", "--skip")
	if err != nil {
		return err
	}
	if code != 0 {
		return ErrRebaseConflict
	}
	return nil
}

// RebaseStatus reports whether a rebase is in progress.
//
// It is in progress when the rebase-merge directory or the rebase-apply
// directory git names for this working copy is present. Those are the
// directories a later abort or continue acts on. Neither being present is a
// false result, including for a clean copy, and that absence is not an error.
//
// REBASE_HEAD is not the signal. A rebase paused before any commit is picked
// has the directory and no such name, and abort still succeeds.
func (r *Repository) RebaseStatus(ctx context.Context) (inProgress bool, err error) {
	if r.kind != KindWorktree {
		return false, errors.New("vcs: rebase requires a working copy")
	}
	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		out, err := r.run(ctx, "rebase-status", "rev-parse", "--git-path", name)
		if err != nil {
			return false, err
		}
		path := strings.TrimSpace(string(out))
		if path == "" {
			return false, errors.New("vcs: git reported no path for " + name)
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(r.path, filepath.FromSlash(path))
		}
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return false, err
		}
		if info.IsDir() {
			return true, nil
		}
	}
	return false, nil
}
