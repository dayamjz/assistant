package gate

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"unicode"
)

// branchRefPrefix is the reference namespace a branch lives in. It is here
// rather than at the one place that reads it because RefUpdate.Branch is what
// decides whether an update is a branch, and a second spelling of the prefix
// would be a second answer to that.
const branchRefPrefix = "refs/heads/"

// RefUpdate is one reference update line git puts on a gate hook's standard
// input: the object the reference stands at, the object the push moves it to,
// and the reference's full name.
//
// It is this package's shape because this package defines the hook protocol.
// The hooks installed here are what put these lines in front of the
// agent-facing command, and hooks.go states the requirement that command
// answers to, so per PRD principle P14 this is the only place they are read.
//
// The two object names are carried as written. What they mean is read through
// Created, Deleted and Branch rather than off the fields, because the meaning
// of "no object on this side" is an all-zero name whose width is the
// repository's hash algorithm rather than a constant this package could pin.
type RefUpdate struct {
	// Old is the object the reference stands at, all zeros when the push
	// creates the reference.
	Old string `json:"old"`
	// New is the object the push moves the reference to, all zeros when the
	// push deletes the reference.
	New string `json:"new"`
	// Ref is the reference's full name, such as refs/heads/work.
	Ref string `json:"ref"`
}

// Created reports that the push brings this reference into existence.
func (u RefUpdate) Created() bool { return isZeroObject(u.Old) }

// Deleted reports that the push removes this reference.
func (u RefUpdate) Deleted() bool { return isZeroObject(u.New) }

// Branch is the branch this update is about, empty when the reference is not a
// branch. A tag, a note, and anything else outside refs/heads/ all answer
// empty, so a caller that acts on branches says so by asking for one.
func (u RefUpdate) Branch() string {
	name, ok := strings.CutPrefix(u.Ref, branchRefPrefix)
	if !ok {
		return ""
	}
	return name
}

// String renders the update the way git wrote it, which is what a message
// about one reference should quote.
func (u RefUpdate) String() string { return u.Old + " " + u.New + " " + u.Ref }

// Validate reports what stops this from being an update git could have
// written: an object name on either side that is not one, no reference name,
// or a reference name carrying a space, a tab, or another control character.
//
// It is exported because ParseRefUpdates is not the only way an update reaches
// a decision. One travels to the background service as a value, and the rule
// for what a well-formed update is has to be one rule asked twice rather than
// a strict reading at the parser and a weaker one behind it. The reference
// name's whitespace is checked here for that reason: the parser splits a line
// on single spaces and so cannot yield a name containing one, and a value
// handed straight to the service would otherwise reach a decision by a weaker
// rule than a parsed line does.
//
// This is not git's reference-name check and does not claim to be. It refuses
// the characters that would make a line ambiguous and nothing else, so the
// rules git applies beyond that - "..", a trailing ".lock", "@{", a leading
// dot in a component, and the punctuation git reserves - are unchecked, and a
// name passing this may still be one git would reject. The object names' width
// is unchecked too, for the reason ParseRefUpdates gives.
func (u RefUpdate) Validate() error {
	for _, object := range []struct{ what, name string }{{"old", u.Old}, {"new", u.New}} {
		if !isObjectName(object.name) {
			return fmt.Errorf("%w: %q is not an object name, and it is this update's %s object",
				ErrMalformedRefUpdate, object.name, object.what)
		}
	}
	if u.Ref == "" {
		return fmt.Errorf("%w: the update names no reference", ErrMalformedRefUpdate)
	}
	for _, r := range u.Ref {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Errorf("%w: the reference name %q carries %q, which git could not have written "+
				"on an update line", ErrMalformedRefUpdate, u.Ref, r)
		}
	}
	return nil
}

// ParseRefUpdates reads the reference update lines a gate hook is given on
// standard input.
//
// Every line has to parse. A line that does not is ErrMalformedRefUpdate and
// no updates are returned, because admission decides whether a push proceeds
// and a decision made over the lines that happened to parse is a decision
// about a push nobody described. An input with no lines at all is no updates
// and no error: nothing was described, which is a state the caller answers
// for rather than a malformed description.
//
// What it checks is the shape git writes: three fields separated by single
// spaces, and then whatever Validate asks of the three, which is the same rule
// the service applies to an update handed to it as a value. The object names'
// width is not checked, because which width is right is the repository's hash
// algorithm and this package does not read that; a name of the wrong width for
// the gate it arrived from resolves to no object there, which is the gate's
// answer and not this one's.
func ParseRefUpdates(r io.Reader) ([]RefUpdate, error) {
	if r == nil {
		return nil, nil
	}
	var updates []RefUpdate
	lines := bufio.NewScanner(r)
	for lines.Scan() {
		update, err := parseRefUpdate(lines.Text())
		if err != nil {
			return nil, err
		}
		updates = append(updates, update)
	}
	if err := lines.Err(); err != nil {
		return nil, fmt.Errorf("%w: reading the reference update lines: %w", ErrMalformedRefUpdate, err)
	}
	return updates, nil
}

// parseRefUpdate reads one line, or says what is wrong with it. The line is
// quoted in every refusal, because the operator reading it is looking at a
// push that git accepted the shape of and this one did not.
func parseRefUpdate(line string) (RefUpdate, error) {
	fields := strings.Split(line, " ")
	if len(fields) != 3 {
		return RefUpdate{}, fmt.Errorf("%w: %q has %d space-separated fields, want the old object, "+
			"the new object, and the reference name", ErrMalformedRefUpdate, line, len(fields))
	}
	update := RefUpdate{Old: fields[0], New: fields[1], Ref: fields[2]}
	if err := update.Validate(); err != nil {
		return RefUpdate{}, fmt.Errorf("%w: %q: %w", ErrMalformedRefUpdate, line, err)
	}
	return update, nil
}

// isObjectName reports whether s is written the way git writes an object name:
// a non-empty run of lowercase hexadecimal digits.
func isObjectName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
		default:
			return false
		}
	}
	return true
}

// isZeroObject reports whether s is the all-zero object name git writes for
// the side of an update that does not exist. It is written as a property of
// the name rather than as a comparison with a constant, so it answers for a
// repository on either hash algorithm without this package reading which.
func isZeroObject(s string) bool {
	if s == "" {
		return false
	}
	return strings.Trim(s, "0") == ""
}
