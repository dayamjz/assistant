package session

import (
	"fmt"
	"testing"

	"github.com/dayamjz/assistant/internal/taskstate"
)

// mappingRule is the package documentation's probe rule as a predicate over
// a reason table: Live only for the held handle, Dead only where absence is
// proven, and every failure to read Unobserved. It is applied to the real
// table and to a counterfeit, so a rule that checks nothing is caught.
func mappingRule(table []struct {
	reason   Reason
	liveness taskstate.Liveness
}) error {
	want := map[Reason]taskstate.Liveness{
		ReasonHeld:        taskstate.Live,
		ReasonExited:      taskstate.Dead,
		ReasonAbsent:      taskstate.Dead,
		ReasonReplaced:    taskstate.Dead,
		ReasonUnheld:      taskstate.Unobserved,
		ReasonForeignHome: taskstate.Unobserved,
		ReasonMalformed:   taskstate.Unobserved,
		ReasonUnreadable:  taskstate.Unobserved,
	}
	seen := map[Reason]bool{}
	for _, row := range table {
		liveness, known := want[row.reason]
		if !known {
			return fmt.Errorf("reason %s has no rule", row.reason)
		}
		if seen[row.reason] {
			return fmt.Errorf("reason %s has two rows", row.reason)
		}
		seen[row.reason] = true
		if row.liveness != liveness {
			return fmt.Errorf("reason %s maps to %s, the rule says %s", row.reason, row.liveness, liveness)
		}
	}
	for reason := range want {
		if !seen[reason] {
			return fmt.Errorf("reason %s has a rule and no row", reason)
		}
	}
	return nil
}

// The mapping is exhaustive over the closed set and obeys the rule, and the
// rule can fail: a counterfeit table that reads a read failure as Dead is
// refused by it, as is one missing a row.
func TestEveryReasonMapsToTheLivenessTheRuleStates(t *testing.T) {
	if err := mappingRule(reasons); err != nil {
		t.Fatalf("the reason table breaks the rule: %v", err)
	}
	if got := Reasons(); len(got) != len(reasons) {
		t.Fatalf("Reasons lists %d, the table has %d", len(got), len(reasons))
	}
	if (Reason("nobody-wrote-this")).Liveness() != taskstate.Unobserved {
		t.Error("a reason outside the table reads as something other than Unobserved")
	}
	for _, r := range Reasons() {
		if got := observe(r, "x"); got.Liveness != r.Liveness() || got.Reason != r {
			t.Errorf("observe(%s) built %+v", r, got)
		}
	}

	counterfeit := append([]struct {
		reason   Reason
		liveness taskstate.Liveness
	}(nil), reasons...)
	for i := range counterfeit {
		if counterfeit[i].reason == ReasonUnreadable {
			counterfeit[i].liveness = taskstate.Dead
		}
	}
	if err := mappingRule(counterfeit); err == nil {
		t.Fatal("a table reading a probe error as Dead passed the rule")
	}
	short := reasons[:len(reasons)-1]
	if err := mappingRule(short); err == nil {
		t.Fatal("a table missing a reason passed the rule")
	}
}
