package machine

import (
	"encoding/json"
	"fmt"
	"io"
)

// Code is the exit status one invocation ends with. PRD section 9 gives the
// machine interface three meanings and says they are never overloaded, so this
// set has three members and nothing maps two situations onto one of them.
type Code int

const (
	// ExitOK is success, and it is also every normal decision point. A run
	// that stopped to ask something has not failed: the answer goes back
	// through the same surface, and a driving agent that treated a decision as
	// a failure would stop driving exactly when it should carry on.
	ExitOK Code = 0
	// ExitFailure is an operational failure: something the command was asked
	// to do could not be done. A refused request, an unreachable service, and
	// a run that ended without a verdict are all this.
	ExitFailure Code = 1
	// ExitUsage is incorrect usage: the command line itself was wrong. It is
	// separate from ExitFailure so that a driving agent can tell a request it
	// got wrong from a request the system could not serve, which are fixed in
	// entirely different ways.
	ExitUsage Code = 2
)

// String renders the code's meaning, for a diagnostic that wants to say which
// of the three it is rather than print a number.
func (c Code) String() string {
	switch c {
	case ExitOK:
		return "ok"
	case ExitFailure:
		return "failure"
	case ExitUsage:
		return "usage"
	default:
		return fmt.Sprintf("code(%d)", int(c))
	}
}

// Failure is what a command writes on standard output when it could not do
// what it was asked. It is a structured answer like any other, because a
// driving agent that has to parse prose to learn what happened is one that
// will parse it wrong.
type Failure struct {
	// Error is what went wrong, in a sentence a caller can report as it is.
	Error string `json:"error"`
	// Code is the machine-readable category, when the failure came from the
	// service and carried one. It is ipc.Code's vocabulary, carried as written
	// so this package does not become a second owner of it.
	Code string `json:"code,omitempty"`
	// NextAction is what to do about it, which PRD section 9 requires of a
	// failure rather than leaving it silent.
	NextAction string `json:"next_action,omitempty"`
}

// Encoder writes structured answers to standard output, one document per line.
//
// The encoding is JSON, whose string form escapes every control character, so
// text an agent wrote arrives as characters a reader can see rather than as an
// instruction to the terminal reading it. That is PRD section 9's requirement
// that control characters are escaped visibly rather than emitted raw, and it
// is a property of the encoding rather than a pass this package makes over the
// text.
//
// One document per line is what makes a stream of them readable: a consumer of
// assistant watch reads one event per line without needing an incremental
// parser.
type Encoder struct {
	enc *json.Encoder
}

// NewEncoder writes to w, which is standard output for a command line.
// Progress belongs on standard error and is not this encoder's.
func NewEncoder(w io.Writer) *Encoder {
	enc := json.NewEncoder(w)
	// HTML escaping would rewrite three ordinary characters into escape
	// sequences that a reader then has to undo. Control characters are escaped
	// either way, which is the requirement; these three are not control
	// characters and reading a diff or a finding with them rewritten is worse.
	enc.SetEscapeHTML(false)
	return &Encoder{enc: enc}
}

// Encode writes one answer. A value that cannot be encoded is an error rather
// than a partial line, because a consumer reading one document per line cannot
// recover from half of one.
func (e *Encoder) Encode(v any) error {
	if err := e.enc.Encode(v); err != nil {
		return fmt.Errorf("machine: writing the answer: %w", err)
	}
	return nil
}
