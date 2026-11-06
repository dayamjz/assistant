package findings_test

import (
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
)

// summaryNamingTheCaller is a review whose summary states the very claim a
// finding would be refused for: it is about a path the reviewer did not
// declare reading. A reviewer writes a paragraph like this whenever it
// explains itself, and it is the part of a review a person reads first.
func summaryNamingTheCaller(read []string, found ...findings.Finding) findings.Report {
	r := review(read, found...)
	r.Summary = "One pass over the change. " + callerPath +
		" adds the summed slice a second time, which is not a weakening."
	return r
}

// The summary passes through the binding untouched, and the evidence note says
// so. The first half is the mechanism as designed: a summary is prose with no
// path to test, so refusing it on a filename appearing in it would refuse an
// honest paragraph and pass a dishonest one. The second half is what was
// missing. A reader of the evidence note, which describes what the comparison
// cost the review's findings and nothing else, has every reason to believe the
// whole report was held to it, and this package's own standard is that a
// partial guarantee states what it does not cover.
//
// The positive control is the finding beside it, which names the same path and
// really is refused, so a note that said nothing happened to anything would
// fail here.
func TestTheEvidenceNoteSaysTheSummaryIsNotBoundToTheEvidenceSet(t *testing.T) {
	t.Parallel()
	raw := printed(t, summaryNamingTheCaller([]string{touchedPath}, crossFileFinding()))

	report, binding, err := findings.ParseReviewReport(raw, demand())
	if err != nil {
		t.Fatalf("parsing the review: %v", err)
	}

	// The control: the finding naming the undeclared path is refused, so the
	// evidence set really is doing something in this report.
	if len(binding.Refused) != 1 {
		t.Fatalf("the finding naming %s should be refused, got %+v", callerPath, binding.Refused)
	}
	refused := findingByID(t, report, "caller-sums-twice")
	if refused.Action != findings.ActionNote {
		t.Fatalf("a refused finding should be a note, got %q", refused.Action)
	}

	// The mechanism: the same claim in the summary is carried as written.
	if !strings.Contains(report.Summary, callerPath) {
		t.Errorf("the summary should pass through as the reviewer wrote it, got %q", report.Summary)
	}

	// The fix: something in the report says the summary was not held to the
	// test the finding was.
	if !saysTheSummaryIsUnbound(report) {
		t.Errorf("nothing in the bound report says the summary is not bound to the evidence set:\n%s",
			everyDescription(report))
	}
}

// The limit holds for every shape the comparison can report, because a reader
// meets the note in whichever shape their own run produced and the sentence
// before it is about findings in all four. The shapes are the evidence set
// declaring nothing, declaring part of the change, declaring all of it, and
// declaring past it.
func TestEveryShapeOfTheEvidenceNoteSaysTheSummaryIsNotBound(t *testing.T) {
	t.Parallel()
	alsoTouched := "internal/total/sum.go"
	wholeChange := findings.Demand{
		Revision: reviewedRevision,
		Touched:  []string{touchedPath, alsoTouched},
	}
	for _, shape := range []struct {
		name string
		read []string
	}{
		{"declaring nothing", nil},
		{"declaring part of the change", []string{touchedPath}},
		{"declaring all of the change", []string{touchedPath, alsoTouched}},
		{"declaring past the change", []string{touchedPath, alsoTouched, callerPath}},
	} {
		t.Run(shape.name, func(t *testing.T) {
			t.Parallel()
			report, _, err := findings.ParseReviewReport(
				printed(t, summaryNamingTheCaller(shape.read)), wholeChange)
			if err != nil {
				t.Fatalf("parsing the review: %v", err)
			}
			if !saysTheSummaryIsUnbound(report) {
				t.Errorf("the note for a set %s does not say the summary is unbound:\n%s",
					shape.name, everyDescription(report))
			}
		})
	}
}

// saysTheSummaryIsUnbound says whether the bound report tells a reader that
// the comparison was on findings and not on the stage's summary. It asks the
// report's own text, because that text is the whole of what a person gets.
func saysTheSummaryIsUnbound(r findings.Report) bool {
	for _, f := range r.Findings {
		d := strings.ToLower(f.Description)
		if strings.Contains(d, "summary") && strings.Contains(d, "not") {
			return true
		}
	}
	return false
}

// everyDescription is what a failure above prints, so the reader of a failure
// sees the text that was judged rather than a count.
func everyDescription(r findings.Report) string {
	var b strings.Builder
	for _, f := range r.Findings {
		b.WriteString("  - ")
		b.WriteString(f.Description)
		b.WriteString("\n")
	}
	return b.String()
}
