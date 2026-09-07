package journey_test

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/fixture"
	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
)

// birth is what one attempt to create or repair a gate answered, and what the
// planted hooks did about it.
type birth struct {
	// what names the attempt, for a failure that says which one.
	what string
	// refused is whether the attempt was refused.
	refused bool
	// message is what it was refused with, or what it reported on succeeding.
	message string
	// fired is every tripwire the scenario had recorded by the end of the
	// attempt.
	fired []string
}

// TestNothingOutsideTheGateChoosesWhatRunsOnAPushToIt drives the trust anchor
// half of PRD principle P7 through the binary that ships.
//
// P7 is about the branch under validation not choosing what runs, and this is
// the channel one step earlier: what a gate repository is born carrying. A
// template puts hooks into a repository at the moment it is created, and
// internal/vcs deliberately keeps GIT_CONFIG_GLOBAL, so a configuration file
// reached that way still selects one. A gate that accepted them would be
// running somebody else's code on every push, in front of the admission it
// believes it has.
//
// Four conditions are driven and only two are refusals. The other two are what
// give the refusals their meaning: one hook name the template channel can no
// longer reach, and one channel closed before git ever sees it. A refusal
// nobody has watched not fire is a refusal nobody has shown is about anything.
//
// The last subtest is the other end of the same question and is not a refusal
// at all. internal/gate names core.hooksPath as an open gap, and this drives a
// push under a configuration file that redirects it, so what the gap costs is
// read off which hook ran rather than out of a document.
//
// Every attempt gets a working copy and a home of its own, so the sequence one
// condition needs cannot decide what another one meets. The redirect subtest
// runs last because it is the one that makes a planted hook fire, and the
// conditions before it are checked on the tripwire file staying empty.
func TestNothingOutsideTheGateChoosesWhatRunsOnAPushToIt(t *testing.T) {
	principles.Cite(t, principles.P7)

	scenario := claim(t, fixture.ScenarioHostileTemplate)
	mixed := map[string]string{"GIT_CONFIG_GLOBAL": scenarioPath(t, scenario, "gitconfig-template-mixed")}
	preReceiveOnly := map[string]string{
		"GIT_CONFIG_GLOBAL": scenarioPath(t, scenario, "gitconfig-template-pre-receive-only"),
	}

	t.Run("closed-git-template-dir-environment", func(t *testing.T) {
		j, dir := gateJourney(t, scenario)
		refuses(t, scenario, "closed-git-template-dir-environment", false,
			attempt(t, scenario, j, dir,
				map[string]string{"GIT_TEMPLATE_DIR": scenarioPath(t, scenario, "template-mixed")},
				"initializing a gate with GIT_TEMPLATE_DIR naming the hostile template"))
	})

	t.Run("refusal-template-hooks-at-birth", func(t *testing.T) {
		j, dir := gateJourney(t, scenario)
		refuses(t, scenario, "refusal-template-hooks-at-birth", true,
			attempt(t, scenario, j, dir, mixed, "creating a gate under init.templateDir"))
	})

	t.Run("on an existing gate", func(t *testing.T) {
		j, dir := gateJourney(t, scenario)
		succeeds(t, j.CommandIn(dir, "init", "--default-branch", fixture.DefaultBranch))
		refuses(t, scenario, "refusal-template-hooks-on-repair", true,
			attempt(t, scenario, j, dir, mixed, "repairing an existing gate under init.templateDir"))
		refuses(t, scenario, "closed-template-pre-receive-on-repair", false,
			attempt(t, scenario, j, dir, preReceiveOnly,
				"repairing an existing gate under a template carrying only pre-receive"))
	})

	t.Run("gap-core-hookspath-redirects-the-gate", func(t *testing.T) {
		condition, err := journey.Condition("gap-core-hookspath-redirects-the-gate")
		if err != nil {
			t.Fatalf("%v", err)
		}
		if condition.Expect.Gap == "" {
			t.Fatalf("this condition no longer records a gap, so what it must produce has changed")
		}
		redirect := map[string]string{
			"GIT_CONFIG_GLOBAL": scenarioPath(t, scenario, "gitconfig-"+hostileHooks),
		}

		// The contrast first, because it is what gives the redirect a meaning:
		// the same push, through the same product, with nothing redirected.
		plain, plainDir := gateJourney(t, scenario)
		succeeds(t, plain.CommandIn(plainDir, "init", "--default-branch", fixture.DefaultBranch))
		observed := admission{}
		observed.ordinaryPush, observed.ordinaryMessage = pushToTheGate(t, scenario, nil, plainDir)

		under, underDir := gateJourney(t, scenario)
		observed.initRefused = under.CommandWith(underDir, redirect,
			"init", "--default-branch", fixture.DefaultBranch).Code != machine.ExitOK
		observed.redirectedPush, observed.redirectedMessage = pushToTheGate(t, scenario, redirect, underDir)
		if observed.fired, err = journey.Fired(scenario); err != nil {
			t.Fatalf("reading the scenario's tripwires: %v", err)
		}

		reaches := journey.Check[admission]{
			What: "a push through the gate under a configuration file that redirects core.hooksPath is " +
				"observed rather than assumed: either the product refuses the redirect, or it accepts " +
				"the push and the hooks that ran are the redirected ones rather than the gate's own",
			Holds: func(a admission) error {
				if a.ordinaryPush {
					return fmt.Errorf("a push through the gate with nothing redirected was accepted, and " +
						"no run started from it; the gate would then be admitting pushes with nothing " +
						"checking them")
				}
				if a.initRefused {
					// The gap is closed. Nothing about the redirect can then be
					// observed on a push, because there is no gate to push to.
					return nil
				}
				if !a.redirectedPush {
					return fmt.Errorf("the redirected push was declined and the initialization was not "+
						"refused, so this observed neither the gap nor its closure; the push said:\n%s",
						a.redirectedMessage)
				}
				redirected := condition.Expect.TripwiresQuiet
				if len(redirected) == 0 {
					redirected = []string{"hookspath-hook-pre-receive"}
				}
				for _, tripwire := range redirected {
					if !slices.Contains(a.fired, tripwire) {
						return fmt.Errorf("the push was accepted and %s did not run, so which hook git ran "+
							"was not observed at all; the scenario recorded %v", tripwire, a.fired)
					}
				}
				return nil
			},
			Counterfeits: []journey.Counterfeit[admission]{
				{Named: "the gate accepted an ordinary push with nothing checking it",
					Break: func(a admission) admission {
						a.ordinaryPush = true
						return a
					}},
				{Named: "the redirected push was accepted and no redirected hook ran, so nothing was observed",
					Break: func(a admission) admission {
						a.fired = nil
						return a
					}},
				{Named: "neither the initialization nor the push told us anything",
					Break: func(a admission) admission {
						a.initRefused = false
						a.redirectedPush = false
						return a
					}},
			},
		}
		if err := reaches.Verify(observed); err != nil {
			t.Fatalf("%v", err)
		}
		if !observed.initRefused {
			t.Logf("KNOWN GAP %s: initialization reported success under a configuration file "+
				"redirecting core.hooksPath, the push was accepted, and the hooks git ran were %v rather "+
				"than the gate's own. %s", condition.ID, observed.fired, condition.Expect.Gap)
		}
		t.Logf("A push through the gate with nothing redirected was declined. What declined it is not an "+
			"admission decision: internal/gate installs a hook invoking \"assistant gate admit\", "+
			"internal/cli carries no such verb, and the push is declined by the command surface reporting "+
			"incorrect usage. It said:\n%s", observed.ordinaryMessage)
	})
}

