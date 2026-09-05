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
// node and once more when a segment claims the run before it starts one.
// Restoring one re-emits the open decision, and a single call can restore and
// answer together.
//
// # Rules enforced when the graph is built, not when it runs
//
// A graph that breaks any of these fails to construct. Construction-time
// failure is the point: these are defects that are close to invisible at run
// time and expensive to diagnose once they are. The first three are the ones
// PRD section 7 states; the two after them are this package's own, so a reader
// looking for them in section 7 will not find them there.
//
//   - Two nodes may not write the same state key unless that key declares a
//     merge rule. Shared mutable state with no declared merge rule produces
//     results that depend on scheduling, which means the same graph can produce
//     different answers on different days.
//   - Every back edge carries a bound. An unbounded cycle is never valid.
//   - A cycle passing through a join must include that join's fork. Otherwise
//     the join can be re-entered with a partial set of inputs and produce a
//     result that looks complete.
//   - A node that declares any outgoing edge declares exactly one
//     unconditional edge, and declares it last. This is this package's own
//     rule rather than one of the three the PRD states. Without it a node
//     whose guards all fail leaves the run with nowhere to go, and a run with
//     nowhere to go reports a clean completion for the work below that node
//     that it never did. The fallback edge is the point: it makes the author
//     say where a run goes when no guard matches instead of defaulting to
//     silent success. A node with no outgoing edges is terminal and says so.
//   - A halt point's answer key is written by that halt point and nothing
//     else: no other node may write it, no second halt point may ask into it,
//     and it declares no merge rule. This is also this package's own rule. An
//     answer is consent to one decision, and consent here is explicit for a
//     bounded scope rather than a quiet default, so a run must never arrive at
//     a halt point already holding an answer nobody gave for it. A state that
//     sets an answer key is refused before a run starts, with an error
//     wrapping ErrAnswerPreseeded, which is the same ownership applied to
//     callers rather than to nodes. The executor
//     clears the key on every checkpoint it writes standing at a halt point
//     without running it, whether the run halted there or a bound parked it
//     there, so a halt re-entered in a loop asks afresh instead of inheriting
//     the last round's answer, and this rule is what keeps anything else from
//     putting one back.
//
// These are the rules worth stating in prose, not the whole list. The checker
// also refuses the structural defects that make them decidable at all, such as
// a node naming a state key the graph does not declare, or a graph with no
// start node. The Rule constants name every rule the checker enforces and say
// what each one covers.
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
// All three have to survive a resume intact, and intact means more than the
// counts. A count is meaningless without the bound it is compared against and
// the edge it accrued on, so a checkpoint's Counters carry those too: the
// run-wide budget the run is being held to, and a digest of the edge vector
// its per-edge counts and fingerprints are indexed against. A graph whose
// edges differ is refused rather than resumed, because its counters would be
// read against edges that did not produce them, and comparing how many entries
// they hold does not catch a change that drops one edge and adds another. That
// refusal has no in-place remedy, deliberately: counts accrued against another
// edge vector are not reinterpretable, forking copies both the counters and
// the digest they were accrued under, and a run's first checkpoint already
// carries that digest. Such a run is resumed under the edges it recorded, or
// left behind for a new run started from the state it reached, which NewState
// takes on its own terms: a halt point's answer key may not be preseeded, so
// what a fresh run begins from is that state without those keys.
//
// The budget is settled differently, because raising one on a resume is a
// legitimate thing to want and refusing it outright would only push a caller
// into forking to work around it. The bound in force is the one the run
// recorded, an executor configured with any other is refused with
// ErrBudgetChanged rather than quietly applying either number, and
// Executor.AdoptBudget moves a run onto a new budget by writing a checkpoint
// that says what it became. So a budget may change, and a change is a line in
// the run's history rather than a difference between two executors.
//
// Integrity checking leaves the three bounds three. It decides whether a
// checkpoint may be resumed at all, and past that each bound still parks runs
// the other two would not: AdoptBudget releases a run its own budget parked
// and leaves one parked by a round limit or by convergence exactly where it
// stands.
//
// # Trust
//
// Checkpoints are resumed with the user's credentials, so a tampered checkpoint
// is a code-execution path. Serialize them as data only, validate on read, and
// never load one from outside the current home.
//
// Checkpoints here encode as JSON, which constructs no types on read, and they
// are refused rather than repaired when decoding meets an unknown field, an
// unrecognized kind or status, or a shape the graph does not declare. Where a
// checkpoint comes from is the storage layer's rule, not this package's: a
// CheckpointStore is the only thing that decides what it will hand back.
//
// # What this package settles that the summary above leaves open
//
// PRD section 7 states a fourth topology rule the summary omits: node
// implementations are constructed per run and never shared, because a single
// process drives concurrent runs and sharing an implementation across them is
// a defect that only appears under concurrency. A Node therefore declares a
// constructor, not a body, and the executor calls it once per advance segment,
// meaning once per Run, Resume, or Answer call. That is strictly stronger than
// once per run, so the rule holds: no body is shared between runs, and none is
// shared across the halt that ends a segment. What a later segment needs
// therefore lives in declared state, not in a Go value a body closed over.
//
// A Guard is data rather than a callback, so an edge's predicate is part of
// the topology that can be inspected and checked rather than something only
// running the graph reveals.
//
// A Checkpoint carries the run's bound accounting alongside state, position,
// and the open decision. The counters have to survive a resume: a resume that
// restarted them would leave the run with bounds that no longer bound
// anything, and each of the three would then be one restart away from useless.
// They travel with the budget they are spent against and the edge digest they
// are indexed against, for the reason given under Bounding cycles above.
//
// Fan-out is declared, not inferred. A node names the fan-out it opens or
// closes, and the cycle-through-a-join rule is checked against those
// declarations. Fan-out is not executed: the rule is in the checker before the
// feature exists, which is where the PRD puts it.
//
// An edge is a back edge when its target can reach its source and the target
// is no further from the start node in hops than the source is. Around any
// cycle the hop distances cannot strictly increase the whole way round, so
// every cycle carries at least one such edge and bounding all of them bounds
// every cycle. The definition does not depend on the order edges were
// declared in, so adding an unrelated edge cannot move where a bound is
// required.
//
// The durability boundary is a CheckpointStore with exactly four operations:
// write, read the latest for a run, list a run's history, and fork from a
// point. Resuming from an earlier point is forking it into a new run and
// resuming that, which is what keeps the original history intact.
//
// Every write is anchored to the checkpoint the operation read, and the store
// refuses one whose run has moved since. That is P6 at this boundary: an
// update is anchored to what the run actually observed rather than to a tip
// read a moment before writing, which always matches and so protects nothing.
// Two operations on one run therefore end with one refused and reported as
// ErrStaleAnchor, never with two walks appended to one history.
//
// A segment claims the run with that write before it executes anything, and
// the executor re-reads the run's latest checkpoint immediately before every
// body, refusing when the claim it wrote is no longer the run's tip. Node
// bodies belong to callers and may touch the world, so a segment that has
// already lost the run stops before it runs another one. Two callers can still
// execute one node concurrently: a segment writes its next checkpoint only
// after a body returns, so a caller reading the tip while that body runs
// claims the run and runs the same node alongside it, and the first learns of
// it only when its own write is refused. Closing that would need a lease with
// an expiry and therefore a clock, which this package does not have.
package graph
