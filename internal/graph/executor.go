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

// Executor walks a built graph, writing a checkpoint after every node. It
// holds no per-run state, so one Executor may drive any number of concurrent
// runs of the same graph.
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
// It refuses a name that already has checkpoint history rather than writing
// over it.
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
	history, err := e.store.History(ctx, run)
	if err != nil {
		return Result{}, err
	}
	if len(history) > 0 {
		return Result{}, fmt.Errorf("%w: %q", ErrRunExists, run)
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
func (e *Executor) Resume(ctx context.Context, run string) (Result, error) {
	cp, err := e.load(ctx, run)
	if err != nil {
		return Result{}, err
	}
	if cp.Status == StatusRunning {
		return e.advance(ctx, cp)
	}
	return e.result(cp), nil
}

// Answer restores a run from its latest checkpoint and answers the decision it
// is waiting on, in one call. The answer is written into the state key the
// halt point declared, and the halted node then runs for the first time.
//
// Answering reaches the same state as calling Resume first and Answer second,
// because Resume re-emits a decision without changing anything.
func (e *Executor) Answer(ctx context.Context, run, answer string) (Result, error) {
	cp, err := e.load(ctx, run)
	if err != nil {
		return Result{}, err
	}
	if cp.Status != StatusHalted || cp.Decision == nil {
		return Result{}, fmt.Errorf("%w: run %q is %s", ErrNoOpenDecision, run, cp.Status)
	}
	if !answerAllowed(cp.Decision, answer) {
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
	return e.advance(ctx, cp)
}

// answerAllowed reports whether answer satisfies the decision. An empty answer
// is never allowed, and a decision that declares options accepts nothing else.
func answerAllowed(d *Decision, answer string) bool {
	if answer == "" {
		return false
	}
	if len(d.Options) == 0 {
		return true
	}
	for _, opt := range d.Options {
		if opt == answer {
			return true
		}
	}
	return false
}

// load reads a run's latest checkpoint and validates it against the graph.
func (e *Executor) load(ctx context.Context, run string) (Checkpoint, error) {
	cp, err := e.store.Latest(ctx, run)
	if err != nil {
		return Checkpoint{}, err
	}
	if err := e.graph.Validate(cp); err != nil {
		return Checkpoint{}, err
	}
	return cp, nil
}

// advance runs nodes from cp.Position until the graph completes, halts, or a
// bound parks the run. cp must be a validated checkpoint with StatusRunning.
//
// Node bodies are constructed here, once per call, and never shared between
// runs: a single process drives concurrent runs, and sharing an implementation
// across them is a defect class that only appears under concurrency.
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
// always passes. When no edge is eligible the run has nowhere to go and
// completes. The second result is the edge index, or -1.
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

// persist writes cp and records the sequence number the store assigned.
func (e *Executor) persist(ctx context.Context, cp *Checkpoint) error {
	id, err := e.store.Write(ctx, *cp)
	if err != nil {
		return err
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
// declared keys with their declared kinds.
func (g *Graph) checkRunState(s State) error {
	if len(s.values) != len(g.keys) {
		return fmt.Errorf("%w: holds %d keys, the graph declares %d",
			ErrStateMismatch, len(s.values), len(g.keys))
	}
	for _, k := range g.keys {
		v, ok := s.values[k.Name]
		if !ok {
			return fmt.Errorf("%w: missing declared key %q", ErrStateMismatch, k.Name)
		}
		if v.Kind() != k.Kind {
			return fmt.Errorf("%w: key %q declares %s, got %s", ErrKindMismatch, k.Name, k.Kind, v.Kind())
		}
	}
	return nil
}
