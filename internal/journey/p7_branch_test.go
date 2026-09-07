package journey_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/fixture"
	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/vcs"
)

// installed is what a whole run over a branch carrying an agent harness
// installation came to.
type installed struct {
	// outcome is where the run ended.
	outcome machine.Outcome
	// fired is every tripwire the scenario recorded by the end of the run.
	// The branch's own agent binary is one of the planted executables, so this
	// is also what says whether the branch's agent was launched: which agent a
	// run resolved is not reported anywhere on the command surface, and the
	// doctor's agent check answers what is runnable on this machine rather
	// than what any run resolved.
	fired []string
	// mustStayQuiet is what the two conditions say may not run. It is the
	// union of both lists rather than either, taken from the catalog once:
	// comparing it afterwards against a second copy of the same expression
	// would be a clause no observation could contradict, and what makes the
	// union worth reading is the Absence precondition that refuses an empty
	// one.
	mustStayQuiet []string
	// requiredRejections is what the pushed-configuration condition records
	// the resolution has to report, and is in the observation for the same
	// reason: a check reading it out of a closure could not be shown to fail
	// when the catalog stops recording any.
	requiredRejections []string
	// rejected is the keys config.Resolve dropped from the pushed layer, and
	// ignorePatterns is what it resolved that key to.
	rejected       []string
	ignorePatterns []string
	// suppressionRefused is whether a run configured to suppress the
	// repository's own instructions refused before launching anything.
	suppressionRefused bool
	// suppressionMessage is what it refused with.
	suppressionMessage string
}

