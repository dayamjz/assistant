package service

import (
	"context"
	"testing"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/store"
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

// RequireSweepClearsHere is requireSweepClearsHere for this package's
// external tests.
func RequireSweepClearsHere(t *testing.T, unestablished string) {
	t.Helper()
	requireSweepClearsHere(t, unestablished)
}

// SeedDatabase is seedDatabase for this package's external tests.
func SeedDatabase(tb testing.TB, path string) {
	tb.Helper()
	seedDatabase(tb, path)
}

// RemoveDatabaseTemplate is removeDatabaseTemplate for TestMain.
func RemoveDatabaseTemplate() { removeDatabaseTemplate() }

// AttemptDelegatedMerge runs the program-merge path for a fixed pull
// request. A subordinate returns ErrDelegationMerge before a code host is
// asked. It exists so a test can reach mergeUnderAutonomy; the product
// reaches that function from a run settling passed.
func (s *Service) AttemptDelegatedMerge(ctx context.Context) error {
	return s.mergeUnderAutonomy(ctx, nil, store.Run{ID: "delegated"}, passedPullRequest{
		specifier: "owner/name",
		number:    1,
		observed:  "0123456789abcdef0123456789abcdef01234567",
	})
}

// TasksForTest reads the home's tasks. It exists so a restart test can see
// whether a second task was recorded. The product lists tasks through the
// protocol.
func (s *Service) TasksForTest(ctx context.Context) ([]store.Task, error) {
	return s.store.Tasks(ctx)
}

// AttemptsForTest reads one task's attempts. It exists so a restart test
// can see whether a second attempt was begun.
func (s *Service) AttemptsForTest(ctx context.Context, id string) ([]store.TaskAttempt, error) {
	return s.store.TaskAttempts(ctx, id)
}

// RelaunchForTest runs the relaunch path. A delegated task whose recovery
// does not allow it returns ErrDelegationOwns and begins no attempt.
func (s *Service) RelaunchForTest(ctx context.Context, id string) error {
	_, err := s.relaunchTask(ctx, id)
	return err
}
