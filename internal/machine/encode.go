package machine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"unicode"
	"unicode/utf8"
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
// The encoding is JSON, and every control character in it is written as its
// \uXXXX escape, so text an agent wrote arrives as characters a reader can see
// rather than as an instruction to the terminal reading it. That is PRD
// section 9's requirement that control characters are escaped visibly rather
// than emitted raw.
//
// It is a pass this package makes rather than a property of the encoding.
// encoding/json escapes U+0000 through U+001F and writes U+007F and the C1
// range U+0080 through U+009F as they stand, and U+009B is the single-byte
// control sequence introducer, so a document it wrote unaided can carry an
// escape sequence to a consumer that pipes it to a terminal. The predicate
// here is unicode.IsControl, which is the one the human rendering uses, so the
// two surfaces are safe by one rule rather than by two that can drift.
//
// One document per line is what makes a stream of them readable: a consumer of
// assistant watch reads one event per line without needing an incremental
// parser.
type Encoder struct {
	w io.Writer
}

// NewEncoder writes to w, which is standard output for a command line.
// Progress belongs on standard error and is not this encoder's.
func NewEncoder(w io.Writer) *Encoder { return &Encoder{w: w} }

// Encode writes one answer. A value that cannot be encoded is an error rather
// than a partial line, because a consumer reading one document per line cannot
// recover from half of one, and the document is built whole before any of it
// is written for the same reason.
func (e *Encoder) Encode(v any) error {
	var document bytes.Buffer
	enc := json.NewEncoder(&document)
	// HTML escaping would rewrite three ordinary characters into escape
	// sequences that a reader then has to undo. They are not control
	// characters, so nothing about the escaping below rests on them, and
	// reading a diff or a finding with them rewritten is worse.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("machine: writing the answer: %w", err)
	}
	line := append(escapeControls(bytes.TrimSuffix(document.Bytes(), []byte("\n"))), '\n')
	if _, err := e.w.Write(line); err != nil {
		return fmt.Errorf("machine: writing the answer: %w", err)
	}
	return nil
}

// escapeControls rewrites every control character in an encoded document as
// its \uXXXX escape, which is what makes the claim above true of the whole of
// unicode.IsControl rather than only of what encoding/json escapes.
//
// It runs over the encoded document rather than over the values that went into
// it, which is what lets one pass cover every string in every shape this
// package writes. That is safe because encoding/json emits no raw control
// character outside a string: the structural tokens are ASCII punctuation, and
// what it did escape is already a backslash sequence rather than the character
// itself, so nothing here is escaped twice. The line ending this package adds
// is added after this runs.
//
// A \uXXXX escape is ordinary JSON, so a consumer decodes the identical string
// it would have decoded from the unescaped document.
func escapeControls(document []byte) []byte {
	if !bytes.ContainsFunc(document, unicode.IsControl) {
		return document
	}
	out := make([]byte, 0, len(document))
	for _, r := range string(document) {
		if unicode.IsControl(r) {
			out = append(out, fmt.Sprintf(`\u%04x`, r)...)
			continue
		}
		out = utf8.AppendRune(out, r)
	}
	return out
}
