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

// birth is what one attempt to create or repair a gate answered.
//
// It carries nothing about the planted hooks: no clause built on it could rest
// on the scenario's tripwire file, for the reason the test's own doc comment
// gives, so nothing here reads that file.
type birth struct {
	// what names the attempt, for a failure that says which one.
	what string
	// refused is whether the attempt was refused.
	refused bool
	// message is what it was refused with, or what it reported on succeeding.
	message string
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
// What these four do not establish is that no template hook ran, and no clause
// here claims it. Those hooks are receive-side, so only a push to the gate
// could run one, and no push in this build reaches any of them: internal/gate
// installs its own pre-receive, whose script runs "assistant gate admit" and
// exits on its status before chaining to a preserved hook at the .local name,
// and internal/cli carries no gate verb. So admission fails, the push is
// declined, and neither a promoted template pre-receive nor update nor
// post-update runs. A clause on the tripwire file would hold whatever a
// template had installed, so these four subtests carry no such clause and read
// that file not at all; README.md records the gap among the limits of the
// binary being driven. Nor does any clause look at the gate's hooks directory,
// so whether a hook arrived is unestablished as well as whether one ran.
//
// What each subtest does establish is how the initialization came out: the two
// refusals refuse, carrying the substrings their conditions record, and the
// two closed channels are not refused, which is what shows the channel closed
// rather than caught. Those two record no substring and no sentinel, so there
// is nothing else there to hold a message to.
//
// The last subtest is the other end of the same question and is not a refusal
// at all. internal/gate names core.hooksPath as an open gap, and this drives a
// push under a configuration file that redirects it, so what the gap costs is
// read off which hook ran rather than out of a document.
//
// Every attempt gets a working copy and a home of its own, so the sequence one
// condition needs cannot decide what another one meets. The redirect subtest
// runs last because it is the only one that writes to the scenario's shared
// tripwire file, so a later subtest that read that file would want to run
// before it rather than after.
func TestNothingOutsideTheGateChoosesWhatRunsOnAPushToIt(t *testing.T) {
	principles.Cite(t, principles.P7)

	scenario := claim(t, fixture.ScenarioHostileTemplate)
	mixed := map[string]string{"GIT_CONFIG_GLOBAL": scenarioPath(t, scenario, "gitconfig-template-mixed")}
	preReceiveOnly := map[string]string{
		"GIT_CONFIG_GLOBAL": scenarioPath(t, scenario, "gitconfig-template-pre-receive-only"),
	}

	t.Run("closed-git-template-dir-environment", func(t *testing.T) {
		j, dir := gateJourney(t, scenario)
		refuses(t, "closed-git-template-dir-environment", false,
			attempt(j, dir,
				map[string]string{"GIT_TEMPLATE_DIR": scenarioPath(t, scenario, "template-mixed")},
				"initializing a gate with GIT_TEMPLATE_DIR naming the hostile template"))
	})

	t.Run("refusal-template-hooks-at-birth", func(t *testing.T) {
		j, dir := gateJourney(t, scenario)
		refuses(t, "refusal-template-hooks-at-birth", true,
			attempt(j, dir, mixed, "creating a gate under init.templateDir"))
	})

	t.Run("on an existing gate", func(t *testing.T) {
		j, dir := gateJourney(t, scenario)
		succeeds(t, j.CommandIn(dir, "init", "--default-branch", fixture.DefaultBranch))
		refuses(t, "refusal-template-hooks-on-repair", true,
			attempt(j, dir, mixed, "repairing an existing gate under init.templateDir"))
		refuses(t, "closed-template-pre-receive-on-repair", false,
			attempt(j, dir, preReceiveOnly,
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
		observed.ordinaryRuns = runsRecorded(t, plain)

		under, underDir := gateJourney(t, scenario)
		observed.initRefused = under.CommandWith(underDir, redirect,
			"init", "--default-branch", fixture.DefaultBranch).Code != machine.ExitOK
		observed.redirectedPush, observed.redirectedMessage = pushToTheGate(t, scenario, redirect, underDir)
		observed.expectedFired = redirectedHooks
		if observed.fired, err = journey.Fired(scenario); err != nil {
			t.Fatalf("reading the scenario's tripwires: %v", err)
		}

		clauses := []journey.Clause[admission]{
			{
				// A disjunction for the reason the one below it is a
				// disjunction, which Settlements states: a clause holding that
				// this push is declined would assert that a gap persists, and
				// would fail on the day the admit verb it is missing lands.
				// What has to hold either way is that the gate does not admit
				// a push with nothing checking it, and the run count is read
				// out of the home's own records so both halves are observed.
				States: "a push through the gate with nothing redirected is not accepted with nothing " +
					"checking it: either it is declined, or it is accepted and a run started from it",
				Holds: func(a admission) error {
					if a.ordinaryPush && a.ordinaryRuns == 0 {
						return fmt.Errorf("a push through the gate with nothing redirected was accepted and "+
							"this home recorded no run, so the gate admitted a push with nothing checking "+
							"it; git said:\n%s", a.ordinaryMessage)
					}
					return nil
				},
			},
			{
				States: "either the redirect was refused at initialization or the push under it was accepted, " +
					"so one of the two was observed",
				Holds: func(a admission) error {
					if !a.initRefused && !a.redirectedPush {
						return fmt.Errorf("the redirected push was declined and the initialization was not "+
							"refused, so this observed neither the gap nor its closure; the push said:\n%s",
							a.redirectedMessage)
					}
					return nil
				},
			},
		}
		counterfeits := []journey.Counterfeit[admission]{
			// Both fields, because the clause this reaches is a disjunction. A
			// counterfeit that flipped one of them would break only the
			// disjunct that happens to hold today, and would return the real
			// observation unchanged once the other one does - which refuses
			// the check for accepting a counterfeit rather than reporting
			// anything about the product. Landing on the one shape the clause
			// rejects, whichever disjunct held, is what keeps it live across
			// that transition.
			{Named: "the gate accepted an ordinary push with nothing checking it",
				Break: func(a admission) admission {
					a.ordinaryPush = true
					a.ordinaryRuns = 0
					return a
				}},
			{Named: "neither the initialization nor the push told us anything",
				Break: func(a admission) admission {
					a.initRefused = false
					a.redirectedPush = false
					return a
				}},
		}
		if observed.redirectedPush {
			// Only reachable once the push under the redirect was accepted: if
			// the product had refused the redirect there would be no gate to
			// push to and no hook of any kind to have run, and a clause about
			// which hook ran would hold over a world in which none could.
			clauses = append(clauses, journey.Clause[admission]{
				States: "the hooks that ran on the accepted push are the redirected ones",
				Holds: func(a admission) error {
					for _, tripwire := range a.expectedFired {
						if !slices.Contains(a.fired, tripwire) {
							return fmt.Errorf("the push was accepted and %s did not run, so which hook git ran "+
								"was not observed at all; the scenario recorded %v", tripwire, a.fired)
						}
					}
					return nil
				},
			})
			counterfeits = append(counterfeits, journey.Counterfeit[admission]{
				Named: "the redirected push was accepted and no redirected hook ran, so nothing was observed",
				Break: func(a admission) admission {
					a.fired = nil
					return a
				},
			})
		}
		reaches := journey.Check[admission]{
			What:         "P7: a push under a redirected core.hooksPath",
			Clauses:      clauses,
			Counterfeits: counterfeits,
		}
		if err := reaches.Verify(observed); err != nil {
			t.Fatalf("%v", err)
		}
		if !observed.initRefused {
			t.Logf("KNOWN GAP %s: initialization reported success under a configuration file "+
				"redirecting core.hooksPath, the push was accepted, and the hooks git ran were %v rather "+
				"than the gate's own. %s", condition.ID, observed.fired, condition.Expect.Gap)
		}
		if observed.ordinaryPush {
			t.Logf("A push through the gate with nothing redirected was accepted and this home recorded "+
				"%d run(s), so the admission boundary is answered by something now. git said:\n%s",
				observed.ordinaryRuns, observed.ordinaryMessage)
		} else {
			// This subtest starts no service, and the gate's admission hook
			// asks one. So a decline here is not evidence about admission and
			// is not reported as any: what the clause holds is only that the
			// gate did not accept the push with nothing checking it.
			// TestAPushToTheGateByNameAuthorizesTheRun is where admission is
			// driven, with a service up to answer the hook.
			t.Logf("A push through the gate with nothing redirected was declined. This subtest runs no "+
				"service, so what declined it is not established here. It said:\n%s",
				observed.ordinaryMessage)
		}
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
	// ordinaryRuns is how many runs this home had recorded once that push was
	// over. It is what makes the accepted half of the clause observable rather
	// than a claim, and it is read out of the home's records because reading it
	// off the surface would need a service this check never starts.
	ordinaryRuns int
	// initRefused is whether creating the gate under the redirect was refused.
	initRefused bool
	// redirectedPush is whether the push under the redirect was accepted, and
	// redirectedMessage is what git reported about it.
	redirectedPush    bool
	redirectedMessage string
	// fired is every tripwire the scenario recorded, which is how which hook
	// git ran is read rather than inferred.
	fired []string
	// expectedFired is the tripwires an accepted push under the redirect has
	// to have set off. It is redirectedHooks, carried on the observation so a
	// check reading it can be shown to fail.
	expectedFired []string
}

// redirectedHooks are the hooks git runs in place of the gate's own when
// core.hooksPath redirects them, named here rather than read off the catalog.
//
// internal/fixture records no field meaning "must have fired": Outcome carries
// TripwiresQuiet, which means the opposite, and this condition is a gap rather
// than a refusal, so its expectation is prose. Reading TripwiresQuiet here
// would be reading a must-not-appear list as a must-have-appeared one, which
// is right only while the list is empty. The gate installs pre-receive and
// post-receive and the plant replaces both; a push git accepts runs pre-receive
// first, so that is the one an accepted push has to show.
var redirectedHooks = []string{"hookspath-hook-pre-receive"}

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
func refuses(t *testing.T, id fixture.ID, want bool, observed birth) {
	t.Helper()
	condition, err := journey.Condition(id)
	if err != nil {
		t.Fatalf("%v", err)
	}
	clauses := []journey.Clause[birth]{
		{
			States: "the attempt came out the way the condition records",
			Holds: func(b birth) error {
				if b.refused != want {
					if want {
						return fmt.Errorf("%s was not refused; it answered %s", b.what, b.message)
					}
					return fmt.Errorf("%s was refused: %s", b.what, b.message)
				}
				return nil
			},
		},
	}
	counterfeits := []journey.Counterfeit[birth]{
		{Named: "the attempt came out the other way round", Break: func(b birth) birth {
			b.refused = !b.refused
			if b.refused {
				b.message = strings.Join(condition.Expect.MessageContains, " ")
			}
			return b
		}},
	}
	if want {
		clauses = append(clauses, journey.Clause[birth]{
			States: "the refusal says what the condition requires it to say",
			Holds: func(b birth) error {
				if missing := journey.Carries(b.message, condition.Expect.MessageContains); len(missing) > 0 {
					return fmt.Errorf("the refusal does not say %q; it said:\n%s", missing, b.message)
				}
				return nil
			},
		})
		counterfeits = append(counterfeits, journey.Counterfeit[birth]{
			Named: "the refusal says none of what the condition requires",
			Break: func(b birth) birth {
				b.message = "gate: something went wrong"
				return b
			},
		})
	}
	check := journey.Check[birth]{
		What:         "P7: " + string(id),
		Clauses:      clauses,
		Counterfeits: counterfeits,
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
//
// It does not read the scenario's tripwire file. No clause built on what it
// returns could rest on that file, for the reason the test's own doc comment
// gives, and reading it anyway would fail these subtests over a value none of
// them uses.
func attempt(j *journey.Journey, dir string, env map[string]string, what string) birth {
	answer := j.CommandWith(dir, env, "init", "--default-branch", fixture.DefaultBranch)
	return birth{
		what:    what,
		refused: answer.Code != machine.ExitOK,
		message: answer.Message(),
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
