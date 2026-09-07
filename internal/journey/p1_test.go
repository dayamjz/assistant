package journey_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/fixture"
	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
)

// consent is what an ordinary push to origin did, either side of the product
// creating a gate for the same working copy.
type consent struct {
	// originBefore and originAfter are every configuration line naming the
	// origin remote, which is what a tool rewiring it would have to change.
	originBefore []string
	originAfter  []string
	// hooksBefore and hooksAfter are the hooks git would run in this working
	// copy, samples excluded.
	hooksBefore []string
	hooksAfter  []string
	// refBefore and refAfter are the branch on origin either side of the push.
	refBefore string
	refAfter  string
	// pushed is the commit the push was asked to land.
	pushed string
	// runs is how many runs the home holds after the push.
	runs int
	// canStartRun is what the doctor says about this home, which is what makes
	// a count of no runs worth reading: a home that could not start one would
	// report none however the push behaved.
	canStartRun bool
}

// TestTheGateDoesNotTouchAnOrdinaryPushToOrigin drives PRD principle P1
// through the binary that ships.
//
// It is section 13's test for P1: after initialization, an ordinary push to
// origin behaves exactly as before, same target, same refs, no run started.
// Every part of it is read off the subject repository with the fixture's own
// git rather than through internal/vcs, because a harness that confirmed a
// push landed by asking the package under validation would be reporting that
// package agreeing with itself.
//
// What it does not establish is the other half of P1, that pushing to the gate
// by name authorizes a run. No push can cross that boundary in this build:
// internal/gate installs a hook invoking "assistant gate admit", and
// internal/cli's verb table carries no such command, so every push to the gate
// is declined by the command surface reporting incorrect usage.
// TestNothingOutsideTheGateChoosesWhatRunsOnAPushToIt drives that.
func TestTheGateDoesNotTouchAnOrdinaryPushToOrigin(t *testing.T) {
	principles.Cite(t, principles.P1)

	scenario := claim(t, fixture.ScenarioEmptyAfterRebase)
	j := open(t, scenario)

	observed := consent{
		originBefore: originConfiguration(t, scenario),
		hooksBefore:  liveHooks(t, scenario.WorkingCopy),
		refBefore:    remoteBranch(t, scenario),
	}
	succeeds(t, j.Command("init", "--default-branch", fixture.DefaultBranch))
	serve(t, j)
	observed.originAfter = originConfiguration(t, scenario)
	observed.hooksAfter = liveHooks(t, scenario.WorkingCopy)

	// A commit of the reader's own, so the push has something to land and the
	// reference on origin has to move for it to have landed at all.
	if err := os.WriteFile(filepath.Join(scenario.WorkingCopy, "notes.md"),
		[]byte("A line somebody added before pushing to their own remote.\n"), 0o600); err != nil {
		t.Fatalf("writing a file to commit: %v", err)
	}
	gitIn(t, scenario, scenario.WorkingCopy, "add", "-A")
	gitIn(t, scenario, scenario.WorkingCopy, "commit", "--quiet", "-m", "add a note")
	observed.pushed = gitIn(t, scenario, scenario.WorkingCopy, "rev-parse", "HEAD")
	gitIn(t, scenario, scenario.WorkingCopy, "push", "origin", scenario.Branch)
	observed.refAfter = remoteBranch(t, scenario)

	var runs machine.Runs
	if err := succeeds(t, j.Command("runs")).Decode(&runs); err != nil {
		t.Fatalf("reading the runs this home holds: %v", err)
	}
	observed.runs = len(runs.Runs)

	var report machine.Doctor
	if err := j.Command("doctor").Decode(&report); err != nil {
		t.Fatalf("reading the doctor's report: %v", err)
	}
	observed.canStartRun = report.CanStartRun

	// A commit this subject really carries and the push did not land: the
	// default branch's tip on the same origin, which the scenario keeps apart
	// from the branch under validation. The counterfeit that says origin ended
	// up somewhere nobody pushed puts this there, because rev-parse answers
	// only with an object it has and a value composed here would show that
	// clause failing against something git cannot report.
	elsewhere := gitIn(t, scenario, scenario.Origin, "rev-parse", "refs/heads/"+fixture.DefaultBranch)

	unchanged := journey.Check[consent]{
		What: "after the product has created a gate for this working copy, an ordinary push to origin " +
			"reaches the same remote, moves the reference it was asked to move, and starts no run",
		Clauses: []journey.Clause[consent]{
			{
				States: "creating the gate left origin's own configuration exactly as it was",
				Holds: func(c consent) error {
					if !slices.Equal(c.originBefore, c.originAfter) {
						return fmt.Errorf("creating the gate changed origin's own configuration:\n  before %v\n  after  %v",
							c.originBefore, c.originAfter)
					}
					return nil
				},
			},
			{
				States: "creating the gate left the hooks git runs in this working copy exactly as they were",
				Holds: func(c consent) error {
					if !slices.Equal(c.hooksBefore, c.hooksAfter) {
						return fmt.Errorf("creating the gate changed the hooks git runs in this working copy:\n  before %v\n  after  %v",
							c.hooksBefore, c.hooksAfter)
					}
					return nil
				},
			},
			{
				States: "the push moved the branch on origin",
				Holds: func(c consent) error {
					if c.refAfter == c.refBefore {
						return fmt.Errorf("the push did not move %s on origin, which still stands at %s",
							scenario.Branch, c.refBefore)
					}
					return nil
				},
			},
			{
				States: "origin stands at the commit the push was asked to land",
				Holds: func(c consent) error {
					if c.refAfter != c.pushed {
						return fmt.Errorf("the push landed %s on origin and the commit pushed was %s",
							c.refAfter, c.pushed)
					}
					return nil
				},
			},
			{
				States:  "the push to origin started no run",
				Absence: true,
				// A home that could not have started a run would report no run
				// whatever the push did, so what makes the count worth reading
				// is that this home is one a run can begin in. The surface is
				// asked rather than assumed, because a gate that failed to
				// register would leave exactly that.
				Possible: func(c consent) error {
					if !c.canStartRun {
						return errors.New("this home reports that it cannot start a run at all, so a " +
							"count of none says nothing about what the push did")
					}
					return nil
				},
				Holds: func(c consent) error {
					if c.runs != 0 {
						return fmt.Errorf("pushing to origin started %d run(s), and nothing but a push to the gate may", c.runs)
					}
					return nil
				},
			},
		},
		Counterfeits: []journey.Counterfeit[consent]{
			{Named: "creating the gate rewrote origin's push address", Break: func(c consent) consent {
				c.originAfter = append(slices.Clone(c.originAfter), "remote.origin.pushurl elsewhere")
				return c
			}},
			{Named: "creating the gate dropped one of origin's own settings", Break: func(c consent) consent {
				c.originAfter = slices.Clone(c.originAfter)[1:]
				return c
			}},
			{Named: "creating the gate installed a hook in the working copy", Break: func(c consent) consent {
				c.hooksAfter = append(slices.Clone(c.hooksAfter), "pre-push")
				return c
			}},
			{Named: "the push did not reach origin at all", Break: func(c consent) consent {
				c.refAfter = c.refBefore
				return c
			}},
			{Named: "origin ended up at a commit nobody pushed", Break: func(c consent) consent {
				c.refAfter = elsewhere
				return c
			}},
			{Named: "the push to origin started a run", Break: func(c consent) consent {
				c.runs = 1
				return c
			}},
		},
	}
	if err := unchanged.Verify(observed); err != nil {
		t.Fatalf("%v", err)
	}
}

