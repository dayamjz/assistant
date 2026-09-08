package gate

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/dayamjz/assistant/internal/vcs"
)

// AddCopy creates the disposable isolated copy one run works in: a linked
// worktree of the working copy's gate, at path, checked out at commit with a
// detached HEAD.
//
// It is a linked worktree rather than a clone on purpose. The object store is
// the gate's, so the commit the copy is checked out at is the commit the push
// actually delivered rather than a second copy of it, and the reachability
// question RemoveCopy asks later is answerable in the gate itself.
//
// The detached head is what PRD section 8's disposable copy wants: the run
// moves it - the rebase stage rebases it and a fix round commits to it - and no
// branch in the gate follows it, so nothing the run does to its own copy
// changes what the gate advertises.
//
// Where the copy lives is the caller's. This package composes the gate's own
// path under the home and nothing else, so the run's directory is named by
// whoever owns that layout, which is internal/home.
//
// It refuses the gate the working copy does not name rather than falling back
// to the one its path hashes to, because a copy taken from another project's
// gate would put a run's stages onto another project's code.
func AddCopy(ctx context.Context, spec Spec, path, commit string, opts ...Option) error {
	if path == "" || commit == "" {
		return fmt.Errorf("%w: a copy needs a path and a commit, got %q and %q",
			ErrInvalidSpec, path, commit)
	}
	return withGate(ctx, spec, gateNamed, opts, func(h *held) error {
		repo, err := vcs.OpenBare(ctx, h.repository)
		if err != nil {
			return fmt.Errorf("gate: opening the gate repository at %s: %w", h.repository, err)
		}
		if _, err := repo.AddWorktree(ctx, vcs.WorktreeSpec{Path: path, Commit: commit}); err != nil {
			return fmt.Errorf("gate: creating the isolated copy at %s from %s: %w", path, h.repository, err)
		}
		return nil
	})
}

// RemoveCopy gives back a copy AddCopy created, and refuses rather than
// removing one whose work nothing in the gate still references.
//
// # What it establishes before removing anything
//
// A copy is checked out at a detached head, so the worktree's own head is the
// only thing referencing a commit made inside it. Giving that up would leave
// the work referenced by nothing, which is the loss PRD principle P6 forbids,
// so this refuses with ErrWorkUnreachable when no reference in the gate
// contains the copy's head.
//
// The proof is bounded and is not P12 in full. It establishes that the commits
// are still referenced in the gate and so will not be collected. It is not a
// claim that the work reached the upstream remote or a merged pull request,
// which are the other two proofs P12 lists and which belong to a custody
// module this repository does not have.
//
// A fourth refusal is git's own and is used rather than worked around: the
// removal is the one that refuses a worktree holding modified or untracked
// files, never the one that discards them, so a copy holding uncommitted work
// survives with git's refusal reported.
//
// # The case that will actually produce the refusal
//
// A reader meeting a leftover copy should find this decided rather than wonder
// whether the reclaim is broken, so the case is named rather than left to be
// derived from the rule.
//
// The rebase stage is the first thing in this product that moves a copy's head
// to commits no reference in the gate contains: it rebases, and those commits
// exist only in the copy until the push stage forwards them. So a run ended
// between those two stages keeps its copy, with ErrWorkUnreachable reported on
// the service log and the run's verdict unchanged.
//
// That is this refusal working rather than a leak. The copy holds the only
// instance of the rebased commits, and P6 ranks losing work far above leaving
// a directory behind. Reclaiming such a copy - once its work is somewhere else
// - is separate work, and it is not a reason to relax this operation or to add
// an exception for the case.
//
// # The reaping gap, stated rather than implied
//
// PRD section 11 reaps the processes running in a copy before removing it, and
// nothing in this build reaps. internal/service registers a stage's process
// group through StageStarted and nothing calls it, so there is no set of live
// processes to ask about and no refusal here for one.
//
// That gap is named rather than filled with a refusal that could never fire. A
// missing refusal is an absence, and an absence is noticed; a refusal whose
// input has no producer reads as protection in the code and is trusted by the
// next reader. What it costs today is nothing, because no stage body in this
// build starts a process, so there is nothing running in a copy to pull the
// directory out from under. When a producer for StageStarted arrives, the
// hazard and this gap arrive together, and the refusal belongs here.
func RemoveCopy(ctx context.Context, spec Spec, path string, opts ...Option) error {
	if path == "" {
		return fmt.Errorf("%w: a copy to remove needs a path", ErrInvalidSpec)
	}
	return withGate(ctx, spec, gateNamed, opts, func(h *held) error {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			// A copy that has already gone is the outcome the caller wanted.
			// Reporting it as a refusal would teach a reader to step past the
			// one refusal that means data loss.
			return nil
		} else if err != nil {
			return fmt.Errorf("gate: reading the isolated copy at %s: %w", path, err)
		}
		repo, err := vcs.OpenBare(ctx, h.repository)
		if err != nil {
			return fmt.Errorf("gate: opening the gate repository at %s: %w", h.repository, err)
		}
		if err := copyWorkIsReferenced(ctx, repo, path); err != nil {
			return err
		}
		// The removal that refuses modified or untracked files, never
		// RemoveWorktreeDiscardingChanges. A copy holding uncommitted work is
		// preserved with git's own refusal reported, which is the fourth
		// refusal this operation applies and the only one it does not
		// implement itself.
		if err := repo.RemoveWorktree(ctx, path); err != nil {
			return fmt.Errorf("gate: removing the isolated copy at %s: %w", path, err)
		}
		return nil
	})
}

