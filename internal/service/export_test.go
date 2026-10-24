package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/session"
)

// SecondCheckHeading is the heading dispatch appends the second check to a
// task's instructions under, for this package's external tests to tell the
// coordinator's text from what dispatch wrote after it.
const SecondCheckHeading = secondCheckHeading

// SessionOf exposes to this package's external tests the terminal session
// the service launched for a task, with the provider that holds it, and
// whether it launched one. It exists only in the tests: the product reaches
// a session through the attempt's recorded reference and the provider, and
// no protocol method hands one out.
func (s *Service) SessionOf(id string) (session.Provider, *session.Session, bool) {
	return s.sessionOf(id)
}

// WithCleanupInterruptForTest returns o with the cleanup decision's seam
// between a copy's removal and its mark set to f, for this package's
// external tests: a removal whose f returns an error is left unmarked, as a
// service that stopped there leaves it.
func WithCleanupInterruptForTest(tb testing.TB, o Options, f func() error) Options {
	o.cleanupTest, o.interruptCleanupMark = tb, f
	return o
}

// WithIntakeInterruptForTest returns o with intake's seam between a task's
// row and its instructions file set to f, for this package's external
// tests: an intake whose f returns an error ends with the row written and
// no file, as a service that stopped there leaves it.
func WithIntakeInterruptForTest(tb testing.TB, o Options, f func() error) Options {
	o.intakeTest, o.interruptIntake = tb, f
	return o
}

// WithLandingInterruptForTest returns o with a landing's seam between its
// run and the entry's record of it set to f, for this package's external
// tests: a landing whose f returns an error ends with the run started and
// the entry naming none, as a service that stopped there leaves it.
func WithLandingInterruptForTest(tb testing.TB, o Options, f func(run string) error) Options {
	o.landingTest, o.interruptLanding = tb, f
	return o
}

// WithCarryOnInterruptForTest returns o with a carry on's seam between the
// copy's move to the new task and the entry's mark set to f, for this
// package's external tests: a carry on whose f returns an error ends with
// the copy at the new task's path and the entry owed, as a service that
// stopped there leaves it.
func WithCarryOnInterruptForTest(tb testing.TB, o Options, f func() error) Options {
	o.carryOnTest, o.interruptCarryOn = tb, f
	return o
}

// WithCarryOnClaimInterruptForTest returns o with a carry-on answer's seam
// between its read or write of the entry's claim and the completion's read
// of the entry set to f, for this package's external tests: what f writes
// to the store in between is what another answer could have written while
// this one ran, and an error f returns ends the answer there.
func WithCarryOnClaimInterruptForTest(tb testing.TB, o Options, f func() error) Options {
	o.carryOnTest, o.interruptCarryOnClaim = tb, f
	return o
}

// WithDiscardInterruptForTest returns o with a discard's seam between the
// record of the person's confirmation and the copy's removal set to f, for
// this package's external tests: a discard whose f returns an error ends
// with the confirmation recorded and the copy standing, as a service that
// stopped there leaves it.
func WithDiscardInterruptForTest(tb testing.TB, o Options, f func() error) Options {
	o.discardTest, o.interruptDiscard = tb, f
	return o
}

// WithDiscardRemovalSeamsForTest returns o with a discard's seams set, for
// this package's external tests: inWindow runs after the removal's last
// read of the copy and before the removal, and beforeMark after the
// removal and before the entry is marked, a discard whose beforeMark
// returns an error ending with the copy gone and the entry unmarked, as a
// service that stopped there leaves it. Either may be nil.
func WithDiscardRemovalSeamsForTest(tb testing.TB, o Options, inWindow func(), beforeMark func() error) Options {
	o.discardRemovalTest, o.inDiscardWindow, o.interruptDiscardMark = tb, inWindow, beforeMark
	return o
}

// WithWorktreeRemovalSeamForTest returns o with a run's copy's discard
// running f once the worktree's directory is removed and before its record
// is, for this package's external tests.
func WithWorktreeRemovalSeamForTest(tb testing.TB, o Options, f func()) Options {
	o.discardRemovalTest, o.inWorktreeRemoval = tb, f
	return o
}

