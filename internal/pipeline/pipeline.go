package pipeline

import (
	"fmt"
	"strings"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/graph"
)

// Stages carries one implementation per stage. It is a struct of nine named
// fields rather than a list, and that is the mechanism P2 rests on: there is
// no way to hand this package ten stages, eight, or the nine in another order.
// The order a run takes them in is the stage table's, which no caller can
// reach.
type Stages struct {
	// Intent establishes what the change set out to do.
	Intent Implementation
	// Rebase brings the branch onto freshly fetched upstream and target.
	Rebase Implementation
	// Review reviews the change against the diff and the intent.
	Review Implementation
	// Test validates this change and this intent.
	Test Implementation
	// Document updates documentation the change made stale.
	Document Implementation
	// Lint runs static analysis.
	Lint Implementation
	// Push forwards the verified commit.
	Push Implementation
	// PR creates or updates the pull request.
	PR Implementation
	// CI watches the checks and mergeability.
	CI Implementation
}

// Options is everything the pipeline is built from. There is deliberately no
// field here that removes a stage, reorders the stages, or skips one: a
// standing configuration may make a stage stricter and may not weaken one, so
// the only skip this package admits is the per-run list in Start.
type Options struct {
	// Stages are the nine implementations. Every field is required.
	Stages Stages
	// Fixer applies fix-eligible findings for every stage that takes automatic
	// fix rounds. It is required when any of those limits is above zero, and
	// unused when they are all zero.
	Fixer Fixer
	// Rounds are the per-stage automatic fix round limits, the first of the
	// three bounds on the fix loop. A limit of zero gives the stage no fixer
	// at all, so every finding it reports goes to the person.
	Rounds config.FixRounds
	// Budget is the run-wide step budget, the second bound: the total number
	// of node executions one run may spend however they are distributed. It is
	// checked by the graph when an executor is built, which is where it takes
	// effect.
	Budget int
}

// Pipeline is the built topology: the nine stages, their halt points, their
// fix loops, and the bounds on those loops. It is immutable and safe for
// concurrent use by any number of runs.
type Pipeline struct {
	graph  *graph.Graph
	budget int
}

// New builds the pipeline. It refuses an Options that leaves a stage without
// an implementation, that gives a stage fix rounds with no fixer to apply
// them, or that declares a read or write the state schema does not admit.
//
// The three bounds on the fix loop are settled here and cannot be settled
// later. Each stage's round limit bounds the cycle it sits on, which the graph
// requires of every back edge at construction. The run-wide budget is carried
// to the executor. Convergence is the graph's, and it applies to every back
// edge whether or not anything here asks for it.
func New(o Options) (*Pipeline, error) {
	limits := make([]int, len(stageTable))
	fixing := false
	for i, row := range stageTable {
		rounds, err := o.roundsFor(row.stage)
		if err != nil {
			return nil, err
		}
		limits[i] = rounds
		fixing = fixing || rounds > 0
	}
	if fixing {
		if o.Fixer.NewBody == nil {
			return nil, ErrMissingFixer
		}
		if err := checkDeclared("the fixer", "reads", o.Fixer.Reads, false); err != nil {
			return nil, err
		}
		if err := checkDeclared("the fixer", "writes", o.Fixer.Writes, true); err != nil {
			return nil, err
		}
	}

	b := graph.NewBuilder().Start(StageIntent.Node())
	for _, key := range graphKeys() {
		b.Key(key)
	}
	for i, row := range stageTable {
		next := NodeDone
		if i+1 < len(stageTable) {
			next = stageTable[i+1].stage.Node()
		}
		if err := wire(b, row.stage, *row.implementation(&o.Stages), o.Fixer, limits[i], next); err != nil {
			return nil, err
		}
	}
	b.Node(doneNode())
	b.Node(cancelNode())

	g, err := b.Build()
	if err != nil {
		return nil, err
	}
	return &Pipeline{graph: g, budget: o.Budget}, nil
}

