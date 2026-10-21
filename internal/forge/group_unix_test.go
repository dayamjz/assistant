//go:build unix

package forge_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/principles"
)

// hangLimit is how long a test waits for a call to a provider that never
// exits before failing it. It is well past every deadline these tests set
// plus the two graces procgroup.Run may spend after one, and well short of
// the hour the stand-in would otherwise wait.
const hangLimit = 20 * time.Second

// hangingChecks reads checks from a stand-in provider that never exits and
// leaves a descendant in its group holding its pipes, and returns the two
// processes the stand-in recorded, how long the read took and what it
// answered. The read runs under a context with callerDeadline, or, when that
// is zero, under context.WithoutCancel, which carries no deadline. A read
// still waiting at hangLimit fails the test, and cleanup ends both processes
// either way.
func hangingChecks(t *testing.T, callerDeadline time.Duration, opts ...forge.Option) ([]int, time.Duration, error) {
	t.Helper()
	h := newHarness(t, ghScript{"checks": {{Hang: true}}}, opts...)
	pids := h.trackStandIns()
	call := context.WithoutCancel(context.Background())
	if callerDeadline > 0 {
		var cancel context.CancelFunc
		call, cancel = context.WithTimeout(context.Background(), callerDeadline)
		defer cancel()
	}
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := h.gh.Checks(call, 12)
		done <- err
	}()
	var err error
	select {
	case err = <-done:
	case <-time.After(hangLimit):
		t.Fatalf("Checks against a provider that never exits had not returned after %s", hangLimit)
	}
	elapsed := time.Since(start)
	recorded := pids()
	if len(recorded) != 2 {
		t.Fatalf("the stand-in recorded %d processes, want the provider and its descendant (the call answered %v)", len(recorded), err)
	}
	return recorded, elapsed, err
}

// requireGone fails the test for each process in pids still running a
// moment after the call that started it returned. Each would otherwise wait
// for the test's release, so a process still here is one nothing ended.
func requireGone(t *testing.T, pids []int) {
	t.Helper()
	for _, pid := range pids {
		if !gone(pid, 2*time.Second) {
			t.Errorf("process %d is still running after the invocation that started it returned", pid)
		}
	}
}

// A provider that never exits, read under a context with no deadline of its
// own, is ended at the adapter's invocation deadline together with the
// descendant it left in its group, and the read is refused as unavailable,
// naming the deadline. That is P15's "an external program the service
// invokes runs under a deadline, and when the deadline passes it is ended
// together with everything it started".
func TestAProviderThatNeverExitsIsEndedWithItsGroupAtTheInvocationDeadline(t *testing.T) {
	principles.Cite(t, principles.P15)
	const deadline = 5 * time.Second
	pids, elapsed, err := hangingChecks(t, 0, forge.WithInvocationDeadline(deadline))
	var refusal *forge.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Checks = %v; want a *Refusal", err)
	}
	if refusal.Reason != forge.ReasonUnavailable {
		t.Errorf("reason is %q, want %q", refusal.Reason, forge.ReasonUnavailable)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("the refusal does not name the deadline that ended the call: %v", err)
	}
	if elapsed < deadline {
		t.Errorf("Checks returned after %s, before its %s deadline", elapsed, deadline)
	}
	requireGone(t, pids)
}

// A caller's own deadline is the one an invocation runs under, and its
// passing ends the group the same way: the invocation deadline only fills in
// where a caller has none, so an hour of it does not keep the call going.
func TestACallersDeadlineEndsTheProvidersGroupAndIsNotReplaced(t *testing.T) {
	principles.Cite(t, principles.P15)
	pids, _, err := hangingChecks(t, 5*time.Second, forge.WithInvocationDeadline(time.Hour))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Checks = %v; want the caller's deadline", err)
	}
	requireGone(t, pids)
}

// A provider that exits while a descendant it left in its process group
// still holds its pipes is answered as soon as it exits, and the descendant
// does not outlive the call: the group is ended before the provider is
// reaped, which closes the pipes the descendant held, so the answer is read
// whole rather than refused at the grace deadline.
func TestAProviderThatExitsLeavingADescendantInItsGroupEndsTheDescendant(t *testing.T) {
	principles.Cite(t, principles.P15)
	h := newHarness(t, ghScript{"checks": {{
		Stdout:    rollup(checkRunNode("build", "COMPLETED", "SUCCESS")),
		HoldPipes: true,
	}}})
	pids := h.trackStandIns()
	if _, err := h.gh.Checks(context.Background(), 12); err != nil {
		t.Errorf("Checks against a provider that answered and exited 0 = %v; want its answer", err)
	}
	recorded := pids()
	if len(recorded) != 1 {
		t.Fatalf("the stand-in recorded %d processes, want its one descendant", len(recorded))
	}
	requireGone(t, recorded)
}
