package graph

import (
	"context"
	"errors"
	"fmt"
)

// Config configures an Executor. Every field is required: an executor with no
// store cannot make a run durable, and an executor with no budget has no
// run-wide bound, which is not the same as having a generous one.
type Config struct {
	// Store is where checkpoints are written and read.
	Store CheckpointStore
	// Budget is the run-wide step budget: the total number of node executions
	// one run may spend, however those steps are distributed. It must be at
	// least 1. It is the bound that catches several nodes each staying under
	// their own round limit while the run as a whole never finishes.
	Budget int
}

// Executor walks a built graph, writing a checkpoint after every node and one
// more when a segment claims the run. It holds no per-run state, so one
// Executor may drive any number of concurrent runs of the same graph.
//
// A run's history never interleaves. Every checkpoint is written anchored to
// the one the operation read, so when a run moves under a Resume or an Answer
// the later write is refused with ErrStaleAnchor instead of a second walk
// being appended to a history that then reports success for both.
//
// A segment that is going to execute claims the run first: Resume and Answer
// write their checkpoint, anchored to the one they read, before the first node
// body runs, which is what Run already did with a run's first checkpoint. Two
// callers that read the same checkpoint therefore both try to claim it, one is
// refused before it starts a node, and a node body is not executed twice. What
// a body does is outside this package, so this is the difference between
// wasted work and an agent, a push, or a network call happening twice.
type Executor struct {
	graph  *Graph
	store  CheckpointStore
	budget int
}

// NewExecutor returns an Executor for g, or an error when the configuration
// leaves the run unbounded or undurable.
func NewExecutor(g *Graph, cfg Config) (*Executor, error) {
	if g == nil {
		return nil, errors.New("graph: an executor needs a graph")
	}
	if cfg.Store == nil {
		return nil, errors.New("graph: an executor needs a checkpoint store")
	}
	if cfg.Budget < 1 {
		return nil, fmt.Errorf("graph: a run-wide step budget of at least 1 is required, got %d", cfg.Budget)
	}
	return &Executor{graph: g, store: cfg.Store, budget: cfg.Budget}, nil
}

// Graph returns the topology this executor walks.
func (e *Executor) Graph() *Graph { return e.graph }

// Result reports where a run stopped and why. It is the same shape whether the
// run completed, halted for a decision, or was parked by a bound, because a
// caller has to handle all three and none of them is an error.
type Result struct {
	// Status is why the run stopped.
	Status Status
	// Position is the node that has not run. It is empty exactly when the run
	// completed.
	Position string
	// Decision is the open decision, non-nil exactly when Status is
	// StatusHalted.
	Decision *Decision
	// State is the state as of the last checkpoint.
	State State
	// Checkpoint identifies the last checkpoint written.
	Checkpoint CheckpointID
	// Steps is how many node executions the run has spent against its budget.
	Steps int
	// Reason explains a parked status, and is empty otherwise.
	Reason string
}

// Run starts a new run of the graph under the given name, from initial state.
// It refuses a name that already has checkpoint history, with an error
// wrapping ErrRunExists, rather than writing over it. The refusal is the
// store's: Run claims the name by writing the run's first checkpoint, so two
// concurrent calls under one name cannot both believe they started it. Run
// checks the answer it gets back, because the first checkpoint of a run is
// Seq 1 and any other sequence number means the store appended into a history
// that already existed instead of honouring the claim.
//
// Run returns when the graph completes, halts before a halt point, or is
// parked by one of the three bounds. A node that returns an error stops the
// run with that error; its writes are discarded and no checkpoint records it.
func (e *Executor) Run(ctx context.Context, run string, initial State) (Result, error) {
	if run == "" {
		return Result{}, errors.New("graph: a run needs a name")
	}
	if err := e.graph.checkRunState(initial); err != nil {
		return Result{}, err
	}
	cp := Checkpoint{
		Run:      run,
		Position: e.graph.start,
		Status:   StatusRunning,
		State:    initial.Clone(),
		Counters: newCounters(len(e.graph.edges)),
	}
	start, _ := e.graph.Node(e.graph.start)
	if start.Halt != nil {
		cp.Status = StatusHalted
		cp.Decision = decisionFor(start)
	}
	if err := e.persist(ctx, &cp); err != nil {
		return Result{}, err
	}
	if cp.Status == StatusHalted {
		return e.result(cp), nil
	}
	return e.advance(ctx, cp)
}

