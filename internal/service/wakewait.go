package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/store"
)

// How the coordinator learns of a wake: the two ways PRD section 5's "write
// to the durable wake queue, then wake the coordinator" reaches it, the drain
// both lead to, and when a row a drain handed out is handed out again.
//
// # The push
//
// wakes.subscribe is the caller's wake stream: the coordinator's, or, for a
// subordinate coordinator, its own domain's, which PRD section 9 grants it
// for its domain's rows alone. Every row the supervisor loop appends is
// pushed by publishWake, from the loop's record, after the write that
// appended it committed, on exactly one stream: the one whose drain would
// offer the row, read on the same route that drain is made on, so a
// coordinator holding its stream is woken without asking again and never
// by a row that is another coordinator's (P14). The coordinator's publisher
// is this service's wakes, and each domain's is made on its subordinate's
// first subscription, never events, which is open to every caller;
// internal/ipc opens the stream only after admitWakeStream, wakes.list's
// worker refusal, has admitted the caller and chosen the stream, so a
// task's worker is never sent a wake and a subordinate is attached to the
// domain the placement derived and to no other. The event is
// a notice, ipc.TypeWakeQueued with the row as it was appended, and it hands
// out and acknowledges nothing: pushing is not draining. It is state on
// internal/ipc's terms, so an overflowing stream never drops it as noise and
// evicts it only into the gap marker, and a coordinator that receives the
// marker, the first delivery of every attach included, drains the queue.
//
// The narrowest reading of "wake the coordinator" is what is built: the
// service pushes to a coordinator client that is attached to this socket. It
// does not reach into the coordinator's host, a terminal or a hook, to start
// a turn; a coordinator that holds no stream is woken by nothing pushed, and
// its own wait or drain is what reaches the queue.
//
// # The drain hands out, and the acknowledgement closes
//
// PRD section 8 as the owner amended it: draining hands a row out, and
// acknowledging closes it. wakes.list and wakes.wait are store.DrainWakes,
// which records each row it returns as handed out to the coordinator, under
// an attachment, and leaves it queued; wakes.acknowledge, which the
// coordinator calls after it acted, is what closes a row. A row a drain
// handed out still counts for the stop guard and the pull guard, because
// both read store.UnacknowledgedWakes.
//
// Which rows a drain offers is decided by attachment and never by a clock.
// The coordinator is attached while this service's wake stream is held:
// wakeAttachment reads the stream publisher's ipc.Publisher.Attachment, a
// stretch that begins when a subscription opens with none open and ends
// when the last one closes, and names it by this service's instance and the
// stretch's number. A drain made while the stream is held hands its rows out
// under that stretch and holds it, so a later drain in the same stretch does
// not offer them again: a coordinator that is still attached is not handed
// its own rows twice. A row is offered again by the first drain after the
// stretch it was handed out under is no longer the one open, which is three
// cases:
//
//   - The coordinator went away: its stream ended, the process holding it
//     died with it or was stopped, and the next drain, by a coordinator that
//     attached again or by one that only polls, is offered the row.
//   - The service restarted: the instance in every earlier stretch's name is
//     not this service's, so nothing handed out before is held.
//   - A drain was made with no stream held at all: it is handed out under a
//     name no stretch holds, so the next drain is offered it again. A
//     coordinator that only polls is never attached, so it is handed a row
//     on every drain until it acknowledges it, which is the at-least-once
//     delivery it gets in place of an attachment the service can observe.
//
// # The wait
//
// wakes.wait blocks one call until the queue holds a row on offer or its
// bound elapses, so a coordinator that does not hold the stream still pays
// no turns for an idle fleet (P9's "supervision costs nothing until
// something is actionable", from the coordinator's side). It answers with a
// drain: every answer is store.DrainWakes on the terms above. A wait armed
// while a row is already on offer answers at once. When the bound elapses
// the queue is drained one last time, so an empty answer means the queue
// offered no row at the bound. Between those, the wait counts the rows on
// offer with store.OfferedWakes, a read, and drains only when one is there,
// so an idle wait writes nothing. A context that ended is checked before
// every drain, so a caller that went away is not answered with rows nobody
// will read; a caller that went away after the drain committed has had them
// handed out, and the rules above hand them out again.
//
// # What wakes the wait early
//
// wakeBell, rung by the supervisor loop's record after a write that
// appended a row, and nothing else. The bell is this service's memory and
// not a record: it carries no wake and nothing reads it as one, it only
// says the queue is worth reading again. The wait takes the bell's channel
// before each read, so a row appended after the read rings a channel the
// wait already holds and is not missed between the read and the block. A
// row that came on offer by any path that does not ring the bell, a stretch
// of the stream ending among them, is still found, by a read at least once
// per wakeWaitRecheck; that bounds how late such a row can be answered and
// is the whole of what the recheck is for.
//
// # A restart
//
// Neither the wait nor the stream keeps anything across calls: no cursor,
// no record of what was pushed or returned. A service that stops closes the
// connection, a wait's call fails as an operational failure and a stream
// ends, and a coordinator that arms either again against the next service
// reads the durable queue: every row still queued is on offer, handed out
// before the restart or not.
//
// # Residual gaps
//
//   - The service cannot see a coordinator, only its stream. A coordinator
//     whose stream outlives it, a listen process left running after its
//     host died, holds what was handed out under it until that process
//     ends; and two streams that overlap are one stretch, so a coordinator
//     that went away while another stream was held, a person's own or a
//     second coordinator's, is not noticed until every stream has closed.
//     Nothing here expires or heartbeats a listen attachment: a zombie
//     listen pins those handouts for as long as the process lives. That is
//     GAP-05's residual on delivery attachment, stated in docs/operators.md
//     rather than papered over with a clock that would invent detachment.
//   - A coordinator that holds the stream, drains and keeps running without
//     acknowledging what it acted on is never handed those rows again by
//     this service, and they keep their tasks in flight; that is the
//     acknowledgement left undone, which nothing here can make for it.
//   - A coordinator that only polls is handed a row it has not acknowledged
//     on every drain, so it sees a row again even when it did act; it is
//     acknowledging that stops that, and a wait it arms before
//     acknowledging answers at once.
//   - A subscription internal/ipc opened for a caller its admission then
//     refuses, such as a task's worker, or moves onto a domain's stream,
//     a subordinate's, is counted on the coordinator's stream for as long
//     as the admission takes, so it can begin or extend a stretch of the
//     coordinator's for that moment. That can only hold rows handed out in
//     that moment, and only until the stretch ends.
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
	p, err := s.admitted(req)
	if err != nil {
		return machine.Wakes{}, err
	}
	handout := s.wakeHandout(req, p)
	bound, limit, err := r.Bounds()
	if err != nil {
		return machine.Wakes{}, fmt.Errorf("%w: %w", ipc.ErrInvalidRequest, err)
	}
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
		// The handout is composed per read, because the stretch it is
		// made under is the one open now; the route is the placement's and
		// does not move.
		handout = s.wakeHandout(req, p)
		offered, err := s.store.OfferedWakes(ctx, handout.Held, handout.Route)
		if err != nil {
			return machine.Wakes{}, err
		}
		offeredDomain, err := s.store.OfferedDomainWakes(ctx, handout.Held, handout.Route)
		if err != nil {
			return machine.Wakes{}, err
		}
		if offered+offeredDomain > 0 {
			if ctx.Err() != nil {
				return machine.Wakes{}, ended()
			}
			wakes, err := s.drainBoth(ctx, limit, handout)
			if err != nil {
				return machine.Wakes{}, err
			}
			if len(wakes.Wakes)+len(wakes.DomainWakes) > 0 {
				return wakes, nil
			}
		}
		select {
		case <-rung:
		case <-recheck.C:
		case <-deadline.C:
			if ctx.Err() != nil {
				return machine.Wakes{}, ended()
			}
			return s.drainBoth(ctx, limit, s.wakeHandout(req, p))
		case <-ctx.Done():
			return machine.Wakes{}, ended()
		}
	}
}

