package findings_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
)

// The change under review in every test here: one touched path, and one path
// the change did not touch that a finding may want to reach for.
const (
	reviewedRevision = "c0ffeeb4be"
	touchedPath      = "internal/total/total.go"
	callerPath       = "internal/report/render.go"
)

// demand is the run's side of the review: what these tests never let the
// reviewer decide for itself.
func demand() findings.Demand {
	return findings.Demand{Revision: reviewedRevision, Touched: []string{touchedPath}}
}

// printed renders a report as the bytes a reviewer prints. Building it by
// marshalling the wire type is what keeps these tests describing shapes the
// parser can actually be handed: a field spelled differently here would not
// reach the parser under the name the reviewer would have used.
func printed(t *testing.T, r findings.Report) string {
	t.Helper()
	encoded, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("encoding the report a reviewer would print: %v", err)
	}
	return "I read the change and here is what I found.\n\n```json\n" + string(encoded) + "\n```\n"
}

// crossFileFinding is the finding the discriminator is about: a claim about a
// caller the change does not touch, which is most of what an independent
// review is for and exactly what a reviewer that read only the diff cannot
// support.
func crossFileFinding() findings.Finding {
	return findings.Finding{
		ID:          "caller-sums-twice",
		Severity:    findings.SeverityError,
		Action:      findings.ActionFix,
		Location:    findings.Location{Path: callerPath, Line: 42},
		Description: "The caller adds the same slice a second time now that Total returns it summed.",
	}
}

// review is a well-formed review report of the right revision, declaring read
// and holding found.
func review(read []string, found ...findings.Finding) findings.Report {
	return findings.Report{
		Summary:  "One pass over the change.",
		Revision: reviewedRevision,
		Read:     read,
		Findings: found,
	}
}

