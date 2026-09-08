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

// TakeBranch puts the working copy's branch into the gate and reports the
// commit the gate then has for it.
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
// The reference is moved only where a push would move it. The refspec carries
// no leading plus, so a branch that would not fast-forward is refused by git
// rather than forced, and a gate reference is never rewritten here to make a
// run startable.
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
		ref := "refs/heads/" + branch
		if err := repo.Fetch(ctx, vcs.FetchSpec{
			Remote:   h.workingPath,
			Refspecs: []string{ref + ":" + ref},
		}); err != nil {
			return fmt.Errorf("gate: taking %s from %s into the gate at %s: %w",
				branch, h.workingPath, h.repository, err)
		}
		commit, err := repo.ResolveCommit(ctx, ref)
		if err != nil {
			return fmt.Errorf("gate: reading %s in the gate at %s after taking it: %w",
				ref, h.repository, err)
		}
		taken = commit
		return nil
	})
	return taken, err
}
