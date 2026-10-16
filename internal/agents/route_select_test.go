package agents_test

import (
	"errors"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
)

// An explicit override is the only entry considered: a runnable pin that
// lacks Need is refused with *CapabilityError and never falls through to a
// later entry that would have covered it.
func TestRouteOverrideRefusesMissingNeedWithoutFallthrough(t *testing.T) {
	t.Parallel()
	cursor := &stubFactory{
		name: "cursor", available: true, namedOnly: true,
		declares: []agents.Capability{agents.CapabilityResumableSessions},
		sessions: true,
	}
	claude := &stubFactory{
		name: "claude", available: true,
		declares: []agents.Capability{
			agents.CapabilityResumableSessions,
			agents.CapabilityWorkerSessions,
		},
		sessions: true, workers: true,
	}
	catalog := agents.NewCatalog(cursor, claude)
	need := agents.NeedForWorker(false)

	_, err := agents.Route(t.Context(), catalog, []string{"cursor", "claude"}, need, "cursor")
	var undeclared *agents.CapabilityError
	if !errors.As(err, &undeclared) || undeclared.Capability != agents.CapabilityWorkerSessions || undeclared.Agent != "cursor" {
		t.Fatalf("override answered %v, want *CapabilityError naming cursor and %s", err, agents.CapabilityWorkerSessions)
	}
	if !errors.Is(err, agents.ErrUndeclaredCapability) {
		t.Errorf("the refusal is not ErrUndeclaredCapability: %v", err)
	}
}

// Without an override, a runnable adapter that lacks Need is skipped and the
// next covering candidate wins, with ReasonOrdered and the skip recorded.
func TestRouteSkipsRunnableMissingNeedAndTakesTheNext(t *testing.T) {
	t.Parallel()
	cursor := &stubFactory{
		name: "cursor", available: true, namedOnly: true,
		declares: []agents.Capability{agents.CapabilityResumableSessions},
		sessions: true,
	}
	claude := &stubFactory{
		name: "claude", available: true,
		declares: []agents.Capability{
			agents.CapabilityResumableSessions,
			agents.CapabilityWorkerSessions,
		},
		sessions: true, workers: true,
	}
	catalog := agents.NewCatalog(cursor, claude)
	got, err := agents.Route(t.Context(), catalog, []string{"cursor", "claude"}, agents.NeedForWorker(false), "")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if got.Reason != agents.ReasonOrdered || got.Resolution.Name != "claude" {
		t.Fatalf("got %+v, want ordered claude", got)
	}
	if len(got.Resolution.Skipped) != 1 || got.Resolution.Skipped[0].Name != "cursor" {
		t.Fatalf("skipped = %+v, want cursor alone", got.Resolution.Skipped)
	}
	var undeclared *agents.CapabilityError
	if !errors.As(got.Resolution.Skipped[0].Err, &undeclared) || undeclared.Capability != agents.CapabilityWorkerSessions {
		t.Fatalf("the skip reason is %v, want *CapabilityError for %s", got.Resolution.Skipped[0].Err, agents.CapabilityWorkerSessions)
	}
}

// A single named pin that covers Need selects with ReasonOverride.
func TestRouteOverrideSelectsWhenNeedIsCovered(t *testing.T) {
	t.Parallel()
	claude := &stubFactory{
		name: "claude", available: true,
		declares: []agents.Capability{
			agents.CapabilityResumableSessions,
			agents.CapabilityWorkerSessions,
		},
		sessions: true, workers: true,
	}
	got, err := agents.Route(t.Context(), agents.NewCatalog(claude), []string{"claude"}, agents.NeedForWorker(false), "claude")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if got.Reason != agents.ReasonOverride || got.Resolution.Name != "claude" {
		t.Fatalf("got %+v, want override claude", got)
	}
}

// auto expands as Resolve does, and a catalog whose auto-eligible adapter
// lacks Need refuses rather than inventing a grant or silent substitute.
func TestRouteAutoExpandsAndRefusesWhenNothingCoversNeed(t *testing.T) {
	t.Parallel()
	only := &stubFactory{
		name: "cursor", available: true,
		declares: []agents.Capability{agents.CapabilityResumableSessions},
		sessions: true,
	}
	_, err := agents.Route(t.Context(), agents.NewCatalog(only), []string{agents.AutoEntry}, agents.NeedForWorker(false), "")
	var refusal *agents.ResolutionError
	if !errors.As(err, &refusal) || !errors.Is(err, agents.ErrNoAgent) {
		t.Fatalf("Route answered %v, want *ResolutionError wrapping ErrNoAgent", err)
	}
	if len(refusal.Tried) != 1 || refusal.Tried[0].Name != "cursor" {
		t.Fatalf("tried = %+v, want cursor", refusal.Tried)
	}
}

// NeedForWorker asks for terminal workers only when tmux is the provider.
func TestNeedForWorkerAddsTerminalWorkersForTmux(t *testing.T) {
	t.Parallel()
	pipe := agents.NeedForWorker(false)
	if !pipe.Has(agents.CapabilityWorkerSessions) || pipe.Has(agents.CapabilityTerminalWorkers) {
		t.Fatalf("pipe need = %s, want worker_sessions alone", pipe)
	}
	tmux := agents.NeedForWorker(true)
	if !tmux.Has(agents.CapabilityWorkerSessions) || !tmux.Has(agents.CapabilityTerminalWorkers) {
		t.Fatalf("tmux need = %s, want both worker capabilities", tmux)
	}
	if !agents.Declare(agents.CapabilityWorkerSessions, agents.CapabilityTerminalWorkers).Covers(tmux) {
		t.Fatal("a declaration of both capabilities does not cover the tmux need")
	}
	if agents.Declare(agents.CapabilityWorkerSessions).Covers(tmux) {
		t.Fatal("worker_sessions alone covered the tmux need")
	}
}
