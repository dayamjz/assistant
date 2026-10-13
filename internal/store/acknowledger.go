package store

import "fmt"

// Acknowledger says who acknowledged a wake, which is the act that takes a
// row off the queue and, once internal/supervisor's in-flight table reads the
// queue, releases the stop guard for a task nothing else holds in flight. It
// is a closed set on Resolver's terms, and for Resolver's reason: a struct
// with one unexported field and no conversion into it, so outside this
// package the only values are the two the functions below return and the
// zero, and nothing decoded from a wire, a file or an agent's output is one.
// Who acknowledged is decided in source at the surface recording it, never
// read out of a request, on the terms internal/ipc's Request.HoldResolver
// sets for holds.
//
// There is no value meaning a person acknowledged, and that is the design:
// no surface in this repository witnesses a person, and the queue is the
// coordinator's, so a wake is acknowledged by the coordinator's turn or by
// the program and by nothing else.
//
// Nothing reads an Acknowledger to decide whether an acknowledgement may
// proceed. AcknowledgeWakes treats every member identically, and the only
// thing the value changes is what the row says afterwards.
//
// The zero Acknowledger names nobody, which is what a caller that has not
// thought about the question holds. AcknowledgeWakes refuses it with
// ErrNoAcknowledger rather than recording an acknowledgement with nobody
// attached to it.
type Acknowledger struct{ name string }

// AcknowledgedByCoordinator is an acknowledgement made by the coordinator's
// turn, which PRD section 5 says drains the queue before doing anything else.
//
// Its one producer is internal/ipc's Request.WakeAcknowledger, which derives
// it from the surface the way Request.HoldResolver derives a resolver, and
// no frame carries it; the method that records it, wakes.acknowledge,
// refuses a task's worker from the worker group set internal/session holds,
// so the value reaches a row only through a coordinator's call.
func AcknowledgedByCoordinator() Acknowledger { return Acknowledger{name: "coordinator"} }

// AcknowledgedByProgram is an acknowledgement the program made itself, for a
// row no coordinator will handle: a wake about a session the program's own
// relaunch replaced, which internal/service's relaunch closes so the queue
// does not hold a coordinator to work that has moved on. That relaunch is
// its one producer, and it acknowledges exactly the terminal-gone rows whose
// marker names the superseded session; every other row stays queued.
func AcknowledgedByProgram() Acknowledger { return Acknowledger{name: "program"} }

// String renders who acknowledged, or "nobody" for the zero Acknowledger, so
// a formatted record cannot print an empty who as though it were a value.
func (a Acknowledger) String() string {
	if a.name == "" {
		return "nobody"
	}
	return a.name
}

// acknowledgers is the closed set, and the only place it is written down. The
// write encodes through it and the read decodes through it, so the column
// cannot come to hold a value a read would then refuse.
func acknowledgers() []Acknowledger {
	return []Acknowledger{AcknowledgedByCoordinator(), AcknowledgedByProgram()}
}

// acknowledgerText returns what the column holds for a. The zero Acknowledger
// is ErrNoAcknowledger, and anything else outside the set is
// ErrUnknownAcknowledger, which is unreachable from outside this package for
// resolverText's reason and kept for the same one.
func acknowledgerText(a Acknowledger) (string, error) {
	if a == (Acknowledger{}) {
		return "", ErrNoAcknowledger
	}
	for _, known := range acknowledgers() {
		if a == known {
			return a.name, nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrUnknownAcknowledger, a.name)
}

// acknowledgerValue turns a stored column back into an Acknowledger. It is
// the one conversion from text, it is unexported, and it is reachable only
// from a read of this package's own column, on resolverValue's terms: a
// column that is absent is unknown, and one present and outside the set is an
// error rather than a fabricated "nobody was recorded".
func acknowledgerValue(o Optional[string], column string) (Optional[Acknowledger], error) {
	s, ok := o.Get()
	if !ok {
		return Unknown[Acknowledger](), nil
	}
	for _, known := range acknowledgers() {
		if s == known.name {
			return Known(known), nil
		}
	}
	return Unknown[Acknowledger](), fmt.Errorf("store: column %s: %w: %q", column, ErrUnknownAcknowledger, s)
}
