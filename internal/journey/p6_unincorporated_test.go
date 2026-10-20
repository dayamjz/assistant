package journey_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/fixture"
	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/safety"
)

// unincorporated is what a run did whose branch on the remote already held a
// commit the run's own commit lacks when the run began, read off the answers
// the binary gave and off the remote it would have pushed to.
type unincorporated struct {
	// stops is the stage of every hold the run reached, and expected is where
	// stages.Holding says it stops before the push stage.
	stops    []string
	expected []string
	// colleague is the commit somebody else landed on the branch before the
	// run started, and so the commit the rebase stage observed it at.
	colleague string
	// forwarded is the head the lint stage last checked, which is the commit
	// the push stage would have forwarded.
	forwarded string
	// pushFindings is what the push stage held with, if it held.
	pushFindings []findings.Finding
	// after is where the branch stood on the remote once the run stopped.
	after string
}

// refusal is the push stage's refusal finding, if it held with one.
func (u unincorporated) refusal() (findings.Finding, error) {
	f, ok := heldWith(u.pushFindings, "push-refused")
	if !ok {
		return f, fmt.Errorf("the push stage held with %d finding(s) and none is push-refused; "+
			"the run stopped at %v", len(u.pushFindings), u.stops)
	}
	return f, nil
}

// withRefusal returns a copy of u whose refusal finding is changed, so a
// counterfeit never writes through the slice the real observation shares.
func (u unincorporated) withRefusal(change func(*findings.Finding)) unincorporated {
	u.pushFindings = slices.Clone(u.pushFindings)
	for i := range u.pushFindings {
		if u.pushFindings[i].ID == "push-refused" {
			change(&u.pushFindings[i])
		}
	}
	return u
}

// TestAPushOverARemoteHoldingCommitsTheRunLacksIsRefused drives issue #396
// through the binary: the branch under validation already holds a commit
// somebody else pushed when the run starts, the run's own commit does not
// contain it, and the push stage refuses rather than replacing the branch.
//
// The colleague's commit lands before the run starts, so the rebase stage
// observes the branch standing at it and the anchor describes the target
// correctly. That is what makes this P6's named failure rather than the lease
// walk's: a lease on that anchor always holds, so it protects nothing, and
// what refuses is internal/safety's rule that an anchored force may drop only
// commits the run held. Two shapes reach it, both from the issue: a working
// copy that committed on top of where the branch stood before the colleague
// pushed (diverged), and one that committed nothing (behind, so a push would
// move the branch backwards).
//
// The P6 cite covers this: the refusal is would-discard, it names as
// discarded exactly the colleague's commit, its anchor is the commit the run
// observed, which is the colleague's, and the remote still holds that commit.
// The positive control is TestARunTakesTheDeliveryPathToAPush, whose remote is
// an ancestor of the run's commit and which still pushes; the anchored force
// over commits the run did hold is driven at package reach, by
// internal/stages' push tests, because no walk here rewrites history the
// remote already holds.
func TestAPushOverARemoteHoldingCommitsTheRunLacksIsRefused(t *testing.T) {
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P6)
	requireStageListMatchesThePRD(t)

	for _, tc := range []struct {
		name   string
		commit bool
	}{
		{"diverged", true},
		{"behind", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observed := walkOverUnincorporated(t, tc.commit)
			if err := unincorporatedCheck().Verify(observed); err != nil {
				t.Fatalf("%v", err)
			}
		})
	}
}

// walkOverUnincorporated lands a colleague's commit on the remote's branch,
// optionally commits a change of the run's own beside it, starts a run and
// walks it to the push stage.
func walkOverUnincorporated(t *testing.T, commit bool) unincorporated {
	t.Helper()
	subject := ownSubject(t, true)
	colleague := subject.scenario.Paths["colleague"]
	if err := writeInto(colleague, "colleague.md", "A paragraph somebody else landed first.\n"); err != nil {
		t.Fatalf("%v", err)
	}
	subject.git(t, colleague, "add", "colleague.md")
	subject.git(t, colleague, "commit", "--quiet", "-m", "land a paragraph somebody else wrote")
	subject.git(t, colleague, "push", "--quiet", "origin", subject.scenario.Branch)
	observed := unincorporated{colleague: subject.git(t, colleague, "rev-parse", "HEAD")}
	if got := subject.remoteBranch(t); got != observed.colleague {
		t.Fatalf("the colleague's push left the remote at %s rather than at %s", got, observed.colleague)
	}
	if commit {
		subject.commit(t, "mine.md", "A change the colleague's commit is not under.\n",
			"add a change beside the colleague's")
	}

	agent := standin.New(t, standin.Script{Steps: []standin.Step{{
		Match: standin.MatchReview(), Times: standin.Always, Reply: standin.Reviewed("the change reads cleanly"),
	}}})
	j := open(t, subject.scenario, func(o *journey.Options) { o.Dir = subject.work; o.AgentArguments = agent.Arguments() })
	const command = "true"
	if err := j.WriteConfiguration(map[string]any{"commands": map[string]any{"test": command, "lint": command}}); err != nil {
		t.Fatalf("writing the home's configuration: %v", err)
	}
	succeeds(t, j.Command("init", "--default-branch", fixture.DefaultBranch))
	serve(t, j)

	skipped := []pipeline.Stage{pipeline.StagePR, pipeline.StageCI}
	observed.expected = expectedThenPush(configuredStops(command, command, skipped))
	run := startRun(t, j, "--skip", pipeline.StagePR.String()+","+pipeline.StageCI.String(),
		"--intent", "deliver a change to a branch somebody else already pushed to")
	observed.stops, run = walkToPush(t, j, run, func(string) {})
	if run.Decision != nil {
		observed.pushFindings = run.Decision.Findings
	}
	for _, v := range run.Stages {
		if v.Stage == pipeline.StageLint.String() && v.Report != nil {
			observed.forwarded = v.Report.Revision
		}
	}
	observed.after = subject.remoteBranch(t)
	return observed
}