// drainBoth is one drain of both shapes under one handout, the one both
// wakes.list and wakes.wait answer with: the task rows, then the domain
// rows, each bounded by limit, and the second none on a subordinate's
// route, handed out in one store transaction (store.DrainBoth), so a drain
// that fails or is cancelled between the two hands out nothing it does not
// return.
func (s *Service) drainBoth(ctx context.Context, limit int, handout store.WakeHandout) (machine.Wakes, error) {
	wakes, domainWakes, err := s.store.DrainBoth(ctx, limit, handout)
	if err != nil {
		return machine.Wakes{}, err
	}
	return machine.Wakes{Wakes: wakes, DomainWakes: domainWakes}, nil
}

// wakeHandout is what a coordinator's drain made now hands rows out as, on
// the terms above: to who the caller's placement says, subordinates.go's
// acknowledgerOf, on the route the placement gives, under the stretch of
// the caller's own wake stream that is open, holding that stretch, or,
// with no stretch open, under a name nothing holds and holding nothing.
// The caller's own stream is the coordinator's for the coordinator and its
// domain's for a subordinate, so a subordinate holding its stream is
// attached on the same terms as the coordinator and one that only polls is
// handed a row on every drain until it acknowledges it. A service with no
// wake publisher, which Open never builds and this package's internal
// tests build by hand, is never attached.
func (s *Service) wakeHandout(req ipc.Request, p placedPeer) store.WakeHandout {
	h := store.WakeHandout{To: s.acknowledgerOf(req, p), Under: s.instance + "/unattached", Route: wakeRouteOf(p)}
	stream, prefix := s.wakes, s.instance+"/stream/"
	if p.isSubordinate {
		stream, prefix = s.existingDomainWakeStream(p.subordinate.Domain), s.instance+"/domain/"+p.subordinate.Domain+"/stream/"
	}
	if stream == nil {
		return h
	}
	if number, open := stream.Attachment(); open {
		h.Under = fmt.Sprintf("%s%d", prefix, number)
		h.Held = []string{h.Under}
	}
	return h
}