// wire adds one stage's nodes and every edge leaving them. Every stage is
// wired by this one function, so the shape of a stage is the same for all nine
// and a stage cannot be given an arrangement of its own.
func wire(b *graph.Builder, stage Stage, impl Implementation, fixer Fixer, rounds int, next string) error {
	if impl.NewBody == nil {
		return fmt.Errorf("%w: %s", ErrMissingStage, stage)
	}
	if err := checkDeclared("stage "+stage.String(), "reads", impl.Reads, false); err != nil {
		return err
	}
	if err := checkDeclared("stage "+stage.String(), "writes", impl.Writes, true); err != nil {
		return err
	}
	b.Node(stageNode(stage, impl))
	b.Node(holdNode(stage))

	// A stage that found something a person must decide holds. A stage that
	// found only fix-eligible findings goes to its fixer, and to the same hold
	// when it has none, which is what a round limit of zero means.
	fixes := stage.HoldNode()
	if rounds > 0 {
		fixes = stage.FixNode()
		b.Node(fixNode(stage, fixer))
		// The back edge, which closes the one cycle in the pipeline. The graph
		// requires a bound on it and the stage's round limit is that bound. It
		// is the same limit the edge into the fixer carries below, and it
		// cannot be the first to stop a run, because every return through here
		// follows an entry that edge already counted.
		b.Edge(graph.Edge{From: stage.FixNode(), To: stage.Node(), Rounds: rounds})
	}
	b.Edge(guarded(stage.Node(), stage, OutcomeHeld, stage.HoldNode()))
	// The edge a round is decided on, and the one the round limit stops a run
	// at: a stage still reporting fix-eligible findings after its last round
	// parks in front of the fixer rather than taking a round nothing would
	// verify. A limit of zero leaves this edge unbounded, and correctly so: it
	// goes to the hold rather than to a fixer, and a hold is not a round.
	entry := guarded(stage.Node(), stage, OutcomeFixable, fixes)
	entry.Rounds = rounds
	b.Edge(entry)
	b.Edge(graph.Edge{From: stage.Node(), To: next})

	b.Edge(guarded(stage.HoldNode(), stage, OutcomeCancelled, NodeCancel))
	b.Edge(graph.Edge{From: stage.HoldNode(), To: next})
	return nil
}

// roundsFor returns the automatic fix round limit for a stage: the limit
// config.FixRounds declares for it, or zero for a stage that takes no
// automatic fix round at all. Which stages those are is the stage table's, not
// a caller's.
func (o Options) roundsFor(stage Stage) (int, error) {
	row, ok := stage.spec()
	if !ok || row.rounds == nil {
		return 0, nil
	}
	rounds := *row.rounds(&o.Rounds)
	if rounds < 0 {
		return 0, fmt.Errorf("%w: %s declares %d", ErrNegativeRounds, stage, rounds)
	}
	return rounds, nil
}

// guarded builds an edge taken when a stage stands at a particular outcome.
// Every conditional edge in the pipeline is one of these: the stage's outcome
// key is the only thing the topology routes on.
func guarded(from string, stage Stage, outcome Outcome, to string) graph.Edge {
	return graph.Edge{
		From: from,
		To:   to,
		Guard: &graph.Guard{
			Key:   string(stage.OutcomeKey()),
			Op:    graph.OpEquals,
			Value: graph.TextValue(string(outcome)),
		},
	}
}

// Graph returns the built topology.
func (p *Pipeline) Graph() *graph.Graph { return p.graph }

// Executor returns an executor for this pipeline against store, carrying the
// run-wide step budget the pipeline was built with. The graph refuses a budget
// below one, because an executor with no run-wide bound is not the same as one
// with a generous bound.
func (p *Pipeline) Executor(store graph.CheckpointStore) (*graph.Executor, error) {
	return graph.NewExecutor(p.graph, graph.Config{Store: store, Budget: p.budget})
}

// Start is what a run begins with. Skip is the only way a stage does not run,
// and it is per run: nothing in Options can set it.
type Start struct {
	// Branch is the branch under validation.
	Branch string
	// Base is the branch target the change is rebased onto and pushed to.
	Base string
	// Submitted is the commit the push submitted to the gate.
	Submitted string
	// Intent is what the change set out to do, when the person supplied it.
	Intent string
	// IntentSupplied says the intent above was supplied rather than inferred,
	// which makes it authoritative acceptance criteria downstream. Setting it
	// with an empty or whitespace-only Intent is refused with ErrEmptyIntent,
	// because there is nothing for those criteria to be.
	IntentSupplied bool
	// Skip names the stages this one run skips, on purpose.
	Skip []Stage
}

// NewState returns the initial state for a run. It refuses a start that does
// not say what is being validated, one that claims a supplied intent and
// supplies none, and one naming a stage to skip that is not one of the nine.
func (p *Pipeline) NewState(s Start) (graph.State, error) {
	if s.Branch == "" || s.Base == "" || s.Submitted == "" {
		return graph.State{}, fmt.Errorf("%w: branch %q, base %q, submitted %q",
			ErrIncompleteRun, s.Branch, s.Base, s.Submitted)
	}
	if s.IntentSupplied && strings.TrimSpace(s.Intent) == "" {
		return graph.State{}, fmt.Errorf("%w: intent %q", ErrEmptyIntent, s.Intent)
	}
	skip := make([]string, 0, len(s.Skip))
	for _, stage := range s.Skip {
		if !stage.Valid() {
			return graph.State{}, fmt.Errorf("%w: %s", ErrUnknownStage, stage)
		}
		skip = append(skip, stage.String())
	}
	return p.graph.NewState(map[string]graph.Value{
		string(KeyBranch):         graph.TextValue(s.Branch),
		string(KeyBase):           graph.TextValue(s.Base),
		string(KeySubmitted):      graph.TextValue(s.Submitted),
		string(KeyHead):           graph.TextValue(s.Submitted),
		string(KeyIntent):         graph.TextValue(s.Intent),
		string(KeyIntentSupplied): graph.BoolValue(s.IntentSupplied),
		string(KeySkip):           graph.ListValue(skip...),
	})
}
