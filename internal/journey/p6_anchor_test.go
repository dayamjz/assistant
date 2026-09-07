package journey_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/fixture"
	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/safety"
	"github.com/dayamjz/assistant/internal/vcs"
)

// anchored is what the safety guard decided about an update whose target moved
// after the run observed it, and what it decided once the run had incorporated
// what moved.
type anchored struct {
	// anchorCommit is the commit the run's anchor names, which is what the
	// lease is taken on.
	anchorCommit string
	// observedCommit is where the branch stood on the remote when the run
	// observed it.
	observedCommit string
	// advancedCommit is where it stood after somebody else pushed.
	advancedCommit string
	// refused is whether the update was refused, and reason and message are
	// what it was refused with.
	refused bool
	reason  safety.Reason
	message string
	// discarded is the commits the refusal says the update would drop.
	discarded []string
	// secondAnchorCommit is the anchor the second attempt was taken on, which
	// is where the branch stood once the colleague's commit had landed.
	secondAnchorCommit string
	// allowedAfterIncorporating is whether the same decision allowed the
	// update once the run had rebased onto what it found, and asKind is what
	// it allowed it as.
	allowedAfterIncorporating bool
	asKind                    safety.Kind
}

// TestAnUpdateIsAnchoredToWhatTheRunObservedRatherThanToAFreshRead drives the
// first two of PRD section 13's tests for principle P6, at package reach.
//
// The binary cannot reach this. No stage body pushes, so no run observes a
// remote and none proposes an update, which means there is no run to drive the
// refusal out of. What is driven instead is internal/safety over
// internal/vcs's own reads of the subject repository, with the out-of-band
// advance planted by the route internal/fixture states: a real push from a
// real second clone, made after the observation and before the decision.
//
// The assertion on the anchor value is the point of the second half. Anchoring
// the lease to the tip read a moment before pushing always succeeds and
// therefore protects nothing, and the failure is invisible behaviourally,
// which is why the PRD asks for the value rather than for the outcome.
func TestAnUpdateIsAnchoredToWhatTheRunObservedRatherThanToAFreshRead(t *testing.T) {
	principles.Cite(t, principles.P6)

	scenario := claim(t, fixture.ScenarioRemoteAdvanced)
	condition, err := journey.Condition("refusal-remote-advanced-out-of-band")
	if err != nil {
		t.Fatalf("%v", err)
	}
	ctx := t.Context()
	repository, err := vcs.OpenWorktree(ctx, scenario.WorkingCopy, vcs.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("opening the subject working copy: %v", err)
	}
	guard := safety.New(subjectGit{Repository: repository, scenario: scenario})
	target := safety.Target{Remote: "origin", Ref: "refs/heads/" + scenario.Branch}

	observed := anchored{observedCommit: remoteBranch(t, scenario)}
	anchor, err := guard.Observe(ctx, target)
	if err != nil {
		t.Fatalf("observing %s: %v", target, err)
	}
	observed.anchorCommit = anchor.State().Commit

	// The plant goes here and nowhere else: after the observation and before
	// the decision. internal/fixture states why an earlier one exercises
	// nothing, and it verifies for itself that the remote actually moved.
	if _, err := fixture.AdvanceRemoteOutOfBand(scenario); err != nil {
		t.Fatalf("landing the colleague's commit: %v", err)
	}
	observed.advancedCommit = remoteBranch(t, scenario)

	// The run's own work: a commit on the branch as the run last saw it, which
	// is a history the advanced branch is not contained in.
	proposed := commitOnBranch(t, scenario, "fixes.md",
		"A line the pipeline wrote while somebody else was pushing.\n", "apply the review's fix")

	// The decision's reachability questions are answered from the local
	// repository, so the commit somebody else landed has to be here for the
	// guard to say anything about it. internal/safety states that an
	// unanswerable question becomes a refusal rather than an allow, which is
	// the safe direction and a different refusal from the one planted here, so
	// the objects are fetched first exactly as a run preparing a push would.
	// Nothing about the anchor changes: it was taken before the advance.
	gitIn(t, scenario, scenario.WorkingCopy, "fetch", "--quiet", "origin", scenario.Branch)

	_, err = guard.Decide(ctx, safety.Update{Target: target, Proposed: proposed, Anchor: anchor})
	var refusal *safety.Refusal
	if errors.As(err, &refusal) {
		observed.refused = true
		observed.reason = refusal.Reason
		observed.message = refusal.Error()
		observed.discarded = refusal.Discarded
	} else if err != nil {
		observed.message = err.Error()
	}

	// The action the refusal names has to get the reader out of the state it
	// put them in, which internal/fixture records as part of the expectation
	// rather than as a remark about it. It is a second attempt rather than the
	// first one retried: the run observes where the branch stands now, then
	// does its work against that, then decides. Reusing the first anchor is
	// refused for target-moved and would be, because an anchor that does not
	// describe the target protects nothing.
	second, err := guard.Observe(ctx, target)
	if err != nil {
		t.Fatalf("observing %s for the second attempt: %v", target, err)
	}
	observed.secondAnchorCommit = second.State().Commit
	gitIn(t, scenario, scenario.WorkingCopy, "fetch", "--quiet", "origin", scenario.Branch)
	gitIn(t, scenario, scenario.WorkingCopy, "rebase", "--quiet", "FETCH_HEAD")
	incorporated := gitIn(t, scenario, scenario.WorkingCopy, "rev-parse", "HEAD")
	decision, err := guard.Decide(ctx, safety.Update{Target: target, Proposed: incorporated, Anchor: second})
	if err != nil {
		observed.message += "\nafter incorporating: " + err.Error()
	}
	observed.allowedAfterIncorporating = decision.Allowed()
	observed.asKind = decision.Kind()

	refuses := journey.Check[anchored]{
		What: "an update whose target moved out of band after the run observed it is refused for " +
			"would-discard, the refusal names the commits it would drop, the lease is anchored to what " +
			"the run observed rather than to a fresh read, and incorporating what moved makes the same " +
			"decision allow a fast-forward",
		Clauses: []journey.Clause[anchored]{
			{
				States: "the anchor names where the branch stood when the run observed it",
				Holds: func(a anchored) error {
					if a.anchorCommit != a.observedCommit {
						return fmt.Errorf("the anchor names %s and the branch stood at %s when the run observed it",
							a.anchorCommit, a.observedCommit)
					}
					return nil
				},
			},
			{
				States: "the anchor is not where the branch stands now, which a fresh read would have given",
				Holds: func(a anchored) error {
					if a.anchorCommit == a.advancedCommit {
						return fmt.Errorf("the anchor names %s, which is where the branch stands now; an anchor "+
							"taken from a fresh read always succeeds and therefore protects nothing", a.anchorCommit)
					}
					return nil
				},
			},
			{
				States: "the update was refused",
				Holds: func(a anchored) error {
					if !a.refused {
						return fmt.Errorf("the update was not refused; it answered %q", a.message)
					}
					return nil
				},
			},
			{
				States: "the refusal reason is the one the condition records",
				Holds: func(a anchored) error {
					if a.reason != safety.ReasonWouldDiscard {
						return fmt.Errorf("the refusal reason is %q and the condition expects %q",
							a.reason, safety.ReasonWouldDiscard)
					}
					return nil
				},
			},
			{
				States: "the refusal says what the condition requires it to say",
				Holds: func(a anchored) error {
					if missing := journey.Carries(a.message, condition.Expect.MessageContains); len(missing) > 0 {
						return fmt.Errorf("the refusal does not say %q; it said:\n%s", missing, a.message)
					}
					return nil
				},
			},
			{
				States: "the refusal names the commits the update would drop",
				Holds: func(a anchored) error {
					if len(a.discarded) == 0 {
						return errors.New("the refusal names no commit it would drop, which is what an operator " +
							"needs to decide whether to incorporate them")
					}
					return nil
				},
			},
			{
				States: "the second attempt anchored on where the branch stands now",
				Holds: func(a anchored) error {
					if a.secondAnchorCommit != a.advancedCommit {
						return fmt.Errorf("the second attempt anchored on %s and the branch stood at %s",
							a.secondAnchorCommit, a.advancedCommit)
					}
					return nil
				},
			},
			{
				States: "incorporating what moved makes the same decision allow the update",
				Holds: func(a anchored) error {
					if !a.allowedAfterIncorporating {
						return fmt.Errorf("incorporating what moved did not make the update allowable, so the "+
							"action the refusal names leads to a second refusal: %s", a.message)
					}
					return nil
				},
			},
			{
				States: "the incorporated update is allowed as a fast-forward rather than as a force",
				Holds: func(a anchored) error {
					if a.asKind != safety.KindFastForward {
						return fmt.Errorf("the incorporated update was allowed as %q rather than as a fast-forward",
							a.asKind)
					}
					return nil
				},
			},
		},
		Counterfeits: []journey.Counterfeit[anchored]{
			{Named: "the anchor was taken from a fresh read just before the update", Break: func(a anchored) anchored {
				a.anchorCommit = a.advancedCommit
				return a
			}},
			// The empty commit is what the read behind this anchor answers with
			// when the remote advertises no such ref, and it is the only value
			// besides an advertised object that internal/safety can put here.
			// A run of zeros would show the clause failing against something
			// that read cannot produce.
			{Named: "the anchor names no commit at all, as a read of an absent ref would leave it",
				Break: func(a anchored) anchored {
					a.anchorCommit = ""
					return a
				}},
			{Named: "the update was allowed over the commit somebody else landed", Break: func(a anchored) anchored {
				a.refused = false
				return a
			}},
			{Named: "the refusal reported a bare move rather than what it would discard",
				Break: func(a anchored) anchored {
					a.reason = safety.ReasonTargetMoved
					return a
				}},
			{Named: "the refusal never names the commits it would drop", Break: func(a anchored) anchored {
				a.discarded = nil
				return a
			}},
			{Named: "the refusal's message says none of what the condition requires",
				Break: func(a anchored) anchored {
					a.message = "safety: refused"
					return a
				}},
			{Named: "the second attempt anchored on the state the first one had already found stale",
				Break: func(a anchored) anchored {
					a.secondAnchorCommit = a.observedCommit
					return a
				}},
			{Named: "incorporating what moved still leaves the update refused",
				Break: func(a anchored) anchored {
					a.allowedAfterIncorporating = false
					return a
				}},
			{Named: "the incorporated update was allowed as a force rather than as a fast-forward",
				Break: func(a anchored) anchored {
					a.asKind = safety.KindAnchoredForce
					return a
				}},
		},
	}
	if err := refuses.Verify(observed); err != nil {
		t.Fatalf("%v", err)
	}
}

