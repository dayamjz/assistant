package journey_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/fixture"
	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/redact"
)

// checked is what the code host answered about the checks on a commit, and
// what the product made of it.
type checked struct {
	// pushed is the commit the run would have pushed, which is the commit the
	// answer has to be about.
	pushed string
	// recorded is the commit internal/fixture baked into the answer when it
	// built the scenario.
	recorded string
	// headServed and headStale are the commits the provider reported when the
	// answer was served with the run's own head substituted and when it was
	// served exactly as the build left it.
	headServed string
	headStale  string
	// verdict is what an empty check list came to with nothing declaring that
	// this repository has none.
	verdict forge.Verdict
	// declared is what the same empty list comes to once no_ci declares there
	// are no checks, which is the only thing that turns it into a pass.
	declared forge.Verdict
	// substitutionRefused is whether substituting a head into an answer that
	// does not carry the recorded one is refused rather than doing nothing.
	substitutionRefused bool
}

// TestAnEmptyCheckListIsNotAPass drives internal/fixture's no-registered-checks
// condition at package reach.
//
// The binary cannot reach it. No stage body talks to a code host in this
// build, so no run asks the provider anything and there is no run to read a
// verdict out of. What is driven is internal/forge over the provider command,
// which is a process this harness stands in for the same way it stands in for
// the agent: a copy of this test binary on PATH, printing the prepared answer.
//
// The head substitution is the part that is easy to leave out and is what the
// condition turns on. The answer internal/fixture recorded names the head the
// build left, a run rebases and may add commits, and internal/forge
// deliberately tells an answer about an older head apart from an empty check
// list. Served unchanged it reports a stale check list, which is a different
// observation from the one planted, so this drives both and shows they differ.
func TestAnEmptyCheckListIsNotAPass(t *testing.T) {
	scenario := scenarioNamed(t, fixture.ScenarioBase)
	condition, err := journey.Condition("refusal-no-registered-checks")
	if err != nil {
		t.Fatalf("%v", err)
	}
	answerPath := scenario.ProviderResponses["checks-empty"]
	if answerPath == "" {
		t.Fatalf("the %s scenario records no checks answer", scenario.Name)
	}

	// The commit a run would have pushed by the time the checks stage asks.
	// It is another real commit on the same branch rather than the head the
	// build left, because a substitution onto the commit already in the answer
	// would succeed and prove nothing.
	observed := checked{
		pushed:   scenario.Commits["logic-bug"],
		recorded: scenario.Commits["branch-head"],
	}
	if observed.pushed == "" || observed.recorded == "" || observed.pushed == observed.recorded {
		t.Fatalf("this scenario has to carry two different commits for the substitution to be visible, "+
			"and carries %q and %q", observed.pushed, observed.recorded)
	}

	served, err := journey.SubstituteHead(observed.recorded, observed.pushed, answerPath,
		filepath.Join(t.TempDir(), "checks.json"))
	if err != nil {
		t.Fatalf("substituting the run's head into the recorded answer: %v", err)
	}
	observed.headServed = readChecks(t, served).HeadCommit

	stale := readChecks(t, answerPath)
	observed.headStale = stale.HeadCommit

	report := readChecks(t, served)
	observed.verdict = report.Evaluate(forge.DeclaredNoCI(config.Config{})).Verdict
	observed.declared = report.Evaluate(forge.DeclaredNoCI(config.Config{NoCI: true})).Verdict

	// A substitution that finds nothing to replace has to say so. Doing
	// nothing quietly is the same failure as not substituting at all, with no
	// symptom.
	_, err = journey.SubstituteHead("a-commit-this-answer-does-not-name", observed.pushed, answerPath,
		filepath.Join(t.TempDir(), "nothing.json"))
	observed.substitutionRefused = errors.Is(err, journey.ErrNoRecordedHead)

	unregistered := journey.Check[checked]{
		What: "an empty check list on the commit the run pushed is not a pass, only a no-CI declaration " +
			"makes it one, and an answer served without the run's own head substituted into it reports a " +
			"different commit rather than the condition",
		Holds: func(c checked) error {
			if c.headServed != c.pushed {
				return fmt.Errorf("the provider reported checks on %s and the run pushed %s",
					c.headServed, c.pushed)
			}
			if c.headStale != c.recorded || c.headStale == c.pushed {
				return fmt.Errorf("the answer served as the build left it reported %s, so nothing here "+
					"shows what serving it unchanged would have cost", c.headStale)
			}
			if c.verdict != forge.VerdictNoChecks {
				return fmt.Errorf("an empty check list came to %s, and an empty list means unregistered "+
					"rather than passing", c.verdict)
			}
			if c.declared != forge.VerdictPassed {
				return fmt.Errorf("an empty check list with no_ci declared came to %s, and the "+
					"declaration is what turns it into a pass", c.declared)
			}
			if !c.substitutionRefused {
				return errors.New("substituting into an answer that names no such head did nothing and " +
					"reported success, so a substitution that stopped working would be invisible")
			}
			return nil
		},
		Counterfeits: []journey.Counterfeit[checked]{
			{Named: "an empty check list was read as green", Break: func(c checked) checked {
				c.verdict = forge.VerdictPassed
				return c
			}},
			{Named: "an empty check list was read as a failure", Break: func(c checked) checked {
				c.verdict = forge.VerdictFailed
				return c
			}},
			{Named: "the no-CI declaration did not make the empty list a pass", Break: func(c checked) checked {
				c.declared = forge.VerdictNoChecks
				return c
			}},
			{Named: "the answer was about a commit the run never pushed", Break: func(c checked) checked {
				c.headServed = c.recorded
				return c
			}},
			{Named: "the substitution changed nothing, so both answers named the same commit",
				Break: func(c checked) checked {
					c.headStale = c.pushed
					return c
				}},
			{Named: "a substitution that found nothing to replace reported success",
				Break: func(c checked) checked {
					c.substitutionRefused = false
					return c
				}},
		},
	}
	if err := unregistered.Verify(observed); err != nil {
		t.Fatalf("%v", err)
	}
	if condition.Expect.Value != "forge.VerdictNoChecks" {
		t.Fatalf("the condition now records the answer %q, so what it must produce has changed",
			condition.Expect.Value)
	}
	if missing := journey.Carries(observed.verdict.String(), condition.Expect.MessageContains); len(missing) > 0 {
		t.Fatalf("the verdict renders as %q and the condition requires %q",
			observed.verdict, condition.Expect.MessageContains)
	}
}

// readChecks asks the code host about the checks on a pull request, with the
// prepared answer standing in for what the provider command prints.
func readChecks(t *testing.T, answer string) forge.ChecksReport {
	t.Helper()
	shims, err := journey.Shims()
	if err != nil {
		t.Fatalf("%v", err)
	}
	provider, err := forge.NewGitHub(redact.New(),
		forge.WithBinary(filepath.Join(shims, journey.ProviderShimName+shimSuffix())),
		forge.WithRepository("fixture/subject"),
		forge.WithBaseEnvironment(append(os.Environ(), journey.ProviderAnswerVariable+"="+answer)))
	if err != nil {
		t.Fatalf("building the code host adapter over the prepared answer: %v", err)
	}
	report, err := provider.Checks(t.Context(), 1)
	if err != nil {
		t.Fatalf("reading the checks: %v", err)
	}
	return report
}
