package graph

import (
	"context"
	"errors"
	"fmt"
)

// Config configures an Executor. Store and Budget are required: an executor
// with no store cannot make a run durable, and an executor with no budget has
// no run-wide bound, which is not the same as having a generous one.
// UsageBudget and Usage are optional, and only as a pair: a zero UsageBudget is
// no usage budget and needs no meter, a positive one requires Usage, and a
// negative one is refused, all by NewExecutor.
type Config struct {
	// Store is where checkpoints are written and read.
	Store CheckpointStore
	// Budget is the run-wide step budget: the total number of node executions
	// one run may spend, however those steps are distributed. It must be at
	// least 1. It is the bound that catches several nodes each staying under
	// their own round limit while the run as a whole never finishes.
	//
	// It is what a run started here is recorded as running under, and from
	// then on the recorded budget is the one in force. Resuming a run that
	// recorded a different one is refused with ErrBudgetChanged rather than
	// quietly held to either number; AdoptBudget is how this budget is moved
	// onto such a run on purpose.
	Budget int
	// UsageBudget is the usage budget: how much a run's own work may report
	// spending, as Usage reads it, before the run parks. Zero is no usage
	// budget, and a negative one is refused. It is the bound that catches a
	// run spending far more than it was meant to while every step and round
	// count stays inside its own bound.
	//
	// It is recorded on a run when it starts and held on exactly Budget's
	// terms: resuming a run that recorded a different one is refused with an
	// error wrapping ErrUsageBudgetChanged and ErrBudgetChanged, and
	// AdoptBudget is how this one is moved onto such a run on purpose.
	UsageBudget int64
	// Usage reads what a run has spent, for the comparison against
	// UsageBudget. It is required when UsageBudget is above zero and never
	// called for a run held to none.
	Usage UsageMeter
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
// body runs, which is what Run already did with a run's first checkpoint. The
// executor then re-reads the run's latest checkpoint immediately before every
// body and refuses with ErrStaleAnchor when it is no longer the one this
// segment wrote, so a segment that has lost the run stops rather than running
// more of a caller's code.
//
// That is the whole of what the mechanism delivers: a segment that has already
// lost the run stops before it runs another body. It does not prevent two
// callers from executing one node concurrently. A segment writes its next
// checkpoint only after a body returns, so from the check until that return
// the run's tip does not move, and a second caller reading it in that interval
// claims the run and runs the same node alongside the first. The first learns
// of it when its own write is refused, which is after its body has finished.
// Closing that would need a lease with an expiry and therefore a clock, which
// this package deliberately does not have.
type Executor struct {
	graph       *Graph
	store       CheckpointStore
	budget      int
	usageBudget int64
	meter       UsageMeter
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
	if cfg.UsageBudget < 0 {
		return nil, fmt.Errorf("graph: a usage budget is zero for none or a positive bound, got %d", cfg.UsageBudget)
	}
	if cfg.UsageBudget > 0 && cfg.Usage == nil {
		// A budget nothing can read the spend for would park every run it
		// holds, or, read as no budget, bound nothing while claiming to.
		return nil, fmt.Errorf("graph: a usage budget of %d needs a usage meter to compare it against", cfg.UsageBudget)
	}
	return &Executor{graph: g, store: cfg.Store, budget: cfg.Budget, usageBudget: cfg.UsageBudget, meter: cfg.Usage}, nil
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
	// Budget is the run-wide step budget the run is being held to, which is
	// the one it recorded rather than the one any executor was configured
	// with.
	Budget int
	// Reason explains a run that stopped short, on the terms
	// Checkpoint.Reason states: this executor sets one only when a bound parks
	// the run, but a reason a checkpoint arrived carrying is reported here as
	// it stands.
	Reason string
	// ParkedAt is the edge whose bound parked the run, on the terms
	// Checkpoint.ParkedAt states.
	ParkedAt *EdgeRef
	// UsageBudget is the usage budget the run is being held to, on Budget's
	// terms, zero for none.
	UsageBudget int64
	// ParkedUsage is the reading a usage park was decided on, on the terms
	// Checkpoint.ParkedUsage states.
	ParkedUsage *Usage
}

// Park reports which bound parked the run, on the terms Checkpoint.Park
// states, and false when the result is not a park.
func (r Result) Park() (Park, bool) {
	if !r.Status.Parked() {
		return Park{}, false
	}
	return Park{
		Status: r.Status, Node: r.Position, Edge: r.ParkedAt.clone(), Budget: r.Budget,
		UsageBudget: r.UsageBudget, Usage: r.ParkedUsage.clone(),
	}, true
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
// parked: by one of the three loop bounds, or by the usage budget when the
// run is held to one. A node that returns an error stops the
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
		State:    e.graph.fillLate(initial.Clone()),
		Counters: e.graph.newCounters(e.budget, e.usageBudget),
	}
	// The run-level bounds are weighed before the first checkpoint is written,
	// so a usage reading that cannot be made refuses the run with nothing
	// written and the same Run may be made again.
	start, _ := e.graph.Node(e.graph.start)
	if start.Halt != nil {
		if err := e.haltBefore(ctx, &cp, start); err != nil {
			return Result{}, err
		}
	} else if _, err := e.parkOnRunBounds(ctx, &cp, start.Name); err != nil {
		return Result{}, err
	}
	if err := e.persist(ctx, &cp); err != nil {
		return Result{}, err
	}
	if cp.Status != StatusRunning {
		return e.result(cp), nil
	}
	return e.advance(ctx, cp)
}

