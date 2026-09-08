package journey_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// boundary is one stage boundary of a run, with the service killed at it.
type boundary struct {
	// standing is the run as the surface reported it just before the service
	// was killed.
	standing machine.Run
	// recovered is the run as the surface reported it once a service was
	// serving the home again.
	recovered machine.Run
	// servedBy and recoveredBy are the operating system's identifiers for the
	// process serving the home either side of the kill. They are the evidence
	// that a kill happened at all: a harness whose kill quietly did nothing
	// would report a run recovering across nothing, and the two answers would
	// be equal for the least interesting reason there is.
	servedBy    int
	recoveredBy int
}

// survival is a whole run driven with the service killed at every stage
// boundary it reaches.
type survival struct {
	// boundaries are the kills, in the order the run reached them.
	boundaries []boundary
	// ended is the run's final answer, after the last boundary was answered.
	ended machine.Run
}

// TestARunSurvivesTheServiceBeingKilledAtEveryStageBoundary drives PRD
// principle P6 through the binary that ships.
//
// It is the third of section 13's tests for P6, and the PRD writes it as the
// verification criterion rather than as an example: kill the service mid-run
// at each stage boundary, and every time the work is recoverable and the
// classification is correct. What the criterion turns on is every boundary,
// not a count of them: a stage boundary in this build is a hold, because
// internal/graph stops before a halt point runs, so that is where the kill
// lands, and a stage that never holds offers none. The intent stage is such a
// stage - it has a body and PRD section 5 has it never block a run, so every
// finding it reports is a note - and so are the review and pull request
// stages on this run, which skips both for the reasons walkableRun states: a
// skipped stage runs nothing that could hold. None of the three is among the
// boundaries here, and manufacturing a kill for any of them would be a kill
// with nothing under it.
//
// The kill is a kill and not a stop. A service asked to stop unwinds and
// writes what it knows on the way out, and what P6 is about is the service
// that never got the chance: everything the run's position rests on has to
// already be durable when the process dies.
//
// Correct classification is asserted as well as recoverability, because those
// are two different failures. A run that comes back reported as running is one
// no answer reaches, since responding is refused for a run that is not held,
// and a caller looking at it is told to wait for something that will never
// move.
func TestARunSurvivesTheServiceBeingKilledAtEveryStageBoundary(t *testing.T) {
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P6)

	j := inClone(t)

	// A run stops at every stage this build has no body for, which is read off
	// internal/stages rather than counted to nine: a body that lands takes a
	// boundary away, and a check written against the stage count would fail
	// for a reason that is not P6.
	holding := stagesWithoutABody(t)

	observed := survival{}
	current := walkableRun(t, j, "narrow the Total loop bound on purpose")
	// Bounded for the reason answerHolds is bounded: a run that stops
	// advancing past a hold turns this into an unbounded kill-and-serve loop,
	// and the suite hanging to the test timeout says nothing about which stage
	// the run stopped at. The bound is a multiple of the stage count because
	// the check below compares the boundaries reached against exactly that.
	for range 2 * len(holding) {
		if current.Outcome != machine.OutcomeDecision {
			break
		}
		served := j.ServicePID()
		if err := j.Kill(); err != nil {
			t.Fatalf("killing the service while the run stood at %s: %v", current.Position, err)
		}
		if err := serving(t, j); err != nil {
			t.Fatalf("serving the home again after the kill at %s: %v", current.Position, err)
		}
		observed.boundaries = append(observed.boundaries, boundary{
			standing:    current,
			recovered:   startRun(t, j),
			servedBy:    served,
			recoveredBy: j.ServicePID(),
		})
		current = decodeRun(t, succeeds(t, j.Command("--answer", "approved")))
	}
	if current.Outcome == machine.OutcomeDecision {
		t.Fatalf("the run was killed and answered at %d boundaries and is still holding at %s; this build "+
			"has %d stage(s) with no body", len(observed.boundaries), current.Position, len(holding))
	}
	observed.ended = current
	if len(observed.boundaries) == 0 {
		t.Fatalf("the run reached no stage boundary at all, so there was nothing to kill it at; it "+
			"ended %s at %q", current.Outcome, current.Position)
	}

	// Which boundary a counterfeit changes is wrapped into the walk it was
	// derived from. How many boundaries a run reaches is how many stages this
	// build has no body for, so a literal position stops being in range the
	// day a body lands, and a counterfeit that panicked would take the check
	// down with something that is not a finding.
	at := func(nth int) int { return nth % len(observed.boundaries) }

	survived := journey.Check[survival]{
		What: "P6: a run killed at every stage boundary",
		Clauses: []journey.Clause[survival]{
			{
				States: "the service was killed at every boundary this run stops at",
				Holds: func(s survival) error {
					if len(s.boundaries) != len(holding) {
						return fmt.Errorf("the service was killed at %d boundaries and this build has %d "+
							"stage(s) with no body, each of which is a boundary a run stops at",
							len(s.boundaries), len(holding))
					}
					return nil
				},
			},
			{
				States: "a serving process was recorded either side of every kill",
				Holds: func(s survival) error {
					return atEachBoundary(s, func(stage string, at boundary) error {
						if at.servedBy == 0 || at.recoveredBy == 0 {
							return fmt.Errorf("at %s: no serving process was recorded either side of the kill", stage)
						}
						return nil
					})
				},
			},
			{
				States: "a different process answered either side of every kill, so something was killed",
				Holds: func(s survival) error {
					return atEachBoundary(s, func(stage string, at boundary) error {
						if at.servedBy == at.recoveredBy {
							return fmt.Errorf("at %s: the same process %d answered either side of the kill, so "+
								"nothing was killed and nothing was recovered", stage, at.servedBy)
						}
						return nil
					})
				},
			},
			{
				States: "the run came back standing at the node it stood at",
				Holds: func(s survival) error {
					return atEachBoundary(s, func(stage string, at boundary) error {
						if at.standing.Position != at.recovered.Position {
							return fmt.Errorf("killed at %s: the run stood at %q and came back at %q",
								stage, at.standing.Position, at.recovered.Position)
						}
						return nil
					})
				},
			},
			{
				States: "the run came back recorded as held, which is the status an answer reaches",
				Holds: func(s survival) error {
					return atEachBoundary(s, func(stage string, at boundary) error {
						if at.recovered.Record.Status != store.RunHeld {
							return fmt.Errorf("killed at %s: the run came back recorded as %q, and a run that is "+
								"not held is one no answer reaches", stage, at.recovered.Record.Status)
						}
						return nil
					})
				},
			},
			{
				States: "the run came back reported as waiting on a decision",
				Holds: func(s survival) error {
					return atEachBoundary(s, func(stage string, at boundary) error {
						if at.recovered.Outcome != machine.OutcomeDecision {
							return fmt.Errorf("killed at %s: the run came back reported as %s rather than waiting "+
								"on a decision", stage, at.recovered.Outcome)
						}
						return nil
					})
				},
			},
			{
				States: "the run came back carrying a decision to answer",
				Holds: func(s survival) error {
					return atEachBoundary(s, func(stage string, at boundary) error {
						if at.recovered.Decision == nil {
							return fmt.Errorf("killed at %s: the run came back with no decision to answer", stage)
						}
						return nil
					})
				},
			},
			{
				States: "the run came back having spent the steps it had spent",
				Holds: func(s survival) error {
					return atEachBoundary(s, func(stage string, at boundary) error {
						if at.recovered.Steps != at.standing.Steps {
							return fmt.Errorf("killed at %s: the run had spent %d steps and came back having spent %d",
								stage, at.standing.Steps, at.recovered.Steps)
						}
						return nil
					})
				},
			},
			{
				States: "the run came back holding for the stage it was killed holding for",
				Holds: func(s survival) error {
					return atEachBoundary(s, func(stage string, boundary boundary) error {
						if boundary.recovered.Decision == nil || boundary.standing.Decision == nil {
							return fmt.Errorf("killed at %s: a decision is missing either side of the kill, "+
								"so there is no stage to compare", stage)
						}
						if got, want := boundary.recovered.Decision.Stage, boundary.standing.Decision.Stage; got != want {
							return fmt.Errorf("the run was killed holding for the %s stage and came back "+
								"holding for %s", want, got)
						}
						return nil
					})
				},
			},
			{
				States: "the recovered decision offers what it offered before the kill",
				Holds: func(s survival) error {
					return atEachBoundary(s, func(stage string, at boundary) error {
						if at.recovered.Decision == nil || at.standing.Decision == nil {
							return fmt.Errorf("killed at %s: a decision is missing either side of the kill, so "+
								"there is nothing to compare", stage)
						}
						if !slices.Equal(at.recovered.Decision.Options, at.standing.Decision.Options) {
							return fmt.Errorf("killed at %s: the decision came back offering %v rather than %v",
								stage, at.recovered.Decision.Options, at.standing.Decision.Options)
						}
						return nil
					})
				},
			},
			{
				States: "the run reached the end of the gate after the kills",
				Holds: func(s survival) error {
					if s.ended.Outcome != machine.OutcomeChecksPassed {
						return fmt.Errorf("the run ended %s rather than reaching the end of the gate", s.ended.Outcome)
					}
					return nil
				},
			},
		},
		Counterfeits: []journey.Counterfeit[survival]{
			{Named: "the service that answered afterwards was the process that had been serving all along",
				Break: func(s survival) survival {
					s = cloneSurvival(s)
					s.boundaries[at(1)].recoveredBy = s.boundaries[at(1)].servedBy
					return s
				}},
			{Named: "no serving process was recorded at one of the boundaries",
				Break: func(s survival) survival {
					s = cloneSurvival(s)
					s.boundaries[at(0)].servedBy = 0
					return s
				}},
			// The node this puts in its place is one this run actually stood
			// at, taken off another boundary of the same walk. A position
			// composed here could name a node no run reaches, and a clause
			// shown failing against a shape the product cannot produce
			// establishes nothing about the clause.
			{Named: "the run came back standing at a different node", Break: func(s survival) survival {
				s = cloneSurvival(s)
				target := at(3)
				for _, other := range s.boundaries {
					if other.standing.Position != s.boundaries[target].standing.Position {
						s.boundaries[target].recovered.Position = other.standing.Position
						break
					}
				}
				return s
			}},
			{Named: "the run came back recorded as still running, which no answer reaches",
				Break: func(s survival) survival {
					s = cloneSurvival(s)
					s.boundaries[at(0)].recovered.Record.Status = store.RunRunning
					return s
				}},
			{Named: "the run came back reported as having failed", Break: func(s survival) survival {
				s = cloneSurvival(s)
				s.boundaries[at(5)].recovered.Outcome = machine.OutcomeFailed
				return s
			}},
			{Named: "the run came back with the decision gone", Break: func(s survival) survival {
				s = cloneSurvival(s)
				s.boundaries[at(2)].recovered.Decision = nil
				return s
			}},
			{Named: "the run came back having lost the steps it had spent", Break: func(s survival) survival {
				s = cloneSurvival(s)
				s.boundaries[at(7)].recovered.Steps = 0
				return s
			}},
			// The stage this names is one this run actually held for, taken off
			// another boundary, for the reason above. Reaching into
			// internal/pipeline's order for a name instead would find the
			// intent stage first, and no run in this build holds for that one.
			{Named: "the run came back holding for a stage other than the one it was killed at",
				Break: func(s survival) survival {
					s = cloneSurvival(s)
					target := at(6)
					stood := s.boundaries[target].standing.Decision
					for _, other := range s.boundaries {
						if stood == nil || other.standing.Decision == nil ||
							other.standing.Decision.Stage == stood.Stage {
							continue
						}
						decision := *s.boundaries[target].recovered.Decision
						decision.Stage = other.standing.Decision.Stage
						s.boundaries[target].recovered.Decision = &decision
						break
					}
					return s
				}},
			// What this drops is what a checkpoint round trip loses when it
			// loses this field: the recovered decision is rebuilt out of the
			// stored one, whose options are a serialized list, so coming back
			// with none of them is a shape that path can produce. An option
			// nobody was offered is not: internal/pipeline gives every hold
			// node the same fixed rendering and internal/graph copies it
			// through, so a clause shown failing against an added option would
			// be shown failing against nothing the mechanism can reach.
			{Named: "the recovered decision came back having lost the options it was offered with",
				Break: func(s survival) survival {
					s = cloneSurvival(s)
					decision := *s.boundaries[at(4)].recovered.Decision
					decision.Options = nil
					s.boundaries[at(4)].recovered.Decision = &decision
					return s
				}},
			{Named: "the service was never killed at one of the boundaries", Break: func(s survival) survival {
				s = cloneSurvival(s)
				s.boundaries = slices.Delete(s.boundaries, at(4), at(4)+1)
				return s
			}},
			{Named: "the run never reached the end of the gate after the kills",
				Break: func(s survival) survival {
					s = cloneSurvival(s)
					s.ended.Outcome = machine.OutcomeFailed
					return s
				}},
		},
	}
	if err := survived.Verify(observed); err != nil {
		t.Fatalf("%v\n\n%s", err, j.ServiceLog())
	}
}

// atEachBoundary answers a question about every boundary of a survival,
// naming the stage the run itself said it stood at, and reports the first
// boundary the question fails at.
//
// The name comes off the observation rather than off internal/pipeline's order
// by position. Which stages a run stops at is the stages this build has no
// body for, so pairing by position would label every boundary with a stage the
// run never stood at the day a body lands.
func atEachBoundary(s survival, ask func(stage string, at boundary) error) error {
	for _, at := range s.boundaries {
		stage := at.standing.Position
		if at.standing.Decision != nil {
			stage = at.standing.Decision.Stage
		}
		if err := ask(stage, at); err != nil {
			return err
		}
	}
	return nil
}

// cloneSurvival copies a survival far enough that a counterfeit can change one
// boundary without reaching the observation it was derived from.
func cloneSurvival(s survival) survival {
	s.boundaries = slices.Clone(s.boundaries)
	return s
}