// hostileHooks is the stem internal/fixture files the core.hooksPath plant
// under, in Scenario.Paths and in the name of the configuration file that
// redirects to it.
const hostileHooks = "hostile-hooks"

// admission is what a push through the gate did, with and without a
// configuration file redirecting the gate's own hooks away from it.
type admission struct {
	// ordinaryPush is whether a push through the gate was accepted with
	// nothing redirected, and ordinaryMessage is what git reported about it.
	ordinaryPush    bool
	ordinaryMessage string
	// initRefused is whether creating the gate under the redirect was refused.
	initRefused bool
	// redirectedPush is whether the push under the redirect was accepted, and
	// redirectedMessage is what git reported about it.
	redirectedPush    bool
	redirectedMessage string
	// fired is every tripwire the scenario recorded, which is how which hook
	// git ran is read rather than inferred.
	fired []string
}

// pushToTheGate pushes the branch under validation to the gate remote and
// reports whether the push was accepted and what git said about it.
func pushToTheGate(t *testing.T, scenario fixture.Scenario, env map[string]string, dir string) (bool, string) {
	t.Helper()
	out, err := journey.GitWith(scenario, env, dir, "push", gateRemote, scenario.Branch)
	return err == nil, out
}

// gateRemote is the name a working copy reaches its gate by. It is
// gate.RemoteName, restated here for the reason internal/fixture restates it:
// a harness that asked the package under validation what to push to would be
// reporting that package agreeing with itself.
const gateRemote = "assistant"