// Resume continues a run from its latest checkpoint, which is validated
// against the graph before anything is done with it.
//
// A run waiting on a decision re-emits it and executes nothing: the halted
// node has not started, so there is nothing to replay. A run parked by a bound
// is returned unchanged, because no amount of resuming moves it, and what
// takes it further differs by bound. AdoptBudget moves a budget-exhausted run
// onto more room where it stands. A run parked by a round limit or by
// convergence is taken further by forking it from an earlier checkpoint, one
// whose counters had not yet reached the bound, and resuming that fork: the
// graph is unchanged, so the digest and the budget match, and the loop counts
// again from what that checkpoint recorded. The one refusal no fork point
// reaches is the edge digest, because every checkpoint a run writes carries
// the digest of the graph it started under. Answering is not among the
// remedies either, because Answer takes a run that is halted and a
// bound-parked one is not. A run interrupted mid-flight continues from the
// node it had not yet reached.
//
// It returns an error wrapping ErrBudgetChanged, before anything else, when
// this executor is configured with a run-wide step budget other than the one
// the run recorded. The run is held to the budget it recorded, so the
// difference is reported rather than resolved: resume under that budget, or
// move the run onto this one with AdoptBudget.
//
// It returns an error wrapping ErrBudgetSpent when the run stands at a halt
// point its budget leaves no step for, whether it is halted waiting on that
// decision or holds the claim an answer left behind. Nothing is written and
// nothing runs: a decision nobody could act on is not put back to a caller,
// and a run that could only be parked is not claimed at the cost of the
// answer its checkpoint carries.
//
// A run standing there whose usage budget leaves no room is parked instead,
// StatusUsageExhausted in front of that halt point with its decision
// withdrawn, by one checkpoint. The usage budget differs from the step budget
// here because a decision can be open on a run already past it: a reading
// that failed before the halt point records the run halted there so the node
// before it is kept, and the resume is where that comparison is finished.
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
	// A run standing at a halt point it still has steps for is read against
	// its usage budget first, and parked there when the reading leaves no
	// room, open decision or not. A decision can be open on a run past its
	// usage budget, because a reading that failed before the halt point
	// records the run halted there so the node before it is kept; refusing
	// such a run with ErrUsageSpent on every resume would strand it with its
	// park never recorded, so the resume is where the deferred comparison is
	// finished. The step budget keeps its refusal: a run is halted past it
	// only by a budget deliberately lowered, which AdoptBudget answers.
	if node, _ := e.graph.Node(cp.Position); awaitsItsNode(cp.Status) && node.Halt != nil && !spent(cp.Counters) {
		u, over, err := e.usageExhausted(ctx, cp)
		if err != nil {
			return Result{}, err
		}
		if over {
			cp.Decision = nil
			parkOnUsage(&cp, node.Name, u)
			return e.finish(ctx, cp)
		}
	}
	if err := e.checkBudgetForHalt(ctx, cp); err != nil {
		return Result{}, err
	}
	if cp.Status != StatusRunning {
		return e.result(cp), nil
	}
	// A run standing in front of an ordinary node is weighed against its
	// run-level bounds before it is claimed, so a usage reading that cannot
	// be made refuses the resume with no claim written; a run standing at a
	// halt point was weighed by checkBudgetForHalt above. A run the bounds
	// park is parked by the write that would have claimed it.
	if node, _ := e.graph.Node(cp.Position); node.Halt == nil {
		if _, err := e.parkOnRunBounds(ctx, &cp, node.Name); err != nil {
			return Result{}, err
		}
	}
	if err := e.persist(ctx, &cp); err != nil {
		return Result{}, err
	}
	if cp.Status != StatusRunning {
		return e.result(cp), nil
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
// resolves to one answer.
//
// Once that claim is written the run is running rather than halted, so if the
// node then fails or the process dies, the run's latest checkpoint stands at
// the halt point with the answer recorded and no decision open. Answering
// again is refused with ErrNoOpenDecision because there is no longer a
// decision to answer; Resume is what continues such a run, and it re-executes
// only the node that had not finished.
//
// It returns an error wrapping ErrBudgetSpent, before the claim is written,
// when the run has no step left for the halted node. The decision stays open
// and the answer is not recorded: an answer that could only be parked away is
// refused rather than consumed. It returns an error wrapping ErrBudgetChanged,
// before that, when this executor's budget is not the one the run recorded, on
// the same terms as Resume.
//
// It returns an error wrapping ErrRoundsSpent, before the claim is written,
// when the answer selects a route out of the halt point whose round bound is
// already reached; routeSpent says how that route is found and where finding
// it is exact. The decision stays open for the same reason: an answer that
// could only be parked away is refused rather than consumed.
func (e *Executor) Answer(ctx context.Context, run, answer string) (Result, error) {
	return e.AnswerWith(ctx, run, answer, "")
}

// AnswerWith is Answer with a detail accompanying the answer, which is written
// into the halt point's Halt.Detail key alongside the answer in the same claim.
// An empty detail is no detail. A non-empty one given with an answer the halt
// point does not list in Halt.Detailed is refused with ErrDetailNotAccepted,
// because a detail dropped silently is an instruction nobody acted on, and an
// answer it does list given with no detail is refused with ErrDetailRequired.
// Both are refused before anything is written, so the decision stays open.
func (e *Executor) AnswerWith(ctx context.Context, run, answer, detail string) (Result, error) {
	return e.answer(ctx, run, answer, detail, false, nil)
}

// AnswerUnattended is AnswerWith for an answer the caller declares no person
// gave, such as one a program gives on consent granted in advance. It is
// refused, accepted, and recorded on exactly AnswerWith's terms, with two
// differences.
//
// The first is the bound. Edge.Answered exempts a back edge from the
// convergence check because a person asked for the round it closes; that
// reason does not hold for an answer nobody attended, so a traversal of an
// Answered edge that follows this answer is held to the convergence check
// like any other back edge, and a round that changed no state parks the run
// StatusConverged. The answer is also measured on the edge it takes out of
// the halt point: when the run leaves by that edge in the state its previous
// traversal left in, the round between the two answers changed nothing, and
// the run parks StatusConverged there, standing where the edge leads, before
// the round this answer asks for runs. The answer itself is recorded first,
// on the checkpoint that claims the run, as any answer is; the package
// documentation's "Bounding cycles" says why that comparison is the one that
// parks a loop before it spends its round limit. The fact travels in the
// run's Counters, so it survives a resume between the answer and the
// traversal, and every answer writes it, so the next answer at a halt point
// decides afresh.
//
// The second is claimed: when it is non-nil it is called once, with the
// identifier of the checkpoint carrying the answer, after that checkpoint is
// written and before any node runs. It is how a caller learns the answer was
// applied rather than inferring it from where the run stands afterwards, and
// it is not called for an answer refused before that write. It runs on the
// caller's goroutine and its outcome decides nothing here: the answer is
// applied once the checkpoint is written, whatever the callback does.
//
// The executor does not know who answers, so the declaration is the caller's,
// and an AnswerWith call for an answer nobody attended keeps the exemption.
// That rests on the caller as Edge.Answered's own reading does.
func (e *Executor) AnswerUnattended(ctx context.Context, run, answer, detail string, claimed func(CheckpointID)) (Result, error) {
	return e.answer(ctx, run, answer, detail, true, claimed)
}

// answer is AnswerWith and AnswerUnattended: unattended is what the claim
// records about who gave the answer, and claimed is called once the claim is
// written, when non-nil.
func (e *Executor) answer(ctx context.Context, run, answer, detail string, unattended bool, claimed func(CheckpointID)) (Result, error) {
	cp, err := e.load(ctx, run)
	if err != nil {
		return Result{}, err
	}
	if cp.Status != StatusHalted || cp.Decision == nil {
		return Result{}, fmt.Errorf("%w: run %q is %s, so continue it with Resume rather than answering it",
			ErrNoOpenDecision, run, cp.Status)
	}
	if err := e.checkBudgetForHalt(ctx, cp); err != nil {
		return Result{}, err
	}
	if !answerAllowed(cp.Decision.Options, answer) {
		return Result{}, fmt.Errorf("%w: run %q, answer %q, options %v",
			ErrAnswerNotAllowed, run, answer, cp.Decision.Options)
	}
	node, ok := e.graph.Node(cp.Position)
	if !ok {
		return Result{}, &CheckpointError{Field: "position", Detail: fmt.Sprintf("names no node: %q", cp.Position)}
	}
	takesDetail := node.Halt != nil && len(node.Halt.Detailed) > 0 && answerAllowed(node.Halt.Detailed, answer)
	if detail != "" && !takesDetail {
		return Result{}, fmt.Errorf("%w: run %q, halt point %q, answer %q", ErrDetailNotAccepted, run, node.Name, answer)
	}
	if detail == "" && takesDetail {
		return Result{}, fmt.Errorf("%w: run %q, halt point %q, answer %q", ErrDetailRequired, run, node.Name, answer)
	}
	acc := e.accessFor(node, cp.State.Clone())
	if err := acc.Set(cp.Decision.Into, TextValue(answer)); err != nil {
		return Result{}, err
	}
	if node.Halt != nil && node.Halt.Detail != "" {
		if err := acc.Set(node.Halt.Detail, TextValue(detail)); err != nil {
			return Result{}, err
		}
	}
	if err := e.routeSpent(cp, acc.work); err != nil {
		return Result{}, err
	}
	cp.State = acc.work
	cp.Decision = nil
	cp.Status = StatusRunning
	cp.Counters.Unattended = unattended
	if err := e.persist(ctx, &cp); err != nil {
		return Result{}, err
	}
	if claimed != nil {
		claimed(cp.ID())
	}
	return e.advance(ctx, cp)
}

// AdoptBudget records this executor's run-wide step budget and usage budget
// on a run that started under different ones, and returns where the run then
// stands. It is how either budget is changed on purpose: the change is a
// checkpoint in the run's history saying what both budgets became, so a run's
// bound is never a number that differs between two executors with nothing
// written down about it. The two are adopted together because they are the
// two run-level bounds an executor is configured with, and load refuses a run
// on either difference.
//
// It refuses a completed run, which has no bound left to change, and it
// refuses to write anything when both budgets it would record are the ones
// the run already has. Both leave the run exactly as it stands.
//
// A run either of its budgets parked is revived by budgets that leave it room:
// raising a budget is the remedy for a run it parked the way an answer is the
// remedy for a halted one, and the run stands at a node that never started,
// so nothing is replayed. The revived run is halted again when the node it
// stands at is a halt point, which re-emits that decision, and running
// otherwise; budgets that still leave no room park it again, on whichever of
// the two leaves none, with the usage budget read afresh. A run parked by a
// round limit or by convergence keeps its status: these budgets are not their
// bounds and adopting them does not release them. It returns an error
// wrapping ErrUsageUnread, with nothing written, when reviving a run held to
// a usage budget needs a reading the meter could not make.
//
// Lowering is a deliberate change too, and it may be lowered past what the run
// has already spent. Such a run parks budget-exhausted at its next step, and
// one standing at a halt point is refused with ErrBudgetSpent rather than
// parked, so an open decision is not thrown away by an arithmetic change.
func (e *Executor) AdoptBudget(ctx context.Context, run string) (Result, error) {
	cp, err := e.read(ctx, run)
	if err != nil {
		return Result{}, err
	}
	if cp.Status == StatusCompleted {
		return Result{}, fmt.Errorf(
			"%w: run %q completed, so it has no run-wide step budget left to spend",
			ErrRunCompleted, run)
	}
	if cp.Counters.Budget == e.budget && cp.Counters.UsageBudget == e.usageBudget {
		return e.result(cp), nil
	}
	cp.Counters.Budget = e.budget
	cp.Counters.UsageBudget = e.usageBudget
	if cp.Status == StatusBudgetExhausted || cp.Status == StatusUsageExhausted {
		if err := e.reviveOnBudget(ctx, &cp); err != nil {
			return Result{}, err
		}
	}
	if err := e.persist(ctx, &cp); err != nil {
		return Result{}, err
	}
	return e.result(cp), nil
}

// reviveOnBudget re-decides a budget-parked run's status under the budget it
// now carries. The run stands in front of a node that never started, so this
// is the same decision haltBefore and advance make on arriving there, made
// again rather than a second rule about when a run may go on: budgets that
// still leave no room park it again, with the reason naming the new number.
func (e *Executor) reviveOnBudget(ctx context.Context, cp *Checkpoint) error {
	node, _ := e.graph.Node(cp.Position)
	cp.Status = StatusRunning
	cp.Reason = ""
	cp.ParkedUsage = nil
	if node.Halt != nil {
		return e.haltBefore(ctx, cp, node)
	}
	_, err := e.parkOnRunBounds(ctx, cp, node.Name)
	return err
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

// load reads a run this executor is going to act on: the run's latest
// checkpoint, read and validated by read, and held to the run-wide step budget
// and the usage budget the run recorded. Every entry point that advances a run goes through here,
// so none of them can act on a run whose budget is not the one this executor
// was configured with without saying so first.
//
// The bound the executor applies is the one the checkpoint carries rather than
// this executor's, so the budget in force has a single owner once a run has
// started. This refusal is what keeps that from turning a configured budget
// into a number nobody applied and nobody was told about: the two can never
// differ while a run advances, and a caller who meant to change it is sent to
// AdoptBudget, which writes the change into the run's history.
func (e *Executor) load(ctx context.Context, run string) (Checkpoint, error) {
	cp, err := e.read(ctx, run)
	if err != nil {
		return Checkpoint{}, err
	}
	if cp.Counters.Budget != e.budget {
		return Checkpoint{}, fmt.Errorf(
			"%w: run %q is running under a step budget of %d and this executor is configured with %d; "+
				"resume it under the budget it recorded, or move it onto this one with AdoptBudget",
			ErrBudgetChanged, run, cp.Counters.Budget, e.budget)
	}
	if cp.Counters.UsageBudget != e.usageBudget {
		return Checkpoint{}, fmt.Errorf(
			"%w: run %q is running under a usage budget of %d and this executor is configured with %d; "+
				"resume it under the budget it recorded, or move it onto this one with AdoptBudget",
			ErrUsageBudgetChanged, run, cp.Counters.UsageBudget, e.usageBudget)
	}
	return cp, nil
}

// read reads a run's latest checkpoint, refuses one the store answered with
// for some other run, and validates what is left against the graph. The run
// check cannot live in Graph.Validate, which is not told which run was asked
// for, and it has to happen here so every checkpoint the executor acts on and
// every checkpoint it writes belongs to the run the caller named.
//
// What it returns is a checkpoint the executor owns outright, because a store
// is free to answer with its own storage and advancing a run must not write
// back into a history that was already recorded.
//
// AdoptBudget is the only caller that takes this rather than load, because it
// is the operation whose whole purpose is a budget that does not match.
func (e *Executor) read(ctx context.Context, run string) (Checkpoint, error) {
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
	// Validate accepts a state written before a late key existed, and this is
	// where that key is filled in, so nothing the executor does with the
	// checkpoint from here on, and nothing it writes, lacks it.
	owned := cp.clone()
	owned.State = e.graph.fillLate(owned.State)
	return owned, nil
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

		parked, err := e.parkOnRunBounds(ctx, &cp, node.Name)
		if err != nil {
			return Result{}, err
		}
		if parked {
			return e.finish(ctx, cp)
		}

		if err := e.holdsClaim(ctx, cp); err != nil {
			return Result{}, err
		}
		body, made := bodies[node.Name]
		if !made {
			body = node.NewBody()
			bodies[node.Name] = body
		}
		acc := e.accessFor(node, cp.State.Clone())
		err = body(ctx, acc, acc)
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
				declared := e.graph.edges[edge]
				cp.Position = to
				cp.Status = parked
				cp.Reason = reason
				cp.ParkedAt = &EdgeRef{From: declared.From, To: declared.To, Rounds: declared.Rounds}
				return e.finish(ctx, cp)
			}
		}

		cp.Position = to
		cp.Decision = nil
		if to == "" {
			cp.Status = StatusCompleted
			return e.finish(ctx, cp)
		}
		if next := e.graph.nodes[e.graph.index[to]]; next.Halt != nil {
			if err := e.haltBefore(ctx, &cp, next); err != nil {
				// The node that just ran is kept rather than lost with the
				// reading: the run is recorded halted at the halt point
				// with its decision open, which is the one status a run
				// standing there unanswered can be recorded with. Recording
				// it in front of that node instead would leave a resume to
				// run the node again. Nothing is decided on the open
				// decision before the budget is weighed again: Resume
				// reads it first and parks the run when it leaves no room,
				// and Answer is refused with ErrUsageSpent then.
				cp.Status = StatusHalted
				cp.Decision = decisionFor(next)
				if perr := e.persist(ctx, &cp); perr != nil {
					return Result{}, errors.Join(err, perr)
				}
				return Result{}, err
			}
			return e.finish(ctx, cp)
		}
		cp.Status = StatusRunning
		if err := e.persist(ctx, &cp); err != nil {
			return Result{}, err
		}
	}
}

