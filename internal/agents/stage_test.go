package agents_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/principles"
)

// stageAgentInvocation is the simplest thing a stage body could ask an agent
// for: its own words, in a directory of its own.
func stageAgentInvocation(t *testing.T) agents.Invocation {
	t.Helper()
	return agents.Invocation{
		Prompt: "say in one line what this change does",
		Shape:  agents.ShapeText,
		Dir:    t.TempDir(),
	}
}

// stageAgentRunner is a scripted agent reached through the production adapter,
// so what these tests hold is a property of this package rather than of a
// double that states typed values directly.
func stageAgentRunner(t *testing.T, reply standin.Reply) agents.Runner {
	t.Helper()
	return standin.New(t, standin.Script{Steps: []standin.Step{{Reply: reply}}}).Runner()
}

// TestAStageAgentRunsTheAgentItWraps establishes the half without which the
// P4 test below would be worthless.
//
// Every assertion in TestAStageAgentIsNotARouteToAFixerSession would hold of a
// wrapper that ran nothing at all, so a StageAgent that had quietly stopped
// working would pass it. This drives one real invocation through the
// production adapter and reads the agent's own words back, so the guarantee is
// asserted over a wrapper that still does its job.
func TestAStageAgentRunsTheAgentItWraps(t *testing.T) {
	t.Parallel()
	runner := stageAgentRunner(t, standin.Text("it narrows the loop bound in Total"))

	result, err := agents.NewStageAgent(runner).Run(
		t.Context(), agents.PurposeReview, stageAgentInvocation(t))
	if err != nil {
		t.Fatalf("the stage agent refused an invocation the runner it wraps accepts: %v", err)
	}
	if result.Text == "" {
		t.Fatal("the stage agent produced no text, so the P4 assertions would be holding " +
			"of a wrapper that runs nothing")
	}
}

// TestTheZeroStageAgentRefusesRatherThanPanicking holds the reachable zero
// value to a typed result. A StageAgent is a field on a struct a caller
// builds, so the value that was never given a runner is not hypothetical, and
// a stage that ran nothing and reported success would be the failure the whole
// gate exists to prevent.
func TestTheZeroStageAgentRefusesRatherThanPanicking(t *testing.T) {
	t.Parallel()

	_, err := agents.StageAgent{}.Run(t.Context(), agents.PurposeReview, stageAgentInvocation(t))
	if err == nil {
		t.Fatal("the zero StageAgent ran an invocation, so a body given no agent reports a pass it did not establish")
	}
	if !errors.Is(err, agents.ErrNoStageAgent) {
		t.Fatalf("the zero StageAgent refused with %v, want ErrNoStageAgent", err)
	}
}

// TestAStageAgentIsNotARouteToAFixerSession is P4 as this type expresses it.
//
// P4 keeps reviewing and fixing in separate memory. The type split on Runner
// and Fixer is where that lives, but the split does not survive a stage body
// being handed a Runner: agents.OpenFixer opens a fixer session from any
// Runner it is given, so a body holding one reaches a session by calling an
// exported function of this package. A StageAgent is what a body holds
// instead, and the assertions below are the three routes a body could try.
//
// The premise is checked first and is what keeps this from passing vacuously.
// If the runner being wrapped were not itself a route to a session, the
// wrapper would be removing nothing and every assertion would hold for the
// wrong reason. So this establishes that OpenFixer would succeed on the
// runner - it declares the capability and it is a SessionRunner, which are the
// two things OpenFixer asks - before asserting that the wrapper is not.
func TestAStageAgentIsNotARouteToAFixerSession(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P4)

	runner := stageAgentRunner(t, standin.Text("anything"))

	if !runner.Capabilities().Has(agents.CapabilityResumableSessions) {
		t.Fatal("the runner under test does not declare resumable sessions, so OpenFixer would " +
			"refuse it anyway and wrapping it removes no route")
	}
	if _, ok := any(runner).(agents.SessionRunner); !ok {
		t.Fatal("the runner under test is not a SessionRunner, so wrapping it removes no route " +
			"and the assertions below would prove nothing")
	}

	stage := agents.NewStageAgent(runner)

	if _, ok := any(stage).(agents.Runner); ok {
		t.Error("a StageAgent asserts to agents.Runner, so a stage body holding one could pass " +
			"it to agents.OpenFixer and reach a fixer session")
	}
	if _, ok := any(stage).(agents.SessionRunner); ok {
		t.Error("a StageAgent asserts to agents.SessionRunner, so a stage body holding one could " +
			"open a fixer session on it directly")
	}
	if _, ok := any(stage).(agents.Fixer); ok {
		t.Error("a StageAgent asserts to agents.Fixer, so a stage body holding one already has " +
			"the session P4 keeps it out of")
	}
}

// TestNoRouteFromAStageAgentToAFixerSession is the general form of the
// guarantee stage_test.go's assertions state specifically, and it is the one
// that catches a route nobody enumerated.
//
// It walks what a caller outside this package can reach from a StageAgent -
// exported fields, and the results of exported methods - and fails naming any
// path that is or yields a session. The specific assertions above stay green
// when StageAgent is given a Runner accessor; this one does not, which was
// checked by adding that accessor and watching this fail.
func TestNoRouteFromAStageAgentToAFixerSession(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P4)

	if routes := routesToAFixerSession(reflect.TypeOf(agents.StageAgent{})); len(routes) != 0 {
		t.Fatalf("a stage body can reach a fixer session from a StageAgent, so P4 is a rule "+
			"callers follow rather than a mechanism:\n  %s", strings.Join(routes, "\n  "))
	}
}

// TestTheRouteWalkFindsARouteThatExists is what keeps the test above from
// passing because the walk looks at nothing.
//
// agents.Resolution is a real type in this package with an exported Runner
// field, which is exactly the shape a Deps struct would take if someone wired
// one carelessly, so it is a route the walk must report. A walk that returned
// nothing for everything would pass the guarantee test forever.
func TestTheRouteWalkFindsARouteThatExists(t *testing.T) {
	t.Parallel()

	routes := routesToAFixerSession(reflect.TypeOf(agents.Resolution{}))
	if len(routes) == 0 {
		t.Fatal("the walk found no route out of agents.Resolution, which has an exported " +
			"Runner field, so it is not looking at anything and the guarantee test above is vacuous")
	}
}
