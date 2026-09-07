package journey_test

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/fixture"
	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
)

// classified is what became of one planted report: the action the finding
// carried once the product had read it, and whether that finding could have
// been sent to a fixer.
type classified struct {
	// condition names the planted condition this came from.
	condition fixture.ID
	// action is the action the finding carries after the product normalized
	// it.
	action findings.Action
	// fixable is how many findings in the report were fix-eligible.
	fixable int
	// findings is how many findings the report carries, which the review path
	// adds to.
	findings int
	// wrongRevision is whether the same bytes, served without the revision the
	// run asked about substituted into them, were refused whole.
	wrongRevision bool
}

// TestAFindingThatIsNotClassifiedStopsForAPerson drives PRD principle P3
// through the binary and over the wire an agent answers on.
//
// Section 13's test for P3 is the parser: a finding with a missing action, an
// empty action, and an unrecognized action all become ask, and none is
// eligible for automatic fixing. internal/fixture plants those three twice
// over, once for each entry point a report can arrive through, and this drives
// all six as the bytes an agent prints rather than as reports built by hand.
//
// The binary half is separate and is what a run actually meets today. Every
// stage of this build reports a finding with the action ask, so a run holds at
// every one of them for a person rather than reporting a pass it did not
// establish, and no such finding enters a fix round.
func TestAFindingThatIsNotClassifiedStopsForAPerson(t *testing.T) {
	principles.Cite(t, principles.P3)

	subject, err := journey.Subject()
	if err != nil {
		t.Fatalf("%v", err)
	}
	base, ok := subject.Scenario(fixture.ScenarioBase)
	if !ok {
		t.Fatalf("the fixture has no %s scenario", fixture.ScenarioBase)
	}

	// The revision the review stage is asked about is the run's, and the
	// planted bytes name the head the build left. The two are substituted
	// rather than assumed equal, and to show the substitution is doing
	// something the same bytes are also served unchanged against the same
	// demand, where findings.ParseReviewReport refuses them whole.
	asked := base.Commits["logic-bug"]
	planted := base.Commits["branch-head"]
	if asked == "" || planted == "" || asked == planted {
		t.Fatalf("this scenario has to carry two different commits to substitute between, and carries "+
			"%q and %q", asked, planted)
	}
	demand := findings.Demand{Revision: asked, Touched: []string{"total.go", "docs/behavior.md"}}

	for _, id := range []fixture.ID{
		"refusal-finding-action-missing",
		"refusal-finding-action-empty",
		"refusal-finding-action-unrecognized",
		"refusal-finding-action-missing-review-path",
		"refusal-finding-action-empty-review-path",
		"refusal-finding-action-unrecognized-review-path",
	} {
		t.Run(string(id), func(t *testing.T) {
			condition, err := journey.Condition(id)
			if err != nil {
				t.Fatalf("%v", err)
			}
			if condition.Expect.Value != "findings.ActionAsk" {
				t.Fatalf("%s records the answer %q, and this test drives the ask default",
					id, condition.Expect.Value)
			}
			path := base.AgentResponses[string(id)]
			if path == "" {
				t.Fatalf("this scenario plants no agent response for %s", id)
			}
			review := strings.HasSuffix(string(id), "-review-path")

			observed := classified{condition: id}
			body := readResponse(t, path)
			if review {
				body = strings.ReplaceAll(body, planted, asked)
				if strings.Contains(body, planted) || !strings.Contains(body, asked) {
					t.Fatalf("substituting the revision into %s did nothing", path)
				}
			}
			result := answerWithBytes(t, body, review, demand)
			if len(result.Report.Findings) == 0 {
				t.Fatalf("the report the product read carries no finding at all")
			}
			observed.action = result.Report.Findings[0].Action
			observed.fixable = len(result.Report.Fixable())
			observed.findings = len(result.Report.Findings)

			if review {
				// The same bytes with the build's own head left in them, which
				// is what a harness relying on the two coinciding would serve.
				_, err := answerFailing(t, readResponse(t, path), demand)
				observed.wrongRevision = errors.Is(err, findings.ErrWrongRevision)
			}

			clauses := []journey.Clause[classified]{
				{
					States: "the finding carries the action ask",
					Holds: func(c classified) error {
						if c.action != findings.ActionAsk {
							return fmt.Errorf("the finding carries the action %q, and an action the product "+
								"cannot read has to become %q", c.action, findings.ActionAsk)
						}
						return nil
					},
				},
				{
					States:  "no finding in the report is eligible for automatic fixing",
					Absence: true,
					// A report carrying no finding at all would have nothing
					// that could have been fix-eligible, so a count of none
					// would hold however the product classified what it read.
					Possible: func(c classified) error {
						if c.findings == 0 {
							return errors.New("the report the product read carries no finding, so nothing " +
								"in it could have been eligible and a count of none says nothing")
						}
						return nil
					},
					Holds: func(c classified) error {
						if c.fixable != 0 {
							return fmt.Errorf("%d finding(s) in this report are fix-eligible, and an "+
								"unclassified finding may never enter a fix round", c.fixable)
						}
						return nil
					},
				},
			}
			counterfeits := []journey.Counterfeit[classified]{
				{Named: "the unreadable action was dropped and the finding came back as a note",
					Break: func(c classified) classified {
						c.action = findings.ActionNote
						return c
					}},
				{Named: "the unreadable action was read as a request to fix",
					Break: func(c classified) classified {
						c.action = findings.ActionFix
						return c
					}},
				{Named: "the finding was eligible for a fix round anyway",
					Break: func(c classified) classified {
						c.fixable = 1
						return c
					}},
			}
			if review {
				// There is nothing to substitute on the other path, so neither
				// the clause about the substitution nor the counterfeit that
				// reaches it belongs to a report that arrived through it.
				clauses = append(clauses, journey.Clause[classified]{
					States: "the same bytes carrying the head the build recorded were refused for naming " +
						"the wrong revision",
					Holds: func(c classified) error {
						if !c.wrongRevision {
							return errors.New("the same bytes carrying the head the build recorded were " +
								"not refused for naming the wrong revision, so nothing here shows the " +
								"substitution was needed")
						}
						return nil
					},
				})
				counterfeits = append(counterfeits, journey.Counterfeit[classified]{
					Named: "the bytes naming the build's own head were accepted rather than refused",
					Break: func(c classified) classified {
						c.wrongRevision = false
						return c
					},
				})
			}
			becomesAsk := journey.Check[classified]{
				What: fmt.Sprintf("%s: the finding the product read out of the planted bytes carries the "+
					"action ask and is never eligible for automatic fixing", id),
				Clauses:      clauses,
				Counterfeits: counterfeits,
			}
			if err := becomesAsk.Verify(observed); err != nil {
				t.Fatalf("%v", err)
			}
		})
	}

	t.Run("through the binary", func(t *testing.T) {
		j := inClone(t)
		held := startRun(t, j, "--intent", "a change whose stages have no bodies in this build")

		holds := journey.Check[machine.Run]{
			What: "a stage of a run through the binary that established nothing reports an unclassified " +
				"finding, holds the run for a person, and offers no automatic fix",
			Clauses: []journey.Clause[machine.Run]{
				{
					States: "the run is waiting on a person rather than reporting a pass",
					Holds: func(run machine.Run) error {
						if run.Outcome != machine.OutcomeDecision {
							return fmt.Errorf("the run came back %s rather than waiting on a person", run.Outcome)
						}
						return nil
					},
				},
				{
					States: "the waiting run carries a decision to answer",
					Holds: func(run machine.Run) error {
						if run.Decision == nil {
							return errors.New("the run is waiting and carries no decision to answer")
						}
						return nil
					},
				},
				{
					States: "the decision is relayed with the findings that produced it",
					Holds: func(run machine.Run) error {
						if run.Decision == nil || len(run.Decision.Findings) == 0 {
							return errors.New("the decision carries none of the findings that produced it, and " +
								"PRD section 9 relays a finding that needs a decision with its full text")
						}
						return nil
					},
				},
				{
					States: "every finding holding the run carries the action ask",
					Holds: func(run machine.Run) error {
						if run.Decision == nil {
							return errors.New("the run carries no decision, so no finding of one could be read")
						}
						for _, finding := range run.Decision.Findings {
							if finding.Action != findings.ActionAsk {
								return fmt.Errorf("the finding %q holding this run carries the action %q",
									finding.ID, finding.Action)
							}
						}
						return nil
					},
				},
				{
					States: "the decision offers ending the run among its options",
					Holds: func(run machine.Run) error {
						if run.Decision == nil {
							return errors.New("the run carries no decision, so it offers no options")
						}
						if !slices.Contains(run.Decision.Options, string(machine.OutcomeCancelled)) &&
							!slices.Contains(run.Decision.Options, "cancelled") {
							return fmt.Errorf("the decision offers %v, and ending the run is not among them",
								run.Decision.Options)
						}
						return nil
					},
				},
			},
			Counterfeits: []journey.Counterfeit[machine.Run]{
				{Named: "the stage reported a pass it did not establish", Break: func(run machine.Run) machine.Run {
					run = cloneRun(run)
					run.Outcome = machine.OutcomeChecksPassed
					return run
				}},
				{Named: "the waiting run came back with no decision at all",
					Break: func(run machine.Run) machine.Run {
						run = cloneRun(run)
						run.Decision = nil
						return run
					}},
				{Named: "the finding holding the run was classified as one a fixer may take",
					Break: func(run machine.Run) machine.Run {
						run = cloneRun(run)
						decision := *run.Decision
						decision.Findings = slices.Clone(decision.Findings)
						decision.Findings[0].Action = findings.ActionFix
						run.Decision = &decision
						return run
					}},
				{Named: "the decision was relayed without the findings that produced it",
					Break: func(run machine.Run) machine.Run {
						run = cloneRun(run)
						decision := *run.Decision
						decision.Findings = nil
						run.Decision = &decision
						return run
					}},
				{Named: "the decision offers no way to end the run",
					Break: func(run machine.Run) machine.Run {
						run = cloneRun(run)
						decision := *run.Decision
						decision.Options = nil
						run.Decision = &decision
						return run
					}},
			},
		}
		if err := holds.Verify(held); err != nil {
			t.Fatalf("%v", err)
		}
	})
}