// findingByID returns the finding carrying id, and fails when the report holds
// no such finding.
func findingByID(t *testing.T, r findings.Report, id string) findings.Finding {
	t.Helper()
	for _, f := range r.Findings {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("the bound report holds no finding %q: %+v", id, r.Findings)
	return findings.Finding{}
}

// TestDeclaringTheWholeDiffForfeitsAFindingReachingPastIt is the case the rule
// exists for. The reviewer declares exactly the paths the change touched,
// which a naive rule would accept and which the run could have derived without
// asking, and then makes a claim about a caller it never said it read.
func TestDeclaringTheWholeDiffForfeitsAFindingReachingPastIt(t *testing.T) {
	t.Parallel()
	raw := printed(t, review([]string{touchedPath}, crossFileFinding()))

	report, binding, err := findings.ParseReviewReport(raw, demand())
	if err != nil {
		t.Fatalf("the report itself is well formed and should parse: %v", err)
	}
	if got := report.Fixable(); len(got) != 0 {
		t.Fatalf("a finding the evidence set does not support reached the fix loop: %+v", got)
	}
	if len(binding.Refused) != 1 {
		t.Fatalf("expected one refusal, got %+v", binding.Refused)
	}
	if binding.Refused[0].Path != callerPath {
		t.Errorf("the refusal names %q, want the path the finding reached for, %q",
			binding.Refused[0].Path, callerPath)
	}
	if binding.Refused[0].Finding.Description != crossFileFinding().Description {
		t.Errorf("the refusal should quote the claim as written, got %q",
			binding.Refused[0].Finding.Description)
	}
	if binding.ReadBeyondChange() {
		t.Errorf("a reviewer that declared only the touched paths read nothing beyond them, got %q",
			binding.Beyond)
	}
	refused := findingByID(t, report, "caller-sums-twice")
	if refused.Action != findings.ActionNote {
		t.Errorf("the refused finding is %q in the report, want it recorded as a note", refused.Action)
	}
	if !strings.Contains(refused.Description, callerPath) {
		t.Errorf("the refusal should be reported with the path it named, got %q", refused.Description)
	}
	if !strings.Contains(refused.Description, crossFileFinding().Description) {
		t.Errorf("the refusal should quote what the review said, got %q", refused.Description)
	}
}

// TestReadingPastTheDiffKeepsTheSameFinding is the control for the test above.
// One thing differs, the path the reviewer declared reading, and the same
// finding survives, so the refusal there is the evidence rule firing and not
// some other property of that report.
func TestReadingPastTheDiffKeepsTheSameFinding(t *testing.T) {
	t.Parallel()
	raw := printed(t, review([]string{touchedPath, callerPath}, crossFileFinding()))

	report, binding, err := findings.ParseReviewReport(raw, demand())
	if err != nil {
		t.Fatalf("parsing a review that read the path it reported on: %v", err)
	}
	if len(binding.Refused) != 0 {
		t.Fatalf("nothing should have been refused, got %+v", binding.Refused)
	}
	if got := report.Fixable(); len(got) != 1 || got[0].ID != "caller-sums-twice" {
		t.Fatalf("the supported finding should still be fix-eligible, got %+v", got)
	}
	if !binding.ReadBeyondChange() {
		t.Fatalf("the reviewer read a path the change did not touch, Beyond is %q", binding.Beyond)
	}
	if len(binding.Beyond) != 1 || binding.Beyond[0] != callerPath {
		t.Errorf("Beyond is %q, want exactly the untouched path the reviewer read", binding.Beyond)
	}
}

// TestACitationOutsideTheEvidenceSetRefusesTheFinding covers the other way a
// finding reaches past the change: it is located in the diff and rests on code
// outside it.
func TestACitationOutsideTheEvidenceSetRefusesTheFinding(t *testing.T) {
	t.Parallel()
	cites := findings.Finding{
		ID:          "breaks-the-caller",
		Severity:    findings.SeverityError,
		Action:      findings.ActionFix,
		Location:    findings.Location{Path: touchedPath, Line: 8},
		Cites:       []string{callerPath},
		Description: "Returning a sum here breaks the one caller, which adds it again.",
	}
	raw := printed(t, review([]string{touchedPath}, cites))

	report, binding, err := findings.ParseReviewReport(raw, demand())
	if err != nil {
		t.Fatalf("parsing the review: %v", err)
	}
	if len(binding.Refused) != 1 || binding.Refused[0].Path != callerPath {
		t.Fatalf("expected one refusal naming the cited path, got %+v", binding.Refused)
	}
	if got := report.Fixable(); len(got) != 0 {
		t.Fatalf("a finding resting on unread code reached the fix loop: %+v", got)
	}
}

// TestTheBindingCannotMakeAnInvalidReportValid is the invariant the extra rule
// may only add to: the review path refuses exactly what the ordinary path
// refuses. Both shapes the binding rewrites a finding into are covered,
// because both write text of their own where the reviewer wrote none, and
// either could otherwise stand in for a description that was never there.
//
// The same bytes are put to ParseReport in each case, so what is asserted is
// the two paths agreeing rather than the review path merely failing somehow.
func TestTheBindingCannotMakeAnInvalidReportValid(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		f    findings.Finding
	}{
		{"a finding the binding would demote", findings.Finding{
			ID:          "says-nothing",
			Severity:    findings.SeverityError,
			Action:      findings.ActionFix,
			Description: "  ",
		}},
		{"a finding the binding would refuse", findings.Finding{
			ID:          "says-nothing",
			Severity:    findings.SeverityError,
			Action:      findings.ActionFix,
			Location:    findings.Location{Path: callerPath, Line: 42},
			Description: "  ",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw := printed(t, review([]string{touchedPath}, tc.f))

			report, _, err := findings.ParseReviewReport(raw, demand())
			var bound *findings.ValidationError
			if !errors.As(err, &bound) {
				t.Fatalf("ParseReviewReport returned (%+v, %v), want a *ValidationError", report, err)
			}
			if !bound.HasDefect(findings.DefectMissingDescription) {
				t.Fatalf("the review path reports %v, want the description the reviewer never wrote",
					bound.Flaws)
			}
			var ordinary *findings.ValidationError
			if _, err := findings.ParseReport(raw); !errors.As(err, &ordinary) ||
				!ordinary.HasDefect(findings.DefectMissingDescription) {
				t.Fatalf("ParseReport is %v, want the same refusal the review path made", err)
			}
		})
	}
}

