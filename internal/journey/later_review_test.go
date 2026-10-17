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
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
)

// laterReviewLandmark is a sentence only the push stage's review of later
// commits carries, which is how the script tells that review apart from the
// review stage's own. It is quoted from the prompt rather than shared with
// it, so a change to that wording leaves the review unscripted and fails this
// walk loudly instead of letting the script answer the wrong review.
const laterReviewLandmark = "no review has read them"

// fixerLandmark opens every fix round's prompt.
const fixerLandmark = "You are fixing a change"

// lintTrailer is the line the configured lint command writes into the
// change's file the first time it runs, and looks for every time after.
const lintTrailer = "trailer the lint command writes"

// reviewedLater is what a run whose fix round committed after its review did,
// read off the answers the binary gave, off what the stand-in recorded of the
// invocations it answered, and off the remote.
type reviewedLater struct {
	// expectedStops is where stages.Holding says the run stops, less the
	// stages it skipped; the run is expected to stop there and then at push.
	expectedStops []pipeline.Stage
	// stops is the stage of every hold the run reached.
	stops []string
	// stages is the last answer's view of every stage.
	stages []machine.Stage
	// pushFindings is what the push stage held with.
	pushFindings []findings.Finding
	// reviews, later and fixes count the invocations the review stage's
	// review, the push stage's review of later commits and the fix rounds
	// were answered by, and unscripted those nothing in the script matched.
	reviews, later, fixes, unscripted int
	// laterSession is the session the review of later commits asked to
	// continue, and laterPrompt is what it was asked.
	laterSession, laterPrompt string
	// fixerSession is the durable fixer session the run's record holds, as
	// the binary's answer relays it: the one session the product could have
	// handed the review of later commits.
	fixerSession string
	// before and after are where the branch stood on the remote around the run.
	before, after string
}

func (r reviewedLater) view(stage pipeline.Stage) (machine.Stage, error) {
	for _, v := range r.stages {
		if v.Stage == stage.String() {
			return v, nil
		}
	}
	return machine.Stage{}, fmt.Errorf("the run reports no %s stage", stage)
}

// revision is the commit a stage's last report names.
func (r reviewedLater) revision(stage pipeline.Stage) (string, error) {
	v, err := r.view(stage)
	if err != nil {
		return "", err
	}
	if v.Report == nil || v.Report.Revision == "" {
		return "", fmt.Errorf("the %s stage's report names no commit", stage)
	}
	return v.Report.Revision, nil
}

// edited returns a copy of r whose view of one stage is changed, so a
// counterfeit never writes through the slice the real observation shares.
func (r reviewedLater) edited(stage pipeline.Stage, change func(*machine.Stage)) reviewedLater {
	r.stages = slices.Clone(r.stages)
	for i := range r.stages {
		if r.stages[i].Stage == stage.String() {
			change(&r.stages[i])
		}
	}
	return r
}

// withRevision returns a copy of r whose view of one stage reports a
// different commit.
func (r reviewedLater) withRevision(stage pipeline.Stage, commit string) reviewedLater {
	return r.edited(stage, func(v *machine.Stage) {
		if v.Report != nil {
			report := *v.Report
			report.Revision = commit
			v.Report = &report
		}
	})
}