// copyWorkIsReferenced refuses when nothing in the gate contains the copy's
// head, so the commits made in it would be left referenced by nothing.
//
// A directory standing where a copy should be but which is not a worktree is
// refused rather than removed or reported as given back. Something this
// package did not create is there, removing it is not this operation's to do,
// and answering "given back" would leave a caller believing a directory is
// gone while it is still on disk. That is the same stance Remove takes toward
// a path it cannot identify as a gate.
func copyWorkIsReferenced(ctx context.Context, gateRepo *vcs.Repository, path string) error {
	copyOf, err := vcs.OpenWorktree(ctx, path)
	if err != nil {
		if errors.Is(err, vcs.ErrNotARepository) {
			return fmt.Errorf("%w: %s is not a worktree, so what is there was not created as this "+
				"run's copy and is not this operation's to remove; look at what is standing there "+
				"and move or delete it by hand, after which the next reclaim of this run finds "+
				"nothing at the path and reports the copy given back", ErrNotACopy, path)
		}
		return fmt.Errorf("gate: reading the isolated copy at %s: %w", path, err)
	}
	head, err := copyOf.ResolveCommit(ctx, "HEAD")
	if err != nil {
		// An unborn head is a copy nothing was committed in, so there is no
		// work in it to lose.
		if errors.Is(err, vcs.ErrRefNotFound) {
			return nil
		}
		return fmt.Errorf("gate: reading the head of the isolated copy at %s: %w", path, err)
	}
	refs, err := gateRepo.ListRefs(ctx)
	if err != nil {
		return fmt.Errorf("gate: reading what the gate references: %w", err)
	}
	for _, ref := range refs {
		contains, err := gateRepo.IsAncestor(ctx, head, ref.Commit)
		if err != nil {
			// A comparison that could not be answered is not evidence that
			// the work is unreferenced, so it is reported as unverifiable
			// rather than folded into the refusal below.
			return fmt.Errorf("gate: whether %s contains %s could not be determined: %w", ref.Name, head, err)
		}
		if contains {
			return nil
		}
	}
	return fmt.Errorf("%w: %s holds %s, which no reference in the gate at %s contains; the copy is "+
		"kept and nothing is lost, and what makes it removable is that commit reaching the gate, "+
		"which pushing the copy's work there does - the next reclaim of this run then gives the "+
		"copy back, and recovery asks again on every service open",
		ErrWorkUnreachable, path, head, gateRepo.Path())
}
