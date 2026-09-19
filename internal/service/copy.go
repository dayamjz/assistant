package service

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// The isolated copy a run works in, and who builds it.
//
// PRD section 8 gives every run a copy of its own at worktrees/<repository>/<run>,
// and internal/stages' StageDeps.Copy opens it. Opening is all a stage body
// does: a body that created its own would be working somewhere this service
// does not know to reclaim, and seven of the nine bodies would each be a
// second place the decision was made. So the service builds it, once, before
// the run walks anything, and takes it away when the run is over.
//
// # What it is cut from
//
// The gate's bare repository, as a linked worktree detached at the commit the
// run validates. Cutting it from the gate rather than from the person's own
// working copy is what keeps everything the run does out of that copy: the
// rebase body fetches and prunes, and those write references. Under the gate
// they are the service's own references, shared only with other runs of the
// same repository. Cut from a person's working copy they would be that
// person's, which is the kind of change to a repository they did not ask for
// that PRD principle P1 exists to keep out.
//
// Detached is deliberate and is what WorktreeSpec calls the shape a disposable
// copy of one run wants. A branch checked out here would be a second writable
// name for the branch under validation, in a repository whose branches are
// what a push writes.
//
// # Getting the commit into the gate repository
//
// A push to the gate puts the commit there, and a run started any other way
// does not: assistant start reads a working copy and records the commit it is
// standing on, which the gate repository has never seen. So the commit is
// fetched from the working copy the repository record names, every time,
// rather than only when it is missing. One path that always runs is worth more
// than a conditional that is exercised on one of the two ways a run starts:
// the fetch is cheap when the commit is already there, and a build where it is
// wrong is wrong for every run rather than for the half nobody tested.
//
// It is fetched by commit and not by branch. The branch tip and the commit the
// run records are the same thing for about as long as it takes to record it,
// and a fetch by name would quietly validate whatever the branch had moved to
// instead. Nothing here reads the working copy for anything else, and the
// fetch writes nothing in it.
//
// # The upstream remote
//
// A worktree shares its repository's configuration, so the remote is set on
// the gate repository and every run of that repository sees it. It names the
// upstream the repository record carries, under the name the rebase body
// fetches from, because that body's job is to rebase onto fresh upstream and
// the gate repository holds only what has been pushed to the gate: the base
// branch is generally not in it.
//
// Setting it here rather than at assistant init is what keeps it true. The
// upstream is the repository record's fact and that record can be written
// again; a remote set once at initialization would go stale the first time it
// was. It is idempotent, so a run of a repository whose remote is already
// right changes nothing.
//
// This is the gate's repository and never a person's own working copy, so PRD
// principle P1's promise about a person's own origin is untouched: nothing
// here opens the working copy at all.
//
// # Reclaim, and the work it may not throw away
//
// A copy is removed when its run reaches a terminal status, and a held run
// keeps its copy, because a hold is answered later and the run resumes in the
// copy it stopped in.
//
// Removal refuses rather than discards. Git will not remove a worktree holding
// modified or untracked files, and that refusal is taken as the answer: the
// copy is left standing and the reason recorded. What is in it at that point
// is whatever the run left - an agent's edits that never reached a commit,
// output a body wrote - and a run that ended badly is exactly when somebody
// may want to look at it. P6 says never lose work, and a removal that forced
// its way past that refusal would be this service deciding, on a run that has
// already failed, that nothing in it mattered.
//
// The cost is that copies accumulate for runs that left changes behind. That
// is the intended trade and not an oversight: they are under the home, they
// are named by the run they belong to, and a person can remove one. Nothing
// here prunes them on a timer, because a timer would be the same decision
// taken later with less information.

// ensureCopy stands up the isolated copy record's run works in, and points the
// gate repository it is cut from at the repository's upstream.
//
// It is idempotent in the one way that matters: a copy already standing at the
// path is kept and reported as-is. A run resumes in the copy it stopped in, so
// a second call for a run that has one must not replace it and lose what the
// run has done there.
//
// Both facts it locates the copy from are the record's, for the reason begin
// takes them from the record: the path a body opens and the path this reclaims
// are derived from the same row, so they cannot disagree.
func (s *Service) ensureCopy(ctx context.Context, record store.Run) error {
	path := s.home.Worktree(record.RepositoryID, record.ID)
	if standing, err := copyStanding(ctx, path); err != nil {
		return err
	} else if standing {
		return nil
	}
	repository, err := s.store.Repository(ctx, record.RepositoryID)
	if err != nil {
		return fmt.Errorf("service: reading the repository run %s is of: %w", record.ID, err)
	}
	bare, err := s.gateRepositoryOf(ctx, repository, record.ID)
	if err != nil {
		return err
	}
	if repository.UpstreamURL != "" {
		if err := bare.SetRemote(ctx, upstreamRemote, repository.UpstreamURL); err != nil {
			return fmt.Errorf("service: pointing %s at the upstream for run %s: %w",
				upstreamRemote, record.ID, err)
		}
	}
	if err := bare.Fetch(ctx, vcs.FetchSpec{
		Remote:   repository.WorkingPath,
		Refspecs: []string{"+" + record.SubmittedHead + ":" + submittedRef(record.ID)},
	}); err != nil {
		return fmt.Errorf("service: bringing the commit run %s validates into the gate repository: %w",
			record.ID, err)
	}
	if _, err := bare.AddWorktree(ctx, vcs.WorktreeSpec{Path: path, Commit: record.SubmittedHead}); err != nil {
		return fmt.Errorf("service: creating the isolated copy for run %s at %s: %w", record.ID, path, err)
	}
	return nil
}