// WithCleanupAskSpacingForTest returns o with the spacing the supervisor
// pass leaves between two asks of one landed task's cleanup set to d, for
// this package's external tests, which cannot wait landedAskSpacing.
func WithCleanupAskSpacingForTest(o Options, d time.Duration) Options {
	o.cleanupAskSpacing = d
	return o
}

// LandedCleanupAsks is landedAsks, the most times the cleanup decision is
// asked for one landed task under one Dead terminal in a service's life, for
// this package's external tests to count against.
const LandedCleanupAsks = landedAsks

// WithBeforeStaleRemovalReleaseForTest returns o with recovery's seam between
// reading a dead eject's removal reservation and releasing it set to f, for
// this package's external tests.
func WithBeforeStaleRemovalReleaseForTest(o Options, f func()) Options {
	o.beforeStaleRemovalRelease = f
	return o
}

// WithAfterIntakeBranchReleaseForTest returns o with the seam after
// internal/tasks gives back an intake's branch exclusion set to f, for this
// package's external tests: for a delivery intake, f runs after the task's
// record and before its instructions file.
func WithAfterIntakeBranchReleaseForTest(o Options, f func()) Options {
	o.afterIntakeBranchRelease = f
	return o
}

// DispatchDueForTest runs one dispatch pass, dispatchDue, for this
// package's external tests, which use it to run the pass at a moment a
// release or another task.create could.
func (s *Service) DispatchDueForTest(ctx context.Context) {
	s.dispatchDue(ctx)
}

// WithAfterMergeReadPassForTest returns o with the seam after each pass of
// mergeread.go's read for merges a person made set to f, for this package's
// external tests: f first runs once the pass Open starts has returned.
func WithAfterMergeReadPassForTest(o Options, f func()) Options {
	o.afterMergeReadPass = f
	return o
}

// ReadPersonMergesForTest runs one pass of mergeread.go's read for merges a
// person made, readPersonMerges, for this package's external tests, which
// use it to read a merge they scripted the code host to report without
// waiting mergeReadInterval for the next pass.
func (s *Service) ReadPersonMergesForTest(ctx context.Context) {
	s.readPersonMerges(ctx)
}

// SetBeforeRefusalEndForTest installs beforeRefusalEnd for this
// package's external tests. It exists only in the tests, on that field's
// terms in service.go.
func (s *Service) SetBeforeRefusalEndForTest(f func()) {
	s.beforeRefusalEnd = f
}

// SetBetweenLandingClaimAndStartForTest installs betweenLandingClaimAndStart
// for this package's external tests, on that field's terms in service.go.
func (s *Service) SetBetweenLandingClaimAndStartForTest(f func()) {
	s.betweenLandingClaimAndStart = f
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

// SweepsAsked is sweepsAsked for this package's external tests: every sweep
// a service asks of a copy, each answered by the real sweep.
type SweepsAsked struct{ asked *sweepsAsked }

// WithSweepsRecordedForTest returns o with every sweep the service asks of a
// copy answered by an agents.Sweeper over o.Home with reapGrace, which is
// the sweep the product asks, and recorded in the SweepsAsked it returns. It
// is set on the options because recovery sweeps inside Open, before a test
// holds the Service.
func WithSweepsRecordedForTest(o Options) (Options, *SweepsAsked) {
	asked := &sweepsAsked{sweeper: agents.NewSweeper(o.Home, reapGrace, nil)}
	o.sweepCopy = asked.sweep
	return o, &SweepsAsked{asked: asked}
}

// Of counts the sweeps the service has asked of dir.
func (s *SweepsAsked) Of(dir string) int { return s.asked.of(dir) }

// UntilClear is untilClear over the latest sweep asked of dir, for a test
// that left nothing of its own running in dir and holds nothing of each
// sweep beyond untilClear's own checks. again must make the service ask
// exactly one more sweep of dir.
func (s *SweepsAsked) UntilClear(t *testing.T, unestablished, dir string, again func()) {
	t.Helper()
	s.asked.untilClear(t, unestablished, dir, nil, func(agents.SweepResult) {}, again)
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

// CarryOnHeading is the heading of the note a carry on appends to the new
// task's instructions, for this package's external tests.
const CarryOnHeading = carryOnHeading
