package feed

import (
	"context"
	"time"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// ObserveFiles reads the three inputs that live outside the database: the
// instance file, the beacon and the lock probe. Each input that could not be
// read is left at its zero value, which supervisor.Assess blocks on, so the
// result is always an observation and never an error; what went wrong is
// returned beside it, every failure joined, for a caller that reports. The
// in-flight reading is left as InFlightUnknown, which a caller fills in from
// Observe's read of the database or from a reading of its own.
func ObserveFiles(ctx context.Context, h *home.Home) (supervisor.Observation, error) {
	var obs supervisor.Observation
	var errs []error
	instance, err := h.ReadInstance()
	if err != nil {
		errs = append(errs, err)
	} else {
		obs.Instance = instance
	}
	beacon, err := ReadBeacon(h.Beacon())
	if err != nil {
		errs = append(errs, err)
	} else {
		obs.Beacon = beacon
	}
	lock, err := h.LockState(ctx)
	if err != nil {
		errs = append(errs, err)
	}
	obs.Lock = lock
	return obs, join(errs)
}

// Observe is ObserveFiles with the in-flight reading taken from the home's
// database through store.OpenReadOnly. A database that cannot be opened that
// way leaves the reading InFlightUnknown, so a hook that cannot read the
// record blocks rather than reading absence as nothing in flight.
func Observe(ctx context.Context, h *home.Home) (supervisor.Observation, error) {
	obs, err := ObserveFiles(ctx, h)
	errs := []error{err}
	s, openErr := store.OpenReadOnly(ctx, h.Database())
	if openErr != nil {
		return obs, join(append(errs, openErr))
	}
	defer func() { _ = s.Close() }()
	inFlight, flightErr := InFlightFrom(ctx, s, taskstate.NewReader(s, nil, nil))
	obs.InFlight = inFlight
	return obs, join(append(errs, flightErr))
}

// InFlightFrom is the in-flight reading over a store: every task's resolved
// state through reader, whether each has an open stall hold, read off the
// snapshot the state was resolved from by supervisor.StallHeld, and the
// queued wakes through s, handed to supervisor.InFlightOf. Either read failing is InFlightUnknown with the
// error. The two reads are not one transaction, so a wake acknowledged
// between them can be counted and a task created between them missed; both
// errors are toward in flight, which is the safe direction, and a caller that
// needs the two as of one instant has no accessor here for it.
//
// The reader is the caller's so that a service can supply the one over its
// own store, with its own advancing answer, while a hook supplies one with
// none; see the package comment for what the difference reads as.
func InFlightFrom(ctx context.Context, s *store.Store, reader *taskstate.Reader) (supervisor.InFlight, error) {
	resolved, err := reader.Observe(ctx)
	if err != nil {
		return supervisor.InFlightUnknown, err
	}
	unacknowledged, err := s.UnacknowledgedWakes(ctx)
	if err != nil {
		return supervisor.InFlightUnknown, err
	}
	states := make(map[string]taskstate.State, len(resolved))
	stallHeld := make(map[string]bool, len(resolved))
	for _, o := range resolved {
		states[o.Snapshot.Task.ID] = o.State.State
		stallHeld[o.Snapshot.Task.ID] = supervisor.StallHeld(o)
	}
	domainWakes, err := s.UnacknowledgedDomainWakes(ctx)
	if err != nil {
		return supervisor.InFlightUnknown, err
	}
	keptWorkWakes, err := s.UnacknowledgedKeptWorkWakes(ctx)
	if err != nil {
		return supervisor.InFlightUnknown, err
	}
	return supervisor.InFlightOf(states, unacknowledged, stallHeld, domainWakes+keptWorkWakes), nil
}

// Guard is the pull guard over a home: Observe, then supervisor.GuardOf at
// now under fresh. It is what a command computes with the service down and
// what the service computes for its own answer, with the service passing
// its own in-flight reading through GuardWith instead.
func Guard(ctx context.Context, h *home.Home, fresh supervisor.Freshness, now time.Time) supervisor.StopVerdict {
	obs, _ := Observe(ctx, h)
	return supervisor.GuardOf(obs, fresh, now)
}

// GuardWith is Guard with the in-flight reading supplied by the caller, for a
// service that resolves its own fleet. The files and the lock are still read
// here, so the two callers differ in one input and not in the predicate.
func GuardWith(ctx context.Context, h *home.Home, inFlight supervisor.InFlight, fresh supervisor.Freshness, now time.Time) supervisor.StopVerdict {
	obs, _ := ObserveFiles(ctx, h)
	obs.InFlight = inFlight
	return supervisor.GuardOf(obs, fresh, now)
}
