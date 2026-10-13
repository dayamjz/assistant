package journey_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/fixture"
	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/stages"
)

// delivered is what a run that took the delivery path did, read off the
// answers the binary gave and off the two repositories it acted on.
type delivered struct {
	// skipped is the stages the run asked to skip.
	skipped []pipeline.Stage
	// expectedStops is where stages.Holding says the run's resolved
	// configuration stops it, less the stages the run skipped. It is derived,
	// never written down, so a body landing moves it without this test.
	expectedStops []pipeline.Stage
	// stops is the stage of every decision the run offered before it ended.
	stops []string
	// outcome is how the run ended.
	outcome machine.Outcome
	// stages is the final answer's view of every stage.
	stages []machine.Stage
	// reviewsAnswered counts invocations the scripted reviewer answered, and
	// unscripted those nothing in the script matched.
	reviewsAnswered, unscripted int
	// submitted is the commit the working copy held when the run began.
	submitted string
	// before and after are where the branch stood on the remote around the run.
	before, after string
}

func (d delivered) view(stage pipeline.Stage) (machine.Stage, bool) {
	for _, v := range d.stages {
		if v.Stage == stage.String() {
			return v, true
		}
	}
	return machine.Stage{}, false
}

// edited returns a copy of d whose view of one stage is changed, so a
// counterfeit never writes through the slice the real observation shares.
func (d delivered) edited(stage pipeline.Stage, change func(*machine.Stage)) delivered {
	d.stages = slices.Clone(d.stages)
	for i := range d.stages {
		if d.stages[i].Stage == stage.String() {
			change(&d.stages[i])
		}
	}
	return d
}

