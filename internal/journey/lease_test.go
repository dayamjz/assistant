package journey_test

import (
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/fixture"
	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/safety"
	"github.com/dayamjz/assistant/internal/stages"
)

// ownedSubject is a remote of one test's own, cut from the walkable scenario's,
// with a working copy cloned from it and, when asked for, a second clone
// standing where internal/fixture's remote-advanced scenario keeps its
// colleague.
type ownedSubject struct {
	// scenario is the walkable scenario with its origin, and its colleague
	// path where there is one, replaced by this test's own.
	scenario fixture.Scenario
	// work is the working copy a run is started from.
	work string
}

// ownSubject copies the walkable scenario's remote for one test, so a push or
// an advance moves nothing another test reads, and clones a working copy of
// it. withColleague also clones the second working copy
// fixture.AdvanceRemoteOutOfBand pushes from, made by the same two git commands
// internal/fixture's builder runs for the remote-advanced scenario: a clone of
// the origin, checked out on the branch under validation.
//
// The walkable scenario is the subject rather than the remote-advanced one
// because a run has to reach the push stage, and that scenario's trusted
// document configures commands.test over a branch planted to fail it, so a run
// over it is parked at the test stage first. The walks that use this say so in
// their gaps.
func ownSubject(t *testing.T, withColleague bool) ownedSubject {
	t.Helper()
	from := scenarioNamed(t, fixture.ScenarioWalkable)
	own := from
	own.Paths = maps.Clone(from.Paths)
	own.Origin = filepath.Join(t.TempDir(), "remote.git")
	if _, err := journey.Git(from, filepath.Dir(own.Origin), "clone", "--quiet", "--bare", from.Origin, own.Origin); err != nil {
		t.Fatalf("copying the remote: %v", err)
	}
	work, err := journey.Clone(own, filepath.Join(t.TempDir(), "work"))
	if err != nil {
		t.Fatalf("cloning: %v", err)
	}
	if withColleague {
		colleague, err := journey.Clone(own, filepath.Join(t.TempDir(), "colleague"))
		if err != nil {
			t.Fatalf("cloning the second working copy: %v", err)
		}
		own.Paths["colleague"] = colleague
	}
	return ownedSubject{scenario: own, work: work}
}

// git runs one git command against the owned subject and fails the test on an
// error.
func (s ownedSubject) git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := journey.Git(s.scenario, dir, args...)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return out
}

// remoteBranch is where the branch under validation stands on the owned
// remote, read through this harness's own git.
func (s ownedSubject) remoteBranch(t *testing.T) string {
	t.Helper()
	return s.git(t, s.scenario.Origin, "rev-parse", "refs/heads/"+s.scenario.Branch)
}

// commit writes one file into the working copy and commits it, returning the
// commit: a change the remote has not seen, so a push has something to move.
func (s ownedSubject) commit(t *testing.T, name, content, message string) string {
	t.Helper()
	if err := writeInto(s.work, name, content); err != nil {
		t.Fatalf("%v", err)
	}
	s.git(t, s.work, "add", name)
	s.git(t, s.work, "commit", "--quiet", "-m", message)
	return s.git(t, s.work, "rev-parse", "HEAD")
}

// configuredStops is where stages.Holding says a run under the defaults plus
// the given commands stops, less the stages the run skips. It is derived, so a
// body landing moves it without the walks resting on it.
func configuredStops(test, lint string, skipped []pipeline.Stage) []pipeline.Stage {
	resolved := config.Defaults()
	resolved.Commands.Test, resolved.Commands.Lint = test, lint
	var stops []pipeline.Stage
	for _, stage := range stages.Holding(resolved) {
		if !slices.Contains(skipped, stage) {
			stops = append(stops, stage)
		}
	}
	return stops
}

