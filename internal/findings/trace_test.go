package findings_test

import (
	"reflect"
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
	want := []findings.Trace{
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
	want := []findings.Trace{{Path: touchedPath}, {Reason: "an account of no path at all"}}
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
	want := []findings.Trace{{Path: "a.go", Reason: "because"}}
	if !reflect.DeepEqual(parsed.Traced, want) {
		t.Fatalf("the parsed report carries traces %+v, want %+v", parsed.Traced, want)
	}
}
