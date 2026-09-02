package findings

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// MaxRawBytes is the largest agent output ParseReport will look at. Beyond it
// the output is refused rather than scanned: the scan is linear in the input,
// but an unbounded input is still unbounded work, and a report this large is
// not a report.
const MaxRawBytes = 1 << 20

// ParseReport reads a stage report out of whatever an agent printed. The
// output is untrusted, so this is the only supported way to turn it into a
// Report.
//
// It accepts a bare JSON object, an object inside a fenced code block, and an
// object printed after prose, without treating those as three cases: it scans
// for balanced top-level JSON objects, tracking string literals so a brace
// inside a string cannot open or close one, and every one of those three
// shapes puts the object in exactly one such span.
//
// Candidates are tried from the last in the text backwards, because an agent
// asked for a report ends with it, and prose before it may quote an example.
// The first candidate that both decodes and validates is returned, normalized.
// Fields this package does not recognize are ignored rather than refused, so a
// stage that added a field of its own to its prompt does not lose an otherwise
// valid set of findings.
//
// It refuses rather than guesses in every other case. If no candidate decodes,
// the error wraps ErrNoReport. If one decoded but none validated, the error is
// the *ValidationError from the last candidate that decoded, which is the one
// most likely to be the report the agent meant.
func ParseReport(raw string) (Report, error) {
	if len(raw) > MaxRawBytes {
		return Report{}, fmt.Errorf("%w: %d bytes, limit %d", ErrRawTooLarge, len(raw), MaxRawBytes)
	}
	spans := objectSpans(raw)
	var firstInvalid error
	for i := len(spans) - 1; i >= 0; i-- {
		var report Report
		if err := json.Unmarshal([]byte(raw[spans[i].start:spans[i].end]), &report); err != nil {
			continue
		}
		report = report.Normalize()
		if err := report.Validate(); err != nil {
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
