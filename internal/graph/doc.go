// Package graph is the execution engine that every run in this product moves
// through. Its contract is PRD section 7; this comment restates the parts that
// are load-bearing so a reader of the code does not have to open the PRD to
// know what must stay true.
//
// The engine is deliberately small and deliberately pure. It performs no git
// operations, opens no network connections, and calls no agent. It advances a
// typed state record through a declared topology and hands work to callers.
//
// # The five concepts
//
// A Node is a named unit of work that declares the state keys it reads and the
// state keys it writes. Those declarations are required rather than
// documentation: they are what makes replay, forking, and the construction-time
// checks below possible.
//
// An Edge is a transition, either unconditional or guarded by a predicate over
// state. Edges are data, not callbacks, so the topology can be inspected,
// checked, and rendered without executing anything.
//
// State is a typed record. Any key that more than one node can write declares a
// merge rule. A key with no declared rule may be written by exactly one node.
//
// A halt point marks a node as one the engine stops before. The node does not
// start. Its inputs are already in state, so a resume re-executes nothing and
// no node needs to be idempotent.
//
// A Checkpoint is state, position, and any open decision, written after every
// node. Restoring one re-emits the open decision, and a single call can restore
// and answer together.
//
// # Rules enforced when the graph is built, not when it runs
//
// A graph that breaks any of these fails to construct. Construction-time
// failure is the point: these are defects that are close to invisible at run
// time and expensive to diagnose once they are.
//
//   - Two nodes may not write the same state key unless that key declares a
//     merge rule. Shared mutable state with no declared merge rule produces
//     results that depend on scheduling, which means the same graph can produce
//     different answers on different days.
//   - Every back edge carries a bound. An unbounded cycle is never valid.
//   - A cycle passing through a join must include that join's fork. Otherwise
//     the join can be re-entered with a partial set of inputs and produce a
//     result that looks complete.
//
// # Bounding cycles
//
// Three independent bounds apply, and each catches something the others miss.
// A per-node round limit catches one node oscillating. A run-wide step budget
// catches several nodes each staying under their own limit while the run as a
// whole never finishes. A convergence check ends the loop when a round produces
// no change at all, which catches work that reports success without doing
// anything; neither counter would notice that before exhausting itself.
//
// # Trust
//
// Checkpoints are resumed with the user's credentials, so a tampered checkpoint
// is a code-execution path. Serialize them as data only, validate on read, and
// never load one from outside the current home.
package graph
