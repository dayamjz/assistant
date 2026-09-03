package ipc

import "errors"

// Errors a caller is expected to handle. Test with errors.Is. Every one of
// them survives the wire: a failure the service reports is decoded back into
// an *Error whose Unwrap names the same sentinel, so a client matches the same
// value the handler returned.
var (
	// ErrStreamClosed reports that a stream ended in an orderly way: the
	// consumer detached, the publisher closed, or the connection carrying it
	// went away.
	ErrStreamClosed = errors.New("ipc: stream closed")
	// ErrSubscriberStalled reports that a subscription ended because its queue
	// held only events that may not be discarded and the consumer was not
	// reading. Attaching again reconciles.
	ErrSubscriberStalled = errors.New("ipc: subscriber stalled")
	// ErrUnknownMethod reports a method this build does not serve.
	ErrUnknownMethod = errors.New("ipc: unknown method")
	// ErrInvalidRequest reports a request this build serves but cannot read:
	// a malformed frame, a body that does not decode, or a stream method
	// called as a request.
	ErrInvalidRequest = errors.New("ipc: invalid request")
	// ErrUnidentifiedPeer reports that a request needing authority arrived on
	// a connection whose peer the kernel could not be asked about. It is a
	// refusal, not a warning: authority comes from the identification, so
	// without one there is nothing to decide on.
	ErrUnidentifiedPeer = errors.New("ipc: peer could not be identified")
	// ErrContained reports a request refused because its peer is running
	// inside an active validation stage. A validating agent may inspect, fix,
	// and return its own stage, and nothing else.
	ErrContained = errors.New("ipc: refused, caller is contained by an active validation stage")
	// ErrUnavailable reports that a fact the service needed to decide the
	// request could not be established. It fails closed, so this is a refusal
	// rather than a request served on an assumption.
	ErrUnavailable = errors.New("ipc: a fact this request depends on could not be established")
	// ErrInternal reports a handler failure with no more specific code.
	ErrInternal = errors.New("ipc: internal error")
	// ErrFrameTooLarge reports a frame longer than the transport accepts. The
	// connection cannot continue, because the rest of the frame is
	// indistinguishable from the frames that follow it.
	ErrFrameTooLarge = errors.New("ipc: frame exceeds the size limit")
	// ErrClientClosed reports use of a client whose connection has been closed.
	ErrClientClosed = errors.New("ipc: client closed")
	// ErrConnectionBusy reports a request refused because the connection it
	// arrived on already holds as many open streams, or as many requests being
	// served, as one connection may. It is a refusal that names the limit and
	// the count rather than a silent drop, and a slot frees when a stream ends
	// or a call completes.
	ErrConnectionBusy = errors.New("ipc: connection is at its concurrency limit")
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

// codeRows is the one owner of which code names which sentinel. A sentinel
// without a row would cross the wire as some other category, and a caller
// would be left matching on prose, so every sentinel in this package has one.
//
// The order is the order codeFor tries: a failure takes the first row whose
// sentinel it wraps. CodeInternal is last because it is also the answer for a
// failure that names none of them.
var codeRows = []struct {
	// Code is the category on the wire.
	Code Code
	// Sentinel is the value a client matches that category against.
	Sentinel error
}{
	{CodeUnknownMethod, ErrUnknownMethod},
	{CodeInvalidRequest, ErrInvalidRequest},
	{CodeUnidentifiedPeer, ErrUnidentifiedPeer},
	{CodeContained, ErrContained},
	{CodeUnavailable, ErrUnavailable},
	{CodeSubscriberStalled, ErrSubscriberStalled},
	{CodeStreamClosed, ErrStreamClosed},
	{CodeConnectionBusy, ErrConnectionBusy},
	{CodeFrameTooLarge, ErrFrameTooLarge},
	{CodeClientClosed, ErrClientClosed},
	{CodeInternal, ErrInternal},
}

// sentinels indexes the table by code. A duplicate code would make one row
// unreachable and would make the table disagree with itself about what a
// failure means, so building the index panics on one rather than letting the
// last row quietly win.
var sentinels = func() map[Code]error {
	m := make(map[Code]error, len(codeRows))
	for _, r := range codeRows {
		if _, dup := m[r.Code]; dup {
			panic("ipc: error table declares " + string(r.Code) + " twice")
		}
		m[r.Code] = r.Sentinel
	}
	return m
}()

// codeFor classifies err for the wire. An error that matches no row is
// internal, which is the category that claims the least.
func codeFor(err error) Code {
	for _, r := range codeRows {
		if errors.Is(err, r.Sentinel) {
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