// TestARunTakesTheDeliveryPathToAPush drives review, test, lint and push
// through the binary, which every other walk here goes around.
//
// The reviewer is the harness's stand-in scripted to answer a clean review, so
// what this establishes is that the stages are reached, run their bodies, and
// hand the run on; it says nothing about whether a real reviewer would find a
// defect, and the README's first limit stands. The test and lint commands are
// configured in the home's own configuration, which is trusted, so those
// stages execute them rather than holding. The run submits a commit the remote
// does not have, so the push stage has an update to make and the remote's
// branch moving is the evidence it made it.
//
// The pull request and checks stages are skipped. Both need a repository on a
// code host, no record here names one and this fixture has no host for
// internal/forge to address, so a run that took either would stop there
// whatever the delivery stages did.
func TestARunTakesTheDeliveryPathToAPush(t *testing.T) {
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P6)
	requireStageListMatchesThePRD(t)

	// A remote of this test's own, so the push moves nothing another test
	// reads: the walkable scenario's origin is cloned by every walk.
	from := scenarioNamed(t, fixture.ScenarioWalkable)
	own := from
	own.Origin = filepath.Join(t.TempDir(), "remote.git")
	if _, err := journey.Git(from, filepath.Dir(own.Origin), "clone", "--quiet", "--bare", from.Origin, own.Origin); err != nil {
		t.Fatalf("copying the remote: %v", err)
	}
	path, err := journey.Clone(own, filepath.Join(t.TempDir(), "work"))
	if err != nil {
		t.Fatalf("cloning: %v", err)
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := journey.Git(own, dir, args...)
		if err != nil {
			t.Fatalf("%v", err)
		}
		return out
	}
	observed := delivered{before: git(own.Origin, "rev-parse", "refs/heads/"+own.Branch)}
	if err := writeInto(path, "delivery.md", "A change the remote has not seen.\n"); err != nil {
		t.Fatalf("%v", err)
	}
	git(path, "add", "delivery.md")
	git(path, "commit", "--quiet", "-m", "add a change the remote has not seen")
	observed.submitted = git(path, "rev-parse", "HEAD")

	const reviewStep = 0
	agent := standin.New(t, standin.Script{Steps: []standin.Step{{
		Match: standin.MatchReview(), Times: standin.Always, Reply: standin.Reviewed("the change reads cleanly"),
	}}})
	j := open(t, own, func(o *journey.Options) { o.Dir = path; o.AgentArguments = agent.Arguments() })
	const command = "true"
	if err := j.WriteConfiguration(map[string]any{"commands": map[string]any{"test": command, "lint": command}}); err != nil {
		t.Fatalf("writing the home's configuration: %v", err)
	}
	succeeds(t, j.Command("init", "--default-branch", fixture.DefaultBranch))
	serve(t, j)

	observed.skipped = []pipeline.Stage{pipeline.StagePR, pipeline.StageCI}
	resolved := config.Defaults()
	resolved.Commands.Test, resolved.Commands.Lint = command, command
	for _, stage := range stages.Holding(resolved) {
		if !slices.Contains(observed.skipped, stage) {
			observed.expectedStops = append(observed.expectedStops, stage)
		}
	}

	run := startRun(t, j, "--skip", pipeline.StagePR.String()+","+pipeline.StageCI.String(),
		"--intent", "deliver a change through review, test, lint and push")
	for range 20 {
		if run.Outcome != machine.OutcomeDecision || run.Decision == nil {
			break
		}
		observed.stops = append(observed.stops, run.Decision.Stage)
		run = decodeRun(t, j.Command("--answer", "approved"))
	}
	observed.outcome = run.Outcome
	observed.stages = run.Stages
	for _, call := range agent.Calls() {
		switch {
		case call.Step == reviewStep:
			observed.reviewsAnswered++
		case !call.Answered():
			observed.unscripted++
		}
	}
	observed.after = git(own.Origin, "rev-parse", "refs/heads/"+own.Branch)

	reached := func(d delivered, stage pipeline.Stage) (machine.Stage, error) {
		v, ok := d.view(stage)
		if !ok {
			return v, fmt.Errorf("the run reports no %s stage", stage)
		}
		return v, nil
	}
	check := journey.Check[delivered]{
		What: "P6: a run through review, test, lint and push",
		Clauses: []journey.Clause[delivered]{
			{
				States: "every stage not skipped ran, as passed or as the approved hold it stopped at",
				Holds: func(d delivered) error {
					for _, stage := range pipeline.Order() {
						if slices.Contains(d.skipped, stage) {
							continue
						}
						v, err := reached(d, stage)
						if err != nil {
							return err
						}
						want := pipeline.OutcomePassed
						if slices.Contains(d.expectedStops, stage) {
							want = pipeline.OutcomeApproved
						}
						if !v.Ran || v.Outcome != want {
							return fmt.Errorf("the %s stage ran=%v and ended %q, wanted a run that ended %q",
								stage, v.Ran, v.Outcome, want)
						}
					}
					return nil
				},
			},
			{
				States: "the run stopped at the stages the resolved configuration holds, in order, and nowhere else",
				Holds: func(d delivered) error {
					var want []string
					for _, stage := range d.expectedStops {
						want = append(want, stage.String())
					}
					if !slices.Equal(d.stops, want) {
						return fmt.Errorf("the run stopped at %v and stages.Holding says it stops at %v", d.stops, want)
					}
					return nil
				},
			},
			{
				States: "the run ended with its checks passed",
				Holds: func(d delivered) error {
					if d.outcome != machine.OutcomeChecksPassed {
						return fmt.Errorf("the run ended %s", d.outcome)
					}
					return nil
				},
			},
			{
				States: "the review stage launched the scripted reviewer and it answered",
				Holds: func(d delivered) error {
					if d.reviewsAnswered == 0 {
						return errors.New("the scripted reviewer answered no review, so the review stage passed " +
							"without one or was never reached")
					}
					return nil
				},
			},
			{
				States:  "no agent invocation went unscripted",
				Absence: true,
				Possible: func(d delivered) error {
					if d.reviewsAnswered == 0 {
						return errors.New("no agent was launched at all, so none could have gone unscripted")
					}
					return nil
				},
				Holds: func(d delivered) error {
					if d.unscripted != 0 {
						return fmt.Errorf("%d agent invocation(s) matched nothing in the script", d.unscripted)
					}
					return nil
				},
			},
			{
				States: "the test and lint stages ran the configured command",
				Holds: func(d delivered) error {
					for _, stage := range []pipeline.Stage{pipeline.StageTest, pipeline.StageLint} {
						v, err := reached(d, stage)
						if err != nil {
							return err
						}
						if v.Report == nil || !slices.Contains(v.Report.Tested, command) {
							return fmt.Errorf("the %s stage reports no test of %q", stage, command)
						}
					}
					return nil
				},
			},
			{
				States: "the stages the run asked to skip are reported skipped and did not run",
				Holds: func(d delivered) error {
					for _, stage := range d.skipped {
						v, err := reached(d, stage)
						if err != nil {
							return err
						}
						if v.Ran || v.Outcome != pipeline.OutcomeSkipped {
							return fmt.Errorf("the skipped %s stage ran=%v and ended %q", stage, v.Ran, v.Outcome)
						}
					}
					return nil
				},
			},
			{
				States: "the remote did not already hold the submitted commit",
				Holds: func(d delivered) error {
					if d.before == d.submitted {
						return errors.New("the remote's branch already stood at the submitted commit, so " +
							"the push had nothing to move and its landing says nothing")
					}
					return nil
				},
			},
			{
				States: "the push stage moved the remote's branch to the submitted commit",
				Holds: func(d delivered) error {
					if d.after != d.submitted {
						return fmt.Errorf("the remote's branch stands at %s and the run submitted %s", d.after, d.submitted)
					}
					return nil
				},
			},
		},
		Counterfeits: []journey.Counterfeit[delivered]{
			{Named: "a hold nothing declared", Break: func(d delivered) delivered {
				d.stops = append(slices.Clone(d.stops), pipeline.StageLint.String())
				return d
			}},
			{Named: "a declared hold the run never stopped at", Break: func(d delivered) delivered {
				d.stops = nil
				return d
			}},
			{Named: "the review stage never ran", Break: func(d delivered) delivered {
				return d.edited(pipeline.StageReview, func(v *machine.Stage) { v.Ran = false })
			}},
			{Named: "the push stage held instead of passing", Break: func(d delivered) delivered {
				return d.edited(pipeline.StagePush, func(v *machine.Stage) { v.Outcome = pipeline.OutcomeHeld })
			}},
			{Named: "the run ended failed", Break: func(d delivered) delivered {
				d.outcome = machine.OutcomeFailed
				return d
			}},
			{Named: "the reviewer was never asked", Break: func(d delivered) delivered {
				d.reviewsAnswered = 0
				return d
			}},
			{Named: "an agent was launched that nothing scripted", Break: func(d delivered) delivered {
				d.unscripted = 1
				return d
			}},
			{Named: "the lint stage ran nothing", Break: func(d delivered) delivered {
				return d.edited(pipeline.StageLint, func(v *machine.Stage) {
					if v.Report != nil {
						r := *v.Report
						r.Tested = nil
						v.Report = &r
					}
				})
			}},
			{Named: "a skipped stage ran", Break: func(d delivered) delivered {
				return d.edited(pipeline.StagePR, func(v *machine.Stage) { v.Ran = true })
			}},
			{Named: "the remote already held the submitted commit", Break: func(d delivered) delivered {
				d.before = d.submitted
				return d
			}},
			{Named: "the remote's branch did not move", Break: func(d delivered) delivered {
				d.after = d.before
				return d
			}},
		},
	}
	if err := check.Verify(observed); err != nil {
		t.Fatalf("%v", err)
	}
}
