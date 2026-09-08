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
		Stages: stages.All(stages.StageDeps{}),
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
	all := stages.All(stages.StageDeps{})
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

// bodied is the set of stages this build is meant to have a body for, written
// out. It is deliberately a second statement of the written table rather than
// a derivation from it, on the same terms as internal/cli/cli_test.go's
// specified: a guard read off the thing it guards cannot notice that thing
// losing a row.
//
// Losing one is the regression worth catching. Every other assertion in this
// package and the hold assertions in internal/cli and internal/service are
// derived from Implemented, so a stage dropped from the table leaves all of
// their sets at once and they follow it down in silence, while a run goes back
// to holding at a stage the build no longer reports a body for.
//
// Landing a body means adding it here. That one edit is the whole cost of the
// guard, and stating the set twice is the point rather than an oversight.
var bodied = []pipeline.Stage{pipeline.StageIntent, pipeline.StageRebase}

// The stages this build has bodies for have to be the ones it is meant to have
// bodies for, in the order a run takes them.
//
// This is the only assertion in the package that fails when a row leaves the
// written table, because it is the only one that does not read its expectation
// off that table.
func TestImplementedIsTheSetThisBuildIsMeantToHave(t *testing.T) {
	t.Parallel()
	if got := stages.Implemented(); !reflect.DeepEqual(got, bodied) {
		t.Fatalf("Implemented reports bodies for %v, and this build is meant to have them for %v. "+
			"A stage missing from the first is a row that left the written table, which sends a run "+
			"back to holding there; a stage missing from the second is a body that landed without "+
			"being recorded here.", got, bodied)
	}
}

// All has to place the written body, and not Pending, at each stage the table
// names. That is the direction nothing else here checks: intent_test.go builds
// from stages.Intent directly rather than from All, and the hold assertions in
// internal/cli and internal/service derive their stage from Implemented.
//
// What it does not catch is a row leaving written altogether. The loop is over
// Implemented, which is read off that same table, so a removed row leaves the
// set and nothing below is reached for it. The test above is what catches
// that, and it is why the set is stated a second time.
//
// A body and Pending are told apart by behaviour rather than by comparing
// function values: Pending cannot fail and reports one ask finding that holds
// the stage for a person, so a body that reports something else, or that fails
// on the state this hands it, is not Pending.
//
// The residual gap is that tolerance and the reader behind it. Treating a
// failure as proof the body is not Pending is what keeps this from having to
// know what a stage landing later needs of the world, and declaredReader
// answers a declared key it has no value for with the empty text, which for a
// stage reading a bool or a list is a value the real graph reader cannot
// produce. A body that failed on it would be tolerated here and this test
// would pass without checking that stage at all, so a stage needing more than
// run state is given it in bodyEnvironment rather than left to the tolerance.
//
// bodyEnvironment is also what closes the gap for the stage it provisions: a
// stage it builds a world for is held to succeeding in it, so the tolerance
// applies only to a body nobody has provisioned yet.
func TestAllPlacesAWrittenBodyAtEveryImplementedStage(t *testing.T) {
	t.Parallel()
	implemented := stages.Implemented()
	if len(implemented) == 0 {
		t.Fatal("this build reports no stage bodies, so the loop below checks nothing; " +
			"TestImplementedIsTheSetThisBuildIsMeantToHave says which stages it should name")
	}
	for _, stage := range implemented {
		t.Run(stage.String(), func(t *testing.T) {
			t.Parallel()
			deps, state, provisioned := bodyEnvironment(t, stage)
			impl := implementationFor(t, stages.All(deps), stage)
			allowed := make(map[pipeline.Key]bool, len(impl.Reads))
			for _, key := range impl.Reads {
				allowed[key] = true
			}
			out, err := impl.NewBody()(t.Context(), pipeline.Input{
				Stage: stage,
				State: declaredReader{allowed: allowed, state: state},
			})
			if err != nil {
				if provisioned {
					t.Fatalf("the %s stage was given what its body needs and failed anyway: %v", stage, err)
				}
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

// bodyEnvironment supplies what one stage's body needs of the world, and says
// whether it supplied it. A stage nobody has built a world for gets the run
// state alone and the caller's tolerance for a body that fails on it; a stage
// this names is held to succeeding, because a world it was given and failed in
// is a defect rather than a body the test cannot reach.
//
// It is a switch rather than a table because each stage's world is built
// differently, and a map of constructors would only move the switch.
func bodyEnvironment(t *testing.T, stage pipeline.Stage) (stages.StageDeps, map[pipeline.Key]graph.Value, bool) {
	t.Helper()
	runState := map[pipeline.Key]graph.Value{
		pipeline.KeyIntent:         graph.TextValue("add a greeting"),
		pipeline.KeyIntentSupplied: graph.BoolValue(true),
	}
	switch stage {
	case pipeline.StageRebase:
		s := newSubject(t)
		state := s.state(nil)
		for key, value := range runState {
			state[key] = value
		}
		return s.deps, state, true
	default:
		return stages.StageDeps{}, runState, false
	}
}
