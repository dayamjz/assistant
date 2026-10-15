package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
)

// The blocking wait on the wake queue: wakes.wait, which lets a coordinator
// block one call until something needs it rather than poll wakes.list, so
// an idle fleet costs it no turns (P9's "supervision costs nothing until
// something is actionable", from the coordinator's side).
//
// # What decides the answer
//
// The queue does. Every answer is a store.DrainWakes read, the same read
// wakes.list makes, so the wait returns early exactly when that read finds
// a queued row, returns those rows, and acknowledges none: delivery stays at
// least once, and a row the wait returned is returned by the next wait until
// wakes.acknowledge names it. A wait armed while a row is already queued
// answers at once. When the bound elapses the queue is read one last time,
// so an empty answer means the queue held no queued row at the bound.
//
// # What wakes it early
//
// wakeBell, rung by the supervisor loop's record after a write that
// appended a row, and nothing else. The bell is this service's memory and
// not a record: it carries no wake and nothing reads it as one, it only
// says the queue is worth reading again. The wait takes the bell's channel
// before each read, so a row appended after the read rings a channel the
// wait already holds and is not missed between the read and the block. A
// row that reached the table by any path that does not ring the bell is
// still found, by a read at least once per wakeWaitRecheck; that bounds how
// late such a row can be answered and is the whole of what the recheck is
// for.
//
// # A restart
//
// The wait keeps nothing across calls: no cursor, no record of what it
// returned, nothing written. A service that stops while a wait is armed
// closes the connection, the caller's call fails as an operational
// failure, and a wait armed against the next service reads the durable
// queue on the same terms as the first, returning at once whatever is still
// queued. Re-arming is therefore safe, and it is also the caller's to do:
// nothing here reconnects.
//
// # Residual gaps
//
//   - A wait answers what is queued, not what is new: a coordinator that
//     re-arms without acknowledging what it handled is answered at once with
//     the same rows, which is at-least-once delivery working as stated.
//   - The wait occupies one in-flight request slot on its connection for as
//     long as it blocks, which is what machine.MaxWakeWaitTimeout bounds.
//   - A wake is surfaced by the loop's pass, so how soon after a terminal
//     dies the wait answers is the loop's cadence, not the wait's.

// wakeWaitRecheck is how often an armed wait reads the queue when the bell
// has not rung, which bounds how late a row that did not ring it is
// answered. It is chosen, not measured.
const wakeWaitRecheck = 5 * time.Second

// wakeBell is the in-memory signal that the wake table gained a row. The
// zero value is ready to use, so a Service this package's tests build by
// hand has one. It is safe for concurrent use.
type wakeBell struct {
	mu sync.Mutex
	ch chan struct{}
}

// armed returns a channel that is closed by the next ring after this call.
func (b *wakeBell) armed() <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ch == nil {
		b.ch = make(chan struct{})
	}
	return b.ch
}

// ring closes the channel every armed caller holds and replaces it, so each
// caller is woken once and a later armed call waits for a later ring.
func (b *wakeBell) ring() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ch != nil {
		close(b.ch)
		b.ch = nil
	}
}

// waitWakes is wakes.wait on the terms above. A worker is refused before
// anything is read, on wakes.list's terms; a bound or a limit
// machine.WakeWaitRequest.Bounds refuses is ipc.ErrInvalidRequest, which
// the command line reports as incorrect usage. A context that ends before
// the bound, because the caller went away or the service is closing, is
// ipc.ErrUnavailable wrapping the context's error.
func (s *Service) waitWakes(ctx context.Context, req ipc.Request, r machine.WakeWaitRequest) (machine.Wakes, error) {
	if err := s.refuseWorker(req); err != nil {
		return machine.Wakes{}, err
	}
	bound, limit, err := r.Bounds()
	if err != nil {
		return machine.Wakes{}, fmt.Errorf("%w: %w", ipc.ErrInvalidRequest, err)
	}
	deadline := time.NewTimer(bound)
	defer deadline.Stop()
	every := wakeWaitRecheck
	if s.sup != nil && s.sup.hooks.wakeRecheck > 0 {
		every = s.sup.hooks.wakeRecheck
	}
	recheck := time.NewTicker(every)
	defer recheck.Stop()
	for {
		rung := s.wakeBell.armed()
		wakes, err := s.store.DrainWakes(ctx, limit)
		if err != nil {
			return machine.Wakes{}, err
		}
		if len(wakes) > 0 {
			return machine.Wakes{Wakes: wakes}, nil
		}
		select {
		case <-rung:
		case <-recheck.C:
		case <-deadline.C:
			wakes, err := s.store.DrainWakes(ctx, limit)
			if err != nil {
				return machine.Wakes{}, err
			}
			return machine.Wakes{Wakes: wakes}, nil
		case <-ctx.Done():
			return machine.Wakes{}, fmt.Errorf("%w: the wait ended before its bound: %w", ipc.ErrUnavailable, ctx.Err())
		}
	}
}
