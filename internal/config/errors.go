package config

import (
	"errors"
	"strconv"
)

// Errors a caller is expected to handle. Each is a typed result, never a
// warning that loading continues past.
var (
	// ErrInvalid is wrapped by every refusal that names a configuration key.
	// The wrapping KeyError says which key, which value, and why.
	ErrInvalid = errors.New("config: invalid configuration")
	// ErrMalformed is returned when a document is not well-formed JSON, so no
	// key can be named. Nothing in the document is applied.
	ErrMalformed = errors.New("config: malformed configuration document")
	// ErrUnknownOrigin is returned when a layer is parsed without stating the
	// origin its bytes came from. Trust is a property of where a document was
	// read from, and this package will not guess it.
	ErrUnknownOrigin = errors.New("config: a configuration layer must state its origin")
	// ErrUntrustedGlobal is returned when the global layer is offered with a
	// pushed origin. The global layer is the operator's own file in their
	// home; a pushed branch is never it.
	ErrUntrustedGlobal = errors.New("config: the global layer must have a trusted origin")
)

// KeyError reports one configuration key that could not be accepted. It names
// the key and the offending value, because a rule that fails without saying
// which line caused it sends the reader back to guessing. Every KeyError
// wraps ErrInvalid.
type KeyError struct {
	// Key names what was refused, in the dotted form the PRD section 10
	// schema uses, such as "fix_rounds.review". Only the refusal of a
	// repeated member indexes into a list, as in
	// "review.path_rules[0].guidance", because only that refusal knows where
	// in the document it stood. Every other fault inside a list names the
	// list key itself, so a caller matching on Key alone gets the list key
	// for those and reads Detail for where in the list it was.
	Key Key
	// Value is the offending value rendered as JSON, or empty when the
	// problem is the key itself rather than a value. A refusal about one
	// element of a list carries that element, or the one field of it the
	// refusal is about, and never the rest of the list. A refusal about the
	// list itself, such as one that exceeds its length limit or is not a list
	// at all, carries the list.
	Value string
	// Detail says what was wrong with it. A refusal raised while decoding one
	// element of a list says which element, counting from zero, either as the
	// phrase it opens with ("rule 3 has no guidance", "entry 7 has no
	// subject", "pattern 5 must be a string") or as the thing it is about
	// ("the guidance of rule 3 is 4097 characters and the limit is 4096").
	Detail string
}

// Error renders the refusal, naming the key and the value.
func (e *KeyError) Error() string {
	msg := "config: " + string(e.Key)
	if e.Value != "" {
		msg += " = " + e.Value
	}
	return msg + ": " + e.Detail
}

// Unwrap reports ErrInvalid, so errors.Is recognizes any key-level refusal.
func (e *KeyError) Unwrap() error { return ErrInvalid }

// DocumentError reports a document that could not be parsed at all. It wraps
// ErrMalformed.
type DocumentError struct {
	// Detail says what was wrong with the document.
	Detail string
	// Err is the underlying decoding error, if there was one.
	Err error
}

// Error renders the refusal.
func (e *DocumentError) Error() string {
	msg := "config: " + e.Detail
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap reports ErrMalformed, so errors.Is recognizes any document-level
// refusal. The underlying decoding error is reachable through Err.
func (e *DocumentError) Unwrap() error { return ErrMalformed }

func quote(s string) string { return strconv.Quote(s) }

func itoa(n int) string { return strconv.Itoa(n) }
