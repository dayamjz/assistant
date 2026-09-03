package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// DefaultBacklog is the per-subscriber queue depth used when a caller does not
// choose one. It is large enough that a consumer doing ordinary work is not
// gapped by a burst of log lines, and small enough that a dead one costs a
// bounded amount of memory.
const DefaultBacklog = 256

// eventEnvelopeBytes is the room a frame reserves around an event payload: the
// frame's own fields, the event's type and revision, the JSON punctuation, and
// the newline. The fields other than the type cost under a hundred bytes at
// their widest, so the rest of this allowance is room for a type name.
const eventEnvelopeBytes = 1 << 10

// DefaultMaxPayloadBytes bounds the payload of one event when a caller does not
// choose a bound. It is DefaultMaxFrameBytes less the envelope a frame adds
// around a payload, so an event with a payload at the bound and a type name
// inside that envelope fits in a default frame.
//
// It bounds what a producer may publish; it does not promise delivery. A server
// may be given a smaller MaxFrameBytes than this, and a stream that meets an
// event it cannot put in a frame still has to decide what to do about it.
const DefaultMaxPayloadBytes = DefaultMaxFrameBytes - eventEnvelopeBytes

// PublisherConfig is what a publisher may be given. Its one field has a
// documented default, so the zero value is usable.
type PublisherConfig struct {
	// MaxPayloadBytes bounds the payload of one published event. Zero means
	// DefaultMaxPayloadBytes, and a negative value is refused.
	MaxPayloadBytes int
}

// Publisher fans events out to subscribers without ever waiting for one.
//
// Publishing takes each subscriber's lock only for as long as it takes to move
// one event into a fixed-size queue, and never holds a lock across a
// consumer's read. A subscriber that stops reading therefore cannot slow, stall
// or fail the work being reported on; it gaps itself instead.
type Publisher struct {
	maxPayload int

	mu     sync.Mutex
	subs   map[*Subscription]struct{}
	closed bool
}

// NewPublisher returns a publisher with no subscribers, or an error naming what
// it was given that is not a bound.
func NewPublisher(cfg PublisherConfig) (*Publisher, error) {
	if cfg.MaxPayloadBytes < 0 {
		return nil, fmt.Errorf("ipc: %d is not an event payload size", cfg.MaxPayloadBytes)
	}
	maxPayload := cfg.MaxPayloadBytes
	if maxPayload == 0 {
		maxPayload = DefaultMaxPayloadBytes
	}
	return &Publisher{maxPayload: maxPayload, subs: make(map[*Subscription]struct{})}, nil
}

// MaxPayloadBytes reports the payload bound this publisher applies, so a
// producer can project its output to fit rather than discover the bound by
// being refused.
func (p *Publisher) MaxPayloadBytes() int { return p.maxPayload }

// Subscribe opens a stream with room for backlog events. A backlog below one
// is a programming error and is refused; DefaultBacklog is used for zero.
//
// The subscription opens in the gap state, so its first delivery is a gap
// marker and a consumer cannot apply a delta to state it never reconciled.
// That is true of a first attach and of a reattach alike, because a stream
// cannot know what a consumer held before it arrived.
func (p *Publisher) Subscribe(backlog int) (*Subscription, error) {
	switch {
	case backlog == 0:
		backlog = DefaultBacklog
	case backlog < 0:
		return nil, fmt.Errorf("ipc: backlog %d is not a queue depth", backlog)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, ErrStreamClosed
	}
	s := newSubscription(backlog, true)
	s.pub = p
	p.subs[s] = struct{}{}
	return s, nil
}