// walkToPush answers every hold with approval until the run stops at the push
// stage or stops holding, and returns the stage of every hold it reached in
// order with the last answer. before is called at each hold before it is
// answered, which is where a walk plants what has to happen partway through.
//
// The loop is bounded, so a run that holds forever fails here rather than
// hanging the suite. Nothing in it waits on time: each answer returns once the
// run has reached its next stop.
func walkToPush(t *testing.T, j *journey.Journey, run machine.Run, before func(stage string)) ([]string, machine.Run) {
	t.Helper()
	var stops []string
	for range 20 {
		if run.Outcome != machine.OutcomeDecision || run.Decision == nil {
			return stops, run
		}
		stops = append(stops, run.Decision.Stage)
		if run.Decision.Stage == pipeline.StagePush.String() {
			return stops, run
		}
		before(run.Decision.Stage)
		run = decodeRun(t, j.Command("--answer", "approved"))
	}
	t.Fatalf("the run was answered 20 times and never reached the push stage; it stands at %s", run.Position)
	return nil, run
}

// expectedThenPush is the stops a walk that ends at the push stage's hold
// expects: the configured ones, then the push stage.
func expectedThenPush(expected []pipeline.Stage) []string {
	var want []string
	for _, stage := range expected {
		want = append(want, stage.String())
	}
	return append(want, pipeline.StagePush.String())
}

// heldWith reports the hold's findings that carry the given identifier.
func heldWith(list []findings.Finding, id string) (findings.Finding, bool) {
	for _, f := range list {
		if f.ID == id {
			return f, true
		}
	}
	return findings.Finding{}, false
}

