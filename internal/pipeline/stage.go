package pipeline

import (
	"strconv"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/graph"
)

// Stage is one of the nine stages of the delivery gate. The set is closed and
// the order below is the order a run takes them in. PRD principle P2 makes
// both structural rather than configurable: a repository configures what a
// stage runs and how many fix rounds it gets, never which stages exist or what
// order they come in, because "it passed the gate" has to mean the same thing
// in every repository.
type Stage uint8

const (
	// StageInvalid is the zero Stage and names no stage.
	StageInvalid Stage = iota
	// StageIntent establishes what the change set out to do.
	StageIntent
	// StageRebase brings the branch onto freshly fetched upstream and target.
	StageRebase
	// StageReview reviews the change against the diff and the intent. It runs
	// before StageTest, so its first look is at the code the person wrote
	// rather than at code a later stage's fixer changed.
	StageReview
	// StageTest validates this change and this intent, not a full suite.
	StageTest
	// StageDocument updates documentation the change made stale.
	StageDocument
	// StageLint runs static analysis, last among the local checks.
	StageLint
	// StagePush forwards the verified commit to the branch target.
	StagePush
	// StagePR creates or updates the pull request. PRD section 5 calls this
	// stage "Pull request"; the short name is what appears in node names,
	// state keys, and reports.
	StagePR
	// StageCI watches the checks and mergeability. PRD section 5 calls this
	// stage "Checks", and its fix round limit is config.FixRounds.Checks.
	StageCI
)

// stageSpec is one row of the stage table: a stage's identity, its wire name,
// where its implementation sits in a Stages, and which fix round limit governs
// it. Every fact about a stage lives in exactly one row, per P14, and the
// table's length is the nine-ness of the pipeline.
type stageSpec struct {
	stage Stage
	name  string
	// implementation locates this stage's implementation in a Stages. It is
	// what makes a Stages a struct of nine named fields rather than a list: a
	// caller cannot pass ten, nine in another order, or eight.
	implementation func(*Stages) *Implementation
	// rounds locates this stage's automatic fix round limit in a
	// config.FixRounds, or is nil for a stage that takes no automatic fix
	// round. The five stages that have one are exactly the five fields
	// config.FixRounds declares.
	rounds func(*config.FixRounds) *int
	// roundsKey is the configuration key that sets the limit rounds locates,
	// and empty exactly when rounds is nil. It sits in this row beside the
	// field it names so a stage's limit and the key a person changes it with
	// cannot come to name different stages.
	roundsKey config.Key
	// requestable says a person may ask for a fix round at this stage's hold,
	// PRD section 5's Fix action. It is exactly the stages before push. A
	// requested round re-runs its stage and holds again so the person sees the
	// result, and push is the stage that forwards a commit: re-running it
	// would forward the round's commit before the person saw it, and a round
	// at pr or ci, which run after it, leaves a commit nothing in this build
	// pushes.
	requestable bool
}

// stageTable is the pipeline: nine rows, in the fixed order a run takes them.
var stageTable = []stageSpec{
	{StageIntent, "intent",
		func(s *Stages) *Implementation { return &s.Intent }, nil, "", true},
	{StageRebase, "rebase",
		func(s *Stages) *Implementation { return &s.Rebase },
		func(r *config.FixRounds) *int { return &r.Rebase }, config.KeyFixRoundsRebase, true},
	{StageReview, "review",
		func(s *Stages) *Implementation { return &s.Review },
		func(r *config.FixRounds) *int { return &r.Review }, config.KeyFixRoundsReview, true},
	{StageTest, "test",
		func(s *Stages) *Implementation { return &s.Test },
		func(r *config.FixRounds) *int { return &r.Test }, config.KeyFixRoundsTest, true},
	{StageDocument, "document",
		func(s *Stages) *Implementation { return &s.Document }, nil, "", true},
	{StageLint, "lint",
		func(s *Stages) *Implementation { return &s.Lint },
		func(r *config.FixRounds) *int { return &r.Lint }, config.KeyFixRoundsLint, true},
	{StagePush, "push",
		func(s *Stages) *Implementation { return &s.Push }, nil, "", false},
	{StagePR, "pr",
		func(s *Stages) *Implementation { return &s.PR }, nil, "", false},
	{StageCI, "ci",
		func(s *Stages) *Implementation { return &s.CI },
		func(r *config.FixRounds) *int { return &r.Checks }, config.KeyFixRoundsChecks, false},
}

