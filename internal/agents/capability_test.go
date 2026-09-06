package agents_test

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
)

// The conformance suite. Its rule is that what an adapter declares is what it
// is held to, so an adapter that declares nothing is tested as having nothing
// and a declaration is never taken on trust where there is a way to check it.
//
// Each capability gets a probe pair: what must hold of an adapter that
// declared it, and what must hold of one that did not. conform picks between
// them by reading the declaration, which is the whole point - an adapter is
// not asked to say what to test it for.

// probe is one capability's conformance pair. declared is run against an
// adapter that declared the capability and undeclared against one that did
// not; either may be nil where this package has no way to tell, and a nil
// declared probe is a refusal rather than a pass, per the rule conform
// applies.
type probe struct {
	capability agents.Capability
	declared   func(t *testing.T, r agents.Runner)
	undeclared func(t *testing.T, r agents.Runner)
}

var probes = []probe{{
	capability: agents.CapabilityResumableSessions,
	declared: func(t *testing.T, r agents.Runner) {
		t.Helper()
		if _, ok := r.(agents.SessionRunner); !ok {
			t.Errorf("%s declares %s and is no SessionRunner, so nothing can open a session on it",
				r.Name(), agents.CapabilityResumableSessions)
		}
		// The route a caller takes reaches the adapter's own mechanism. What
		// that mechanism then answers is the adapter's business; what is
		// asserted here is that the declaration did not stand in its way.
		if _, err := agents.OpenFixer(t.Context(), r, ""); errors.Is(err, agents.ErrUndeclaredCapability) {
			t.Errorf("%s declares %s and OpenFixer refused it as undeclared: %v",
				r.Name(), agents.CapabilityResumableSessions, err)
		}
	},
	undeclared: func(t *testing.T, r agents.Runner) {
		t.Helper()
		// The type is the load-bearing half: an adapter with no session
		// mechanism has no method a caller could reach one through, so running
		// review and fix in one session is not something it can express.
		if _, ok := r.(agents.SessionRunner); ok {
			t.Errorf("%s has not declared %s and carries the mechanism anyway",
				r.Name(), agents.CapabilityResumableSessions)
		}
		_, err := agents.OpenFixer(t.Context(), r, "")
		var refusal *agents.CapabilityError
		if !errors.As(err, &refusal) {
			t.Fatalf("%s has not declared %s and OpenFixer answered %v, want a *CapabilityError",
				r.Name(), agents.CapabilityResumableSessions, err)
		}
		if refusal.Capability != agents.CapabilityResumableSessions {
			t.Errorf("the refusal names %q, want %q", refusal.Capability, agents.CapabilityResumableSessions)
		}
		if refusal.Agent != r.Name() {
			t.Errorf("the refusal names agent %q, want %q", refusal.Agent, r.Name())
		}
	},
}}

// probeFor returns the pair for a capability, and false when it has none.
func probeFor(capability agents.Capability) (probe, bool) {
	for _, p := range probes {
		if p.capability == capability {
			return p, true
		}
	}
	return probe{}, false
}

// conform holds one adapter to its own declaration, capability by capability.
//
// A capability the adapter declared and this suite cannot probe fails here.
// That is the rule that keeps a declaration from being a comment: a capability
// may be declared only where something checks it, so the day an adapter claims
// instruction suppression, whoever wrote it owes a probe before the claim
// counts for anything.
func conform(t *testing.T, r agents.Runner) {
	t.Helper()
	declared := r.Capabilities()
	for _, capability := range agents.AllCapabilities() {
		p, ok := probeFor(capability)
		if declared.Has(capability) {
			if !ok || p.declared == nil {
				t.Errorf("%s declares %s and nothing here checks it, so the declaration means nothing",
					r.Name(), capability)
				continue
			}
			p.declared(t, r)
			continue
		}
		if ok && p.undeclared != nil {
			p.undeclared(t, r)
		}
	}
}

// TestTheShippedAdapterConformsToItsDeclaration runs the suite against the
// production Claude Code adapter, built over the stand-in agent so what it
// proves is a property of this package rather than of whatever is installed.
func TestTheShippedAdapterConformsToItsDeclaration(t *testing.T) {
	conform(t, newRunner(t))
}

// TestAnAdapterWithoutSessionsConformsToItsDeclaration runs the same suite
// against a second adapter that declares nothing, which is the case the
// declaration exists for and the one this build has no real example of.
func TestAnAdapterWithoutSessionsConformsToItsDeclaration(t *testing.T) {
	conform(t, (&stubFactory{name: "sessionless", available: true}).runner())
}

