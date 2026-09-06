package home

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

// ErrLocked reports that another process holds this home's lock. PRD section 8
// gives a home exactly one service, so this is what a second one meets.
var ErrLocked = errors.New("home: another process holds this home's lock")

// lockPoll is how often a bounded wait re-asks for the lock. It is short
// enough that a successor waiting for a predecessor to let go starts promptly,
// and long enough that waiting costs nothing measurable.
const lockPoll = 20 * time.Millisecond

// Lock is the home's exclusive lock, held for as long as the process that took
// it holds this value.
//
// The operating system releases it when the holding process ends, whatever
// ends it, so a home whose service was killed is immediately available to the
// next one and there is no stale state to reason about. That is the property
// PRD section 8 asks for and the reason this is a kernel lock rather than a
// file naming a process.
type Lock struct {
	file *os.File
}

// Acquire takes the home's exclusive lock, waiting no longer than ctx allows
// and no longer than wait.
//
// A wait of zero asks once and refuses with ErrLocked if the lock is held,
// which is what a command wanting to know whether a service is running asks
// for. A positive wait is for a successor that is meant to replace a service
// still shutting down: it re-asks until the predecessor lets go.
//
// The home root must exist. Acquire does not create it, because a lock is
// taken on a home rather than as a way of making one.
func (h *Home) Acquire(ctx context.Context, wait time.Duration) (*Lock, error) {
	file, err := os.OpenFile(h.LockFile(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("home: opening the lock file: %w", err)
	}
	deadline := time.Now().Add(wait)
	for {
		taken, err := tryLock(file)
		if err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("home: locking %s: %w", h.LockFile(), err)
		}
		if taken {
			return &Lock{file: file}, nil
		}
		if !time.Now().Before(deadline) {
			_ = file.Close()
			return nil, fmt.Errorf("%w: %s", ErrLocked, h.LockFile())
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-time.After(lockPoll):
		}
	}
}

// Release gives up the lock. It is safe to call once; a second call reports
// that the lock is already released rather than closing a descriptor twice.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	if err := unlock(file); err != nil {
		_ = file.Close()
		return fmt.Errorf("home: releasing the lock: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("home: releasing the lock: %w", err)
	}
	return nil
}