// TestARefusedNoteKeepsALocationTheEvidenceSetHolds pins what the refused note
// is allowed to carry. The two cases differ in the one thing the rule asks
// about, whether the location's own path is in the evidence set, and they are
// answered differently, so neither keeping every location nor dropping every
// one passes both. The first is the case worth having: a finding sitting in
// code the reviewer did read, refused for the caller it reaches to, still
// tells a person where to look.
//
// What this cannot separate is the value predicate from a predicate on which
// path decided the refusal, because firstOutside scans the location first and
// so the two agree on every input reachable today. That is the reason the rule
// is written on the value rather than tested into it; refusedNote says why.
func TestARefusedNoteKeepsALocationTheEvidenceSetHolds(t *testing.T) {
	t.Parallel()
	const thirdPath = "internal/total/helper.go"
	for _, tc := range []struct {
		name     string
		located  findings.Location
		read     []string
		want     findings.Location
		refusing string
	}{
		{
			name:     "located in code the reviewer read",
			located:  findings.Location{Path: touchedPath, Line: 8},
			read:     []string{touchedPath},
			want:     findings.Location{Path: touchedPath, Line: 8},
			refusing: callerPath,
		},
		{
			name:     "located in code the reviewer did not read",
			located:  findings.Location{Path: thirdPath, Line: 8},
			read:     []string{touchedPath},
			want:     findings.Location{},
			refusing: thirdPath,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := findings.Finding{
				ID:          "breaks-the-caller",
				Severity:    findings.SeverityError,
				Action:      findings.ActionFix,
				Location:    tc.located,
				Cites:       []string{callerPath},
				Description: "Returning a sum here breaks the one caller, which adds it again.",
			}
			report, binding, err := findings.ParseReviewReport(printed(t, review(tc.read, f)), demand())
			if err != nil {
				t.Fatalf("parsing the review: %v", err)
			}
			if len(binding.Refused) != 1 || binding.Refused[0].Path != tc.refusing {
				t.Fatalf("expected one refusal naming %q, got %+v", tc.refusing, binding.Refused)
			}
			note := findingByID(t, report, "breaks-the-caller")
			if note.Location != tc.want {
				t.Errorf("the refused note is located at %+v, want %+v", note.Location, tc.want)
			}
			if note.Action != findings.ActionNote {
				t.Errorf("a refused note is %q, want %q", note.Action, findings.ActionNote)
			}
		})
	}
}

// TestACitationInsideTheEvidenceSetKeepsTheFinding is that case's control.
func TestACitationInsideTheEvidenceSetKeepsTheFinding(t *testing.T) {
	t.Parallel()
	cites := findings.Finding{
		ID:          "breaks-the-caller",
		Severity:    findings.SeverityError,
		Action:      findings.ActionFix,
		Location:    findings.Location{Path: touchedPath, Line: 8},
		Cites:       []string{callerPath},
		Description: "Returning a sum here breaks the one caller, which adds it again.",
	}
	raw := printed(t, review([]string{touchedPath, callerPath}, cites))

	report, binding, err := findings.ParseReviewReport(raw, demand())
	if err != nil {
		t.Fatalf("parsing the review: %v", err)
	}
	if len(binding.Refused) != 0 {
		t.Fatalf("nothing should have been refused, got %+v", binding.Refused)
	}
	if got := report.Fixable(); len(got) != 1 {
		t.Fatalf("the cited finding should be fix-eligible, got %+v", got)
	}
}

// TestPathsAreMatchedExactly pins the comparison the guidance asks the
// reviewer to write for. A path that differs only in letter case names a
// different path, which costs the reviewer the finding rather than buying it
// one.
func TestPathsAreMatchedExactly(t *testing.T) {
	t.Parallel()
	recased := "Internal/Report/Render.go"
	raw := printed(t, review([]string{touchedPath, recased}, crossFileFinding()))

	_, binding, err := findings.ParseReviewReport(raw, demand())
	if err != nil {
		t.Fatalf("parsing the review: %v", err)
	}
	if len(binding.Refused) != 1 || binding.Refused[0].Path != callerPath {
		t.Fatalf("a re-cased path should name a different path, got %+v", binding.Refused)
	}
}