// haltBefore holds cp in front of n, which declares a halt point. It emits the
// decision n asks, unless either run-level budget leaves the run no room for
// n, in which case it parks the run there on that budget and asks nothing: a
// decision that cannot be acted on must not be put to a person, because
// consent that cannot be used should not be requested. It returns an error
// wrapping ErrUsageUnread, having changed nothing, when the usage budget's
// reading could not be made, so a decision is neither asked nor parked away
// on a spend nobody read. The answer key that decision lands in is
// cleared by persist, which owns that for every checkpoint the executor writes
// at a halt point however the run got there, not only for this one.
//
// This is the one place the executor stops a run in front of a halt point, so
// the two rules hold for every route that gets there: the run that halts at
// the start node and the run that reaches one mid-flight.
func (e *Executor) haltBefore(ctx context.Context, cp *Checkpoint, n Node) error {
	parked, err := e.parkOnRunBounds(ctx, cp, n.Name)
	if err != nil || parked {
		return err
	}
	cp.Status = StatusHalted
	cp.Decision = decisionFor(n)
	return nil
}

// awaitsItsNode reports whether a run with this status still has the node its
// position names to run: a halted run runs it once its decision is answered,
// and a running one once the segment holding the claim goes on. For every
// other status the position records where the run stopped rather than what it
// is waiting to do.
func awaitsItsNode(s Status) bool { return s == StatusHalted || s == StatusRunning }