// Resume continues a run from its latest checkpoint, which is validated
// against the graph before anything is done with it.
//
// A run waiting on a decision re-emits it and executes nothing: the halted
// node has not started, so there is nothing to replay. A run parked by a bound
// is returned unchanged, because no amount of resuming moves it; answer it,
// fork it, or change the graph. A run interrupted mid-flight continues from
// the node it had not yet reached.
//
// A run it is going to advance is claimed first, by writing the checkpoint it
// read back under a new sequence number before any node runs. It returns an
// error wrapping ErrStaleAnchor when that claim finds the run already moved,
// which is what stops a second caller from re-executing the node this one is
// about to start.
func (e *Executor) Resume(ctx context.Context, run string) (Result, error) {
	cp, err := e.load(ctx, run)
	if err != nil {
		return Result{}, err
	}
	if cp.Status != StatusRunning {
		return e.result(cp), nil
	}
	if err := e.persist(ctx, &cp); err != nil {
		return Result{}, err
	}
	return e.advance(ctx, cp)
}

// Answer restores a run from its latest checkpoint and answers the decision it
// is waiting on, in one call. The answer is written into the state key the
// halt point declared, and the halted node then runs for the first time.
//
// Answering reaches the same state as calling Resume first and Answer second,
// because Resume re-emits a decision without changing anything.
//
// The answer is claimed before the halted node runs: the checkpoint carrying
// it is written, still positioned at the halt point, and only then does the
// node start. It returns an error wrapping ErrStaleAnchor when that claim
// finds the run already moved, which is how a decision answered twice at once
// resolves to one answer and one execution of the node behind it.
func (e *Executor) Answer(ctx context.Context, run, answer string) (Result, error) {
	cp, err := e.load(ctx, run)
	if err != nil {
		return Result{}, err
	}
	if cp.Status != StatusHalted || cp.Decision == nil {
		return Result{}, fmt.Errorf("%w: run %q is %s", ErrNoOpenDecision, run, cp.Status)
	}
	if !answerAllowed(cp.Decision.Options, answer) {
		return Result{}, fmt.Errorf("%w: run %q, answer %q, options %v",
			ErrAnswerNotAllowed, run, answer, cp.Decision.Options)
	}
	node, ok := e.graph.Node(cp.Position)
	if !ok {
		return Result{}, &CheckpointError{Field: "position", Detail: fmt.Sprintf("names no node: %q", cp.Position)}
	}
	acc := e.accessFor(node, cp.State.Clone())
	if err := acc.Set(cp.Decision.Into, TextValue(answer)); err != nil {
		return Result{}, err
	}
	cp.State = acc.work
	cp.Decision = nil
	cp.Status = StatusRunning
	if err := e.persist(ctx, &cp); err != nil {
		return Result{}, err
	}
	return e.advance(ctx, cp)
}

// answerAllowed reports whether answer is one a halt point declaring these
// options accepts. An empty answer is never allowed, and a halt point that
// declares options accepts nothing else. It is the one place that decides what
// counts as an answer, for the call that supplies one and for the validator
// that has to recognize a checkpoint carrying one.
func answerAllowed(options []string, answer string) bool {
	if answer == "" {
		return false
	}
	if len(options) == 0 {
		return true
	}
	for _, opt := range options {
		if opt == answer {
			return true
		}
	}
	return false
}

// load reads a run's latest checkpoint, refuses one the store answered with
// for some other run, and validates what is left against the graph. The run
// check cannot live in Graph.Validate, which is not told which run was asked
// for, and it has to happen here so every checkpoint the executor acts on and
// every checkpoint it writes belongs to the run the caller named.
//
// What it returns is a checkpoint the executor owns outright, because a store
// is free to answer with its own storage and advancing a run must not write
// back into a history that was already recorded.
func (e *Executor) load(ctx context.Context, run string) (Checkpoint, error) {
	cp, err := e.store.Latest(ctx, run)
	if err != nil {
		return Checkpoint{}, err
	}
	if cp.Run != run {
		return Checkpoint{}, &CheckpointError{Field: "run", Detail: fmt.Sprintf(
			"the store answered with run %q when run %q was asked for", cp.Run, run)}
	}
	if err := e.graph.Validate(cp); err != nil {
		return Checkpoint{}, err
	}
	return cp.clone(), nil
}

