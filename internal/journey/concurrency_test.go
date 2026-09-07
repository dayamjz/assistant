package journey_test

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
)

// contended is what became of one run several callers answered at once.
type contended struct {
	// perStage is how many node executions one stage costs, measured on this
	// same run by answering it once with nobody else driving it.
	perStage int
	// before and after are where the run stood either side of the callers, and
	// stagesAdvanced is how many stages it moved through.
	before         machine.Run
	after          machine.Run
	stagesAdvanced int
	// answers is what each caller exited with, so a caller that crashed or
	// wedged is visible rather than absorbed.
	answers []machine.Code
	// completed is whether the run was driven to the end of the gate
	// afterwards, which is what says its history is still usable.
	completed bool
}

// TestSeveralCallersDrivingOneRunExecuteNoNodeTwice drives the concurrency
// half of PRD principle P6 through the binary that ships.
//
// Two callers driving one run is the case a run's history is corrupted by, and
// the two failures it produces are different. A node body executed twice does
// work twice, which for a real stage means an agent invoked twice and a
// reference moved twice. A history written by two segments from one position
// loses whatever the loser wrote or leaves the run standing somewhere neither
// caller believes it is.
//
// Neither is asserted against a constant. What one stage costs is measured on
// this same run first, by answering it once with nobody else driving, so the
// arithmetic holds as the pipeline's own shape changes. Several callers may
// legitimately carry a run through more than one stage between them, because a
// run that reaches its next hold is one the next caller may answer, so what is
// checked is that the total spent is exactly what the stages it moved through
// cost.
func TestSeveralCallersDrivingOneRunExecuteNoNodeTwice(t *testing.T) {
	principles.Cite(t, principles.P6)

	j := inClone(t)
	started := startRun(t, j, "--intent", "a change several callers answer at once")
	observed := contended{}

	// One answer with nobody else driving, which is the measurement everything
	// below is compared against.
	measured := decodeRun(t, succeeds(t, j.Command("--answer", "approved")))
	observed.perStage = measured.Steps - started.Steps
	observed.before = measured

	const callers = 5
	var wait sync.WaitGroup
	codes := make([]machine.Code, callers)
	for i := range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			codes[i] = j.Command("--answer", "approved").Code
		}()
	}
	wait.Wait()
	observed.answers = codes

	observed.after = startRun(t, j)
	observed.stagesAdvanced = stagePosition(observed.after) - stagePosition(observed.before)
	observed.completed = last(answerHolds(t, j, observed.after, "approved")).Outcome == machine.OutcomeChecksPassed

	sound := journey.Check[contended]{
		What: "a run several callers answered at once spent exactly what the stages it moved through " +
			"cost, moved forward rather than sideways, and was still drivable to the end of the gate " +
			"afterwards",
		Clauses: []journey.Clause[contended]{
			{
				States: "one stage was measured at a cost there is something to compare against",
				Holds: func(c contended) error {
					if c.perStage <= 0 {
						return fmt.Errorf("one stage was measured at %d node executions, so there is nothing to "+
							"compare the contended run against", c.perStage)
					}
					return nil
				},
			},
			{
				States: "the run moved forward through at least one stage",
				Holds: func(c contended) error {
					if c.stagesAdvanced < 1 {
						return fmt.Errorf("%d callers answered and the run moved through %d stages",
							len(c.answers), c.stagesAdvanced)
					}
					return nil
				},
			},
			{
				States: "the run spent exactly what the stages it moved through cost",
				Holds: func(c contended) error {
					spent := c.after.Steps - c.before.Steps
					if want := c.stagesAdvanced * c.perStage; spent != want {
						return fmt.Errorf("the run moved through %d stage(s) and spent %d node executions, and "+
							"one stage costs %d, so %d were expected; a node body ran more than once or a "+
							"segment wrote over another's history", c.stagesAdvanced, spent, c.perStage, want)
					}
					return nil
				},
			},
			{
				States: "every caller was answered or refused rather than crashing",
				Holds: func(c contended) error {
					for i, code := range c.answers {
						if code != machine.ExitOK && code != machine.ExitFailure {
							return fmt.Errorf("caller %d exited %d, which is neither an answer nor a refusal", i, code)
						}
					}
					return nil
				},
			},
			{
				States: "at least one caller advanced the run",
				Holds: func(c contended) error {
					if !slices.Contains(c.answers, machine.ExitOK) {
						return fmt.Errorf("no caller advanced the run, so nothing here shows what happens when "+
							"one does: they exited %v", c.answers)
					}
					return nil
				},
			},
			{
				States: "the run was still drivable to the end of the gate afterwards",
				Holds: func(c contended) error {
					if !c.completed {
						return errors.New("the run could not be driven to the end afterwards, so what the callers " +
							"left behind is not a history the run can be resumed from")
					}
					return nil
				},
			},
		},
		Counterfeits: []journey.Counterfeit[contended]{
			{Named: "a node body executed twice", Break: func(c contended) contended {
				c.after.Steps += c.perStage
				return c
			}},
			{Named: "a segment's node executions went unrecorded", Break: func(c contended) contended {
				c.after.Steps -= c.perStage
				return c
			}},
			{Named: "one stage was measured at nothing, so there is no cost to compare against",
				Break: func(c contended) contended {
					c.perStage = 0
					return c
				}},
			{Named: "the run went backwards", Break: func(c contended) contended {
				c.stagesAdvanced = 0
				return c
			}},
			{Named: "every caller was refused, so nothing shows what one that is not does",
				Break: func(c contended) contended {
					c.answers = slices.Clone(c.answers)
					for i := range c.answers {
						c.answers[i] = machine.ExitFailure
					}
					return c
				}},
			{Named: "a caller was killed by a signal rather than being answered or refused",
				Break: func(c contended) contended {
					c.answers = slices.Clone(c.answers)
					c.answers[0] = signalled
					return c
				}},
			{Named: "the run could not be driven to the end afterwards", Break: func(c contended) contended {
				c.completed = false
				return c
			}},
		},
	}
	if err := sound.Verify(observed); err != nil {
		t.Fatalf("%v\n\n%s", err, j.ServiceLog())
	}
}

// signalled is the status this harness records for a caller the operating
// system killed, which is what exec reports through ProcessState.ExitCode for
// a process that ended on a signal rather than by exiting.
//
// It is deliberately not a number from internal/machine's own vocabulary. Two
// is ExitUsage, which the surface produces on purpose, so a counterfeit
// carrying it would be showing that this clause rejects a wrong command line
// while telling a reader it rejects a caller that died.
const signalled = machine.Code(-1)

// stagePosition is how far through the gate a run stands, counted in stages,
// with a run that has finished standing past the last of them.
func stagePosition(run machine.Run) int {
	if run.Decision == nil {
		return len(pipeline.Order())
	}
	return slices.Index(stageOrder(), run.Decision.Stage)
}