// TestAnEmptyEvidenceSetRefusesEveryLocatedFinding checks the degenerate
// declaration. It is not an error, and it supports nothing.
func TestAnEmptyEvidenceSetRefusesEveryLocatedFinding(t *testing.T) {
	t.Parallel()
	inTheDiff := findings.Finding{
		ID:          "off-by-one",
		Severity:    findings.SeverityError,
		Action:      findings.ActionFix,
		Location:    findings.Location{Path: touchedPath, Line: 10},
		Description: "The loop stops one element short.",
	}
	raw := printed(t, review(nil, inTheDiff))

	report, binding, err := findings.ParseReviewReport(raw, demand())
	if err != nil {
		t.Fatalf("a report declaring nothing read is still a report: %v", err)
	}
	if len(binding.Refused) != 1 || binding.Refused[0].Path != touchedPath {
		t.Fatalf("expected the finding refused for want of evidence, got %+v", binding.Refused)
	}
	if got := report.Fixable(); len(got) != 0 {
		t.Fatalf("a finding no declared reading supports reached the fix loop: %+v", got)
	}
}

// TestAFindingRestingOnNothingBecomesANote covers the third rule: a finding
// with no location and no citation.
func TestAFindingRestingOnNothingBecomesANote(t *testing.T) {
	t.Parallel()
	unlocated := findings.Finding{
		ID:          "vague",
		Severity:    findings.SeverityError,
		Action:      findings.ActionFix,
		Description: "Error handling in this change is weak.",
	}
	raw := printed(t, review([]string{touchedPath}, unlocated))

	report, binding, err := findings.ParseReviewReport(raw, demand())
	if err != nil {
		t.Fatalf("parsing the review: %v", err)
	}
	if len(binding.Demoted) != 1 || binding.Demoted[0].ID != "vague" {
		t.Fatalf("expected the unlocated finding recorded as demoted, got %+v", binding.Demoted)
	}
	if got := findingByID(t, report, "vague"); got.Action != findings.ActionNote {
		t.Fatalf("an unsupported finding is %q, want note", got.Action)
	}
	if report.HasParked() {
		t.Errorf("a demoted finding must not hold the stage: %+v", report.Parked())
	}
}

// TestP3OutranksTheDemotion is the boundary the PRD draws around the rule
// above. A finding whose action the reviewer never stated is an ask P3 made,
// and the demotion may not quietly take that hold away. The three inputs are
// P3's three ways in, and none of them names a location either.
func TestP3OutranksTheDemotion(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		action findings.Action
	}{
		{"no action field at all", ""},
		{"an action nobody recognizes", "autofix"},
		{"an action that is only space", "   "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw := printed(t, review([]string{touchedPath}, findings.Finding{
				ID:          "unclassified",
				Severity:    findings.SeverityError,
				Action:      tc.action,
				Description: "Something about this change worries me.",
			}))

			report, binding, err := findings.ParseReviewReport(raw, demand())
			if err != nil {
				t.Fatalf("parsing the review: %v", err)
			}
			if len(binding.Demoted) != 0 {
				t.Fatalf("P3's ask was demoted: %+v", binding.Demoted)
			}
			got := findingByID(t, report, "unclassified")
			if got.Action != findings.ActionAsk {
				t.Fatalf("action is %q, want ask: P3 outranks the evidence demotion", got.Action)
			}
			if !report.HasParked() {
				t.Errorf("an unclassified finding must still hold the stage for a person")
			}
		})
	}
}

// TestARefusalAppliesWhateverTheActionWas is the other side of the boundary
// P3 draws. The carve-out is on the demotion alone: a claim about code the
// reviewer never said it read is unsupported whoever was going to resolve it,
// so a finding P3 would have made an ask is refused for the path it named and
// reported as refused rather than held.
func TestARefusalAppliesWhateverTheActionWas(t *testing.T) {
	t.Parallel()
	unclassified := crossFileFinding()
	unclassified.Action = ""
	raw := printed(t, review([]string{touchedPath}, unclassified))

	report, binding, err := findings.ParseReviewReport(raw, demand())
	if err != nil {
		t.Fatalf("parsing the review: %v", err)
	}
	if len(binding.Refused) != 1 || binding.Refused[0].Path != callerPath {
		t.Fatalf("expected the finding refused for the path it named, got %+v", binding.Refused)
	}
	if got := findingByID(t, report, "caller-sums-twice"); got.Action != findings.ActionNote {
		t.Fatalf("a refused finding is %q, want it reported as a note", got.Action)
	}
	if report.HasParked() {
		t.Errorf("a refused finding must not hold the stage: %+v", report.Parked())
	}
}

