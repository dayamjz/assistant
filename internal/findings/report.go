package findings

import "strings"

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
	// about; Validate requires nothing of it, because the other eight stages
	// answer to no such rule.
	Revision string `json:"revision,omitempty"`
	// Read is the evidence set: the repository-relative paths the stage
	// actually read. It is what ParseReviewReport binds a review's findings
	// to, and it is a claim the stage makes about itself, carried here and
	// never resolved against a filesystem. Paths says what an agent may write
	// here and what becomes of a value this package cannot read.
	Read Paths `json:"read,omitempty"`
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
	r.Tested = normalizeTested(r.Tested)
	r.Evidence = normalizeEvidence(r.Evidence)
	return r
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

// Parked returns the report's findings that hold for a person's decision, in
// their original order. On the name, see Finding.Parks.
func (r Report) Parked() []Finding { return Parked(r.Findings) }

// HasParked reports whether any finding holds for a person's decision. A stage
// with one holds regardless of how many fix rounds its configuration allows.
// On the name, see Finding.Parks.
func (r Report) HasParked() bool {
	for _, f := range r.Findings {
		if f.Parks() {
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