// Publish delivers e to every current subscriber. It never blocks on a
// subscriber and never fails because of one: a subscriber that cannot take the
// event is gapped, and the error reported here is only about e itself.
//
// A state event must carry a revision, because a consumer orders deltas by it
// and one that cannot be ordered cannot be applied.
//
// Nothing here enforces that revisions increase. Two goroutines publishing
// adjacent revisions can legitimately reach this call inverted, and refusing
// the older one would discard state to enforce an ordering the consumer's
// Cursor already enforces without discarding anything.
//
// An event whose payload is past this publisher's bound, or whose payload is
// not JSON, is refused, and the refusal names what was wrong so the caller can
// report it. Both are producer bugs rather than delivery decisions: PRD section
// 8 makes the full log the authority and what travels a bounded projection of
// it, so producing a projection that is bounded and that can be written is the
// publishing caller's obligation. Both apply to every class, because neither
// depends on what the event is about.
func (p *Publisher) Publish(e Event) error {
	if e.Type == "" {
		return errors.New("ipc: event has no type")
	}
	if len(e.Payload) > p.maxPayload {
		return fmt.Errorf("%w: %q carries %d bytes of payload, and an event may carry at most %d",
			ErrPayloadTooLarge, e.Type, len(e.Payload), p.maxPayload)
	}
	if len(e.Payload) > 0 && !json.Valid(e.Payload) {
		return fmt.Errorf("%w: %q carries a payload that is not JSON", ErrInvalidPayload, e.Type)
	}
	if e.Class() == ClassState && e.Revision == 0 {
		return fmt.Errorf("ipc: state event %q has no revision", e.Type)
	}
	if e.Type == TypeGap {
		return errors.New("ipc: a gap marker is produced by the stream, not published")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ErrStreamClosed
	}
	for s := range p.subs {
		// A subscription that ended under this delivery is the subscriber's
		// own state, and Publish never reports one: it detaches itself, and
		// this returns only what is wrong with e.
		_ = s.deliver(e)
	}
	return nil
}

// Subscribers reports how many subscriptions are open.
func (p *Publisher) Subscribers() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.subs)
}

// Close ends every subscription and refuses later ones. A consumer still
// receives whatever its queue holds, and then ErrStreamClosed.
func (p *Publisher) Close() error {
	p.mu.Lock()
	subs := make([]*Subscription, 0, len(p.subs))
	for s := range p.subs {
		subs = append(subs, s)
	}
	p.subs = make(map[*Subscription]struct{})
	p.closed = true
	p.mu.Unlock()
	for _, s := range subs {
		s.finish(ErrStreamClosed)
	}
	return nil
}

// remove detaches s from the publisher. It is safe to call more than once.
func (p *Publisher) remove(s *Subscription) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.subs, s)
}

// Subscription is one consumer's bounded view of a stream.
//
// # What overflow does
//
// The queue has a fixed depth and publishing never waits for it to drain, so
// something has to give when a consumer falls behind. What gives is decided by
// class, in this order:
//
//   - The oldest queued activity event is discarded to make room. An arriving
//     activity event that finds no queued activity is itself discarded, so
//     progress never displaces a delta.
//   - With no activity left to discard, the oldest queued state event is
//     discarded. This is safe only because of what a gap means: a gapped
//     consumer must reconcile from a full read before applying another delta,
//     and that read supersedes every delta still queued. It is a collapse into
//     the marker rather than a quiet discard.
//   - With neither left, the queue holds only control events, which no read
//     can give a consumer back. The subscription ends with ErrSubscriberStalled
//     rather than discarding one or growing without bound. The consumer sees
//     the stream end and must attach again, which reconciles.
//
// Every discard raises the gap, and consecutive discards collapse into one
// marker. The marker is delivered ahead of whatever the queue holds, so a
// consumer learns it is missing something before it acts on what follows.
type Subscription struct {
	pub *Publisher

	mu      sync.Mutex
	ring    []Event
	head    int
	length  int
	dropped uint64
	gap     bool
	done    bool
	err     error
	ready   chan struct{}
}

// newSubscription builds a subscription with a fixed queue depth.
//
// gapped is what the subscription opens in. A subscription onto a Publisher
// opens gapped, because nothing before it attached is recoverable. A queue
// relaying a stream that already carries its own markers opens ungapped: the
// marker the service put at the head of that stream is the opening gap, and
// raising a second one here would only make a consumer reconcile twice.
func newSubscription(backlog int, gapped bool) *Subscription {
	return &Subscription{
		ring:  make([]Event, backlog),
		gap:   gapped,
		ready: make(chan struct{}, 1),
	}
}

