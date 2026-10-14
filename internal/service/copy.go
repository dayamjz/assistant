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
	"github.com/dayamjz/assistant/internal/tasks"
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
// standing on, which the gate repository has never seen. The commit used to be
// fetched from the working copy here, on every run, whichever way the run
// started. For a run a push started from a clone the working copy has never
// seen - a task's worker pushes from its own copy - that fetch asked the
// working copy for a commit it does not hold, and the run went on only
// because of a property of git this package does not own: a fetch whose
// wanted object the repository already holds writes the reference without
// transferring it, so the gate's own copy of the pushed object answered a
// fetch addressed at the working copy. The working copy was still contacted,
// and the run rested on that short-circuit and on the working copy standing
// where the record says.
//
// So the fetch moved to where a run is created, which is the one place that
// knows where the commit is. create writes the run's reference in the gate
// repository right after it writes the row, from the gate's own object for a
// push, which already landed the commit there, and by a fetch from the
// working copy for a start or a rerun. What this file keeps is the one path
// that always runs: every run's reference is written at creation, by one
// function, and this reads it from the gate and nowhere else, with no
// conditional on where the commit came from and nothing contacted. One source
// here is worth more than a conditional exercised on one of the ways a run
// starts, for the same reason the unconditional fetch was: a build where it
// is wrong is wrong for every run rather than for the half nobody tested.
//
// It is written by commit and not by branch. The branch tip and the commit the
// run records are the same thing for about as long as it takes to record it,
// and a reference taken by name would quietly validate whatever the branch had
// moved to instead. Nothing here reads the working copy at all, and the fetch
// create makes writes nothing in it.
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
// # Cutting and removing a copy take the gate's copies lock
//
// A copy's administrative entry is written inside the gate repository, and a
// push or a fetch into that repository reads every such entry, so one that met
// an entry part-written was refused by git (#144). The cut here and the
// removal in internal/tasks hold internal/gate's copies lock exclusively while
// git writes the entry, and the fetches this service makes into the gate,
// fetchIntoGate's two callers, hold it shared, as a push through the
// assistant remote does. internal/gate's copies.go owns the lock and names the
// writers into the gate that do not take it, the rebase stage's fetch in a
// run's copy among them.
//
// # Reclaim is a P12 decision, and the work it may not throw away
//
// A copy is removed when its run reaches a terminal status, and a held run
// keeps its copy, because a hold is answered later and the run resumes in the
// copy it stopped in. The move to that status has already swept the copy by
// then, on runsweep.go's terms, and nothing here reads what the sweep found.
//
// What removes it is internal/tasks' cleanup decision, the same one a task's
// copy goes through: PRD principle P12 covers every isolated copy, and a
// run's copy holds work, the fix rounds' commits, that a failed or terminated
// run may never have pushed. So reclaimCopy asks tasks.CleanUpRun, which
// proves the commit the copy stands on is reachable from the upstream's
// branch, or in the merged pull request the run's merge record names, or
// present in the default branch by content, and refuses otherwise; a passed
// run's pushed branch satisfies the first at once. The removal behind the
// proof is the non-discarding vcs.RemoveWorktree, so a modified or untracked
// file git finds there is a second refusal behind the first, and ignored
// files are the residual gap internal/vcs's Inventory names.
//
// A refusal is returned, not logged past. The run has settled by the time
// it is reached, so the callers that reach it, settle and the checks-hold
// closure, have no caller of their own to return it to and log it; what
// makes it more than a log line is that tasks.CleanUpRun records it on the
// run's cleanup record, store.RunCleanup, which report composes into the
// run's answer, so status can say a local copy was kept and why. cleanup.go
// owns that wording and the task-copy call beside it.
//
// The cost is that copies accumulate for runs whose work is not yet safely
// elsewhere. That is the intended trade and not an oversight: they are under
// the home, they are named by the run they belong to, and a person can
// remove one. Nothing here prunes them on a timer, because a timer would be
// the same decision taken later with less information.

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
//
// The commit is read from the gate repository, under the reference create
// wrote for the run, and the copy is refused rather than cut when that
// reference is absent or names another commit: a reference the row does not
// vouch for is not the commit the run was recorded to validate. The working
// copy is not read here; this file's opening comment says where the fetch
// went.
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
	held, err := bare.ResolveCommit(ctx, submittedRef(record.ID))
	if err != nil {
		return fmt.Errorf("service: the gate repository holds no %s for run %s, so the commit it validates "+
			"is not there to cut a copy from: %w", submittedRef(record.ID), record.ID, err)
	}
	if held != record.SubmittedHead {
		return fmt.Errorf("service: %s in the gate repository is %s and run %s was recorded to validate %s; "+
			"the copy is refused rather than cut at either", submittedRef(record.ID), held, record.ID, record.SubmittedHead)
	}
	// The copy is given no configuration write of its own: it is a linked
	// worktree of the gate, whose repository internal/gate gives automatic
	// maintenance off, and the copy test reads both keys back from inside a
	// run's copy, which is what that rests on.
	// The cut is made under the gate's copies lock, held exclusively, so a
	// push or a fetch into the gate that takes the lock waits for the cut
	// rather than meeting it half-written; internal/gate's copies.go says
	// who takes it and who does not.
	lock, err := gate.LockCopies(ctx, bare.Path())
	if err != nil {
		return fmt.Errorf("service: waiting to cut the isolated copy for run %s: %w", record.ID, err)
	}
	defer func() { _ = lock.Release() }()
	if _, err := bare.AddWorktree(ctx, vcs.WorktreeSpec{Path: path, Commit: held}); err != nil {
		return fmt.Errorf("service: creating the isolated copy for run %s at %s: %w", record.ID, path, err)
	}
	return nil
}

