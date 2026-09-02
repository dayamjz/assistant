package findings

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Defect names a way a report can be malformed. A refusal names the defect, so
// a caller can tell what was wrong without parsing prose.
type Defect string

const (
	// DefectMissingSummary is a report with no summary. A stage always says
	// what it did, and without that an empty or truncated output would read as
	// a clean pass.
	DefectMissingSummary Defect = "missing-summary"
	// DefectMissingDescription is a finding with no description. It tells a
	// person nothing they can act on.
	DefectMissingDescription Defect = "missing-description"
	// DefectMissingID is a finding with no identifier. NormalizeFindings
	// assigns one, so this means the report was never normalized.
	DefectMissingID Defect = "missing-id"
	// DefectDuplicateID is two findings in one report sharing an identifier.
	// Selecting a finding at a hold names it by identifier, so a shared one
	// would make a selection ambiguous.
	DefectDuplicateID Defect = "duplicate-id"
	// DefectUnrecognizedAction is a finding whose action is not one of the
	// three. Normalize resolves that to ActionAsk, so this means the report
	// was never normalized rather than that an agent misbehaved.
	DefectUnrecognizedAction Defect = "unrecognized-action"
	// DefectUnrecognizedSeverity is a finding whose severity is not one of the
	// three, which likewise means the report was never normalized.
	DefectUnrecognizedSeverity Defect = "unrecognized-severity"
	// DefectUnrecognizedRisk is a report whose risk is not one of the three
	// levels and is not unstated. Normalize deliberately leaves it alone,
	// because there is no level it could safely be read as.
	DefectUnrecognizedRisk Defect = "unrecognized-risk"
	// DefectNegativeLine is a location with a negative line number.
	DefectNegativeLine Defect = "negative-line"
	// DefectMissingEvidencePath is an evidence artifact with no path, which
	// points at nothing.
	DefectMissingEvidencePath Defect = "missing-evidence-path"
)

// Flaw is one defect found in one part of a report.
type Flaw struct {
	// Defect is what was wrong.
	Defect Defect
	// Field names the part of the report that was wrong, as a path such as
	// "findings[2].action".
	Field string
	// Detail says what it held, when quoting that helps.
	Detail string
}

// Error renders the flaw as "defect: field" with the detail appended when
// there is one.
func (f Flaw) Error() string {
	if f.Detail == "" {
		return string(f.Defect) + ": " + f.Field
	}
	return string(f.Defect) + ": " + f.Field + ": " + f.Detail
}

// ValidationError reports every defect a report holds. Validate returns one of
// these rather than the first problem it met, so a stage author fixing a
// prompt sees the whole list at once.
type ValidationError struct {
	// Flaws is every defect found, in the order they were found.
	Flaws []Flaw
}

// Error lists every flaw, one per line.
func (e *ValidationError) Error() string {
	if len(e.Flaws) == 1 {
		return "findings: " + e.Flaws[0].Error()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "findings: %d defects:", len(e.Flaws))
	for _, f := range e.Flaws {
		b.WriteString("\n  " + f.Error())
	}
	return b.String()
}

// HasDefect reports whether any flaw is of the given defect.
func (e *ValidationError) HasDefect(d Defect) bool {
	for _, f := range e.Flaws {
		if f.Defect == d {
			return true
		}
	}
	return false
}

// Validate reports every defect in the report, or nil when it holds none. The
// error is a *ValidationError.
//
// A report that Validate accepts holds a summary, findings that each have an
// identifier unique within the report, a description, a recognized action, and
// a recognized severity, and a risk that is either unstated or one of the three
// levels. ParseReport normalizes before validating, so the unrecognized-value
// defects cannot come from an agent's wording; they mean a caller built a
// report by hand and did not normalize it.
func (r Report) Validate() error {
	var flaws []Flaw
	add := func(d Defect, field, detail string) {
		flaws = append(flaws, Flaw{Defect: d, Field: field, Detail: detail})
	}
	if strings.TrimSpace(r.Summary) == "" {
		add(DefectMissingSummary, "summary", "")
	}
	if !r.Risk.Recognized() {
		add(DefectUnrecognizedRisk, "risk", strconv.Quote(string(r.Risk)))
	}
	seen := make(map[string]int, len(r.Findings))
	for i, f := range r.Findings {
		field := "findings[" + strconv.Itoa(i) + "]"
		if f.ID == "" {
			add(DefectMissingID, field+".id", "")
		} else if first, dup := seen[f.ID]; dup {
			add(DefectDuplicateID, field+".id",
				strconv.Quote(f.ID)+" is also findings["+strconv.Itoa(first)+"].id")
		} else {
			seen[f.ID] = i
		}
		if strings.TrimSpace(f.Description) == "" {
			add(DefectMissingDescription, field+".description", "")
		}
		if !f.Action.Recognized() {
			add(DefectUnrecognizedAction, field+".action", strconv.Quote(string(f.Action)))
		}
		if !f.Severity.Recognized() {
			add(DefectUnrecognizedSeverity, field+".severity", strconv.Quote(string(f.Severity)))
		}
		if f.Location.Line < 0 {
			add(DefectNegativeLine, field+".location.line", strconv.Itoa(f.Location.Line))
		}
	}
	for i, e := range r.Evidence {
		if strings.TrimSpace(e.Path) == "" {
			add(DefectMissingEvidencePath, "evidence["+strconv.Itoa(i)+"].path", "")
		}
	}
	if len(flaws) == 0 {
		return nil
	}
	return &ValidationError{Flaws: flaws}
}

// Errors a caller is expected to handle. Each is a typed result, never a
// warning execution continues past.
var (
	// ErrNoReport is returned when agent output holds no balanced JSON object
	// that decodes into a report at all. The output is quoted in the wrapping
	// error so the failure can be diagnosed from the refusal alone.
	ErrNoReport = errors.New("findings: agent output holds no report object")
	// ErrUnreadableReport is returned when agent output holds an object that is
	// a report by its keys, carrying "summary", "findings", or "risk", but
	// whose shape this package cannot decode, such as a findings field that is
	// not a list. The decoder's own error is wrapped, so the refusal names the
	// field that could not be read. It is a different fact from ErrNoReport: a
	// report was found rather than missing, so no earlier object in the output
	// is read in its place.
	ErrUnreadableReport = errors.New("findings: agent output holds a report this package cannot read")
	// ErrRawTooLarge is returned when agent output exceeds MaxRawBytes.
	// Scanning it is bounded work, and a report that large is not a report.
	ErrRawTooLarge = errors.New("findings: agent output is larger than the parser accepts")
)
