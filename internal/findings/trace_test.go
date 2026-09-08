package findings_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
)

// A trace is the scope lens's input and not a claim about code, so it answers
// to none of the rules a finding answers to. This drives one through the same
// entry point a reviewer's report takes and asks what came out the other side:
// the trace survives, it is trimmed, and it is refused nothing even though it
// names a path the evidence set does not hold.
//
// The last part is what the test is for. A trace naming an unread path would
// be refused if the binding treated it as a claim, and it must not be: the
// path it names came from the run, so there is nothing in it for an evidence
// set to support.
func TestATraceSurvivesTheBindingAndIsBoundToNothing(t *testing.T) {
	t.Parallel()

	reported := review(nil)
	reported.Traced = []findings.Trace{
		{Path: "  " + touchedPath + "  ", Reason: "  the change asked for this  "},
		{Path: callerPath, Reason: "a path the evidence set does not hold"},
	}
	bound, binding, err := findings.ParseReviewReport(printed(t, reported), demand())
	if err != nil {
		t.Fatalf("a review report carrying traces was refused: %v", err)
	}
	want := findings.Traces{
		{Path: touchedPath, Reason: "the change asked for this"},
		{Path: callerPath, Reason: "a path the evidence set does not hold"},
	}
	if !reflect.DeepEqual(bound.Traced, want) {
		t.Fatalf("the bound report carries traces %+v, want %+v", bound.Traced, want)
	}
	if len(binding.Refused) != 0 {
		t.Fatalf("the binding refused %d of the report's parts, and a trace is not a claim "+
			"it may refuse: %+v", len(binding.Refused), binding.Refused)
	}
}

// A trace that accounts for nothing may not refuse the report it arrived in.
// The lens these answer is note-only and may not stop a run, so a malformed
// trace costs its path the observation it would have silenced and costs the
// report nothing at all.
//
// The wholly empty entry is dropped, because it names no path and gives no
// reason and so is indistinguishable from a trace nobody wrote. A half-filled
// one is kept, so a reader sees what the reviewer actually said.
func TestAMalformedTraceRefusesNothingAndIsKeptWhereItSaysAnything(t *testing.T) {
	t.Parallel()

	reported := review([]string{touchedPath})
	reported.Traced = []findings.Trace{
		{Path: "   ", Reason: "  "},
		{Path: touchedPath},
		{Reason: "an account of no path at all"},
	}
	bound, _, err := findings.ParseReviewReport(printed(t, reported), demand())
	if err != nil {
		t.Fatalf("a review report carrying malformed traces was refused: %v", err)
	}
	want := findings.Traces{{Path: touchedPath}, {Reason: "an account of no path at all"}}
	if !reflect.DeepEqual(bound.Traced, want) {
		t.Fatalf("the bound report carries traces %+v, want %+v", bound.Traced, want)
	}
}

// A stage that is not the review stage does not answer to the lens, and the
// field is carried rather than refused there too: ParseReport normalizes it on
// the same terms and requires nothing of it, so no stage loses a report over a
// field it did not mean to fill in.
func TestAnOrdinaryStageReportCarriesTracesWithoutAnsweringForThem(t *testing.T) {
	t.Parallel()

	reported := findings.Report{Summary: "a stage that is not the review stage"}
	reported.Traced = []findings.Trace{{Path: " a.go ", Reason: " because "}}
	parsed, err := findings.ParseReport(printed(t, reported))
	if err != nil {
		t.Fatalf("an ordinary stage report carrying a trace was refused: %v", err)
	}
	want := findings.Traces{{Path: "a.go", Reason: "because"}}
	if !reflect.DeepEqual(parsed.Traced, want) {
		t.Fatalf("the parsed report carries traces %+v, want %+v", parsed.Traced, want)
	}
}

