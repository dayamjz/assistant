package ipc

import "errors"

// Errors a caller is expected to handle. Test with errors.Is. Every one of
// them survives the wire: a failure the service reports is decoded back into
// an *Error whose Unwrap names the same sentinel, so a client matches the same
// value the handler returned.
//
// That holds by construction rather than by anyone remembering it. Each of
// these is taken out of codeRows by its code, so a sentinel that no code names
// cannot be declared here at all, and none of them can cross the wire as a
// category a caller cannot act on.
var (
	// ErrStreamClosed reports that a stream ended in an orderly way: the
	// consumer detached, the publisher closed, or the connection carrying it
	// went away.
	ErrStreamClosed = sentinelFor(CodeStreamClosed)
	// ErrSubscriberStalled reports that a subscription ended because its queue
	// held only events that may not be discarded and the consumer was not
	// reading. Attaching again reconciles.
	ErrSubscriberStalled = sentinelFor(CodeSubscriberStalled)
	// ErrUnknownMethod reports a method this build does not serve.
	ErrUnknownMethod = sentinelFor(CodeUnknownMethod)
	// ErrInvalidRequest reports a request this build serves but cannot read:
	// a malformed frame, a body that does not decode, or a stream method
	// called as a request.
	ErrInvalidRequest = sentinelFor(CodeInvalidRequest)
	// ErrUnidentifiedPeer reports that a request needing authority arrived on
	// a connection whose peer the kernel could not be asked about. It is a
	// refusal, not a warning: authority comes from the identification, so
	// without one there is nothing to decide on.
	ErrUnidentifiedPeer = sentinelFor(CodeUnidentifiedPeer)
	// ErrContained reports a request refused because its peer is running
	// inside an active validation stage. A validating agent may inspect, fix,
	// and return its own stage, and nothing else.
	ErrContained = sentinelFor(CodeContained)
	// ErrUnavailable reports that a fact the service needed to decide the
	// request could not be established. It fails closed, so this is a refusal
	// rather than a request served on an assumption.
	ErrUnavailable = sentinelFor(CodeUnavailable)
	// ErrInternal reports a handler failure with no more specific code.
	ErrInternal = sentinelFor(CodeInternal)
	// ErrFrameTooLarge reports a frame longer than the transport accepts. The
	// connection cannot continue, because the rest of the frame is
	// indistinguishable from the frames that follow it.
	ErrFrameTooLarge = sentinelFor(CodeFrameTooLarge)
	// ErrClientClosed reports use of a client whose connection has been closed.
	ErrClientClosed = sentinelFor(CodeClientClosed)
	// ErrPayloadTooLarge reports an event refused at the publisher because its
	// payload is past the bound a producer's projection must stay inside. It
	// is a producer's own bug rather than anything about a subscriber, so it
	// is reported to whoever published and nothing is delivered.
	ErrPayloadTooLarge = sentinelFor(CodePayloadTooLarge)
	// ErrInvalidPayload reports an event refused at the publisher because its
	// payload is not JSON. A payload travels as written, so one that cannot be
	// encoded could not have reached anybody, and it is refused where it was
	// written rather than ending some consumer's stream later.
	ErrInvalidPayload = sentinelFor(CodeInvalidPayload)
	// ErrEventUndeliverable reports that a stream ended because an event that
	// may not be discarded could not be put in a frame on that connection. It
	// is not ErrSubscriberStalled: the consumer was keeping up, and the event
	// itself is what could not travel. Attaching again reconciles, and an
	// event that keeps failing this way is a producer that did not bound its
	// projection.
	ErrEventUndeliverable = sentinelFor(CodeEventUndeliverable)
	// ErrConnectionBusy reports a request refused because the connection it
	// arrived on already holds as many open streams, or as many requests being
	// served, as one connection may. It is a refusal that names the limit and
	// the count rather than a silent drop, and a slot frees when a stream ends
	// or a call completes.
	ErrConnectionBusy = sentinelFor(CodeConnectionBusy)
)

// Code is the machine-readable category of a failure crossing the wire. It
// exists so a client can act on a failure without matching on prose.
type Code string