// originConfiguration is every configuration line naming the origin remote, in
// a stable order.
func originConfiguration(t *testing.T, scenario fixture.Scenario) []string {
	t.Helper()
	out, err := journey.Git(scenario, scenario.WorkingCopy, "config", "--local", "--get-regexp", `^remote\.origin\.`)
	if err != nil {
		t.Fatalf("reading origin's configuration: %v", err)
	}
	lines := strings.Split(out, "\n")
	slices.Sort(lines)
	return lines
}

// liveHooks is the hooks git would run in a working copy, with the samples git
// itself installs left out.
func liveHooks(t *testing.T, workingCopy string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(workingCopy, ".git", "hooks"))
	if err != nil {
		t.Fatalf("reading the hooks of %s: %v", workingCopy, err)
	}
	var live []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".sample") {
			continue
		}
		live = append(live, entry.Name())
	}
	slices.Sort(live)
	return live
}

// remoteBranch is where the branch under validation stands on the scenario's
// origin, read out of the bare repository rather than through the working copy.
func remoteBranch(t *testing.T, scenario fixture.Scenario) string {
	t.Helper()
	out, err := journey.Git(scenario, scenario.Origin, "rev-parse", "refs/heads/"+scenario.Branch)
	if err != nil {
		t.Fatalf("reading %s on origin: %v", scenario.Branch, err)
	}
	return out
}

// gitIn runs a git command in the subject and fails the test when it does not
// succeed.
func gitIn(t *testing.T, scenario fixture.Scenario, dir string, args ...string) string {
	t.Helper()
	out, err := journey.Git(scenario, dir, args...)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return out
}
