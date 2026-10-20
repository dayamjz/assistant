package service

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/session"
)

// The terminal hookup: the Terminals a service observes its tasks' terminals
// through, over its own session provider.
//
// # One producer of liveness, reached through one seam
//
// internal/session's Probe is the one producer of a terminal's liveness, and
// this file adds no second one: heldTerminals answers Probe by asking the
// provider the launches go through, so a reference the provider holds a
// session for reads Live out of that handle, a reference whose process the
// identity proves gone reads Dead, and everything else, a survivor an earlier
// service launched, another home's reference, and every failure to read,
// reads Unobserved, on that package's terms. Capture is answered out of the
// same held set, through the provider's CaptureReference, so the reference a
// probe answers Live for is the one a capture reads, and the reference to
// handle lookup has one owner, the provider's held set. When this service
// still holds the session, Probe and Capture prefer that launch's host
// (which may be a different adapter than the run agent); otherwise they use
// the driver's provider for identity-only answers after a restart. Every
// task reader this service builds, the task surface's, the queue's,
// reconciliation's and the loop's,
// probes through this type, so what a person listing tasks sees of a
// terminal is what the loop acts on and what a restart relaunches on.
//
// # The provider is built when it is first asked for
//
// The provider is the driver's, built in driverFor the first time something
// needs an agent, because PRD section 10 resolves the agent against what is
// runnable at that moment and not at open. So a probe asks driverFor first.
// A service that cannot resolve an agent observes nothing: the probe answers
// Unobserved with session.ReasonUnreadable, which is that package's word for
// every failure to read, and reconciliation, the loop and the task surface
// read the terminal as PRD section 11's ambiguous liveness, which is the
// safe direction. A driver that fails to build is not remembered, which
// driverFor states, so a probe on such a service asks agents.Resolve again
// each time, once per task whose latest attempt names a session; that is a
// cost and not a correctness gap, and it ends when an agent is installed or
// the service restarts.
//
// Capture on a service with no provider is the one producer of
// ErrTerminalsUnavailable: the provider as a whole could not be obtained,
// which is the case that error names, rather than one terminal failing to
// read. Once the driver is built it stays built, so the provider is never
// unavailable afterwards, and the loop reaches Capture only for a terminal
// the same pass probed Live, which only a built provider answers; the
// producer is therefore reached by a caller that captures without probing
// and not by the loop's pass, and it is stated rather than widened.
//
// # Residual gaps
//
//   - A probe's answer is as of the probe. The time between a probe and
//     what a caller does on it is open on every path: between the loop's
//     probe and its capture, between reconciliation's re-probe and its
//     claim, and between a cleanup site's read and internal/tasks' removal;
//     each of those files states its own window, and this type closes none.
//   - A reference's identity narrows the reuse of a process identifier to
//     the same start tick and does not close it, and on a platform with no
//     start time read a reference proves nothing, on internal/session's
//     terms; what that package answers is what this one hands on.
//   - On Windows a worker ended through Close is ended as far as
//     internal/agents' tree termination reaches there, which internal/session
//     states, so a descendant that outlived its leader is not what a Dead
//     reading is about.
//   - A probe asks driverFor under the service's mutex, briefly, as every
//     caller that needs the driver does; a reader that held that mutex
//     across a probe would deadlock, and none does, which is a rule the
//     readers keep and nothing enforces.

// sessionHost builds the provider this service launches workers through, as
// the operator's file chose with config.KeySessionProvider, over r, the
// resolved agent unwrapped. It reads only the global layer, which is the one
// layer that key may come from, so no repository's copy chooses how this
// service starts processes (P7).
//
// For tmux the binary is resolved off this process's PATH once, here, and
// the provider holds the absolute path. A machine with none still gets a
// tmux provider, one built with no binary: every Create on it is refused
// with session.ErrNoTmux, which launch.go reports as it reports an adapter
// without the capability, the task staying Dispatched, while probes answer
// on the shared identity rule as they would for any provider. The service
// does not fall back to the pipe provider: the operator chose an attachable
// terminal, and a worker they cannot attach to is not what they chose.
func (s *Service) sessionHost(r agents.Runner) session.Host {
	if s.cfg.SessionProvider != config.SessionProviderTmux {
		return session.NewPipe(s.home, r)
	}
	tmux, err := exec.LookPath("tmux")
	if err == nil {
		tmux, err = filepath.Abs(tmux)
	}
	if err != nil {
		s.log.Printf("the configured session provider is tmux and no tmux binary could be resolved, so no worker will launch: %v", err)
		tmux = ""
	}
	return session.NewTmux(s.home, r, tmux)
}

// heldTerminals is the Terminals over this service's own session provider,
// on the file comment's terms. It is what Open wires when Options.Terminals
// is nil, which is what the product passes.
type heldTerminals struct{ s *Service }

// Probe answers through the per-task host when this service still holds the
// session, else through the driver's provider for identity-only probes after
// a restart, on session.Provider.Probe's terms, and Unobserved with
// session.ReasonUnreadable when no provider can be built.
func (h heldTerminals) Probe(ctx context.Context, ref string) session.Observation {
	if host, ok := h.s.hostForReference(ref); ok {
		return host.Probe(ctx, ref)
	}
	built, err := h.s.driverFor(ctx)
	if err != nil {
		return session.Observation{
			Liveness: session.ReasonUnreadable.Liveness(),
			Reason:   session.ReasonUnreadable,
			Detail:   fmt.Sprintf("no session provider could be built to probe with: %v", err),
		}
	}
	return built.sessions.Probe(ctx, ref)
}

// Capture answers through the per-task host when held, else through the
// driver's CaptureReference, so a reference the provider does not hold is
// session.ErrNotHeld, this terminal's failure, and a service that cannot
// build a provider is ErrTerminalsUnavailable, wrapped, the provider's.
func (h heldTerminals) Capture(ctx context.Context, ref string) (session.Capture, error) {
	if host, ok := h.s.hostForReference(ref); ok {
		return host.CaptureReference(ctx, ref)
	}
	built, err := h.s.driverFor(ctx)
	if err != nil {
		return session.Capture{}, fmt.Errorf("%w: no session provider could be built to capture with: %w", ErrTerminalsUnavailable, err)
	}
	return built.sessions.CaptureReference(ctx, ref)
}

// hostForReference reports the per-task host this service holds for the
// session ref names, when the reference parses and names a launched task,
// or the host holding a domain's subordinate coordinator when it names a
// delegated domain.
func (s *Service) hostForReference(ref string) (session.Host, bool) {
	parsed, err := session.ParseReference(ref)
	if err != nil {
		return nil, false
	}
	if parsed.Subordinate() {
		host, _, ok := s.subordinateSessionOf(parsed.Domain)
		return host, ok
	}
	host, _, ok := s.sessionOf(parsed.Task)
	return host, ok
}
