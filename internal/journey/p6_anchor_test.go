package journey_test

import (
	"errors"
	"fmt"
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
// The binary cannot reach this. The push stage's body exists and no run of
// this build gets an update out of it, because it requires the observation a
// rebase body would record and refuses every run for its absence, so there is
// no run to drive the refusal out of. What is driven instead is
// internal/safety over internal/vcs's own reads of the subject repository,
// with the out-of-band advance planted by the route internal/fixture states: a
// real push from a real second clone, made after the observation and before
// the decision.
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
	guard := safety.New(repository)
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
		What: "P6: an update whose target moved under the run",
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

// firstPushed is what the safety guard decided about a branch's first push,
// and what performing that decision left on the remote.
type firstPushed struct {
	// observedAbsent is whether the anchor records the target absent, which is
	// the state the decision leases the creation on.
	observedAbsent bool
	// allowed and kind are the decision, and message is its own render, which
	// is the text the condition's substrings are held to.
	allowed bool
	kind    safety.Kind
	message string
	// leaseExists is what the decision's anchor says the lease compares
	// against: a commit when true, absence when false.
	leaseExists bool
	// proposed is the commit the run wants the branch created at, pushErr is
	// what performing the permitted update reported, and remoteAfter is where
	// the branch stood on the remote afterwards.
	proposed    string
	pushErr     error
	remoteAfter string
}

// TestAFirstPushIsAllowedAsACreationAnchoredOnAbsence drives the negative case
// that gives P6's refusal its meaning, at package reach: a branch nobody has
// published, whose observation records absence and whose update the same
// decision path allows as a creation leased on that absence.
//
// The binary cannot reach this for the reason the test above states. What is
// driven is the whole mechanism a run's push rests on: internal/safety over
// internal/vcs's own reads for the decision, and internal/vcs's one leased
// push for the update the decision permits, so the branch the remote ends up
// holding was created under the decision's own lease rather than by test
// plumbing.
func TestAFirstPushIsAllowedAsACreationAnchoredOnAbsence(t *testing.T) {
	principles.Cite(t, principles.P6)

	scenario := claim(t, fixture.ScenarioFirstPush)
	condition, err := journey.Condition("allowed-first-push-of-a-new-branch")
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(condition.Expect.MessageContains) == 0 {
		t.Fatal("the condition records no expected message substrings, so the message clause would " +
			"hold the decision to nothing")
	}
	ctx := t.Context()
	repository, err := vcs.OpenWorktree(ctx, scenario.WorkingCopy, vcs.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("opening the subject working copy: %v", err)
	}
	guard := safety.New(repository)
	target := safety.Target{Remote: "origin", Ref: "refs/heads/" + scenario.Branch}

	anchor, err := guard.Observe(ctx, target)
	if err != nil {
		t.Fatalf("observing %s: %v", target, err)
	}
	observed := firstPushed{observedAbsent: !anchor.State().Exists}

	// The run's own work: a commit on the branch after the observation, so
	// what is proposed is not the commit the branch happened to be born with.
	observed.proposed = commitOnBranch(t, scenario, "docs/second-change.md",
		"A line the pipeline wrote before the branch was ever pushed.\n", "extend the branch's first change")

	decision, err := guard.Decide(ctx, safety.Update{Target: target, Proposed: observed.proposed, Anchor: anchor})
	if err != nil {
		observed.message = err.Error()
	} else {
		observed.allowed = decision.Allowed()
		observed.kind = decision.Kind()
		observed.message = decision.String()
		state := decision.Anchor().State()
		observed.leaseExists = state.Exists
		observed.pushErr = repository.Push(ctx, vcs.PushSpec{
			Remote: target.Remote,
			Ref:    target.Ref,
			Commit: decision.Proposed(),
			Lease:  vcs.Lease{Exists: state.Exists, Commit: state.Commit},
		})
		if observed.pushErr == nil {
			observed.remoteAfter = remoteBranch(t, scenario)
		}
	}

	creates := journey.Check[firstPushed]{
		What: "P6: a branch's first push",
		Clauses: []journey.Clause[firstPushed]{
			{
				States: "the anchor records the target absent, which is the state the creation leases on",
				Holds: func(f firstPushed) error {
					if !f.observedAbsent {
						return errors.New("the anchor records the target at a commit, so what follows is an " +
							"ordinary update and nothing here is about a first push")
					}
					return nil
				},
			},
			{
				States: "the update was allowed",
				Holds: func(f firstPushed) error {
					if !f.allowed {
						return fmt.Errorf("the update was not allowed; it answered %q, and the condition "+
							"records that a refusal here is the wrong answer whatever it says", f.message)
					}
					return nil
				},
			},
			{
				States: "the update was allowed as a creation",
				Holds: func(f firstPushed) error {
					if f.kind != safety.KindCreate {
						return fmt.Errorf("the update was allowed as %q rather than as a creation", f.kind)
					}
					return nil
				},
			},
			{
				States: "the decision says what the condition requires it to say",
				Holds: func(f firstPushed) error {
					if missing := journey.Carries(f.message, condition.Expect.MessageContains); len(missing) > 0 {
						return fmt.Errorf("the decision does not say %q; it said:\n%s", missing, f.message)
					}
					return nil
				},
			},
			{
				States: "the lease the decision hands back compares against absence",
				Holds: func(f firstPushed) error {
					if f.leaseExists {
						return errors.New("the lease compares against a commit, and the read behind this " +
							"anchor found none: a creation leased on a commit is a different update than " +
							"the one that was allowed")
					}
					return nil
				},
			},
			{
				States: "performing the permitted update created the branch at the proposed commit",
				Holds: func(f firstPushed) error {
					if f.pushErr != nil {
						return fmt.Errorf("the permitted update failed under its own lease: %w", f.pushErr)
					}
					if f.remoteAfter != f.proposed {
						return fmt.Errorf("the branch on the remote stands at %q and the decision permitted %s",
							f.remoteAfter, f.proposed)
					}
					return nil
				},
			},
		},
		Counterfeits: []journey.Counterfeit[firstPushed]{
			{Named: "the run observed the target at a commit rather than absent", Break: func(f firstPushed) firstPushed {
				f.observedAbsent = false
				return f
			}},
			{Named: "the update was refused", Break: func(f firstPushed) firstPushed {
				f.allowed = false
				return f
			}},
			{Named: "the update was allowed as a fast-forward over history the target does not hold",
				Break: func(f firstPushed) firstPushed {
					f.kind = safety.KindFastForward
					return f
				}},
			// "no decision" is the zero Decision's own render, which is the
			// shape a caller that ignored the error beside it would hold.
			{Named: "the decision's render says none of what the condition requires",
				Break: func(f firstPushed) firstPushed {
					f.message = "no decision"
					return f
				}},
			{Named: "the lease compares against a commit rather than against absence",
				Break: func(f firstPushed) firstPushed {
					f.leaseExists = true
					return f
				}},
			{Named: "the permitted push was rejected under its lease", Break: func(f firstPushed) firstPushed {
				f.pushErr = errors.New("the remote refused the update")
				return f
			}},
			{Named: "the branch landed somewhere other than the proposed commit", Break: func(f firstPushed) firstPushed {
				f.remoteAfter = f.proposed + "0"
				return f
			}},
		},
	}
	if err := creates.Verify(observed); err != nil {
		t.Fatalf("%v", err)
	}
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
