package stages_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
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
var bodied = []pipeline.Stage{pipeline.StageIntent, pipeline.StageReview, pipeline.StagePush}

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
// function values: Pending cannot fail and reports one finding it names after
// the stage it stands in for, so a body that fails on the state this hands it,
// or that reports no finding carrying that name, is not Pending.
//
// There is one condition rather than two. Comparing the whole report against
// Pending's stood beside the identifier check until this round, and it could
// reject nothing the identifier check did not already reject: equality admits
// exactly one report, Pending's own, and that report carries the identifier by
// construction, so the identifier condition fires on the only value the
// comparison ever fires on. A condition no control can show rejecting on its
// own is one condition wearing two names, so it went.
//
// What the comparison covered and the identifier does not is a body whose
// report matches Pending's in every field except the finding identifier. That
// body is not Pending, since Pending is what produces Pending's identifier, so
// what would be lost is a report imitating one - and TestImplementedIsTheSetThisBuildIsMeantToHave
// is what catches a stage that lost its body, which is the regression that
// imitation would stand in for.
//
// TestThePendingDiscriminatorRejectsAnImpersonationAndAcceptsARealBody is the
// control. It drives the same predicate this loop does, in both directions, so
// what is shown rejecting is the thing in use rather than a copy of it.
//
// Whether a body holds is not one of those signals, and it was one until the
// push stage landed. Holding for a person is what a body does when it cannot
// establish what it is there to establish, and a run state carrying nothing
// but an intent is exactly that case for every stage that reads the world.
// The intent stage's own never-blocks guarantee is PRD section 5's rule about
// that stage, checked in intent_test.go where it belongs, rather than a
// property of being written.
//
// The residual gap is that tolerance and the reader behind it. Treating a
// failure as proof the body is not Pending is what keeps this from having to
// know what a stage landing later needs of the world, and declaredReader
// answers a declared key it has no value for with the empty text, which for a
// stage reading a bool or a list is a value the real graph reader cannot
// produce. A body that failed on it would be tolerated here and this test
// would pass without checking that stage at all, so a stage needing more than
// run state has to be given it here rather than left to the tolerance.
func TestAllPlacesAWrittenBodyAtEveryImplementedStage(t *testing.T) {
	t.Parallel()
	implemented := stages.Implemented()
	if len(implemented) == 0 {
		t.Fatal("this build reports no stage bodies, so the loop below checks nothing; " +
			"TestImplementedIsTheSetThisBuildIsMeantToHave says which stages it should name")
	}
	all := stages.All(stages.StageDeps{})
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
			if problem := pendingImpersonation(report, pendingID(t, stage)); problem != "" {
				t.Fatalf("Implemented names %s, but %s", stage, problem)
			}
		})
	}
}

// pendingImpersonation says why a report is the one Pending produces rather
// than a body's own, and returns the empty string when it is a body's.
//
// It is a predicate returning a reason rather than a set of assertions so that
// a control can drive it directly and watch it answer in both directions,
// which is what keeps the loop above from being a guard nothing has shown
// rejecting.
func pendingImpersonation(report findings.Report, pendingFinding string) string {
	for _, found := range report.Findings {
		if found.ID == pendingFinding {
			return fmt.Sprintf("the body placed there reported %s's own finding %q, so a run "+
				"reaching it is told the stage is not implemented in this build: %+v",
				"Pending", pendingFinding, report)
		}
	}
	return ""
}

// The discriminator has to reject a body that reports what Pending reports and
// accept one that does not, and it is shown doing both here rather than only
// in the loop that uses it, where a guard that rejected nothing would look the
// same as one that had nothing to reject.
//
// The impersonation is Pending's own report, which is the strongest case
// available: nothing a body could produce is closer to Pending than what
// Pending produces. It is also what establishes the subsumption the loop's
// documentation states, because whole-report equality against Pending admits
// exactly that value and no other, so a predicate rejecting it rejects
// everything that comparison could have.
//
// The second case is the direction a check that rejects everything would fail.
// It is a real body's report rather than one written out here, so a rule this
// package's own bodies would trip is one this fails on.
func TestThePendingDiscriminatorRejectsAnImpersonationAndAcceptsARealBody(t *testing.T) {
	t.Parallel()
	stage := pipeline.StageIntent
	identifier := pendingID(t, stage)

	impersonation, err := stages.Pending(stage.String()).NewBody()(t.Context(), pipeline.Input{Stage: stage})
	if err != nil {
		t.Fatalf("running Pending for %s: %v", stage, err)
	}
	if problem := pendingImpersonation(impersonation.Report.Normalize(), identifier); problem == "" {
		t.Fatalf("the discriminator accepted %s's own report as a body's, so the loop that uses it "+
			"would pass on a stage All left pending: %+v", "Pending", impersonation.Report)
	}
	// The same report with everything but the identifier changed. Whole-report
	// equality against Pending would accept this one, which is what the
	// identifier condition covers and the removed one did not.
	disguised := impersonation.Report.Normalize()
	disguised.Summary = "the intent stage read the intent and reported on it"
	disguised.Findings[0].Description = "a description no Pending report carries"
	if problem := pendingImpersonation(disguised, identifier); problem == "" {
		t.Fatalf("the discriminator accepted a report carrying %q under different text, which is "+
			"the case whole-report equality misses: %+v", identifier, disguised)
	}

	real, err := implementationFor(t, stages.All(stages.StageDeps{}), stage).NewBody()(t.Context(),
		pipeline.Input{Stage: stage, State: declaredReader{
			allowed: map[pipeline.Key]bool{pipeline.KeyIntent: true, pipeline.KeyIntentSupplied: true},
			state: map[pipeline.Key]graph.Value{
				pipeline.KeyIntent:         graph.TextValue("add a greeting"),
				pipeline.KeyIntentSupplied: graph.BoolValue(true),
			},
		}})
	if err != nil {
		t.Fatalf("running the %s stage's body: %v", stage, err)
	}
	if problem := pendingImpersonation(real.Report.Normalize(), identifier); problem != "" {
		t.Fatalf("the discriminator rejected the %s stage's real body, so it rejects everything "+
			"and the loop that uses it checks nothing: %s", stage, problem)
	}
}

// pendingID is the finding identifier Pending reports for a stage, read off
// Pending itself rather than spelled out, so a change to how it names its
// finding cannot leave the discriminator checking for a name nothing produces
// any more.
//
// It fails the test rather than returning nothing when it cannot read one.
// findings.Report.Validate refuses an empty identifier, so a caller comparing
// against one would be comparing against a value no finding it sees can carry,
// and pendingImpersonation would accept every report.
func pendingID(t *testing.T, stage pipeline.Stage) string {
	t.Helper()
	out, err := stages.Pending(stage.String()).NewBody()(t.Context(), pipeline.Input{Stage: stage})
	if err != nil {
		t.Fatalf("running Pending for %s: %v", stage, err)
	}
	if len(out.Report.Findings) != 1 {
		t.Fatalf("Pending for %s reports %d findings, want exactly one: there is no single "+
			"identifier to tell a body's report apart from Pending's by", stage, len(out.Report.Findings))
	}
	return out.Report.Findings[0].ID
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
