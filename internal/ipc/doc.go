// Package ipc is the protocol between the command line, an agent driving the
// machine interface, and the background service. PRD section 8 gives it the
// local endpoint, its methods, and its bounded event stream, and its interface
// is a request with an answer plus a subscription to a stream of events.
//
// It carries calls and events. It decides nothing about what a call means: the
// method table says what exists and who may reach it, and a Handler supplied by
// the service answers.
//
// # Event delivery is the part that is easy to get subtly wrong
//
// A stream is bounded, so what happens when a consumer falls behind is a
// decision this package makes rather than an accident it suffers. Every event
// has a class, and the class is the whole of the policy.
//
// Publishing never waits for a consumer. Publish moves an event into each
// subscriber's fixed queue and returns; it never holds a lock across a
// consumer's read, and it has no path that blocks on one. A consumer that
// stopped reading entirely, including one whose process is gone, cannot slow
// the run being reported on.
//
// What that does not cover is worth naming. A consumer that stops reading its
// own connection does stall that connection: the goroutine writing events to it
// blocks in the write, and the replies to that consumer's own calls queue up
// behind it. Nothing beyond that one connection is affected, and the queue in
// front of the write is what turns the stall into a gap rather than into
// pressure on the producer.
//
// Activity is discarded first. Activity is progress, and nothing a consumer
// holds depends on it. State is a delta to something a consumer holds, and
// control is about the channel itself. An event type this build does not
// recognize is classified as state, so a type added by a newer peer is retained
// rather than silently treated as noise.
//
// Anything discarded collapses into one sticky gap marker, and the marker is
// delivered ahead of whatever the queue holds. A consumer therefore learns it
// is missing something before it acts on what follows, which is the order that
// matters: the other way round, it would apply a delta first and find out
// afterwards that its base was wrong.
//
// Every state event carries a monotonic revision, and Cursor is the consumer
// half of that. A delta is applied only when its revision is newer than what
// the consumer holds, so a repeat or a delivery that arrived out of order
// cannot move a consumer backwards. A cursor opens gapped and a subscription
// opens gapped, so a first attach and a reattach both reconcile from a full
// read before anything is applied.
//
// # Where state may be discarded, and why that is not the exception it looks like
//
// Bounded, never blocking the executor, and never evicting state is a trilemma,
// so the property that holds is that state is never silently dropped: activity
// is discarded first, a state event may be evicted only when that eviction is
// collapsed into the sticky gap marker, and control is never discarded. PRD
// section 8 states it in those words, and Subscription implements it in that
// order.
//
// Discarding a queued state event looks like the rule quietly relaxed, so it is
// worth being exact about why it is not. A discard raises the gap, and a gapped
// consumer must read the state back in full before applying another delta. That
// read supersedes every delta still queued, so the queued deltas are not
// information the consumer loses. This is the collapse into a marker the design
// calls for, not a quiet discard.
//
// Control has no such recovery, because no read gives a consumer back an event
// about the channel. So when a queue holds only control events and something
// undroppable arrives, the subscription ends with ErrSubscriberStalled rather
// than discarding one or growing without bound. The consumer sees the stream
// end, and attaching again reconciles. The residual cost is real and named: a
// consumer that hits this loses its stream rather than a log line, and reaching
// it takes more queued control events than the queue is deep, which the current
// control vocabulary makes unlikely and which this package cannot promise about
// a caller that adds to it.
//
// # Peer identification is authority
//
// PRD section 9 contains an agent that is running inside a validation stage: it
// may inspect, fix, and return its own stage, and nothing else. That decision
// rests on who is on the other end of the connection, and this package asks the
// operating system for it rather than the caller. Credentials is read off the
// socket, and the thing that buys is verifiable here rather than somewhere
// else: no frame this package defines has a field a caller could put a process
// identifier or a user in, so nothing a caller sends can reach the decision.
//
// What identification establishes is bounded, and the bound is real. It
// describes the peer as it was when the connection was made, which is the only
// thing a connection can be about, and a process identifier is reusable once
// its process is gone. A platform this build has no read for produces an
// unidentified peer rather than a guess, and every restricted method refuses
// there.
//
// MarkerVar is the other half of the same rule, stated as the thing it is not.
// A process inside a stage carries an environment marker, a client may send it,
// and a handler may report it. It is never consulted to allow or refuse
// anything, because a caller writes its own environment. The type split is what
// keeps that from eroding: Marker and Credentials are different types, and a
// Peer will not produce credentials without an error to handle when the kernel
// could not be asked.
//
// The containment question itself is not answered here. Ancestry is the seam,
// and resolving a process tree and knowing which runs are active are both state
// this package does not own, so nothing in this repository implements it yet.
// A server cannot be built without one: a default would serve restricted
// methods to a validating agent, which is the failure the rule exists to
// prevent. Where a fact cannot be established, the request is refused. An
// unidentified peer, and an ancestry that could not answer, both refuse.
//
// # What one connection may hold at once
//
// A connection holds a bounded number of open streams and a bounded number of
// requests being served, because the method that opens a stream is open to any
// identified caller and an unbounded resource reachable without authority is
// the hazard the frame limit already exists for. Exceeding either bound is a
// refusal naming the limit and the current count, never a silent drop, and a
// slot frees when a stream ends or a call is answered. The bound on requests is
// applied without waiting: the goroutine that would wait is the one reading the
// connection, so waiting would stop the connection rather than pace it.
//
// # What this package does not do
//
// It does not open the socket, hold the home's lock, or decide when the service
// is ready. Serve takes a listener that a caller made, and readiness is a real
// answer to MethodHealth from a handler, per PRD section 8.
//
// It does not own any payload's shape. An event body and a method's parameters
// and result travel as written, so nothing here becomes a second owner of a
// record another package defines, per P14.
//
// It does not bound what a producer puts in a payload. PRD section 8 makes the
// full log the authority and what travels a bounded projection of it, and
// producing that projection belongs to whatever writes the event. A frame past
// the size limit is refused rather than written, because the receiver's only
// recovery from an over-long frame is to drop the connection. An event that
// cannot be put in a frame is therefore a discard rather than the end of the
// stream: it raises the gap the consumer reconciles from, and the stream
// carries on. An answer to a request that cannot be put in a frame is reported
// to the caller as that refusal, because the alternative is a caller waiting
// for an answer that is never coming.
//
// It does not cancel work that is already running. A Call whose context ended
// stops waiting for the answer; the handler keeps its connection's context, so
// closing the client is what ends it.
//
// It does not retry or reconnect. A client whose connection ended reports it,
// and a consumer that attaches again reconciles by the same rule as a first
// attach.
package ipc
