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
//   - A home with no lock file answers LockFree with a Detail saying so. The
//     probe never creates the file, so a home that was never created or never
//     served is left as it was found.
//   - Every failure, a root that cannot be read, a context already done, a
//     lock call the operating system refused, answers LockUnknown with the
//     error, never LockFree. A reader that treats Free as "no service" must not
//     be given Free by a permissions error.
//
// What it does not establish is who holds the lock. A LockHeld says some
// process holds it, and matching that process to an instance identity is the
// beacon's job, not this probe's.
func (h *Home) LockState(ctx context.Context) (LockReading, error) {
	if err := ctx.Err(); err != nil {
		return LockReading{State: LockUnknown}, fmt.Errorf("home: probing %s: %w", h.LockFile(), err)
	}
	file, err := os.OpenFile(h.LockFile(), os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return LockReading{
			State:  LockFree,
			Detail: fmt.Sprintf("no lock file at %s: this home was never served, or never created", h.LockFile()),
		}, nil
	}
	if err != nil {
		return LockReading{State: LockUnknown}, fmt.Errorf("home: probing %s: %w", h.LockFile(), err)
	}
	defer func() { _ = file.Close() }()
	taken, err := tryLock(file)
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
