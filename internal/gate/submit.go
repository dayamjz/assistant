package gate

import (
	"context"
	"errors"
	"fmt"

	"github.com/dayamjz/assistant/internal/vcs"
)

// Holds reports whether the gate bound to the working copy holds commit.
//
// It is what makes "the gate holds every commit under validation" checkable
// rather than assumed. A run recorded against a commit the gate does not hold
// is a run whose isolated copy cannot be made, because that copy is a linked
// worktree of this repository, and every stage after the first reads the
// change out of it.
//
// What it establishes is that the object is in the gate, which is what
// creating a worktree at it needs. It is not a claim that a reference points
// at it: an object reachable from nothing is still resolvable until it is
// collected. TakeBranch is what puts a reference over it, and the start path
// calls that rather than relying on this.
func Holds(ctx context.Context, spec Spec, commit string, opts ...Option) (bool, error) {
	if commit == "" {
		return false, fmt.Errorf("%w: no commit to look for", ErrInvalidSpec)
	}
	found := false
	err := withGate(ctx, spec, gateNamed, opts, func(h *held) error {
		repo, err := vcs.OpenBare(ctx, h.repository)
		if err != nil {
			return fmt.Errorf("gate: opening the gate repository at %s: %w", h.repository, err)
		}
		if _, err := repo.ResolveCommit(ctx, commit); err != nil {
			if errors.Is(err, vcs.ErrRefNotFound) {
				return nil
			}
			return fmt.Errorf("gate: asking whether the gate at %s holds %s: %w", h.repository, commit, err)
		}
		found = true
		return nil
	})
	return found, err
}

// submittedRefPrefix is where TakeBranch puts a branch it takes into the gate.
//
// It is a namespace this package writes and nothing else does. No push a
// caller makes lands in it, no branch of a working copy tracks it, and no
// other operation in this repository writes it, which is what makes forcing
// the update there safe: there is no history under it to lose.
const submittedRefPrefix = "refs/assistant/submitted/"

// TakeBranch puts the working copy's branch into the gate, under a reference
// this package owns, and reports the commit the gate then has for it.
//
// PRD principle P1 makes pushing to the gate the consent boundary, and PRD
// section 9's bare command starts a run from the branch you are on. Those two
// only agree if the branch reaches the gate, so this is what the start path
// calls before a run is recorded: the invocation is the consent, and this is
// how the thing consented to reaches somewhere the gate can see it. The gate
// is a local bare repository, so nothing is published anywhere by it.
//
// The gate fetches from the working copy rather than the working copy pushing
// to the gate, because internal/vcs has no push and this needs none: a fetch
// writes references and objects and nothing else. The direction is not a
// weakening of P1. What P1 guards against is validation starting without the
// person asking - an ambient hook, a rewired origin, a background trigger -
// and none of those reach this, which runs only inside a command somebody
// invoked.
//
// # Where it writes, and why the forced refspec is safe there
//
// The destination is submittedRefPrefix + branch, never refs/heads/branch,
// and the refspec carries a leading plus. A reader meeting that plus on its
// own would be right to read a P6 hazard, so the thing that makes it safe is
// named here with it: the safety is a property of the namespace rather than of
// the update. Nothing but this operation writes under refs/assistant/, so
// there is no history there for a force to destroy - what it overwrites is the
// commit a previous take of the same branch put there, which is by
// construction something this operation wrote and nothing a person has.
//
// This path never writes refs/heads in the gate and never forces one. A branch
// in the gate moves only where a push moves it, so git's own rejection of a
// non-fast-forward push stays exactly what it was, which is what keeps P6's
// answer there true.
//
// Writing outside refs/heads is also what makes this total rather than
// conditional. Rewriting a commit is what a fix round does to a branch, and a
// rewritten branch is not a descendant of what the gate last took; a take into
// refs/heads would be refused for that, and the bare command would then have
// no way to start a run on the branch at all.
//
// # What the reference establishes, and what it does not
//
// It puts a reference over the submitted commit, so that commit is contained
// by something in the gate for as long as the branch's latest take stands and
// is not collected while a run validates it.
//
// It says nothing about a commit made later inside a run's copy.
// RemoveCopy's reachability refusal reads every reference in the gate, and
// this one contains the submitted commit and nothing beyond it, so a copy
// whose head the rebase stage moved is still contained by no reference and is
// still kept rather than removed.
func TakeBranch(ctx context.Context, spec Spec, branch string, opts ...Option) (string, error) {
	if branch == "" {
		return "", fmt.Errorf("%w: no branch to take", ErrInvalidSpec)
	}
	taken := ""
	err := withGate(ctx, spec, gateNamed, opts, func(h *held) error {
		repo, err := vcs.OpenBare(ctx, h.repository)
		if err != nil {
			return fmt.Errorf("gate: opening the gate repository at %s: %w", h.repository, err)
		}
		source := branchRefPrefix + branch
		destination := submittedRefPrefix + branch
		if err := repo.Fetch(ctx, vcs.FetchSpec{
			Remote:   h.workingPath,
			Refspecs: []string{"+" + source + ":" + destination},
		}); err != nil {
			return fmt.Errorf("gate: taking %s from %s into the gate at %s: %w",
				branch, h.workingPath, h.repository, err)
		}
		commit, err := repo.ResolveCommit(ctx, destination)
		if err != nil {
			return fmt.Errorf("gate: reading %s in the gate at %s after taking it: %w",
				destination, h.repository, err)
		}
		taken = commit
		return nil
	})
	return taken, err
}
