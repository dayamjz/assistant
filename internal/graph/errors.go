package graph

import (
	"errors"
	"fmt"
	"strings"
)

// Rule names a construction-time rule. A graph that breaks one fails to build,
// and the refusal names the rule, so a caller can tell which invariant was
// violated without parsing prose.
type Rule string

const (
	// RuleSingleWriter is PRD section 7 rule 1: no two nodes write the same
	// state key without a declared merge rule.
	RuleSingleWriter Rule = "single-writer"
	// RuleBoundedCycle is PRD section 7 rule 2: every back edge carries a
	// bound at construction time.
	RuleBoundedCycle Rule = "bounded-cycle"
	// RuleJoinCycle is PRD section 7 rule 3: a cycle that passes through a
	// join must include that join's fork.
	RuleJoinCycle Rule = "join-cycle"
	// RuleDeclaredKey covers reads, writes, guards, and halt answers that name
	// a state key the graph does not declare, and merge rules that cannot
	// apply to the kind they were declared on.
	RuleDeclaredKey Rule = "declared-key"
	// RuleWellFormed covers the structural requirements that make the other
	// rules decidable: a start node, unique node names, edge endpoints that
	// exist, a body on every node, and no node unreachable from the start.
	RuleWellFormed Rule = "well-formed"
	// RuleDeterministicEdges is this package's own rule that a node's
	// outgoing edges say exactly one thing: a node that declares any outgoing
	// edge declares exactly one unconditional edge, and declares it last. It
	// covers both halves of that, an edge that could never be reached because
	// an unconditional edge on the same node precedes it, and a node whose
	// edges are all guarded and so has nowhere to send a run when no guard
	// matches. A node with no outgoing edges is terminal and is unaffected.
	RuleDeterministicEdges Rule = "deterministic-edges"
)

// Violation is one construction rule broken by one part of a graph.
type Violation struct {
	// Rule is the rule that was broken.
	Rule Rule
	// Detail names the nodes, edges, or keys that broke it.
	Detail string
}

// Error renders the violation as "rule: detail".
func (v Violation) Error() string { return string(v.Rule) + ": " + v.Detail }

// BuildError reports every construction rule a graph broke. Build returns one
// of these instead of a graph, so a defect that would be close to invisible at
// run time surfaces before anything executes.
type BuildError struct {
	// Violations is every rule broken, in the order the checker found them.
	Violations []Violation
}

// Error lists every violation, one per line.
func (e *BuildError) Error() string {
	if len(e.Violations) == 1 {
		return "graph: " + e.Violations[0].Error()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "graph: %d construction rules broken:", len(e.Violations))
	for _, v := range e.Violations {
		b.WriteString("\n  " + v.Error())
	}
	return b.String()
}

// HasRule reports whether any violation broke rule r.
func (e *BuildError) HasRule(r Rule) bool {
	for _, v := range e.Violations {
		if v.Rule == r {
			return true
		}
	}
	return false
}

// Rules returns the distinct rules broken, in the order they were found.
func (e *BuildError) Rules() []Rule {
	seen := make(map[Rule]struct{}, len(e.Violations))
	out := make([]Rule, 0, len(e.Violations))
	for _, v := range e.Violations {
		if _, ok := seen[v.Rule]; ok {
			continue
		}
		seen[v.Rule] = struct{}{}
		out = append(out, v.Rule)
	}
	return out
}

// CheckpointError reports a checkpoint refused on read. A checkpoint is
// resumed with the user's credentials, so anything that does not match the
// graph it claims to belong to is refused rather than repaired.
type CheckpointError struct {
	// Field names the part of the checkpoint that failed validation.
	Field string
	// Detail says what was wrong with it.
	Detail string
}

// Error renders the refusal as "graph: invalid checkpoint: field: detail".
func (e *CheckpointError) Error() string {
	return "graph: invalid checkpoint: " + e.Field + ": " + e.Detail
}

// Errors a caller is expected to handle. Each is a typed result, never a
// warning execution continues past.
var (
	// ErrUndeclaredRead is returned when a node reads a state key it did not
	// declare. The step fails.
	ErrUndeclaredRead = errors.New("graph: node read a state key it did not declare")
	// ErrUndeclaredWrite is returned when a node writes a state key it did not
	// declare. The step fails.
	ErrUndeclaredWrite = errors.New("graph: node wrote a state key it did not declare")
	// ErrKindMismatch is returned when a written value's kind does not match
	// the key's declared kind.
	ErrKindMismatch = errors.New("graph: value kind does not match the declared key kind")
	// ErrUndeclaredKey is returned when an initial state names a key the graph
	// does not declare.
	ErrUndeclaredKey = errors.New("graph: state names a key the graph does not declare")
	// ErrStateMismatch is returned when a state handed to a run does not hold
	// exactly the keys the graph declares.
	ErrStateMismatch = errors.New("graph: state does not match the graph's declared keys")
	// ErrNoOpenDecision is returned when a run is answered but is not waiting
	// on a decision.
	ErrNoOpenDecision = errors.New("graph: run has no open decision to answer")
	// ErrAnswerNotAllowed is returned when an answer is empty or is outside the
	// decision's declared options.
	ErrAnswerNotAllowed = errors.New("graph: answer is not one of the decision's options")
	// ErrRunExists is returned when a run is started under a name that already
	// has checkpoint history. Starting over a run would discard work.
	ErrRunExists = errors.New("graph: run already has checkpoint history")
	// ErrNoSuchRun is returned when a run has no checkpoints.
	ErrNoSuchRun = errors.New("graph: no such run")
	// ErrNoSuchCheckpoint is returned when a checkpoint identifier names
	// nothing the store holds.
	ErrNoSuchCheckpoint = errors.New("graph: no such checkpoint")
	// ErrNodeFailed wraps the error a node's body returned. The node's writes
	// were discarded and no checkpoint was written for it.
	ErrNodeFailed = errors.New("graph: node failed")
)
