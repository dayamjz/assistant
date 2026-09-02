package ipc_test

import (
	"testing"

	"github.com/dayamjz/assistant/internal/ipc"
)

// TestCursorOpensGapped is the acceptance criterion that a consumer cannot
// apply a delta to state it never reconciled, stated about the consumer half
// rather than about the stream that feeds it.
func TestCursorOpensGapped(t *testing.T) {
	c := ipc.NewCursor()
	if !c.Gapped() {
		t.Fatal("a new cursor is not gapped")
	}
	if got := c.Observe(state("s1", 5)); got != ipc.DispositionReconcile {
		t.Errorf("first delta observed = %s, want reconcile", got)
	}
	if got := c.Revision(); got != 0 {
		t.Errorf("Revision = %d after a delta that was not applied, want 0", got)
	}
	if !c.Gapped() {
		t.Error("the cursor stopped being gapped without reconciling")
	}
}

// TestCursorDoesNotMoveBackwards is the acceptance criterion about duplicated
// and out-of-order deliveries.
func TestCursorDoesNotMoveBackwards(t *testing.T) {
	c := ipc.NewCursor()
	c.Reconciled(4)

	steps := []struct {
		event ipc.Event
		want  ipc.Disposition
		after uint64
	}{
		{state("same as reconciled", 4), ipc.DispositionIgnore, 4},
		{state("older", 2), ipc.DispositionIgnore, 4},
		{state("newer", 5), ipc.DispositionApply, 5},
		{state("repeat of 5", 5), ipc.DispositionIgnore, 5},
		{state("out of order, older than 5", 3), ipc.DispositionIgnore, 5},
		{state("newer again", 9), ipc.DispositionApply, 9},
		{state("the delivery 9 raced past", 8), ipc.DispositionIgnore, 9},
	}
	for _, step := range steps {
		got := c.Observe(step.event)
		name := label(t, step.event)
		if got != step.want {
			t.Errorf("Observe(%s) = %s, want %s", name, got, step.want)
		}
		if c.Revision() != step.after {
			t.Errorf("after %s, Revision = %d, want %d", name, c.Revision(), step.after)
		}
	}
}

func TestCursorReconcileDoesNotRewind(t *testing.T) {
	c := ipc.NewCursor()
	c.Reconciled(10)
	if got := c.Observe(state("s", 11)); got != ipc.DispositionApply {
		t.Fatalf("Observe = %s, want apply", got)
	}
	c.Reconciled(7) // a read that answered from behind what was already applied
	if got := c.Revision(); got != 11 {
		t.Errorf("Revision = %d after reconciling to an older read, want 11", got)
	}
	if c.Gapped() {
		t.Error("the cursor is still gapped after reconciling")
	}
}

func TestCursorGapsOnAMarkerAndClearsOnReconcile(t *testing.T) {
	c := ipc.NewCursor()
	c.Reconciled(3)
	if got := c.Observe(ipc.Event{Type: "stream.gap"}); got != ipc.DispositionReconcile {
		t.Errorf("Observe(marker) = %s, want reconcile", got)
	}
	if !c.Gapped() {
		t.Fatal("a marker did not gap the cursor")
	}
	if got := c.Observe(state("s", 4)); got != ipc.DispositionReconcile {
		t.Errorf("Observe(delta while gapped) = %s, want reconcile", got)
	}
	if got := c.Revision(); got != 3 {
		t.Errorf("Revision = %d, want 3: a delta seen while gapped was applied", got)
	}
	c.Reconciled(6)
	if got := c.Observe(state("s", 7)); got != ipc.DispositionApply {
		t.Errorf("Observe after reconciling = %s, want apply", got)
	}
}

func TestCursorAppliesActivityAndControlWhileGapped(t *testing.T) {
	c := ipc.NewCursor()
	if got := c.Observe(activity("a")); got != ipc.DispositionApply {
		t.Errorf("Observe(activity) = %s, want apply: activity is not a delta to state", got)
	}
	if got := c.Observe(control("c")); got != ipc.DispositionApply {
		t.Errorf("Observe(control) = %s, want apply", got)
	}
	if !c.Gapped() {
		t.Error("observing activity cleared the gap")
	}
}

// TestCursorGapsOnAStateEventItCannotOrder covers an event a newer build sent:
// it is classified as state, and a state event with no revision cannot be
// placed against what the consumer holds. It is neither applied nor quietly
// ignored.
func TestCursorGapsOnAStateEventItCannotOrder(t *testing.T) {
	c := ipc.NewCursor()
	c.Reconciled(2)
	future := ipc.Event{Type: "future.thing"}
	if got := c.Observe(future); got != ipc.DispositionReconcile {
		t.Errorf("Observe(unorderable state) = %s, want reconcile", got)
	}
	if !c.Gapped() {
		t.Error("an event that could not be ordered left the cursor ungapped")
	}
}

func TestDispositionString(t *testing.T) {
	names := map[ipc.Disposition]string{
		ipc.DispositionReconcile: "reconcile",
		ipc.DispositionApply:     "apply",
		ipc.DispositionIgnore:    "ignore",
	}
	for d, want := range names {
		if got := d.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", d, got, want)
		}
	}
}
