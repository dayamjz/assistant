package stages_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/stages"
)

// The set this build wires has to build a pipeline. A missing field is
// unsayable, so what this catches is a field left holding the zero
// Implementation, which pipeline.New refuses with ErrMissingStage.
func TestTheStagesThisBuildHasBuildAPipeline(t *testing.T) {
	t.Parallel()
	if _, err := pipeline.New(pipeline.Options{
		Stages: stages.All(),
		Rounds: config.FixRounds{},
		Budget: 40,
	}); err != nil {
		t.Fatalf("building a pipeline from stages.All: %v", err)
	}
}

// Every stage without a body holds for a person. This runs the implementation
// the pipeline would run and classifies its report the way the stage node
// does, so what it demonstrates is the behaviour rather than the source text.
func TestAStageWithNoBodyHoldsForAPersonRatherThanPassing(t *testing.T) {
	t.Parallel()
	implemented := make(map[pipeline.Stage]bool)
	for _, stage := range stages.Implemented() {
		implemented[stage] = true
	}
	all := stages.All()
	for _, stage := range pipeline.Order() {
		if implemented[stage] {
			continue
		}
		t.Run(stage.String(), func(t *testing.T) {
			t.Parallel()
			out, err := implementationFor(t, all, stage).NewBody()(t.Context(), pipeline.Input{Stage: stage})
			if err != nil {
				t.Fatalf("running the pending %s stage: %v", stage, err)
			}
			report := out.Report.Normalize()
			if err := report.Validate(); err != nil {
				t.Fatalf("the pending %s stage produced a report the pipeline refuses: %v", stage, err)
			}
			if !report.HasHeld() {
				t.Fatalf("the pending %s stage reported %+v, which does not hold for a person", stage, report)
			}
			if len(report.Fixable()) != 0 {
				t.Fatalf("the pending %s stage reported a fix-eligible finding, which would send it to a fixer", stage)
			}
			if !strings.Contains(report.Summary, stage.String()) {
				t.Fatalf("the pending %s stage's summary %q does not name the stage", stage, report.Summary)
			}
		})
	}
}

// Implemented names the stages this build has a body for, and All has to place
// those bodies where a run actually walks. Nothing else checks that direction.
// Removing a stage's row from written would leave the pending loop above
// simply covering it, intent_test.go builds from stages.Intent directly rather
// than from All, and the hold assertions in internal/cli and internal/service
// derive their stage from Implemented, so they would follow the table down
// with it and the whole suite would stay green while the product went back to
// holding at a stage it reports a body for.
//
// So this runs what All holds for each implemented stage and requires it not
// to be Pending. The two are told apart by behaviour rather than by comparing
// function values: Pending cannot fail and reports one ask finding that holds
// the stage for a person, so a body that reports something else, or that fails
// on the state this hands it, is not Pending. Tolerating a failure is what
// keeps this from having to know what a stage landing later needs of the
// world; the stage bodies that are pure functions of run state give it teeth.
func TestAllPlacesAWrittenBodyAtEveryImplementedStage(t *testing.T) {
	t.Parallel()
	implemented := stages.Implemented()
	if len(implemented) == 0 {
		t.Fatal("this build reports no stage bodies at all. A body has landed, so an empty " +
			"written table is a row that went missing rather than a build that never had one, " +
			"and skipping here is how that would go unnoticed: with nothing to loop over, every " +
			"assertion below stops being reached.")
	}
	all := stages.All()
	for _, stage := range implemented {
		t.Run(stage.String(), func(t *testing.T) {
			t.Parallel()
			impl := implementationFor(t, all, stage)
			allowed := make(map[pipeline.Key]bool, len(impl.Reads))
			for _, key := range impl.Reads {
				allowed[key] = true
			}
			out, err := impl.NewBody()(t.Context(), pipeline.Input{
				Stage: stage,
				State: declaredReader{allowed: allowed, state: map[pipeline.Key]graph.Value{
					pipeline.KeyIntent:         graph.TextValue("add a greeting"),
					pipeline.KeyIntentSupplied: graph.BoolValue(true),
				}},
			})
			if err != nil {
				return // Pending cannot fail, so a body that did is not it.
			}
			report := out.Report.Normalize()
			if err := report.Validate(); err != nil {
				t.Fatalf("the %s stage produced a report the pipeline refuses: %v", stage, err)
			}
			pending, err := stages.Pending(stage.String()).NewBody()(t.Context(), pipeline.Input{Stage: stage})
			if err != nil {
				t.Fatalf("running Pending for %s: %v", stage, err)
			}
			if reflect.DeepEqual(report, pending.Report.Normalize()) {
				t.Fatalf("Implemented names %s, but All places Pending at it: a run stops for a "+
					"person at a stage this build reports a body for", stage)
			}
			if report.HasHeld() {
				t.Fatalf("the %s stage's body held for a person over a supplied intent: %+v", stage, report)
			}
		})
	}
}

// Implemented is read off the same table All places implementations from, so a
// stage it names must not be pending. Nothing here can compare two function
// values, so what it checks is the relationship the table guarantees: the set
// is a subset of the nine, in the order a run takes them.
func TestImplementedNamesOnlyStagesOfThisPipeline(t *testing.T) {
	t.Parallel()
	order := pipeline.Order()
	position := make(map[pipeline.Stage]int, len(order))
	for i, stage := range order {
		position[stage] = i
	}
	previous := -1
	for _, stage := range stages.Implemented() {
		at, ok := position[stage]
		if !ok {
			t.Fatalf("Implemented names %s, which is not one of the nine stages", stage)
		}
		if at <= previous {
			t.Fatalf("Implemented is not in the order a run takes the stages: %s came after position %d", stage, previous)
		}
		previous = at
	}
}

// implementationFor returns the implementation a Stages holds for one stage.
// pipeline.Stages is nine named fields rather than a list, so this is the one
// place a test maps a stage onto a field.
func implementationFor(t *testing.T, s pipeline.Stages, stage pipeline.Stage) pipeline.Implementation {
	t.Helper()
	switch stage {
	case pipeline.StageIntent:
		return s.Intent
	case pipeline.StageRebase:
		return s.Rebase
	case pipeline.StageReview:
		return s.Review
	case pipeline.StageTest:
		return s.Test
	case pipeline.StageDocument:
		return s.Document
	case pipeline.StageLint:
		return s.Lint
	case pipeline.StagePush:
		return s.Push
	case pipeline.StagePR:
		return s.PR
	case pipeline.StageCI:
		return s.CI
	default:
		t.Fatalf("no field for stage %s", stage)
		return pipeline.Implementation{}
	}
}
