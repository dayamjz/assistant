package vcs

import (
	"context"
	"errors"
)

// Inventory is what a working copy holds that could be work: every local
// reference and the commit it reaches, where HEAD stands, and whether the
// tree, the untracked files, or the stash hold anything a reference does
// not. Repository.Inventory fills one and nothing else does.
//
// PRD section 5 makes a task's copy a full clone, so its work is whatever its
// local references reach, plus a HEAD that is on no reference at all. That is
// what Refs and Head together are for: a cleanup decision compares an
// inventory against the one its dispatch recorded, and again against the one
// it just proved landed, and this type is the set compared. Nothing here
// decides; the comparison and what it refuses on belong to the caller.
//
// Remote-tracking references are deliberately not in Refs. A proof that work
// landed fetches the default branch into this same copy, which writes those
// references, so an inventory that counted them would differ from itself
// across the proof's own fetch and no copy could ever be shown unchanged.
type Inventory struct {
	// Refs is every reference under refs/heads/, refs/tags/, and refs/stash,
	// in git's own order, each with the commit it reaches once annotated
	// tags are followed. It is nil when there are none.
	Refs []Ref
	// Head is where HEAD stands.
	Head HeadState
	// Dirty reports that a tracked file differs from HEAD in the tree or in
	// the index: a modification, a deletion, a rename, or an unresolved
	// merge. Ignored files are not consulted, which is the residual gap:
	// a file git is configured to ignore is invisible here as it is to
	// CommitAll.
	Dirty bool
	// Untracked reports that the working copy holds a file no commit records
	// and git is not configured to ignore.
	Untracked bool
	// Stash reports that refs/stash exists. The stash is in Refs as well,
	// because it is a commit the worker made and so part of what the copy
	// reaches; it is reported here too because a cleanup decision refuses on
	// it the way it refuses on a dirty tree, without having to know the
	// reference's name.
	Stash bool
}

// HeadState is where a working copy's HEAD stands.
type HeadState struct {
	// Commit is the commit HEAD resolves to.
	Commit string
	// Branch is the short name of the branch HEAD is on, and empty when HEAD
	// is detached.
	Branch string
	// Detached reports that HEAD is on no branch, and is the only case in
	// which Branch is empty. A detached Commit that no entry of
	// Inventory.Refs reaches is work on no reference, and the inventory
	// reports both halves so a caller can see that it is.
	Detached bool
}

// Inventory reads what this working copy holds, as Inventory describes. It
// reads: it writes no reference and moves nothing. It refuses with
// ErrNotARepository on a bare repository, which has no tree to inventory, and
// with ErrRefNotFound on a working copy whose HEAD names no commit yet,
// because a copy with no commit was not observed.
//
// What two inventories taken apart establish is that the references, HEAD,
// and the three indicators read the same both times. A fetch into the copy
// between the two does not change what this reports, by the exclusion
// Inventory states; a commit, a branch, a tag, a stash, or an edit does.
func (r *Repository) Inventory(ctx context.Context) (Inventory, error) {
	if r.kind != KindWorktree {
		return Inventory{}, &openError{path: r.path, kind: KindWorktree}
	}
	var inv Inventory
	refs, err := r.ListRefs(ctx, "refs/heads/", "refs/tags/", "refs/stash")
	if err != nil {
		return Inventory{}, err
	}
	inv.Refs = refs
	for _, ref := range refs {
		if ref.Name == "refs/stash" {
			inv.Stash = true
		}
	}
	inv.Head.Commit, err = r.ResolveCommit(ctx, "HEAD")
	if err != nil {
		return Inventory{}, err
	}
	branch, err := r.HeadBranch(ctx)
	switch {
	case err == nil:
		inv.Head.Branch = branch
	case errors.Is(err, ErrDetachedHead):
		inv.Head.Detached = true
	default:
		return Inventory{}, err
	}
	inv.Dirty, inv.Untracked, err = r.treeState(ctx)
	if err != nil {
		return Inventory{}, err
	}
	return inv, nil
}

// treeState reports whether a tracked file differs from HEAD and whether an
// untracked file exists, through the one status read Changes makes, so the
// inventory and Changes never disagree about a tree. That read is what keeps
// this one from taking the index lock a worker's own git may hold.
func (r *Repository) treeState(ctx context.Context) (dirty, untracked bool, err error) {
	c, err := r.changes(ctx)
	if err != nil {
		return false, false, err
	}
	return c.Dirty(), c.Untracked, nil
}
