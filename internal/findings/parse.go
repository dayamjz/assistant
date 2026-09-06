package findings

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// MaxRawBytes is the largest agent output ParseReport will look at. Beyond it
// the output is refused rather than scanned: the scan is linear in the input,
// but an unbounded input is still unbounded work, and a report this large is
// not a report.
const MaxRawBytes = 1 << 20

// reportFields are the keys whose presence says an object was meant to be a
// stage report. Presence is compared the way encoding/json matches a field
// name, ignoring ASCII case, so this asks the same question the decoder did.
var reportFields = [...]string{"summary", "findings", "risk"}

// ParseReport reads a stage report out of whatever an agent printed. The
// output is untrusted, so this is the only supported way to turn it into a
// Report.
//
// It accepts a bare JSON object, an object inside a fenced code block, and an
// object printed after prose, without treating those as three cases: it scans
// for balanced top-level {...} spans, and each of those three shapes puts the
// object in exactly one such span. Inside a candidate span the scan tracks JSON
// string literals, so a brace or a quote inside a string cannot open or close a
// span. At depth zero, outside every span, a quote in prose is deliberately not
// tracked, so prose holding one unmatched brace can still absorb the object
// into a span that never closes or that is not valid JSON.
//
// Candidates are tried from the last in the text backwards, because an agent
// asked for a report ends with it, and prose before it may quote an example.
// Each candidate is resolved by exactly one of these rules:
//
//   - A span that is not a valid JSON object is skipped, and the scan continues
//     with the candidate before it. No key can be read out of it, so nothing
//     tells it apart from prose whose braces happened to balance.
//   - A span that is a valid JSON object, carries at least one of the keys
//     "summary", "findings", or "risk", and does not decode into a Report is
//     the report the agent meant to write in a shape this package cannot read,
//     such as a findings field that is not a list. The error wraps
//     ErrUnreadableReport and no earlier candidate is tried.
//   - A span that decodes and validates is returned, normalized. This is the
//     only path that returns a report. Fields this package does not recognize
//     are ignored rather than refused, so a stage that added a field of its own
//     to its prompt does not lose an otherwise valid set of findings; an
//     unrecognized field never fails the decode, so it never reaches the rule
//     above either.
//   - A span that decodes, fails validation, and carries at least one of those
//     keys is the report the agent meant to write, so its *ValidationError is
//     returned and no earlier candidate is tried. Presence of the key is what
//     counts, not its value: an empty summary is a report with a defect, not an
//     object that is not a report.
//   - A span that decodes and fails validation while carrying none of those
//     keys is not a report at all, so the scan continues with the candidate
//     before it and its defects are discarded. A stray JSON object in an
//     agent's prose is not evidence about a report that was never written.
//
// Those keys are therefore the one thing that decides what is a report, on
// every path. Two consequences a caller can rely on: a *ValidationError only
// ever describes a candidate carrying at least one of them, and if no candidate
// carried one, the error wraps ErrNoReport rather than complaining about
// whatever else the output held.
//
// All of that reasons about candidates, and finding the candidates is best
// effort, which is the limit worth stating. A report mangled badly enough to
// break JSON syntax exposes no keys, so it is skipped like prose; a report
// absorbed by an unmatched brace in the prose before it is never offered as a
// candidate at all. Either way the report the agent meant is not among the
// candidates, and what a caller then sees depends on the rest of the output:
// a refusal when no earlier object validates, and that earlier object returned
// with no error when one does. That is the one outcome here that is not a
// refusal, and the package doc lists the ways to reach it.
func ParseReport(raw string) (Report, error) {
	report, _, err := parse(raw, nil)
	return report, err
}

