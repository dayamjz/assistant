package pipeline

import (
	"fmt"
	"strings"

	"github.com/dayamjz/assistant/internal/agents"
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
// the only skip a caller may ask for is the per-run list in Start.
type Options struct {
	// Stages are the nine implementations. Every field is required.
	Stages Stages
	// Fixer applies fix-eligible findings for every stage that takes automatic
	// fix rounds. It is required when any of those limits is above zero, and a
	// Fixer that is supplied is validated whatever the limits are, so that one
	// legal under a repository's current limits does not become illegal when
	// they change.
	Fixer Fixer
	// Rounds are the per-stage automatic fix round limits, the first of the
	// three bounds on the fix loop. A limit of zero gives the stage no fixer
	// at all, so every finding it reports goes to the person.
	Rounds config.FixRounds
	// Budget is the run-wide step budget, the second bound: the total number
	// of node executions one run may spend however they are distributed. It is
	// checked by the graph when an executor is built, and recorded on a run
	// when it starts, which is what holds the run to it across a resume.
	Budget int
	// UsageBudget is the run's usage budget, beside Budget and on its terms:
	// carried to the executor, recorded on a run when it starts, and the
	// bound a run parks on once its own work has reported spending it. Zero is
	// none. What is counted is Usage's to say; this package counts nothing.
	UsageBudget int64
	// Usage reads what a run has spent against UsageBudget, and is required
	// when UsageBudget is above zero, which the graph refuses to build an
	// executor without.
	Usage graph.UsageMeter
	// Adapter is what the resolved agent adapter declared it supports, which
	// is agents.Resolution.Capabilities. A path this pipeline would build that
	// needs a capability absent from it refuses New, naming the path and the
	// capability, per PRD section 8.
	//
	// The zero value declares nothing, which is the same answer an adapter
	// that says nothing gets: undeclared means unavailable. A pipeline whose
	// paths need nothing therefore builds against it, and one whose paths need
	// anything does not.
	Adapter agents.Capabilities
	// SuppressProjectInstructions is config.Config.SuppressProjectInstructions:
	// every agent is to ignore the repository's own instruction files. It is
	// here rather than left to a stage's own Requires because PRD section 10
	// makes it a property of the run - if any resolved agent lacks a verified
	// suppression mechanism the run fails before launching it - so no stage may
	// decline to need it, and no adapter this build ships declares the
	// capability today.
	SuppressProjectInstructions bool
}

// Pipeline is the built topology: the nine stages, their halt points, their
// fix loops, and the bounds on those loops. It is immutable and safe for
// concurrent use by any number of runs.
type Pipeline struct {
	graph       *graph.Graph
	budget      int
	usageBudget int64
	usage       graph.UsageMeter
}

// New builds the pipeline. This package's own refusals are the sentinels in
// errors.go. A topology internal/graph refuses arrives as its error, which
// ErrUnmergeableFixerWrite's comment names the reachable case of.
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
	if fixing && o.Fixer.NewBody == nil {
		return nil, ErrMissingFixer
	}
	// A fixer is required only when some stage takes rounds, and checked
	// whenever one was supplied. Whether its declaration is legal must not
	// depend on the fix round limits, because P7 has those re-read from the
	// default branch and they can change under a running service.
	if o.Fixer.NewBody != nil {
		if err := checkDeclared("the fixer", "reads", o.Fixer.Reads, declaredReads); err != nil {
			return nil, err
		}
		if err := checkDeclared("the fixer", "writes", o.Fixer.Writes, declaredFixerWrites); err != nil {
			return nil, err
		}
	}
	// What the adapter can do is settled before a node is built, which is what
	// PRD section 8 means by refusing a path before that adapter is launched.
	// A pipeline that would build a path the adapter has not declared is not
	// built at all, so there is no run that could reach one and no weaker path
	// standing in for it.
	if err := checkRequirements(o, fixing); err != nil {
		return nil, err
	}

	b := graph.NewBuilder().Start(StageIntent.Node())
	for _, key := range graphKeys() {
		b.Key(key)
	}
	repairs := repairRounds(limits)
	for i, row := range stageTable {
		next := NodeDone
		if i+1 < len(stageTable) {
			next = stageTable[i+1].stage.Node()
		}
		if err := wire(b, row.stage, *row.implementation(&o.Stages), o.Fixer, limits[i], repairs, next); err != nil {
			return nil, err
		}
	}
	b.Node(doneNode())
	b.Node(cancelNode())

	g, err := b.Build()
	if err != nil {
		return nil, err
	}
	return &Pipeline{graph: g, budget: o.Budget, usageBudget: o.UsageBudget, usage: o.Usage}, nil
}

