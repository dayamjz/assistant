package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// The product's terminal seam answers every question through the one
// provider the launches go through: a launched session's reference probes
// Live and captures the worker's output by reference, the same bytes the
// handle's Capture reads; a reference the provider holds no session for
// probes by identity and captures session.ErrNotHeld, this terminal's
// failure; and a cancelled context is refused before the read.
func TestTheTerminalSeamAnswersThroughTheServicesProvider(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	sub := newLaunchSubject(t, workerStandIn(t))
	terminals := heldTerminals{sub.service}
	sub.service.terminals = terminals
	d, err := sub.service.dispatchTask(ctx, sub.task)
	if err != nil {
		t.Fatalf("dispatching: %v", err)
	}
	launched, err := sub.service.launchTask(ctx, d)
	if err != nil {
		t.Fatalf("launching: %v", err)
	}
	provider, _, _ := sub.service.sessionOf(sub.task)
	waitCaptured(t, provider, launched, "worker ready")
	ref := launched.Reference().String()

	if got := terminals.Probe(ctx, ref); got.Liveness != taskstate.Live || got.Reason != session.ReasonHeld {
		t.Fatalf("the launched session probes %+v through the seam, want Live through the held handle", got)
	}
	// The worker may still be printing what it was sent, so the two reads
	// are compared only when a handle read on each side of the reference
	// read agrees: the output did not move across it.
	deadline := time.Now().Add(30 * time.Second)
	for {
		before, err := provider.Capture(launched)
		if err != nil {
			t.Fatal(err)
		}
		byReference, err := terminals.Capture(ctx, ref)
		if err != nil {
			t.Fatalf("the capture by reference failed: %v", err)
		}
		after, err := provider.Capture(launched)
		if err != nil {
			t.Fatal(err)
		}
		if before.Digest == after.Digest && before.Text == after.Text {
			if byReference.Digest != before.Digest || byReference.Text != before.Text {
				t.Fatalf("the capture by reference is %+v, want the handle's %+v", byReference, before)
			}
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the worker's output never settled; the last handle read is %+v", after)
		}
		time.Sleep(10 * time.Millisecond)
	}

	other := session.Reference{Home: session.HomeMarker(sub.service.home), Task: sub.task, PID: 1 << 30, Start: 1}
	if got := terminals.Probe(ctx, other.String()); got.Liveness != taskstate.Dead {
		t.Fatalf("a reference naming no process probes %+v, want Dead by identity", got)
	}
	if _, err := terminals.Capture(ctx, other.String()); !errors.Is(err, session.ErrNotHeld) || errors.Is(err, ErrTerminalsUnavailable) {
		t.Fatalf("a capture of an unheld reference answered %v, want the terminal's own ErrNotHeld", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := terminals.Capture(cancelled, ref); !errors.Is(err, context.Canceled) {
		t.Fatalf("a capture under a cancelled context answered %v, want the context's error", err)
	}
}

// A service that cannot build a provider observes nothing: the seam answers
// every probe Unobserved with the reason a failure to read carries, never
// Dead, so reconciliation leaves such a task alone, and a capture is the
// provider's failure, ErrTerminalsUnavailable, which this is the one
// producer of. The control is the same seam over a service that resolves
// an agent, above.
func TestTheTerminalSeamOverNoProviderObservesNothing(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	sub := newDispatchSubject(t)
	// A catalog holding a factory that refuses, so the driver cannot be
	// built; the shipped catalog would reach the operator's own agent.
	sub.service.catalog = agents.NewCatalog(refusingFactory{})
	terminals := heldTerminals{sub.service}
	ref := session.Reference{Home: session.HomeMarker(sub.service.home), Task: sub.task, PID: 1 << 30, Start: 1}
	if got := terminals.Probe(ctx, ref.String()); got.Liveness != taskstate.Unobserved || got.Reason != session.ReasonUnreadable {
		t.Fatalf("a probe with no provider answered %+v, want Unobserved as unreadable", got)
	}
	if _, err := terminals.Capture(ctx, ref.String()); !errors.Is(err, ErrTerminalsUnavailable) {
		t.Fatalf("a capture with no provider answered %v, want ErrTerminalsUnavailable", err)
	}
	if _, err := sub.service.driverFor(ctx); err == nil {
		t.Fatal("the control: the driver built, so the readings above were not those of a service with no provider")
	}
}

// refusingFactory is an agent factory that cannot build a runner, the shape
// a machine with no agent installed presents to agents.Resolve.
type refusingFactory struct{}

func (refusingFactory) Name() string { return "claude" }

func (refusingFactory) AutoEligible() bool { return true }

func (refusingFactory) Declares() agents.Capabilities { return agents.Capabilities{} }

func (refusingFactory) New(context.Context, []string) (agents.Runner, error) {
	return nil, errors.New("no agent is installed on this machine")
}
