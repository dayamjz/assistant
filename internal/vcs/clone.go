package vcs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Clone creates a working copy at to of the repository at from and returns a
// handle on it. What it buys a caller is a copy with exactly the remotes the
// caller named: when remote is non-empty, the copy's one remote is named
// remote and points at from; when remote is empty, the copy has no remote at
// all. The clone is made under a remote name this package passes and that
// remote is removed before the named one is added, so a caller asking for the
// same name gets one remote and not two settings of it.
//
// The copy is checked out on the branch from's HEAD names, and nothing here
// moves it from there; a caller that wants a task branch or a detached HEAD
// makes that change afterwards. from is a path or a URL. Submodule recursion
// is disabled, for the reason Fetch gives: a submodule configuration in the
// source would otherwise choose further URLs to contact, which PRD principle
// P7 does not let the branch under validation do.
//
// to must not exist. A to that is there, whatever it holds, is refused with
// ErrDestinationExists before git is invoked, so a copy already standing is
// never written into. That is this package refusing
// to decide what a directory already there means; the caller that put it
// there knows.
//
// Nothing is promised about remote-tracking references or branch
// configuration in the copy beyond the remote set: a test reads the copy's
// remote configuration back to establish that the result carries the named
// remote's URL and no other, and that is the whole of the claim.
func Clone(ctx context.Context, from, to, remote string, opts ...Option) (*Repository, error) {
	if err := checkArg("source", from); err != nil {
		return nil, err
	}
	abs, err := absolutePath(to)
	if err != nil {
		return nil, err
	}
	if remote != "" {
		if err := checkArg("remote", remote); err != nil {
			return nil, err
		}
	}
	if _, err := os.Lstat(abs); err == nil {
		return nil, &destinationError{path: abs}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("vcs: checking %s before cloning into it: %w", abs, err)
	}
	// git clone addresses its destination as an argument, so, as in InitBare,
	// the handle that runs it addresses the parent directory and git creates
	// the target.
	seed := &Repository{path: filepath.Dir(abs), kind: KindWorktree, set: newSettings(opts)}
	if _, err := seed.run(ctx, "clone", "clone", "--quiet", "--no-recurse-submodules",
		"--origin", clonedRemote, "--end-of-options", from, abs); err != nil {
		return nil, err
	}
	cloned, err := OpenWorktree(ctx, abs, opts...)
	if err != nil {
		return nil, err
	}
	if err := cloned.RemoveRemote(ctx, clonedRemote); err != nil {
		return nil, fmt.Errorf("vcs: removing the remote the clone at %s was made with: %w", abs, err)
	}
	if remote != "" {
		if err := cloned.SetRemote(ctx, remote, from); err != nil {
			return nil, err
		}
	}
	return cloned, nil
}

// clonedRemote is the name Clone makes the clone under and then removes. It is
// passed rather than left to git's default, so the name removed is the name
// this package chose. A clone that comes out without it is reported as a
// failure rather than taken as already done, because that is not the shape
// this package knows how to finish.
const clonedRemote = "origin"

// destinationError reports a Clone refused because something is already at
// the destination.
type destinationError struct {
	path string
}

func (e *destinationError) Error() string {
	return "vcs: refusing to clone into " + e.path + ": something is already there"
}

// Unwrap makes every refused destination match ErrDestinationExists.
func (e *destinationError) Unwrap() error { return ErrDestinationExists }
