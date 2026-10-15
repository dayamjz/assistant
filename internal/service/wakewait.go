package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
)

// How the coordinator learns of a wake: the two ways PRD section 5's "write
// to the durable wake queue, then wake the coordinator" reaches it, and the
// drain both lead to.
//
// # The push
//
// wakes.subscribe is the coordinator's wake stream. Every row the supervisor
// loop appends is pushed on it by publishWake, from the loop's record, after
// the write that appended it committed, so a coordinator holding the stream
// is woken without asking again. The stream's publisher is this service's
// wakes and never events, which is open to every caller; internal/ipc opens
// the stream only after admitWakeStream, wakes.list's worker refusal, has
// admitted the caller, so a task's worker is never sent a wake. The event is
// a notice, ipc.TypeWakeQueued with the row as it was appended, and it
// acknowledges nothing: pushing is not draining. It is state on internal/ipc's
// terms, so an overflowing stream never drops it as noise and evicts it only
// into the gap marker, and a coordinator that receives the marker, the first
// delivery of every attach included, drains the queue.
//
// The narrowest reading of "wake the coordinator" is what is built: the
// service pushes to a coordinator client that is attached to this socket. It
// does not reach into the coordinator's host, a terminal or a hook, to start
// a turn; a coordinator that holds no stream is woken by nothing pushed, and
// its own wait or drain is what reaches the queue.
//
// # The wait
//
// wakes.wait blocks one call until the queue holds a queued row or its bound
// elapses, so a coordinator that does not hold the stream still pays no turns
// for an idle fleet (P9's "supervision costs nothing until something is
// actionable", from the coordinator's side). It answers with a drain: every
// answer is store.DrainWakes, the drain wakes.list makes, so the rows it
// returns are acknowledged by the coordinator in the transaction that read
// them, and the next wait does not return them. A wait armed while a row is
// already queued answers at once. When the bound elapses the queue is
// drained one last time, so an empty answer means the queue held no queued
// row at the bound. Between those, the wait checks for a queued row with
// store.UnacknowledgedWakes, a read, and drains only when one is there, so
// an idle wait writes nothing. A context that ended is checked before every
// drain, so a caller that went away is not answered with rows nobody will
// read; the window between a drain's commit and its answer reaching the
// caller is the residual gap below.
//
// # What wakes the wait early
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
// Neither the wait nor the stream keeps anything across calls: no cursor,
// no record of what was pushed or returned. A service that stops closes the
// connection, a wait's call fails as an operational failure and a stream
// ends, and a coordinator that arms either again against the next service
// reads the durable queue on the same terms as before: a wait drains what is
// still queued at once, and a stream opens gapped, which is the coordinator's
// signal to drain. Arming again is the caller's to do: nothing here
// reconnects.
//
// # Residual gaps
//
//   - Draining acknowledges, so delivery to a drain is at most once, which is
//     PRD section 8's text and store.DrainWakes' doc. A wait whose caller
//     went away after the drain committed, or a coordinator that crashed
//     after a drain and before acting, has had its rows acknowledged; they
//     are kept in the table and not handed out again.
//   - A pushed wake reaches only a coordinator attached when it is
//     published; one attached later learns of it from the gap marker it
//     opens with and the drain that follows.
//   - The wait occupies one in-flight request slot on its connection for as
//     long as it blocks, which is what machine.MaxWakeWaitTimeout bounds;
//     the stream occupies one stream slot for as long as it is held.
//   - A wake is surfaced by the loop's pass, so how soon after a terminal
//     dies either answers is the loop's cadence, not theirs.

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
// ipc.ErrUnavailable wrapping the context's error, and drains nothing.
func (s *Service) waitWakes(ctx context.Context, req ipc.Request, r machine.WakeWaitRequest) (machine.Wakes, error) {
	if err := s.refuseWorker(req); err != nil {
		return machine.Wakes{}, err
	}
	bound, limit, err := r.Bounds()
	if err != nil {
		return machine.Wakes{}, fmt.Errorf("%w: %w", ipc.ErrInvalidRequest, err)
	}
	by := req.WakeAcknowledger()
	ended := func() error {
		return fmt.Errorf("%w: the wait ended before its bound: %w", ipc.ErrUnavailable, ctx.Err())
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
		queued, err := s.store.UnacknowledgedWakes(ctx)
		if err != nil {
			return machine.Wakes{}, err
		}
		if len(queued) > 0 {
			if ctx.Err() != nil {
				return machine.Wakes{}, ended()
			}
			wakes, err := s.store.DrainWakes(ctx, limit, by)
			if err != nil {
				return machine.Wakes{}, err
			}
			if len(wakes) > 0 {
				return machine.Wakes{Wakes: wakes}, nil
			}
		}
		select {
		case <-rung:
		case <-recheck.C:
		case <-deadline.C:
			if ctx.Err() != nil {
				return machine.Wakes{}, ended()
			}
			wakes, err := s.store.DrainWakes(ctx, limit, by)
			if err != nil {
				return machine.Wakes{}, err
			}
			return machine.Wakes{Wakes: wakes}, nil
		case <-ctx.Done():
			return machine.Wakes{}, ended()
		}
	}
}

// admitWakeStream is the handler's half of wakes.subscribe: internal/ipc
// has already refused a contained caller, and this refuses a task's worker
// on wakes.list's terms. It returns nil to admit, and the stream itself is
// internal/ipc's to open and pump.
func (s *Service) admitWakeStream(req ipc.Request) error {
	return s.refuseWorker(req)
}

// publishWake pushes a row the loop appended to the coordinator's wake
// stream, as ipc.TypeWakeQueued with the row's identifier as its revision.
// A publication is not a record: a payload that cannot be encoded or
// published is logged, and the row is still queued for a drain to take. A
// service with no wake publisher, which Open never builds and this
// package's internal tests build by hand, publishes nothing.
func (s *Service) publishWake(w store.Wake) {
	if s.wakes == nil {
		return
	}
	payload, err := json.Marshal(w)
	if err != nil {
		s.log.Printf("could not encode wake %d of task %s: %v", w.ID, w.TaskID, err)
		return
	}
	if err := s.wakes.Publish(ipc.Event{Type: ipc.TypeWakeQueued, Revision: uint64(w.ID), Payload: payload}); err != nil {
		s.log.Printf("could not push wake %d of task %s: %v", w.ID, w.TaskID, err)
	}
}
