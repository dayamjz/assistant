package agents_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
)

// The stubs below model a second agent adapter, which is what the capability
// declaration exists for and what this build does not yet have. They state a
// declaration and a shape and nothing else: neither returns an agents.Result,
// an agents.Fixer, or an *agents.InvocationError, so nothing here can hand a
// test a value the real adapter could not have built. What they are asked is
// only what an adapter says about itself and what its type carries, which is
// exactly the pair internal/agents checks.

// stubFactory is an adapter that is available or not, on demand, and remembers
// the flags the configuration entry carried. declares is what its runner
// declares, and sessions says whether that runner carries the session
// mechanism, so a test can build an adapter whose declaration and type agree
// and one whose do not.
type stubFactory struct {
	name      string
	available bool
	declares  []agents.Capability
	sessions  bool
	args      []string
}

func (f *stubFactory) Name() string { return f.name }

func (f *stubFactory) New(_ context.Context, args []string) (agents.Runner, error) {
	if !f.available {
		return nil, errors.New(f.name + " is not installed here")
	}
	f.args = args
	return f.runner(), nil
}

// runner builds the adapter's runner without going through availability, for a
// test asking about a declaration rather than about resolution.
func (f *stubFactory) runner() agents.Runner {
	base := &stubRunner{name: f.name, declared: agents.Declare(f.declares...)}
	if f.sessions {
		return &sessionStubRunner{stubRunner: base}
	}
	return base
}

// stubRunner is an adapter with no session mechanism: it has no Fixer method,
// which is the shape an agent that cannot reopen a conversation has, and it is
// why such an adapter has nothing to satisfy the fixer path with.
type stubRunner struct {
	name     string
	declared agents.Capabilities
}

func (r *stubRunner) Name() string { return r.name }

func (r *stubRunner) Capabilities() agents.Capabilities { return r.declared }

func (r *stubRunner) Run(context.Context, agents.Purpose, agents.Invocation) (agents.Result, error) {
	return agents.Result{}, errors.New("the stub runner runs nothing")
}

// sessionStubRunner carries the session mechanism. Its Fixer refuses rather
// than returning a session, because a stub session would have to produce
// agents.Result values the real adapter builds out of a process's output. What
// a test reads from this refusal is that OpenFixer got past the declaration
// and reached the mechanism, which is the only thing it is asked.
type sessionStubRunner struct{ *stubRunner }

func (r *sessionStubRunner) Fixer(context.Context, string) (agents.Fixer, error) {
	return nil, errStubSessionReached
}

// errStubSessionReached is what a stub adapter's session mechanism answers
// with. It is a sentinel so a test asserts on reaching the mechanism rather
// than on the wording of a message.
var errStubSessionReached = errors.New("the stub adapter opens no session")

func TestResolvePicksTheFirstAvailableAndReportsWhichOneItWas(t *testing.T) {
	first := &stubFactory{name: "first", available: false}
	second := &stubFactory{name: "second", available: true}
	third := &stubFactory{name: "third", available: true}
	catalog := agents.NewCatalog(first, second, third)

	got, err := agents.Resolve(t.Context(), []string{"first", "second", "third"}, catalog)
	if err != nil {
		t.Fatalf("resolution refused with an available agent in the list: %v", err)
	}
	if got.Name != "second" {
		t.Errorf("resolved %q, want the first available which is \"second\"", got.Name)
	}
	if got.Index != 1 {
		t.Errorf("reported index %d, want 1", got.Index)
	}
	if got.Runner == nil || got.Runner.Name() != "second" {
		t.Errorf("returned a runner for %v, want one for \"second\"", got.Runner)
	}
	if len(got.Skipped) != 1 || got.Skipped[0].Name != "first" {
		t.Fatalf("skipped list is %+v, want exactly the unavailable \"first\"", got.Skipped)
	}
	if !strings.Contains(got.Skipped[0].Err.Error(), "not installed") {
		t.Errorf("the skipped entry does not say why: %v", got.Skipped[0].Err)
	}
	// Nothing after the winner was built, so a fallback list does not start
	// every agent it names.
	if third.args != nil {
		t.Error("an agent after the resolved one was constructed")
	}
}

