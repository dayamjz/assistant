package agents

import (
	"errors"
	"fmt"
)

// ErrIdentityUnreadable is Identify's answer on a platform none of the
// identity files builds for: no start time can be read there, so no process
// can be told from a later one given the same identifier.
var ErrIdentityUnreadable = errors.New("agents: this platform has no process start-time read")

// Identity is one process named well enough to tell it from a later process
// given the same identifier: the identifier and the platform's reading of
// the process's start time. It is the PID-reuse check every record of a
// process this package started is verified against, and this package is its
// one owner: internal/session embeds one in a terminal session's reference,
// and the run process record internal/store keeps carries one.
//
// Start is compared for equality only and carries no other meaning. It is
// read per platform: Linux from the process's stat entry in clock ticks
// since boot, macOS from the kernel's process table in microseconds since
// the epoch, Windows from the process's creation time in 100-nanosecond
// intervals since the Windows epoch. A process that exits and whose
// identifier is reused by another started at the same tick, microsecond or
// interval reads as the same process; the start time narrows reuse to that
// coincidence and does not close it.
type Identity struct {
	// PID is the process identifier.
	PID int
	// Start is the platform's reading of the process's start time.
	Start uint64
}

// String renders the identity for a log line.
func (id Identity) String() string {
	return fmt.Sprintf("process %d started at %d", id.PID, id.Start)
}

// Identify reads the identity of the process pid has now. found is false
// when no running process has the identifier, which is the one answer that
// proves absence; a process that has exited and not yet been reaped counts
// as not running where the platform lets that be seen. err is every failure
// to read, and ErrIdentityUnreadable where this platform can read nothing;
// neither says anything about whether the process is there.
func Identify(pid int) (id Identity, found bool, err error) {
	start, found, err := startTime(pid)
	if err != nil || !found {
		return Identity{}, false, err
	}
	return Identity{PID: pid, Start: start}, true, nil
}

// Presence is what Identity.Presence found when it looked for the process
// an identity names. It is a closed set, and no value in it is an error: a
// read that failed is an error beside the zero Presence, never a member.
type Presence string

const (
	// PresencePresent is a running process with the identity's identifier
	// and start time, which is the process the identity was read from
	// within the coincidence Identity names.
	PresencePresent Presence = "present"
	// PresenceAbsent is no running process with the identifier.
	PresenceAbsent Presence = "absent"
	// PresenceReplaced is a running process with the identifier and a
	// different start time: another process later given the same number.
	PresenceReplaced Presence = "replaced"
)

// String renders the presence.
func (p Presence) String() string { return string(p) }

// Presence looks for the process the identity names, on Identify's terms. An
// identity with a zero Start was read on a platform that could read none,
// and is refused with ErrIdentityUnreadable rather than compared, because
// the identifier alone proves nothing. Every other error is a failure to
// read, after which nothing is known about the process.
func (id Identity) Presence() (Presence, error) {
	if id.Start == 0 {
		return "", fmt.Errorf("%w: identity of process %d records no start time", ErrIdentityUnreadable, id.PID)
	}
	now, found, err := Identify(id.PID)
	switch {
	case err != nil:
		return "", err
	case !found:
		return PresenceAbsent, nil
	case now.Start != id.Start:
		return PresenceReplaced, nil
	default:
		return PresencePresent, nil
	}
}
