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
	// perHold is how many node executions carrying the run from one hold to
	// the next costs, measured on this same run by answering one hold with
	// nobody else driving it.
	//
	// It is a per-hold cost rather than a per-stage one, because
	// internal/pipeline gives a stage that holds a hold node as well as a
	// stage node and sends one that does not straight on. The two are the same
	// number only while the stages a run holds at run consecutively, which is
	// checked before this is measured rather than assumed.
	perHold int
	// before and after are where the run stood either side of the callers, and
	// holdsAdvanced is how many holds it moved through, counted in the same
	// unit perHold was measured in.
	before        machine.Run
	after         machine.Run
	holdsAdvanced int
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
// Neither is asserted against a constant. What carrying the run from one hold
// to the next costs is measured on this same run first, by answering one hold
// with nobody else driving, and the advance is then counted in that same unit:
// holds reached, not positions in the stage order. Counting positions would
// make the arithmetic depend on which stages have bodies, because a stage that
// holds costs a node more than one that does not. Several callers may
// legitimately carry a run through more than one hold between them, because a
// run that reaches its next hold is one the next caller may answer, so what is
// checked is that the total spent is exactly what the holds it moved through
// cost.
func TestSeveralCallersDrivingOneRunExecuteNoNodeTwice(t *testing.T) {
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P6)

	holding := stagesWithoutABody(t)
	// One hold-to-hold transition costs what the next one does only while the
	// stages a run holds at run consecutively: internal/pipeline gives a stage
	// that holds a hold node as well as a stage node, and a stage that does
	// not hold goes straight on, so a non-holding stage sitting between two
	// holds adds a node to that transition and to no other. Refusing here is
	// what keeps the day a middle stage gets a body from arriving as a
	// contention failure rather than as the measurement no longer applying.
	consecutiveHolds(t, holding)

	j := inClone(t)
	started := startRun(t, j, "--intent", "a change several callers answer at once")
	observed := contended{}

	// One answer with nobody else driving, which is the measurement everything
	// below is compared against.
	measured := decodeRun(t, succeeds(t, j.Command("--answer", "approved")))
	observed.perHold = measured.Steps - started.Steps
	observed.before = measured

	// As many callers as the run has holds left to be carried through while
	// still holding at the end of them, which is what the reads below need:
	// one hold is spent on the measurement above, and one has to survive so
	// the run this reads back is the same run, still waiting. A count written
	// here instead would be safe only for as long as this build has the number
	// of stage bodies it has today, and would then fail as a contention
	// failure rather than as the stale number it was.
	callers := len(holding) - 2
	if callers < 2 {
		t.Fatalf("this build leaves %d hold(s) after the measurement, so there is no room for several "+
			"callers to answer one run; this check needs rewriting against whatever holds a run now",
			len(holding)-1)
	}
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
	observed.holdsAdvanced = holdPosition(t, holding, observed.after) - holdPosition(t, holding, observed.before)
	observed.completed = last(answerHolds(t, j, observed.after, "approved")).Outcome == machine.OutcomeChecksPassed

	sound := journey.Check[contended]{
		What: "P6: one run several callers answer at once",
		Clauses: []journey.Clause[contended]{
			{
				States: "one hold was measured at a cost there is something to compare against",
				Holds: func(c contended) error {
					if c.perHold <= 0 {
						return fmt.Errorf("carrying the run from one hold to the next was measured at %d node "+
							"executions, so there is nothing to compare the contended run against", c.perHold)
					}
					return nil
				},
			},
			{
				States: "the run moved forward through at least one hold",
				Holds: func(c contended) error {
					if c.holdsAdvanced < 1 {
						return fmt.Errorf("%d callers answered and the run moved through %d hold(s)",
							len(c.answers), c.holdsAdvanced)
					}
					return nil
				},
			},
			{
				States: "the run spent exactly what the holds it moved through cost",
				Holds: func(c contended) error {
					spent := c.after.Steps - c.before.Steps
					if want := c.holdsAdvanced * c.perHold; spent != want {
						return fmt.Errorf("the run moved through %d hold(s) and spent %d node executions, and "+
							"one hold costs %d, so %d were expected; a node body ran more than once or a "+
							"segment wrote over another's history", c.holdsAdvanced, spent, c.perHold, want)
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
				c.after.Steps += c.perHold
				return c
			}},
			{Named: "a segment's node executions went unrecorded", Break: func(c contended) contended {
				c.after.Steps -= c.perHold
				return c
			}},
			{Named: "one hold was measured at nothing, so there is no cost to compare against",
				Break: func(c contended) contended {
					c.perHold = 0
					return c
				}},
			{Named: "the run went backwards", Break: func(c contended) contended {
				c.holdsAdvanced = 0
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

// holdPosition is how far through the gate a run stands, counted in the holds
// it reaches rather than in positions in the stage order, with a run that has
// finished standing past the last of them.
//
// The unit is what makes it right: the cost this test compares against is
// measured from one hold to the next, so an advance measured in stage
// positions would count a stage the run passed straight through as though it
// had cost a hold.
func holdPosition(t *testing.T, holding []pipeline.Stage, run machine.Run) int {
	t.Helper()
	if run.Decision == nil {
		return len(holding)
	}
	at := slices.IndexFunc(holding, func(stage pipeline.Stage) bool {
		return stage.String() == run.Decision.Stage
	})
	if at < 0 {
		t.Fatalf("the run is holding at %s, which is not one of the stages this build has no body for "+
			"(%v), so a stage with a body is holding too and what one hold costs is no longer one "+
			"number; this check needs rewriting against whatever holds a run now",
			run.Decision.Stage, holding)
	}
	return at
}

// consecutiveHolds refuses unless the stages this build holds at run
// consecutively in internal/pipeline's order.
//
// That is the property the per-hold cost rests on. internal/pipeline gives a
// stage that holds both a stage node and a hold node and sends a stage that
// does not hold straight to the next one, so a non-holding stage between two
// holds makes that one transition cost a node more than its neighbours and
// there is no single per-hold cost to measure. Refusing here says that
// plainly, rather than letting the arithmetic below report it as a node body
// having run twice.
func consecutiveHolds(t *testing.T, holding []pipeline.Stage) {
	t.Helper()
	order := pipeline.Order()
	first := slices.Index(order, holding[0])
	for i, stage := range holding {
		if order[first+i] != stage {
			t.Fatalf("this build holds at %v, which are not consecutive in %v, so one hold does not cost "+
				"what the next one does and there is no per-hold cost to measure; this check needs "+
				"rewriting against whatever holds a run now", holding, order)
		}
	}
}