// TestTheBranchUnderValidationChoosesNothingThatRuns drives PRD principle P7
// over the shape a real agent harness installation takes.
//
// P7's own test in section 13 is that a branch setting a command, an agent, or
// a review rule in its own configuration has no effect on what runs. What
// internal/fixture plants is the same rule against a shape a guard written for
// git configuration would walk straight past: executable hooks under
// .claude/hooks and .githooks, a settings document binding them to lifecycle
// events, an agent definition, a slash command, a direnv file, and project
// instructions telling the reader to run one of them. The working copy's own
// git configuration points core.hooksPath at the committed directory, so the
// two .githooks scripts are the hooks git runs there rather than two inert
// files.
//
// "Nothing executed" is checked as a fact rather than assumed. Every planted
// executable appends to the scenario's tripwire file, so the claim is a file
// that does not exist rather than an absence nobody looked for.
//
// The agent the branch ships is one of those executables, which is what
// carries the agent half of P7 here. Which agent a run resolved is reported by
// no shipped surface - no internal/machine shape carries one, and the doctor's
// agent check resolves the constant "auto" against the default catalog, so it
// answers what is runnable on this machine rather than what a run resolved - so
// a check comparing a name off that report would hold whatever the run chose.
// The tripwire does not have that problem: a run that launched the branch's
// agent would have fired it.
//
// The suppressed case is here too, and it is a refusal rather than a run. The
// shipped adapter declares no instruction suppression, so a home asking for it
// cannot build a pipeline at all, which is PRD section 10's refusal before
// launch arriving through the binary.
func TestTheBranchUnderValidationChoosesNothingThatRuns(t *testing.T) {
	principles.Cite(t, principles.P7)

	scenario := claim(t, fixture.ScenarioBase)
	condition, err := journey.Condition("refusal-hostile-harness-installation")
	if err != nil {
		t.Fatalf("%v", err)
	}
	// The pushed document is a condition of its own, with its own recorded
	// tripwire and its own recorded rejections, and both conditions are
	// planted on the same branch. A run over that branch has to satisfy both,
	// so what must stay quiet is the union rather than either list.
	pushedConfig, err := journey.Condition("refusal-pushed-commands-and-agent")
	if err != nil {
		t.Fatalf("%v", err)
	}
	observed := installed{
		mustStayQuiet: slices.Concat(
			condition.Expect.TripwiresQuiet, pushedConfig.Expect.TripwiresQuiet),
		requiredRejections: pushedConfig.Expect.MessageContains,
	}

	j := open(t, scenario)
	succeeds(t, j.Command("init", "--default-branch", fixture.DefaultBranch))
	serve(t, j)
	observed.outcome = last(answerHolds(t, j,
		startRun(t, j, "--intent", "narrow the Total loop bound on purpose"), "approved")).Outcome
	if observed.fired, err = journey.Fired(scenario); err != nil {
		t.Fatalf("reading the scenario's tripwires: %v", err)
	}

	// The one call the pushed-configuration condition names: the operator's
	// own layer and the branch's, which is the only composition anything in
	// this build performs. What the trusted document carries reaches no run,
	// and Settlements records that as a gap rather than as a pass.
	pushed := pushedLayer(t, scenario)
	resolution, err := config.Resolve(config.Absent(config.OriginGlobal), pushed)
	if err != nil {
		t.Fatalf("resolving the branch's own configuration document: %v", err)
	}
	for _, rejection := range resolution.Rejected {
		observed.rejected = append(observed.rejected, rejection.String())
	}
	for _, pattern := range resolution.Config.IgnorePatterns {
		observed.ignorePatterns = append(observed.ignorePatterns, pattern.String())
	}

	// The suppressed case, in a home of its own so the run above is not driven
	// under a configuration it was not written for.
	suppressed := inCloneConfigured(t, map[string]any{"suppress_project_instructions": true})
	answer := suppressed.Command("--intent", "a run under a suppression nothing implements")
	observed.suppressionRefused = answer.Code != machine.ExitOK
	observed.suppressionMessage = answer.Message()

	governs := journey.Check[installed]{
		What: "a run over a branch carrying an agent harness installation executes none of it, the agent " +
			"binary the branch ships among the rest, drops every key the branch was not allowed to set " +
			"while keeping the one it was, and refuses outright when asked for a suppression the " +
			"resolved adapter does not implement. Which agent the run resolved is not established " +
			"here: no shipped surface reports it, so what is established is that the branch's own " +
			"agent binary never ran",
		Clauses: []journey.Clause[installed]{
			{
				States: "the run reached the stages the installation was planted in front of",
				Holds: func(i installed) error {
					if i.outcome != machine.OutcomeChecksPassed {
						return fmt.Errorf("the run ended %s, so it did not reach the stages the installation was "+
							"planted in front of", i.outcome)
					}
					return nil
				},
			},
			{
				States:  "nothing the branch installed ran during the run",
				Absence: true,
				// Every planted executable appends to the scenario's tripwire
				// file, so a scenario planting none would leave that file empty
				// however the product behaved. What makes the file worth
				// reading is that these two conditions plant executables and
				// record their names. One of them is the agent binary the
				// branch ships, so this clause is also what establishes that
				// the branch's agent was not the one a stage's work went
				// through.
				Possible: func(i installed) error {
					if len(i.mustStayQuiet) == 0 {
						return errors.New("the conditions name no tripwire that has to stay quiet, so nothing " +
							"here could tell an installation that ran from one that did not")
					}
					return nil
				},
				Holds: func(i installed) error {
					for _, tripwire := range i.mustStayQuiet {
						if slices.Contains(i.fired, tripwire) {
							return fmt.Errorf("%s ran during the run, and nothing a pushed branch installs may "+
								"execute; the scenario recorded %v", tripwire, i.fired)
						}
					}
					return nil
				},
			},
			{
				States: "the pushed-configuration condition records rejections to look for",
				Holds: func(i installed) error {
					if len(i.requiredRejections) == 0 {
						return errors.New("the pushed-configuration condition records no rejection to look for")
					}
					return nil
				},
			},
			{
				States: "the resolution reports every rejection the condition records",
				Holds: func(i installed) error {
					for _, want := range i.requiredRejections {
						if !slices.ContainsFunc(i.rejected, func(got string) bool { return strings.Contains(got, want) }) {
							return fmt.Errorf("the resolution does not report %q; it reported %v", want, i.rejected)
						}
					}
					return nil
				},
			},
			{
				States: "the resolution kept the one key the branch was allowed to set",
				Holds: func(i installed) error {
					if !slices.Contains(i.ignorePatterns, "vendor/**") {
						return fmt.Errorf("the resolution did not keep the branch's own ignore_patterns, which is a "+
							"key a pushed branch may set; it resolved %v", i.ignorePatterns)
					}
					return nil
				},
			},
			{
				States: "a run asking for a suppression the adapter does not implement was refused",
				Holds: func(i installed) error {
					if !i.suppressionRefused {
						return errors.New("a run asking for a suppression the adapter does not implement was not " +
							"refused, so it launched an agent that suppresses nothing while reporting that it did")
					}
					return nil
				},
			},
			{
				States: "that refusal names what it refused",
				Holds: func(i installed) error {
					if !strings.Contains(i.suppressionMessage, "suppress_project_instructions") {
						return fmt.Errorf("the refusal does not name what it refused: %q", i.suppressionMessage)
					}
					return nil
				},
			},
		},
		Counterfeits: []journey.Counterfeit[installed]{
			{Named: "one of the installed hooks ran during the run", Break: func(i installed) installed {
				i.fired = append(slices.Clone(i.fired), i.mustStayQuiet[0])
				return i
			}},
			{Named: "the condition records no rejection, so looking for them proves nothing",
				Break: func(i installed) installed {
					i.requiredRejections = nil
					return i
				}},
			{Named: "a key the branch was not allowed to set was applied in silence",
				Break: func(i installed) installed {
					i.rejected = nil
					return i
				}},
			{Named: "the whole pushed document was dropped, including the key it was allowed to set",
				Break: func(i installed) installed {
					i.ignorePatterns = nil
					return i
				}},
			{Named: "a run asking for a suppression nothing implements was started anyway",
				Break: func(i installed) installed {
					i.suppressionRefused = false
					return i
				}},
			{Named: "the refusal never names what it refused", Break: func(i installed) installed {
				i.suppressionMessage = "assistant: something went wrong"
				return i
			}},
			{Named: "the run stopped before it reached the stages the installation was planted for",
				Break: func(i installed) installed {
					i.outcome = machine.OutcomeFailed
					return i
				}},
		},
	}
	if err := governs.Verify(observed); err != nil {
		t.Fatalf("%v", err)
	}
}

// pushedLayer parses the configuration document the branch under validation
// committed, labelled as what it is: a document read from a pushed branch.
func pushedLayer(t *testing.T, scenario fixture.Scenario) config.Layer {
	t.Helper()
	subject := subject(t)
	repository, err := vcs.OpenWorktree(t.Context(), scenario.WorkingCopy, vcs.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("opening the subject working copy: %v", err)
	}
	body, err := repository.FileAt(t.Context(), scenario.Commits["branch-head"], subject.ConfigPath)
	if err != nil {
		t.Fatalf("reading %s from the branch under validation: %v", subject.ConfigPath, err)
	}
	layer, err := config.Parse(config.OriginPushed, body)
	if err != nil {
		t.Fatalf("parsing the branch's own configuration document: %v", err)
	}
	return layer
}