// spent reports whether a run has no step left to spend. The bound it reads is
// the one the run recorded, not the one this executor was configured with, so
// a run is held to the budget it is running under however it was loaded.
func spent(c Counters) bool { return c.Steps >= c.Budget }

// parkOnBudget records that the run stopped in front of node because its
// run-wide budget was spent. It is the one owner of what that park says.
func parkOnBudget(cp *Checkpoint, node string) {
	cp.Status = StatusBudgetExhausted
	cp.Reason = fmt.Sprintf("the run-wide step budget of %d was spent before node %q could run",
		cp.Counters.Budget, node)
}

// checkBudgetForHalt refuses to take a run standing at a halt point any
// further when its budget leaves no step for that halt point's node. Answer
// and Resume both call it before they claim the run, so the answer stays on
// the checkpoint that carries it rather than being consumed by a segment that
// could only park, and Resume calls it before it hands a halted run back, so
// a decision the run cannot act on is not put to a caller a second time.
//
// It judges only a run that still has the node it stands at to run. A run a
// bound parked in front of a halt point runs nothing more whatever the budget
// says, and Resume returns it unchanged.
//
// It is not the same guard as the one in haltBefore, and neither makes the
// other redundant. haltBefore is what stops a run from asking a question it
// has no step left to act on; this is what stops a run already standing at
// such a question from being taken further. AdoptBudget lowering a budget
// below what a run has already spent is the only way this executor itself
// produces a run standing at a halt point it cannot afford: haltBefore parks
// rather than halts when the budget is spent, and Answer checks this before it
// records anything. It is not the only way a run arrives here, because a
// checkpoint comes from a store and validateCounters compares no step count
// against the budget, so one that already stands at such a halt point is
// refused here rather than anywhere earlier.
//
// The usage budget is judged on the same terms, read afresh, and refused with
// an error wrapping ErrUsageSpent and ErrBudgetSpent. Only Answer meets that
// refusal: Resume parks a run its usage budget leaves no room at a halt point
// before it calls this. A run is halted there past its usage budget after a
// reading failed on the way in, after AdoptBudget lowered the budget, or with
// a checkpoint the store supplied. A reading the meter could not make is ErrUsageUnread,
// with nothing written either way.
func (e *Executor) checkBudgetForHalt(ctx context.Context, cp Checkpoint) error {
	if !awaitsItsNode(cp.Status) {
		return nil
	}
	node, ok := e.graph.Node(cp.Position)
	if !ok || node.Halt == nil {
		return nil
	}
	if spent(cp.Counters) {
		return fmt.Errorf("%w: run %q has spent all %d of its steps, so node %q cannot run",
			ErrBudgetSpent, cp.Run, cp.Counters.Budget, node.Name)
	}
	u, over, err := e.usageExhausted(ctx, cp)
	if err != nil {
		return err
	}
	if over {
		return fmt.Errorf("%w: run %q has %d spent and %d unreported against its usage budget of %d, so node %q cannot run",
			ErrUsageSpent, cp.Run, u.Spent, u.Unreported, cp.Counters.UsageBudget, node.Name)
	}
	return nil
}

