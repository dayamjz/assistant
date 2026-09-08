package journey_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/machine"
)

// bounded is what became of a run held to a step budget smaller than the gate
// it was asked to walk.
type bounded struct {
	// budget is the budget the run was recorded against.
	budget int
	// spent is how many node executions it spent.
	spent int
	// outcome is what the surface reported, and progress is where its
	// execution stopped.
	outcome  machine.Outcome
	progress graph.Status
	// reason is what the surface said about the park.
	reason string
	// answerable is whether the parked run still offered a decision to answer,
	// which no park does.
	answerable bool
	// heldAtLeastOnce is whether any answer in the same walk offered a
	// decision, which is what makes the parked run offering none worth
	// reading: a gate that never offered one would report none whatever the
	// budget did.
	heldAtLeastOnce bool
	// unbounded is what the same journey does with the budget left at its
	// default, which is what says the bound rather than the gate stopped it.
	unbounded machine.Outcome
}

// TestTheRunBudgetStopsARunTheGateWouldHaveFinished drives one of the three
// loop bounds internal/graph owns, through the binary that ships.
//
// PRD section 13 asks for each of the three bounds to stop a run the other two
// would not. Only this one is reachable here. The per-stage round limit and the
// convergence bound both sit on the back edge into a fixer, and a fix round
// needs a stage that reports a fix-eligible finding; no stage of this build
// reports one. A stage with no body reports one unclassified finding and holds
// for a person, which P3 keeps out of a fix round by construction, and the one
// stage that has a body declares no fix rounds and reports only notes. Drives
// records that the same way it records every other condition the missing
// bodies put out of reach.
//
// What makes this a bound rather than a failure is the pair. The same journey
// with the budget left alone reaches the end of the gate, so what stopped the
// bounded run is the budget and not the subject, and a run a bound stopped is
// parked rather than held: no answer reaches it.
func TestTheRunBudgetStopsARunTheGateWouldHaveFinished(t *testing.T) {
	requiresIdentifiedPeer(t)

	// The same subject with nothing bounding it, driven first because the
	// budget is derived from it: it is what says the bound rather than the
	// gate stopped the other run, and it is also the only honest source for a
	// budget that both reaches a hold and stops short of the end. What it
	// costs to reach the first hold rises as stage bodies land, so a number
	// written here would be safe only for as long as this build has the bodies
	// it has today, and the day it stopped being enough the run would park
	// before it ever held - which reads as an absence clause with nothing
	// under it rather than as the stale number it was.
	free := inClone(t)
	freeWalk := answerHolds(t, free,
		startRun(t, free, "--intent", "the same change with the budget left alone"), "approved")
	observed := bounded{unbounded: last(freeWalk).Outcome}

	budget := freeWalk[0].Steps + 1
	if whole := last(freeWalk).Steps; budget >= whole {
		t.Fatalf("this run reaches its first hold after %d node execution(s) and the whole gate costs %d, "+
			"so no budget both reaches a hold and stops the run short of the end; this check needs "+
			"rewriting against whatever the gate costs now", freeWalk[0].Steps, whole)
	}
	observed.budget = budget

	j := inCloneConfigured(t, map[string]any{"run_budget": budget})
	walk := answerHolds(t, j, startRun(t, j, "--intent", "a change held to a budget smaller than the gate"),
		"approved")
	stopped := last(walk)
	for _, answer := range walk[:len(walk)-1] {
		if answer.Decision != nil {
			observed.heldAtLeastOnce = true
		}
	}
	observed.spent = stopped.Steps
	observed.outcome = stopped.Outcome
	observed.reason = stopped.Reason
	observed.answerable = stopped.Decision != nil
	if stopped.Progress != nil {
		observed.progress = *stopped.Progress
	}

	stops := journey.Check[bounded]{
		What: "P2: a run held to a step budget",
		Clauses: []journey.Clause[bounded]{
			{
				States: "the same subject with nothing bounding it reaches the end of the gate",
				Holds: func(b bounded) error {
					if b.unbounded != machine.OutcomeChecksPassed {
						return fmt.Errorf("the same subject with no budget ended %s, so nothing here shows the "+
							"budget is what stopped the other run", b.unbounded)
					}
					return nil
				},
			},
			{
				States: "the run's execution stopped as the budget parks a run",
				Holds: func(b bounded) error {
					if b.progress != graph.StatusBudgetExhausted {
						return fmt.Errorf("the run's execution stopped as %q, and the budget parks a run as %q",
							b.progress, graph.StatusBudgetExhausted)
					}
					return nil
				},
			},
			{
				States: "the run reached the bound it was held to",
				Holds: func(b bounded) error {
					if b.spent < b.budget {
						return fmt.Errorf("the run spent %d node executions against a budget of %d, so it did not "+
							"reach the bound", b.spent, b.budget)
					}
					return nil
				},
			},
			{
				States:  "the parked run offers no decision anybody could answer",
				Absence: true,
				// A walk that never held would show a run offering no decision
				// whatever the budget did. What makes the park's silence worth
				// reading is that this same run was answerable at every stage
				// it reached before the bound stopped it.
				Possible: func(b bounded) error {
					if !b.heldAtLeastOnce {
						return errors.New("no answer before the park offered a decision either, so this " +
							"run never offered one to lose and the park offering none says nothing")
					}
					return nil
				},
				Holds: func(b bounded) error {
					if b.answerable {
						return errors.New("the parked run still offers a decision, and no park is answered")
					}
					return nil
				},
			},
			{
				States: "the parked run is reported as one nothing will move",
				Holds: func(b bounded) error {
					if !b.outcome.Terminal() {
						return fmt.Errorf("the run came back %s, which is not terminal, so a driving agent would "+
							"keep waiting on a run nothing will move", b.outcome)
					}
					return nil
				},
			},
			{
				States: "the parked run says why it parked",
				Holds: func(b bounded) error {
					if b.reason == "" {
						return errors.New("the parked run says nothing about why it parked")
					}
					return nil
				},
			},
		},
		Counterfeits: []journey.Counterfeit[bounded]{
			{Named: "the run was stopped by something other than the budget", Break: func(b bounded) bounded {
				b.progress = graph.StatusConverged
				return b
			}},
			{Named: "the same subject does not finish even without a budget", Break: func(b bounded) bounded {
				b.unbounded = machine.OutcomeFailed
				return b
			}},
			{Named: "the parked run is offered as a decision somebody could answer",
				Break: func(b bounded) bounded {
					b.answerable = true
					return b
				}},
			{Named: "the parked run is reported as one that may still move", Break: func(b bounded) bounded {
				b.outcome = machine.OutcomeExecuting
				return b
			}},
			{Named: "the run never reached the bound at all", Break: func(b bounded) bounded {
				b.spent = 0
				return b
			}},
			{Named: "the park explains nothing", Break: func(b bounded) bounded {
				b.reason = ""
				return b
			}},
		},
	}
	if err := stops.Verify(observed); err != nil {
		t.Fatalf("%v", err)
	}
}
