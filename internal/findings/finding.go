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
// an object with path and line fields, or the single string that tools print.
// A JSON null leaves the location empty.
//
// The string form is read in three shapes, which parseLocationText decides
// between by looking only at whether the trailing segments are positive
// integers:
//
//   - "path:line:column", when the text after the final colon and the text
//     between the two final colons both parse as positive integers. The line
//     is the middle segment. The column is read and then discarded, because
//     Location has no column field, and every common compiler and linter
//     prints this form.
//   - "path:line", when the text after the final colon parses as a positive
//     integer and the segment before it does not.
//   - "path", for anything else, so a path that happens to contain a colon is
//     not silently truncated. That fallback is why the two rules above test
//     for positive integers rather than splitting on colons.
//
// In the object form each part is read on its own terms, on the same grounds
// Severity.UnmarshalJSON reads a severity that way: a location decides nothing
// about who resolves a finding, so a part of it this package cannot read must
// not discard the surrounding findings. A line that is not a JSON number leaves
// Line at zero while Path is still read, which keeps the path rather than
// pointing a person at the wrong line, and a path that is not a JSON string
// leaves Path empty.
//
// A value that is neither an object, nor a string, nor null is refused, because
// that shape says nothing readable at all.
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
	// Holding each part as raw JSON is what lets one unreadable part be
	// dropped without the other, and it does not re-enter this method.
	var obj struct {
		Path json.RawMessage `json:"path"`
		Line json.RawMessage `json:"line"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	var read Location
	var path string
	if json.Unmarshal(obj.Path, &path) == nil {
		read.Path = path
	}
	var line int
	if json.Unmarshal(obj.Line, &line) == nil {
		read.Line = line
	}
	*l = read
	return nil
}

// parseLocationText splits the three string forms described on
// Location.UnmarshalJSON. It is total: every input yields a location, and one
// it cannot split becomes a path.
func parseLocationText(s string) Location {
	s = strings.TrimSpace(s)
	last := strings.LastIndex(s, ":")
	if last <= 0 {
		return Location{Path: s}
	}
	trailing, err := strconv.Atoi(s[last+1:])
	if err != nil || trailing <= 0 {
		return Location{Path: s}
	}
	if middle := strings.LastIndex(s[:last], ":"); middle > 0 {
		if line, err := strconv.Atoi(s[middle+1 : last]); err == nil && line > 0 {
			return Location{Path: strings.TrimSpace(s[:middle]), Line: line}
		}
	}
	return Location{Path: strings.TrimSpace(s[:last]), Line: trailing}
}

// Paths is a list of repository-relative paths an agent wrote: a finding's
// Cites and a report's Read. Both are read from untrusted output, so both
// decode on the terms Action, Severity, and Location already set here, which
// is why they share a type rather than a slice each: a part of a report this
// package cannot read must not discard the findings around it.
//
// The paths are carried and never resolved against a filesystem.
type Paths []string

// UnmarshalJSON reads the shapes an agent writes a list of paths in, and
// cannot fail:
//
//   - A JSON list yields one entry per element, each element's own string
//     where it is a string.
//   - A single JSON string yields a one-element list. An agent asked for the
//     paths one finding rests on writes the single path often enough that
//     refusing it would cost the report for a shape everyone understands.
//   - The whole value written as JSON null yields no paths, as does a list
//     with no elements. Null there is how JSON says the field is absent, which
//     is a readable answer meaning nothing was named.
//   - Every other value, a number or an object among them, and every element
//     of a list that is not a JSON string, null included, is kept as one entry
//     holding the JSON as it was written. Null inside a list is not the case
//     above: the list says here is a path and the element is not one.
//
// The last rule is the one that decides rather than reports, and it is chosen
// to fail toward refusing a finding. An entry holding JSON text names
// something no evidence set of real paths holds, so a finding resting on it is
// refused for want of evidence and the refusal quotes what was written, rather
// than the finding being admitted because the thing it rests on could not be
// read. What decides is what the value is rather than whether some decode of
// it reported an error, because unmarshalling null into a string reports none
// and would drop the entry the rule exists to keep.
//
// It is one rule for both fields because it is one type, and an evidence set
// is the safe side of it too: an entry no real path equals supports nothing,
// so a reviewer whose declaration could not be read admits nothing by it, and
// the entry stands in the reported comparison where the unreadable declaration
// is a person's to see rather than something silently dropped.
func (p *Paths) UnmarshalJSON(b []byte) error {
	if strings.TrimSpace(string(b)) == "null" {
		*p = nil
		return nil
	}
	var list []json.RawMessage
	if json.Unmarshal(b, &list) == nil {
		read := make(Paths, 0, len(list))
		for _, entry := range list {
			read = append(read, pathText(entry))
		}
		*p = read
		return nil
	}
	*p = Paths{pathText(b)}
	return nil
}

// pathText is one entry as a path: the string a JSON string holds, or, for
// every other value, the JSON as it was written, so a path this package could
// not read stays quotable in the refusal it causes.
//
// Which of the two it is is decided by what the value is, a JSON string or
// not, rather than by whether decoding it into a string returned an error.
// Null is why: encoding/json unmarshals it into a string without complaint and
// without writing anything, so an error is not the question that separates a
// path from a value that is not one.
func pathText(raw json.RawMessage) string {
	text := strings.TrimSpace(string(raw))
	if strings.HasPrefix(text, `"`) {
		var path string
		if json.Unmarshal(raw, &path) == nil {
			return path
		}
	}
	return text
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
	// Cites are the repository-relative paths this finding relies on beyond
	// its own location: the caller, the interface, or the test that makes the
	// claim true. A review's finding is bound to them, so a citation the
	// review did not read refuses the finding rather than supporting it; see
	// ParseReviewReport. Paths says what an agent may write here and what
	// becomes of a value this package cannot read.
	Cites Paths `json:"cites,omitempty"`
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
//
// The name predates the PRD section 5 amendment on this branch and reads
// against the vocabulary that amendment settled: what Parks, Parked, and
// Report.HasParked mean is a hold, a stage waiting on a person's decision, and
// not a park, which now names a bound stopping the run and is the sense
// internal/graph uses the word in. Renaming the three is a queued follow-up,
// not an oversight.
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
	f.Cites = trimmedEntries(f.Cites)
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
// original order, as a new slice. On the name, see Finding.Parks.
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