// What the shipped adapter declares is a fact about it, so it is asserted
// rather than derived. Claude Code has resumable sessions, and it has no
// instruction-suppression mechanism, so a run configured to suppress a
// repository's own instructions is refused against it.
//
// The second half is the one worth spelling out. It is not an accident of the
// list: internal/agents implements no suppression mechanism at all, so a
// declaration of it here would be a claim with nothing behind it, and this is
// what fails if one is ever added without the mechanism.
func TestWhatTheShippedAdapterDeclares(t *testing.T) {
	declared := newRunner(t).Capabilities()
	want := []agents.Capability{agents.CapabilityResumableSessions}
	if got := declared.List(); !slices.Equal(got, want) {
		t.Errorf("the claude adapter declares %v, want exactly %v", got, want)
	}
	if declared.Has(agents.CapabilitySuppressProjectInstructions) {
		t.Errorf("the claude adapter declares %s, and nothing in this repository implements it",
			agents.CapabilitySuppressProjectInstructions)
	}
	if names := agents.DefaultCatalog().Names(); !slices.Equal(names, []string{agents.ClaudeName}) {
		t.Errorf("this build ships adapters %v, which this test does not cover", names)
	}
}

// Resolve holds an adapter to its declaration in both directions, and refuses
// the whole resolution rather than passing over the entry.
func TestResolveRefusesAnAdapterWhoseDeclarationDoesNotMatchWhatItIs(t *testing.T) {
	cases := []struct {
		name    string
		factory *stubFactory
		want    string
	}{{
		name: "declares sessions it cannot open",
		factory: &stubFactory{
			name:      "boaster",
			available: true,
			declares:  []agents.Capability{agents.CapabilityResumableSessions},
			sessions:  false,
		},
		want: "carries no mechanism",
	}, {
		name: "opens sessions it did not declare",
		factory: &stubFactory{
			name:      "quiet",
			available: true,
			sessions:  true,
		},
		want: "does not declare it",
	}, {
		name: "declares a capability this build does not define",
		factory: &stubFactory{
			name:      "inventive",
			available: true,
			declares:  []agents.Capability{"time_travel"},
		},
		want: "not a capability this build defines",
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// A working adapter sits behind the defective one, so a resolution
			// that fell through to it would look like a success. That is the
			// outcome this refusal exists to prevent.
			fallback := &stubFactory{name: "fallback", available: true}
			catalog := agents.NewCatalog(c.factory, fallback)

			got, err := agents.Resolve(t.Context(), []string{c.factory.name, "fallback"}, catalog)
			if !errors.Is(err, agents.ErrAdapterDeclaration) {
				t.Fatalf("resolution answered %v with runner %v, want ErrAdapterDeclaration",
					err, got.Runner)
			}
			var defect *agents.AdapterError
			if !errors.As(err, &defect) {
				t.Fatalf("the refusal is not an *AdapterError: %v", err)
			}
			if defect.Agent != c.factory.name {
				t.Errorf("the refusal names %q, want %q", defect.Agent, c.factory.name)
			}
			if !strings.Contains(defect.Error(), c.want) {
				t.Errorf("the refusal reads %q, want it to say %q", defect.Error(), c.want)
			}
		})
	}
}

// What Resolve reports is the resolved adapter's own declaration, so a caller
// that refuses a path before building it reads the same answer the adapter
// would give.
func TestResolveReportsTheAdapterDeclaration(t *testing.T) {
	factory := &stubFactory{
		name:      "sessions",
		available: true,
		declares:  []agents.Capability{agents.CapabilityResumableSessions},
		sessions:  true,
	}
	got, err := agents.Resolve(t.Context(), []string{"sessions"}, agents.NewCatalog(factory))
	if err != nil {
		t.Fatalf("resolving an adapter that agrees with itself: %v", err)
	}
	if !got.Capabilities.Has(agents.CapabilityResumableSessions) {
		t.Errorf("the resolution declares %v, want resumable sessions", got.Capabilities)
	}
	if !reflect.DeepEqual(got.Capabilities.List(), got.Runner.Capabilities().List()) {
		t.Errorf("the resolution declares %v and its runner declares %v",
			got.Capabilities, got.Runner.Capabilities())
	}
}

