package pipeline

import "github.com/dayamjz/assistant/internal/graph"

// RebaseAfterPushRounds bounds how many times one run may go back from the
// checks stage to the rebase stage, PRD section 14 decision 7's rebase of a
// pull request that conflicts with its base after the push. The path is a
// back edge from the checks stage's node to the rebase stage's, so
// internal/graph refuses to build it without a bound, and this is that bound.
//
// Each traversal is one rebase, one review, the checks before push again, a
// second anchored push and the pull request update, all counted by the
// run-wide step budget as well. A request for one more than this parks the
// run rounds-exhausted in front of the rebase stage, naming the edge, and
// does not hold it: a pull request that keeps conflicting is a base moving
// faster than the run, and a park says which bound stopped it.
//
// It is a constant rather than a configuration key because PRD section 10
// declares none for it. Two is enough for a base that moved twice while one
// change was in flight, and a third conflict is a person's to look at.
//
// The convergence bound applies to this edge as to every back edge that is
// not answered: a pass that left the run's state exactly as the pass before
// it did parks the run rather than repeating it.
const RebaseAfterPushRounds = 2

// rebaseAfterPushEdge is the back edge a stage whose row says
// Stage.RebasesAfterPush takes when its outcome is OutcomeRebase. It is the
// only edge in the pipeline that leaves one stage for an earlier one other
// than through that stage's own fix or request node.
func rebaseAfterPushEdge(stage Stage) graph.Edge {
	e := guarded(stage.Node(), stage, OutcomeRebase, StageRebase.Node())
	e.Rounds = RebaseAfterPushRounds
	return e
}

// holdExitRounds is the bound on the edge a run leaves stage's hold by when a
// person lets it advance, and zero, which is no bound, for a stage outside the
// path back from the checks stage.
//
// It exists because of how internal/graph tells a back edge from a forward
// one, and not because the edge loops. A stage's hold and the stage after it
// are the same number of hops from the start, so once the path back to rebase
// lets the next stage reach the hold again, the graph's rule reads the edge
// between them as a back edge and refuses to build it unbounded. Every stage
// from rebase up to the one that sends the run back sits on that path, so
// each of their holds gets the bound.
//
// The bound is one traversal for the first pass and one for each pass the path
// back allows. A run leaves one stage's hold that way at most once a pass,
// because the edge leaves the stage behind, so the count can never pass the
// bound before RebaseAfterPushRounds parks the run on the path back first;
// it is there because the graph requires a number, and it is not the bound
// that stops anything.
//
// What the graph applies to every unanswered back edge applies to these as
// well: it fingerprints the state at each traversal, and a pass that leaves a
// hold in exactly the state the previous pass left it parks the run converged.
// That is a pass that changed nothing before that hold, which is the loop the
// convergence bound is for.
func holdExitRounds(stage Stage) int {
	for _, row := range stageTable {
		if row.rebasesAfterPush && stage >= StageRebase && stage < row.stage {
			return RebaseAfterPushRounds + 1
		}
	}
	return 0
}
