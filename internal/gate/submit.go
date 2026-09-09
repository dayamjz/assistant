package gate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

// incomingRefPrefix is where TakeBranch stages a fetch before the commit it
// delivered is known. Each take fetches into a fresh name of its own here,
// reads what arrived, and only then creates the commit-keyed anchor under
// submittedRefPrefix; the staging name is deleted once the anchor stands.
//
// The staging step exists so that no fetch ever has a commit-keyed name as
// its destination. A fetch's destination is written from whatever the source
// holds at fetch time, so a fetch aimed at a name chosen from an earlier read
// is what could put one commit under a name that says another. A name used by
// no other take cannot collide with anything, and what it comes to hold is
// the answer rather than a claim to verify.
//
// Every path out of a take gives its staging name back, so what can leave one
// behind is a take that died between the fetch and the anchor, or one whose
// cleanup itself failed. A reference left that way is kept objects and
// nothing else: nothing reads this namespace, and extra reachability strands
// nothing. Nothing in this build sweeps it, which is the same accounting the
// anchors above carry, at the same forty bytes a name.
const incomingRefPrefix = "refs/assistant/incoming/"

// stagingRefName names one take's staging reference: a fresh name under
// incomingRefPrefix no other take is using.
func stagingRefName() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("gate: naming a take's staging reference: %w", err)
	}
	return incomingRefPrefix + hex.EncodeToString(raw[:]), nil
}

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
// # The reference is named for the commit, and the name is chosen after the fetch
//
// The fetch's destination is a staging reference under incomingRefPrefix used
// by no other take, never a commit-keyed name and never refs/heads/branch.
// The commit under validation is read off what the fetch actually delivered,
// and the anchor is then created at submittedRefPrefix + that commit, so the
// name and the target agree by construction rather than by verification. An
// earlier design chose the name from a read made before the fetch, and the
// branch moving between those two steps could put one commit under a name
// that says another, or fast-forward an earlier take's anchor; there are no
// longer two reads for the branch to move between.
//
// No force is used anywhere here, in any namespace, and no unforced update
// can move an existing reference either: the anchor is written with
// vcs.CreateRef, which can only create, so a name that exists is never
// rewritten - by this take, a racing take, or a retry. A take of a commit
// already anchored finds the name holding exactly that commit and reports it
// taken; the check is a read after a refused creation, and it cannot go stale
// against this package because nothing here ever moves a reference in this
// namespace.
//
// This is what keeps a take from taking an earlier run's anchor away. A branch
// rewritten locally - which is what a fix round does to one - takes cleanly
// under a NEW name, and the reference anchoring a run still validating the old
// commit is untouched. Keying the anchor by branch instead would have had the
// second take move the first run's only anchor off its copy's head and strand
// that copy permanently.
//
// A name in the submitted namespace holding a commit other than the one it is
// named for is refused with ErrForeignAnchor: this operation only ever
// creates that name over its own commit, so something else wrote what stands
// there, and it is not repaired here because the reference may be the
// reachability keeping another run's work alive. The refusal says what gets a
// caller out - moving the branch to a new commit takes it under a name
// nothing has written.
//
// This path never writes refs/heads in the gate. A branch there moves only
// where a push moves it, so git's own rejection of a non-fast-forward push
// stays exactly what it was, which is what keeps P6's answer there true.
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
		repo, err := vcs.OpenBare(ctx, h.repository)
		if err != nil {
			return fmt.Errorf("gate: opening the gate repository at %s: %w", h.repository, err)
		}
		staging, err := stagingRefName()
		if err != nil {
			return err
		}
		source := branchRefPrefix + branch
		if err := repo.Fetch(ctx, vcs.FetchSpec{
			Remote:   h.workingPath,
			Refspecs: []string{source + ":" + staging},
		}); err != nil {
			return fmt.Errorf("gate: taking %s from %s into the gate at %s: %w",
				branch, h.workingPath, h.repository, err)
		}
		landed, err := repo.ResolveCommit(ctx, staging)
		if err != nil {
			return fmt.Errorf("gate: reading what taking %s delivered to %s in the gate at %s: %w",
				branch, staging, h.repository, err)
		}
		destination := submittedRefPrefix + landed
		if err := repo.CreateRef(ctx, destination, landed); err != nil {
			standing, readErr := repo.ResolveCommit(ctx, destination)
			var refused error
			switch {
			case readErr == nil && standing == landed:
				// The anchor already stands over exactly this commit, which is
				// what a second take of an already-taken commit meets. The
				// creation was refused because there was nothing left to do.
			case readErr == nil:
				refused = fmt.Errorf("%w: %s in the gate at %s holds %s, and this operation only ever "+
					"creates that name over %s itself, so something else wrote it; it is not repaired "+
					"here, because the reference standing there may be the reachability keeping another "+
					"run's work alive; moving %s to a new commit, which an amend does, takes it under a "+
					"name nothing has written",
					ErrForeignAnchor, destination, h.repository, standing, landed, branch)
			default:
				refused = fmt.Errorf("gate: anchoring %s in the gate at %s: %w", landed, h.repository, err)
			}
			if refused != nil {
				// The staging reference is this take's own, so a refusal gives
				// it back rather than leaking one per retry. A cleanup failure
				// is joined rather than dropped, and the refusal stays
				// matchable through the join.
				if derr := repo.DeleteRef(ctx, staging, landed); derr != nil {
					return errors.Join(refused, fmt.Errorf("gate: giving back the take's staging "+
						"reference %s: %w", staging, derr))
				}
				return refused
			}
		}
		if err := repo.DeleteRef(ctx, staging, landed); err != nil {
			return fmt.Errorf("gate: %s is anchored in the gate at %s, but the take could not give "+
				"back its staging reference %s: %w; the leftover keeps objects reachable and nothing "+
				"reads it, and running the command again takes the branch without it",
				landed, h.repository, staging, err)
		}
		taken = landed
		return nil
	})
	return taken, err
}
