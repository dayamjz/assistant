package service

import (
	"context"
	"errors"
	"os"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/runs"
	"github.com/dayamjz/assistant/internal/store"
)

// The sweep of a finished run's isolated copy, PRD section 8's "once a run
// reaches a final state, its isolated copy is swept. The sweep must never
// touch a copy whose run is still active."
//
// # Where it is decided
//
// sweepFinished is internal/runs' OnFinish, which driverFor wires, so it is
// called from inside the call that moved the run, with the row
// store.TransitionRun read back in the transaction that wrote the move, and
// only when runs.Finished says that row's status is one no move leads out of.
// Every site that ends a run in this service goes through that call: settle
// (a segment that completed, stopped or was cancelled, and a run recovery
// reconciles), cancel, a push that supersedes a run, a create whose commit
// could not be recorded, and the checks-hold closure. None of them decides
// whether to sweep, and none can skip it.
//
// The second caller is recovery. A service that stopped between an ending's
// commit and that ending's sweep leaves a finished run whose copy nothing
// swept, so recover asks sweepFinishedAtRecovery, once the runs are
// reconciled and their recorded groups reaped and before the fleet pass can
// launch anything, of every run whose record recovery read as finished and
// whose copy still stands. Finished is read off that record through
// runs.Finished, on the same refusal of an unrecognized status, and never off
// the copy; a run recovery read as unfinished is passed over whatever it
// became since, because the move that finished it during recovery swept it
// then. A run's copy is its own path, which no other run's copy shares, and
// recovery continues only a run it reconciled to running, so what recovery
// itself starts is never started in a copy it sweeps.
//
// What holds is the guarantee internal/runs' apply states: the decision is
// read off the record the ending wrote and not off a second read, and since no
// row of internal/runs' table leads out of a terminal status, a run that
// record shows finished is never active again through that package. The copy
// swept is home.Worktree of the run's own repository and identifier, which no
// other run's copy shares. So the sweep cannot be pointed at a copy whose run
// is active, as long as every move is made through internal/runs, which is
// true of every move this build makes and is not enforced:
// store.TransitionRun is exported, internal/runs' doc.go says so, and a move
// made around that package is one nothing here hears of. sweepFinished also
// refuses a row whose status runs.Finished calls finished only because this
// build does not recognize it, on reap.go's terms: reading an unknown status
// as finished keeps a run from going further, and here the same reading
// would end processes, which is the other direction.
//
// # The order
//
// The call runs after the move has committed and before the move's caller
// gets the row back, so the ending never waits on what the sweep finds
// and whatever the caller does with the copy next comes after it: settle and
// the checks-hold closure reclaim the copy through reclaimCopy only once the
// sweep has returned. Inside the call, the run's agent process groups are
// ended first and the copy is swept second. The groups are the ones the run
// process record holds for the run: what containRunner registered and whose
// termination the adapter has not finished, and what an earlier service of
// this home recorded and did not live to end. Each is ended only through
// agents.EndGroup, which signals a group only when its leader still carries
// the identity recorded at launch. A segment that completed has none of its
// own left, because the adapter terminates an invocation's tree, on
// internal/agents' terms per platform, before the invocation returns; a run
// ended while its segment is inside a stage, by cancel or by a push, can
// have one, because ending it signals the segment and does not join it. The
// sweep then ends what the groups did not reach: a process whose working
// directory is in the copy and which is in none of them, a descendant that
// left its process group among them.
//
// A segment that was inside a stage when its run was ended and that then
// returns a result settles the run again, which is a move to the status the
// run already holds and reads finished just the same, so the copy is swept a
// second time, after that segment's invocations have returned and before the
// reclaim settle makes. A segment that returns an error settles nothing and
// is followed by no second sweep. Both sweeps are of a finished run, so the
// repeat is safe, and nothing here counts on it.
//
// # What a sweep comes to, and where that is said
//
// Nothing about the run's status. The ending has committed, and the record
// says what the run decided; whether something it started still runs is a
// separate fact, and holding the ending on it would be a run nobody can
// answer. So every reading goes to the service log, in sweep.go's words
// where they apply, and to the run's sweep record, store.RunCopySweep, with
// the occasion that asked it, and none refuses anything. The record is the
// latest sweep, one row per run that every sweep rewrites, and report
// composes it into the run's status answer as machine.Run.CopySweep:
//
//   - store.SweepCleared: what was ended, if anything, and what was not
//     looked at under agents.Sweeper's carve-outs.
//   - store.SweepNothingStanding: not a copy, with nothing standing at the
//     path. The run never had a copy or it is already gone, and there is
//     nothing to sweep.
//   - store.SweepUnsupported: agents.ErrSweepUnsupported, which is every
//     platform but Linux and macOS. Nothing was looked for, and a descendant
//     that left its group may still be running in the copy.
//   - store.SweepFailed: the listing failed, including agents.ErrSweepTimedOut
//     for a listing program that ran past its deadline, or the copy path
//     stands as something that is not the copy.
//   - store.SweepNotCleared: the sweep left survivors, unverified or
//     unreadable processes, named as agents.SweepResult.String names them in
//     the record's detail and counted in its Remaining.
//
// A record that cannot be written is said in the log and refuses nothing
// either. The record says what a sweep found when it ran, never whether
// anything runs in the copy now, on store.RunCopySweep's terms.
//
// The copy's files are never touched here. Whether it is removed is still
// internal/tasks' P12 decision, which reclaimCopy asks after this returns and
// which does not read the sweep's answer: a copy whose sweep did not clear is
// removed on the same proof as one whose did, behind the same non-discarding
// removal, and a process still writing there after the proof is what that
// package's inventory re-read is the only defence against. That the reclaim
// does not wait on a sweep that did not clear is the owner's decision, and the one
// that keeps a platform with no sweep from keeping every run's copy.
//
// # Residual gaps
//
//   - The sweep is a pass over a moment, as agents.Sweeper says: a process
//     started in the copy after its listing is not found. A segment ended
//     while inside a stage body is not joined, and a body between
//     cancellation points can still start one; the second sweep above narrows
//     that for a segment that returns a result and settles, and closes
//     nothing for one that returns an error.
//   - A service that stops between the ending's commit and the sweep leaves
//     the copy unswept until the next service opens on the home, whose
//     recovery sweeps it; a process there runs until then. One whose reclaim
//     already removed the copy is not looked for, because the sweep's scope
//     is the copy and nothing stands there.
//   - Recovery sweeps every finished run's standing copy at every open,
//     including copies a sweep already cleared, so opening costs one read of
//     the process table per copy that cleanup kept, plus the waits below for
//     each copy where something is found.
//   - Two sweeps of one run that overlap, which the second sweep above can
//     produce, record whichever wrote last, so the record may describe the
//     earlier of the two.
//   - Every gap agents.Sweeper names: a process that left the copy before the
//     listing, the carve-outs, and identifier reuse.
//   - The sweep runs inside the call that ended the run, so its cost is that
//     call's: a read of the process table, on macOS one lsof run bounded by
//     its own deadline whatever the context here carries, and when
//     something is found, waits of up to reapGrace after each of the two
//     signals, on top of the same for every group ended first. A push that supersedes a run pays it under
//     the branch's exclusion.