// TestCommitsAFixRoundWroteAfterReviewAreReviewedBeforeThePush drives PRD
// principle P5 at the push stage through the binary, which is section 14's
// seventh decision: when the head the push stage would forward is not the
// commit the review stage read, the commits between them are reviewed, in a
// fresh session, before anything is pushed, and a review of them that is not
// all notes holds the push.
//
// The commit after review comes from the lint stage. The home configures a
// lint command that, the first time it runs, appends a line to the change's
// file and fails, and passes once the line is there, the way a linter run with
// its fixes applied does. The failure takes an automatic fix round through
// the stand-in, scripted to say what it did and change nothing itself, and
// the round's body commits every change in the run's copy, which is the
// line the command wrote. That is the gap to read this walk with: the
// commit after review is the lint command's edit committed by the product's
// fix body, never an edit the stand-in made, because the stand-in prints and
// cannot write a working copy. What follows the commit is all the product's.
//
// The review stage's review is scripted clean and the review of the later
// commits is scripted with one ask finding, so the only thing that can hold
// the push is the review this decision added. The P5 cite covers that: the
// push stage asked for a second review, measured from the reviewed commit
// and shown the line the round committed, carrying no session, held with
// that review's finding, and moved nothing on the remote. It does not cover
// the clean half, a later review that approves and lets the push go through;
// internal/stages' own tests drive that at package reach, and this walk adds
// nothing to it. Nor does it cover a test stage's round, or a round a person
// asked for at a hold, which take the same path in the push stage and are
// not walked here.
func TestCommitsAFixRoundWroteAfterReviewAreReviewedBeforeThePush(t *testing.T) {
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P5)
	requireStageListMatchesThePRD(t)

	subject := ownSubject(t, false)
	observed := reviewedLater{before: subject.remoteBranch(t)}
	subject.commit(t, "later.md", "A change the remote has not seen.\n", "add a change the remote has not seen")

	objection := findings.Finding{
		ID:       "later-commit-unexplained",
		Severity: findings.SeverityError,
		Action:   findings.ActionAsk,
		// A location inside what the review read is what keeps the finding:
		// internal/findings records one that names nothing the review read as
		// a note, which would let the push through.
		Location: findings.Location{Path: "later.md", Line: 2},
		Description: "a line was appended to later.md after the review, and nothing in the change " +
			"says why it belongs there",
	}
	const laterStep, reviewStep, fixStep = 0, 1, 2
	agent := standin.New(t, standin.Script{Steps: []standin.Step{
		{
			Match: standin.Match{PromptContains: laterReviewLandmark}, Times: standin.Always,
			Reply: standin.Reviewed("the later commit adds a line nobody asked for", objection),
		},
		{Match: standin.MatchReview(), Times: standin.Always, Reply: standin.Reviewed("the change reads cleanly")},
		{
			Match: standin.Match{PromptContains: fixerLandmark}, Times: standin.Always,
			Reply: standin.Text("left the lint command's own edit in place"),
		},
	}})
	j := open(t, subject.scenario, func(o *journey.Options) { o.Dir = subject.work; o.AgentArguments = agent.Arguments() })
	const test = "true"
	lint := "grep -q '" + lintTrailer + "' later.md || { echo '" + lintTrailer + "' >> later.md; exit 1; }"
	if err := j.WriteConfiguration(map[string]any{"commands": map[string]any{"test": test, "lint": lint}}); err != nil {
		t.Fatalf("writing the home's configuration: %v", err)
	}
	succeeds(t, j.Command("init", "--default-branch", fixture.DefaultBranch))
	serve(t, j)

	// The pull request and checks stages are passed over for want of a code
	// host rather than asked to be skipped: the fixture's upstream is a local
	// repository, issue #283's owner decision. The walk stops at the push in
	// any case, so neither is reached.
	skipped := []pipeline.Stage{pipeline.StagePR, pipeline.StageCI}
	observed.expectedStops = configuredStops(test, lint, skipped)

	run := startRun(t, j, "--intent", "deliver a change whose lint round writes after the review")
	observed.stops, run = walkToPush(t, j, run, func(string) {})
	observed.stages = run.Stages
	observed.fixerSession, _ = run.Record.FixerSession.Get()
	if run.Decision != nil {
		observed.pushFindings = run.Decision.Findings
	}
	for _, call := range agent.Calls() {
		switch {
		case !call.Answered():
			observed.unscripted++
		case call.Step == laterStep:
			observed.later++
			observed.laterSession, observed.laterPrompt = call.Session(), call.Prompt
		case call.Step == reviewStep:
			observed.reviews++
		case call.Step == fixStep:
			observed.fixes++
		}
	}
	observed.after = subject.remoteBranch(t)

	check := journey.Check[reviewedLater]{
		What: "P5: a fix round's commit after review, at the push stage",
		Clauses: []journey.Clause[reviewedLater]{
			{
				States: "the run stopped at the stages stages.Holding gives, in order, then at the push stage, and nowhere else",
				Holds: func(r reviewedLater) error {
					if want := expectedThenPush(r.expectedStops); !slices.Equal(r.stops, want) {
						return fmt.Errorf("the run stopped at %v and was expected at %v", r.stops, want)
					}
					return nil
				},
			},
			{
				States: "the review stage passed over a commit it named",
				Holds: func(r reviewedLater) error {
					v, err := r.view(pipeline.StageReview)
					if err != nil {
						return err
					}
					if !v.Ran || v.Outcome != pipeline.OutcomePassed {
						return fmt.Errorf("the review stage ran=%v and ended %q", v.Ran, v.Outcome)
					}
					_, err = r.revision(pipeline.StageReview)
					return err
				},
			},
			{
				States: "the lint stage passed after a fix round that committed past the reviewed commit",
				Holds: func(r reviewedLater) error {
					v, err := r.view(pipeline.StageLint)
					if err != nil {
						return err
					}
					if v.Outcome != pipeline.OutcomePassed || v.Fix == "" || len(v.Executions) < 2 {
						return fmt.Errorf("the lint stage ended %q after %d execution(s) with fix summary %q; "+
							"wanted a pass after a round", v.Outcome, len(v.Executions), v.Fix)
					}
					reviewed, err := r.revision(pipeline.StageReview)
					if err != nil {
						return err
					}
					linted, err := r.revision(pipeline.StageLint)
					if err != nil {
						return err
					}
					if linted == reviewed {
						return fmt.Errorf("the lint stage last checked %s, the commit the review read, so no "+
							"commit followed the review", linted)
					}
					return nil
				},
			},
			{
				States: "the review stage asked the reviewer once",
				Holds: func(r reviewedLater) error {
					if r.reviews != 1 {
						return fmt.Errorf("the review stage's review was answered %d time(s)", r.reviews)
					}
					return nil
				},
			},
			{
				States: "the fix round launched the scripted fixer",
				Holds: func(r reviewedLater) error {
					if r.fixes == 0 {
						return errors.New("no fix round reached the fixer")
					}
					return nil
				},
			},
			{
				States: "the push stage asked for one review of the later commits",
				Holds: func(r reviewedLater) error {
					if r.later != 1 {
						return fmt.Errorf("the review of the later commits was asked %d time(s), wanted once", r.later)
					}
					return nil
				},
			},
			{
				States:  "that review carried no session, though the run held a fixer session it could have continued",
				Absence: true,
				Possible: func(r reviewedLater) error {
					if r.fixerSession == "" {
						return errors.New("the run's record holds no fixer session, so there was none the " +
							"review of the later commits could have continued")
					}
					return nil
				},
				Holds: func(r reviewedLater) error {
					if r.laterSession != "" {
						return fmt.Errorf("the review of the later commits continued session %q", r.laterSession)
					}
					return nil
				},
			},
			{
				States: "that review was measured from the commit the review stage read",
				Holds: func(r reviewedLater) error {
					reviewed, err := r.revision(pipeline.StageReview)
					if err != nil {
						return err
					}
					if !strings.Contains(r.laterPrompt, "measured against "+reviewed) {
						return fmt.Errorf("the review of the later commits is not measured against %s", reviewed)
					}
					return nil
				},
			},
			{
				States: "that review was shown the line the round committed",
				Holds: func(r reviewedLater) error {
					if !strings.Contains(r.laterPrompt, lintTrailer) {
						return errors.New("the review of the later commits was not shown the line the round committed")
					}
					return nil
				},
			},
			{
				States: "the push stage held with that review's finding",
				Holds: func(r reviewedLater) error {
					if _, ok := heldWith(r.pushFindings, objection.ID); !ok {
						return fmt.Errorf("the push stage held with %d finding(s) and none is %s",
							len(r.pushFindings), objection.ID)
					}
					return nil
				},
			},
			{
				States: "the push stage's report names the round's commit as the one reviewed",
				Holds: func(r reviewedLater) error {
					pushed, err := r.revision(pipeline.StagePush)
					if err != nil {
						return err
					}
					linted, err := r.revision(pipeline.StageLint)
					if err != nil {
						return err
					}
					if pushed != linted {
						return fmt.Errorf("the push stage's report names %s and the round committed %s", pushed, linted)
					}
					return nil
				},
			},
			{
				States:  "the remote's branch did not move",
				Absence: true,
				Possible: func(r reviewedLater) error {
					linted, err := r.revision(pipeline.StageLint)
					if err != nil {
						return err
					}
					if linted == r.before {
						return errors.New("the run's head is where the remote already stood, so a push would " +
							"have moved nothing")
					}
					return nil
				},
				Holds: func(r reviewedLater) error {
					if r.after != r.before {
						return fmt.Errorf("the remote's branch moved from %s to %s", r.before, r.after)
					}
					return nil
				},
			},
			{
				States:  "no agent invocation went unscripted",
				Absence: true,
				Possible: func(r reviewedLater) error {
					if r.reviews+r.later+r.fixes == 0 {
						return errors.New("no agent was launched at all, so none could have gone unscripted")
					}
					return nil
				},
				Holds: func(r reviewedLater) error {
					if r.unscripted != 0 {
						return fmt.Errorf("%d agent invocation(s) matched nothing in the script", r.unscripted)
					}
					return nil
				},
			},
		},
		Counterfeits: []journey.Counterfeit[reviewedLater]{
			{Named: "the push went through and the run never held there", Break: func(r reviewedLater) reviewedLater {
				r.stops = slices.Clone(r.stops[:len(r.stops)-1])
				return r
			}},
			{Named: "the review stage was skipped", Break: func(r reviewedLater) reviewedLater {
				return r.edited(pipeline.StageReview, func(v *machine.Stage) {
					v.Ran, v.Outcome = false, pipeline.OutcomeSkipped
				})
			}},
			{Named: "the lint round committed nothing", Break: func(r reviewedLater) reviewedLater {
				reviewed, _ := r.revision(pipeline.StageReview)
				return r.withRevision(pipeline.StageLint, reviewed)
			}},
			{Named: "the lint stage passed on its first execution", Break: func(r reviewedLater) reviewedLater {
				return r.edited(pipeline.StageLint, func(v *machine.Stage) {
					v.Fix, v.Executions = "", slices.Clone(v.Executions[:1])
				})
			}},
			{Named: "the review stage's review was never asked for", Break: func(r reviewedLater) reviewedLater {
				r.reviews = 0
				return r
			}},
			{Named: "the fix round reached no fixer", Break: func(r reviewedLater) reviewedLater {
				r.fixes = 0
				return r
			}},
			{Named: "the push stage forwarded without reviewing the later commits", Break: func(r reviewedLater) reviewedLater {
				r.later, r.laterSession, r.laterPrompt = 0, "", ""
				return r
			}},
			{Named: "the review of the later commits continued the fixer's session", Break: func(r reviewedLater) reviewedLater {
				r.laterSession = r.fixerSession
				return r
			}},
			{Named: "the review of the later commits was measured from where the branch stood", Break: func(r reviewedLater) reviewedLater {
				reviewed, _ := r.revision(pipeline.StageReview)
				r.laterPrompt = strings.ReplaceAll(r.laterPrompt, "measured against "+reviewed, "measured against "+r.before)
				return r
			}},
			{Named: "the review of the later commits was not shown the round's line", Break: func(r reviewedLater) reviewedLater {
				r.laterPrompt = strings.ReplaceAll(r.laterPrompt, lintTrailer, "")
				return r
			}},
			{Named: "the push stage held on something other than the later review", Break: func(r reviewedLater) reviewedLater {
				r.pushFindings = []findings.Finding{{
					ID: "push-no-approval", Severity: findings.SeverityError, Action: findings.ActionAsk,
					Description: "no completed review approved a commit this one descends from",
				}}
				return r
			}},
			{Named: "the push stage reviewed the reviewed commit again", Break: func(r reviewedLater) reviewedLater {
				reviewed, _ := r.revision(pipeline.StageReview)
				return r.withRevision(pipeline.StagePush, reviewed)
			}},
			{Named: "the remote's branch moved to the round's commit", Break: func(r reviewedLater) reviewedLater {
				r.after, _ = r.revision(pipeline.StageLint)
				return r
			}},
			{Named: "an agent was launched that nothing scripted", Break: func(r reviewedLater) reviewedLater {
				r.unscripted = 1
				return r
			}},
		},
	}
	if err := check.Verify(observed); err != nil {
		t.Fatalf("%v", err)
	}
}
