package journey_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/fixture"
	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/principles"
)

// roles is what the agent was actually asked, over a review, a fix round, a
// second fix round, and the re-review that checks them.
type roles struct {
	// calls are the invocations the stand-in processes recorded, in arrival
	// order, each labelled by the role that made it.
	calls []invocationRole
	// reviewRefusedAtTheFixer is whether asking the fixer for a review shape
	// was refused before any process started.
	reviewRefusedAtTheFixer bool
}

// invocationRole is one recorded invocation and the role that made it.
type invocationRole struct {
	role    string
	session string
}

// TestTheReviewerNeverCertifiesItsOwnPrescription drives PRD principle P4 at
// package reach.
//
// The binary cannot reach it. No stage body launches an agent in this build,
// so a run makes no invocation to assert over, and a harness that drove a run
// and found no session would be reporting that nothing happened.
//
// Section 13 writes the test as a structural assertion over invocation
// records rather than a behavioural one, and this is that: a review, two fix
// rounds, and the re-review that checks them are put through the production
// adapter, and what each invocation carried is read off the wire the stand-in
// wrote rather than out of the adapter's account of itself.
//
// The half of the split the compiler holds is not restated here as a value a
// test sets for itself. agents.Runner.Run has no parameter a session could be
// named in, so a review carrying one does not compile, and internal/agents
// claims that. What this drives is what the two roles put on the wire, and the
// one refusal the compiler cannot make: a review shape offered to the fixer,
// where the shape rides on the Invocation both roles share.
func TestTheReviewerNeverCertifiesItsOwnPrescription(t *testing.T) {
	principles.Cite(t, principles.P4)

	subject, err := journey.Subject()
	if err != nil {
		t.Fatalf("%v", err)
	}
	base, ok := subject.Scenario(fixture.ScenarioBase)
	if !ok {
		t.Fatalf("the fixture has no %s scenario", fixture.ScenarioBase)
	}
	asked := base.Commits["logic-bug"]
	reviewBytes := strings.ReplaceAll(
		readResponse(t, base.AgentResponses["refusal-finding-action-unrecognized-review-path"]),
		base.Commits["branch-head"], asked)

	agent := standin.New(t, standin.Script{Steps: []standin.Step{
		{
			Match: standin.Match{PromptContains: "review"},
			Times: standin.Always,
			Reply: standin.Text(reviewBytes),
		},
		{
			Match: standin.Match{PromptContains: "fix"},
			Times: standin.Always,
			Reply: standin.Text("I moved the loop bound back to len(xs)."),
		},
	}})
	runner := agent.Runner()

	observed := roles{}
	dir := t.TempDir()
	demand := findings.Demand{Revision: asked, Touched: []string{"total.go"}}

	// The review. Runner.Run is the whole entry point, and it has no parameter
	// a session could be named in.
	if _, err := runner.Run(t.Context(), agents.PurposeReview, agents.Invocation{
		Prompt: "review this change against the recorded intent",
		Shape:  agents.ShapeReview,
		Review: demand,
		Dir:    dir,
	}); err != nil {
		t.Fatalf("the review invocation: %v", err)
	}
	observed.calls = append(observed.calls, invocationRole{role: "review", session: sessionOf(t, agent, 0)})

	// Two fix rounds through the one durable session a run's fixer keeps. The
	// second is where a session becomes visible on the wire at all: the first
	// opens it.
	fixer, err := agents.OpenFixer(t.Context(), runner, "")
	if err != nil {
		t.Fatalf("opening the run's fixer session: %v", err)
	}
	for round := range 2 {
		if _, err := fixer.Apply(t.Context(), agents.Invocation{
			Prompt: fmt.Sprintf("fix the finding the review reported, round %d", round+1),
			Shape:  agents.ShapeText,
			Dir:    dir,
		}); err != nil {
			t.Fatalf("fix round %d: %v", round+1, err)
		}
		observed.calls = append(observed.calls,
			invocationRole{role: "fix", session: sessionOf(t, agent, len(observed.calls))})
	}

	// The re-review, which PRD section 5 has treat the fix as author code. It
	// goes through the same entry point the first review did, so it cannot
	// carry the session that prescribed the fix even if a caller wanted it to.
	if _, err := runner.Run(t.Context(), agents.PurposeReview, agents.Invocation{
		Prompt: "review the change again, including what the fix round wrote",
		Shape:  agents.ShapeReview,
		Review: demand,
		Dir:    dir,
	}); err != nil {
		t.Fatalf("the re-review invocation: %v", err)
	}
	observed.calls = append(observed.calls,
		invocationRole{role: "re-review", session: sessionOf(t, agent, len(observed.calls))})

	// A review shape offered to the fixer, which is the one thing a caller can
	// put on an invocation that says "this is a review".
	_, err = fixer.Apply(t.Context(), agents.Invocation{
		Prompt: "review the change from inside the fixer's own session",
		Shape:  agents.ShapeReview,
		Review: demand,
		Dir:    dir,
	})
	observed.reviewRefusedAtTheFixer = errors.Is(err, agents.ErrReviewInFixerSession)

	separated := journey.Check[roles]{
		What: "P4: what a review and a fix round carry",
		Clauses: []journey.Clause[roles]{
			{
				States:  "no review or re-review invocation carried a session",
				Absence: true,
				// A wire on which no invocation ever carries a session would
				// show reviews carrying none whatever the roles did, so what
				// makes the reviews worth reading is that a session is
				// something this wire does carry, which the fix rounds show.
				Possible: func(r roles) error {
					for _, call := range r.calls {
						if call.role == "fix" && call.session != "" {
							return nil
						}
					}
					return errors.New("no fix round carried a session either, so nothing here shows a " +
						"session reaches the wire at all and the reviews carrying none says nothing")
				},
				Holds: func(r roles) error {
					for _, call := range r.calls {
						switch call.role {
						case "review", "re-review":
							if call.session != "" {
								return fmt.Errorf("the %s invocation carried the session %q, and a review runs in "+
									"a fresh context", call.role, call.session)
							}
						}
					}
					return nil
				},
			},
			{
				States: "the fixer's rounds carry a session, so the two roles are distinguishable at all",
				Holds: func(r roles) error {
					for _, call := range r.calls {
						if call.role == "fix" && call.session != "" {
							return nil
						}
					}
					return errors.New("no fix round carried a session, so nothing here shows the fixer keeps " +
						"one and the review being session-free says nothing")
				},
			},
			{
				States: "a review shape offered to the fixer is refused before a process starts",
				Holds: func(r roles) error {
					if !r.reviewRefusedAtTheFixer {
						return errors.New("the fixer accepted a review shape, so the session that prescribed a " +
							"fix can be seated as the certifier of it")
					}
					return nil
				},
			},
		},
		Counterfeits: []journey.Counterfeit[roles]{
			{Named: "the re-review resumed the session the fix rounds were made in",
				Break: func(r roles) roles {
					r.calls = slices.Clone(r.calls)
					for i, call := range r.calls {
						if call.role == "re-review" {
							r.calls[i].session = "standin-session-2"
						}
					}
					return r
				}},
			{Named: "the first review already carried a session", Break: func(r roles) roles {
				r.calls = slices.Clone(r.calls)
				r.calls[0].session = "standin-session-0"
				return r
			}},
			{Named: "no fix round kept a session, so nothing distinguishes the two roles",
				Break: func(r roles) roles {
					r.calls = slices.Clone(r.calls)
					for i := range r.calls {
						r.calls[i].session = ""
					}
					return r
				}},
			{Named: "the fixer accepted a review shape", Break: func(r roles) roles {
				r.reviewRefusedAtTheFixer = false
				return r
			}},
		},
	}
	if err := separated.Verify(observed); err != nil {
		t.Fatalf("%v", err)
	}
}

// sessionOf is the session the call at this position carried, read off what
// the stand-in process recorded rather than out of the adapter's own account.
func sessionOf(t *testing.T, agent *standin.Agent, at int) string {
	t.Helper()
	calls := agent.Calls()
	if at >= len(calls) {
		t.Fatalf("expected at least %d recorded call(s), the stand-in recorded %d", at+1, len(calls))
	}
	return calls[at].Session()
}
