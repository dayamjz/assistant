package service_test

import (
	"fmt"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/forge/forgetest"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/store"
)

// A stuck loop under standing consent parks on convergence instead of
// spending the requested-round limit (#320 item 5a).
//
// The run is the real service's, under standing consent, over the real
// review body and a scripted stand-in that is both the reviewer and the
// fixer. The fixer never changes anything: it commits nothing and says so in
// the same words every round. What differs between the two cases is the
// reviewer alone.
//
// In the first the reviewer reports the same ask finding every time, which the stage holds on rather than taking an automatic round for, so
// every requested round leaves the run's state exactly where the round
// before it left it. Consent answers the hold with a fix round each time,
// and the run parks converged once one round has been compared with the
// round before it: two rounds, one fewer than pipeline.RequestedFixRounds,
// which is the limit it would otherwise have spent before holding for a
// person.
//
// The second is the positive control: the reviewer words its finding
// differently every time, so every round's state differs from the one
// before it, and the same fixer, the same consent and the same bound do not
// park the run. It spends all pipeline.RequestedFixRounds rounds and then
// holds at the review for a person, because the round limit refuses consent
// a fourth. Without it the first case would hold just as well for a bound
// that parked every consented round.
func TestAConsentedRoundThatChangesNothingParksOnConvergence(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	// The fewest rounds a loop that changes nothing can spend: the first
	// round leaves the hold its result is seen by in the stage's next report,
	// so it is the second that is seen to change nothing, and the answer
	// that would start a third is where that is seen.
	const converged = 2
	if converged >= pipeline.RequestedFixRounds {
		t.Fatalf("convergence at round %d would not come before the limit of %d", converged, pipeline.RequestedFixRounds)
	}
	located := findings.Location{Path: "work.txt", Line: 1}
	finding := func(description string) findings.Finding {
		return findings.Finding{ID: "scope", Severity: findings.SeverityWarning, Action: findings.ActionAsk,
			Location: located, Description: description}
	}
	for _, tc := range []struct {
		name string
		// reviews is what the reviewer reports, one review per entry and
		// the last one for every review after.
		reviews []findings.Finding
		rounds  int
		parked  bool
	}{
		{
			name:    "the same finding every review",
			reviews: []findings.Finding{finding("this widens what the change was asked to do")},
			rounds:  converged,
			parked:  true,
		},
		{
			name: "a different finding every review",
			reviews: func() []findings.Finding {
				var out []findings.Finding
				for i := range pipeline.RequestedFixRounds + 2 {
					out = append(out, finding(fmt.Sprintf("this widens what the change was asked to do, reading %d", i+1)))
				}
				return out
			}(),
			rounds: pipeline.RequestedFixRounds,
			parked: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHome(t)
			subject := consentingSubject(t, h, allowsConsent)
			var steps []standin.Step
			for i, f := range tc.reviews {
				times := 1
				if i == len(tc.reviews)-1 {
					times = standin.Always
				}
				steps = append(steps, standin.Step{Match: standin.MatchReview(), Times: times,
					Reply: standin.Reviewed("the review's findings", f)})
			}
			steps = append(steps, standin.Step{Times: standin.Always, Reply: standin.Text("nothing needed changing")})
			agent := standin.New(t, standin.Script{Steps: steps})
			provider := forgetest.New(t, forgetest.Script{})

			withServiceOptions(t, consentOptions(t, h, agent, provider, nil), func(running serviceUnderTest) {
				run, err := startUnderConsent(t, running.client, subject)
				if err != nil {
					t.Fatalf("starting a run under standing consent: %v", err)
				}
				if got := len(fixRounds(agent)); got != tc.rounds {
					t.Errorf("the fixer ran %d rounds, want %d", got, tc.rounds)
				}
				park, parked := run.Record.Park.Get()
				if !tc.parked {
					if parked {
						t.Fatalf("a run whose every round changed the state parked: %+v (%s)", park, run.Reason)
					}
					holdingAt(t, run, pipeline.StageReview)
					return
				}
				if run.Outcome != machine.OutcomeFailed || !parked {
					t.Fatalf("the run ended %s (%s) with park %v: %s; want parked on convergence",
						run.Outcome, run.Record.Status, parked, run.Reason)
				}
				// It parks in front of the third round's fixer, which has
				// not run, rather than after it.
				if park.Bound != store.ParkConvergence || park.Stage != pipeline.StageReview.String() ||
					park.Node != pipeline.StageReview.RequestNode() {
					t.Fatalf("the run parked %+v, want convergence at the review stage's request node", park)
				}
			})
		})
	}
}
