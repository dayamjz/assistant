package tasks

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/dayamjz/assistant/internal/safety"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// supportedLayout refuses, with ErrLayoutUnsupported naming the layout,
// a copy whose repository is not laid out the one way Loss supports,
// before anything of it is read (round 25: each layout the discard's
// set-aside tried to handle opened another way to lose work, so they are
// refused rather than handled; P6, a safety fact that cannot be verified
// refuses). Loss is a land's and a discard preview's read, so both are
// refused the same. The one layout is:
//
//   - for a task's clone, .git a directory of the clone's own, holding no
//     commondir file and no linked worktree; for a run's worktree, .git
//     the file git writes, which copyRecord holds to the gate's record of
//     the worktree;
//   - no symbolic link anywhere in the repository directory git reads for
//     the copy, its references, reflogs, objects and index among it;
//   - core.symlinks not false, no sparse checkout, and the references in
//     files (no reftable);
//   - the whole history (partialHistory, round 32): not shallow (git
//     rev-parse --is-shallow-repository), no graft file (info/grafts),
//     no replacement references (refs/replace/), and not a partial clone
//     (extensions.partialClone unset, no remote.<name>.promisor true, and
//     no promisor pack), since in each git reads the commits and objects a
//     preview is built from as other than the copy holds, so the preview
//     would leave out commits the copy holds, and a land would decide
//     what it leaves behind from that same partial reading.
//
// Before any of that, git is asked which working tree and repository
// directory it reads for the copy (vcs.Observe: rev-parse --show-toplevel
// --absolute-git-dir), and both must be the copy's own: the working tree
// the copy itself, and for a clone the repository directory its own .git,
// each compared once links are resolved.
// So a copy git reads as part of another checkout is refused rather than
// read or recovered into, whether through a .git git does not take for a
// repository, a core.worktree of the copy naming another directory, or
// another repository's core.worktree naming the copy. The configuration is read as git
// reads it for the copy, the user's and the system's included. The
// repository directory is walked through the file system, so a link is
// refused wherever it stands.
func (c *Cleaner) supportedLayout(ctx context.Context, entry store.KeptWork, gatePath string) error {
	if err := c.VerifyKeptCopy(ctx, entry, gatePath); err != nil {
		return err
	}
	refuse := func(why string) error {
		return fmt.Errorf("%w: %s %s", ErrLayoutUnsupported, entry.CopyPath, why)
	}
	copied, err := vcs.OpenWorktree(ctx, entry.CopyPath, c.git...)
	if err != nil {
		return refuse("could not be opened: " + err.Error())
	}
	if entry.TaskID.IsKnown() {
		dotGit := filepath.Join(entry.CopyPath, ".git")
		if _, err := os.Lstat(filepath.Join(dotGit, "commondir")); !errors.Is(err, fs.ErrNotExist) {
			return refuse("has a commondir file in its .git, naming another directory its references and objects are in")
		}
		if linked, err := os.ReadDir(filepath.Join(dotGit, "worktrees")); err == nil && len(linked) > 0 {
			return refuse("has a linked worktree")
		}
	}
	if symlinks, set, err := copied.ConfigBool(ctx, "core.symlinks"); err != nil {
		return refuse("has a core.symlinks that could not be read: " + err.Error())
	} else if set && !symlinks {
		return refuse("sets core.symlinks false, so git checks a link out as a file")
	}
	if sparse, _, err := copied.ConfigBool(ctx, "core.sparseCheckout"); err != nil || sparse {
		return refuse("is a sparse checkout, whose paths left out read as deleted")
	}
	if format, set, err := copied.ConfigValue(ctx, "extensions.refStorage"); err != nil || set && format != "files" {
		return refuse("keeps its references in a " + format + " store rather than in files")
	}
	return partialHistory(ctx, copied, refuse)
}

