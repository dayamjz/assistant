package graph

import "fmt"

// Op is the comparison a Guard performs. Guards are data rather than
// callbacks, so the set of comparisons is closed and every one of them can be
// checked against the key's declared kind when the graph is built.
type Op uint8

const (
	// OpInvalid is the zero Op and is never a valid declaration.
	OpInvalid Op = iota
	// OpEquals passes when the key equals the guard's value. Any kind.
	OpEquals
	// OpNotEquals passes when the key differs from the guard's value. Any kind.
	OpNotEquals
	// OpLessThan passes when the key is below the guard's value. KindInt only.
	OpLessThan
	// OpGreaterThan passes when the key is above the guard's value. KindInt only.
	OpGreaterThan
	// OpContains passes when the key's list holds the guard's text. The key
	// must be KindList and the guard's value KindText.
	OpContains
)

// String returns the name of the operator as it appears in errors.
func (o Op) String() string {
	switch o {
	case OpEquals:
		return "equals"
	case OpNotEquals:
		return "not-equals"
	case OpLessThan:
		return "less-than"
	case OpGreaterThan:
		return "greater-than"
	case OpContains:
		return "contains"
	case OpInvalid:
		return "invalid"
	default:
		return fmt.Sprintf("op(%d)", uint8(o))
	}
}

// Guard is a predicate over state, expressed as data. Because it is data and
// not a callback, the topology can be inspected, checked, and rendered without
// executing anything.
type Guard struct {
	// Key is the state key the predicate reads. It must be a declared key.
	// Guards are evaluated by the engine, not by a node, so the edge's source
	// node does not have to declare reading it.
	Key string
	// Op is the comparison performed.
	Op Op
	// Value is the right-hand side of the comparison.
	Value Value
}

// check reports whether the guard is well formed against a key declaration.
func (g Guard) check(spec Key) error {
	switch g.Op {
	case OpEquals, OpNotEquals:
		if g.Value.Kind() != spec.Kind {
			return fmt.Errorf("guard on key %q compares %s against a %s value",
				g.Key, spec.Kind, g.Value.Kind())
		}
	case OpLessThan, OpGreaterThan:
		if spec.Kind != KindInt || g.Value.Kind() != KindInt {
			return fmt.Errorf("guard operator %s on key %q requires an int key and an int value",
				g.Op, g.Key)
		}
	case OpContains:
		if spec.Kind != KindList || g.Value.Kind() != KindText {
			return fmt.Errorf("guard operator %s on key %q requires a list key and a text value",
				g.Op, g.Key)
		}
	case OpInvalid:
		return fmt.Errorf("guard on key %q declares no operator", g.Key)
	default:
		return fmt.Errorf("guard on key %q declares unknown operator %s", g.Key, g.Op)
	}
	return nil
}

// eval reports whether the guard passes against s. It is total for a built
// graph, because every declared key is always present in state.
func (g Guard) eval(s State) bool {
	current, ok := s.Get(g.Key)
	if !ok {
		return false
	}
	switch g.Op {
	case OpEquals:
		return current.Equal(g.Value)
	case OpNotEquals:
		return !current.Equal(g.Value)
	case OpLessThan:
		return current.num < g.Value.num
	case OpGreaterThan:
		return current.num > g.Value.num
	case OpContains:
		for _, item := range current.list {
			if item == g.Value.text {
				return true
			}
		}
		return false
	case OpInvalid:
		return false
	default:
		return false
	}
}

// Edge is a transition between two nodes, either unconditional or guarded by a
// predicate over state. Edges are data, so the topology can be inspected
// without executing anything.
type Edge struct {
	// From is the node the transition leaves.
	From string
	// To is the node the transition enters.
	To string
	// Guard, when non-nil, must pass for the edge to be taken. A nil Guard is
	// unconditional. At most one outgoing edge of a node may be unconditional,
	// and it must be declared last, because edges are evaluated in declaration
	// order and anything after an unconditional edge could never be reached.
	Guard *Guard
	// Rounds bounds how many times this edge may be traversed in one run.
	// It is required on every back edge and permitted on any edge. On a back
	// edge it is the round limit for the node the edge re-enters: Rounds of 3
	// admits three further executions of that node. Reaching it parks the run
	// rather than continuing.
	Rounds int
}

// clone returns a copy of the edge with its guard detached.
func (e Edge) clone() Edge {
	out := e
	if e.Guard != nil {
		g := *e.Guard
		out.Guard = &g
	}
	return out
}