// TestAStatedAskWithNothingBehindItIsDemoted is the other half of the boundary
// above. The two findings reach the parser as the same value after P3 has run,
// and only the action the reviewer wrote tells them apart.
func TestAStatedAskWithNothingBehindItIsDemoted(t *testing.T) {
	t.Parallel()
	raw := printed(t, review([]string{touchedPath}, findings.Finding{
		ID:          "judgment",
		Severity:    findings.SeverityWarning,
		Action:      findings.ActionAsk,
		Description: "This change may not be what you wanted.",
	}))

	report, binding, err := findings.ParseReviewReport(raw, demand())
	if err != nil {
		t.Fatalf("parsing the review: %v", err)
	}
	if len(binding.Demoted) != 1 {
		t.Fatalf("a stated ask resting on nothing should be demoted, got %+v", binding.Demoted)
	}
	if got := findingByID(t, report, "judgment"); got.Action != findings.ActionNote {
		t.Fatalf("action is %q, want note", got.Action)
	}
}

// TestAReportOfAnotherRevisionIsRefused covers the first rule. A reading of
// some other commit is not a review of this change.
func TestAReportOfAnotherRevisionIsRefused(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		revision string
	}{
		{"another commit", "d15ea5e000"},
		{"no revision at all", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := review([]string{touchedPath})
			r.Revision = tc.revision
			raw := printed(t, r)

			_, _, err := findings.ParseReviewReport(raw, demand())
			if !errors.Is(err, findings.ErrWrongRevision) {
				t.Fatalf("error is %v, want ErrWrongRevision", err)
			}
			if !strings.Contains(err.Error(), reviewedRevision) {
				t.Errorf("the refusal should quote the commit the run asked about, got %q", err)
			}
		})
	}
}

// TestTheEvidenceComparisonIsReportedWhenItFindsNothing is the part that may
// not be invisible. An evidence set equal to the touched paths is permitted,
// and it still leaves a fact in the report a person can count across runs.
func TestTheEvidenceComparisonIsReportedWhenItFindsNothing(t *testing.T) {
	t.Parallel()
	raw := printed(t, review([]string{touchedPath}))

	report, binding, err := findings.ParseReviewReport(raw, demand())
	if err != nil {
		t.Fatalf("parsing the review: %v", err)
	}
	if binding.ReadBeyondChange() {
		t.Fatalf("Beyond should be empty, got %q", binding.Beyond)
	}
	if !report.AllNotes() {
		t.Fatalf("a review that found nothing must still be approved as it stands: %+v", report.Findings)
	}
	if len(report.Findings) != 1 {
		t.Fatalf("expected the evidence comparison reported as one finding, got %+v", report.Findings)
	}
	if got := report.Findings[0].Description; !strings.Contains(got, "beyond") {
		t.Errorf("the evidence finding should say the review read nothing beyond the change, got %q", got)
	}
}

// TestTheEvidenceComparisonNamesWhatWasReadBeyondTheChange is the same fact in
// the other direction.
func TestTheEvidenceComparisonNamesWhatWasReadBeyondTheChange(t *testing.T) {
	t.Parallel()
	raw := printed(t, review([]string{touchedPath, callerPath}))

	report, _, err := findings.ParseReviewReport(raw, demand())
	if err != nil {
		t.Fatalf("parsing the review: %v", err)
	}
	if len(report.Findings) != 1 {
		t.Fatalf("expected one evidence finding, got %+v", report.Findings)
	}
	if got := report.Findings[0].Description; !strings.Contains(got, callerPath) {
		t.Errorf("the evidence finding should name the path read beyond the change, got %q", got)
	}
}

