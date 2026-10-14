package gate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// A gate repository's linked worktrees, and the lock a push waits on for them.
//
// A run's isolated copy is a linked worktree of the gate repository, so cutting
// one and removing one both write an administrative entry inside this
// repository, and the push and fetch that write into this repository read every
// such entry while they run. Issue #144 observed what that costs: a push that
// arrived while an entry was part-written was refused with "bad object
// worktrees/<id>/HEAD", and so was a fetch into the gate. The person pushing
// had done nothing wrong and nothing told them why.
//
// The copies lock is what this package offers instead. It is an
// operating-system lock on one file inside the gate repository, taken
// exclusively by whatever cuts or removes a copy (LockCopies) and shared by
// whatever writes into the repository through git's receive or fetch
// (AwaitCopies). A holder that dies releases it, so it cannot go stale, which
// is the property internal/home's lock is chosen for too.
//
// The lock serializes those who ask for it and nobody else. Three askers take
// it in this build: internal/service when it cuts a copy and when it fetches
// into the gate, internal/tasks when it removes one, and Receive, which is the
// receive command the assistant remote of an initialized working copy runs. A
// git process that does not go through one of them - a push to the gate named
// by its path rather than by that remote, a push from a task's own copy, the
// rebase stage's fetch in a run's copy, a cleanup proof's fetch into the gate -
// takes no lock and keeps the exposure the issue describes.
//
// A push does not wait forever. ReceiveWait bounds it, and past the bound the
// push is refused with ErrCopyInTransit, which names the state it waited on, so
// a person is told to send the change again rather than shown git's own
// message about an object. Nothing about what a push may do changes: the lock decides when the
// receive starts and never what it accepts.

// copiesLockName is the file inside a gate repository whose lock is the copies
// lock. It is a file of its own rather than one git writes, so taking it asks
// nothing of git and leaves nothing git reads.
const copiesLockName = "assistant-copies.lock"

// copiesPoll is how often a wait re-asks for the lock. The lock is taken
// without blocking so a wait can end on a deadline or a cancelled context,
// which a blocking request cannot; this is the cost of that, and it is short
// against the length of a cut.
const copiesPoll = 10 * time.Millisecond

// ReceiveWait is how long a push into a gate waits for a copy being cut or
// removed there before it is refused with ErrCopyInTransit.
//
// It is chosen, not measured. It has to outlast a cut, which is a checkout of
// the commit under validation and grows with the repository, and it has to be
// short enough that a person watching a push does not read the wait as a hang;
// Receive tells its caller when it starts to wait, so the wait need never be
// silent. It is a constant rather than a configuration key
// because no layer of configuration is in reach of the receive command: it
// runs in the pushing process, before the service is asked anything.
const ReceiveWait = 30 * time.Second

// ErrCopyInTransit reports that a run's copy was still being cut or removed in
// the gate when a wait for it ran out. Nothing was received or fetched: the
// refusal comes before git is started. Match it with errors.Is; the error
// carrying it is a *CopyInTransitError.
var ErrCopyInTransit = errors.New("gate: a run's local copy was still being set up or cleaned up in this gate")

// CopyInTransitError is ErrCopyInTransit with what was waited on and for how
// long.
type CopyInTransitError struct {
	// Repository is the gate repository whose copies were in transit.
	Repository string
	// Waited is how long the caller waited before giving up.
	Waited time.Duration
}

// Error names the transient state for an operator's log. What a person
// pushing reads is the command surface's wording, which internal/machine
// holds to PRD section 9's vocabulary.
func (e *CopyInTransitError) Error() string {
	return fmt.Sprintf("gate: a run's copy was still being cut or removed in %s after %s, so nothing was "+
		"received or fetched", e.Repository, e.Waited)
}

// Is makes every CopyInTransitError match ErrCopyInTransit.
func (e *CopyInTransitError) Is(target error) bool { return target == ErrCopyInTransit }

// CopiesLock is a held copies lock. Release gives it back; the operating
// system gives it back when the holding process ends, whatever ends it.
type CopiesLock struct {
	file *os.File
}

// LockCopies takes the copies lock of the gate repository at repository
// exclusively, for cutting or removing a linked worktree there, and waits for
// it as long as ctx allows.
//
// It is not bounded otherwise, because the holders it waits on are a push and
// a fetch, which end; a push suspended part way through holds a cut back for
// as long as it is suspended, and that is the residual gap. A steady stream of
// overlapping pushes can also keep it waiting, since a shared lock already held
// lets another be taken beside it.
//
// repository is a gate repository as RepositoryFor returns it. This writes no
// state git reads: the lock file is created when it is missing and is never
// removed, because removing it would let two holders lock two different files.
func LockCopies(ctx context.Context, repository string) (*CopiesLock, error) {
	return lockCopies(ctx, repository, true, 0, nil)
}

// AwaitCopies takes the copies lock of the gate repository at repository
// shared, for writing into the repository while no copy is being cut or
// removed there, and waits no longer than wait.
//
// When it has to wait it calls waiting once, before the wait begins, so a
// caller can tell a person why nothing is happening. When the wait runs out it
// refuses with a *CopyInTransitError, and when ctx ends first it returns
// ctx's error.
func AwaitCopies(ctx context.Context, repository string, wait time.Duration, waiting func()) (*CopiesLock, error) {
	return lockCopies(ctx, repository, false, wait, waiting)
}

func lockCopies(ctx context.Context, repository string, exclusive bool, wait time.Duration,
	waiting func()) (*CopiesLock, error) {
	path := filepath.Join(repository, copiesLockName)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("gate: opening the copies lock at %s: %w", path, err)
	}
	var deadline <-chan time.Time
	if !exclusive {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		deadline = timer.C
	}
	ticker := time.NewTicker(copiesPoll)
	defer ticker.Stop()
	for announced := false; ; {
		taken, err := tryLockCopies(file, exclusive)
		if err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("gate: locking %s: %w", path, err)
		}
		if taken {
			return &CopiesLock{file: file}, nil
		}
		if !announced {
			announced = true
			if waiting != nil {
				waiting()
			}
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-deadline:
			_ = file.Close()
			return nil, &CopyInTransitError{Repository: repository, Waited: wait}
		case <-ticker.C:
		}
	}
}

// Release gives the lock back. A second call, and a call on nil, does nothing.
func (l *CopiesLock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	if err := unlockCopies(file); err != nil {
		_ = file.Close()
		return fmt.Errorf("gate: releasing the copies lock: %w", err)
	}
	return file.Close()
}
