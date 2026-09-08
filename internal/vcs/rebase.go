package vcs

import (
	"context"
	"errors"
	"strings"
)

// RebaseSpec describes one rebase: replay the commits Onto does not already
// contain from Commit's history, on top of Onto.
//
// Commit is a revision rather than a branch name on purpose. Git checks out
// what it is given before replaying, so a branch name would leave that branch
// pointing at the result; a commit leaves HEAD detached and moves no branch,
// which is what keeps this package's rule that no operation here moves a
// branch in a working copy.
type RebaseSpec struct {
	// Onto is the commit the replayed commits are placed on. It is also what
	// decides which commits are replayed: those reachable from Commit and not
	// from Onto.
	Onto string
	// Commit is the revision whose history is replayed. Naming a branch here
	// would move that branch, so callers name a commit.
	Commit string
}

// rebaseArgs are the options every rebase this package performs runs with.
// Each one takes a decision away from the repository's configuration, so two
// copies of the same history rebase the same way whatever the copy inherited.
var rebaseArgs = []string{
	"--quiet",
	"--no-stat",
	// rebase.autoStash and rebase.autoSquash are configuration a repository
	// may carry. Autostash would hide an unexpectedly dirty working tree by
	// stashing it, and autosquash would reorder and combine commits by reading
	// their subject lines. Neither is something a caller asked for here.
	"--no-autostash",
	"--no-autosquash",
}

// Rebase replays spec.Commit's history onto spec.Onto and returns the commit
// HEAD ends at. It leaves HEAD detached at that commit and moves no branch.
//
// A rebase with nothing to replay leaves HEAD at spec.Onto, which is what a
// change already contained in Onto produces. Whether that means the change has
// nothing left in it is the caller's comparison to make, not something read
// out of this result.
//
// A conflict is a *RebaseConflict, which matches ErrRebaseConflict and names
// the paths git left unmerged. The rebase is aborted before it is returned, so
// the working copy is left as this call found it rather than mid-rebase: a
// caller that stops on the error is not holding a repository nothing can be
// done with. What that costs is stated rather than implied: the conflicted
// index is gone, so a caller resolving the conflict resolves it in the history
// being replayed and rebases again, and cannot continue the stopped rebase.
//
// Any other failure is returned as git reported it. The abort attempt is what
// tells the two apart: a rebase that is not in progress cannot be aborted, so
// an abort that succeeds is what establishes that git stopped part way through
// rather than refusing to start.
func (r *Repository) Rebase(ctx context.Context, spec RebaseSpec) (string, error) {
	if r.kind != KindWorktree {
		return "", &argumentError{
			what:   "repository",
			value:  r.path,
			reason: "a rebase needs a working copy, and this handle addresses a " + r.kind.String() + " repository",
		}
	}
	onto, err := r.ResolveCommit(ctx, spec.Onto)
	if err != nil {
		return "", err
	}
	commit, err := r.ResolveCommit(ctx, spec.Commit)
	if err != nil {
		return "", err
	}
	args := append([]string{"rebase"}, rebaseArgs...)
	args = append(args, onto, commit)
	if _, err := r.run(ctx, "rebase", args...); err != nil {
		return "", r.afterFailedRebase(ctx, onto, commit, err)
	}
	return r.ResolveCommit(ctx, "HEAD")
}

// afterFailedRebase reads what a failed rebase left behind, puts the working
// copy back, and returns the error the caller sees.
//
// The unmerged paths are read before the abort, because aborting is what
// removes them. Reading them is best effort: a path list this could not
// produce leaves the conflict reported with no paths, which is a conflict
// described less well, whereas failing here would replace a conflict the
// caller can act on with an error about reading it.
func (r *Repository) afterFailedRebase(ctx context.Context, onto, commit string, cause error) error {
	paths, _ := r.unmergedPaths(ctx)
	if !r.abortRebase(ctx) {
		// No rebase was in progress, so git refused to start rather than
		// stopping part way through. The working copy was never changed and
		// git's own message is the whole answer.
		return cause
	}
	return &RebaseConflict{Onto: onto, Commit: commit, Paths: paths, Cause: cause}
}

// abortRebase puts the working copy back and reports whether there was a
// rebase to put back. Git refuses the abort when none is in progress, and that
// refusal is the answer rather than a failure to report.
func (r *Repository) abortRebase(ctx context.Context) bool {
	_, code, err := r.runExpecting(ctx, "rebase-abort", []int{1, 128}, "rebase", "--abort")
	return err == nil && code == 0
}

// unmergedPaths lists the paths the index holds more than one stage of, which
// is what a stopped merge leaves behind. Each path is listed once however many
// stages it has.
func (r *Repository) unmergedPaths(ctx context.Context) ([]string, error) {
	out, err := r.run(ctx, "unmerged-paths", "diff", "--name-only", "--diff-filter=U", "-z", "--")
	if err != nil {
		return nil, err
	}
	var paths []string
	seen := make(map[string]struct{})
	for _, path := range strings.Split(string(out), "\x00") {
		if path == "" {
			continue
		}
		if _, dup := seen[path]; dup {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	return paths, nil
}

// ErrRebaseConflict is what every rebase stopped by a conflict matches.
var ErrRebaseConflict = errors.New("vcs: rebase stopped on a conflict")

// RebaseConflict reports a rebase git stopped part way through, and what it
// stopped on. The rebase has been aborted by the time a caller holds one, so
// the working copy is not mid-rebase.
//
// Paths are the paths git left unmerged, read before the abort. It may be
// empty: the read is best effort, and an empty list means the paths could not
// be read rather than that nothing conflicted.
type RebaseConflict struct {
	// Onto is the commit the replay was onto, resolved.
	Onto string
	// Commit is the commit whose history was being replayed, resolved.
	Commit string
	// Paths are the paths left unmerged, repository-relative, or empty when
	// they could not be read.
	Paths []string
	// Cause is the failure git reported, with its message redacted.
	Cause error
}

// Error describes the conflict and the paths it stopped on.
func (e *RebaseConflict) Error() string {
	msg := "vcs: rebasing " + e.Commit + " onto " + e.Onto + " stopped on a conflict"
	if len(e.Paths) > 0 {
		msg += " in " + strings.Join(e.Paths, ", ")
	} else {
		msg += " whose paths could not be read"
	}
	if e.Cause != nil {
		msg += ": " + e.Cause.Error()
	}
	return msg
}

// Unwrap makes a conflict match ErrRebaseConflict and whatever git reported.
// A conflict with no cause unwraps to the sentinel alone, because a nil in the
// list would be an error nothing can be compared against.
func (e *RebaseConflict) Unwrap() []error {
	if e.Cause == nil {
		return []error{ErrRebaseConflict}
	}
	return []error{ErrRebaseConflict, e.Cause}
}
