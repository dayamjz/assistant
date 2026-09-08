package findings

import (
	"encoding/json"
	"strings"
)

// Evidence is one artifact a stage produced to support what it reported, such
// as a captured test run. The PRD keeps evidence outside the isolated copy so
// test artifacts never become part of the change being validated; this package
// carries the reference and never reads, writes, or resolves it.
type Evidence struct {
	// Label says what the artifact shows.
	Label string `json:"label"`
	// Path locates the artifact. It is opaque here: this package neither
	// interprets it nor requires it to exist.
	Path string `json:"path"`
}

// Report is what a stage returns: its summary, its findings, an optional risk
// judgment with the rationale for it, what it tested, and any evidence it
// produced.
//
// ParseReport is the path agent output takes to become one, because it is the
// path that normalizes and validates. Nothing here prevents a caller from
// decoding or building a Report some other way, so a Report that did not come
// from ParseReport carries no guarantee that its actions are recognized. It is
// still safe against the one thing that matters: fix eligibility is equality
// with ActionFix, so an unrecognized action is ineligible however the Report
// was built.
type Report struct {
	// Summary is the stage's account of what it did and concluded. Validate
	// requires it, so an empty or truncated output cannot be read as a clean
	// pass.
	Summary string `json:"summary"`
	// Revision is the commit the stage read, in the stage's own words. A
	// review report has to carry the commit it reviewed, and
	// ParseReviewReport refuses one that is not the commit the run asked
	// about; Validate requires nothing of it, because no other stage answers
	// to such a rule.
	Revision string `json:"revision,omitempty"`
	// Read is the evidence set: the repository-relative paths the stage
	// actually read. It is what ParseReviewReport binds a review's findings
	// to, and it is a claim the stage makes about itself, carried here and
	// never resolved against a filesystem. Paths says what an agent may write
	// here and what becomes of a value this package cannot read.
	Read Paths `json:"read,omitempty"`
	// Traced is the review stage's answer to the scope lens: for each path
	// the change touched, what part of the recorded intent it follows from.
	// It is carried here for the reason Revision and Read are, and it is
	// answered for by the same one stage; Trace says what it is worth, and
	// Traces says what an agent may write here and what becomes of a value
	// this package cannot read.
	Traced Traces `json:"traced,omitempty"`
	// Findings is what the stage found, in the order it reported them. Empty
	// is valid and means the stage found nothing.
	Findings []Finding `json:"findings,omitempty"`
	// Risk is the stage's optional risk judgment. RiskUnstated means the stage
	// offered none, which is legal; a risk word this package does not
	// recognize is refused by Validate rather than mapped to a level.
	Risk Risk `json:"risk,omitempty"`
	// RiskRationale says why the stage chose that risk level. It is optional
	// even when Risk is stated.
	RiskRationale string `json:"risk_rationale,omitempty"`
	// Tested is what the stage actually checked, one entry per check, in the
	// stage's own words.
	Tested []string `json:"tested,omitempty"`
	// Evidence is the artifacts the stage produced.
	Evidence []Evidence `json:"evidence,omitempty"`
}

// Normalize returns a copy of the report with every finding normalized and
// with identifiers assigned to the findings that arrived without one. It
// resolves every unrecognized action to ActionAsk and every unrecognized
// severity to SeverityWarning, trims the text fields, and drops entries of
// Read, Tested, and a finding's Cites that are empty once trimmed.
//
// It does not resolve Risk, since there is no safe value to resolve an
// unrecognized one to; it trims and folds a recognized one and otherwise
// leaves what the stage said in place for Validate to refuse and for a person
// to read.
//
// Normalize is total: it always returns a report and never reports an error.
// The receiver and its slices are not modified.
func (r Report) Normalize() Report {
	r.Summary = strings.TrimSpace(r.Summary)
	r.RiskRationale = strings.TrimSpace(r.RiskRationale)
	// ParseRisk returns the trimmed original when it recognizes nothing, so
	// this both folds a recognized level and leaves an unrecognized one intact
	// for Validate to refuse by name.
	r.Risk, _ = ParseRisk(string(r.Risk))
	r.Revision = strings.TrimSpace(r.Revision)
	r.Findings = NormalizeFindings(r.Findings)
	r.Read = trimmedEntries(r.Read)
	r.Traced = normalizeTraces(r.Traced)
	r.Tested = normalizeTested(r.Tested)
	r.Evidence = normalizeEvidence(r.Evidence)
	return r
}