// advance runs nodes from cp.Position until the graph completes, halts, or a
// bound parks the run. cp must be a validated checkpoint with StatusRunning.
//
// Node bodies are constructed here, once for this segment, and never shared
// between runs or between segments: a single process drives concurrent runs,
// and sharing an implementation across them is a defect class that only
// appears under concurrency. A segment ends where the run does, at a halt, a
// completion, or a bound, so state a body holds does not outlive one.
func (e *Executor) advance(ctx context.Context, cp Checkpoint) (Result, error) {
	bodies := make(map[string]Body, len(e.graph.nodes))
	for {
		if err := ctx.Err(); err != nil {
			return Result{}, fmt.Errorf("run %q: %w", cp.Run, err)
		}
		idx, ok := e.graph.index[cp.Position]
		if !ok {
			return Result{}, &CheckpointError{Field: "position", Detail: fmt.Sprintf("names no node: %q", cp.Position)}
		}
		node := e.graph.nodes[idx]

		if cp.Counters.Steps >= e.budget {
			cp.Status = StatusBudgetExhausted
			cp.Reason = fmt.Sprintf("the run-wide step budget of %d was spent before node %q could run",
				e.budget, node.Name)
			return e.park(ctx, cp)
		}

		body, made := bodies[node.Name]
		if !made {
			body = node.NewBody()
			bodies[node.Name] = body
		}
		acc := e.accessFor(node, cp.State.Clone())
		err := body(ctx, acc, acc)
		if err == nil {
			err = acc.err
		}
		if err != nil {
			return Result{}, fmt.Errorf("%w: %q: %w", ErrNodeFailed, node.Name, err)
		}
		cp.State = acc.work
		cp.Counters.Steps++

		to, edge := e.next(cp.State, idx)
		if edge >= 0 {
			if parked, reason := e.checkEdgeBounds(&cp, edge); parked != StatusRunning {
				cp.Position = to
				cp.Status = parked
				cp.Reason = reason
				return e.park(ctx, cp)
			}
		}

		cp.Position = to
		cp.Decision = nil
		if to == "" {
			cp.Status = StatusCompleted
			return e.park(ctx, cp)
		}
		if next := e.graph.nodes[e.graph.index[to]]; next.Halt != nil {
			cp.Status = StatusHalted
			cp.Decision = decisionFor(next)
			return e.park(ctx, cp)
		}
		cp.Status = StatusRunning
		if err := e.persist(ctx, &cp); err != nil {
			return Result{}, err
		}
	}
}

// checkEdgeBounds applies the two bounds that live on an edge and, when
// neither parks the run, records the traversal. It returns StatusRunning when
// the edge may be taken.
//
// The round limit is checked before convergence, matching the order the fix
// loop uses: a round that is not eligible to happen never happens, so it never
// gets to report that it changed nothing.
func (e *Executor) checkEdgeBounds(cp *Checkpoint, edge int) (Status, string) {
	declared := e.graph.edges[edge]
	if declared.Rounds > 0 && cp.Counters.Traversals[edge] >= declared.Rounds {
		return StatusRoundsExhausted, fmt.Sprintf(
			"the edge from %q to %q reached its bound of %d rounds",
			declared.From, declared.To, declared.Rounds)
	}
	if e.graph.back[edge] {
		fingerprint := cp.State.fingerprint()
		if prev := cp.Counters.Fingerprints[edge]; prev != "" && prev == fingerprint {
			return StatusConverged, fmt.Sprintf(
				"the round from %q to %q changed no state", declared.From, declared.To)
		}
		cp.Counters.Fingerprints[edge] = fingerprint
	}
	cp.Counters.Traversals[edge]++
	return StatusRunning, ""
}