// OpenFixer reaches the adapter's mechanism once the declaration allows it,
// and refuses before reaching it when the declaration does not. The pair is
// what makes the declaration decide rather than describe.
func TestOpenFixerConsultsTheDeclarationBeforeTheMechanism(t *testing.T) {
	declared := (&stubFactory{
		name:      "declared",
		available: true,
		declares:  []agents.Capability{agents.CapabilityResumableSessions},
		sessions:  true,
	}).runner()
	if _, err := agents.OpenFixer(t.Context(), declared, ""); !errors.Is(err, errStubSessionReached) {
		t.Errorf("OpenFixer on a declared adapter answered %v, want the adapter's own mechanism", err)
	}

	// The same mechanism, undeclared. Reaching it would be the substitution
	// PRD section 8 refuses, so the refusal must come from the declaration and
	// not from the type.
	undeclared := &sessionStubRunner{stubRunner: &stubRunner{name: "undeclared"}}
	_, err := agents.OpenFixer(t.Context(), undeclared, "")
	if errors.Is(err, errStubSessionReached) {
		t.Fatal("OpenFixer reached the session mechanism of an adapter that declared nothing")
	}
	if !errors.Is(err, agents.ErrUndeclaredCapability) {
		t.Errorf("OpenFixer answered %v, want ErrUndeclaredCapability", err)
	}
}

// An adapter that declares sessions and carries no mechanism is a defect
// Resolve refuses, and OpenFixer is reached by a caller that built a Runner
// without going through Resolve, which every test in this repository and
// internal/agents/standin do. It refuses there too rather than returning a nil
// Fixer with a nil error, which is what a caller would otherwise dereference.
func TestOpenFixerRefusesAnAdapterThatDeclaresSessionsItCannotOpen(t *testing.T) {
	boaster := &stubRunner{
		name:     "boaster",
		declared: agents.Declare(agents.CapabilityResumableSessions),
	}
	fixer, err := agents.OpenFixer(t.Context(), boaster, "")
	if fixer != nil {
		t.Errorf("OpenFixer returned %v for an adapter with no mechanism, want nothing", fixer)
	}
	var defect *agents.AdapterError
	if !errors.As(err, &defect) {
		t.Fatalf("OpenFixer answered %v, want an *AdapterError", err)
	}
	if defect.Agent != "boaster" {
		t.Errorf("the refusal names %q, want %q", defect.Agent, "boaster")
	}
}

// The Runner interface is where the sessionless adapter's inability lives, so
// it is asserted the way session_test.go asserts the rest of P4's arrangement:
// on the type, where adding a method would break it.
func TestRunnerCarriesNoRouteToASession(t *testing.T) {
	runner := reflect.TypeFor[agents.Runner]()
	for i := range runner.NumMethod() {
		method := runner.Method(i)
		if method.Type.NumOut() > 0 && method.Type.Out(0) == reflect.TypeFor[agents.Fixer]() {
			t.Errorf("Runner.%s returns a Fixer, so an adapter with no sessions could still be asked for one",
				method.Name)
		}
	}
	if _, ok := reflect.TypeFor[agents.SessionRunner]().MethodByName("Fixer"); !ok {
		t.Error("SessionRunner has no Fixer method, so nothing distinguishes an adapter that has sessions")
	}
}

// An unrecognized capability satisfies nothing, whatever a declaration says
// about it, so a typo can only refuse a path and never open one.
func TestAnUnrecognizedCapabilityIsNeverHad(t *testing.T) {
	declared := agents.Declare("resumable_session", agents.CapabilityResumableSessions)
	if declared.Has("resumable_session") {
		t.Error("a capability this build does not define was had")
	}
	if !declared.Has(agents.CapabilityResumableSessions) {
		t.Error("an unrecognized capability alongside a recognized one lost the recognized one")
	}
	if agents.Capability("resumable_session").Recognized() {
		t.Error("a capability this build does not define reported itself recognized")
	}
}

// The zero declaration is the one an adapter that says nothing has, and it has
// nothing.
func TestTheZeroDeclarationHasNothing(t *testing.T) {
	var none agents.Capabilities
	for _, capability := range agents.AllCapabilities() {
		if none.Has(capability) {
			t.Errorf("the zero Capabilities has %s", capability)
		}
	}
	if got := none.List(); len(got) != 0 {
		t.Errorf("the zero Capabilities lists %v, want nothing declared", got)
	}
	if none.String() != "none" {
		t.Errorf("the zero Capabilities renders as %q, want %q", none.String(), "none")
	}
}

// List renders a declaration in table order whatever order it was declared in,
// so one declaration reads the same on every run.
func TestListIsStableWhateverOrderWasDeclared(t *testing.T) {
	table := agents.AllCapabilities()
	reversed := slices.Clone(table)
	slices.Reverse(reversed)

	forwards, backwards := agents.Declare(table...), agents.Declare(reversed...)
	if !slices.Equal(forwards.List(), backwards.List()) {
		t.Errorf("declaring in two orders lists as %v and %v", forwards.List(), backwards.List())
	}
	if !slices.Equal(forwards.List(), table) {
		t.Errorf("List returned %v, want the table order %v", forwards.List(), table)
	}
}