// Trace is one claim that a path the change touched follows from the recorded
// intent. It is the reviewer's own words, recorded and attributable, and
// nothing here or in internal/scope checks it against the intent or against
// the code.
//
// It lives in this package because this is where a review report is decoded,
// and a field the decoder does not know about is dropped rather than carried.
// internal/scope, which is the lens that asks for these and reads them, names
// this type rather than declaring a second one.
//
// A Trace is not a Finding and is bound to no evidence set. A finding is a
// claim about code the reviewer read, so ParseReviewReport binds it to what
// the reviewer declared reading; a trace is an account of a path the change
// touched, which the run already knows, and it silences an observation rather
// than making one. Traces is how one is read off the wire, and it is what
// keeps a trace this package cannot read from costing the report the findings
// around it.
type Trace struct {
	// Path is the repository-relative path being accounted for.
	Path string `json:"path"`
	// Reason is the part of the intent the path follows from. A trace with no
	// reason accounts for nothing, because a bare path asserts only that a
	// file was changed, which was already known.
	Reason string `json:"reason"`
}

// Traces is the review stage's whole answer to the scope lens, as an agent
// wrote it. It is read from untrusted output, so it decodes on the terms
// Action, Severity, Location, and Paths already set here, which is why it is a
// named type rather than a plain slice: a part of a report this package cannot
// read must not discard the findings around it.
type Traces []Trace

// UnmarshalJSON reads the shapes an agent writes its answer to the scope lens
// in, and cannot fail:
//
//   - A JSON list yields one entry per element, and an element that is a JSON
//     object holding a trace is that trace.
//   - The whole value written as JSON null yields no traces, as does a list
//     with no elements, and so does every other value that is not a list: an
//     object keyed by path and a single bare path are both shapes an agent
//     writes, and neither is a list of accounts this package can read.
//   - Every element of a list that is not a JSON object, a bare string, a
//     number, a nested list and null among them, and every object this package
//     read nothing out of, yields a trace holding the JSON as it was written
//     as its reason and naming no path.
//
// The last two rules are the ones that decide rather than report, and both are
// chosen to fail toward the scope note firing. A trace naming no path accounts
// for no path, so it silences nothing and every path it might have been meant
// for keeps its note; and an unreadable answer read as no answer at all leaves
// every note standing rather than any of them silenced. Neither direction
// refuses anything, which is what the lens being note-only requires: a review
// may not lose the findings it wrote over the shape of an account that blocks
// nothing.
//
// Keeping the JSON as it was written, rather than dropping the element, is
// what Paths does for the same reason: the reviewer said something here, and a
// person reading the report sees what it was instead of a gap. blankTrace is
// what makes that hold all the way to the report, because this is not the only
// place a trace can be dropped.
func (t *Traces) UnmarshalJSON(b []byte) error {
	if strings.TrimSpace(string(b)) == "null" {
		*t = nil
		return nil
	}
	var list []json.RawMessage
	if json.Unmarshal(b, &list) != nil {
		*t = nil
		return nil
	}
	read := make(Traces, 0, len(list))
	for _, entry := range list {
		read = append(read, traceValue(entry))
	}
	*t = read
	return nil
}

// traceValue is one element as a trace: the trace a JSON object holding one
// spells, or, for every other value, a trace naming no path and holding the
// JSON as it was written as its reason.
//
// Which of the two it is is decided by what came out rather than by what went
// in. A value that is not a JSON object is not an account whatever
// unmarshalling it would report; an object with a field whose value the type
// cannot hold does not decode at all; and an object that decodes to nothing
// read nothing this package could use, which is what an object keyed by
// something other than "path" and "reason" does, because encoding/json passes
// over a field it was not asked for rather than refusing it. All three come to
// the same place, because from a reviewer's side they are one thing: an
// account this package could not read.
//
// Asking what came out is also what keeps the answer from resting on a
// distinction the reviewer cannot see. Deciding on the decode error alone
// would keep {"file": "a.go", "reason": "..."} and lose
// {"file": "a.go", "why": "..."} over which of two unasked-for keys happened
// to collide with one this package knows.
func traceValue(raw json.RawMessage) Trace {
	text := strings.TrimSpace(string(raw))
	if strings.HasPrefix(text, "{") {
		var trace Trace
		if json.Unmarshal(raw, &trace) == nil && !blankTrace(trace) {
			return trace
		}
	}
	return Trace{Reason: text}
}

