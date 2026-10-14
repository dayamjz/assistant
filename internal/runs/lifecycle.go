package runs

import (
	"context"
	"fmt"
	"slices"

	"github.com/dayamjz/assistant/internal/store"
)

// move is one legal change of a run's status: the statuses it may be made
// from, and the status it establishes. Every exported method below is one of
// these rows and makes its change no other way, so what this service can do to
// a run's status is the table rather than a rule spread over the calls that
// make them.
//
// A move is anchored: store.TransitionRun reads the status and writes the new
// one in one transaction, so two callers moving one run cannot both find it
// where they expected. That is what makes this a mechanism rather than a
// convention. A move to the status the run already holds writes nothing and is
// not an error, which is what makes each of these safe to repeat.
type move struct {
	from []store.RunStatus
	to   store.RunStatus
	// end is which state of the run's pull request this move records as what
	// ended the run, for the two moves a pull request's state makes. Such a
	// move writes PRD section 8's merge record with the status, in one
	// transaction, so a run never reads as ended by its pull request without
	// the status that goes with it or the reverse. The zero value records
	// none and is every other row.
	end store.PullRequestEnd
}

// The legal moves. A run is created pending, runs, and ends in exactly one of
// passed, failed, or terminated; held is where it waits on a person. No move
// names a terminal status as one it may be made from, which is what makes a
// finished run finished: its verdict is not something a later call revises.
var (
	moveStart     = move{from: []store.RunStatus{store.RunPending}, to: store.RunRunning}
	moveHold      = move{from: []store.RunStatus{store.RunRunning}, to: store.RunHeld}
	moveRelease   = move{from: []store.RunStatus{store.RunHeld}, to: store.RunRunning}
	movePass      = move{from: []store.RunStatus{store.RunRunning}, to: store.RunPassed}
	moveFail      = move{from: []store.RunStatus{store.RunRunning}, to: store.RunFailed}
	moveTerminate = move{
		from: []store.RunStatus{store.RunPending, store.RunRunning, store.RunHeld},
		to:   store.RunTerminated,
	}
	// The two moves a run's pull request makes, per PRD section 5's "Waiting
	// on checks". Each is legal from running, which is the checks stage's
	// wait reading the pull request, and from held, which is the hold that
	// wait leaves when checks_timeout elapses and the service re-reads the
	// pull request before resuming it. No other hold is an origin here in
	// any other sense: the row says what the record may move from, and PRD
	// section 11 says which hold the service may re-read, which is
	// internal/service's rule.
	movePassOnMerge = move{
		from: []store.RunStatus{store.RunRunning, store.RunHeld},
		to:   store.RunPassed,
		end:  store.PullRequestMerged,
	}
	moveFailOnClose = move{
		from: []store.RunStatus{store.RunRunning, store.RunHeld},
		to:   store.RunFailed,
		end:  store.PullRequestClosed,
	}

	// moves is every row above. It exists so that "no move leads out of a
	// terminal status" is read off the table rather than restated as a second
	// list that could disagree with it.
	moves = []move{moveStart, moveHold, moveRelease, movePass, moveFail, moveTerminate,
		movePassOnMerge, moveFailOnClose}
)

// Start records that a pending run has begun.
func (s *Service) Start(ctx context.Context, id string) (store.Run, error) {
	return s.apply(ctx, id, moveStart)
}

// Hold records that a running run is waiting on a person.
//
// It records where the run stands and nothing about what is being decided. The
// decision itself is a hold record, which store.RegisterHold owns and which
// PRD section 8 keys, makes idempotent to register, and closes only by an
// explicit resolution; a run's status cannot answer any of that.
func (s *Service) Hold(ctx context.Context, id string) (store.Run, error) {
	return s.apply(ctx, id, moveHold)
}

// Release records that a held run is running again.
//
// PRD section 8's verb list for this module calls this one "resolve". What is
// resolved is the hold the run was waiting on, which is the separate record
// Hold's comment names, and closing it is a separate act from the run leaving
// held. This is only the run leaving held, so it is named for that.
func (s *Service) Release(ctx context.Context, id string) (store.Run, error) {
	return s.apply(ctx, id, moveRelease)
}

// Pass records that a running run finished and the change was validated.
func (s *Service) Pass(ctx context.Context, id string) (store.Run, error) {
	return s.apply(ctx, id, movePass)
}

// Fail records that a running run finished and the change was not validated.
// It is a verdict on the change, not a failure of the service: a run that
// could not be driven at all has not reached one of these.
func (s *Service) Fail(ctx context.Context, id string) (store.Run, error) {
	return s.apply(ctx, id, moveFail)
}

// PassOnMerge records that a run ended because its pull request was read as
// merged, which PRD section 5 makes complete and not newly validated. The
// move writes the run's merge record from read, in the same transaction as
// the passed status, and machine.OutcomeOf reads the two together as the
// passed outcome rather than as checks-passed. It is legal from running and
// from held, for the reason the row gives. closing is the hold to close with
// the move, for the one closure PRD section 8 lets the program make, and the
// zero value closes nothing; read.End is what the row records and not what
// the caller says, so a read naming the other end is refused by the store.
func (s *Service) PassOnMerge(ctx context.Context, id string, read store.PullRequestRead, closing store.HoldClosure) (store.Run, error) {
	return s.applyOnPullRequest(ctx, id, movePassOnMerge, read, closing)
}

