package machine_test

import (
	"testing"

	"github.com/dayamjz/assistant/internal/custody"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
)

// Every class, refusal and step internal/custody's closed sets hold has a
// row of its own, derived from those sets rather than listed here, so a
// class, reason or step added there fails this until it is phrased; none
// reads as the fallback rows. A plan carried out reads differently from the
// same plan printed for every class that acts, so a person can tell a
// change made from one proposed. The rows are in TablePhrases, which is what
// puts them under the vocabulary test's P13 check.
func TestEveryCustodyValueHasItsOwnRow(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P13)
	listed := map[string]bool{}
	for _, phrase := range machine.Phrases() {
		listed[phrase] = true
	}
	undetermined := machine.PlanPhrase(machine.Plan{Refusal: "never-a-reason"})
	unclassified := machine.PlanPhrase(machine.Plan{})
	if undetermined == unclassified {
		t.Fatal("the two fallback rows are one row, so a missing classification reads as a refusal")
	}
	unfamiliar := machine.StepPhrase(custody.Step{Kind: "never-a-step"})
	for _, class := range custody.Classes() {
		c := &custody.Classification{Class: class}
		planned := machine.PlanPhrase(machine.Plan{Classification: c})
		done := machine.PlanPhrase(machine.Plan{Classification: c, Applied: true})
		if planned == undetermined || planned == unclassified {
			t.Errorf("%s has no row", class)
		}
		if class.Acts() && planned == done {
			t.Errorf("%s reads the same carried out as printed: %q", class, planned)
		}
		for _, row := range []machine.Phrase{planned, done} {
			if !listed[row.String()] {
				t.Errorf("%s's row %q is not in TablePhrases", class, row)
			}
		}
		if row, ok := machine.CustodyPhrase(machine.Custody{Classification: c}); ok && !listed[row.String()] {
			t.Errorf("%s's status row %q is not in TablePhrases", class, row)
		}
	}
	for _, reason := range custody.Reasons() {
		row := machine.PlanPhrase(machine.Plan{Refusal: reason})
		if !listed[row.String()] {
			t.Errorf("%s's row %q is not in TablePhrases", reason, row)
		}
	}
	for _, kind := range custody.StepKinds() {
		row := machine.StepPhrase(custody.Step{Kind: kind})
		if row == unfamiliar || !listed[row.String()] {
			t.Errorf("the %s step has no row of its own in TablePhrases", kind)
		}
	}
	// A plan is blocked when refused or genuinely diverged, and not when it
	// waits on confirmation or has nothing to do.
	if !(machine.Plan{Refusal: custody.ReasonRefused}).Blocked() ||
		!(machine.Plan{Classification: &custody.Classification{Class: custody.ClassDiverged}}).Blocked() ||
		(machine.Plan{Classification: &custody.Classification{Class: custody.ClassBehind}}).Blocked() ||
		(machine.Plan{Classification: &custody.Classification{Class: custody.ClassYoursAlready}}).Blocked() {
		t.Fatal("Blocked does not read refusals and genuine divergence alone")
	}
}