// admitWakeStream is the handler's half of wakes.subscribe: internal/ipc
// has already refused a contained caller, and this refuses a task's worker
// on wakes.list's terms, admits the coordinator to the method's own
// publisher, the coordinator's stream, and admits a subordinate coordinator
// to its own domain's, which PRD section 9 grants it ("listing, awaiting,
// subscribing to, and acknowledging the wake queue, for its domain's rows
// alone"), by answering a choice naming the domain the placement derived,
// never one the request names. It returns the zero choice and nil to admit
// the coordinator, and the stream itself is internal/ipc's to open and
// pump.
func (s *Service) admitWakeStream(req ipc.Request) (ipc.StreamChoice, error) {
	p, err := s.admitted(req)
	if err != nil {
		return ipc.StreamChoice{}, err
	}
	if p.isSubordinate {
		return ipc.StreamChoice{Name: p.subordinate.Domain}, nil
	}
	return ipc.StreamChoice{}, nil
}

// domainWakeStream is the wake stream of a domain, made on the first ask,
// which internal/ipc attaches a subordinate's subscription to through
// ServerConfig.StreamFor once admitWakeStream chose the domain. A name
// home.ValidDomain refuses is refused, and a service closing makes none.
func (s *Service) domainWakeStream(domain string) (*ipc.Publisher, error) {
	if err := home.ValidDomain(domain); err != nil {
		return nil, fmt.Errorf("%w: %w", ipc.ErrInvalidRequest, err)
	}
	s.domainWakesMu.Lock()
	defer s.domainWakesMu.Unlock()
	if s.domainWakesClosed {
		return nil, fmt.Errorf("%w: the service is closing", ipc.ErrUnavailable)
	}
	if pub, ok := s.domainWakes[domain]; ok {
		return pub, nil
	}
	if s.domainWakes == nil {
		s.domainWakes = make(map[string]*ipc.Publisher)
	}
	pub, err := ipc.NewPublisher(ipc.PublisherConfig{})
	if err != nil {
		return nil, err
	}
	s.domainWakes[domain] = pub
	return pub, nil
}