// TestADemandThatCannotBeAnsweredIsRefusedBeforeTheReviewer checks that both
// halves of the review path refuse the same demands, so a caller learns before
// it spends an invocation rather than after.
func TestADemandThatCannotBeAnsweredIsRefusedBeforeTheReviewer(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		d    findings.Demand
		want error
	}{
		{"no revision", findings.Demand{Touched: []string{touchedPath}}, findings.ErrNoRevision},
		{"blank revision", findings.Demand{Revision: "  ", Touched: []string{touchedPath}}, findings.ErrNoRevision},
		{"no touched paths", findings.Demand{Revision: reviewedRevision}, findings.ErrNoTouched},
		{"only blank touched paths",
			findings.Demand{Revision: reviewedRevision, Touched: []string{"", "  "}}, findings.ErrNoTouched},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := tc.d.Validate(); !errors.Is(err, tc.want) {
				t.Errorf("Demand.Validate is %v, want %v", err, tc.want)
			}
			if _, err := tc.d.Guidance(); !errors.Is(err, tc.want) {
				t.Errorf("Guidance is %v, want %v", err, tc.want)
			}
			raw := printed(t, review([]string{touchedPath}))
			if _, _, err := findings.ParseReviewReport(raw, tc.d); !errors.Is(err, tc.want) {
				t.Errorf("ParseReviewReport is %v, want %v", err, tc.want)
			}
		})
	}
}

// TestEmptyIsTrueOnlyForADemandCarryingNothing pins the question a caller
// outside this package asks instead of reading Demand's fields: whether a
// demand was supplied at all. It is narrower than Validate, so a demand half
// filled in answers false and is refused where none belongs rather than read
// as absent.
func TestEmptyIsTrueOnlyForADemandCarryingNothing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		d    findings.Demand
		want bool
	}{
		{"the zero demand", findings.Demand{}, true},
		{"an empty touched list", findings.Demand{Touched: []string{}}, true},
		{"a revision alone", findings.Demand{Revision: reviewedRevision}, false},
		{"a blank revision alone", findings.Demand{Revision: "  "}, false},
		{"touched paths alone", findings.Demand{Touched: []string{touchedPath}}, false},
		{"only blank touched paths", findings.Demand{Touched: []string{"  "}}, false},
		{"a demand that validates",
			findings.Demand{Revision: reviewedRevision, Touched: []string{touchedPath}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.d.Empty(); got != tc.want {
				t.Errorf("Demand.Empty is %v, want %v", got, tc.want)
			}
		})
	}
}

// TestGuidanceNamesTheRevisionAndEveryTouchedPath checks that the reviewer is
// told the things it will be held to, since a rule nobody stated is a trap
// rather than a check.
func TestGuidanceNamesTheRevisionAndEveryTouchedPath(t *testing.T) {
	t.Parallel()
	d := findings.Demand{
		Revision: reviewedRevision,
		Touched:  []string{touchedPath, "internal/total/total_test.go", touchedPath},
	}
	text, err := d.Guidance()
	if err != nil {
		t.Fatalf("Guidance: %v", err)
	}
	for _, want := range []string{
		reviewedRevision, touchedPath, "internal/total/total_test.go",
		`"revision"`, `"read"`, `"cites"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the guidance does not mention %q:\n%s", want, text)
		}
	}
	if got := strings.Count(text, "  - "+touchedPath+"\n"); got != 1 {
		t.Errorf("a path listed twice should be asked about once, it appears %d times", got)
	}
}

// TestParseReportIsUnchangedByTheEvidenceRule keeps the rule where the PRD
// puts it. The other eight stages answer to none of it, so the same output
// read as an ordinary stage report keeps its finding as written.
func TestParseReportIsUnchangedByTheEvidenceRule(t *testing.T) {
	t.Parallel()
	raw := printed(t, review([]string{touchedPath}, crossFileFinding()))

	report, err := findings.ParseReport(raw)
	if err != nil {
		t.Fatalf("ParseReport: %v", err)
	}
	if got := report.Fixable(); len(got) != 1 || got[0].ID != "caller-sums-twice" {
		t.Fatalf("ParseReport should return the finding as written, got %+v", got)
	}
	if len(report.Findings) != 1 {
		t.Fatalf("ParseReport should add nothing to the report, got %+v", report.Findings)
	}
}
