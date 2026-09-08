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

// terminalMove is one of internal/runs' moves to a status no move leads out
// of. It is a type so endAndReclaim can take any of them and so a reader can
// see that the seam is about the class rather than about one of them.
type terminalMove func(context.Context, string) (store.Run, error)

// endAndReclaim moves a run to a terminal status and arranges its isolated
// copy given back, and it is the one place this package ends a run.
//
// Every path that ends one goes through here rather than calling a move and
// then remembering to reclaim: a segment settling, a caller cancelling, a push
// superseding the run it displaces, and a run whose copy could not be created.
// Four sites calling reclaimCopy would have been four agreeing by convention,
// and a fifth would have been written without it - which is how the two paths
// into a run came to disagree about the gate in the first place.
//
// The move is now and the reclaim is not always. The status is what a person
// who cancelled is watching, so it is durable before this returns, while a
// segment may still be executing a stage body in the copy - a cancel and a
// supersession both signal a segment without waiting for it. When the copy
// comes back is therefore a decision of its own, and reclaimWhenEnded is its
// one owner.
//
// What that is worth is bounded, and the bound is stated because the guard it
// resembles is stronger. gateHoldsHead sits inside create, and create is the
// only function that writes a run row, so no path that records a run can skip
// it; that one is structural. This is not. Every terminal move stays callable
// beside this - built.runs.Terminate, Pass and Fail are all reachable, and
// driverFor hands the whole run service to any code added later - so a fifth
// ending that never gives a copy back is as writable as it ever was, and what
// keeps it from being written is review rather than construction. Making it
// structural means the terminal moves being reachable only through this seam:
// a wrapper this package holds that exposes the endings and not the moves, so
// a caller has nothing else to call.
//
// A move refused because the run had already reached some other status is not
// swallowed: the error is returned unchanged so the caller keeps whatever it
// makes of it, and the copy is reclaimed against where the run actually is
// rather than where this move wanted to put it. A run that ended by another
// route still has a copy to give back.
func (s *Service) endAndReclaim(ctx context.Context, runID string, move terminalMove) (store.Run, error) {
	record, err := move(ctx, runID)
	if err == nil {
		s.reclaimWhenEnded(ctx, record)
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
	s.reclaimWhenEnded(ctx, current)
	return current, err
}

// reclaimWhenEnded gives a run's copy back no earlier than the moment no
// segment may still be executing in it. It is called only from endAndReclaim,
// after the move it answers for is durable, so the record it reads and the
// mark it leaves both stand over a run whose status is already settled.
//
// # Why the reclaim does not simply ride the move
//
// A run's record can reach a terminal status while a segment is still inside
// a stage body: cancel signals the segment and moves the record without
// waiting for it, which is a promise the status path makes to a person, and a
// push superseding the branch's run moves the displaced record the same way.
// Reclaiming at the move would remove the directory from under whatever that
// segment is doing in it - PRD section 11's reaping hazard arriving through
// the filesystem rather than through processes, against the stage bodies that
// read and write the copy. So the status moves at the move, and the copy
// waits for the segment.
//
// # The segment's end is observed rather than awaited
//
// The run's slot is the one owner of whether a segment is executing, and this
// decision is made under the mutex the slot lives under. When no slot stands,
// or the slot stands for an ending rather than a segment, nothing is
// executing in the copy and it is given back here and now - under the
// ending's own placeholder where one stands, so no segment can begin while
// the removal runs. When a segment holds the slot, the slot is marked
// instead: the segment's release reports the mark and carryOn gives the copy
// back, so the reclaim rides the segment's own departure, and nothing new
// waits and nothing new polls. A slot already marked is left alone, because
// some earlier ending owns the reclaim and a second would race it over one
// directory.
//
// That mark is how the ordinary completion travels too, not only a cancel: a
// segment settling its own result still holds its own slot, so its reclaim
// rides its release a moment later, on the same goroutine, before the call
// that advanced the run returns.
//
// # The failure direction is the leak
//
// A service that dies after the move and before the segment's end leaves the
// copy standing, as does a segment that never returns. That is deliberate: a
// copy outliving its cancelled run is a directory, and a directory removed
// under an executing body is lost work. The leak is collected - the next
// service open reclaims the copies of finished runs, and one whose service
// stays up while its segment never returns is the stranded-copy work
// internal/gate's TakeBranch documentation names - so failing toward it costs
// a directory for a while rather than work.
//
// # The residual gap
//
// The decision and the removal are two steps, so a claim landing between them
// can put a segment into a copy as it is removed. Such a segment is one
// endRun's own documentation already describes - it read a record that
// predates the ending - and what bounds the loss is internal/gate's refusals:
// git refuses to remove a worktree holding modified or untracked files, and
// the submitted commit stays anchored in the gate either way.
func (s *Service) reclaimWhenEnded(ctx context.Context, record store.Run) {
	s.mu.Lock()
	held, taken := s.advancing[record.ID]
	if taken && held.owesReclaim {
		s.mu.Unlock()
		return
	}
	if taken {
		held.owesReclaim = true
		if held.cancel != nil {
			s.mu.Unlock()
			return
		}
	}
	s.mu.Unlock()
	s.reclaimCopy(ctx, record)
}

// reclaimOwed gives back the copy a run's ending left riding a segment, at the
// moment reclaimWhenEnded deferred it to: the segment has released the run's
// slot, so nothing is executing in the copy any more. The record is read
// fresh, and it is already terminal, because the mark this answers is set only
// after the terminal move committed.
//
// The context is the service's own rather than a caller's, because the caller
// the segment answered may be the very one whose cancel or departure ended it.
// A reclaim cut short by the service stopping leaves the copy for the next
// open's recovery, which is the leak direction reclaimWhenEnded chooses.
func (s *Service) reclaimOwed(runID string) {
	record, err := s.store.Run(s.stopCtx, runID)
	if err != nil {
		s.log.Printf("the isolated copy for run %s was kept: %v", runID, err)
		return
	}
	s.reclaimCopy(s.stopCtx, record)
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