// existingDomainWakeStream is the domain's wake stream if one was made,
// and nil otherwise: a push or a handout never makes one, since a stream
// nobody subscribed to has nobody to wake and holds no stretch.
func (s *Service) existingDomainWakeStream(domain string) *ipc.Publisher {
	s.domainWakesMu.Lock()
	defer s.domainWakesMu.Unlock()
	return s.domainWakes[domain]
}

// closeDomainWakeStreams closes every domain's wake stream, for Close.
func (s *Service) closeDomainWakeStreams() []error {
	s.domainWakesMu.Lock()
	streams := s.domainWakes
	s.domainWakes, s.domainWakesClosed = nil, true
	s.domainWakesMu.Unlock()
	var errs []error
	for _, pub := range streams {
		errs = append(errs, pub.Close())
	}
	return errs
}

// publishWake pushes a row the loop appended to the one wake stream whose
// drain would offer it, as ipc.TypeWakeQueued with the row's identifier as
// its revision: the coordinator's when the coordinator's route offers it,
// and otherwise the stream of the row's task's domain when that domain's
// subordinate's route offers it, each read through store.WakeRouted in the
// row's own task and delegation rows, so a row reaches at most one stream
// and it is the one whose drain is offered it (P14). A publication is not a
// record: a payload that cannot be encoded or published is logged, and the
// row is still queued for a drain to take; a route that cannot be read is
// logged and nothing is pushed, since a push that might be another
// coordinator's is the one error a stream cannot take back. A service with
// no wake publisher, which Open never builds and this package's internal
// tests build by hand, publishes nothing.
func (s *Service) publishWake(ctx context.Context, w store.Wake) {
	if s.wakes == nil {
		return
	}
	stream := s.wakes
	routed, err := s.store.WakeRouted(ctx, w.ID, wakeRouteOf(placedPeer{}))
	if err != nil {
		s.log.Printf("could not read whether wake %d of task %s is the coordinator's, so it is not pushed: %v", w.ID, w.TaskID, err)
		return
	}
	if !routed {
		scopes, err := s.store.WakeScopes(ctx, []int64{w.ID})
		if err != nil {
			s.log.Printf("could not read the domain of wake %d of task %s, so it is not pushed: %v", w.ID, w.TaskID, err)
			return
		}
		domain := scopes[w.ID].Domain
		if domain == "" {
			return
		}
		if stream = s.existingDomainWakeStream(domain); stream == nil {
			return
		}
		sub := placedPeer{subordinate: session.Placement{Domain: domain}, isSubordinate: true}
		if routed, err = s.store.WakeRouted(ctx, w.ID, wakeRouteOf(sub)); err != nil {
			s.log.Printf("could not read whether wake %d of task %s is domain %s's, so it is not pushed: %v", w.ID, w.TaskID, domain, err)
			return
		}
		if !routed {
			return
		}
	}
	payload, err := json.Marshal(w)
	if err != nil {
		s.log.Printf("could not encode wake %d of task %s: %v", w.ID, w.TaskID, err)
		return
	}
	if err := stream.Publish(ipc.Event{Type: ipc.TypeWakeQueued, Revision: uint64(w.ID), Payload: payload}); err != nil {
		s.log.Printf("could not push wake %d of task %s: %v", w.ID, w.TaskID, err)
	}
}
