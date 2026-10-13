package service

import (
	"context"
	"fmt"

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
// same held set, through session.Pipe.CaptureReference, so the reference a
// probe answers Live for is the one a capture reads, and the reference to
// handle lookup has one owner, the provider's held set; nothing here reads
// the launched map for either question. Every task reader this service
// builds, the task surface's, the queue's, reconciliation's and the loop's,
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

// heldTerminals is the Terminals over this service's own session provider,
// on the file comment's terms. It is what Open wires when Options.Terminals
// is nil, which is what the product passes.
type heldTerminals struct{ s *Service }

// Probe answers through the provider the launches go through, on
// session.Provider.Probe's terms, and Unobserved with
// session.ReasonUnreadable when no provider can be built.
func (h heldTerminals) Probe(ctx context.Context, ref string) session.Observation {
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

// Capture answers through session.Pipe.CaptureReference, so a reference
// the provider does not hold is session.ErrNotHeld, this terminal's failure,
// and a service that cannot build a provider is ErrTerminalsUnavailable,
// wrapped, the provider's.
func (h heldTerminals) Capture(ctx context.Context, ref string) (session.Capture, error) {
	built, err := h.s.driverFor(ctx)
	if err != nil {
		return session.Capture{}, fmt.Errorf("%w: no session provider could be built to capture with: %w", ErrTerminalsUnavailable, err)
	}
	return built.sessions.CaptureReference(ctx, ref)
}
