package journey_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
)

// TestAPassMeansTheSameThingEverywhere drives PRD principle P2 through the
// binary that ships.
//
// It answers section 13's three-part test for P2 in one scenario, because the
// three parts are one claim: a run walks the nine stages in the specified
// order, a person may skip stages for one run on purpose, and a configuration
// document that asks for a standing skip is refused before the service serves
// anything.
//
// The order itself is internal/pipeline's, which makes another order unsayable
// rather than checked, so this points at that owner rather than restating the
// nine names. What it establishes that the owner cannot is that the shipped
// binary walks them.
func TestAPassMeansTheSameThingEverywhere(t *testing.T) {
	principles.Cite(t, principles.P2)

	j := inClone(t)

	walked := journey.Check[machine.Run]{
		What: "a run through the binary walks the nine stages in the order internal/pipeline fixes, " +
			"every one of them, and reaches the end of the gate",
		Clauses: []journey.Clause[machine.Run]{
			{
				States: "the stages come back in the order internal/pipeline fixes, all nine and no more",
				Holds: func(run machine.Run) error {
					if got, want := stageNames(run), stageOrder(); !slices.Equal(got, want) {
						return fmt.Errorf("the run reported the stages %v, and the order is %v", got, want)
					}
					return nil
				},
			},
			{
				States: "every stage of a run that skipped nothing ran",
				Holds: func(run machine.Run) error {
					for _, stage := range run.Stages {
						if !stage.Ran {
							return fmt.Errorf("the %s stage did not run, and this run skipped nothing", stage.Stage)
						}
					}
					return nil
				},
			},
			{
				// Every hold in this run was answered with the same option, so
				// every stage has to carry it. A stage reporting another
				// outcome was answered by something other than the answer this
				// test gave, which internal/machine says a stage's Ran flag
				// alone cannot tell apart from being skipped past at its hold.
				States: "every stage carries the outcome this run's holds were answered with",
				Holds: func(run machine.Run) error {
					for _, stage := range run.Stages {
						if stage.Outcome != pipeline.OutcomeApproved {
							return fmt.Errorf("the %s stage came back %q, and every hold in this run was approved",
								stage.Stage, stage.Outcome)
						}
					}
					return nil
				},
			},
			{
				States: "the run reached the end of the gate",
				Holds: func(run machine.Run) error {
					if run.Outcome != machine.OutcomeChecksPassed {
						return fmt.Errorf("the run ended %s rather than reaching the end of the gate", run.Outcome)
					}
					return nil
				},
			},
		},
		Counterfeits: []journey.Counterfeit[machine.Run]{
			{Named: "two stages came back in the other order", Break: func(run machine.Run) machine.Run {
				run = cloneRun(run)
				run.Stages[2], run.Stages[3] = run.Stages[3], run.Stages[2]
				return run
			}},
			{Named: "a stage was left out of the walk", Break: func(run machine.Run) machine.Run {
				run = cloneRun(run)
				run.Stages = slices.Delete(run.Stages, 5, 6)
				return run
			}},
			{Named: "a tenth stage was added", Break: func(run machine.Run) machine.Run {
				run = cloneRun(run)
				run.Stages = append(run.Stages, machine.Stage{Stage: "publish", Ran: true})
				return run
			}},
			{Named: "a stage nobody skipped did not run", Break: func(run machine.Run) machine.Run {
				run = cloneRun(run)
				run.Stages[4].Ran = false
				return run
			}},
			{Named: "a stage came back carrying an outcome nobody gave it", Break: func(run machine.Run) machine.Run {
				run = cloneRun(run)
				run.Stages[6].Outcome = pipeline.OutcomeSkipped
				return run
			}},
			{Named: "the run stopped without reaching the end", Break: func(run machine.Run) machine.Run {
				run = cloneRun(run)
				run.Outcome = machine.OutcomeFailed
				return run
			}},
		},
	}
	whole := last(answerHolds(t, j, startRun(t, j, "--intent", "narrow the Total loop bound on purpose"), "approved"))
	if err := walked.Verify(whole); err != nil {
		t.Fatalf("%v\n\nthe run stands at %q, outcome %s, over stages %v",
			err, whole.Position, whole.Outcome, stageNames(whole))
	}

	asked := []string{pipeline.StageReview.String(), pipeline.StageLint.String()}
	skipped := journey.Check[machine.Run]{
		What: "a per-run skip takes exactly the stages it named out of that one run, and no others",
		Clauses: []journey.Clause[machine.Run]{
			{
				States: "no stage this run was told to skip ran",
				Holds: func(run machine.Run) error {
					for _, stage := range run.Stages {
						if slices.Contains(asked, stage.Stage) && stage.Ran {
							return fmt.Errorf("the %s stage was skipped for this run and ran anyway", stage.Stage)
						}
					}
					return nil
				},
			},
			{
				States: "every stage this run was told to skip came back reported as skipped",
				Holds: func(run machine.Run) error {
					for _, stage := range run.Stages {
						if slices.Contains(asked, stage.Stage) && stage.Outcome != pipeline.OutcomeSkipped {
							return fmt.Errorf("the %s stage was skipped for this run and came back %q",
								stage.Stage, stage.Outcome)
						}
					}
					return nil
				},
			},
			{
				States: "every stage nobody asked to skip ran",
				Holds: func(run machine.Run) error {
					for _, stage := range run.Stages {
						if !slices.Contains(asked, stage.Stage) && !stage.Ran {
							return fmt.Errorf("the %s stage was not skipped and did not run", stage.Stage)
						}
					}
					return nil
				},
			},
			{
				States: "the run reached the end of the gate",
				Holds: func(run machine.Run) error {
					if run.Outcome != machine.OutcomeChecksPassed {
						return fmt.Errorf("the run ended %s rather than reaching the end of the gate", run.Outcome)
					}
					return nil
				},
			},
		},
		Counterfeits: []journey.Counterfeit[machine.Run]{
			{Named: "a stage the run skipped ran anyway", Break: func(run machine.Run) machine.Run {
				run = cloneRun(run)
				for i, stage := range run.Stages {
					if stage.Stage == pipeline.StageReview.String() {
						run.Stages[i].Ran = true
					}
				}
				return run
			}},
			{Named: "a stage nobody asked to skip was skipped too", Break: func(run machine.Run) machine.Run {
				run = cloneRun(run)
				for i, stage := range run.Stages {
					if stage.Stage == pipeline.StageTest.String() {
						run.Stages[i].Ran = false
						run.Stages[i].Outcome = pipeline.OutcomeSkipped
					}
				}
				return run
			}},
			{Named: "a skipped stage came back reporting it had passed", Break: func(run machine.Run) machine.Run {
				run = cloneRun(run)
				for i, stage := range run.Stages {
					if stage.Stage == pipeline.StageLint.String() {
						run.Stages[i].Outcome = pipeline.OutcomeApproved
					}
				}
				return run
			}},
			{Named: "the run carrying the skip stopped without reaching the end",
				Break: func(run machine.Run) machine.Run {
					run = cloneRun(run)
					run.Outcome = machine.OutcomeFailed
					return run
				}},
		},
	}
	second := last(answerHolds(t, j,
		startRun(t, j, "--intent", "the same change, checked without two stages", "--skip", "review,lint"),
		"approved"))
	if err := skipped.Verify(second); err != nil {
		t.Fatalf("%v", err)
	}

	// The standing skip is asked for the way an operator would ask for one, in
	// the home's own configuration document, and the service is then started
	// in the foreground so that its refusal is this command's answer rather
	// than a line in a log of a process nobody is holding.
	if err := j.Kill(); err != nil {
		t.Fatalf("ending the service before changing its configuration: %v", err)
	}
	if err := j.WriteConfiguration(map[string]any{"skip": []string{pipeline.StageReview.String()}}); err != nil {
		t.Fatalf("writing a configuration asking for a standing skip: %v", err)
	}
	standing := journey.Check[journey.Answer]{
		What: "a configuration document asking for a standing skip stops the service before it serves",
		Clauses: []journey.Clause[journey.Answer]{
			{
				// The command is bounded rather than left to run, because the
				// failure this exists to catch is the service accepting the
				// standing skip and serving. Unbounded, that failure never
				// returns and the suite hangs to the test timeout instead of
				// reporting it, so the one check here whose whole subject is a
				// refusal would be the one that cannot fail.
				States: "the command ended on its own rather than serving until this harness stopped it",
				Holds: func(answer journey.Answer) error {
					if answer.Ended {
						return fmt.Errorf("the service was still serving when this harness stopped it, "+
							"so it accepted a standing skip: %s", answer)
					}
					return nil
				},
			},
			{
				States: "the command refused rather than exiting successfully",
				Holds: func(answer journey.Answer) error {
					if answer.Code == machine.ExitOK {
						return fmt.Errorf("the service accepted a standing skip: %s", answer)
					}
					return nil
				},
			},
			{
				States: "the refusal was reported as a document a driving agent can read",
				Holds: func(answer journey.Answer) error {
					if _, ok := answer.Failure(); !ok {
						return fmt.Errorf("the refusal was not reported as a document: %s", answer)
					}
					return nil
				},
			},
			{
				States: "the refusal names the key it refused",
				Holds: func(answer journey.Answer) error {
					failure, ok := answer.Failure()
					if !ok {
						return fmt.Errorf("the refusal was not reported as a document: %s", answer)
					}
					if !strings.Contains(failure.Error, "skip") {
						return fmt.Errorf("the refusal does not name the key it refused: %q", failure.Error)
					}
					return nil
				},
			},
		},
		Counterfeits: []journey.Counterfeit[journey.Answer]{
			{Named: "the service served with the standing skip in place and had to be stopped",
				Break: func(a journey.Answer) journey.Answer {
					a.Ended = true
					return a
				}},
			{Named: "the service started with the standing skip in place", Break: func(a journey.Answer) journey.Answer {
				a.Code = machine.ExitOK
				return a
			}},
			{Named: "the refusal reached standard error alone, where a driving agent would not find it",
				Break: func(a journey.Answer) journey.Answer {
					a.Stdout = ""
					return a
				}},
			{Named: "the refusal never says which key it refused", Break: func(a journey.Answer) journey.Answer {
				a.Stdout = `{"error":"something went wrong","code":"internal"}`
				return a
			}},
		},
	}
	if err := standing.Verify(j.CommandBounded(j.Dir(), standingSkipBound, "service", "start", "--foreground")); err != nil {
		t.Fatalf("%v", err)
	}
}

// standingSkipBound is how long a service refusing a standing skip is given to
// answer before this harness stops it and reports that it served.
//
// It is generous for a process that reads one configuration document and
// refuses, and it is what turns the regression the standing-skip check exists
// for into an assertion failure rather than a suite that hangs.
const standingSkipBound = 15 * time.Second

// stageOrder is the nine stage names in the order internal/pipeline fixes,
// which is the one owner of that order.
func stageOrder() []string {
	order := pipeline.Order()
	names := make([]string, 0, len(order))
	for _, stage := range order {
		names = append(names, stage.String())
	}
	return names
}
