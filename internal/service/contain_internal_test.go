package service

import (
	"context"
	"errors"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
)

// The two halves of the containment seam are pinned in two places, and this
// file is the service's half. internal/agents' TestStarted tests drive the
// real adapter over the stand-in and pin what an adapter does with
// Invocation.Started: told once when the process exists, with a live group,
// and the ended function runs before Run returns. What is left for this file
// is what the wrapper puts IN that field: a registration into this service's
// registry, keyed by the run the segment context names and the invocation's
// purpose, that adds on start and forgets on ended. The probes below stand in
// for the adapter at exactly that seam - they call the field the way the
// pinned adapter does and produce no agent output at all, only a refusal - so
// no shape the real mechanism cannot produce is stated anywhere here.

// errProbe is what every probe returns, so a test knows its assertions ran
// because the call came back with it.
var errProbe = errors.New("seam probe: the invocation stops here")

// startedProbe exercises an Invocation.Started value the way the adapter
// does, and asserts the registration it makes against the registry it is
// given.
type startedProbe struct {
	t         *testing.T
	registry  *registry
	wantRun   string
	wantStage string
	asserted  bool
}

func (p *startedProbe) exercise(inv agents.Invocation) {
	p.t.Helper()
	p.asserted = true
	if inv.Started == nil {
		p.t.Fatal("the wrapper set no Started, so nothing this invocation runs would be registered")
	}
	const pgid = 4242
	ended := inv.Started(pgid)
	got, ok := p.registry.lookup(pgid)
	if !ok {
		p.t.Fatal("Started registered nothing, so a caller inside this invocation is not contained")
	}
	if got.run != p.wantRun || got.stage != p.wantStage {
		p.t.Errorf("the invocation is registered as run %q stage %q, want run %q stage %q",
			got.run, got.stage, p.wantRun, p.wantStage)
	}
	ended()
	if _, still := p.registry.lookup(pgid); still {
		p.t.Error("the registration outlived the ended call, so a finished stage would contain forever")
	}
}

// probeRunner is a Runner-shaped stand-in for the adapter's side of the
// Started seam and nothing else; the file comment says why that does not
// repeat the dead-guard mistake internal/agents/standin exists against.
type probeRunner struct{ probe *startedProbe }

func (r probeRunner) Name() string                      { return "probe" }
func (r probeRunner) Capabilities() agents.Capabilities { return agents.Capabilities{} }
func (r probeRunner) Run(_ context.Context, _ agents.Purpose, inv agents.Invocation) (
	agents.Result, error) {
	r.probe.exercise(inv)
	return agents.Result{}, errProbe
}

func TestEveryInvocationOfTheResolvedAgentIsRegisteredWhileItRuns(t *testing.T) {
	s := &Service{registry: newRegistry()}
	probe := &startedProbe{t: t, registry: s.registry, wantRun: "run-1", wantStage: "review"}
	wrapped := s.containRunner(probeRunner{probe: probe})
	if _, ok := wrapped.(agents.SessionRunner); ok {
		t.Fatal("wrapping gave a session-free runner a session mechanism, which agents.Resolve vouched it has not")
	}

	ctx := withAdvancing(t.Context(), "run-1")
	if _, err := wrapped.Run(ctx, agents.PurposeReview, agents.Invocation{}); !errors.Is(err, errProbe) {
		t.Fatalf("the wrapped runner answered %v, want the probe's refusal", err)
	}
	if !probe.asserted {
		t.Fatal("the probe never ran, so nothing here was checked")
	}
	if !s.registry.empty() {
		t.Fatal("the registry still holds an entry after the invocation returned")
	}
}

// probeSessionRunner is probeRunner with the session mechanism, so the
// wrapper's fixer path can be driven the same way.
type probeSessionRunner struct {
	probeRunner
	fixer *probeFixer
}

func (r probeSessionRunner) Fixer(context.Context, string) (agents.Fixer, error) {
	return r.fixer, nil
}

type probeFixer struct{ probe *startedProbe }

func (f probeFixer) Apply(_ context.Context, inv agents.Invocation) (agents.Result, error) {
	f.probe.exercise(inv)
	return agents.Result{}, errProbe
}

func (f probeFixer) Reference() string { return "probe-session" }

func TestAFixRoundOfTheResolvedAgentIsRegisteredWhileItRuns(t *testing.T) {
	s := &Service{registry: newRegistry()}
	probe := &startedProbe{t: t, registry: s.registry, wantRun: "run-2",
		wantStage: string(agents.PurposeFix)}
	wrapped := s.containRunner(probeSessionRunner{fixer: &probeFixer{probe: probe}})
	sessions, ok := wrapped.(agents.SessionRunner)
	if !ok {
		t.Fatal("wrapping lost the session mechanism agents.Resolve vouched for")
	}

	fixer, err := sessions.Fixer(t.Context(), "")
	if err != nil {
		t.Fatalf("opening the probe fixer: %v", err)
	}
	ctx := withAdvancing(t.Context(), "run-2")
	if _, err := fixer.Apply(ctx, agents.Invocation{}); !errors.Is(err, errProbe) {
		t.Fatalf("the wrapped fixer answered %v, want the probe's refusal", err)
	}
	if !probe.asserted {
		t.Fatal("the probe never ran, so nothing here was checked")
	}
	if got := fixer.Reference(); got != "probe-session" {
		t.Errorf("the wrapped fixer reports the reference %q, want the inner fixer's", got)
	}
	if !s.registry.empty() {
		t.Fatal("the registry still holds an entry after the fix round returned")
	}
}

// TestAnInvocationOutsideARunIsStillContained pins that a missing run on the
// context weakens the refusal's wording and not the relation: the group is
// registered either way.
func TestAnInvocationOutsideARunIsStillContained(t *testing.T) {
	s := &Service{registry: newRegistry()}
	probe := &startedProbe{t: t, registry: s.registry, wantRun: "", wantStage: "intent"}
	wrapped := s.containRunner(probeRunner{probe: probe})

	if _, err := wrapped.Run(t.Context(), agents.PurposeIntent, agents.Invocation{}); !errors.Is(err, errProbe) {
		t.Fatalf("the wrapped runner answered %v, want the probe's refusal", err)
	}
	if !probe.asserted {
		t.Fatal("the probe never ran, so nothing here was checked")
	}
}
