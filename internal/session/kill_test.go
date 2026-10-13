package session_test

import (
	"context"
	"testing"

	"github.com/dayamjz/assistant/internal/session"
)

// A worker killed from outside probes Dead through the handle once the
// provider has seen it go, and Dead by identity from a provider holding no
// handle, which finds no running process with its identifier.
func TestAKilledWorkerProbesDead(t *testing.T) {
	sub := newSubject(t)
	p, _ := provider(t, sub.home, workerScript())
	s := create(t, p, sub.spec("begin"))
	ref := s.Reference()
	waitCapture(t, p, s, "worker ready")
	expect(t, p.Probe(context.Background(), ref.String()), session.ReasonHeld)

	kill(t, ref)
	expect(t, waitProbe(t, p, ref.String(), session.ReasonExited), session.ReasonExited)

	restarted := session.NewPipe(sub.home, p2runner(t))
	got := waitProbe(t, restarted, ref.String(), session.ReasonAbsent)
	expect(t, got, session.ReasonAbsent)
}
