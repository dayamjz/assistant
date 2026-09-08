package outcomes

import "github.com/dayamjz/assistant/internal/machine"

// Built is one outcome as internal/machine declares it: the value that
// travels, whether the build treats it as an end to the run, and what the
// build tells a caller to do about it.
//
// It carries the action and Listed does not, because the two sides declare
// different amounts. The PRD's row requires an action of every outcome and
// says what none of them is, so the text has one owner and it is the build.
type Built struct {
	// Name is the value as it travels on the wire.
	Name string
	// Terminal is what machine.Outcome.Terminal reports.
	Terminal bool
	// Action is what machine.Outcome.NextAction answers.
	Action string
}

// FromBuild returns the outcomes internal/machine declares, in the order
// machine.Outcomes returns them.
func FromBuild() []Built {
	set := machine.Outcomes()
	out := make([]Built, 0, len(set))
	for _, o := range set {
		out = append(out, Built{Name: o.String(), Terminal: o.Terminal(), Action: o.NextAction()})
	}
	return out
}

// unrecognizedAction is what internal/machine answers for an outcome it has
// no row for. It is derived from the mechanism rather than written down here,
// by asking machine about a value the set does not contain, so a change to
// that sentence cannot leave a stale copy behind.
//
// A declared outcome answered with it has lost its row in machine's table.
// That reads to a caller like every other answer and is a different failure
// from an empty one, which is why the two are reported apart.
func unrecognizedAction(set []Built) string {
	absent := "not-an-outcome"
	for taken(set, absent) {
		absent += "-x"
	}
	return machine.Outcome(absent).NextAction()
}

// taken reports whether a name is one the build declares.
func taken(set []Built, name string) bool {
	for _, b := range set {
		if b.Name == name {
			return true
		}
	}
	return false
}
