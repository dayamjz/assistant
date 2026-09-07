package outcomes

import (
	"strings"
	"testing"
)

// agreed is a pair the two sides state identically, which every case below
// changes exactly one thing about.
func agreed() ([]Listed, []Built) {
	listed := []Listed{
		{"checks-passed", true}, {"passed", true}, {"failed", true}, {"cancelled", true},
		{"decision", false}, {"executing", false},
	}
	built := make([]Built, 0, len(listed))
	for _, l := range listed {
		built = append(built, Built{Name: l.Name, Terminal: l.Terminal, Action: "do something about " + l.Name})
	}
	return listed, built
}

// TestTwoSidesThatSayTheSameThingAgree is the negative control for every case
// below: without it, a Compare that reported a difference for any input at all
// would pass all of them.
func TestTwoSidesThatSayTheSameThingAgree(t *testing.T) {
	listed, built := agreed()
	a := Compare(listed, built)
	if !a.OK() {
		t.Fatalf("two sides stating the same set do not agree:\n%s", a.Report())
	}
	if a.Report() != "" {
		t.Fatalf("an agreement reports %q", a.Report())
	}
}

// TestCompareFindsEveryWayTheTwoCanDisagree drives one difference at a time,
// and checks that the report names the outcome the difference is about. A
// pinning check that cannot fire is worse than none, because the tick it
// prints looks like the pair being held together.
func TestCompareFindsEveryWayTheTwoCanDisagree(t *testing.T) {
	cases := []struct {
		name    string
		change  func(*[]Listed, *[]Built)
		mention string
	}{
		{
			name:    "the PRD gains one the build does not",
			change:  func(l *[]Listed, _ *[]Built) { *l = append(*l, Listed{"merged", false}) },
			mention: "merged",
		},
		{
			name:    "the build gains one the PRD does not",
			change:  func(_ *[]Listed, b *[]Built) { *b = append(*b, Built{"merged", false, "merge it"}) },
			mention: "merged",
		},
		{
			name:    "one side renames an outcome",
			change:  func(_ *[]Listed, b *[]Built) { (*b)[0].Name = "checks-green" },
			mention: "checks-green",
		},
		{
			name:    "the build drops one",
			change:  func(_ *[]Listed, b *[]Built) { *b = (*b)[1:] },
			mention: "checks-passed",
		},
		{
			name:    "the same members in a different order",
			change:  func(_ *[]Listed, b *[]Built) { (*b)[0], (*b)[1] = (*b)[1], (*b)[0] },
			mention: "different orders",
		},
		{
			name:    "the build moves one out of its group",
			change:  func(_ *[]Listed, b *[]Built) { (*b)[3].Terminal = false },
			mention: "cancelled",
		},
		{
			name:    "the PRD moves one out of its group",
			change:  func(l *[]Listed, _ *[]Built) { (*l)[4].Terminal = true },
			mention: "decision",
		},
		{
			name:    "an outcome carries no next action",
			change:  func(_ *[]Listed, b *[]Built) { (*b)[2].Action = "  " },
			mention: "failed",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			listed, built := agreed()
			c.change(&listed, &built)
			a := Compare(listed, built)
			if a.OK() {
				t.Fatalf("%s and the two still agree", c.name)
			}
			if report := a.Report(); !strings.Contains(report, c.mention) {
				t.Fatalf("the report does not mention %q:\n%s", c.mention, report)
			}
		})
	}
}

// TestAnOutcomeAnsweredWithTheUnrecognizedActionIsActionless is the case an
// emptiness check alone reads past. internal/machine answers an outcome it has
// no row for with a sentence, so an outcome whose row went missing carries
// text rather than nothing and would otherwise look like it carries an action.
//
// The sentence is not written down here. It is asked of internal/machine the
// way the check asks for it, so this cannot pass against a copy that has gone
// stale.
func TestAnOutcomeAnsweredWithTheUnrecognizedActionIsActionless(t *testing.T) {
	listed, built := agreed()
	fallback := unrecognizedAction(built)
	if strings.TrimSpace(fallback) == "" {
		t.Fatal("internal/machine answers an outcome it does not recognize with nothing, so this case cannot be told from an empty one")
	}
	built[1].Action = fallback
	a := Compare(listed, built)
	if a.OK() {
		t.Fatal("an outcome answered with the unrecognized action still agrees")
	}
	if !strings.Contains(a.Report(), "passed") {
		t.Fatalf("the report does not name it:\n%s", a.Report())
	}
}

// TestOrderIsReportedOnlyWhenThereIsAnOrderToCompare keeps the report from
// burying the difference that matters. Two sets with different members have no
// order to disagree about, and reporting one would be a second complaint that
// follows from the first.
func TestOrderIsReportedOnlyWhenThereIsAnOrderToCompare(t *testing.T) {
	listed, built := agreed()
	built = built[1:]
	a := Compare(listed, built)
	if len(a.Unbuilt) != 1 || a.Unbuilt[0] != "checks-passed" {
		t.Fatalf("the missing member is reported as %v", a.Unbuilt)
	}
	if len(a.Reordered) != 0 {
		t.Fatalf("an order was compared between sets with different members: %v", a.Reordered)
	}
}