// spec returns the stage's row, and false when s names no stage.
func (s Stage) spec() (stageSpec, bool) {
	for _, row := range stageTable {
		if row.stage == s {
			return row, true
		}
	}
	return stageSpec{}, false
}

// At returns the field of s that holds stage's implementation, and nil for a
// value that names no stage. It reads the stage table, so a caller that walks
// Order with it reaches all nine fields without naming any, and a caller
// wrapping every stage's body - internal/service records each execution's
// timing that way - cannot miss one.
func (s *Stages) At(stage Stage) *Implementation {
	row, ok := stage.spec()
	if !ok {
		return nil
	}
	return row.implementation(s)
}

// Order returns the nine stages in the order a run takes them. The result is a
// copy, so a caller cannot reorder the pipeline by writing to it.
func Order() []Stage {
	out := make([]Stage, len(stageTable))
	for i, row := range stageTable {
		out[i] = row.stage
	}
	return out
}

// String returns the stage's wire name, which is what appears in node names,
// state keys, and anything shown to a caller. A Stage that names no stage
// renders as "stage(N)" so a diagnostic can still quote it.
func (s Stage) String() string {
	if row, ok := s.spec(); ok {
		return row.name
	}
	return "stage(" + strconv.Itoa(int(s)) + ")"
}

// Valid reports whether s is one of the nine stages.
func (s Stage) Valid() bool {
	_, ok := s.spec()
	return ok
}

// ParseStage maps a wire name back to a Stage. An unrecognized name returns
// StageInvalid and false rather than a default, because naming a stage that
// does not exist is a caller's mistake and not something to resolve for them.
func ParseStage(name string) (Stage, bool) {
	for _, row := range stageTable {
		if row.name == name {
			return row.stage, true
		}
	}
	return StageInvalid, false
}

// Node returns the name of the graph node that runs this stage.
func (s Stage) Node() string { return s.String() }

// HoldNode returns the name of the graph node this stage halts at when it
// holds for a person. The node is a halt point, so the engine stops before it
// runs and it does not start until the decision is answered.
func (s Stage) HoldNode() string { return "hold:" + s.String() }

// FixNode returns the name of the graph node that applies this stage's
// fix-eligible findings. The node exists only when the stage takes automatic
// fix rounds and its configured limit is at least one.
func (s Stage) FixNode() string { return "fix:" + s.String() }

// RequestNode returns the name of the graph node that runs a fix round a
// person asked for at this stage's hold. The node exists for every requestable
// stage whenever the pipeline was given a fixer, whatever the stage's
// automatic round limit is, because asking for a round is the person's call
// and not the configuration's.
func (s Stage) RequestNode() string { return "request:" + s.String() }

// RoundsKey returns the configuration key that sets this stage's automatic
// fix round limit, and false for a stage that takes no automatic fix round.
// It is what a person changes when that limit parks a run.
func (s Stage) RoundsKey() (config.Key, bool) {
	row, ok := s.spec()
	if !ok || row.roundsKey == "" {
		return "", false
	}
	return row.roundsKey, true
}

// StageOfNode returns the stage a graph node belongs to: the stage's own node,
// its hold, its fixer, or its requested round. It returns false for a node
// that is no stage's, which is the pipeline's terminal nodes and any name
// this pipeline does not build.
func StageOfNode(node string) (Stage, bool) {
	for _, row := range stageTable {
		switch node {
		case row.stage.Node(), row.stage.HoldNode(), row.stage.FixNode(), row.stage.RequestNode():
			return row.stage, true
		}
	}
	return StageInvalid, false
}

// ParkedStage returns the stage a park belongs to. For a park on an edge's
// bound it is the stage the edge leaves, which is the stage whose loop the
// bound was counting; for a budget park, and for an edge park whose
// checkpoint recorded no edge, it is the stage the run stands in front of.
// It returns false when that node is no stage's.
func ParkedStage(p graph.Park) (Stage, bool) {
	if p.Edge != nil {
		return StageOfNode(p.Edge.From)
	}
	return StageOfNode(p.Node)
}

// Requestable reports whether a person may ask for a fix round at this
// stage's hold, which the stage table answers: the stages before push. A hold
// at any other stage does not offer the round, and a request there is refused
// with ErrFixNotRequestable by the surface answering it and by internal/graph
// as an answer the hold does not offer.
func (s Stage) Requestable() bool {
	row, ok := s.spec()
	return ok && row.requestable
}