// next selects the edge leaving the node at idx. Outgoing edges are evaluated
// in declaration order and the first whose guard passes is taken; a nil guard
// always passes. A node that declares any outgoing edge declares an
// unconditional one last, so the only node with no eligible edge is a terminal
// node, which declares none at all and completes the run. The second result is
// the edge index, or -1.
func (e *Executor) next(s State, idx int) (string, int) {
	for _, edge := range e.graph.outgoing[idx] {
		declared := e.graph.edges[edge]
		if declared.Guard == nil || declared.Guard.eval(s) {
			return declared.To, edge
		}
	}
	return "", -1
}

// accessFor builds the Reader and Writer one node execution sees.
func (e *Executor) accessFor(n Node, work State) *access {
	idx := e.graph.index[n.Name]
	return &access{
		keys:   e.graph.keyIndex,
		reads:  e.graph.reads[idx],
		writes: e.graph.writes[idx],
		node:   n.Name,
		work:   work,
	}
}

// park writes a final checkpoint and returns the result it describes.
func (e *Executor) park(ctx context.Context, cp Checkpoint) (Result, error) {
	if err := e.persist(ctx, &cp); err != nil {
		return Result{}, err
	}
	return e.result(cp), nil
}

// persist writes cp and records the sequence number the store assigned. It
// clears the fork lineage first: ForkedFrom marks a checkpoint a fork copied,
// and a checkpoint the executor produced was copied from nothing, so carrying
// the field forward off a resumed fork would give that fact a second owner.
// It clears the reason on the same grounds whenever the status it is writing
// is not a parked one, because a reason explains a park and nothing else, and
// a run that moved on from a park has left that explanation behind.
//
// What it hands over shares nothing with the checkpoint the run keeps
// advancing, so a store is free to retain it as it stands without its history
// rewriting itself underneath it.
//
// The write is anchored to the checkpoint the run last observed, which is the
// identifier cp already carries: the store assigned it, and every write since
// has recorded what it assigned. Reading the tip here instead would anchor to
// something this run never acted on, which is the mistake the anchor exists
// to prevent.
//
// It then checks the answer rather than trusting it. A store that honoured the
// anchor assigned exactly the checkpoint after it, so any other identifier
// means the anchor decided nothing and this run is appending into a history
// that moved. This is the one place that verification lives: a zero anchor is
// the claim on a new run and every other anchor is the run's tip, so the same
// comparison covers both halves of the Write contract.
func (e *Executor) persist(ctx context.Context, cp *Checkpoint) error {
	cp.ForkedFrom = nil
	if !cp.Status.Parked() {
		cp.Reason = ""
	}
	anchor := cp.ID()
	id, err := e.store.Write(ctx, anchor, cp.clone())
	if err != nil {
		return err
	}
	if want := (CheckpointID{Run: cp.Run, Seq: anchor.Seq + 1}); id != want {
		refusal := ErrStaleAnchor
		if anchor.Seq == 0 {
			refusal = ErrRunExists
		}
		return fmt.Errorf("%w: the store answered %s to a write anchored to %s, which it must have answered %s",
			refusal, id, anchor, want)
	}
	cp.Seq = id.Seq
	return nil
}

// result renders a checkpoint as the result a caller sees.
func (e *Executor) result(cp Checkpoint) Result {
	return Result{
		Status:     cp.Status,
		Position:   cp.Position,
		Decision:   cp.Decision.clone(),
		State:      cp.State.Clone(),
		Checkpoint: cp.ID(),
		Steps:      cp.Counters.Steps,
		Reason:     cp.Reason,
	}
}

// checkRunState refuses a state that does not hold exactly the graph's
// declared keys with their declared kinds. It reports the same invariant
// Graph.Validate applies to a checkpoint's state, as the sentinel errors a
// caller starting a run handles.
func (g *Graph) checkRunState(s State) error {
	m := g.checkStateShape(s)
	if m == nil {
		return nil
	}
	if m.wrongKind {
		return fmt.Errorf("%w: %s", ErrKindMismatch, m.detail)
	}
	return fmt.Errorf("%w: state %s", ErrStateMismatch, m.detail)
}