// gateRepositoryOf opens the gate repository a run of this repository is cut
// from.
//
// A run needs a gate, and this is where that stops being advice. assistant
// doctor has always reported a working copy with no gate as one no run can
// start from; until a run needed something out of the gate repository, nothing
// held a run to it, and a run could start against a repository that had never
// been initialized. It cannot now, and the refusal names the command that
// fixes it.
func (s *Service) gateRepositoryOf(ctx context.Context, repository store.Repository, runID string) (
	*vcs.Repository, error) {
	binding, err := s.store.GateBinding(ctx, repository.WorkingPath)
	if err != nil {
		return nil, fmt.Errorf("service: run %s is of %s, which is not bound to a gate, so there is no "+
			"repository to cut its isolated copy from; run assistant init there: %w",
			runID, repository.WorkingPath, err)
	}
	repositoryPath, err := gate.RepositoryFor(s.home.Root(), binding.GateID)
	if err != nil {
		return nil, fmt.Errorf("service: locating the gate repository run %s is cut from: %w", runID, err)
	}
	bare, err := vcs.OpenBare(ctx, repositoryPath, vcs.WithRedactor(redact.New()))
	if err != nil {
		return nil, fmt.Errorf("service: opening the gate repository run %s is cut from: %w", runID, err)
	}
	return bare, nil
}

// submittedRef is the name the gate repository holds a run's submitted commit
// under. It is named for the run so that two runs cannot write each other's,
// and it is what keeps the commit reachable between the fetch that brings it
// in and the worktree that checks it out.
func submittedRef(runID string) string { return "refs/assistant/runs/" + runID }

// upstreamRemote is the name the run's copy reaches the upstream under.
//
// It is internal/stages' constant rather than a second spelling of the same
// word: that package's rebase body fetches from it and this sets it, and the
// two have to be the same name or the stage fetches from a remote nobody
// configured.
const upstreamRemote = stages.UpstreamRemote

// copyStanding reports whether a usable copy is already at path.
//
// A path holding something that is not a worktree is refused rather than
// removed and rebuilt. This service put a directory there or nothing did, and
// removing whatever else is there would be this code guessing about a
// directory under the home it cannot account for.
func copyStanding(ctx context.Context, path string) (bool, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("service: reading the isolated copy at %s: %w", path, err)
	}
	if _, err := vcs.OpenWorktree(ctx, path, vcs.WithRedactor(redact.New())); err != nil {
		return false, fmt.Errorf("service: %s is where an isolated copy belongs and holds something "+
			"that is not one, so a run cannot be given a copy there: %w", path, err)
	}
	return true, nil
}

// reclaimCopy removes the isolated copy of a run that has ended.
//
// It reports nothing to the caller. The run has already settled by the time
// this is reached, so there is no answer a failure here could change, and a
// copy that outlives its run costs disk rather than correctness. What it does
// instead is say so in the log, because a copy left standing is a fact about
// the home that is otherwise only visible by looking.
//
// The refusal it expects is git's, on a copy holding modified or untracked
// files. That copy is kept on purpose; see this file's opening comment.
func (s *Service) reclaimCopy(ctx context.Context, record store.Run) {
	path := s.home.Worktree(record.RepositoryID, record.ID)
	if _, err := os.Stat(path); err == nil {
		copied, err := vcs.OpenWorktree(ctx, path, vcs.WithRedactor(redact.New()))
		if err != nil {
			s.log.Printf("the isolated copy of run %s at %s was not removed: %v", record.ID, path, err)
			return
		}
		if err := copied.RemoveWorktree(ctx, path); err != nil {
			// The copy is kept on purpose when git refuses it, so the
			// reference that keeps its commit reachable is kept with it: a
			// copy standing on an unreachable commit is one a person cannot
			// use, which is the whole reason it was not removed.
			s.log.Printf("the isolated copy of run %s is left standing at %s: %v", record.ID, path, err)
			return
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		s.log.Printf("the isolated copy of run %s at %s was not removed: %v", record.ID, path, err)
		return
	}
	repository, err := s.store.Repository(ctx, record.RepositoryID)
	if err != nil {
		s.log.Printf("the submitted reference of run %s was not removed: %v", record.ID, err)
		return
	}
	bare, err := s.gateRepositoryOf(ctx, repository, record.ID)
	if err != nil {
		s.log.Printf("the submitted reference of run %s was not removed: %v", record.ID, err)
		return
	}
	if err := bare.DeleteRef(ctx, submittedRef(record.ID)); err != nil {
		s.log.Printf("the submitted reference of run %s was not removed: %v", record.ID, err)
	}
	// The trusted-configuration reference is the run's too, written by
	// resolveRunConfig when the run began or resumed, and it goes with the
	// run on the same terms. A run that never resolved has none, and deleting
	// an absent reference is not an error internal/vcs reports.
	if err := bare.DeleteRef(ctx, trustedRef(record.ID)); err != nil {
		s.log.Printf("the trusted-configuration reference of run %s was not removed: %v", record.ID, err)
	}
}