// holdsClaim refuses when the run's latest checkpoint is no longer the one
// this segment wrote, which is what says another caller has claimed the run
// since. It is checked immediately before every node body, the first of a
// segment included, where the claim the segment just wrote makes it trivially
// true: one path, no special case for the node a segment starts on.
//
// It narrows the window rather than closing it. Between this read and the body
// starting, another caller can still claim the run and execute the same node,
// and nothing here can see that. Only a lease with an expiry would close it,
// and expiry needs a clock this package does not have.
func (e *Executor) holdsClaim(ctx context.Context, cp Checkpoint) error {
	latest, err := e.store.Latest(ctx, cp.Run)
	if err != nil {
		return err
	}
	if latest.ID() != cp.ID() {
		return fmt.Errorf("%w: this segment holds %s and the run now stands at %s",
			ErrStaleAnchor, cp.ID(), latest.ID())
	}
	return nil
}

// checkEdgeBounds applies the two bounds that live on an edge and, when
// neither parks the run, records the traversal. It returns StatusRunning when
// the edge may be taken.
//
// The round limit is checked before convergence, matching the order the fix
// loop uses: a round that is not eligible to happen never happens, so it never
// gets to report that it changed nothing.
//
// Which traversals the convergence check records and compares is
// Graph.recordsFingerprint and Graph.comparesFingerprint, the one statement of
// that rule, which validateParkedAt reads too.
func (e *Executor) checkEdgeBounds(cp *Checkpoint, edge int) (Status, string) {
	declared := e.graph.edges[edge]
	if declared.Rounds > 0 && cp.Counters.Traversals[edge] >= declared.Rounds {
		return StatusRoundsExhausted, fmt.Sprintf(
			"the edge from %q to %q reached its bound of %d rounds",
			declared.From, declared.To, declared.Rounds)
	}
	if e.graph.recordsFingerprint(edge) {
		fingerprint := e.graph.fingerprint(cp.State)
		held := e.graph.comparesFingerprint(edge, cp.Counters.Unattended)
		if prev := cp.Counters.Fingerprints[edge]; held && prev != "" && prev == fingerprint {
			return StatusConverged, e.graph.convergedReason(edge)
		}
		cp.Counters.Fingerprints[edge] = fingerprint
	}
	cp.Counters.Traversals[edge]++
	return StatusRunning, ""
}