// unincorporatedCheck is what the walk is held to, with the counterfeits that
// show each clause can fail.
func unincorporatedCheck() journey.Check[unincorporated] {
	return journey.Check[unincorporated]{
		What: "P6: a push over a branch holding a commit the run lacks",
		Clauses: []journey.Clause[unincorporated]{
			{
				States: "the run stopped at the stages stages.Holding gives, in order, then at the push stage",
				Holds: func(u unincorporated) error {
					if !slices.Equal(u.stops, u.expected) {
						return fmt.Errorf("the run stopped at %v and was expected at %v", u.stops, u.expected)
					}
					return nil
				},
			},
			{
				States: "the push stage held on a would-discard refusal",
				Holds: func(u unincorporated) error {
					f, err := u.refusal()
					if err != nil {
						return err
					}
					if got := lineAfter(f.Description, "reason: "); got != string(safety.ReasonWouldDiscard) {
						return fmt.Errorf("the refusal's reason is %q, want %q", got, safety.ReasonWouldDiscard)
					}
					return nil
				},
			},
			{
				States: "the refusal names as discarded exactly the colleague's commit",
				Holds: func(u unincorporated) error {
					f, err := u.refusal()
					if err != nil {
						return err
					}
					if got := lineAfter(f.Description, "discarded: "); got != u.colleague {
						return fmt.Errorf("the refusal names %q as discarded, and the colleague's commit is %s",
							got, u.colleague)
					}
					return nil
				},
			},
			{
				States: "the refusal's anchor is the commit the rebase stage observed, the colleague's",
				Holds: func(u unincorporated) error {
					f, err := u.refusal()
					if err != nil {
						return err
					}
					anchor := lineAfter(f.Description, "anchor: ")
					if _, commit, _ := strings.Cut(anchor, "="); commit != u.colleague {
						return fmt.Errorf("the refusal reports the anchor %q, and the branch stood at %s "+
							"when the run observed it", anchor, u.colleague)
					}
					return nil
				},
			},
			{
				States:  "the remote's branch still stands at the colleague's commit",
				Absence: true,
				Possible: func(u unincorporated) error {
					switch u.forwarded {
					case "":
						return errors.New("the run reports no head it would have forwarded, so whether a " +
							"push could have moved the remote is not established")
					case u.colleague:
						return fmt.Errorf("the head the run would have forwarded is %s, which the remote "+
							"already held, so a push would have left nothing to see", u.forwarded)
					}
					return nil
				},
				Holds: func(u unincorporated) error {
					if u.after != u.colleague {
						return fmt.Errorf("the remote's branch stands at %s and no longer at the colleague's "+
							"commit %s, which the run's commit %s does not contain", u.after, u.colleague, u.forwarded)
					}
					return nil
				},
			},
		},
		Counterfeits: []journey.Counterfeit[unincorporated]{
			{Named: "the push went through and the run never held there", Break: func(u unincorporated) unincorporated {
				u.stops = slices.Clone(u.stops[:len(u.stops)-1])
				return u
			}},
			{Named: "the push stage held on no completed review instead", Break: func(u unincorporated) unincorporated {
				return u.withRefusal(func(f *findings.Finding) { f.ID = "push-no-approval" })
			}},
			{Named: "the refusal reported a bare move", Break: func(u unincorporated) unincorporated {
				return u.withRefusal(func(f *findings.Finding) {
					f.Description = strings.Replace(f.Description,
						"reason: "+string(safety.ReasonWouldDiscard), "reason: "+string(safety.ReasonTargetMoved), 1)
				})
			}},
			{Named: "the refusal named no commit it would discard", Break: func(u unincorporated) unincorporated {
				return u.withRefusal(func(f *findings.Finding) {
					f.Description = strings.ReplaceAll(f.Description, "discarded: "+u.colleague+"\n", "")
				})
			}},
			{Named: "the refusal named the run's own commit as discarded", Break: func(u unincorporated) unincorporated {
				return u.withRefusal(func(f *findings.Finding) {
					f.Description = strings.ReplaceAll(f.Description, "discarded: "+u.colleague, "discarded: "+u.forwarded)
				})
			}},
			{Named: "the anchor named the commit the run would forward", Break: func(u unincorporated) unincorporated {
				return u.withRefusal(func(f *findings.Finding) {
					f.Description = strings.ReplaceAll(f.Description, "="+u.colleague, "="+u.forwarded)
				})
			}},
			{Named: "the run's commit replaced the colleague's", Break: func(u unincorporated) unincorporated {
				u.after = u.forwarded
				return u
			}},
		},
	}
}
