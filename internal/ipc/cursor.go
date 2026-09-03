package ipc

import (
	"fmt"
	"sync"
)

// Disposition is what a consumer should do with an event it received.
type Disposition uint8

const (
	// DispositionReconcile means the consumer must read the current state in
	// full before applying this or any later delta. It is the zero value on
	// purpose: a Cursor that was never told anything is gapped.
	DispositionReconcile Disposition = iota
	// DispositionApply means the event may be acted on now.
	DispositionApply
	// DispositionIgnore means the event is older than or the same as what the
	// consumer already holds. Acting on it would move the consumer backwards.
	DispositionIgnore
)

// String renders the disposition as it appears in diagnostics.
func (d Disposition) String() string {
	switch d {
	case DispositionReconcile:
		return "reconcile"
	case DispositionApply:
		return "apply"
	case DispositionIgnore:
		return "ignore"
	default:
		return fmt.Sprintf("disposition(%d)", uint8(d))
	}
}

// Cursor is the consumer half of the delivery contract: it decides what may be
// done with each event a stream delivers.
//
// It opens gapped. That is the type's own contract rather than a check on the
// stream that feeds it: a consumer holds no state until it has read some, so
// there is nothing a delta could be applied to, and a cursor that survives a
// reattach is in exactly the same position about a service it was away from.
//
// A cursor is safe for concurrent use, so a consumer may hand it to whatever
// applies deltas and whatever performs the reconciling read.
type Cursor struct {
	mu       sync.Mutex
	gapped   bool
	revision uint64
}

// NewCursor returns a cursor in the gap state, holding no revision.
func NewCursor() *Cursor { return &Cursor{gapped: true} }

// Observe reports what to do with e and records what it implies.
//
//   - A gap marker gaps the cursor and asks for a reconcile.
//   - Activity and control are applied whatever the cursor holds, because
//     neither is a delta to state.
//   - A state event is applied only when the cursor is not gapped and the
//     event's revision is newer than what the cursor holds. An older or
//     repeated revision is ignored, so a duplicate or a delivery that arrived
//     out of order cannot move a consumer backwards.
//   - A state event with no revision cannot be ordered, so it gaps the cursor
//     rather than being applied or quietly ignored. A build that added an
//     event type this one does not know reaches this path, because an
//     unrecognized type is classified as state.
func (c *Cursor) Observe(e Event) Disposition {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e.Type == TypeGap {
		c.gapped = true
		return DispositionReconcile
	}
	if e.Class() != ClassState {
		return DispositionApply
	}
	if e.Revision == 0 {
		c.gapped = true
		return DispositionReconcile
	}
	if c.gapped {
		return DispositionReconcile
	}
	if e.Revision <= c.revision {
		return DispositionIgnore
	}
	c.revision = e.Revision
	return DispositionApply
}

// Reconciled records that the consumer read the state in full at revision, and
// clears the gap. A revision no newer than what the cursor already holds
// clears the gap without moving the cursor backwards.
func (c *Cursor) Reconciled(revision uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gapped = false
	if revision > c.revision {
		c.revision = revision
	}
}

// Gapped reports whether the consumer owes itself a reconciling read.
func (c *Cursor) Gapped() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gapped
}

// Revision reports the newest revision the consumer has applied or reconciled
// to. It is zero before the first of either.
func (c *Cursor) Revision() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.revision
}
