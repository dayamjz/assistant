package service

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
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

// SetBeforeRefusalEndForTest installs beforeRefusalEnd for this
// package's external tests. It exists only in the tests, on that field's
// terms in service.go.
func (s *Service) SetBeforeRefusalEndForTest(f func()) {
	s.beforeRefusalEnd = f
}

// SetAfterConsentRegisteredForTest installs afterConsentRegistered for this
// package's external tests. It exists only in the tests, on that field's
// terms in service.go.
func (s *Service) SetAfterConsentRegisteredForTest(f func()) {
	s.afterConsentRegistered = f
}

// SubordinateSessionOf exposes to this package's external tests the
// terminal session the service started for a domain's subordinate
// coordinator, with the host that holds it, on SessionOf's terms.
func (s *Service) SubordinateSessionOf(domain string) (session.Host, *session.Session, bool) {
	return s.subordinateSessionOf(domain)
}

// AbandonSessionsForTest forgets every session this service launched
// without ending it, so Close ends none of them. It is the shape a service
// killed with SIGKILL leaves, which a test cannot produce of its own
// process: the worker, its terminal and the attempt's recorded reference
// stand, and no service holds a handle to any of them. It exists only in
// the tests and models nothing a running service does; the test that calls
// it ends the worker itself.
func (s *Service) AbandonSessionsForTest(tb testing.TB) {
	tb.Helper()
	s.mu.Lock()
	s.launched = make(map[string]launchedWorker)
	s.mu.Unlock()
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

// FatalUnlessSweepsStoppedClearing is fatalUnlessSweepsStoppedClearing for
// this package's external tests.
func FatalUnlessSweepsStoppedClearing(t *testing.T, unestablished, format string, args ...any) {
	t.Helper()
	fatalUnlessSweepsStoppedClearing(t, unestablished, format, args...)
}

// SeedDatabase is seedDatabase for this package's external tests.
func SeedDatabase(tb testing.TB, path string) {
	tb.Helper()
	seedDatabase(tb, path)
}

// RemoveDatabaseTemplate is removeDatabaseTemplate for TestMain.
func RemoveDatabaseTemplate() { removeDatabaseTemplate() }

// LateRecorder is an agents.Recorder for this package's external tests that
// hands each record to the service's own invocation recorder, the one the
// default catalog's adapters are built with, once Bind names the service. A
// test needs it because the catalog is built before Open returns the
// service the recorder writes through. A record before Bind panics rather
// than being dropped, so a test cannot pass over a call nothing recorded.
type LateRecorder struct{ s atomic.Pointer[Service] }

// Bind names the service whose recorder the records go to.
func (r *LateRecorder) Bind(s *Service) { r.s.Store(s) }

// RecordInvocation hands rec to the bound service's recorder.
func (r *LateRecorder) RecordInvocation(rec agents.Record) {
	s := r.s.Load()
	if s == nil {
		panic("service.LateRecorder: a record arrived before Bind")
	}
	invocationRecorder{s: s}.RecordInvocation(rec)
}

// FailUsageReadsForTest makes the next n usage budget readings fail as a
// store read can, so a test can put a failed reading at the point it needs
// one. It exists only in the tests.
func (s *Service) FailUsageReadsForTest(n int32) { s.usageReadFaults.Store(n) }
