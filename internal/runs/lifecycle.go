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

	// moves is every row above. It exists so that "no move leads out of a
	// terminal status" is read off the table rather than restated as a second
	// list that could disagree with it.
	moves = []move{moveStart, moveHold, moveRelease, movePass, moveFail, moveTerminate}
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
	r, err := s.store.TransitionRun(ctx, id, m.from, m.to)
	if err != nil {
		return store.Run{}, err
	}
	if finished(r.Status) {
		delete(s.fixers, id)
	}
	return r, nil
}

// Finished reports whether a run in this status is one no move leads out of,
// so a caller outside this package can ask the same question the table answers
// rather than keeping a second list of terminal statuses that has to be
// remembered when a status is added.
//
// internal/service asks it to decide whether a run's isolated copy may be
// given back: a copy belongs to its run for as long as any move leads out of
// the status it is in, which includes a run held for a person.
func Finished(status store.RunStatus) bool { return finished(status) }

// finished reports whether a run in this status is one no move leads out of.
//
// That is the three terminal statuses, and it is read off the table rather
// than listed again, so a status added later is covered by whichever rows are
// written for it. It is also every status this build does not define: a run
// this service cannot move is a run it cannot take further, so a word nobody
// recognizes answers here the way a finished run does rather than opening a
// path on the strength of not being understood.
func finished(status store.RunStatus) bool {
	for _, m := range moves {
		if slices.Contains(m.from, status) {
			return false
		}
	}
	return true
}
