package machine

import (
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/store"
)

// Outcome is what a driving agent reads to decide what to do next. The set is
// closed, and every member is one row of the table below.
type Outcome string

const (
	// OutcomeDecision is a run waiting on an answer. It is not a failure and
	// not a terminal state: it is the normal way a run stops, and answering it
	// is what carries the run on. The answer goes back through the same
	// surface, so a driving agent continues without a person unless the
	// finding needs one.
	OutcomeDecision Outcome = "decision"
	// OutcomeChecksPassed is a run that reached the end of the gate and was
	// not merged. PRD section 9 gives it one meaning beyond that: stop driving
	// and ask the person. Merging is not the gate's to do.
	//
	// What it claims is that the run walked the gate to its end, and no more
	// than that. Each stage cleared in one of the ways PRD section 5 allows -
	// it passed, it was fixed and re-checked, this run skipped it, or a person
	// approved what it found - and which of those happened is on the stage's
	// own report rather than compressed into this word. A build whose stages
	// have no bodies reaches it with every stage approved by a person, and
	// says so stage by stage.
	OutcomeChecksPassed Outcome = "checks-passed"
	// OutcomePassed is a change that merged or closed.
	//
	// Nothing in this build produces it. No record here says a pull request
	// merged: store.Run carries the pull request the run opened and not its
	// state, and the stage that watches checks and mergeability has no body
	// yet. Naming it anyway is deliberate, on the same terms as
	// store.ResolvedByPerson: the vocabulary an agent is written against is
	// the whole set, and a member that appears later must not be a member a
	// driving agent has never seen.
	OutcomePassed Outcome = "passed"
	// OutcomeFailed is a run that ended without a verdict on the change: a
	// bound parked it, or a stage could not run at all. It is terminal and
	// carries a next action, because PRD section 9 makes failure loud rather
	// than silent.
	OutcomeFailed Outcome = "failed"
	// OutcomeCancelled is a run a person ended at a hold. It is terminal, and
	// the work is not undone: the run stopped.
	OutcomeCancelled Outcome = "cancelled"
)

// outcomes is the closed set in the order the table above declares them.
var outcomes = []Outcome{
	OutcomeDecision, OutcomeChecksPassed, OutcomePassed, OutcomeFailed, OutcomeCancelled,
}

// Outcomes returns the closed set. The result is a copy, so a caller cannot
// add to the set by writing to it.
func Outcomes() []Outcome {
	out := make([]Outcome, len(outcomes))
	copy(out, outcomes)
	return out
}

// Terminal reports whether the run this outcome describes has ended. A
// terminal outcome is never followed by another answer to the same run, so a
// driving agent that receives one stops driving that run.
func (o Outcome) Terminal() bool {
	switch o {
	case OutcomeChecksPassed, OutcomePassed, OutcomeFailed, OutcomeCancelled:
		return true
	case OutcomeDecision:
		return false
	default:
		return false
	}
}

// String renders the outcome as it travels.
func (o Outcome) String() string { return string(o) }

// OutcomeOf translates a run into the outcome a driving agent reads. It is the
// one place that translation happens, so the command line and the service
// cannot disagree about what a stopped run means.
//
// The record decides first, and that ordering is the point. A run's status is
// the authoritative record of where it stands, and its last checkpoint is where
// its execution stopped; those are different facts, and they disagree exactly
// when a run was ended from outside its own execution. A run cancelled while it
// stood at a hold has a terminal record and a checkpoint that still says a
// decision is open, and reading the checkpoint alone would offer an answer to a
// run that has ended.
//
// A completed run is checks-passed rather than passed because nothing here
// merges: the gate stops at the end, and PRD section 9 makes that the point at
// which a driving agent asks the person. A run that ended without a verdict is
// cancelled when a person ended it and failed when a bound parked it, which is
// the difference between the two things a terminated record covers; the reason
// on a parked run says which bound. A parked run is terminal here even though
// internal/graph can take it further through a fork or a new budget, and the
// next action says so.
//
// It reads where the run stopped and nothing about what any stage established.
// A run reaches the end when every stage cleared, and the stage reports say how
// each one did; translating those into a verdict about the change is not this
// function's, and a caller that needs it reads them.
func OutcomeOf(record store.RunStatus, status graph.Status, state graph.State) Outcome {
	switch record {
	case store.RunPassed:
		return OutcomeChecksPassed
	case store.RunFailed:
		return OutcomeFailed
	case store.RunTerminated:
		if status != graph.StatusHalted && status.Stopped() {
			return OutcomeFailed
		}
		return OutcomeCancelled
	case store.RunPending, store.RunRunning, store.RunHeld:
		return outcomeOfExecution(status, state)
	default:
		// A status this build does not define is one nothing here can
		// interpret, so it reports a run that ended without a verdict rather
		// than one a caller may drive.
		return OutcomeFailed
	}
}

// outcomeOfExecution translates where a run's execution stopped, for a run
// whose record says it may still move.
func outcomeOfExecution(status graph.Status, state graph.State) Outcome {
	switch status {
	case graph.StatusHalted:
		return OutcomeDecision
	case graph.StatusCompleted:
		if pipeline.Cancelled(state) {
			return OutcomeCancelled
		}
		return OutcomeChecksPassed
	case graph.StatusRoundsExhausted, graph.StatusBudgetExhausted, graph.StatusConverged:
		return OutcomeFailed
	case graph.StatusRunning, graph.StatusInvalid:
		return OutcomeFailed
	default:
		return OutcomeFailed
	}
}

// nextActions is what a caller does about each outcome. PRD section 9 requires
// a terminal outcome to carry one, so this is a row per member rather than a
// sentence for the cases somebody remembered.
var nextActions = map[Outcome]string{
	OutcomeDecision:     "Answer the decision to carry the run on.",
	OutcomeChecksPassed: "The gate is done with this change. Ask the person whether to merge it.",
	OutcomePassed:       "Nothing. The change is merged or closed.",
	OutcomeFailed:       "Read the reason. A run a bound parked is taken further by forking it or by giving it more budget; a run that could not proceed needs the failure fixed and a fresh run.",
	OutcomeCancelled:    "Nothing was undone. Start a fresh run when the change is ready again.",
}

// NextAction is what to do about a run that stopped with this outcome. Every
// member has one, so no outcome is answered with silence.
func (o Outcome) NextAction() string {
	if action, ok := nextActions[o]; ok {
		return action
	}
	return "This build does not recognize that outcome. Report it."
}
