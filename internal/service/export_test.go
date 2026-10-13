package service

import (
	"context"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/session"
)

// SessionOf exposes to this package's external tests the terminal session
// the service launched for a task, with the provider that holds it, and
// whether it launched one. It exists only in the tests: the product reaches
// a session through the attempt's recorded reference and the provider, and
// no protocol method hands one out.
func (s *Service) SessionOf(id string) (session.Provider, *session.Session, bool) {
	return s.sessionOf(id)
}

// ReconcileProbeBound is the bound one read of the fleet at reconciliation
// is held to, exposed so a test can hold Open to it.
const ReconcileProbeBound = reconcileProbeBound

// WorkerOf exposes the worker-set derivation to this package's external
// tests, so a test can ask which task a process group's worker is from the
// same credentials the handlers derive it from. It exists only in the
// tests; the product derives it per request and hands it out nowhere.
func (s *Service) WorkerOf(c ipc.Credentials) (session.Membership, bool, error) {
	return s.workerOf(c)
}

// Contained exposes the containment registry's answer, the other relation
// over the same credentials, so a test can show the two relations placing
// one group differently.
func (s *Service) Contained(ctx context.Context, c ipc.Credentials) (ipc.Containment, error) {
	return s.registry.Contained(ctx, c)
}