// subjectGit is the git mechanism the guard decides on.
//
// Three of its four methods are internal/vcs's own, so the reads a decision is
// made against are the product's. The fourth is not implemented there:
// internal/safety states that CommitsNotIn is a typed operation internal/vcs
// still owes, and that adding it is not that package's change to make. Until
// it exists this answers it the way the interface spells it, with the fixture's
// own git under the scenario's own isolation, which is the same exception
// internal/fixture takes for its plants and for the same reason.
type subjectGit struct {
	*vcs.Repository
	scenario fixture.Scenario
}

// CommitsNotIn returns the commits reachable from have and not from
// incorporated, most recent first, which is git rev-list incorporated..have.
func (g subjectGit) CommitsNotIn(ctx context.Context, have, incorporated string) ([]string, error) {
	_ = ctx
	out, err := journey.Git(g.scenario, g.scenario.WorkingCopy, "rev-list", incorporated+".."+have)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// commitOnBranch writes a file in the subject's working copy and commits it,
// returning the commit.
func commitOnBranch(t *testing.T, scenario fixture.Scenario, name, content, message string) string {
	t.Helper()
	if err := writeInto(scenario.WorkingCopy, name, content); err != nil {
		t.Fatalf("%v", err)
	}
	gitIn(t, scenario, scenario.WorkingCopy, "add", "-A")
	gitIn(t, scenario, scenario.WorkingCopy, "commit", "--quiet", "-m", message)
	return gitIn(t, scenario, scenario.WorkingCopy, "rev-parse", "HEAD")
}
