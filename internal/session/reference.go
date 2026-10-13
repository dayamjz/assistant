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

// referencePrefix names the format, and is the one thing a later format
// changes. A reference with another prefix is refused rather than read.
const referencePrefix = "terminal1"

// homeMarkerLength is the length of the home marker in hex characters: the
// first 128 bits of the SHA-256 of the home root, which is enough to tell two
// homes on one machine apart and short enough to read.
const homeMarkerLength = 32

// Reference is the identity of one terminal session: which home and task it
// belongs to, and which process it is, by identifier and start time. It is
// what the caller records as the attempt's session reference, and the one
// thing Probe reads.
type Reference struct {
	// Home is HomeMarker of the home root the session was created under.
	Home string
	// Task is the task identifier, as home.ValidTaskID accepts it.
	Task string
	// PID is the process identifier of the agent process.
	PID int
	// Start is the platform's reading of the process's start time, compared
	// for equality only, and zero where the platform could not read one.
	Start uint64
}

// HomeMarker is the marker a reference carries for a home: a hash of its
// root, so a reference names its home without spelling a path.
func HomeMarker(h *home.Home) string {
	sum := sha256.Sum256([]byte(h.Root()))
	return hex.EncodeToString(sum[:])[:homeMarkerLength]
}

// String encodes the reference in the closed format ParseReference reads:
// the format name, the home marker, the task identifier, the process
// identifier and the start time, joined by colons.
func (r Reference) String() string {
	return strings.Join([]string{
		referencePrefix, r.Home, r.Task, strconv.Itoa(r.PID), strconv.FormatUint(r.Start, 10),
	}, ":")
}

// ParseReference decodes what String wrote and refuses everything else with
// an error wrapping ErrReference: another format name, a field count other
// than five, a home marker that is not lowercase hex of the fixed length, a
// task identifier home.ValidTaskID refuses, a process identifier that is not
// a positive decimal, or a start time that is not a decimal.
func ParseReference(text string) (Reference, error) {
	parts := strings.Split(text, ":")
	if len(parts) != 5 {
		return Reference{}, fmt.Errorf("%w: %d fields, want 5", ErrReference, len(parts))
	}
	if parts[0] != referencePrefix {
		return Reference{}, fmt.Errorf("%w: format %q, want %q", ErrReference, parts[0], referencePrefix)
	}
	marker := parts[1]
	if len(marker) != homeMarkerLength || strings.Trim(marker, "0123456789abcdef") != "" {
		return Reference{}, fmt.Errorf("%w: home marker %q is not %d lowercase hex characters", ErrReference, marker, homeMarkerLength)
	}
	if err := home.ValidTaskID(parts[2]); err != nil {
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
	return Reference{Home: marker, Task: parts[2], PID: pid, Start: start}, nil
}
