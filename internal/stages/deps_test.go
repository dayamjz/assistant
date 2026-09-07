package stages_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/route"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/stages"
)

// TestStageDepsIsNoRouteToAFixerSession is where P4 binds the wiring rather
// than one type.
//
// internal/agents keeps reviewing and fixing in separate memory, and
// agents.StageAgent is what a body holds so that agents.OpenFixer has no
// Runner to be given. None of that survives a StageDeps that carries a route
// to one anyway: a field of a type exposing a Runner, an adapter handed over
// whole, or a method returning one would each put a fixer session back within
// a review body's reach, and every stage body in this package is handed this
// struct.
//
// So the question is asked of the type graph. An assertion that a StageDeps is
// not a Runner would answer whether it is one, and what matters is whether a
// body can obtain one: agents.StageAgent given a Runner accessor is not a
// Runner and hands one over, which is the regression that motivated
// internal/agents/route and is recorded there.
//
// That the walk itself still inspects anything is established in
// internal/agents/route, which holds it to a type that really does expose a
// Runner, so this does not re-establish it.
//
// This is the test the review and pull request stage bodies inherit. A field
// added to StageDeps that reaches an agent adapter fails here rather than in
// review.
func TestStageDepsIsNoRouteToAFixerSession(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P4)

	if found := route.ToFixerSession(reflect.TypeOf(stages.StageDeps{})); len(found) != 0 {
		t.Fatalf("a stage body can reach a fixer session from the dependencies it is given, "+
			"so P4 is a rule callers follow rather than a mechanism:\n  %s",
			strings.Join(found, "\n  "))
	}
}

// TestTheStageAgentInStageDepsStillRuns keeps the guarantee above from holding
// of a seam that hands a body an agent it cannot use.
//
// Every assertion about routes would pass for a StageDeps whose agent was
// inert, so this checks the other direction: the zero value refuses as a typed
// result rather than panicking, which is what a body given no agent has to
// meet.
func TestTheStageAgentInStageDepsStillRuns(t *testing.T) {
	t.Parallel()

	_, err := stages.StageDeps{}.Agent.Run(t.Context(), agents.PurposeReview, agents.Invocation{
		Prompt: "say what this change does",
		Shape:  agents.ShapeText,
		Dir:    t.TempDir(),
	})
	if err == nil {
		t.Fatal("the agent on a zero StageDeps ran an invocation, so a body wired with no agent " +
			"would report a stage it did not establish")
	}
}
