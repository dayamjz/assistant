package runs

import (
	"context"
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
	// ended the run, for the two moves a pull request's state makes. It is
	// written with the status, in one transaction, so a run never reads as
	// ended by its pull request without the status that goes with it or the
	// reverse. The zero value records none and is every other row.
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
// merged, which PRD section 5 makes complete and not newly validated: the
// record carries store.PullRequestMerged beside the passed status, and
// machine.OutcomeOf reads the two together as the passed outcome rather than
// as checks-passed. It is legal from running and from held, for the reason
// the row gives.
func (s *Service) PassOnMerge(ctx context.Context, id string) (store.Run, error) {
	return s.apply(ctx, id, movePassOnMerge)
}

// FailOnClose records that a run ended because its pull request was read as
// closed without being merged. The record carries store.PullRequestClosed
// beside the failed status, which is the reason PRD section 5 has recorded:
// "pull request closed without merging" is what that end renders as. It is a
// verdict on the change on the same terms as Fail, and legal from the same
// two statuses as PassOnMerge.
func (s *Service) FailOnClose(ctx context.Context, id string) (store.Run, error) {
	return s.apply(ctx, id, moveFailOnClose)
}

// Terminate records that a run ended without reaching a verdict, which is what
// a cancellation and a supersession leave behind. It is legal from every
// status a run can still be in and from none it cannot, so a run cancelled
// before it started ends the same way as one cancelled halfway through.
func (s *Service) Terminate(ctx context.Context, id string) (store.Run, error) {
	return s.apply(ctx, id, moveTerminate)
}

// apply makes one move and, when it ends the run, drops the run's fixer role.
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
func (s *Service) apply(ctx context.Context, id string, m move) (store.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var r store.Run
	var err error
	if m.end != "" {
		r, err = s.store.TransitionRunOnPullRequest(ctx, id, m.from, m.to, m.end)
	} else {
		r, err = s.store.TransitionRun(ctx, id, m.from, m.to)
	}
	if err != nil {
		return store.Run{}, err
	}
	if Finished(r.Status) {
		delete(s.fixers, id)
	}
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
