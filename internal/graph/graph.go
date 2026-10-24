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
	digest   string
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

// Entered reports how many times a run whose bound accounting is c has taken
// an edge into node, summed over every edge that enters it. It is how a reader
// asks a run's counters a question about one node without indexing the
// positional traversal vector itself, which only means something against the
// edges it was kept against.
//
// So it asks the same question a resume does first: counters this graph would
// refuse to resume are refused here with the same *CheckpointError, rather
// than read against edges that did not produce them. A node the graph does not
// declare has been entered no times, which is not an error: a stage built
// without a fix node has taken no automatic round, and that is the answer.
func (g *Graph) Entered(c Counters, node string) (int, error) {
	if err := g.validateCounters(c); err != nil {
		return 0, err
	}
	total := 0
	for i, e := range g.edges {
		if e.To == node {
			total += c.Traversals[i]
		}
	}
	return total, nil
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
// mismatch is refused rather than repaired. The one absence it accepts is a
// late key's, which Key.Late declares and the executor fills in at the zero
// value of the key's kind; that is a key the checkpoint could not have held,
// not a mismatch.
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
	// The park edge is read against the edge vector, so it is checked after
	// the counters have established that the vector is the one the run
	// accrued under: a changed edge is then reported as the changed edge it
	// is rather than as a park on an edge this graph does not declare.
	if err := g.validateCounters(c.Counters); err != nil {
		return err
	}
	if err := g.validateParkedUsage(c); err != nil {
		return err
	}
	return g.validateParkedAt(c)
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
// kind, except that a late key may be absent, for the reason Key.Late gives.
// It returns nil when s matches, and otherwise the mismatch, which each caller
// wraps in the error type its own boundary reports.
func (g *Graph) checkStateShape(s State) *stateMismatch {
	for name := range s.values {
		if _, ok := g.keyIndex[name]; !ok {
			return &stateMismatch{detail: fmt.Sprintf(
				"holds key %q, which the graph does not declare (it holds %d keys, the graph declares %d)",
				name, len(s.values), len(g.keys))}
		}
	}
	for _, k := range g.keys {
		v, ok := s.values[k.Name]
		if !ok && k.Late {
			continue
		}
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

// fillLate returns s holding every late key it lacks at the zero value of that
// key's kind, and s itself when it lacks none. It is how a state checkStateShape
// accepted without a late key becomes one that holds every declared key before
// the executor acts on it, so no body and no checkpoint the executor writes
// sees the absence.
func (g *Graph) fillLate(s State) State {
	var filled State
	for _, k := range g.keys {
		if !k.Late {
			continue
		}
		if _, ok := s.values[k.Name]; ok {
			continue
		}
		if filled.values == nil {
			filled = s.Clone()
		}
		filled.values[k.Name] = zeroValue(k.Kind)
	}
	if filled.values == nil {
		return s
	}
	return filled
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

// validateCounters checks the bound accounting a checkpoint carries. It
// refuses counts that could not have been produced by a run of this graph, and
// it refuses accounting whose bounds are not the ones it accrued under: a
// budget below one bounds nothing, and counters indexed against another edge
// vector would be read against edges that did not produce them.
//
// Only the two length checks precede the digest comparison, because a wrongly
// sized counter vector is a length mismatch, which names the vector, rather
// than a digest mismatch, which names neither. Everything indexed by edge
// comes after it: comparing a count against the bound on the edge sitting at
// its index says something only once the edge vector is known to be the one
// the count accrued against, so a changed edge is reported as the changed edge
// it is rather than as a count past a bound it never ran under.
func (g *Graph) validateCounters(c Counters) error {
	if c.Steps < 0 {
		return &CheckpointError{Field: "counters.steps", Detail: fmt.Sprintf("is negative: %d", c.Steps)}
	}
	if c.Budget < 1 {
		return &CheckpointError{Field: "counters.budget", Detail: fmt.Sprintf(
			"is %d, and a run-wide step budget of at least 1 is required", c.Budget)}
	}
	if c.UsageBudget < 0 {
		return &CheckpointError{Field: "counters.usage_budget", Detail: fmt.Sprintf(
			"is negative: %d, and a usage budget is zero for none or a positive bound", c.UsageBudget)}
	}
	if len(c.Traversals) != len(g.edges) {
		return &CheckpointError{Field: "counters.traversals", Detail: fmt.Sprintf(
			"holds %d entries, the graph has %d edges", len(c.Traversals), len(g.edges))}
	}
	if len(c.Fingerprints) != len(g.edges) {
		return &CheckpointError{Field: "counters.fingerprints", Detail: fmt.Sprintf(
			"holds %d entries, the graph has %d edges", len(c.Fingerprints), len(g.edges))}
	}
	if c.EdgeDigest != g.digest {
		return &CheckpointError{Field: "counters.edge_digest", Detail: fmt.Sprintf(
			"was accrued against edges digesting %s and this graph's edges digest %s, so its "+
				"per-edge counts and fingerprints would be read against edges that did not produce them",
			shortDigest(c.EdgeDigest), shortDigest(g.digest))}
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

// shortDigest renders a digest for a diagnostic. A refusal names both of them,
// so a reader can tell a mismatch from a checkpoint that carried none, and a
// prefix is as much of either as that takes.
func shortDigest(digest string) string {
	if digest == "" {
		return "nothing"
	}
	if len(digest) <= 12 {
		return digest
	}
	return digest[:12]
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

// validateReason refuses a reason on a checkpoint the run can still advance
// out of on its own. A reason explains why a run stopped short, so carrying
// one on a run that is still going or that finished would report a stop that
// never happened. It is Status.Stopped that is read here rather than the
// narrower question of a park, so a hand-built halted checkpoint carrying a
// reason passes: the executor sets a reason only when a bound parks the run,
// and nothing here holds a caller to that.
func (g *Graph) validateReason(c Checkpoint) error {
	if c.Reason != "" && !c.Status.Stopped() {
		return &CheckpointError{Field: "reason", Detail: fmt.Sprintf(
			"is set on a run that is %s, and a reason explains a run that stopped short and nothing else", c.Status)}
	}
	return nil
}

// validateParkedAt holds a recorded park edge to what the executor writes:
// only a round limit or a convergence park carries one, it names an edge this
// graph declares with the bound it declares, a convergence park's edge is one
// the convergence check applies to, and the run stands where that edge leads.
// An edge park with no edge recorded passes, because a checkpoint written
// before the field existed reads back that way; Park then reports the bound
// without the edge rather than guessing one.
func (g *Graph) validateParkedAt(c Checkpoint) error {
	ref := c.ParkedAt
	if ref == nil {
		return nil
	}
	if c.Status != StatusRoundsExhausted && c.Status != StatusConverged {
		return &CheckpointError{Field: "parked_at", Detail: fmt.Sprintf(
			"names an edge on a run that is %s, and only a round limit or a convergence park has one", c.Status)}
	}
	if ref.To != c.Position {
		return &CheckpointError{Field: "parked_at", Detail: fmt.Sprintf(
			"names the edge from %q to %q while the run stands at %q", ref.From, ref.To, c.Position)}
	}
	for i, e := range g.edges {
		if e.From != ref.From || e.To != ref.To || e.Rounds != ref.Rounds {
			continue
		}
		switch {
		case c.Status == StatusRoundsExhausted && e.Rounds > 0:
			return nil
		case c.Status == StatusConverged && g.comparesFingerprint(i, c.Counters.Unattended):
			return nil
		}
	}
	return &CheckpointError{Field: "parked_at", Detail: fmt.Sprintf(
		"names the edge from %q to %q with %d rounds, and this graph declares no such edge whose bound parks a run %s",
		ref.From, ref.To, ref.Rounds, c.Status)}
}

// leavesHalt reports whether edge i leaves a halt point, which is what makes
// every traversal of it the first edge a run takes after an answer.
func (g *Graph) leavesHalt(i int) bool {
	idx, ok := g.index[g.edges[i].From]
	return ok && g.nodes[idx].Halt != nil
}

// recordsFingerprint reports whether the convergence check records the state
// on every traversal of edge i: a back edge, which is where a cycle closes,
// and an edge leaving a halt point, which is where an answer sends a run on.
// Every such traversal is recorded whoever gave the answer it follows, so a
// comparison is always against the edge's previous traversal.
func (g *Graph) recordsFingerprint(i int) bool { return g.back[i] || g.leavesHalt(i) }

// comparesFingerprint reports whether a traversal of edge i is compared with
// the one before it, given whether the latest answer the run took was
// unattended. That is the convergence check's whole rule for comparing, and
// two of its three cases exist only for an unattended answer; restartedBy
// says when a person's answer empties what a comparison would read.
//
// A back edge not marked Edge.Answered is always compared. An Answered back
// edge is compared after an unattended answer, measuring the round it closes
// against the round before it on that edge. An edge leaving a halt point is
// compared after an unattended answer too, and measures something different:
// the run's state as it leaves the halt point carrying that answer, against
// its state the last time it left by the same edge. Equal means the run is
// being sent on from exactly where it was sent on from last time, so the
// round between the two answers changed nothing, and the run parks before
// spending another round. It is the comparison that sees a round's whole
// effect, because it is taken after everything the round led to has run,
// where the back edge closing the round is taken before what follows it.
func (g *Graph) comparesFingerprint(i int, unattended bool) bool {
	switch {
	case g.back[i] && !g.edges[i].Answered:
		return true
	case !unattended:
		return false
	default:
		return g.recordsFingerprint(i)
	}
}

// restartedBy returns the back edges whose convergence measure a traversal of
// edge i starts afresh when it follows a person's answer, which is the whole
// of that rule: none unless i is marked Edge.Answered, and otherwise every
// back edge into the node i enters that is not itself Answered, does not
// leave a halt point, and leaves a node entered from that node and from
// nowhere else, a round the node runs for itself. An edge leaving a halt
// point is where an answer sends the run on rather than where a round
// closes, so it keeps its measure. checkEdgeBounds applies the rule only
// after an attended answer.
func (g *Graph) restartedBy(i int) []int {
	if !g.edges[i].Answered {
		return nil
	}
	into := g.edges[i].To
	var out []int
	for j, e := range g.edges {
		if g.back[j] && !e.Answered && !g.leavesHalt(j) && e.To == into && g.enteredOnlyFrom(e.From, into) {
			out = append(out, j)
		}
	}
	return out
}

// enteredOnlyFrom reports whether every edge entering node leaves from.
//
// restartedBy asks it only of the node a back edge into from leaves, and the
// run's own entry into the start node, which no edge records, needs no case
// there. A back edge never leads farther from the start node than it leaves,
// so one leaving the start node is a loop on it, and from is then the start
// node too. The Answered edge into from then leaves some other node, because
// checkAnswered refuses an Answered edge leaving the start node, so the start
// node is never entered only from itself. Every other node a run can reach is
// entered by an edge, and an unreachable node fails to build.
func (g *Graph) enteredOnlyFrom(node, from string) bool {
	for _, e := range g.edges {
		if e.To == node && e.From != from {
			return false
		}
	}
	return true
}

// convergedReason is what a convergence park on edge i says, which is the
// one owner of that sentence.
func (g *Graph) convergedReason(i int) string {
	e := g.edges[i]
	switch {
	case g.back[i] && !e.Answered:
		return fmt.Sprintf("the round from %q to %q changed no state", e.From, e.To)
	case g.back[i]:
		return fmt.Sprintf("the round from %q to %q changed no state, and the answer that asked for it was not a person's",
			e.From, e.To)
	default:
		return fmt.Sprintf("the run left %q for %q in the state it left in last time, so the round between changed no state, "+
			"and the answer that sent it on again was not a person's", e.From, e.To)
	}
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
		Detailed: append([]string(nil), n.Halt.Detailed...),
	}
}
