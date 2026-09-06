package runs_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/runs"
)

// A service missing something it cannot work without is refused at
// construction rather than on the first run that needed the missing thing.
func TestNewRefusesAnIncompleteService(t *testing.T) {
	s, _ := openStore(t)
	agent := resolution(standin.New(t, standin.Script{}))

	if _, err := runs.New(runs.Options{Agent: agent}); !errors.Is(err, runs.ErrIncompleteService) {
		t.Fatalf("New with no store answered %v, want ErrIncompleteService", err)
	}
	if _, err := runs.New(runs.Options{Store: s}); !errors.Is(err, runs.ErrIncompleteService) {
		t.Fatalf("New with no agent answered %v, want ErrIncompleteService", err)
	}
}

// Session reuse against an adapter that has not declared resumable sessions is
// refused before any run exists, naming the capability. The declaration is
// what decides: the same service builds once it carries the capability, so the
// refusal is not about the field being set at all.
//
// What is stated here is a declaration, which is what Options.Agent carries
// and what a second adapter without sessions would report of itself. This
// build ships one adapter and it can keep a session, so the declaration is the
// only place that difference can come from today.
func TestNewRefusesSessionReuseAgainstAnAdapterThatDeclaresNoSession(t *testing.T) {
	s, _ := openStore(t)
	agent := resolution(standin.New(t, standin.Script{}))
	undeclared := agent
	undeclared.Capabilities = agents.Declare()

	_, err := runs.New(runs.Options{Store: s, Agent: undeclared, SessionReuse: true})
	var refusal *agents.CapabilityError
	if !errors.As(err, &refusal) {
		t.Fatalf("New answered %v, want a *agents.CapabilityError", err)
	}
	if !errors.Is(err, agents.ErrUndeclaredCapability) {
		t.Errorf("the refusal does not match ErrUndeclaredCapability: %v", err)
	}
	if refusal.Capability != agents.CapabilityResumableSessions {
		t.Errorf("the refusal names %q, want %q", refusal.Capability, agents.CapabilityResumableSessions)
	}
	if !strings.Contains(refusal.Path, "fixer session") {
		t.Errorf("the refusal names the path %q, want it to name the fixer session", refusal.Path)
	}

	// The same adapter with the same runner, declaring what it carries.
	if _, err := runs.New(runs.Options{Store: s, Agent: agent, SessionReuse: true}); err != nil {
		t.Fatalf("New against a declared session: %v", err)
	}
	// And with no session asked for, an adapter that declares nothing is fine:
	// that run keeps no memory across rounds, which is a mode rather than a
	// shortfall.
	if _, err := runs.New(runs.Options{Store: s, Agent: undeclared}); err != nil {
		t.Fatalf("New without session reuse against an adapter declaring nothing: %v", err)
	}
}

// FixerRequires and New's refusal are one table. Every capability the fixer
// path says it needs is one New refuses a service without, and a service that
// keeps no session needs nothing.
func TestFixerRequiresIsWhatNewRefusesAServiceWithout(t *testing.T) {
	s, _ := openStore(t)
	agent := resolution(standin.New(t, standin.Script{}))

	keeping := service(t, s, agent, true)
	needed := keeping.FixerRequires()
	if len(needed) == 0 {
		t.Fatal("a service keeping a durable session needs nothing of the adapter")
	}
	for _, capability := range needed {
		without := agent
		without.Capabilities = agents.Declare(remove(agents.AllCapabilities(), capability)...)
		_, err := runs.New(runs.Options{Store: s, Agent: without, SessionReuse: true})
		var refusal *agents.CapabilityError
		if !errors.As(err, &refusal) || refusal.Capability != capability {
			t.Errorf("New against an adapter without %q answered %v, want a refusal naming it", capability, err)
		}
	}

	free := service(t, s, agent, false)
	if got := free.FixerRequires(); len(got) != 0 {
		t.Errorf("a session-free service needs %v of the adapter, want nothing", got)
	}
	if keeping.SessionReuse() == free.SessionReuse() {
		t.Error("the two services report the same session reuse")
	}
}

// remove returns caps without one of them.
func remove(caps []agents.Capability, drop agents.Capability) []agents.Capability {
	return slices.DeleteFunc(slices.Clone(caps), func(c agents.Capability) bool { return c == drop })
}