// fetchIntoGate fetches into the gate repository under its copies lock, held
// shared, so the fetch waits for a copy being cut or removed there rather than
// being refused by git for it. It waits no longer than a push does, and a
// fetch that waited that long is refused with internal/gate's
// ErrCopyInTransit, which the caller returns like any other failure to fetch.
func fetchIntoGate(ctx context.Context, bare *vcs.Repository, spec vcs.FetchSpec) error {
	lock, err := gate.AwaitCopies(ctx, bare.Path(), gate.ReceiveWait, nil)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()
	return bare.Fetch(ctx, spec)
}

// recordSubmittedCommit writes the run's reference in the gate repository at
// the commit the run was recorded to validate, right after create wrote the
// row, which is the order PRD section 8 gives a record and the thing it names.
//
// Where the commit comes from is the one conditional on how a run started,
// and it is here and nowhere else. A push landed the commit in the gate
// before the notification ran, so for a push the reference is written at the
// object already there and nothing is fetched; a start and a rerun record a
// commit the gate has never seen, so for those it is fetched by commit from
// the working copy the repository record names, which is the fetch ensureCopy
// used to make. Either way the reference is the same name at the same commit,
// so ensureCopy reads one thing. A fetch by commit from a working copy asks
// for a commit that working copy holds, which is what a start and a rerun
// record and what a worker's push does not; the push path is why this is not
// an unconditional fetch, and this file's opening comment says what that
// fetch used to rest on there.
func (s *Service) recordSubmittedCommit(ctx context.Context, record store.Run, heldByGate bool) error {
	repository, err := s.store.Repository(ctx, record.RepositoryID)
	if err != nil {
		return fmt.Errorf("service: reading the repository run %s is of: %w", record.ID, err)
	}
	bare, err := s.gateRepositoryOf(ctx, repository, record.ID)
	if err != nil {
		return err
	}
	if heldByGate {
		if err := bare.UpdateRef(ctx, submittedRef(record.ID), record.SubmittedHead); err != nil {
			return fmt.Errorf("service: recording the commit run %s validates in the gate repository: %w",
				record.ID, err)
		}
		return nil
	}
	if err := fetchIntoGate(ctx, bare, vcs.FetchSpec{
		Remote:   repository.WorkingPath,
		Refspecs: []string{"+" + record.SubmittedHead + ":" + submittedRef(record.ID)},
	}); err != nil {
		return fmt.Errorf("service: bringing the commit run %s validates into the gate repository: %w",
			record.ID, err)
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
// and it is what keeps the commit reachable between the write that records it
// at creation and the worktree that checks it out.
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

// reclaimCopy removes the isolated copy of a run that has ended, through
// internal/tasks' cleanup decision, and the two references the run holds in
// the gate repository with it.
//
// It returns what it could not do. A *tasks.CleanupRefusal is the copy kept,
// with the reason, recorded by the decision on the run's cleanup record
// before it is returned; any other error is a record or a repository that
// could not be read, and nothing was removed. The references are deleted
// only after the copy is: a copy standing on a refusal needs the reference
// that keeps its commit reachable, which is the whole reason it was kept.
func (s *Service) reclaimCopy(ctx context.Context, record store.Run) error {
	repository, err := s.store.Repository(ctx, record.RepositoryID)
	if err != nil {
		return fmt.Errorf("service: reading the repository run %s is of: %w", record.ID, err)
	}
	gatePath, err := s.gatePathOf(ctx, repository)
	if err != nil {
		return err
	}
	if _, err := s.cleaner.CleanUpRun(ctx, tasks.RunCopy{Run: record, Gate: gatePath}); err != nil {
		return err
	}
	bare, err := s.gateRepositoryOf(ctx, repository, record.ID)
	if err != nil {
		return err
	}
	if err := bare.DeleteRef(ctx, submittedRef(record.ID)); err != nil {
		return fmt.Errorf("service: the submitted reference of run %s was not removed: %w", record.ID, err)
	}
	// The trusted-configuration reference is the run's too, written by
	// resolveRunConfig when the run began or resumed, and it goes with the
	// run on the same terms. A run that never resolved has none, and deleting
	// an absent reference is not an error internal/vcs reports.
	if err := bare.DeleteRef(ctx, trustedRef(record.ID)); err != nil {
		return fmt.Errorf("service: the trusted-configuration reference of run %s was not removed: %w", record.ID, err)
	}
	return nil
}