// RecordMergeAfterPass records that a run which had already passed, with no
// pull request read ending it, was then read as merged: the code-host read
// after a merge internal/service made under PRD section 6's autonomy flag.
// It is not a move: the run's status stays passed, which no row of this
// table leads out of, and the store writes the merge record beside it once,
// so machine.OutcomeOf reads the run as passed from then on. A run that is
// not passed and a run that already has a record are refused by the store.
func (s *Service) RecordMergeAfterPass(ctx context.Context, id string, read store.PullRequestRead) (store.MergeRecord, error) {
	return s.store.RecordMergeOfPassedRun(ctx, id, read)
}

// FailOnClose records that a run ended because its pull request was read as
// closed without being merged. The merge record carries store.PullRequestClosed
// beside the failed status, which is the reason PRD section 5 has recorded:
// "pull request closed without merging" is what that end renders as. It is a
// verdict on the change on the same terms as Fail, and legal from the same
// two statuses as PassOnMerge, with the same closure.
func (s *Service) FailOnClose(ctx context.Context, id string, read store.PullRequestRead, closing store.HoldClosure) (store.Run, error) {
	return s.applyOnPullRequest(ctx, id, moveFailOnClose, read, closing)
}

// Terminate records that a run ended without reaching a verdict, which is what
// a cancellation and a supersession leave behind. It is legal from every
// status a run can still be in and from none it cannot, so a run cancelled
// before it started ends the same way as one cancelled halfway through.
func (s *Service) Terminate(ctx context.Context, id string) (store.Run, error) {
	return s.apply(ctx, id, moveTerminate)
}

// apply makes one move and, when it ends the run, drops the run's fixer role
// and then calls OnFinish.
//
// Dropping it there rather than on a timer is why the two halves of this
// service are one service: a run that has ended is a run whose agent
// conversation has no next round, and the index that makes one run's fixer
// role one object is the thing that would otherwise hold it for the life of
// the process. A handle a caller already holds is not revoked; doc.go says
// what that leaves open.
//
// The move and the drop happen under the lock Fixer hands roles out under, so
// a role cannot be handed out for a run this service is in the middle of
// ending. The lock is held across the store call, which serializes this
// service's lifecycle moves against each other; rounds do not take it, so what
// that costs is bounded by how often a run changes status.
//
// OnFinish is called after that lock is given back and before apply returns,
// so a caller acting on the ending's return, internal/service reclaiming the
// run's copy among them, acts after it. What it is decided from is the row
// store.TransitionRun read back inside the transaction that wrote the move, and
// the decision is Finished over that row's status, so no second read stands
// between the ending and the call. Because no row of the table names a
// terminal status as one it may be made from, a run that row shows finished
// is finished for good through this package, so OnFinish is never called for
// a run that is still active or that a later move of this package could make
// active again. It is called for a move to the status the run already held as
// well, since that row reads finished just the same; a caller that must act
// once per ending has to make its act safe to repeat. It is not under the
// lock, so two endings of one run in flight at once may each call it, and
// nothing here orders those calls against each other or against a move of
// another run.
//
// What that is not is the reach of store.TransitionRun itself, which doc.go
// names: another package that calls it with an origin set of its own moves a
// run without this package, and OnFinish hears nothing of that move.
func (s *Service) apply(ctx context.Context, id string, m move) (store.Run, error) {
	r, err := s.locked(func() (store.Run, error) { return s.store.TransitionRun(ctx, id, m.from, m.to) })
	if err != nil {
		return store.Run{}, err
	}
	s.finish(ctx, r)
	return r, nil
}

// locked makes one store move under the lock and drops the run's fixer role
// when the row it returns is finished, on apply's terms.
func (s *Service) locked(transition func() (store.Run, error)) (store.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := transition()
	if err != nil {
		return store.Run{}, err
	}
	if Finished(r.Status) {
		delete(s.fixers, r.ID)
	}
	return r, nil
}

// finish calls OnFinish with r when r is finished, on apply's terms.
func (s *Service) finish(ctx context.Context, r store.Run) {
	if s.onFinish != nil && Finished(r.Status) {
		s.onFinish(ctx, r)
	}
}

// applyOnPullRequest is apply for the two rows a pull request's state makes:
// the move, the merge record, and any hold closure are one store transaction,
// under the same lock, with the same drop of the fixer role and the same call
// of OnFinish after it. The row's end is what is recorded; a read naming
// another end is refused here so the table and the record cannot disagree
// about which move was made.
func (s *Service) applyOnPullRequest(ctx context.Context, id string, m move, read store.PullRequestRead, closing store.HoldClosure) (store.Run, error) {
	if read.End != m.end {
		return store.Run{}, fmt.Errorf("runs: run %s: a move on a %s pull request was given a read of a %s one", id, m.end, read.End)
	}
	r, err := s.locked(func() (store.Run, error) {
		return s.store.TransitionRunOnPullRequest(ctx, id, m.from, m.to, read, closing)
	})
	if err != nil {
		return store.Run{}, err
	}
	s.finish(ctx, r)
	return r, nil
}

// Finished reports whether a run in this status is one no move leads out of.
//
// That is the three terminal statuses, and it is read off the table rather
// than listed again, so a status added later is covered by whichever rows are
// written for it. It is also every status this build does not define: a run
// this service cannot move is a run it cannot take further, so a word nobody
// recognizes answers here the way a finished run does rather than opening a
// path on the strength of not being understood.
//
// It is exported for internal/service, which reclaims a run's isolated copy
// when the run is over and must not reclaim one a hold will resume in. That
// caller asks here rather than naming the terminal statuses beside its own
// switch, because a second list of them is a second owner of which they are.
func Finished(status store.RunStatus) bool {
	for _, m := range moves {
		if slices.Contains(m.from, status) {
			return false
		}
	}
	return true
}
