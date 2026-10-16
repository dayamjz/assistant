package service

import (
	"errors"
	"testing"

	"github.com/dayamjz/assistant/internal/tasks"
)

// A relaunch takes a slot this service already holds only once the claim
// holding it has settled: while a dispatch's claim is in flight the relaunch
// is refused and the slot stays the dispatch's, and after it settles the
// relaunch takes it, and a second relaunch is refused behind that one.
func TestARelaunchRetakesASettledSlotAndNeverOneInFlight(t *testing.T) {
	t.Parallel()
	s := &Service{}
	settleDispatch, err := s.claimDispatch("t1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.claimRelaunch("t1"); !errors.Is(err, errRelaunchInFlight) {
		t.Fatalf("a relaunch beside a dispatch in flight = %v, want errRelaunchInFlight", err)
	}
	settleDispatch(tasks.Isolation{})
	if _, err := s.claimDispatch("t1"); err == nil {
		t.Fatal("a settled slot was dispatched again")
	}
	settleRelaunch, err := s.claimRelaunch("t1")
	if err != nil {
		t.Fatalf("a relaunch of a settled slot = %v", err)
	}
	if _, err := s.claimRelaunch("t1"); !errors.Is(err, errRelaunchInFlight) {
		t.Fatalf("a second relaunch beside one in flight = %v, want errRelaunchInFlight", err)
	}
	settleRelaunch(tasks.Isolation{})
	if _, held := s.isolationOf("t1"); !held {
		t.Fatal("a settled relaunch gave its slot up, so dispatchDue would dispatch the task again")
	}
}