// sweepFinished ends what still runs in a finished run's isolated copy, on
// this file's terms, and records what the sweep came to on the run, with
// occasion naming the path that asked it. It is internal/runs' OnFinish with
// store.SweptAtFinish, and recovery's with store.SweptAtRecovery.
func (s *Service) sweepFinished(ctx context.Context, run store.Run, occasion store.SweepOccasion) {
	// The ending has committed, so a caller that gives up now cannot take it
	// back; the sweep's waits are bounded by reapGrace and its listing by
	// the deadline agents gives the listing's program, not by that caller.
	ctx = context.WithoutCancel(ctx)
	if !run.Status.Recognized() || !runs.Finished(run.Status) {
		s.log.Printf("the isolated copy of run %s is not swept: the run is %s, which is not a status this build reads as final", run.ID, run.Status)
		return
	}
	s.endRunGroups(ctx, run.ID)
	dir := s.home.Worktree(run.RepositoryID, run.ID)
	sweep := s.sweepCopy
	if sweep == nil {
		sweep = agents.NewSweeper(s.home, reapGrace, nil).Sweep
	}
	r, err := sweep(ctx, dir)
	record := store.RunCopySweep{Run: run.ID, Occasion: occasion}
	switch {
	case errors.Is(err, home.ErrNotACopy):
		if _, statErr := os.Lstat(dir); errors.Is(statErr, os.ErrNotExist) {
			record.Outcome = store.SweepNothingStanding
			s.log.Printf("run %s is %s and has no isolated copy standing, so there is nothing to sweep", run.ID, run.Status)
			break
		}
		record.Outcome, record.Detail = store.SweepFailed, err.Error()
		s.log.Printf("run %s is %s and its isolated copy was not swept, because the path does not stand as the copy itself: %v", run.ID, run.Status, err)
	case errors.Is(err, agents.ErrSweepUnsupported):
		record.Outcome, record.Detail = store.SweepUnsupported, err.Error()
		s.log.Printf("run %s is %s and this platform has no process sweep, so nothing still running in its isolated copy was "+
			"looked for or ended; a descendant that left its process group may still be running there", run.ID, run.Status)
	case err != nil:
		record.Outcome, record.Detail = store.SweepFailed, err.Error()
		s.log.Printf("run %s is %s and its isolated copy could not be swept: %v", run.ID, run.Status, err)
	case !r.Clear():
		record.Outcome, record.Detail = store.SweepNotCleared, r.String()
		record.Ended = store.Known(len(r.Ended))
		record.Remaining = store.Known(len(r.Survived) + len(r.Unverified) + len(r.UnreadableBlocking))
		s.log.Printf("run %s is %s and the sweep of its isolated copy did not clear it: %s", run.ID, run.Status, r)
	default:
		record.Outcome, record.Detail = store.SweepCleared, r.String()+unreadable(r)
		record.Ended, record.Remaining = store.Known(len(r.Ended)), store.Known(0)
		s.log.Printf("run %s is %s and its isolated copy was swept: %s%s", run.ID, run.Status, r, unreadable(r))
	}
	if _, err := s.store.RecordRunCopySweep(ctx, record); err != nil {
		s.log.Printf("what the sweep of run %s's isolated copy came to (%s) could not be recorded on the run: %v", run.ID, record.Outcome, err)
	}
}