// refuses holds one attempt to the condition internal/fixture recorded for it,
// with want saying whether the condition is a refusal at all.
func refuses(t *testing.T, scenario fixture.Scenario, id fixture.ID, want bool, observed birth) {
	t.Helper()
	condition, err := journey.Condition(id)
	if err != nil {
		t.Fatalf("%v", err)
	}
	quiet := condition.Expect.TripwiresQuiet
	if len(quiet) == 0 {
		t.Fatalf("%s records no tripwire that has to stay quiet, and every condition here has one", id)
	}
	check := journey.Check[birth]{
		What: string(id) + ": " + firstSentence(condition.Expect.Summary),
		Holds: func(b birth) error {
			if b.refused != want {
				if want {
					return fmt.Errorf("%s was not refused; it answered %s", b.what, b.message)
				}
				return fmt.Errorf("%s was refused: %s", b.what, b.message)
			}
			if want {
				if missing := journey.Carries(b.message, condition.Expect.MessageContains); len(missing) > 0 {
					return fmt.Errorf("the refusal does not say %q; it said:\n%s", missing, b.message)
				}
			}
			for _, tripwire := range quiet {
				if slices.Contains(b.fired, tripwire) {
					return fmt.Errorf("the planted hook %s ran, and nothing a template carries may "+
						"execute here; the scenario recorded %v", tripwire, b.fired)
				}
			}
			return nil
		},
		Counterfeits: []journey.Counterfeit[birth]{
			{Named: "the attempt came out the other way round", Break: func(b birth) birth {
				b.refused = !b.refused
				if b.refused {
					b.message = strings.Join(condition.Expect.MessageContains, " ")
				}
				return b
			}},
			{Named: "a hook the template carries ran", Break: func(b birth) birth {
				b.fired = append(slices.Clone(b.fired), quiet[0])
				return b
			}},
		},
	}
	if want {
		check.Counterfeits = append(check.Counterfeits,
			journey.Counterfeit[birth]{
				Named: "the refusal says none of what the condition requires",
				Break: func(b birth) birth {
					b.message = "gate: something went wrong"
					return b
				},
			})
	}
	if err := check.Verify(observed); err != nil {
		t.Fatalf("%v", err)
	}
}

// gateJourney returns a journey over a working copy of this scenario's origin
// that belongs to one attempt alone, so the sequence one condition needs
// cannot decide what another one meets.
func gateJourney(t *testing.T, scenario fixture.Scenario) (*journey.Journey, string) {
	t.Helper()
	dir, err := journey.Clone(scenario, filepath.Join(t.TempDir(), "work"))
	if err != nil {
		t.Fatalf("cloning a working copy for this attempt: %v", err)
	}
	return open(t, scenario, func(o *journey.Options) { o.Dir = dir }), dir
}

// attempt initializes a gate through the binary under an environment of its
// own and records what happened.
func attempt(t *testing.T, scenario fixture.Scenario, j *journey.Journey, dir string,
	env map[string]string, what string) birth {
	t.Helper()
	answer := j.CommandWith(dir, env, "init", "--default-branch", fixture.DefaultBranch)
	fired, err := journey.Fired(scenario)
	if err != nil {
		t.Fatalf("reading the scenario's tripwires: %v", err)
	}
	return birth{
		what:    what,
		refused: answer.Code != machine.ExitOK,
		message: answer.Message(),
		fired:   fired,
	}
}

// scenarioPath is one of the paths a scenario carries, and fails the test when
// it carries none under that name.
func scenarioPath(t *testing.T, scenario fixture.Scenario, key string) string {
	t.Helper()
	path := scenario.Paths[key]
	if path == "" {
		t.Fatalf("scenario %s carries no path named %q", scenario.Name, key)
	}
	return path
}

// firstSentence is the opening sentence of a recorded expectation, for a check
// that says what it establishes in the catalog's own words without carrying a
// paragraph.
func firstSentence(summary string) string {
	if at := strings.Index(summary, ". "); at > 0 {
		return summary[:at+1]
	}
	return summary
}