// routeSpent refuses an answer whose route out of the halt point is already
// at its round bound, so the answer is refused while the decision is still
// open instead of being recorded and then parked away by checkEdgeBounds.
//
// It applies to every answer AnswerWith takes, at every halt point of every
// graph, and not only to the requested-round cycle internal/pipeline built it
// for: any halt point whose answered state selects a bounded edge that is
// already spent has that answer refused. Before it existed such an answer was
// recorded and the run parked; now the decision stays open for another one.
//
// The route is the edge next selects from the halt point over the answered
// state, before the halt point's node has run. That is exact for an edge whose
// guard reads the answer or detail key, or any key the node does not write,
// because the node cannot change what the guard reads. A guard over a key the
// node writes is evaluated on the value from before it runs, so for such an
// edge this can select a different route than the run then takes; a bounded
// edge chosen that way is not refused here and is parked by checkEdgeBounds as
// before. Nothing here inspects which kind of guard it is.
func (e *Executor) routeSpent(cp Checkpoint, answered State) error {
	idx, ok := e.graph.index[cp.Position]
	if !ok {
		return nil
	}
	_, edge := e.next(answered, idx)
	if edge < 0 {
		return nil
	}
	declared := e.graph.edges[edge]
	if declared.Rounds > 0 && cp.Counters.Traversals[edge] >= declared.Rounds {
		return fmt.Errorf("%w: run %q, the edge from %q to %q reached its bound of %d rounds",
			ErrRoundsSpent, cp.Run, declared.From, declared.To, declared.Rounds)
	}
	return nil
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

// finish writes a final checkpoint and returns the result it describes.
func (e *Executor) finish(ctx context.Context, cp Checkpoint) (Result, error) {
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
// is one the run advances out of on its own, because a reason explains a run
// that stopped short, and a run that moved on has left that explanation
// behind.
//
// It clears a halt point's answer key on every checkpoint it writes that
// stands at that halt point without running it, whichever route left the run
// there: halted for the decision, or parked by a bound in front of it. That is
// the point rather than housekeeping. An answer is consent to one decision,
// and consent here is explicit for a bounded scope rather than a quiet
// default, so an answer must not outlive the decision it answered: a halt
// point re-entered in a loop asks afresh instead of inheriting the answer the
// last round was given. It is also what makes "the answer key holds an
// answer this halt point accepts" mean "this decision was answered", which is
// what the validator reads it as. The one checkpoint at a halt point that
// keeps its answer is the running one a segment claims the run with, which is
// the answer being given rather than an old one lingering, and which the
// validator requires an accepted answer on for exactly that reason.
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
	if !cp.Status.Stopped() {
		cp.Reason = ""
	}
	if cp.Status != StatusRoundsExhausted && cp.Status != StatusConverged {
		cp.ParkedAt = nil
	}
	if cp.Status != StatusUsageExhausted {
		cp.ParkedUsage = nil
	}
	if idx, ok := e.graph.index[cp.Position]; ok && cp.Status != StatusRunning {
		if node := e.graph.nodes[idx]; node.Halt != nil {
			cp.State.set(node.Halt.Into, TextValue(""))
			if node.Halt.Detail != "" {
				cp.State.set(node.Halt.Detail, TextValue(""))
			}
		}
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
func (e *Executor) result(cp Checkpoint) Result { return cp.Result() }

// checkRunState refuses a state a run cannot start from. It reports the same
// shape invariant Graph.Validate applies to a checkpoint's state, as the
// sentinel errors a caller starting a run handles, and it refuses a state that
// already holds a halt point's answer.
//
// NewState refuses that answer too, and this is not the same check twice: a
// State can also arrive from a Result, whose state is whatever the run it came
// from was holding, so this is the door a state that never passed through
// NewState comes in by.
func (g *Graph) checkRunState(s State) error {
	for key, halt := range g.answers {
		v, ok := s.values[key]
		if !ok {
			continue
		}
		if answer, _ := v.Text(); answer != "" {
			return fmt.Errorf("%w: %q holds %q and is the answer key of the halt point on node %q",
				ErrAnswerPreseeded, key, answer, halt)
		}
	}
	m := g.checkStateShape(s)
	if m == nil {
		return nil
	}
	if m.wrongKind {
		return fmt.Errorf("%w: %s", ErrKindMismatch, m.detail)
	}
	return fmt.Errorf("%w: state %s", ErrStateMismatch, m.detail)
}
