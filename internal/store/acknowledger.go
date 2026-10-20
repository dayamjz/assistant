package store

import (
	"fmt"
	"strings"

	"github.com/dayamjz/assistant/internal/home"
)

// Acknowledger says who acknowledged a wake, which is the act that takes a
// row off the queue and, once internal/supervisor's in-flight table reads the
// queue, releases the stop guard for a task nothing else holds in flight.
// The same set says who a drain handed a row out to, which closes nothing. It
// is a closed set on Resolver's terms, and for Resolver's reason: a struct
// with unexported fields and no conversion into it, so outside this package
// the only values are the ones the functions below return and the zero, and
// nothing decoded from a wire, a file or an agent's output is one. Who
// acknowledged is decided in source at the surface recording it, never read
// out of a request, on the terms internal/ipc's Request.HoldResolver sets for
// holds: the coordinator's member is what that package produces, and the
// subordinate's is what internal/service substitutes when the kernel's
// credentials place the caller as a domain's subordinate coordinator.
//
// There is no value meaning a person acknowledged, and that is the design:
// no surface in this repository witnesses a person, and the queue is the
// coordinators', so a wake is acknowledged by a coordinator's turn or by the
// program and by nothing else.
//
// Nothing reads an Acknowledger to decide whether an acknowledgement may
// proceed. AcknowledgeWakes treats every member identically, and the only
// thing the value changes is what the row says afterwards.
//
// The zero Acknowledger names nobody, which is what a caller that has not
// thought about the question holds. AcknowledgeWakes refuses it with
// ErrNoAcknowledger rather than recording an acknowledgement with nobody
// attached to it.
type Acknowledger struct {
	name string
	// domain is set for the subordinate member alone: which domain's
	// subordinate coordinator acknowledged.
	domain string
}

// AcknowledgedByCoordinator is an acknowledgement made by the coordinator's
// turn, which PRD section 5 says drains the queue before doing anything else.
//
// Its one producer is internal/ipc's Request.WakeAcknowledger, which derives
// it from the surface the way Request.HoldResolver derives a resolver, and
// no frame carries it; the methods that record it, the drains wakes.list
// and wakes.wait as who a row was handed to and the acknowledgement
// wakes.acknowledge as who closed it, refuse a task's worker from the
// worker group set internal/session holds, so the value reaches a row only
// through a coordinator's call.
func AcknowledgedByCoordinator() Acknowledger { return Acknowledger{name: "coordinator"} }

// AcknowledgedByProgram is an acknowledgement the program made itself, for a
// row no coordinator will handle: a wake about a session the program's own
// relaunch replaced, which internal/service's relaunch closes so the queue
// does not hold a coordinator to work that has moved on. That relaunch is
// its one producer, and it acknowledges exactly the terminal-gone rows whose
// marker names the superseded session; every other row stays queued.
func AcknowledgedByProgram() Acknowledger { return Acknowledger{name: "program"} }

// AcknowledgedBySubordinate is an acknowledgement made by the subordinate
// coordinator of the named domain, PRD section 8's "the coordinator that
// drained a row acknowledges it". Its one producer is internal/service,
// which substitutes it for the coordinator's member when the kernel's
// credentials and the caller's parent chain place the caller as that
// domain's subordinate, never from anything the caller sends. A domain
// home.ValidDomain refuses is refused at the write with
// ErrUnknownAcknowledger, so no row records a domain that names nothing.
func AcknowledgedBySubordinate(domain string) Acknowledger {
	return Acknowledger{name: subordinateMember, domain: domain}
}

// Domain is the domain of the subordinate member, and empty for every other
// member, so a reader can say which subordinate a row was handed to without
// a text form of the member existing.
func (a Acknowledger) Domain() string { return a.domain }

// String renders who acknowledged, or "nobody" for the zero Acknowledger, so
// a formatted record cannot print an empty who as though it were a value.
func (a Acknowledger) String() string {
	switch {
	case a.name == "":
		return "nobody"
	case a.domain != "":
		return a.name + " of domain " + a.domain
	}
	return a.name
}

// subordinateMember is the name of the member a domain's subordinate
// coordinator records as, and the prefix its column text carries before the
// domain.
const subordinateMember = "subordinate"

// acknowledgerText returns what the column holds for a. The zero Acknowledger
// is ErrNoAcknowledger, and anything else outside the set is
// ErrUnknownAcknowledger, which is unreachable from outside this package for
// resolverText's reason except through a subordinate member naming a domain
// home.ValidDomain refuses, and kept for the same one. The column text is
// the member's name, or for the subordinate member the name, a colon and
// the domain, which acknowledgerValue reads back.
func acknowledgerText(a Acknowledger) (string, error) {
	if a == (Acknowledger{}) {
		return "", ErrNoAcknowledger
	}
	if a.name == subordinateMember {
		if err := home.ValidDomain(a.domain); err != nil {
			return "", fmt.Errorf("%w: %w", ErrUnknownAcknowledger, err)
		}
		return subordinateMember + ":" + a.domain, nil
	}
	for _, known := range []Acknowledger{AcknowledgedByCoordinator(), AcknowledgedByProgram()} {
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
	if domain, found := strings.CutPrefix(s, subordinateMember+":"); found {
		if err := home.ValidDomain(domain); err != nil {
			return Unknown[Acknowledger](), fmt.Errorf("store: column %s: %w: %w", column, ErrUnknownAcknowledger, err)
		}
		return Known(AcknowledgedBySubordinate(domain)), nil
	}
	for _, known := range []Acknowledger{AcknowledgedByCoordinator(), AcknowledgedByProgram()} {
		if s == known.name {
			return Known(known), nil
		}
	}
	return Unknown[Acknowledger](), fmt.Errorf("store: column %s: %w: %q", column, ErrUnknownAcknowledger, s)
}