const (
	// CodeUnknownMethod maps to ErrUnknownMethod.
	CodeUnknownMethod Code = "unknown-method"
	// CodeInvalidRequest maps to ErrInvalidRequest.
	CodeInvalidRequest Code = "invalid-request"
	// CodeUnidentifiedPeer maps to ErrUnidentifiedPeer.
	CodeUnidentifiedPeer Code = "unidentified-peer"
	// CodeContained maps to ErrContained.
	CodeContained Code = "contained"
	// CodeUnavailable maps to ErrUnavailable.
	CodeUnavailable Code = "unavailable"
	// CodeSubscriberStalled maps to ErrSubscriberStalled. The service reports
	// it when a subscription ends because its consumer fell too far behind, so
	// a consumer can tell "attach again and reconcile" from "the service
	// broke" without reading the message.
	CodeSubscriberStalled Code = "subscriber-stalled"
	// CodeStreamClosed maps to ErrStreamClosed.
	CodeStreamClosed Code = "stream-closed"
	// CodePayloadTooLarge maps to ErrPayloadTooLarge.
	CodePayloadTooLarge Code = "payload-too-large"
	// CodeInvalidPayload maps to ErrInvalidPayload.
	CodeInvalidPayload Code = "invalid-payload"
	// CodeEventUndeliverable maps to ErrEventUndeliverable. A consumer that
	// receives it knows its stream ended over one event rather than over
	// anything it did, which is what tells it apart from a stalled
	// subscription.
	CodeEventUndeliverable Code = "event-undeliverable"
	// CodeConnectionBusy maps to ErrConnectionBusy.
	CodeConnectionBusy Code = "connection-busy"
	// CodeFrameTooLarge maps to ErrFrameTooLarge. It is what an answer that
	// does not fit in one frame comes back as, so a caller learns its request
	// was served and the answer could not be delivered.
	CodeFrameTooLarge Code = "frame-too-large"
	// CodeClientClosed maps to ErrClientClosed. The service never reports it,
	// because it is about a client's own connection; the row exists so every
	// sentinel this package defines has a code, and the claim that a sentinel
	// survives the wire holds for all of them rather than for most of them.
	CodeClientClosed Code = "client-closed"
	// CodeInternal maps to ErrInternal, and is what an unrecognized code
	// decodes to as well.
	CodeInternal Code = "internal"
)

// codeRows is the one owner of the failures this package names. A row is a
// code and the message its sentinel carries, and the exported sentinels are
// taken from here rather than declared beside it, so a failure cannot exist
// without a code that carries it across the wire.
//
// The order is the order codeFor tries: a failure takes the first row whose
// sentinel it wraps. CodeInternal is last because it is also the answer for a
// failure that names none of them.
var codeRows = []struct {
	// Code is the category on the wire.
	Code Code
	// Message is what the sentinel for that category says.
	Message string
}{
	{CodeUnknownMethod, "ipc: unknown method"},
	{CodeInvalidRequest, "ipc: invalid request"},
	{CodeUnidentifiedPeer, "ipc: peer could not be identified"},
	{CodeContained, "ipc: refused, caller is contained by an active validation stage"},
	{CodeUnavailable, "ipc: a fact this request depends on could not be established"},
	{CodeSubscriberStalled, "ipc: subscriber stalled"},
	{CodeEventUndeliverable, "ipc: event cannot be delivered on this connection"},
	{CodePayloadTooLarge, "ipc: event payload exceeds the publisher's bound"},
	{CodeInvalidPayload, "ipc: event payload is not JSON"},
	{CodeStreamClosed, "ipc: stream closed"},
	{CodeConnectionBusy, "ipc: connection is at its concurrency limit"},
	{CodeFrameTooLarge, "ipc: frame exceeds the size limit"},
	{CodeClientClosed, "ipc: client closed"},
	{CodeInternal, "ipc: internal error"},
}

// sentinels is the table indexed by code, built once. A duplicate code would
// make one row unreachable and would make the table disagree with itself about
// what a failure means, so building the index panics on one rather than letting
// the last row quietly win.
var sentinels = func() map[Code]error {
	m := make(map[Code]error, len(codeRows))
	for _, r := range codeRows {
		if _, dup := m[r.Code]; dup {
			panic("ipc: error table declares " + string(r.Code) + " twice")
		}
		m[r.Code] = errors.New(r.Message)
	}
	return m
}()

// sentinelFor returns the value a client matches a code against. A code with no
// row is a programming error and panics at build time rather than producing a
// sentinel that would cross the wire as something else.
func sentinelFor(c Code) error {
	s, ok := sentinels[c]
	if !ok {
		panic("ipc: no error row for code " + string(c))
	}
	return s
}

// codeFor classifies err for the wire. An error that matches no row is
// internal, which is the category that claims the least.
func codeFor(err error) Code {
	for _, r := range codeRows {
		if errors.Is(err, sentinels[r.Code]) {
			return r.Code
		}
	}
	return CodeInternal
}

// Error is a failure as it crosses the wire. It carries a code a client can
// act on and a message a client can report, and nothing else: a handler's
// wrapped chain does not travel, because the receiver could not act on the
// parts of it that name things it cannot see.
type Error struct {
	// Code is the category of the failure.
	Code Code `json:"code"`
	// Message says what happened, in a sentence a caller can report as it is.
	Message string `json:"message"`
	// Method is the method that failed, when the failure was about one.
	Method Method `json:"method,omitempty"`
}

// Error renders the code and the message.
func (e *Error) Error() string {
	if e.Method != "" {
		return string(e.Method) + ": " + e.Message + " (" + string(e.Code) + ")"
	}
	return e.Message + " (" + string(e.Code) + ")"
}

// Unwrap resolves the code to the sentinel a caller matches with errors.Is. A
// code this build does not know resolves to ErrInternal, so an unrecognized
// failure is still a failure rather than an error that matches nothing.
func (e *Error) Unwrap() error {
	if s, ok := sentinels[e.Code]; ok {
		return s
	}
	return ErrInternal
}

// newError builds the wire form of err for method m.
func newError(m Method, err error) *Error {
	var wire *Error
	if errors.As(err, &wire) {
		out := *wire
		if out.Method == "" {
			out.Method = m
		}
		return &out
	}
	return &Error{Code: codeFor(err), Message: err.Error(), Method: m}
}
