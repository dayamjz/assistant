package graph

import (
	"fmt"
	"sort"
)

// Graph is a built topology: nodes, edges, and the state keys they may touch.
// Every construction rule this package documents has already been checked by
// the time a Graph exists, so an executor never re-checks them at run time.
//
// A Graph is immutable and safe for concurrent use by any number of runs.
type Graph struct {
	start    string
	nodes    []Node
	index    map[string]int
	edges    []Edge
	outgoing [][]int
	back     []bool
	keys     []Key
	keyIndex map[string]Key
	answers  map[string]string
	reads    []map[string]struct{}
	writes   []map[string]struct{}
}

// Start returns the name of the node a run begins at.
func (g *Graph) Start() string { return g.start }

// Nodes returns the graph's nodes in declaration order. The result is a copy;
// mutating it does not change the graph.
func (g *Graph) Nodes() []Node {
	out := make([]Node, len(g.nodes))
	for i, n := range g.nodes {
		out[i] = n.clone()
	}
	return out
}

// Node returns the node with the given name. The second result is false when
// the graph has no such node.
func (g *Graph) Node(name string) (Node, bool) {
	i, ok := g.index[name]
	if !ok {
		return Node{}, false
	}
	return g.nodes[i].clone(), true
}

// Edges returns the graph's edges in declaration order. The result is a copy.
func (g *Graph) Edges() []Edge {
	out := make([]Edge, len(g.edges))
	for i, e := range g.edges {
		out[i] = e.clone()
	}
	return out
}

// Keys returns the declared state keys, sorted by name. The result is a copy.
func (g *Graph) Keys() []Key {
	out := make([]Key, len(g.keys))
	copy(out, g.keys)
	return out
}

// IsBackEdge reports whether the edge at index i, in the order Edges returns,
// closes a cycle and therefore had to declare a bound.
//
// An edge is a back edge when its target can reach its source and the target
// is no further from the start node than the source is. Around any cycle the
// hop distances from the start cannot strictly increase all the way, so every
// cycle contains at least one such edge, and bounding all of them bounds every
// cycle. The definition does not depend on declaration order.
func (g *Graph) IsBackEdge(i int) bool {
	if i < 0 || i >= len(g.back) {
		return false
	}
	return g.back[i]
}

// NewState returns the initial state for a run: every declared key holding the
// zero value of its declared kind, with values applied on top. An override
// naming an undeclared key, or holding a value of the wrong kind, is refused.
//
// So is an override naming a halt point's answer key, with an error wrapping
// ErrAnswerPreseeded. That key belongs to its halt point the same way the
// construction rules give it exactly one writer, and this extends that
// ownership from nodes to callers: a run does not start already holding an
// answer to a decision nobody has been asked yet. The override is refused
// rather than dropped, because silently discarding what a caller passed is its
// own way of being wrong.
func (g *Graph) NewState(values map[string]Value) (State, error) {
	s := State{values: make(map[string]Value, len(g.keys))}
	for _, k := range g.keys {
		s.values[k.Name] = zeroValue(k.Kind)
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		spec, ok := g.keyIndex[name]
		if !ok {
			return State{}, fmt.Errorf("%w: %q", ErrUndeclaredKey, name)
		}
		if halt, ok := g.answers[name]; ok {
			return State{}, fmt.Errorf("%w: %q is the answer key of the halt point on node %q",
				ErrAnswerPreseeded, name, halt)
		}
		v := values[name]
		if v.Kind() != spec.Kind {
			return State{}, fmt.Errorf("%w: key %q declares %s, got %s",
				ErrKindMismatch, name, spec.Kind, v.Kind())
		}
		s.values[name] = v
	}
	return s, nil
}

// Validate checks a checkpoint against this graph and refuses anything that
// does not match it. This is the half of validating a checkpoint on read that
// needs a graph; decoding already refused unknown fields and unrecognized
// enumerations. A checkpoint is resumed with the user's credentials, so a
// mismatch is refused rather than repaired.
func (g *Graph) Validate(c Checkpoint) error {
	if c.Run == "" {
		return &CheckpointError{Field: "run", Detail: "is empty"}
	}
	if c.Seq < 1 {
		return &CheckpointError{Field: "seq", Detail: fmt.Sprintf("is %d, want at least 1", c.Seq)}
	}
	if c.Status == StatusInvalid {
		return &CheckpointError{Field: "status", Detail: "is missing"}
	}
	if c.Position == "" {
		if c.Status != StatusCompleted {
			return &CheckpointError{Field: "position", Detail: "is empty but the run did not complete"}
		}
	} else {
		if _, ok := g.index[c.Position]; !ok {
			return &CheckpointError{Field: "position", Detail: fmt.Sprintf("names no node in this graph: %q", c.Position)}
		}
		if c.Status == StatusCompleted {
			return &CheckpointError{Field: "position", Detail: "names a node but the run is recorded as completed"}
		}
		if node := g.nodes[g.index[c.Position]]; node.Halt != nil && c.Status == StatusRunning && !haltAnswered(node, c.State) {
			return &CheckpointError{Field: "status", Detail: fmt.Sprintf(
				"is running at %q, which is a halt point, while state key %q holds no answer that halt point accepts: "+
					"a run standing there is halted with that halt point's decision, parked by a bound, or running "+
					"because it was answered",
				c.Position, node.Halt.Into)}
		}
	}
	if err := g.validateState(c.State); err != nil {
		return err
	}
	if err := g.validateDecision(c); err != nil {
		return err
	}
	if err := g.validateReason(c); err != nil {
		return err
	}
	return g.validateCounters(c.Counters)
}

