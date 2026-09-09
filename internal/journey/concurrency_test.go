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
//
// The contention stays inside the holds that sit consecutively from the first
// measured one, because the review and pull request stages have bodies and a
// stage with one between two holds prices that transition differently. The
// run asks to skip both, for the reasons walkableRun states. The review
// crossing is spent as setup before anything is measured, and the run is
// driven to the end across the pull request crossing afterwards, where no
// arithmetic rests on what the crossing costs.
func TestSeveralCallersDrivingOneRunExecuteNoNodeTwice(t *testing.T) {
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P6)

	// One hold-to-hold transition costs what the next one does only while the
	// stages a run holds at run consecutively: internal/pipeline gives a stage
	// that holds a hold node as well as a stage node, and a stage that does
	// not hold goes straight on, so a non-holding stage sitting between two
	// holds adds a node to that transition and to no other. This run has two
	// such stages: it skips review and the pull request stage for the reasons
	// walkableRun states, and a skipped stage's node still executes to record
	// the skip, so the transition that crosses one costs more than its
	// neighbours. The review crossing sits on the first transition, so the
	// first answer below is spent as setup rather than measured; the pull
	// request crossing sits before the last hold, so the contention stays
	// inside the consecutive span before it. Bounding the measurement that
	// way is what keeps the day another middle stage gets a body from
	// arriving as a contention failure rather than as the measurement no
	// longer applying.
	holding := contendedHolds(t, stagesARunStopsAt(t)[1:])

	j := inClone(t)
	walkableRun(t, j, "a change several callers answer at once")

	// The setup answer: it carries the run across the skipped review stage,
	// whose extra node would otherwise be measured into the per-hold cost.
	started := decodeRun(t, succeeds(t, j.Command("--answer", "approved")))
	observed := contended{}

	// One answer with nobody else driving, which is the measurement everything
	// below is compared against.
	measured := decodeRun(t, succeeds(t, j.Command("--answer", "approved")))
	observed.perHold = measured.Steps - started.Steps
	observed.before = measured

	// As many callers as the run has holds left to be carried through while
	// still holding at the end of them, which is what the reads below need:
	// the setup hold sits outside the contended span, one of the span's holds
	// is spent on the measurement above, and one has to survive so the run
	// this reads back is the same run, still waiting. A count written here
	// instead would be safe only for as long as this build has the number of
	// stage bodies it has today, and would then fail as a contention failure
	// rather than as the stale number it was.
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
		t.Fatalf("the run is holding at %s, which is not one of the consecutive holds this check "+
			"contends over (%v), so what one hold costs is no longer one number; this check needs "+
			"rewriting against whatever holds a run now",
			run.Decision.Stage, holding)
	}
	return at
}

// contendedHolds returns the holds this test may contend over: the longest
// prefix of the span it is given that sits consecutively in
// internal/pipeline's order. The caller passes the holds from its first
// measured transition onward; a hold the walk spends as setup before
// measuring is outside the span.
//
// Consecutive is the property the per-hold cost rests on. internal/pipeline
// gives a stage that holds both a stage node and a hold node and sends a
// stage that does not hold straight to the next one, so a stage with a body
// sitting between two holds makes that one transition cost a node more than
// its neighbours, and there is no single per-hold cost across it. The pull
// request stage is that stage today, so the hold after it is left out of the
// contention rather than priced wrong. Refusing a prefix too short for a
// measurement, several callers and a hold that survives them says plainly
// that this check needs rewriting against whatever holds a run now, rather
// than letting the arithmetic report a node body having run twice.
func contendedHolds(t *testing.T, holding []pipeline.Stage) []pipeline.Stage {
	t.Helper()
	order := pipeline.Order()
	first := slices.Index(order, holding[0])
	prefix := holding[:1]
	for i := 1; i < len(holding); i++ {
		if first+i >= len(order) || order[first+i] != holding[i] {
			break
		}
		prefix = holding[:i+1]
	}
	if len(prefix) < 4 {
		t.Fatalf("this build holds consecutively at %v out of %v, which leaves no room for a measurement, "+
			"several callers and a hold that survives them; this check needs rewriting against whatever "+
			"holds a run now", prefix, holding)
	}
	return prefix
}
