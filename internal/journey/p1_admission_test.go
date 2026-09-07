package journey_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/fixture"
	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
)

// consented is what a push to the gate by name did: whether it was admitted,
// and what run it authorized.
type consented struct {
	// pushed is the commit the push was asked to land.
	pushed string
	// branch is the branch it was pushed on.
	branch string
	// accepted is whether the gate admitted the push, and message is what git
	// reported about it either way.
	accepted bool
	message  string
	// before and after are the runs this home held either side of the push.
	before []machine.Run
	after  []machine.Run
	// startedBranch and startedHead are the branch and the submitted commit of
	// the run the push authorized, empty when it authorized none.
	startedBranch string
	startedHead   string
}

// TestAPushToTheGateByNameAuthorizesTheRun drives the half of PRD principle P1
// that says what a push to the gate does, rather than what it must not do to
// origin.
//
// It is new coverage of the product's central promise. The gate's admission
// hook invokes a subcommand internal/cli did not carry until recently, so every
// push to a gate was declined by the command surface reporting incorrect usage
// and this half of P1 was not drivable at all. It is now, and a harness that
// only drove the negative half would be reporting on the door it could not open.
//
// The consent is asserted as a binding, not just as an event. P1 says pushing to
// the gate by name authorizes THAT run to validate THAT change, so the check
// holds the run that appeared to the branch and the commit that were pushed; a
// push that started some other run would satisfy "a run started" and none of
// what the principle actually says.
//
// The service is up on purpose. With it down the same push is declined for a
// reason that has nothing to do with admission, which is what
// TestNothingOutsideTheGateChoosesWhatRunsOnAPushToIt was observing without
// meaning to, and a check that accepted that as evidence would be reading a
// service that never answered as a gate that refused.
func TestAPushToTheGateByNameAuthorizesTheRun(t *testing.T) {
	principles.Cite(t, principles.P1)
	requiresIdentifiedPeer(t)

	j := inClone(t)
	scenario := j.Scenario()
	dir := j.Dir()

	observed := consented{branch: scenario.Branch, before: runsHere(t, j)}
	observed.pushed = gitIn(t, scenario, dir, "rev-parse", "HEAD")
	observed.accepted, observed.message = pushToTheGate(t, scenario, nil, dir)
	observed.after = runsHere(t, j)
	for _, run := range observed.after {
		if !heldBefore(observed.before, run.Record.ID) {
			observed.startedBranch, observed.startedHead = run.Record.Branch, run.Record.SubmittedHead
		}
	}

	authorizes := journey.Check[consented]{
		What: "a push to the gate by name is admitted and authorizes a run of the branch and the commit " +
			"that were pushed",
		Clauses: []journey.Clause[consented]{
			{
				States: "the gate admitted the push",
				Holds: func(c consented) error {
					if !c.accepted {
						return fmt.Errorf("the push to the gate was declined; git said:\n%s", c.message)
					}
					return nil
				},
			},
			{
				States: "exactly one run appeared that was not there before the push",
				Holds: func(c consented) error {
					if got := len(c.after) - len(c.before); got != 1 {
						return fmt.Errorf("this home held %d run(s) before the push and %d after, a "+
							"difference of %d", len(c.before), len(c.after), got)
					}
					return nil
				},
			},
			{
				States: "the run it authorized is of the branch that was pushed",
				Holds: func(c consented) error {
					if c.startedBranch != c.branch {
						return fmt.Errorf("the push was on %s and the run it started is of %q",
							c.branch, c.startedBranch)
					}
					return nil
				},
			},
			{
				States: "the run it authorized validates the commit that was pushed, which is what binds " +
					"the consent to the change",
				Holds: func(c consented) error {
					if c.startedHead != c.pushed {
						return fmt.Errorf("the push landed %s and the run it started validates %q",
							c.pushed, c.startedHead)
					}
					return nil
				},
			},
		},
		Counterfeits: []journey.Counterfeit[consented]{
			{Named: "the gate declined the push", Break: func(c consented) consented {
				c.accepted = false
				c.message = "! [remote rejected] (pre-receive hook declined)"
				return c
			}},
			{Named: "the push was admitted and authorized no run", Break: func(c consented) consented {
				c.after = c.before
				c.startedBranch, c.startedHead = "", ""
				return c
			}},
			{Named: "the run it authorized is of another branch", Break: func(c consented) consented {
				c.startedBranch = fixture.DefaultBranch
				return c
			}},
			// The commit before the one that was pushed: a real commit of this
			// subject, so the counterfeit names a head a run really could
			// carry rather than a value nothing produces.
			{Named: "the run it authorized validates a commit the push did not land",
				Break: func(c consented) consented {
					c.startedHead = gitIn(t, scenario, dir, "rev-parse", "HEAD~1")
					return c
				}},
		},
	}
	if err := authorizes.Verify(observed); err != nil {
		t.Fatalf("%v\n\n%s", err, j.ServiceLog())
	}
	t.Logf("the gate admitted the push and authorized run of %s at %s; git said:\n%s",
		observed.startedBranch, observed.startedHead, strings.TrimSpace(observed.message))
}

// runsHere is the runs this home holds, read off the surface.
func runsHere(t *testing.T, j *journey.Journey) []machine.Run {
	t.Helper()
	var listed machine.Runs
	if err := succeeds(t, j.Command("runs")).Decode(&listed); err != nil {
		t.Fatalf("reading the runs this home holds: %v", err)
	}
	out := make([]machine.Run, 0, len(listed.Runs))
	for _, record := range listed.Runs {
		out = append(out, machine.Run{Record: record})
	}
	return out
}

// heldBefore reports whether a run was already here before the push.
func heldBefore(before []machine.Run, id string) bool {
	for _, run := range before {
		if run.Record.ID == id {
			return true
		}
	}
	return false
}
