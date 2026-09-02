package findings

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"strconv"
)

const (
	// idPrefix marks a derived identifier so it is recognizable in a log or a
	// terminal next to one a stage supplied itself.
	idPrefix = "f-"
	// idDigits is how much of the digest a derived identifier keeps. Forty
	// eight bits is far more than a report's handful of findings needs, and it
	// stays short enough to read aloud.
	idDigits = 12
	// idAttempts bounds the search for a free identifier. Each attempt hashes a
	// different occurrence number, and the search ends at the first candidate
	// not already taken or at this many attempts, so it terminates on any
	// input. Reaching the bound needs no digest collision, only this many
	// findings identical in every hashed field; deriveID states what it returns
	// then and what that costs.
	idAttempts = 64
)

// NormalizeFindings returns a copy of fs with every finding normalized and
// with an identifier assigned to each finding that arrived without one.
//
// Assignment is deterministic: the same input set always produces the same
// identifiers, and an identifier a finding already carries is never rewritten.
//
// A derived identifier is a function of two things and of nothing else: the
// finding's own normalized severity, action, location, and description, and the
// identifiers already taken in that same set. It does not otherwise depend on
// where the finding sits, so reordering a set does not move one.
//
// The second half of that is load-bearing, because it means a finding's
// identifier can move when the set around it changes. It moves in exactly two
// cases, both of them a clash with something already taken:
//
//   - Another finding identical in every field named above precedes it. Such
//     findings cannot be told apart by content, so they are separated by how
//     many identical ones come first, and adding another identical finding
//     ahead of one does move its identifier.
//   - Another finding in the set already carries the identifier this one would
//     otherwise derive. Identifiers that arrive with a finding are collected
//     before any is derived, so this holds whether that finding sits before or
//     after this one.
//
// Neither avoidance makes the whole set unique: two findings that arrive
// already carrying one identifier between them still do, and the fallback
// deriveID documents does not check what it returns. Validate is what refuses a
// set with duplicates.
//
// The result is a new slice and shares no backing array with fs. A nil input
// returns nil.
func NormalizeFindings(fs []Finding) []Finding {
	if fs == nil {
		return nil
	}
	out := make([]Finding, len(fs))
	taken := make(map[string]struct{}, len(fs))
	for i, f := range fs {
		out[i] = f.normalized()
		if out[i].ID != "" {
			taken[out[i].ID] = struct{}{}
		}
	}
	// Identifiers already carried are collected before any is derived, so a
	// derived one cannot land on an identifier belonging to a later finding.
	for i := range out {
		if out[i].ID != "" {
			continue
		}
		out[i].ID = deriveID(out[i], taken)
		taken[out[i].ID] = struct{}{}
	}
	return out
}

// deriveID returns an identifier for f that is not in taken. The digest covers
// the fields that describe the finding, with each field length-prefixed so no
// field's content can imitate the boundary between two fields. The occurrence
// number distinguishes findings that are identical in every one of those
// fields, which is the only case where content alone cannot separate them.
//
// After idAttempts collisions it returns the last candidate with the attempt
// count appended rather than searching on, so it terminates on any input. That
// last identifier is not checked against taken, which is the one path by which
// a derived identifier can collide; Validate refuses the result.
func deriveID(f Finding, taken map[string]struct{}) string {
	var candidate string
	for occurrence := 0; occurrence < idAttempts; occurrence++ {
		sum := sha256.New()
		hashField(sum, string(f.Severity))
		hashField(sum, string(f.Action))
		hashField(sum, f.Location.Path)
		hashField(sum, strconv.Itoa(f.Location.Line))
		hashField(sum, f.Description)
		hashField(sum, strconv.Itoa(occurrence))
		candidate = idPrefix + hex.EncodeToString(sum.Sum(nil))[:idDigits]
		if _, clash := taken[candidate]; !clash {
			return candidate
		}
	}
	return candidate + "-" + strconv.Itoa(idAttempts)
}

// hashField writes s to h prefixed by its length, so that the concatenation of
// several fields is unambiguous no matter what any field contains.
func hashField(h hash.Hash, s string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(s)))
	// hash.Hash documents that Write never returns an error.
	_, _ = h.Write(length[:])
	_, _ = h.Write([]byte(s))
}