// blankTrace reports whether a trace carries nothing on either side once
// trimmed. It is the one test of whether a trace is worth keeping, and it is
// one function because the two places that decide a trace's visibility have to
// give the same answer: traceValue chooses what an element becomes, and
// normalizeTraces chooses what survives into the report.
//
// How they compose is the whole point. traceValue never returns a blank trace
// for an element of a list, because an element this package could not read
// becomes its own JSON text and no JSON value is blank once trimmed, so
// normalizeTraces cannot erase what the decoder kept. What normalizeTraces
// still drops is a blank entry in a Report a caller built in Go rather than
// parsed, which no wire shape produces.
func blankTrace(t Trace) bool {
	return strings.TrimSpace(t.Path) == "" && strings.TrimSpace(t.Reason) == ""
}

// normalizeTraces trims each trace and drops the ones that carry nothing at
// all, preserving order and a nil input.
//
// It drops only the wholly empty entry. A trace naming a path with no reason,
// and a reason naming no path, are each kept and each account for nothing, so
// they stay visible to a reader of the report rather than being filtered here
// into something indistinguishable from a trace nobody wrote.
//
// Validate asks nothing of a trace, which is the deliberate difference from
// Evidence, whose missing path is a defect. The lens these answer is
// note-only and may not stop a run, so a report carrying a malformed trace is
// refused nothing: the trace accounts for nothing, its path keeps whatever
// observation it would have silenced, and the report is read as it stands.
// This is the half of that which runs once a trace has been read, and
// Traces.UnmarshalJSON is the half that decides what a value this package
// cannot read becomes before it gets here. blankTrace is the test both ask,
// and states how the two compose.
func normalizeTraces(traces Traces) Traces {
	if traces == nil {
		return nil
	}
	out := make(Traces, 0, len(traces))
	for _, t := range traces {
		if blankTrace(t) {
			continue
		}
		t.Path, t.Reason = strings.TrimSpace(t.Path), strings.TrimSpace(t.Reason)
		out = append(out, t)
	}
	return out
}

// normalizeTested trims each entry and drops the ones that were only
// whitespace. A blank line in a list of what was tested carries nothing and
// would otherwise have to be filtered by every reader.
func normalizeTested(tested []string) []string { return trimmedEntries(tested) }

// trimmedEntries trims each entry and drops the ones that were only
// whitespace, preserving order and a nil input. It is what every list of
// short strings in a report is normalized with, so a blank entry never has to
// be filtered by a reader. It does not collapse repeats: a list that says the
// same thing twice said it twice.
func trimmedEntries(list []string) []string {
	if list == nil {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, entry := range list {
		if entry = strings.TrimSpace(entry); entry != "" {
			out = append(out, entry)
		}
	}
	return out
}

// normalizeEvidence trims each artifact's label and path, keeping every entry
// so Validate can refuse one with no path rather than silently losing it.
func normalizeEvidence(evidence []Evidence) []Evidence {
	if evidence == nil {
		return nil
	}
	out := make([]Evidence, len(evidence))
	for i, e := range evidence {
		out[i] = Evidence{Label: strings.TrimSpace(e.Label), Path: strings.TrimSpace(e.Path)}
	}
	return out
}

// Fixable returns the report's findings that are eligible for the automatic
// fix loop, in their original order. It can never return an ask or a note.
func (r Report) Fixable() []Finding { return Fixable(r.Findings) }

// Held returns the report's findings that hold for a person's decision, in
// their original order. On the name, see Finding.Holds.
func (r Report) Held() []Finding { return Held(r.Findings) }

// HasHeld reports whether any finding holds for a person's decision. A stage
// with one holds regardless of how many fix rounds its configuration allows.
// On the name, see Finding.Holds.
func (r Report) HasHeld() bool {
	for _, f := range r.Findings {
		if f.Holds() {
			return true
		}
	}
	return false
}

// AllNotes reports whether every finding is informational, which is the
// condition under which a stage is approved as it stands. It is true for a
// report with no findings at all, and it is defined as the absence of any
// finding that is not a note, so a finding carrying an action nobody
// recognizes makes it false.
func (r Report) AllNotes() bool {
	for _, f := range r.Findings {
		if f.Action != ActionNote {
			return false
		}
	}
	return true
}