// The account a reviewer writes reaches this package as whatever JSON the
// reviewer printed, and a shape this package cannot read may not cost the
// report the findings around it. The lens is note-only and may not stop a run,
// so an unreadable answer has exactly two permitted directions: the report is
// read as it stands, and the paths it might have accounted for keep their
// notes.
//
// The shapes are stated as the raw text a reviewer prints rather than as Go
// values marshalled back out, because a Go value can only spell shapes the
// wire type already admits and every case here is one it does not: the whole
// defect this covers is that a "traced" value that is not a list of objects
// used to fail json.Unmarshal and refuse the report entire.
//
// Each case carries a supported finding, and the assertion that it survived is
// what makes this a test about the report rather than about the field.
func TestATracedValueThisPackageCannotReadCostsTheReportNothing(t *testing.T) {
	t.Parallel()

	const supported = "sum-loses-the-last-element"
	for _, c := range []struct {
		name string
		// traced is the JSON text the reviewer printed for the field.
		traced string
		// want is what the decoder and Normalize make of it.
		want findings.Traces
		// silences says whether that answer accounts for touchedPath, which
		// is the one thing a trace is able to do and the one direction an
		// answer this package could not read may not fail in.
		silences bool
	}{
		{
			name:   "a list of bare paths, which answers the question without its reason",
			traced: `["` + touchedPath + `"]`,
			want:   findings.Traces{{Reason: `"` + touchedPath + `"`}},
		},
		{
			name:   "an object keyed by path, which is a list of accounts written as a map",
			traced: `{"` + touchedPath + `":"the intent asked for it"}`,
			want:   nil,
		},
		{
			name:   "a single bare path, which is one account with everything but the path left out",
			traced: `"` + touchedPath + `"`,
			want:   nil,
		},
		{
			name:   "a list holding values that are not objects at all",
			traced: `[7, null, ["` + touchedPath + `"]]`,
			want:   findings.Traces{{Reason: "7"}, {Reason: "null"}, {Reason: `["` + touchedPath + `"]`}},
		},
		{
			name:   "an object whose fields are not the ones a trace has",
			traced: `[{"path": 7, "reason": "the intent asked for it"}]`,
			want:   findings.Traces{{Reason: `{"path": 7, "reason": "the intent asked for it"}`}},
		},
		{
			name:     "the shape that was asked for",
			traced:   `[{"path": "` + touchedPath + `", "reason": "the intent asked for it"}]`,
			want:     findings.Traces{{Path: touchedPath, Reason: "the intent asked for it"}},
			silences: true,
		},
		{
			name:   "JSON null, which is how JSON says the reviewer answered nothing",
			traced: `null`,
			want:   nil,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			reported := review([]string{touchedPath}, findings.Finding{
				ID:          supported,
				Severity:    findings.SeverityError,
				Action:      findings.ActionFix,
				Location:    findings.Location{Path: touchedPath, Line: 4},
				Description: "The loop stops one element early, so the last value is never added.",
			})
			bound, _, err := findings.ParseReviewReport(tracedAs(t, reported, c.traced), demand())
			if err != nil {
				t.Fatalf("a review report whose traced value is %s was refused whole: %v", c.name, err)
			}
			if !reflect.DeepEqual(bound.Traced, c.want) {
				t.Fatalf("the bound report carries traces %+v, want %+v", bound.Traced, c.want)
			}
			found := findingByID(t, bound, supported)
			if found.Action != findings.ActionFix {
				t.Fatalf("the finding the review wrote came back as %q rather than the fix it "+
					"stated, so the traced value cost the report the findings around it",
					found.Action)
			}
			// What internal/scope's lens reads is a trace naming the path and
			// giving a reason, so that is what is asked here: only the shape
			// that was asked for may silence the path's note, and every shape
			// this package could not read leaves it standing.
			silenced := false
			for _, trace := range bound.Traced {
				if trace.Path == touchedPath && trace.Reason != "" {
					silenced = true
				}
			}
			if silenced != c.silences {
				t.Fatalf("a traced value that is %s accounts for %s: %t, want %t; an answer "+
					"this package could not read may not silence a path's note",
					c.name, touchedPath, silenced, c.silences)
			}
		})
	}
}

// tracedAs renders the bytes a reviewer prints, with "traced" carrying this
// JSON text verbatim and every other field marshalled from the wire type the
// way printed does. Splicing one field is what lets a test state a shape the
// Go type cannot hold while the rest of the report stays the shape the parser
// is really handed.
func tracedAs(t *testing.T, r findings.Report, traced string) string {
	t.Helper()
	r.Traced = nil
	encoded, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("encoding the report a reviewer would print: %v", err)
	}
	rest := strings.TrimPrefix(string(encoded), "{")
	if rest == string(encoded) {
		t.Fatalf("the encoded report is not a JSON object, so there is nothing to splice a "+
			"traced value into: %s", encoded)
	}
	object := `{"traced":` + traced + `,` + rest
	return "I read the change and here is what I found.\n\n```json\n" + object + "\n```\n"
}
