package graph

import "context"

// Body is the implementation of one node for one run. It reads through r and
// writes through w, and both refuse any key the node did not declare. A Body
// that returns a non-nil error leaves state untouched: the executor applies a
// node's writes only when it completes.
type Body func(ctx context.Context, r Reader, w Writer) error

// Node is a named unit of work that declares the state keys it reads and the
// state keys it writes. The declarations are required, not documentation:
// they are what the construction-time checks reason over and what makes
// replay and forking possible.
type Node struct {
	// Name identifies the node and is unique within a graph.
	Name string
	// Reads lists every state key the node's Body may read. A read of any
	// other key fails the step.
	Reads []string
	// Writes lists every state key the node's Body may write. A write of any
	// other key fails the step. A halt point's answer key counts as a write by
	// its node for the purpose of the single-writer rule.
	Writes []string
	// Halt, when non-nil, marks this node as one the engine stops before. The
	// node does not start, so a resume re-executes nothing.
	Halt *Halt
	// Fork, when non-empty, names the fan-out this node opens. Fan-out is not
	// executed; the name exists so the cycle-through-a-join rule can be
	// checked before the feature lands.
	Fork string
	// Join, when non-empty, names the fan-out this node closes. Every cycle
	// containing this node must also contain the node that opens that fan-out.
	Join string
	// NewBody constructs the node's implementation. The executor calls it once
	// per run, so no implementation state is ever shared between concurrent
	// runs of the same graph.
	NewBody func() Body
}

// Halt marks a node the engine stops before, and describes the decision the
// run is waiting on. The node's inputs are already in state; the answer is
// collected outside the graph and supplied on the next call.
type Halt struct {
	// Question is the decision put to whoever answers it.
	Question string
	// Options, when non-empty, is the closed set of permitted answers. An
	// answer outside the set is refused. When empty, any non-empty answer is
	// accepted.
	Options []string
	// Into is the state key the answer is written to. It must be declared with
	// KindText, and it counts as a write by this node.
	Into string
}

// Stateless adapts a Body that holds no per-run state into a NewBody
// constructor. The name is the point: use it only when there is genuinely
// nothing to construct, and write a real constructor when there is.
func Stateless(b Body) func() Body {
	return func() Body { return b }
}

// clone returns a copy of the node with its slices detached, so a caller
// inspecting a built graph cannot reach into the graph's own declarations.
func (n Node) clone() Node {
	out := n
	out.Reads = append([]string(nil), n.Reads...)
	out.Writes = append([]string(nil), n.Writes...)
	if n.Halt != nil {
		h := *n.Halt
		h.Options = append([]string(nil), n.Halt.Options...)
		out.Halt = &h
	}
	return out
}