// sweepFinishedAtRecovery asks sweepFinished, as store.SweptAtRecovery, of
// every run among records that the record shows finished and whose isolated
// copy still stands, on this file's terms. records are the runs recovery
// read before it reconciled any; a run among them that was unfinished is
// passed over here whatever it became since, because the move that finished
// it during recovery swept it then.
func (s *Service) sweepFinishedAtRecovery(ctx context.Context, records []store.Run) {
	for _, record := range records {
		if !record.Status.Recognized() || !runs.Finished(record.Status) {
			continue
		}
		dir := s.home.Worktree(record.RepositoryID, record.ID)
		if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
			continue
		}
		s.sweepFinished(ctx, record, store.SweptAtRecovery)
	}
}

// endRunGroups ends every agent process group the run process record holds
// for runID, through agents.EndGroup, and logs what each came to. It clears
// no record: the adapter clears the one it is still terminating once that
// termination finishes, and recovery clears what an earlier service left,
// each on reap.go's terms, and both find a group ended here gone.
func (s *Service) endRunGroups(ctx context.Context, runID string) {
	records, err := s.store.ProcessRecords(ctx)
	if err != nil {
		s.log.Printf("the agent process groups of run %s could not be read, so none is ended before its copy is swept: %v", runID, err)
		return
	}
	for _, record := range records {
		if record.RunID != runID {
			continue
		}
		start, known := record.Identity.Get()
		if !known {
			s.log.Printf("process group %d of run %s carries no identity to verify it by, so it is not ended before the copy is swept", record.Group, runID)
			continue
		}
		end, err := agents.EndGroup(agents.Identity{PID: record.Group, Start: start}, reapGrace)
		if err != nil {
			s.log.Printf("process group %d of run %s was not ended before the copy is swept: %v", record.Group, runID, err)
			continue
		}
		s.log.Printf("process group %d of run %s, which has finished: %s", record.Group, runID, end)
	}
}