// lineAfter is the rest of the first line of text that starts with prefix,
// which is how the push stage's refusal lays out its reason and its anchor.
func lineAfter(text, prefix string) string {
	for line := range strings.Lines(text) {
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// leased is what a run whose push target moved under it did, read off the
// answers the binary gave and off the remote it would have pushed to.
type leased struct {
	// expectedStops is where stages.Holding says the run stops, less the
	// stages it skipped; the run is expected to stop there and then at push.
	expectedStops []pipeline.Stage
	// stops is the stage of every hold the run reached.
	stops []string
	// observed is where the branch stood on the remote before the run began,
	// which is what the rebase stage observed: nothing else moved it.
	observed string
	// advanced is the commit somebody else landed while the run was held.
	advanced string
	// forwarded is the commit the run would have forwarded: the head the
	// lint stage last checked, which is the last stage before push that can
	// move it. It is read off the answer rather than taken to be the commit
	// the working copy held, because the rebase stage may rewrite that.
	forwarded string
	// pushFindings is what the push stage held with.
	pushFindings []findings.Finding
	// after is where the branch stood on the remote once the push stage had
	// decided.
	after string
}

// refusal is the push stage's refusal finding, if it held with one.
func (l leased) refusal() (findings.Finding, error) {
	f, ok := heldWith(l.pushFindings, "push-refused")
	if !ok {
		return f, fmt.Errorf("the push stage held with %d finding(s) and none is push-refused", len(l.pushFindings))
	}
	return f, nil
}

// withRefusal returns a copy of l whose refusal finding is changed, so a
// counterfeit never writes through the slice the real observation shares.
func (l leased) withRefusal(change func(*findings.Finding)) leased {
	l.pushFindings = slices.Clone(l.pushFindings)
	for i := range l.pushFindings {
		if l.pushFindings[i].ID == "push-refused" {
			change(&l.pushFindings[i])
		}
	}
	return l
}

// TestAPushOverARemoteThatMovedUnderTheRunIsRefused drives PRD principle P6's
// lease through the binary: the remote's branch is advanced out of band while
// the run is held between the rebase stage's observation and the push stage's
// decision, and the push stage refuses rather than forwarding.
//
// The advance is internal/fixture's AdvanceRemoteOutOfBand, a real push from a
// real second clone, made at the first hold stages.Holding gives between the
// rebase stage and the push stage; the hold is where a person is being asked
// something, so the run is genuinely partway through and the plant lands after
// the observation and before the decision, which is the placement the
// condition requires. The reviewer is the stand-in scripted to answer a clean
// review, so the push stage has a completed review and reaches its decision.
//
// The P6 cite covers this: the anchor the push stage's refusal reports is the
// commit the run observed rather than where the branch stood at the decision,
// the refusal is would-discard, and the remote still holds the commit somebody
// else landed. It does not cover the remedy the condition records, fetching
// and rebasing onto the advanced branch: no surface here re-runs a run onto
// its new upstream, and TestAnUpdateIsAnchoredToWhatTheRunObservedRatherThanToAFreshRead
// drives that half at package reach. Nor does it cover the commits the
// refusal would drop: the push stage relays the refusal's reason, detail and
// anchor, but not Refusal.Discarded, so a person at this hold is not told
// which commit was landed, and that is asserted at package reach only.
//
// The subject is the walkable scenario's remote rather than the
// remote-advanced scenario's, for the reason ownSubject gives, with the
// second clone made the way internal/fixture makes that scenario's.
func TestAPushOverARemoteThatMovedUnderTheRunIsRefused(t *testing.T) {
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P6)
	requireStageListMatchesThePRD(t)

	condition, err := journey.Condition("refusal-remote-advanced-out-of-band")
	if err != nil {
		t.Fatalf("%v", err)
	}
	// The substrings of the condition's message the push stage relays: it
	// reports the refusal's detail, not its whole message, so the rest of the
	// list is held at package reach. Taken out of the condition rather than
	// spelled here, so a change to what it records changes what is held.
	var relayed []string
	for _, want := range condition.Expect.MessageContains {
		if want == "holds commits that" || want == "does not contain" {
			relayed = append(relayed, want)
		}
	}
	if len(relayed) != 2 {
		t.Fatalf("the condition no longer records the detail substrings this walk holds the push "+
			"stage's refusal to; it records %q", condition.Expect.MessageContains)
	}

	subject := ownSubject(t, true)
	observed := leased{observed: subject.remoteBranch(t)}
	subject.commit(t, "lease.md", "A change the remote has not seen.\n",
		"add a change the remote has not seen")

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
	observed.expectedStops = configuredStops(command, command, skipped)
	plantAt := ""
	for _, stage := range observed.expectedStops {
		if stage > pipeline.StageRebase && stage < pipeline.StagePush {
			plantAt = stage.String()
			break
		}
	}
	if plantAt == "" {
		t.Fatalf("stages.Holding gives no hold between the rebase stage's observation and the push "+
			"stage's decision under this configuration (%v), so there is nowhere partway through the run "+
			"to land the advance", observed.expectedStops)
	}

	run := startRun(t, j, "--skip", pipeline.StagePR.String()+","+pipeline.StageCI.String(),
		"--intent", "deliver a change while somebody else pushes to the branch")
	var stops []string
	stops, run = walkToPush(t, j, run, func(stage string) {
		if stage != plantAt || observed.advanced != "" {
			return
		}
		landed, err := fixture.AdvanceRemoteOutOfBand(subject.scenario)
		if err != nil {
			t.Fatalf("landing the colleague's commit: %v", err)
		}
		observed.advanced = landed
	})
	observed.stops = stops
	if run.Decision != nil {
		observed.pushFindings = run.Decision.Findings
	}
	for _, v := range run.Stages {
		if v.Stage == pipeline.StageLint.String() && v.Report != nil {
			observed.forwarded = v.Report.Revision
		}
	}
	observed.after = subject.remoteBranch(t)
	if observed.advanced == "" {
		t.Fatalf("the run never held at the %s stage, so the advance was never landed; it stopped at %v",
			plantAt, observed.stops)
	}

	check := journey.Check[leased]{
		What: "P6: a push whose target moved after the run observed it",
		Clauses: []journey.Clause[leased]{
			{
				States: "the run stopped at the stages stages.Holding gives, in order, then at the push stage, and nowhere else",
				Holds: func(l leased) error {
					if want := expectedThenPush(l.expectedStops); !slices.Equal(l.stops, want) {
						return fmt.Errorf("the run stopped at %v and was expected at %v", l.stops, want)
					}
					return nil
				},
			},
			{
				States: "the push stage held on a refusal",
				Holds: func(l leased) error {
					_, err := l.refusal()
					return err
				},
			},
			{
				States: "the refusal's reason is the one the condition records",
				Holds: func(l leased) error {
					f, err := l.refusal()
					if err != nil {
						return err
					}
					if got := lineAfter(f.Description, "reason: "); got != string(safety.ReasonWouldDiscard) {
						return fmt.Errorf("the refusal's reason is %q and the condition expects %q",
							got, safety.ReasonWouldDiscard)
					}
					return nil
				},
			},
			{
				States: "the refusal carries the detail the condition records, as far as the push stage relays it",
				Holds: func(l leased) error {
					f, err := l.refusal()
					if err != nil {
						return err
					}
					if missing := journey.Carries(f.Description, relayed); len(missing) > 0 {
						return fmt.Errorf("the refusal does not say %q; it said:\n%s", missing, f.Description)
					}
					return nil
				},
			},
			{
				States: "the refusal's anchor is the commit the run observed, not where the branch stood at the decision",
				Holds: func(l leased) error {
					f, err := l.refusal()
					if err != nil {
						return err
					}
					anchor := lineAfter(f.Description, "anchor: ")
					_, commit, _ := strings.Cut(anchor, "=")
					if commit != l.observed {
						return fmt.Errorf("the refusal reports the anchor %q, and the run observed the branch at %s; "+
							"an anchor read at the decision would name %s and protect nothing", anchor, l.observed, l.advanced)
					}
					return nil
				},
			},
			{
				States:  "the remote's branch still stands at the commit somebody else landed",
				Absence: true,
				Possible: func(l leased) error {
					switch l.forwarded {
					case "":
						return errors.New("the run reports no head it would have forwarded, so whether a " +
							"push could have moved the remote is not established")
					case l.advanced, l.observed:
						return fmt.Errorf("the head the run would have forwarded is %s, which the remote "+
							"already held, so a push would have left nothing to see", l.forwarded)
					}
					return nil
				},
				Holds: func(l leased) error {
					if l.after != l.advanced {
						return fmt.Errorf("the remote's branch stands at %s, and the commit somebody else landed "+
							"while the run was held is %s", l.after, l.advanced)
					}
					return nil
				},
			},
		},
		Counterfeits: []journey.Counterfeit[leased]{
			{Named: "the push went through and the run never held there", Break: func(l leased) leased {
				l.stops = slices.Clone(l.stops[:len(l.stops)-1])
				return l
			}},
			{Named: "the push stage held on no completed review instead", Break: func(l leased) leased {
				return l.withRefusal(func(f *findings.Finding) { f.ID = "push-no-approval" })
			}},
			{Named: "the refusal reported a bare move", Break: func(l leased) leased {
				return l.withRefusal(func(f *findings.Finding) {
					f.Description = strings.Replace(f.Description,
						"reason: "+string(safety.ReasonWouldDiscard), "reason: "+string(safety.ReasonTargetMoved), 1)
				})
			}},
			{Named: "the refusal's detail is the bare move's sentence", Break: func(l leased) leased {
				return l.withRefusal(func(f *findings.Finding) {
					for _, want := range relayed {
						f.Description = strings.ReplaceAll(f.Description, want, "now stands at")
					}
				})
			}},
			{Named: "the anchor was read at the decision", Break: func(l leased) leased {
				return l.withRefusal(func(f *findings.Finding) {
					f.Description = strings.ReplaceAll(f.Description, "="+l.observed, "="+l.advanced)
				})
			}},
			{Named: "the run's commit was forced over the one somebody else landed", Break: func(l leased) leased {
				l.after = l.forwarded
				return l
			}},
			{Named: "the remote was rewound to where the run observed it", Break: func(l leased) leased {
				l.after = l.observed
				return l
			}},
		},
	}
	if err := check.Verify(observed); err != nil {
		t.Fatalf("%v", err)
	}
}