// Recv returns the next event, waiting until one is available, the stream
// ends, or ctx is done.
//
// A pending gap marker is returned before any queued event. When the stream
// has ended, whatever the queue still holds is delivered first, and the reason
// it ended is returned after that: ErrStreamClosed for an orderly end and
// ErrSubscriberStalled for a consumer that fell too far behind to be served
// without discarding something no read could give back.
func (s *Subscription) Recv(ctx context.Context) (Event, error) {
	for {
		s.mu.Lock()
		if s.gap {
			dropped := s.dropped
			s.gap, s.dropped = false, 0
			s.mu.Unlock()
			return gapEvent(dropped), nil
		}
		if s.length > 0 {
			e := s.ring[s.head]
			s.ring[s.head] = Event{}
			s.head = (s.head + 1) % len(s.ring)
			s.length--
			s.mu.Unlock()
			return e, nil
		}
		if s.done {
			err := s.err
			s.mu.Unlock()
			return Event{}, err
		}
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return Event{}, ctx.Err()
		case <-s.ready:
		}
	}
}

// Gapped reports whether a marker is waiting to be delivered. It is for tests
// and diagnostics; a consumer learns about a gap by receiving the marker.
func (s *Subscription) Gapped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gap
}

// Len reports how many events the queue holds, not counting a pending marker.
func (s *Subscription) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.length
}

// Close detaches the subscription. A Recv already waiting returns
// ErrStreamClosed, as does every later one once the queue is drained. It
// returns nothing because detaching cannot fail; there is no half-closed state
// a caller would have to do something about.
func (s *Subscription) Close() {
	if s.pub != nil {
		s.pub.remove(s)
	}
	s.finish(ErrStreamClosed)
}

// finish ends the subscription with err, keeping the first reason.
func (s *Subscription) finish(err error) {
	s.mu.Lock()
	if !s.done {
		s.done, s.err = true, err
	}
	s.mu.Unlock()
	s.signal()
}

// deliver queues e, applying the overflow policy. It never blocks.
//
// It reports whether the subscription is still open afterwards. A delivery that
// could only be made by discarding something no read gives back ends it, and a
// caller relaying a stream into this queue has to know that so it can stop the
// stream it is relaying rather than keep feeding a queue nobody reads.
func (s *Subscription) deliver(e Event) bool {
	s.mu.Lock()
	defer func() {
		s.mu.Unlock()
		s.signal()
	}()
	if s.done {
		return false
	}
	if s.length < len(s.ring) {
		s.push(e)
		return true
	}
	if e.Class() == ClassActivity {
		// Progress never displaces a delta. Room is made by discarding older
		// progress, and when there is none the arriving event is what goes.
		if !s.evict(ClassActivity) {
			s.discarded()
			return true
		}
		s.push(e)
		return true
	}
	if s.evict(ClassActivity) || s.evict(ClassState) {
		s.push(e)
		return true
	}
	// Only control events are queued, and none of them may be discarded. The
	// arriving event is still a discard, so it raises the gap, and the stream
	// ends behind it.
	s.discarded()
	s.done, s.err = true, ErrSubscriberStalled
	return false
}

// discard records that an event this subscription already handed out could not
// be delivered any further. It is the same discard the queue does under
// overflow: the gap is raised, so the consumer reconciles rather than acting on
// state it never saw, and the subscription survives.
func (s *Subscription) discard() {
	s.mu.Lock()
	if !s.done {
		s.discarded()
	}
	s.mu.Unlock()
	s.signal()
}

// push appends e to the queue, which the caller has checked has room.
func (s *Subscription) push(e Event) {
	s.ring[(s.head+s.length)%len(s.ring)] = e
	s.length++
}

// evict discards the oldest queued event of class c and reports whether it
// found one. The gap is raised for whatever it discarded.
func (s *Subscription) evict(c Class) bool {
	for i := 0; i < s.length; i++ {
		idx := (s.head + i) % len(s.ring)
		if s.ring[idx].Class() != c {
			continue
		}
		for j := i; j < s.length-1; j++ {
			s.ring[(s.head+j)%len(s.ring)] = s.ring[(s.head+j+1)%len(s.ring)]
		}
		s.ring[(s.head+s.length-1)%len(s.ring)] = Event{}
		s.length--
		s.discarded()
		return true
	}
	return false
}

// discarded records one discard. Consecutive discards collapse into the one
// marker that is still waiting to be delivered.
func (s *Subscription) discarded() {
	s.dropped++
	s.gap = true
}

// signal wakes a waiting Recv without blocking when one is already awake.
func (s *Subscription) signal() {
	select {
	case s.ready <- struct{}{}:
	default:
	}
}
