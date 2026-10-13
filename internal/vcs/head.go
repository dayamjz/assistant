package vcs

import (
	"context"
	"strings"
)

// SwitchToNewBranch creates branch at commit in this working copy and checks
// it out, so HEAD is on the new branch and the working tree holds that
// commit. The branch must not already exist; git's refusal arrives as a
// *CommandError. commit is resolved through ResolveCommit first, so a
// revision that names nothing is ErrRefNotFound rather than whatever git
// would have said about the switch.
//
// It moves HEAD and the working tree and nothing else: no remote is
// consulted and no tracking configuration is written. It is what cuts a
// delivery task's branch in its clone, after Clone has checked the copy out
// on whatever branch the source's HEAD named.
func (r *Repository) SwitchToNewBranch(ctx context.Context, branch, commit string) error {
	if err := checkArg("branch", branch); err != nil {
		return err
	}
	id, err := r.ResolveCommit(ctx, commit)
	if err != nil {
		return err
	}
	_, err = r.run(ctx, "switch-create", "switch", "--quiet", "--no-track", "--create", branch, "--", id)
	return err
}

// Detach checks commit out at a detached HEAD in this working copy, so no
// branch moves when the copy is committed on. commit is resolved through
// ResolveCommit first, on SwitchToNewBranch's terms. It is what an
// investigation task's clone gets, because that copy creates no branch.
func (r *Repository) Detach(ctx context.Context, commit string) error {
	id, err := r.ResolveCommit(ctx, commit)
	if err != nil {
		return err
	}
	_, err = r.run(ctx, "switch-detach", "switch", "--quiet", "--detach", "--", id)
	return err
}

// UpdateRef points the reference name at commit, creating it when it does not
// exist and moving it when it does. commit is resolved through ResolveCommit
// first, so the object has to be in this repository already: nothing is
// fetched, and a commit that is not here is ErrRefNotFound.
//
// The name is given in full, as refs/..., for DeleteRef's reason: a short
// name resolves through git's disambiguation rules and could name something
// else tomorrow. It writes the one reference and moves no branch a working
// copy has checked out, because the gate repository it exists for is bare and
// the references it is written under are the program's own, not refs/heads/.
func (r *Repository) UpdateRef(ctx context.Context, name, commit string) error {
	if err := checkArg("ref", name); err != nil {
		return err
	}
	if !strings.HasPrefix(name, "refs/") {
		return &argumentError{what: "ref", value: name, reason: "is not a full reference name, which begins refs/"}
	}
	id, err := r.ResolveCommit(ctx, commit)
	if err != nil {
		return err
	}
	_, err = r.run(ctx, "update-ref", "update-ref", "--no-deref", "--end-of-options", name, id)
	return err
}