// partialHistory refuses, through refuse, a copy whose history git reads
// as other than every commit its objects record, or whose objects may name
// what it does not hold: shallow, a graft file, replacement references, or
// a partial clone. Each is read the way git reads it for the copy, through
// internal/vcs, and one that cannot be read refuses as well (round 32).
func partialHistory(ctx context.Context, copied *vcs.Repository, refuse func(string) error) error {
	unread := func(what string, err error) error {
		return refuse("could not be read for " + what + ": " + err.Error())
	}
	if shallow, err := copied.IsShallow(ctx); err != nil {
		return unread("whether its history is shallow", err)
	} else if shallow {
		return refuse("is shallow, so the commits its history leaves out are read as not there and no preview shows them")
	}
	// The graft file is found through its directory: the Cleaner reads
	// with GIT_GRAFT_FILE set to the null device (vcs.WithoutReplaceObjects),
	// which git's --git-path info/grafts answers with.
	info, err := copied.GitPath(ctx, "info")
	if err != nil {
		return unread("a graft file", err)
	}
	grafts := filepath.Join(info, "grafts")
	if _, err := os.Lstat(grafts); !errors.Is(err, fs.ErrNotExist) {
		return refuse("has a graft file at " + grafts + ", which git reads a commit's parents through")
	}
	if replace, err := copied.ListRefs(ctx, "refs/replace/"); err != nil {
		return unread("replacement references", err)
	} else if len(replace) > 0 {
		return refuse("has replacement references, " + replace[0].Name + " among them, which git reads an object through as another")
	}
	if remote, set, err := copied.ConfigValue(ctx, "extensions.partialClone"); err != nil {
		return unread("extensions.partialClone", err)
	} else if set {
		return refuse("is a partial clone: extensions.partialClone names " + remote + ", so its objects may name what it does not hold")
	}
	promisors, err := copied.ConfigNames(ctx, `^remote\..*\.promisor$`)
	if err != nil {
		return unread("promisor remotes", err)
	}
	for _, key := range promisors {
		if promisor, _, err := copied.ConfigBool(ctx, key); err != nil {
			return unread(key, err)
		} else if promisor {
			return refuse("is a partial clone: " + key + " is true, so its objects may name what it does not hold")
		}
	}
	packs, err := copied.GitPath(ctx, "objects/pack")
	if err != nil {
		return unread("promisor packs", err)
	}
	marked, err := filepath.Glob(filepath.Join(packs, "*.promisor"))
	if err != nil {
		return unread("promisor packs", err)
	}
	if len(marked) > 0 {
		return refuse("is a partial clone: it holds the promisor pack " + marked[0] + ", whose objects may name what it does not hold")
	}
	return nil
}

// VerifyKeptCopy refuses, before anything of the entry's copy is read or
// written, a copy that is not the one its registry recorded, on
// internal/safety's terms (safety.VerifyKeptCopy): the registry is the
// entry's copy path and its own .git for a task's clone, and for a run's
// worktree the gate at gatePath, whose registry names the worktree's
// record (copyRecord, which also holds the worktree's .git file to it).
// Every path that answers a kept copy asks it first: the loss a discard
// shows and a land reads, and carry on, before its claim and, when
// a stopped carry on is
// taken up, on the copy where it stands, at the reserved task's path once
// moved (internal/service's verifyCarriedCopy, with the entry's copy path
// set to where it stands); and a promotion, whose flip answers its copy's
// entries carry on, before the flip and again before it writes into the
// copy (Queue.Promote, through verifyKeptCopy, this method's body). A
// refusal matches ErrLayoutUnsupported, and safety.ErrCopyNotVerified or
// ErrRecordNotTheCopys as well.
func (c *Cleaner) VerifyKeptCopy(ctx context.Context, entry store.KeptWork, gatePath string) error {
	return verifyKeptCopy(ctx, c.git, entry, gatePath)
}

// verifyKeptCopy is VerifyKeptCopy over the git options given, so a Queue,
// which holds no Cleaner, asks the one check and not a second one; it
// passes its own options with replacement references off, as a Cleaner's
// are.
func verifyKeptCopy(ctx context.Context, git []vcs.Option, entry store.KeptWork, gatePath string) error {
	want := safety.KeptCopy{Worktree: entry.CopyPath, GitDir: filepath.Join(entry.CopyPath, ".git")}
	if !entry.TaskID.IsKnown() {
		bare, err := vcs.OpenBare(ctx, gatePath, git...)
		if err != nil {
			return fmt.Errorf("tasks: opening the gate repository at %s: %w", gatePath, err)
		}
		if want.GitDir, err = copyRecord(ctx, bare, entry.CopyPath); err != nil {
			return fmt.Errorf("%w: %w", ErrLayoutUnsupported, err)
		}
	}
	if err := safety.VerifyKeptCopy(ctx, want, git...); err != nil {
		return fmt.Errorf("%w: %w", ErrLayoutUnsupported, err)
	}
	return nil
}
