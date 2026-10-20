package session

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/dayamjz/assistant/internal/home"
)

// ErrReference is the class every refused decode belongs to: the text is not
// a reference this package wrote.
var ErrReference = errors.New("session: not a session reference")

// referencePrefix names the format of a worker's reference, and is the one
// thing a later format changes. A reference with another prefix is refused
// rather than read, except subordinatePrefix.
const referencePrefix = "terminal1"

// subordinatePrefix names the format of a subordinate coordinator's
// reference: the same five fields, with the domain in the task's place. The
// prefix is what tells the two apart, so a domain and a task identifier that
// happened to spell the same text could not be confused, and a reader that
// knows one format alone refuses the other rather than reading it.
const subordinatePrefix = "subordinate1"

// homeMarkerLength is the length of the home marker in hex characters: the
// first 128 bits of the SHA-256 of the home root, which is enough to tell two
// homes on one machine apart and short enough to read.
const homeMarkerLength = 32

// Reference is the identity of one terminal session: which home and task,
// or domain, it belongs to, and which process it is, by identifier and
// start time. It is what the caller records as the attempt's session
// reference, and the one thing Probe reads. Exactly one of Task and Domain
// is set: a worker's reference names its task and a subordinate
// coordinator's names its domain, and Encode refuses one naming both or
// neither rather than choosing, so no owner is dropped on the way to a
// record.
type Reference struct {
	// Home is HomeMarker of the home root the session was created under.
	Home string
	// Task is the task identifier, as home.ValidTaskID accepts it, and
	// empty for a subordinate coordinator's reference.
	Task string
	// Domain is the domain, as home.ValidDomain accepts it, for a
	// subordinate coordinator's reference, and empty for a worker's.
	Domain string
	// PID is the process identifier of the agent process.
	PID int
	// Start is the platform's reading of the process's start time, compared
	// for equality only, and zero where the platform could not read one.
	Start uint64
}

// Subordinate reports whether the reference names a subordinate
// coordinator's session rather than a worker's.
func (r Reference) Subordinate() bool { return r.Domain != "" }

// HomeMarker is the marker a reference carries for a home: a hash of its
// root, so a reference names its home without spelling a path.
func HomeMarker(h *home.Home) string {
	sum := sha256.Sum256([]byte(h.Root()))
	return hex.EncodeToString(sum[:])[:homeMarkerLength]
}

// Encode encodes the reference in the closed format ParseReference reads:
// the format name, the home marker, the task identifier or the domain, the
// process identifier and the start time, joined by colons. A reference with
// a Domain and no Task encodes under subordinatePrefix and one with a Task
// and no Domain under referencePrefix. One naming both, or neither, is
// refused with an error wrapping ErrReference: it is the identity of no
// session this package creates, and encoding either half would record an
// owner the reference does not have.
func (r Reference) Encode() (string, error) {
	prefix, owner := referencePrefix, r.Task
	switch {
	case r.Task != "" && r.Domain != "":
		return "", fmt.Errorf("%w: a reference names task %q and domain %q, and a session has exactly one owner", ErrReference, r.Task, r.Domain)
	case r.Task == "" && r.Domain == "":
		return "", fmt.Errorf("%w: a reference names neither a task nor a domain", ErrReference)
	case r.Subordinate():
		prefix, owner = subordinatePrefix, r.Domain
	}
	return strings.Join([]string{
		prefix, r.Home, owner, strconv.Itoa(r.PID), strconv.FormatUint(r.Start, 10),
	}, ":"), nil
}

// String renders the reference for a log line: Encode's text, or, for a
// reference Encode refuses, the refusal itself, which ParseReference
// refuses too, so a rendering of a reference with no one owner can never be
// read back as a reference. A record is written from Encode, never from
// this.
func (r Reference) String() string {
	text, err := r.Encode()
	if err != nil {
		return err.Error()
	}
	return text
}

// ParseReference decodes what String wrote and refuses everything else with
// an error wrapping ErrReference: a format name that is neither the
// worker's nor the subordinate's, a field count other than five, a home
// marker that is not lowercase hex of the fixed length, a task identifier
// home.ValidTaskID refuses or a domain home.ValidDomain refuses, a process
// identifier that is not a positive decimal, or a start time that is not a
// decimal.
func ParseReference(text string) (Reference, error) {
	parts := strings.Split(text, ":")
	if len(parts) != 5 {
		return Reference{}, fmt.Errorf("%w: %d fields, want 5", ErrReference, len(parts))
	}
	subordinate := parts[0] == subordinatePrefix
	if parts[0] != referencePrefix && !subordinate {
		return Reference{}, fmt.Errorf("%w: format %q, want %q or %q", ErrReference, parts[0], referencePrefix, subordinatePrefix)
	}
	marker := parts[1]
	if len(marker) != homeMarkerLength || strings.Trim(marker, "0123456789abcdef") != "" {
		return Reference{}, fmt.Errorf("%w: home marker %q is not %d lowercase hex characters", ErrReference, marker, homeMarkerLength)
	}
	if subordinate {
		if err := home.ValidDomain(parts[2]); err != nil {
			return Reference{}, fmt.Errorf("%w: %w", ErrReference, err)
		}
	} else if err := home.ValidTaskID(parts[2]); err != nil {
		return Reference{}, fmt.Errorf("%w: %w", ErrReference, err)
	}
	pid, err := strconv.Atoi(parts[3])
	if err != nil || pid <= 0 || strconv.Itoa(pid) != parts[3] {
		return Reference{}, fmt.Errorf("%w: process identifier %q is not a positive decimal", ErrReference, parts[3])
	}
	start, err := strconv.ParseUint(parts[4], 10, 64)
	if err != nil || strconv.FormatUint(start, 10) != parts[4] {
		return Reference{}, fmt.Errorf("%w: start time %q is not a decimal", ErrReference, parts[4])
	}
	ref := Reference{Home: marker, Task: parts[2], PID: pid, Start: start}
	if subordinate {
		ref.Task, ref.Domain = "", parts[2]
	}
	return ref, nil
}