// ParseReviewReport reads a review stage's report out of whatever the reviewer
// printed and binds every finding in it to the evidence the reviewer declared.
// It is ParseReport with one more rule applied, so everything ParseReport
// documents about which object in the output is the report holds here
// unchanged, and the Report it returns is normalized and validated on exactly
// the same terms.
//
// The rule is PRD section 5, "What a review report has to carry". A claim is
// worth what a reader can check, so a review report carries the revision it
// read and the set of paths it actually read, and its findings are bound to
// them:
//
//   - A report whose revision is not the commit the run asked about is refused
//     with ErrWrongRevision, findings and all. A reading of some other commit
//     is not a review of this change, and a report stating no revision states
//     no commit and is refused the same way.
//   - A finding naming a path the evidence set does not hold is refused. It
//     names a path through its location, through what it cites, or through
//     both, and every one of them has to be in the evidence set. The refusal
//     is in the returned Binding and is also in the returned report, as an
//     informational finding quoting what was claimed, so a reviewer asserting
//     past what it read is visible rather than quietly trimmed.
//   - A finding naming no path at all becomes a note, because nothing supports
//     it. That rule does not reach P3: it is applied to the action the
//     reviewer stated, before Normalize resolves anything, so a finding whose
//     action was missing, empty, or unrecognized is still the ask P3 makes of
//     it and still holds the stage. Only a finding whose action the reviewer
//     stated and this package recognized is demoted. The carve-out is on this
//     rule alone: the refusal above is decided by the path a finding names and
//     applies whatever its action was, so an unclassified claim about unread
//     code is refused and reported as refused rather than held.
//   - The evidence set and the paths the change touched are compared, and the
//     comparison is reported as a finding whether or not it found anything.
//     Binding says why that is the discriminator rather than a decoration. One
//     consequence is worth stating rather than discovering: a review report is
//     therefore never returned with an empty findings list, and a review that
//     found nothing is a report whose findings are all notes, which
//     Report.AllNotes still answers true for.
//
// The Demand is checked first, so a caller that would be refused after
// spending an invocation is refused before it. Demand.Guidance is the other
// half: what it tells the reviewer is what this binds, and the two are written
// together for that reason.
//
// The rule is only ever added to the ones ParseReport applies. A report this
// returns would also have been accepted by ParseReport, and a report
// ParseReport refuses is refused here with the same *ValidationError naming
// the same defects: the report is put to Validate as the reviewer wrote it,
// before the binding rewrites any finding, so binding cannot make an otherwise
// invalid report valid. A reviewer that prints a finding with no description
// is therefore refused here exactly as it is anywhere else, rather than
// clearing the stage on the strength of a note the binding wrote for it.
//
// One thing ParseReport documents is narrower here rather than gone. An object
// earlier in the output that validates as a report, such as a schema example
// quoted in prose, is returned by ParseReport with no error; here it has to
// carry the run's own revision to be returned at all, which an example
// generally does not. That makes the case rarer, not impossible: it is still
// the output deciding which object is read, and an example carrying the right
// revision is read as the report exactly as before.
func ParseReviewReport(raw string, d Demand) (Report, Binding, error) {
	if err := d.Validate(); err != nil {
		return Report{}, Binding{}, err
	}
	return parse(raw, &d)
}

