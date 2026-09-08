package forge

import (
	"errors"
	"fmt"
	"strings"
)

// Errors a caller is expected to handle. Test with errors.Is.
var (
	// ErrRefused matches every refusal this package produces. A refusal is a
	// typed result carrying what the provider could not do and why; use
	// errors.As with **Refusal to read it.
	ErrRefused = errors.New("forge: the provider did not answer the request")
	// ErrInvalidArgument is returned when an argument would be read by the
	// provider command as an option, is empty where a value is required, or
	// carries a byte that cannot survive the wire. The refusal happens before
	// the provider is invoked.
	ErrInvalidArgument = errors.New("forge: argument is not usable as a provider argument")
)

// Reason says why a request did not produce an answer. Every reason is a fact
// about what happened, never a preference, and every one of them is a result
// the caller has to handle rather than a state it may read as an empty answer.
type Reason string

const (
	// ReasonUnavailable is the provider did not answer: its command line could
	// not be executed, the call ended before the provider reported anything,
	// the process ended without reporting a status of its own because
	// something else ended it, or its output was still held open when the
	// grace period ran out and so may be short of what it wrote. In none of
	// them did the provider answer the request, and in the first three nothing
	// at all is known about it.
	ReasonUnavailable Reason = "unavailable"
	// ReasonUnauthenticated is the provider reported that this caller is not
	// authenticated for the repository. It is distinct from ReasonUnavailable
	// because the remedy is different and a caller reports it differently.
	ReasonUnauthenticated Reason = "unauthenticated"
	// ReasonRejected is the provider ran, answered, and refused the request.
	// The provider's own message is in Detail.
	ReasonRejected Reason = "rejected"
	// ReasonMalformed is the provider answered with something this package
	// cannot read as the shape the request asked for, or answered in a way
	// that contradicts the request it was given. The answer is discarded
	// rather than partially interpreted.
	//
	// A refusal carrying this reason has no Cause. A decoder reports on the
	// text it was reading, and that text is the provider's, so keeping it out
	// of the error chain leaves one route for provider text into a refusal:
	// Detail, which is redacted.
	ReasonMalformed Reason = "malformed-response"
	// ReasonOversizeAnswer is the provider answered with more than the
	// configured output bound allows, so the answer was discarded rather than
	// read in part. It is separate from ReasonMalformed because the answer may
	// have been perfectly well formed and because the remedy is different: a
	// caller raises the bound or reports, and reading again at the same bound
	// cannot help.
	ReasonOversizeAnswer Reason = "oversize-answer"
	// ReasonWrongRepository is the provider reports that the repository
	// specifier this adapter was given names a different repository. It is
	// separate from ReasonRejected because the provider refused nothing: it
	// answered, and the answer was about somewhere the run did not ask about.
	// A caller re-points the run rather than retrying.
	ReasonWrongRepository Reason = "wrong-repository"
	// ReasonAmbiguous is the provider answered with more than one thing where
	// the request identifies one, such as a branch carrying two open pull
	// requests. Picking one would be a guess, so this package refuses and
	// names the count.
	ReasonAmbiguous Reason = "ambiguous"
)

// Refusal reports a request that produced no answer. It is the result a caller
// handles: there is no pull request and no check list inside it, so a caller
// that dropped the error holds nothing it can act on.
//
// Detail carries provider text that has passed through the Redactor supplied
// at construction. The argument vector is deliberately absent: it is not
// needed to act on a refusal, and leaving it out is one fewer route by which a
// value this package was handed could reach a log.
type Refusal struct {
	// Reason is the category of refusal.
	Reason Reason
	// Op is the operation that did not complete, such as "checks".
	Op string
	// Detail states what happened, in a sentence a caller can report without
	// adding to it. It has been redacted.
	Detail string
	// Cause is the underlying failure, when the refusal came from one. It is
	// the error from starting or waiting on the provider command, or the
	// error of a context that ended the call, or a decoding failure.
	Cause error
}

// Error renders the operation, the reason, and the detail.
func (r *Refusal) Error() string {
	var b strings.Builder
	b.WriteString("forge: " + r.Op + " (" + string(r.Reason) + ")")
	if r.Detail != "" {
		b.WriteString(": " + r.Detail)
	}
	return b.String()
}

// Unwrap makes every refusal match ErrRefused, so a caller that only needs to
// know the request did not produce an answer does not have to enumerate the
// reasons, and exposes the failure it came from alongside it, so errors.Is
// against a cause such as context.DeadlineExceeded still matches.
func (r *Refusal) Unwrap() []error {
	if r.Cause == nil {
		return []error{ErrRefused}
	}
	return []error{ErrRefused, r.Cause}
}

// argumentError reports an argument refused before the provider was invoked.
// It wraps ErrInvalidArgument and names which argument and why.
//
// value has been redacted by whoever built the error, and is empty where the
// argument is one that must not be echoed at all. An empty value is rendered
// as no value rather than as an empty one, so a refusal never suggests the
// caller passed something blank when it did not.
type argumentError struct {
	what   string
	value  string
	reason string
}

func (e *argumentError) Error() string {
	if e.value == "" {
		return "forge: " + e.what + " " + e.reason
	}
	return fmt.Sprintf("forge: %s %q %s", e.what, e.value, e.reason)
}

// Unwrap makes every argument refusal match ErrInvalidArgument.
func (e *argumentError) Unwrap() error { return ErrInvalidArgument }
