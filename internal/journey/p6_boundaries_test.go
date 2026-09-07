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
// classification is correct. A stage boundary in this build is a hold, because
// internal/graph stops before a halt point runs, so that is where the kill
// lands.
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
	principles.Cite(t, principles.P6)

	j := inClone(t)

	observed := survival{}
	current := startRun(t, j, "--intent", "narrow the Total loop bound on purpose")
	// Bounded for the reason answerHolds is bounded: a run that stops
	// advancing past a hold turns this into an unbounded kill-and-serve loop,
	// and the suite hanging to the test timeout says nothing about which stage
	// the run stopped at. The bound is a multiple of the stage count because
	// the check below compares the boundaries reached against exactly that.
	for range 2 * len(stageOrder()) {
		if current.Outcome != machine.OutcomeDecision {
			break
		}
		served := j.ServicePID()
		if err := j.Kill(); err != nil {
			t.Fatalf("killing the service while the run stood at %s: %v", current.Position, err)
		}
		if err := j.Serve(); err != nil {
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
		t.Fatalf("the run was killed and answered at %d boundaries and is still holding at %s; the gate "+
			"has %d stages", len(observed.boundaries), current.Position, len(stageOrder()))
	}
	observed.ended = current

	survived := journey.Check[survival]{
		What: "a run killed at every stage boundary comes back standing exactly where it stood, " +
			"classified as waiting on the same decision, and is driven to the end of the gate afterwards",
		Clauses: []journey.Clause[survival]{
			{
				States: "the service was killed at every one of the gate's stage boundaries",
				Holds: func(s survival) error {
					if len(s.boundaries) != len(stageOrder()) {
						return fmt.Errorf("the service was killed at %d boundaries and the gate has %d stages",
							len(s.boundaries), len(stageOrder()))
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
				States: "the recovered decision is the one the boundary's own stage holds for",
				Holds: func(s survival) error {
					return atEachBoundary(s, func(stage string, at boundary) error {
						if at.recovered.Decision == nil {
							return fmt.Errorf("killed at %s: the run came back with no decision to read a stage off", stage)
						}
						if got := at.recovered.Decision.Stage; got != stage {
							return fmt.Errorf("the %s boundary came back holding for the %s stage", stage, got)
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
					s.boundaries[1].recoveredBy = s.boundaries[1].servedBy
					return s
				}},
			{Named: "no serving process was recorded at one of the boundaries",
				Break: func(s survival) survival {
					s = cloneSurvival(s)
					s.boundaries[0].servedBy = 0
					return s
				}},
			{Named: "the run came back standing at a different node", Break: func(s survival) survival {
				s = cloneSurvival(s)
				s.boundaries[3].recovered.Position = "hold:intent"
				return s
			}},
			{Named: "the run came back recorded as still running, which no answer reaches",
				Break: func(s survival) survival {
					s = cloneSurvival(s)
					s.boundaries[0].recovered.Record.Status = store.RunRunning
					return s
				}},
			{Named: "the run came back reported as having failed", Break: func(s survival) survival {
				s = cloneSurvival(s)
				s.boundaries[5].recovered.Outcome = machine.OutcomeFailed
				return s
			}},
			{Named: "the run came back with the decision gone", Break: func(s survival) survival {
				s = cloneSurvival(s)
				s.boundaries[2].recovered.Decision = nil
				return s
			}},
			{Named: "the run came back having lost the steps it had spent", Break: func(s survival) survival {
				s = cloneSurvival(s)
				s.boundaries[7].recovered.Steps = 0
				return s
			}},
			{Named: "the run came back holding for a stage other than the one it was killed at",
				Break: func(s survival) survival {
					s = cloneSurvival(s)
					decision := *s.boundaries[6].recovered.Decision
					decision.Stage = stageOrder()[0]
					s.boundaries[6].recovered.Decision = &decision
					return s
				}},
			{Named: "the recovered decision offers something nobody was offered before the kill",
				Break: func(s survival) survival {
					s = cloneSurvival(s)
					decision := *s.boundaries[4].recovered.Decision
					decision.Options = append(slices.Clone(decision.Options), "publish")
					s.boundaries[4].recovered.Decision = &decision
					return s
				}},
			{Named: "the service was never killed at one of the boundaries", Break: func(s survival) survival {
				s = cloneSurvival(s)
				s.boundaries = slices.Delete(s.boundaries, 4, 5)
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
// naming the stage the boundary belongs to, and reports the first boundary the
// question fails at.
//
// It pairs each boundary with a stage by position, so it says nothing when
// there are more boundaries than stages; the clause that counts them is what
// holds that, and this is deliberately not a second owner of it.
func atEachBoundary(s survival, ask func(stage string, at boundary) error) error {
	order := stageOrder()
	for i, at := range s.boundaries {
		if i >= len(order) {
			return nil
		}
		if err := ask(order[i], at); err != nil {
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
