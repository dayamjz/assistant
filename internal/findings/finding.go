package findings

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Location is where a finding is. Every part of it is optional, because not
// every finding is about a file: a check failure or a missing piece of
// evidence has no path, and a finding about a whole file has no line.
type Location struct {
	// Path is the repository-relative path the finding is about, empty when
	// the finding is not about a file. This package carries it and never
	// resolves it against a filesystem.
	Path string `json:"path,omitempty"`
	// Line is the 1-based line the finding is about, zero when the finding is
	// not about one line. Normalize replaces a negative line with zero.
	Line int `json:"line,omitempty"`
}

// String renders the location as "path:line", as "path" when there is no line,
// and as the empty string when there is no path. It is for display and for
// diagnostics, not a wire format.
func (l Location) String() string {
	switch {
	case l.Path == "":
		return ""
	case l.Line > 0:
		return l.Path + ":" + strconv.Itoa(l.Line)
	default:
		return l.Path
	}
}

// Empty reports whether the location names nothing at all.
func (l Location) Empty() bool { return l.Path == "" && l.Line == 0 }

// UnmarshalJSON reads a location in either of the two shapes agents produce:
// an object with path and line fields, or the single string "path:line" that
// tools print. In the string form the text after the final colon is the line
// when it parses as a positive integer, and otherwise the whole string is the
// path, so a path that happens to contain a colon is not silently truncated.
// A JSON null leaves the location empty. Any other JSON value is refused,
// because a location this package cannot read is not P3's case and guessing at
// one would point a person at the wrong code.
func (l *Location) UnmarshalJSON(b []byte) error {
	trimmed := strings.TrimSpace(string(b))
	if trimmed == "null" {
		*l = Location{}
		return nil
	}
	if strings.HasPrefix(trimmed, `"`) {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*l = parseLocationText(s)
		return nil
	}
	// The alias exists so decoding the object form does not re-enter this
	// method and recurse forever.
	type locationObject Location
	var obj locationObject
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	*l = Location(obj)
	return nil
}

// parseLocationText splits the "path:line" form described on
// Location.UnmarshalJSON.
func parseLocationText(s string) Location {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, ":"); i > 0 {
		if n, err := strconv.Atoi(s[i+1:]); err == nil && n > 0 {
			return Location{Path: strings.TrimSpace(s[:i]), Line: n}
		}
	}
	return Location{Path: s}
}

// Finding is one thing a stage found. Its Action decides who resolves it, and
// only ActionFix is eligible for the automatic fix loop.
type Finding struct {
	// ID identifies the finding within its report, so a person or an agent can
	// select it at a hold. Normalize assigns one to a finding that arrives
	// without one and never rewrites one that arrives with one.
	ID string `json:"id,omitempty"`
	// Severity orders the finding for the person reading the list. It decides
	// nothing about who resolves the finding.
	Severity Severity `json:"severity,omitempty"`
	// Action decides who resolves the finding. Anything unrecognized becomes
	// ActionAsk in Normalize, and only ActionFix is ever fix-eligible.
	Action Action `json:"action,omitempty"`
	// Location is where the finding is, and may name nothing.
	Location Location `json:"location,omitzero"`
	// Description says what was found, in the stage's own words. Validate
	// refuses a finding whose description is empty, since it tells a person
	// nothing they can act on.
	Description string `json:"description"`
}

// FixEligible reports whether this finding may enter the automatic fix loop.
// It is true for exactly ActionFix and for nothing else, so an ask, a note, an
// absent action, and an action nobody recognizes are all ineligible whether or
// not the finding has been normalized. Every selector in this package is built
// on this predicate.
func (f Finding) FixEligible() bool { return f.Action == ActionFix }

// Parks reports whether this finding holds the stage for a person's decision.
// It is true for exactly ActionAsk after normalization; on an un-normalized
// finding an unrecognized action reports false here while still reporting
// false from FixEligible, so nothing is ever both.
func (f Finding) Parks() bool { return f.Action == ActionAsk }

// normalized returns the finding with its text trimmed, its action and
// severity resolved to recognized values, and a negative line replaced by
// zero. It does not touch the identifier; NormalizeFindings owns that, because
// assigning one requires seeing the whole set.
func (f Finding) normalized() Finding {
	f.ID = strings.TrimSpace(f.ID)
	f.Action = ParseAction(string(f.Action))
	f.Severity = ParseSeverity(string(f.Severity))
	f.Description = strings.TrimSpace(f.Description)
	f.Location.Path = strings.TrimSpace(f.Location.Path)
	if f.Location.Line < 0 {
		f.Location.Line = 0
	}
	return f
}

// Fixable returns the findings eligible for the automatic fix loop, in their
// original order. It selects on Finding.FixEligible, so it can never return an
// ask or a note. The result is a new slice and shares no backing array with fs.
func Fixable(fs []Finding) []Finding { return selectBy(fs, Finding.FixEligible) }

// Parked returns the findings holding for a person's decision, in their
// original order, as a new slice.
func Parked(fs []Finding) []Finding { return selectBy(fs, Finding.Parks) }

// selectBy returns the findings satisfying keep, in order, as a new slice.
func selectBy(fs []Finding, keep func(Finding) bool) []Finding {
	out := make([]Finding, 0, len(fs))
	for _, f := range fs {
		if keep(f) {
			out = append(out, f)
		}
	}
	return out
}