func TestResolvePassesTheEntrysOwnFlagsToTheAdapter(t *testing.T) {
	only := &stubFactory{name: "only", available: true}
	if _, err := agents.Resolve(t.Context(), []string{"only --model a-model"}, agents.NewCatalog(only)); err != nil {
		t.Fatalf("resolution refused: %v", err)
	}
	if len(only.args) != 2 || only.args[0] != "--model" || only.args[1] != "a-model" {
		t.Errorf("adapter received %q, want the entry's own flags", only.args)
	}
}

func TestResolveExpandsAutoToTheCatalogOrder(t *testing.T) {
	first := &stubFactory{name: "first", available: false}
	second := &stubFactory{name: "second", available: true}
	catalog := agents.NewCatalog(first, second)

	got, err := agents.Resolve(t.Context(), []string{agents.AutoEntry}, catalog)
	if err != nil {
		t.Fatalf("auto refused with an available agent in the catalog: %v", err)
	}
	if got.Name != "second" {
		t.Errorf("auto resolved %q, want \"second\"", got.Name)
	}
	if got.Entry != "second" {
		t.Errorf("reported entry %q, want the expansion rather than the word auto", got.Entry)
	}
}

func TestResolveRefusesWhenNothingResolves(t *testing.T) {
	catalog := agents.NewCatalog(&stubFactory{name: "first", available: false})

	_, err := agents.Resolve(t.Context(), []string{"first", "absent"}, catalog)
	if err == nil {
		t.Fatal("resolution succeeded with nothing available")
	}
	if !errors.Is(err, agents.ErrNoAgent) {
		t.Fatalf("refusal does not match ErrNoAgent: %v", err)
	}
	var refusal *agents.ResolutionError
	if !errors.As(err, &refusal) {
		t.Fatalf("refusal is not a *ResolutionError: %v", err)
	}
	if len(refusal.Tried) != 2 {
		t.Fatalf("refusal lists %d entries, want both that were tried", len(refusal.Tried))
	}
	if !strings.Contains(refusal.Error(), "first") || !strings.Contains(refusal.Error(), "absent") {
		t.Errorf("refusal does not name every entry tried: %v", refusal)
	}
	if !strings.Contains(refusal.Tried[1].Err.Error(), "no adapter named absent") {
		t.Errorf("an unknown adapter is not explained: %v", refusal.Tried[1].Err)
	}
}

func TestResolveRefusesAnEmptyList(t *testing.T) {
	_, err := agents.Resolve(t.Context(), nil, agents.NewCatalog(&stubFactory{name: "a", available: true}))
	if !errors.Is(err, agents.ErrNoAgent) {
		t.Fatalf("an empty agent list resolved to %v, want a refusal", err)
	}
	if !strings.Contains(err.Error(), "no agent was configured") {
		t.Errorf("the refusal does not distinguish an empty list: %v", err)
	}
}

func TestCatalogReplacesAnAdapterRatherThanHoldingTwo(t *testing.T) {
	unavailable := &stubFactory{name: "same", available: false}
	available := &stubFactory{name: "same", available: true}
	catalog := agents.NewCatalog(unavailable, available)

	if names := catalog.Names(); len(names) != 1 || names[0] != "same" {
		t.Fatalf("catalog holds %q, want one entry", names)
	}
	got, err := agents.Resolve(t.Context(), []string{"same"}, catalog)
	if err != nil {
		t.Fatalf("the replacing adapter did not resolve: %v", err)
	}
	if got.Runner == nil {
		t.Fatal("resolution returned no runner")
	}
}

func TestDefaultCatalogHoldsTheClaudeAdapter(t *testing.T) {
	names := agents.DefaultCatalog().Names()
	want := []string{agents.CursorName, agents.ClaudeName, agents.OpenAIName, agents.GrokName}
	if len(names) != len(want) {
		t.Fatalf("the default catalog holds %d adapters, want %d", len(names), len(want))
	}
	for i, name := range want {
		if names[i] != name {
			t.Fatalf("the default catalog holds %q at position %d, want %q", names[i], i, name)
		}
	}
}

func TestClaudeFactoryRefusesAnAbsentInstallation(t *testing.T) {
	factory := agents.ClaudeFactory(agents.WithBinary(t.TempDir() + "/definitely-not-installed"))
	if _, err := factory.New(t.Context(), nil); err == nil {
		t.Fatal("an absent installation was reported as runnable")
	}
	// And the accepting side: the stand-in agent is a real executable.
	if _, err := agents.ClaudeFactory(agents.WithBinary(helperBinary(t))).New(t.Context(), nil); err != nil {
		t.Fatalf("a present installation was refused: %v", err)
	}
}
