package vcs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
//
// The replay is started in the merge backend with --merge on the command
// line, whatever rebase.backend the configuration names, because that is the
// backend RebaseContinue's contract is stated for and tested against.
func (r *Repository) Rebase(ctx context.Context, spec RebaseSpec) error {
	if r.kind != KindWorktree {
		return errors.New("vcs: rebase requires a working copy")
	}
	onto, err := r.ResolveCommit(ctx, spec.Onto)
	if err != nil {
		return err
	}
	args := []string{"rebase", "--no-autostash", "--merge"}
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
//
// That contract is stated for the merge backend, which Rebase starts. A
// rebase-apply directory, which Rebase does not create, is refused with
// ErrRebaseBackend before git runs, and the copy is left as it was.
func (r *Repository) RebaseContinue(ctx context.Context) error {
	if r.kind != KindWorktree {
		return errors.New("vcs: rebase requires a working copy")
	}
	if _, err := r.rebaseInProgress(ctx); err != nil {
		return err
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
//
// Like RebaseContinue, it is stated for the merge backend, which Rebase
// starts: a rebase-apply directory is refused with ErrRebaseBackend before
// git runs, so no patch is dropped under a backend this package does not
// continue, and the copy is left as it was.
func (r *Repository) RebaseSkip(ctx context.Context) error {
	if r.kind != KindWorktree {
		return errors.New("vcs: rebase requires a working copy")
	}
	if _, err := r.rebaseInProgress(ctx); err != nil {
		return err
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
// It is in progress when the rebase-merge directory git names for this
// working copy is present. That is the directory a later abort or continue
// acts on. A rebase-apply directory, which Rebase does not create and
// RebaseContinue refuses, is refused here too with ErrRebaseBackend, rather
// than reported as in progress to a caller that would go on to continue it.
// Neither being present is a false result, including for a clean copy, and
// that absence is not an error.
//
// REBASE_HEAD is not the signal. A rebase paused before any commit is picked
// has the directory and no such name, and abort still succeeds.
func (r *Repository) RebaseStatus(ctx context.Context) (inProgress bool, err error) {
	if r.kind != KindWorktree {
		return false, errors.New("vcs: rebase requires a working copy")
	}
	return r.rebaseInProgress(ctx)
}

// rebaseInProgress is RebaseStatus's answer, including its refusal of the
// apply backend. The apply directory is read first, so a copy holding both
// directories is refused.
func (r *Repository) rebaseInProgress(ctx context.Context) (bool, error) {
	for _, name := range []string{"rebase-apply", "rebase-merge"} {
		out, err := r.run(ctx, "rebase-status", "rev-parse", "--git-path", name)
		if err != nil {
			return false, err
		}
		path := gitPath(out)
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
			if name == "rebase-apply" {
				return false, ErrRebaseBackend
			}
			return true, nil
		}
	}
	return false, nil
}