// stateMismatch describes one way a State fails to hold exactly the graph's
// declared keys with their declared kinds.
type stateMismatch struct {
	// detail says what did not match.
	detail string
	// wrongKind distinguishes a key holding a value of the wrong kind from a
	// key set that does not match the declaration at all.
	wrongKind bool
}

// checkStateShape is the single owner of what the right shape for a state is:
// exactly the graph's declared keys, each holding a value of its declared
// kind. It returns nil when s matches, and otherwise the mismatch, which each
// caller wraps in the error type its own boundary reports.
func (g *Graph) checkStateShape(s State) *stateMismatch {
	if len(s.values) != len(g.keys) {
		return &stateMismatch{detail: fmt.Sprintf(
			"holds %d keys, the graph declares %d", len(s.values), len(g.keys))}
	}
	for _, k := range g.keys {
		v, ok := s.values[k.Name]
		if !ok {
			return &stateMismatch{detail: fmt.Sprintf("is missing declared key %q", k.Name)}
		}
		if v.Kind() != k.Kind {
			return &stateMismatch{
				detail:    fmt.Sprintf("key %q declares %s but holds %s", k.Name, k.Kind, v.Kind()),
				wrongKind: true,
			}
		}
	}
	return nil
}

func (g *Graph) validateState(s State) error {
	if m := g.checkStateShape(s); m != nil {
		return &CheckpointError{Field: "state", Detail: m.detail}
	}
	return nil
}

func (g *Graph) validateDecision(c Checkpoint) error {
	if c.Status != StatusHalted {
		if c.Decision != nil {
			return &CheckpointError{Field: "decision", Detail: fmt.Sprintf(
				"is present but the run is %s, not halted", c.Status)}
		}
		return nil
	}
	if c.Decision == nil {
		return &CheckpointError{Field: "decision", Detail: "is missing from a halted run"}
	}
	node, ok := g.Node(c.Position)
	if !ok || node.Halt == nil {
		return &CheckpointError{Field: "decision", Detail: fmt.Sprintf(
			"halts at %q, which is not a halt point in this graph", c.Position)}
	}
	if !c.Decision.equals(decisionFor(node)) {
		return &CheckpointError{Field: "decision", Detail: fmt.Sprintf(
			"does not match the halt point declared on node %q", node.Name)}
	}
	return nil
}

func (g *Graph) validateCounters(c Counters) error {
	if c.Steps < 0 {
		return &CheckpointError{Field: "counters.steps", Detail: fmt.Sprintf("is negative: %d", c.Steps)}
	}
	if len(c.Traversals) != len(g.edges) {
		return &CheckpointError{Field: "counters.traversals", Detail: fmt.Sprintf(
			"holds %d entries, the graph has %d edges", len(c.Traversals), len(g.edges))}
	}
	if len(c.Fingerprints) != len(g.edges) {
		return &CheckpointError{Field: "counters.fingerprints", Detail: fmt.Sprintf(
			"holds %d entries, the graph has %d edges", len(c.Fingerprints), len(g.edges))}
	}
	for i, n := range c.Traversals {
		if n < 0 {
			return &CheckpointError{Field: "counters.traversals", Detail: fmt.Sprintf(
				"edge %d has a negative count: %d", i, n)}
		}
		if bound := g.edges[i].Rounds; bound > 0 && n > bound {
			return &CheckpointError{Field: "counters.traversals", Detail: fmt.Sprintf(
				"edge %d records %d traversals, past its bound of %d", i, n, bound)}
		}
	}
	return nil
}

// haltAnswered reports whether s holds an answer the halt point on n accepts.
// It is what separates a run that was answered and is now running its halt
// node from a checkpoint forged to walk straight through one: the halt-before
// guarantee is that the node does not start until an answer exists, and
// supplying an answer the halt point accepts is answering it.
func haltAnswered(n Node, s State) bool {
	if n.Halt == nil {
		return false
	}
	v, ok := s.Get(n.Halt.Into)
	if !ok {
		return false
	}
	answer, ok := v.Text()
	if !ok {
		return false
	}
	return answerAllowed(n.Halt.Options, answer)
}

// validateReason refuses a reason on a checkpoint that is not parked. A reason
// explains why a run stopped short, so carrying one on a run that is still
// going or that finished would report a park that never happened.
func (g *Graph) validateReason(c Checkpoint) error {
	if c.Reason != "" && !c.Status.Parked() {
		return &CheckpointError{Field: "reason", Detail: fmt.Sprintf(
			"is set on a run that is %s, and a reason explains a parked run and nothing else", c.Status)}
	}
	return nil
}

// decisionFor builds the decision a halt point emits. It is the one place the
// open decision is derived from the halt declaration.
func decisionFor(n Node) *Decision {
	if n.Halt == nil {
		return nil
	}
	return &Decision{
		Node:     n.Name,
		Question: n.Halt.Question,
		Options:  append([]string(nil), n.Halt.Options...),
		Into:     n.Halt.Into,
	}
}
