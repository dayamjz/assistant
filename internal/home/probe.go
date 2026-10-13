package home

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// LockState is what a probe of the home's lock can say about it. The set is
// closed: a probe answers with one of the three and nothing else.
type LockState int

const (
	// LockUnknown is every answer the probe could not establish. It is the
	// zero value so that a reading nobody filled in is not read as Free.
	LockUnknown LockState = iota
	// LockFree is a lock no process holds at the instant of the probe.
	LockFree
	// LockHeld is a lock some open handle holds, in this process or another.
	LockHeld
)

// String names the state for a report.
func (s LockState) String() string {
	switch s {
	case LockFree:
		return "free"
	case LockHeld:
		return "held"
	default:
		return "unknown"
	}
}

// LockReading is one probe's answer: the state and, where the state alone
// would hide how it was reached, the detail. The one detail this package
// writes is on a Free answered for a lock file that does not exist, because
// a home that was never served and a home that was never created both look
// like that, and a reader deciding anything from Free should know it was
// decided without a lock file to ask.
type LockReading struct {
	State  LockState
	Detail string
}

// probeHolding, when set, runs while a probe holds the lock between taking it
// and letting go. It exists so a test can overlap a second probe with that
// instant deterministically, and nothing else sets it.
var probeHolding func()

// LockState probes whether the home's lock is held, without holding it.
//
// There is no read-only query of an operating-system lock, so the probe takes
// the lock for an instant and releases it. What that can disturb is an
// Acquire with a wait of zero that races the instant, which is refused with
// ErrLocked as if a service held the lock; an Acquire with a positive wait
// re-asks and is not disturbed, which is what a starting service uses.
//
// The semantics are the whole of what a probe can deliver, so they are the
// contract:
//
//   - A lock file no process holds answers LockFree.
//   - A lock held by any open handle answers LockHeld, whichever process holds
//     it. The lock is per open handle rather than per process, so a probe from
//     inside the service that holds it answers LockHeld because the service
//     holds it, not because a supervisor is live: nothing in-process may read
//     this answer as evidence about itself.
//   - A root that exists with no lock file answers LockFree with a Detail
//     saying so. The probe never creates the file, so a home that was never
//     served is left as it was found.
//   - Every failure, a root that is not there or cannot be read, a lock file
//     that cannot be opened, a context already done, a lock call the
//     operating system refused, answers LockUnknown with the error, never
//     LockFree. A reader that treats Free as "no service" must not be given
//     Free by a permissions error or a wrong path.
//
// Two more things it does not establish. A LockHeld says some open handle held
// the lock at the instant, and the handle may be another probe's: two probes
// overlapping, a supervisor's and a command's, make the second answer LockHeld
// with no service running, and there is no read-only query to close that
// (flock is invisible to F_GETLK, open-file-description locks are not
// everywhere the module builds, and Windows offers no query). So a lone
// LockHeld is not evidence of a live service. The consumer rule is: LockHeld
// counts as live only together with a beacon whose instance matches the
// instance file, and a reader acting on a LockHeld with nothing beside it
// probes once more before it acts, because a probe's instant does not repeat.
// And matching a holder to an instance identity is the beacon's job, not this
// probe's.
//
// A root that does not exist is LockUnknown, not LockFree: a misconfigured
// home path would otherwise read as a home with no service, which is the
// answer a stop guard must not be handed by a typo. LockFree with a Detail is
// kept for a root that exists and holds no lock file, which is a home that
// was never served. In practice LockUnknown comes from a lock file the caller
// cannot open for writing, since the probe needs a writable handle to ask.
func (h *Home) LockState(ctx context.Context) (LockReading, error) {
	if err := ctx.Err(); err != nil {
		return LockReading{State: LockUnknown}, fmt.Errorf("home: probing %s: %w", h.LockFile(), err)
	}
	file, err := os.OpenFile(h.LockFile(), os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		if _, statErr := os.Stat(h.root); statErr != nil {
			return LockReading{State: LockUnknown}, fmt.Errorf("home: probing %s: the home root is not there: %w", h.LockFile(), statErr)
		}
		return LockReading{
			State:  LockFree,
			Detail: fmt.Sprintf("no lock file at %s: this home has never been served", h.LockFile()),
		}, nil
	}
	if err != nil {
		return LockReading{State: LockUnknown}, fmt.Errorf("home: probing %s: %w", h.LockFile(), err)
	}
	defer func() { _ = file.Close() }()
	taken, err := tryLock(file)
	if err == nil && taken && probeHolding != nil {
		probeHolding()
	}
	if err != nil {
		return LockReading{State: LockUnknown}, fmt.Errorf("home: probing %s: %w", h.LockFile(), err)
	}
	if !taken {
		return LockReading{State: LockHeld}, nil
	}
	if err := unlock(file); err != nil {
		return LockReading{State: LockUnknown}, fmt.Errorf("home: releasing the probe of %s: %w", h.LockFile(), err)
	}
	return LockReading{State: LockFree}, nil
}
