package service

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/runs"
	"github.com/dayamjz/assistant/internal/store"
)

// createCopy makes the isolated copy a run works in, as a linked worktree of
// the repository's gate checked out at the commit the push submitted.
//
// It is called after the run's row exists, which is PRD section 8's ordering
// rule: a directory with no row is always safe to remove, and the reverse -
// a row whose directory could not be made - is a run recorded and failed with
// the reason rather than a directory nobody can account for.
//
// A failure here fails the run, and that is the only honest answer. Every
// stage after the intent stage reads the change out of this copy, so a run
// without one cannot validate anything, and a run that carried on would report
// stage outcomes it did not establish.
func (s *Service) createCopy(ctx context.Context, record store.Run) error {
	spec, err := s.gateSpecFor(ctx, record.RepositoryID)
	if err != nil {
		return err
	}
	path := s.home.Worktree(record.RepositoryID, record.ID)
	if err := gate.AddCopy(ctx, spec, path, record.SubmittedHead, gate.WithIndex(s.store)); err != nil {
		return fmt.Errorf("service: creating the isolated copy for run %s: %w", record.ID, err)
	}
	return nil
}

// reclaimCopy gives back the isolated copy of a run that has finished, and
// reports rather than acts when something stands in the way.
//
// # Why both endings come through here
//
// PRD section 8 has a copy removed when its run ends, and a service meant to
// run for weeks that reclaimed only at startup would leak one directory per
// finished run. So a run ending reclaims, and so does recovery, for the copies
// a service that died left behind. Both call this, rather than each carrying
// its own sequence: the refusals below are the whole of what stands between a
// copy and its removal, and two copies of them would agree today and drift
// later, which is the defect internal/gate's own seam was built to answer.
//
// # Reporting is what handling this refusal means
//
// This takes no error back to its caller, which is a departure from the rule
// that a refusal is a typed result a caller must handle, and it is deliberate.
// What handling means here is that the copy stays and somebody is told: a
// refusal is not a failure of the run, it does not change the recorded
// verdict, and the copy it preserved is seen again by the next startup. Both
// call sites would otherwise write that same handling, and one of them would
// eventually not.
//
// What that costs is that a refusal is only as good as the surface carrying
// it, so the service log is where it goes and
// TestARefusedReclaimReachesTheServiceLog drives a real refusal and reads it
// back off disk. A refusal reported into something nobody reads is
// indistinguishable from no refusal at all.
//
// # The refusal this asks, and the ones internal/gate asks
//
// A copy belongs to its run for as long as any move leads out of the status it
// is in, so this refuses to give back the copy of a run that is not finished.
// runs.Finished answers that off the same table the moves are declared in,
// rather than from a second list of terminal statuses here. A run held for a
// person is not finished: PRD section 5 has it waiting on an answer, and its
// copy is what the answer resumes into.
//
// The other refusals are internal/gate's, because they are questions about the
// gate and the worktree rather than about the run: work no reference in the
// gate contains, a path that is not a copy at all, and git's own refusal of a
// worktree holding modified or untracked files. RemoveCopy's documentation
// states what each establishes, and states the reaping gap PRD section 11
// leaves open in this build.
// terminalMove is one of internal/runs' moves to a status no move leads out
// of. It is a type so endAndReclaim can take any of them and so a reader can
// see that the seam is about the class rather than about one of them.
type terminalMove func(context.Context, string) (store.Run, error)

// endAndReclaim moves a run to a terminal status and gives its isolated copy
// back, and it is the only way this package ends a run.
//
// Every path that ends one goes through here rather than calling a move and
// then remembering to reclaim: a segment settling, a caller cancelling, a push
// superseding the run it displaces, and a run whose copy could not be created.
// Adding two more calls to reclaimCopy would have been three sites agreeing by
// convention, and a fourth would have been written without it - which is how
// the two paths into a run came to disagree about the gate in the first place.
// This is the same answer as putting the gate-holds guard at create rather
// than at its callers: make the state unreachable rather than remembered.
//
// A move refused because the run had already reached some other status is not
// swallowed: the error is returned unchanged so the caller keeps whatever it
// makes of it, and the copy is reclaimed against where the run actually is
// rather than where this move wanted to put it. A run that ended by another
// route still has a copy to give back.
func (s *Service) endAndReclaim(ctx context.Context, runID string, move terminalMove) (store.Run, error) {
	record, err := move(ctx, runID)
	if err == nil {
		s.reclaimCopy(ctx, record)
		return record, nil
	}
	var wrong *store.RunStatusError
	if !errors.As(err, &wrong) {
		return store.Run{}, err
	}
	current, readErr := s.store.Run(ctx, runID)
	if readErr != nil {
		return store.Run{}, errors.Join(err, readErr)
	}
	s.reclaimCopy(ctx, current)
	return current, err
}

func (s *Service) reclaimCopy(ctx context.Context, record store.Run) {
	if !runs.Finished(record.Status) {
		s.log.Printf("the isolated copy for run %s was kept: the run is %s, which is not a status "+
			"the run is finished in", record.ID, record.Status)
		return
	}
	path := s.home.Worktree(record.RepositoryID, record.ID)
	// Recovery asks this of every finished run the store holds, and almost
	// none of them still have a directory. Resolving the gate is what costs -
	// it opens the working copy, reads its remote, settles ownership and
	// seals hooks - so a copy that is not there is answered before any of
	// that happens.
	//
	// This is an optimization and not the guard. gate.RemoveCopy asks the
	// same question again where it can act on the answer, and that one is
	// what decides; a reader must not take this as the thing keeping a copy
	// safe.
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return
	}
	spec, err := s.gateSpecFor(ctx, record.RepositoryID)
	if err != nil {
		s.log.Printf("the isolated copy for run %s was kept: %v", record.ID, err)
		return
	}
	if err := gate.RemoveCopy(ctx, spec, path, gate.WithIndex(s.store)); err != nil {
		s.log.Printf("the isolated copy for run %s was kept at %s: %v", record.ID, path, err)
		return
	}
}

// gateSpecFor names the gate of the repository a run belongs to.
//
// It reads the working copy off the repository's record and lets
// internal/gate resolve which gate that names, so this service holds no second
// answer to a question that package owns. No Command is supplied because
// neither operation here writes a hook; only initialization does.
func (s *Service) gateSpecFor(ctx context.Context, repositoryID string) (gate.Spec, error) {
	repository, err := s.store.Repository(ctx, repositoryID)
	if err != nil {
		return gate.Spec{}, fmt.Errorf("service: reading the repository of the run: %w", err)
	}
	return gate.Spec{Home: s.home.Root(), WorkingPath: repository.WorkingPath}, nil
}