// wire adds one stage's nodes and every edge leaving them. Every stage is
// wired by this one function, so the shape of a stage is the same for all nine
// and a stage cannot be given an arrangement of its own. repairs is the fix
// round limit of the stage that repairs after the push, which every hold on
// that path is bounded by as well as its own.
func wire(b *graph.Builder, stage Stage, impl Implementation, fixer Fixer, rounds, repairs int, next string) error {
	requests := fixer.NewBody != nil && stage.Requestable()
	repairing := stage.RepairsAfterPush() && rounds > 0
	if impl.NewBody == nil {
		return fmt.Errorf("%w: %s", ErrMissingStage, stage)
	}
	if err := checkDeclared("stage "+stage.String(), "reads", impl.Reads, declaredReads); err != nil {
		return err
	}
	if err := checkDeclared("stage "+stage.String(), "writes", impl.Writes, declaredStageWrites); err != nil {
		return err
	}
	b.Node(stageNode(stage, impl, rounds > 0 || requests, rounds > 0, repairing))
	b.Node(holdNode(stage, requests))
	if requests {
		// PRD section 5's Fix action at a hold: the stage's fixer runs one
		// round over what the person selected and the stage runs again. It is
		// built only at a requestable stage, one before push, because only
		// there does the stage's re-run hold before the round's commit leaves
		// the run's copy. That is a second cycle through the stage, so both
		// of its edges carry RequestedFixRounds, and the edge in reaches the
		// bound first for the reason the automatic entry does: every return
		// follows an entry it counted.
		//
		// The edge back is graph.Edge.Answered: every traversal follows an
		// answer at the hold, which internal/graph checks at build, so a
		// round a person asked for that changed nothing returns to the hold
		// for them to see rather than parking the run on convergence. A round
		// asked for by an unattended answer is not exempt, which
		// internal/graph decides from how the answer was given, and it
		// measures such an answer on the edge in as well, so a loop of them
		// that changes nothing parks before the third round; doc.go says
		// why the edge back alone would not.
		// RequestedFixRounds and the run-wide budget bound both.
		//
		// The edge in is guarded on the answer key rather than on the
		// outcome the hold node writes from it. Both say the same thing once
		// the node has run, and the answer is the one graph.Executor.Answer
		// can read before it has, which is what lets it refuse an answer
		// asking for a round past the bound while the decision is still open
		// instead of recording it and parking the run.
		//
		// They are the first edges of the stage's block, which is what keeps
		// every back edge's index inside the stage's own range; doc.go has the
		// arithmetic and TestBackEdgeIndicesNeverCrossStagesAcrossConfigurations
		// pins it.
		b.Node(requestNode(stage, fixer))
		b.Edge(graph.Edge{
			From:   stage.HoldNode(),
			To:     stage.RequestNode(),
			Rounds: RequestedFixRounds,
			Guard: &graph.Guard{
				Key:   string(stage.AnswerKey()),
				Op:    graph.OpEquals,
				Value: graph.TextValue(string(OutcomeFixRequested)),
			},
		})
		b.Edge(graph.Edge{From: stage.RequestNode(), To: stage.Node(), Rounds: RequestedFixRounds, Answered: true})
	}

	// PRD section 5's Retry: the stage runs again from the top, through the
	// retry node, which drops the stale holds a refused round left; a fix
	// round the ceiling refused is taken again after the stage, over
	// rerunEntry below. Every
	// stage has it, because the usage ceiling can refuse a call at any stage
	// whose body or fix round makes one, and RetryOffered decides which holds
	// offer it. It is a second cycle through the stage, bounded by
	// RetryRounds on both edges; retry.go says why the edge back is answered.
	// Its edges sit with the request cycle's, ahead of the hold's other exits
	// and the stage's own edges, for the reason those do.
	b.Node(retryNode(stage))
	for _, e := range retryEdges(stage) {
		b.Edge(e)
	}

	// The hold's other two exits. They come before the stage's own edges
	// rather than after them because the exit to the next stage is a back
	// edge for a stage on a path back from the checks stage, and
	// every back edge of a stage sits ahead of the stage's configured fix
	// return; repush.go says why that exit is a back edge at all, and doc.go
	// has the arithmetic. Order among edges matters only per node they leave,
	// and these leave the hold, whose unconditional exit is still its last.
	b.Edge(guarded(stage.HoldNode(), stage, OutcomeCancelled, NodeCancel))
	b.Edge(graph.Edge{From: stage.HoldNode(), To: next, Rounds: holdExitRounds(stage, repairs)})
	if stage.RebasesAfterPush() {
		// PRD section 14 decision 7's rebase of a conflicted pull request:
		// the stage sends the run back to rebase, and from there through
		// review, the checks before push, and a second push, to stand here
		// again. It is a back edge, so it carries a bound, and it comes ahead
		// of the stage's own edges for the reason the hold's exits do.
		// Its guard and the stage's other guards read the one outcome key, so
		// at most one of them passes and the order among them decides nothing.
		b.Edge(rebaseAfterPushEdge(stage))
	}
	if repairing {
		// PRD section 5's repair of a red CI run: a fix round of the stage
		// committed after the push, and the stage sends the run back to the
		// review stage with that commit, from where it passes the checks
		// before push and a second anchored push to stand here again. It is
		// a back edge bounded by the stage's own round limit, built only when
		// that limit is above zero, since without a fix round there is no
		// repair; repair.go says why the limit is the bound and why it is the
		// edge into the fixer that reaches it first. It sits with the path
		// back to rebase, ahead of the fix return and the stage's own edges,
		// for the reason that path does, and its guard reads the same outcome
		// key, so the order between them decides nothing.
		b.Edge(repairAfterPushEdge(stage, rounds))
	}

	// A stage that found something a person must decide holds. A stage that
	// found only fix-eligible findings goes to its fixer, and to the same hold
	// when it has none, which is what a round limit of zero means.
	fixes := stage.HoldNode()
	if rounds > 0 {
		fixes = stage.FixNode()
		b.Node(fixNode(stage, fixer))
		// The back edge, which closes the stage's fix cycle. The graph
		// requires a bound on it, and it is the sum of the bounds on the two
		// edges into the fixer below, the stage's round limit and
		// RetryRounds. It cannot be the first to reach its round bound,
		// because every return through here follows an entry one of those
		// edges already counted, so one of them reaches its bound first.
		// That once failed across a resume under changed fix round limits,
		// where the two edges could inherit counts from different edges;
		// internal/graph now refuses such a resume, which the
		// counter-misattribution paragraphs in doc.go trace.
		//
		// The fix node is entered from the stage alone, so internal/graph
		// starts this edge's convergence measure afresh when a person's
		// retry or requested round sends the run back into the stage over
		// an answered edge: a round the ceiling refuses again after a retry
		// repeats the round refused before it, and compared with that round
		// it would park the run on the second retry with no room, which
		// retry.go's edges say a retry never does.
		b.Edge(graph.Edge{From: stage.FixNode(), To: stage.Node(), Rounds: rounds + RetryRounds})
	}
	b.Edge(guarded(stage.Node(), stage, OutcomeHeld, stage.HoldNode()))
	b.Edge(rerunEntry(stage, fixes))
	// The edge a round is decided on, and the one the round limit stops a run
	// at: a stage still reporting fix-eligible findings after its last round
	// parks in front of the fixer rather than taking a round nothing would
	// verify. A limit of zero leaves this edge unbounded, and correctly so: it
	// goes to the hold rather than to a fixer, and a hold is not a round.
	entry := guarded(stage.Node(), stage, OutcomeFixable, fixes)
	entry.Rounds = rounds
	b.Edge(entry)
	b.Edge(graph.Edge{From: stage.Node(), To: next})
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
// run-wide step budget and the usage budget, with its meter, the pipeline was
// built with. The graph refuses a budget
// below one, because an executor with no run-wide bound is not the same as one
// with a generous bound.
//
// A run records the budget it starts under, so a pipeline rebuilt with a
// different one does not resume an existing run: the graph refuses it with
// graph.ErrBudgetChanged, and graph.Executor.AdoptBudget is how a run is moved
// onto the new budget on purpose; the usage budget is held the same way. The
// same applies to the fix round limits,
// which the graph refuses on the edge digest a checkpoint carries.
func (p *Pipeline) Executor(store graph.CheckpointStore) (*graph.Executor, error) {
	return graph.NewExecutor(p.graph, graph.Config{
		Store: store, Budget: p.budget, UsageBudget: p.usageBudget, Usage: p.usage,
	})
}

// Start is what a run begins with. Skip is the only way a caller may choose
// that a stage does not run, and it is per run: nothing in Options can set it.
type Start struct {
	// Repository identifies the repository being validated, as
	// internal/store records it.
	Repository string
	// Run identifies this run, as internal/store records it.
	Run string
	// ForgeRepository names the repository on the code host, in the notation
	// that host addresses a repository by. It is empty when the run's record
	// does not name one on a host this build talks to, and a stage that needs
	// a code host refuses on an empty one rather than resolving a repository
	// for itself.
	//
	// NewState does not require it, because eight of the nine stages need no
	// code host and a run that cannot reach one still has to be able to walk
	// them and report.
	ForgeRepository string
	// ForkRepository names the fork the run pushes to and proposes its pull
	// request from, in ForgeRepository's notation, and is empty when the run
	// pushes to the upstream itself. NewState refuses one given without a
	// ForgeRepository, because a fork with no repository to propose to is
	// half of a pair.
	ForkRepository string
	// Branch is the branch under validation.
	Branch string
	// Base is the branch target the change is rebased onto and pushed to.
	Base string
	// Submitted is the commit the push submitted to the gate.
	Submitted string
	// SubmittedSubject is the subject of the Submitted commit, already passed
	// through internal/redact. It may be empty, and the pull request stage
	// then titles a run without intent with the branch name.
	SubmittedSubject string
	// Intent is what the change set out to do, when the run carries one.
	// Whether it is acceptance criteria or a hint is IntentSupplied's to say,
	// not this text's.
	Intent string
	// IntentSupplied says the intent above was supplied rather than inferred,
	// which makes it authoritative acceptance criteria downstream. Setting it
	// with an empty or whitespace-only Intent is refused with ErrEmptyIntent,
	// because there is nothing for those criteria to be.
	IntentSupplied bool
	// Skip names the stages this one run skips, on purpose.
	Skip []Stage
	// NoCodeHost says the run's upstream is a local path or file:// remote,
	// so the pull request and checks stages skip after push. Begin sets it
	// from forge.ClassifyRemote; a caller does not invent it.
	NoCodeHost bool
	// HousekeepingPass says the run is of a housekeeping pass's branch, on
	// KeyHousekeepingPass's terms, whoever pushed it. internal/service sets
	// it at begin from the task records; a caller does not invent it.
	HousekeepingPass bool
}

// NewState returns the initial state for a run. It refuses a start that does
// not say what is being validated, one that claims a supplied intent and
// supplies none, and one naming a stage to skip that is not one of the nine.
func (p *Pipeline) NewState(s Start) (graph.State, error) {
	if s.Repository == "" || s.Run == "" || s.Branch == "" || s.Base == "" || s.Submitted == "" {
		return graph.State{}, fmt.Errorf("%w: repository %q, run %q, branch %q, base %q, submitted %q",
			ErrIncompleteRun, s.Repository, s.Run, s.Branch, s.Base, s.Submitted)
	}
	if s.ForkRepository != "" && s.ForgeRepository == "" {
		return graph.State{}, fmt.Errorf("%w: fork %q names no repository on the code host to propose to",
			ErrIncompleteRun, s.ForkRepository)
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
		string(KeyRepository):       graph.TextValue(s.Repository),
		string(KeyRun):              graph.TextValue(s.Run),
		string(KeyForgeRepository):  graph.TextValue(s.ForgeRepository),
		string(KeyForkRepository):   graph.TextValue(s.ForkRepository),
		string(KeyBranch):           graph.TextValue(s.Branch),
		string(KeyBase):             graph.TextValue(s.Base),
		string(KeySubmitted):        graph.TextValue(s.Submitted),
		string(KeySubmittedSubject): graph.TextValue(s.SubmittedSubject),
		string(KeyHead):             graph.TextValue(s.Submitted),
		string(KeyIntent):           graph.TextValue(s.Intent),
		string(KeyIntentSupplied):   graph.BoolValue(s.IntentSupplied),
		string(KeySkip):             graph.ListValue(skip...),
		string(KeyNoCodeHost):       graph.BoolValue(s.NoCodeHost),
		string(KeyHousekeepingPass): graph.BoolValue(s.HousekeepingPass),
	})
}
