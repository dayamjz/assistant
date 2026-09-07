package route_test

import (
	"context"
	"errors"
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

// constructedRunnerDeps, deliveredFixerDeps, keyedRunnerDeps and
// addressableFixerDeps are the four shapes a value can be held in that a walk
// over fields and method results alone reads straight past.
//
// None of them is contrived. A constructor-valued field is what
// service.Options already uses to hand a fixer over, so it is the next thing a
// deps struct grows; a channel is how a value gets delivered to a body that
// did not build it; a map keyed by an adapter is how a caller names several;
// and a method set on the pointer is the default a Go author gets without
// asking, since a value in a struct field is addressable.
type constructedRunnerDeps struct {
	NewRunner func() agents.Runner
}

type deliveredFixerDeps struct {
	Sessions chan agents.Fixer
}

type keyedRunnerDeps struct {
	Named map[agents.Runner]string
}

type addressableFixerDeps struct {
	Session pointerOnlyFixer
}

// promotedRunnerDeps holds its Runner where a reader of the struct definition
// would not look for it and where a caller reaches it anyway: Go promotes an
// embedded type's exported fields, so a body writes deps.Runner even though
// the embedded field's own name is unexported and even though the type
// carrying it is not exported at all.
//
// The walk's rule that unexported fields are unreachable is what StageAgent's
// mechanism rests on, so it stays. Embedding is what makes that rule not apply
// here, and the difference is one bool on the field.
type promotedRunnerDeps struct {
	carriedRunner
	Agent agents.StageAgent
}

type carriedRunner struct {
	Runner agents.Runner
}

// pointerOnlyFixer is an agents.Fixer on its pointer and not on its value,
// which is what a Go author writes by default. A caller holding one in a field
// reaches the session by writing an ampersand.
type pointerOnlyFixer struct{}

func (*pointerOnlyFixer) Apply(context.Context, agents.Invocation) (agents.Result, error) {
	return agents.Result{}, errors.New("route: this fixer exists to be reachable, not to run")
}

func (*pointerOnlyFixer) Reference() string { return "" }

// TestTheWalkFindsARouteHeldInAnyShape is the rest of the positive control,
// one case per shape the walk has to follow.
//
// The control over agents.Resolution establishes that the walk inspects
// something, and a walk that inspected only plain fields would pass it while
// missing every case here. Each of these types hands a body a fixer session
// and none of them is one, so a walk that reports nothing for them reports the
// guarantee for a deps struct that has already lost it.
func TestTheWalkFindsARouteHeldInAnyShape(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P4)

	for _, c := range []struct {
		shape string
		root  reflect.Type
	}{
		{"a func field that returns one", reflect.TypeOf(constructedRunnerDeps{})},
		{"a channel field that carries one", reflect.TypeOf(deliveredFixerDeps{})},
		{"a map keyed by one", reflect.TypeOf(keyedRunnerDeps{})},
		{"a field whose pointer is one", reflect.TypeOf(addressableFixerDeps{})},
		{"a field promoted from an unexported embedded type", reflect.TypeOf(promotedRunnerDeps{})},
	} {
		t.Run(c.shape, func(t *testing.T) {
			t.Parallel()

			for _, iface := range []reflect.Type{
				reflect.TypeOf((*agents.Runner)(nil)).Elem(),
				reflect.TypeOf((*agents.SessionRunner)(nil)).Elem(),
				reflect.TypeOf((*agents.Fixer)(nil)).Elem(),
			} {
				if c.root.Implements(iface) || reflect.PointerTo(c.root).Implements(iface) {
					t.Fatalf("%s is itself %s, so this checks the shallow case a plain "+
						"assertion already catches rather than %s", c.root, iface, c.shape)
				}
			}
			if found := route.ToFixerSession(c.root); len(found) == 0 {
				t.Fatalf("the walk reports no route out of %s, which holds a fixer session in %s, "+
					"so a deps struct written that way is reported safe while handing a body one",
					c.root, c.shape)
			}
		})
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

// runnerCarrier is a value satisfying an ordinary interface that has nothing
// to do with running an agent, and an agents.Runner as well. It is the shape a
// forge provider or any other adapter takes when it happens to implement both:
// nothing in the field's static type says so, and a body that guesses reaches
// a fixer session through agents.OpenFixer.
type runnerCarrier struct{}

func (runnerCarrier) Carry() string { return "" }

func (runnerCarrier) Name() string { return "route-carrier" }

func (runnerCarrier) Capabilities() agents.Capabilities { return agents.Capabilities{} }

func (runnerCarrier) Run(context.Context, agents.Purpose, agents.Invocation) (agents.Result, error) {
	return agents.Result{}, errors.New("route: this runner exists to be reachable, not to run")
}

// carrier is the interface the field is typed as: methods, so the walk does
// not treat it as an empty interface, and no relation to agents.Runner.
type carrier interface{ Carry() string }

type interfaceCarrierDeps struct {
	Provider carrier
}

type concreteCarrierDeps struct {
	Provider runnerCarrier
}

// TestTheWalkCannotSeeThroughAnInterfaceField pins a gap this walk does not
// close, so it cannot reopen wider or narrower in silence.
//
// The walk reads static types, and an interface field's dynamic value is not
// in its static type. runnerCarrier satisfies carrier and agents.Runner both,
// so a body handed interfaceCarrierDeps asserts on Provider and has a Runner
// while the walk reports nothing. stages.StageDeps.Forge is that shape today.
//
// This is not fixed by flagging every non-empty interface: no static predicate
// separates an interface whose dynamic value might be a Runner from one that
// might not, so that would report every interface-typed field as a route and
// leave no deps struct able to hold one. The gap stays, and this test is what
// makes it visible - the concrete control holds the same value in a field the
// walk can read, so a walk that had stopped inspecting anything fails here
// rather than agreeing with the recorded gap.
func TestTheWalkCannotSeeThroughAnInterfaceField(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P4)

	var value any = runnerCarrier{}
	if _, ok := value.(carrier); !ok {
		t.Fatal("runnerCarrier does not satisfy carrier, so the field below holds something else")
	}
	if _, ok := value.(agents.Runner); !ok {
		t.Fatal("runnerCarrier is no longer an agents.Runner, so this pins nothing")
	}

	if found := route.ToFixerSession(reflect.TypeOf(concreteCarrierDeps{})); len(found) == 0 {
		t.Fatal("the walk reports no route out of a struct holding runnerCarrier in a concrete " +
			"field, so it is inspecting nothing and the gap recorded below is not the reason")
	}
	if found := route.ToFixerSession(reflect.TypeOf(interfaceCarrierDeps{})); len(found) != 0 {
		t.Fatalf("the walk now reports a route through an interface-typed field: %v. That is a "+
			"change to what internal/agents/route documents as its one gap. If interfaces are "+
			"flagged now, stages.StageDeps.Forge is a route too and that guarantee is unpassable; "+
			"update the package documentation and both callers deliberately rather than this line",
			found)
	}
}
