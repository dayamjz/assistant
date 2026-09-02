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
// into a span that does not decode. That residual gap yields a refusal and
// never a different report; objectSpans states it in full.
//
// Candidates are tried from the last in the text backwards, because an agent
// asked for a report ends with it, and prose before it may quote an example.
// Each candidate is resolved by exactly one of these rules:
//
//   - A span that is not a valid JSON object, or that does not decode into a
//     Report, is skipped and the scan continues with the candidate before it.
//     Fields this package does not recognize are ignored rather than refused,
//     so a stage that added a field of its own to its prompt does not lose an
//     otherwise valid set of findings.
//   - A span that decodes and validates is returned, normalized. This is the
//     only path that returns a report.
//   - A span that decodes, fails validation, and carries at least one of the
//     keys "summary", "findings", or "risk" is the report the agent meant to
//     write, so its *ValidationError is returned and no earlier candidate is
//     tried. Presence of the key is what counts, not its value: an empty
//     summary is a report with a defect, not an object that is not a report.
//   - A span that decodes, fails validation, and carries none of those keys is
//     not a report at all, so the scan continues with the candidate before it.
//     Its refusal is kept only in case no candidate turns out to be a report.
//
// It refuses rather than guesses in every other case. If no candidate decodes,
// the error wraps ErrNoReport. If candidates decoded but none was a report and
// none validated, the error is the *ValidationError from the last candidate in
// the text that decoded.
func ParseReport(raw string) (Report, error) {
	if len(raw) > MaxRawBytes {
		return Report{}, fmt.Errorf("%w: %d bytes, limit %d", ErrRawTooLarge, len(raw), MaxRawBytes)
	}
	spans := objectSpans(raw)
	var firstInvalid error
	for i := len(spans) - 1; i >= 0; i-- {
		object := []byte(raw[spans[i].start:spans[i].end])
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(object, &fields); err != nil {
			continue
		}
		var report Report
		if err := json.Unmarshal(object, &report); err != nil {
			continue
		}
		report = report.Normalize()
		if err := report.Validate(); err != nil {
			if carriesReportField(fields) {
				return Report{}, err
			}
			if firstInvalid == nil {
				firstInvalid = err
			}
			continue
		}
		return report, nil
	}
	if firstInvalid != nil {
		return Report{}, firstInvalid
	}
	return Report{}, fmt.Errorf("%w: read %d bytes, %d candidate objects: %s",
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
// not pair the way JSON would, such as prose holding one unmatched brace or
// one unmatched double quote before the report, can absorb the report into a
// span that does not decode. That produces a refusal from ParseReport, never a
// different report.
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