// readResponse reads the exact bytes internal/fixture recorded an agent as
// printing.
func readResponse(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the planted agent response %s: %v", path, err)
	}
	return string(body)
}

// answerWithBytes puts the planted bytes through the production adapter as the
// agent's own output, and returns what the product made of them.
//
// It goes through a process rather than calling internal/findings, because the
// path P3 lives on runs from an agent's text through the adapter that reads a
// result envelope and only then through the parser. A test that handed the
// parser a string would skip the half of that path a stage actually takes.
func answerWithBytes(t *testing.T, body string, review bool, demand findings.Demand) agents.Result {
	t.Helper()
	result, err := invokeStandin(t, body, review, demand)
	if err != nil {
		t.Fatalf("the product refused output it was supposed to read: %v", err)
	}
	return result
}

// answerFailing is answerWithBytes for output the product is expected to
// refuse.
func answerFailing(t *testing.T, body string, demand findings.Demand) (agents.Result, error) {
	t.Helper()
	return invokeStandin(t, body, true, demand)
}

// invokeStandin scripts the stand-in to print body and runs one invocation
// through the production adapter.
func invokeStandin(t *testing.T, body string, review bool, demand findings.Demand) (agents.Result, error) {
	t.Helper()
	agent := standin.New(t, standin.Script{Steps: []standin.Step{{
		Times: standin.Always,
		Reply: standin.Text(body),
	}}})
	invocation := agents.Invocation{
		Prompt: "read the change against the intent and report what you find",
		Shape:  agents.ShapeReport,
		Dir:    t.TempDir(),
	}
	if review {
		invocation.Shape = agents.ShapeReview
		invocation.Review = demand
	}
	return agent.Runner().Run(t.Context(), agents.PurposeReview, invocation)
}
