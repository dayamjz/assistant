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

// submittedRefPrefix is where TakeBranch anchors a commit it takes into the
// gate. The reference's name carries the commit it holds, so the full name of
// a reference is submittedRefPrefix followed by that commit.
//
// These references are not litter, and the shape they have is exactly the
// shape somebody later mistakes for it, so what they are comes first. They ARE
// the reachability that keeps a validated commit alive in the gate: a run's
// isolated copy is a detached worktree, and git does not list a linked
// worktree's head among a repository's references, so the anchor here is the
// only thing containing that commit until a push forwards it. Deleting one
// strands the copy it anchored - RemoveCopy then refuses to give that copy back
// with ErrWorkUnreachable, on every service open, for as long as the directory
// stands.
//
// They accumulate: one per distinct commit any run validated, about forty
// bytes each, and nothing in this build reads one by name. Growth is the price
// of the property above rather than an oversight. Reclaiming them is separate
// work, filed as assistant-stranded-copy-reclaim, and it belongs with whatever
// establishes that a commit's work has landed somewhere else; there is no
// cleanup here because a cleanup that cannot establish that would be the
// stranding above with a schedule.
const submittedRefPrefix = "refs/assistant/submitted/"

// TakeBranch puts the working copy's branch into the gate, under a reference
// this package owns, and reports the commit the gate then holds for it.
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
// # The reference is named for the commit, which is what makes it safe
//
// The destination is submittedRefPrefix + commit, never refs/heads/branch, and
// the refspec carries no leading plus. It needs none, and that is a property
// of the name rather than a risk accepted: a reference whose name IS its
// target either does not exist or already holds exactly the commit being
// fetched, so there is no update for git to reject and nothing this could
// overwrite. No force is used anywhere here, in any namespace.
//
// This is what keeps a take from taking an earlier run's anchor away. A branch
// rewritten locally - which is what a fix round does to one - takes cleanly
// under a NEW name, and the reference anchoring a run still validating the old
// commit is untouched, because nothing here ever moves a reference. Keying the
// anchor by branch instead would have had the second take move the first run's
// only anchor off its copy's head and strand that copy permanently.
//
// This path never writes refs/heads in the gate. A branch there moves only
// where a push moves it, so git's own rejection of a non-fast-forward push
// stays exactly what it was, which is what keeps P6's answer there true.
//
// # The name and the target are checked to agree
//
// The reference cannot be named until the commit is known, so the branch is
// resolved in the working copy first and fetched into the reference named for
// what it read. Those are two steps, and the branch can move between them, in
// which case the fetch would put one commit under a name that says another.
// The whole design rests on the name and the target agreeing, so that is
// established rather than assumed: the reference is read back afterwards and a
// disagreement is refused, naming running the command again as the step that
// succeeds. What comes back is the verified commit.
//
// # What the reference establishes, and what it does not
//
// It contains the submitted commit, and no later take can move it off, so that
// commit is not collected while a run validates it.
//
// It says nothing about a commit made later inside a run's copy. RemoveCopy's
// reachability refusal reads every reference in the gate, and this one contains
// the submitted commit and nothing beyond it, so a copy whose head the rebase
// stage moved is still contained by no reference and is still kept rather than
// removed.
func TakeBranch(ctx context.Context, spec Spec, branch string, opts ...Option) (string, error) {
	if branch == "" {
		return "", fmt.Errorf("%w: no branch to take", ErrInvalidSpec)
	}
	taken := ""
	err := withGate(ctx, spec, gateNamed, opts, func(h *held) error {
		working, err := vcs.OpenWorktree(ctx, h.workingPath)
		if err != nil {
			return fmt.Errorf("gate: opening the working copy at %s: %w", h.workingPath, err)
		}
		source := branchRefPrefix + branch
		commit, err := working.ResolveCommit(ctx, source)
		if err != nil {
			return fmt.Errorf("gate: reading %s in %s to name the reference it is taken into: %w",
				source, h.workingPath, err)
		}
		repo, err := vcs.OpenBare(ctx, h.repository)
		if err != nil {
			return fmt.Errorf("gate: opening the gate repository at %s: %w", h.repository, err)
		}
		destination := submittedRefPrefix + commit
		if err := repo.Fetch(ctx, vcs.FetchSpec{
			Remote:   h.workingPath,
			Refspecs: []string{source + ":" + destination},
		}); err != nil {
			return fmt.Errorf("gate: taking %s from %s into the gate at %s: %w",
				branch, h.workingPath, h.repository, err)
		}
		landed, err := repo.ResolveCommit(ctx, destination)
		if err != nil {
			return fmt.Errorf("gate: reading %s in the gate at %s after taking it: %w",
				destination, h.repository, err)
		}
		if landed != commit {
			return fmt.Errorf("gate: %s in the gate at %s holds %s, and the reference is named for "+
				"%s: %s moved in %s while it was being taken, so nothing here anchors either commit "+
				"by a name that means it; run the command again to take the branch where it now stands",
				destination, h.repository, landed, commit, branch, h.workingPath)
		}
		taken = commit
		return nil
	})
	return taken, err
}