// parse is the candidate scan both entry points run. demand is nil for a
// report answering to no evidence rule, and the binding it returns is then the
// zero Binding.
//
// Where the binding sits in the sequence is load-bearing rather than
// incidental: it runs on the decoded report, after the shape is known to be
// readable and before Normalize, because Normalize is what makes a stated ask
// and a defaulted one the same value. A binding that refuses is treated as a
// defect in the candidate, on the same terms as a validation failure: an
// object carrying a report field is the report the agent meant, so its refusal
// is returned and no earlier candidate is tried, and an object carrying none
// is not a report at all, so the scan continues past it.
//
// The candidate is validated on both sides of the binding, and the first of
// those is what makes the review path refuse exactly what the ordinary path
// refuses. Validate owns what a valid report is and stays the only thing
// asked, here as everywhere; what the two calls differ in is the value put to
// it, the reviewer's report and then the bound one, so neither a defect the
// reviewer wrote nor one the binding would introduce can reach a caller as an
// accepted report.
func parse(raw string, demand *Demand) (Report, Binding, error) {
	if len(raw) > MaxRawBytes {
		return Report{}, Binding{}, fmt.Errorf("%w: %d bytes, limit %d",
			ErrRawTooLarge, len(raw), MaxRawBytes)
	}
	spans := objectSpans(raw)
	for i := len(spans) - 1; i >= 0; i-- {
		object := []byte(raw[spans[i].start:spans[i].end])
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(object, &fields); err != nil {
			continue
		}
		var report Report
		if err := json.Unmarshal(object, &report); err != nil {
			if carriesReportField(fields) {
				return Report{}, Binding{}, fmt.Errorf("%w: %w", ErrUnreadableReport, err)
			}
			continue
		}
		var binding Binding
		if demand != nil {
			// The report is put to the one owner of what a valid report is,
			// as the reviewer wrote it and on exactly the terms the ordinary
			// path below puts it there, before the binding rewrites anything.
			// The binding replaces a refused finding with a note of its own
			// and annotates a demoted one, so a defect the reviewer wrote can
			// stop being visible in the text that reaches Validate afterwards;
			// asking first is what keeps a report ParseReport would refuse
			// from being made valid by having gone through the binding.
			// Normalize runs on a copy, so what bindEvidence is handed is
			// still the report as the reviewer wrote it, stated actions and
			// all.
			if err := report.Normalize().Validate(); err != nil {
				if carriesReportField(fields) {
					return Report{}, Binding{}, err
				}
				continue
			}
			bound, made, err := bindEvidence(report, *demand)
			if err != nil {
				if carriesReportField(fields) {
					return Report{}, Binding{}, err
				}
				continue
			}
			report, binding = bound, made
		}
		report = report.Normalize()
		if err := report.Validate(); err != nil {
			if carriesReportField(fields) {
				return Report{}, Binding{}, err
			}
			continue
		}
		return report, binding, nil
	}
	return Report{}, Binding{}, fmt.Errorf("%w: read %d bytes, %d candidate objects: %s",
		ErrNoReport, len(raw), len(spans), quoteForDiagnostic(raw))
}

// carriesReportField reports whether the decoded object holds any of
// reportFields as a key, whatever that key's value is. An object that holds one
// is a report an agent wrote, so a defect in it is reported rather than being a
// reason to look for a different object earlier in the text.
func carriesReportField(fields map[string]json.RawMessage) bool {
	for key := range fields {
		for _, name := range reportFields {
			if strings.EqualFold(key, name) {
				return true
			}
		}
	}
	return false
}

// span is a half-open byte range within the raw output.
type span struct{ start, end int }

// objectSpans returns every balanced top-level {...} range in raw, in the
// order they start, skipping braces inside JSON string literals and the
// escapes inside those literals.
//
// Only top-level spans are returned: an object nested inside another is part
// of its parent's span and is not offered separately. So text whose braces do
// not pair the way JSON would, such as prose holding one unmatched brace before
// the report, can absorb the report into a span that never returns to depth
// zero, and is therefore never returned at all, or into one that closes around
// text that is not valid JSON. A quote in prose does not do this: string
// literals are tracked only at depth greater than zero, so a quote outside
// every span is ignored. Either way the report is not among the candidates
// ParseReport sees, which yields a refusal when no earlier span holds a valid
// report and that earlier report otherwise. ParseReport states the limit.
func objectSpans(raw string) []span {
	var spans []span
	depth, start := 0, 0
	inString, escaped := false, false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			// A quote outside any object is not part of a JSON string this
			// scanner needs to track, and treating it as one would let prose
			// swallow the report far more often than an unmatched brace does.
			if depth > 0 {
				inString = true
			}
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			if depth == 0 {
				continue
			}
			depth--
			if depth == 0 {
				spans = append(spans, span{start: start, end: i + 1})
			}
		}
	}
	return spans
}

// quoteForDiagnostic renders raw for an error message, shortened so a refusal
// stays readable while still showing what was actually read.
func quoteForDiagnostic(raw string) string {
	const limit = 200
	if len(raw) <= limit {
		return strconv.Quote(raw)
	}
	return strconv.Quote(raw[:limit]) + fmt.Sprintf(" ... (%d more bytes)", len(raw)-limit)
}
