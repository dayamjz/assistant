package service

import (
	"context"

	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// SessionOf exposes to this package's external tests the terminal session
// the service launched for a task, with the provider that holds it, and
// whether it launched one. It exists only in the tests: the product reaches
// a session through the attempt's recorded reference and the provider, and
// no protocol method hands one out.
func (s *Service) SessionOf(id string) (session.Provider, *session.Session, bool) {
	return s.sessionOf(id)
}

// WithProbe returns the options with a terminal probe the service's task
// reader is built with, which is what reconciliation reads a terminal's
// liveness through. It exists only in the tests, because in production the
// probe is nil until #91's last PR wires the session provider's Probe into
// the service; reconcile.go states what that leaves.
func WithProbe(o Options, probe func(ctx context.Context, sessionRef string) taskstate.Liveness) Options {
	o.probe = probe
	return o
}

// ReconcileProbeBound is the bound one read of the fleet at reconciliation
// is held to, exposed so a test can hold Open to it.
const ReconcileProbeBound = reconcileProbeBound
