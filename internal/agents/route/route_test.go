package route_test

import (
	"reflect"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/route"
	"github.com/dayamjz/assistant/internal/principles"
)

// Whether this walk works is one fact, so it is established here rather than
// re-established by every caller. A caller asserts that its own type has no
// route and relies on these; if the walk stopped inspecting anything, these
// fail and every caller's guarantee is reported as unproven in the same run.

// TestTheWalkFindsAnExposedRunner is the positive control the whole mechanism
// rests on.
//
// A walk that had stopped inspecting anything would return no routes, which
// reads exactly like the guarantee holding, so every caller would report P4
// intact forever. agents.Resolution has an exported Runner field and therefore
// really is a route to a fixer session, so finding nothing here means the walk
// is broken rather than that the type is safe.
func TestTheWalkFindsAnExposedRunner(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P4)

	found := route.ToFixerSession(reflect.TypeOf(agents.Resolution{}))
	if len(found) == 0 {
		t.Fatal("the walk reports no route out of agents.Resolution, which has an exported " +
			"Runner field, so it is inspecting nothing and every caller's guarantee is vacuous")
	}
}

// carelessDeps is the shape a dependency struct takes when someone wires it
// without thinking about P4: not a Runner itself, and not holding one
// directly, but carrying a value that hands one over.
//
// It is what this package exists for. No assertion on carelessDeps asserts to
// agents.Runner, and neither does one on agents.Resolution, so the route
// exists only across two hops and only a walk finds it.
type carelessDeps struct {
	Agent      agents.StageAgent
	Resolution agents.Resolution
}

// TestTheWalkFollowsARouteMoreThanOneHopDeep is the property that makes this a
// walk rather than a list of assertions.
//
// The realistic regression is not a deps struct that is a Runner. It is one
// that carries something ordinary which happens to expose a Runner two fields
// down, which is how agents.Resolution reaches a body: it is what
// agents.Resolve returns, so it is the obvious thing to put on a deps struct.
func TestTheWalkFollowsARouteMoreThanOneHopDeep(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P4)

	if _, direct := any(carelessDeps{}).(agents.Runner); direct {
		t.Fatal("carelessDeps is itself a Runner, so this checks a shallower case than intended")
	}
	found := route.ToFixerSession(reflect.TypeOf(carelessDeps{}))
	if len(found) == 0 {
		t.Fatal("the walk misses a Runner two hops down, so it catches only what a plain " +
			"assertion would and buys nothing")
	}
}

// TestTheWalkClearsATypeThatExposesNothing keeps the walk from passing every
// caller by reporting a route in everything.
//
// A walk that answered "there is a route" unconditionally would fail every
// guarantee test rather than pass it, which is loud rather than silent, but it
// would also make the mechanism useless and it would be fixed by weakening it.
// agents.StageAgent exposes only Run, so it must come back clean.
func TestTheWalkClearsATypeThatExposesNothing(t *testing.T) {
	t.Parallel()

	if found := route.ToFixerSession(reflect.TypeOf(agents.StageAgent{})); len(found) != 0 {
		t.Fatalf("the walk reports a route out of agents.StageAgent, which exposes only Run: %v", found)
	}
}
