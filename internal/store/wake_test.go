package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// terminalMarker composes a terminal kind's marker the way the supervisor
// loop will: the capture digest, the change epoch the markers carry, and the
// stall repetition count for an escalation of the same stall. The store
// checks only that it is non-empty; this is the loop's convention, stated
// once here so the tests below read as the loop's passes.
func terminalMarker(digest string, epoch, repetition int64) string {
	return fmt.Sprintf("%s@%d#%d", digest, epoch, repetition)
}

// surface is one pass of the loop that surfaces a terminal-unchanged wake on
// the given screen: it reads the markers for the epoch, composes the marker,
// and appends the wake with the markers advanced to that screen.
func surface(t *testing.T, s *Store, task, digest string, repetition int64) WakeAppend {
	t.Helper()
	ctx := context.Background()
	m, _, err := s.WakeMarkers(ctx, task)
	if err != nil {
		t.Fatalf("WakeMarkers: %v", err)
	}
	epoch := m.ChangeEpoch
	if m.CaptureDigest != Known(digest) {
		epoch++ // the store will bump it in the same write; the loop knows the rule
	}
	out, err := s.AppendWakeWithMarkers(ctx, incarnationOf(t, s), WakeRequest{
		TaskID: task, Kind: "terminal-unchanged", Marker: terminalMarker(digest, epoch, repetition),
	}, MarkerAdvance{CaptureDigest: Known(digest), Surfaced: true, StallRepetitions: repetition})
	if err != nil {
		t.Fatalf("AppendWakeWithMarkers(%s): %v", digest, err)
	}
	if out.Markers.ChangeEpoch != epoch {
		t.Fatalf("the store derived epoch %d for digest %s, the loop expected %d", out.Markers.ChangeEpoch, digest, epoch)
	}
	return out
}

// A screen that returns to a value it showed before is a new surface and not
// a replay. The change epoch the store derives advances whenever the digest
// written differs from the one before, so A, B, A composes three distinct
// markers and three rows land, and the third is not read as the first. The
// control is the counterfeit marker without the epoch, whose third surface
// reads as already queued.
func TestAScreenReturningToAnEarlierValueIsANewSurface(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "taskaaaaaaab").ID

	for i, digest := range []string{"A", "B", "A"} {
		out := surface(t, s, task, digest, 1)
		if out.Disposition != WakeAppended {
			t.Fatalf("surface %d on %s = %s, want appended", i+1, digest, out.Disposition)
		}
	}
	wakes, err := s.TaskWakes(ctx, task)
	if err != nil || len(wakes) != 3 {
		t.Fatalf("A, B, A left %d rows (%v), want 3", len(wakes), err)
	}
	if wakes[0].Marker == wakes[2].Marker {
		t.Fatalf("the first and third surfaces share marker %q", wakes[0].Marker)
	}
	m, _, _ := s.WakeMarkers(ctx, task)
	if m.ChangeEpoch != 3 {
		t.Fatalf("after A, B, A the epoch is %d, want 3", m.ChangeEpoch)
	}

	// The counterfeit: a marker of the digest alone. The third surface has
	// the first's key and is swallowed as already queued, which is the drop
	// the epoch exists to prevent.
	other := seedTask(t, s, "taskaaaaaaac").ID
	var last WakeAppend
	for _, digest := range []string{"A", "B", "A"} {
		last, err = s.AppendWakeWithMarkers(ctx, incarnationOf(t, s), WakeRequest{TaskID: other, Kind: "terminal-unchanged", Marker: digest},
			MarkerAdvance{CaptureDigest: Known(digest)})
		if err != nil {
			t.Fatalf("counterfeit append: %v", err)
		}
	}
	if last.Disposition != WakeAlreadyQueued {
		t.Fatalf("the control's third surface = %s, so the test cannot show what the epoch buys", last.Disposition)
	}
}

// A second escalation of the same stall carries the next repetition count in
// its marker, so two escalations are two rows while the screen and the epoch
// stay the same.
func TestTwoEscalationsOfOneStallAreTwoRows(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "taskaaaaaaab").ID
	first := surface(t, s, task, "A", 1)
	second := surface(t, s, task, "A", 2)
	if first.Disposition != WakeAppended || second.Disposition != WakeAppended {
		t.Fatalf("escalations = %s, %s, want both appended", first.Disposition, second.Disposition)
	}
	if first.Markers.ChangeEpoch != second.Markers.ChangeEpoch {
		t.Fatalf("the epoch moved from %d to %d on an unchanged screen", first.Markers.ChangeEpoch, second.Markers.ChangeEpoch)
	}
	if wakes, err := s.TaskWakes(ctx, task); err != nil || len(wakes) != 2 {
		t.Fatalf("two escalations left %d rows (%v), want 2", len(wakes), err)
	}
}

// A detection the loop replays, after a restart or a pass that repeated
// itself, is one wake: the same task, kind and marker against a row still
// queued is WakeAlreadyQueued, the row returned is the one already there,
// and the markers are advanced all the same. The restart is simulated by
// closing and reopening the store between the two.
func TestAReplayedDetectionIsOneWake(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/state.db"
	s := openStoreAt(t, path)
	task := seedTask(t, s, "taskaaaaaaab").ID
	first := surface(t, s, task, "A", 1)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s = openStoreAt(t, path)
	again, err := s.AppendWakeWithMarkers(ctx, incarnationOf(t, s), WakeRequest{TaskID: task, Kind: first.Wake.Kind, Marker: first.Wake.Marker},
		MarkerAdvance{CaptureDigest: Known("A"), Surfaced: true, StallRepetitions: 1, EventSequence: 4})
	if err != nil {
		t.Fatalf("the replay: %v", err)
	}
	if again.Disposition != WakeAlreadyQueued || again.Wake.ID != first.Wake.ID {
		t.Fatalf("the replay = %s of wake %d, want already queued wake %d", again.Disposition, again.Wake.ID, first.Wake.ID)
	}
	if again.Markers.EventSequence != 4 {
		t.Fatalf("the replay left the markers at event %d, want them advanced to 4", again.Markers.EventSequence)
	}
	if wakes, err := s.TaskWakes(ctx, task); err != nil || len(wakes) != 1 {
		t.Fatalf("the replay left %d rows (%v), want 1", len(wakes), err)
	}
}

// The same key against a row already acknowledged is a marker that did not
// advance for a new surface, which is refused with ErrWakeHandled rather
// than swallowed, and the refusal rolls the markers back with it.
func TestAWakeConflictingWithAHandledRowIsRefused(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "taskaaaaaaab").ID
	first := surface(t, s, task, "A", 1)
	if err := s.AcknowledgeWakes(ctx, []int64{first.Wake.ID}, AcknowledgedByProgram()); err != nil {
		t.Fatalf("AcknowledgeWakes: %v", err)
	}
	_, err := s.AppendWakeWithMarkers(ctx, incarnationOf(t, s), WakeRequest{TaskID: task, Kind: first.Wake.Kind, Marker: first.Wake.Marker},
		MarkerAdvance{CaptureDigest: Known("A"), Surfaced: true, StallRepetitions: 9})
	if !errors.Is(err, ErrWakeHandled) {
		t.Fatalf("the same key after an acknowledgement = %v, want ErrWakeHandled", err)
	}
	m, _, err := s.WakeMarkers(ctx, task)
	if err != nil || m.StallRepetitions != 1 {
		t.Fatalf("the refused append moved the markers to %+v (%v)", m, err)
	}
}

// The wake row and the marker advance are one transaction, shown from both
// sides with a planted fault: with the wake insert refused the markers do
// not move, and with the marker write refused no wake lands.
func TestAWakeAndItsMarkersAreOneWrite(t *testing.T) {
	ctx := context.Background()
	for name, fault := range map[string]string{
		"the wake cannot be written": `CREATE TRIGGER fault BEFORE INSERT ON wake
			BEGIN SELECT RAISE(ABORT, 'planted fault'); END`,
		"the markers cannot be written": `CREATE TRIGGER fault BEFORE UPDATE ON wake_marker
			BEGIN SELECT RAISE(ABORT, 'planted fault'); END`,
	} {
		t.Run(name, func(t *testing.T) {
			s := openStore(t)
			task := seedTask(t, s, "taskaaaaaaab").ID
			before := surface(t, s, task, "A", 1).Markers
			if _, err := s.write.ExecContext(ctx, fault); err != nil {
				t.Fatalf("planting the fault: %v", err)
			}
			_, err := s.AppendWakeWithMarkers(ctx, incarnationOf(t, s), WakeRequest{TaskID: task, Kind: "terminal-unchanged", Marker: "B@2#1"},
				MarkerAdvance{CaptureDigest: Known("B"), Surfaced: true, StallRepetitions: 1})
			if err == nil || !strings.Contains(err.Error(), "planted fault") {
				t.Fatalf("the append succeeded past the planted fault (%v), so the control shows nothing", err)
			}
			after, _, err := s.WakeMarkers(ctx, task)
			if err != nil || after != before {
				t.Fatalf("the markers moved to %+v (%v) with the wake unrecorded; were %+v", after, err, before)
			}
			if wakes, err := s.TaskWakes(ctx, task); err != nil || len(wakes) != 1 {
				t.Fatalf("a wake landed without its markers: %d rows (%v)", len(wakes), err)
			}
		})
	}
}

// Reading the queue is a read. Two reads with nothing between them return
// the same rows in the same order, oldest first, bounded by the limit, and
// neither acknowledges anything.
func TestReadingTheQueueReturnsTheSameRows(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "taskaaaaaaab").ID
	for i := int64(1); i <= 3; i++ {
		surface(t, s, task, "A", i)
	}
	first, err := s.QueuedWakes(ctx, 10)
	if err != nil || len(first) != 3 {
		t.Fatalf("QueuedWakes = %d rows (%v), want 3", len(first), err)
	}
	second, err := s.QueuedWakes(ctx, 10)
	if err != nil || len(second) != 3 {
		t.Fatalf("the second read = %d rows (%v), want the same 3", len(second), err)
	}
	for i := range first {
		if first[i].ID != second[i].ID || !second[i].Queued() {
			t.Fatalf("read 2: row %d is %+v, the first read had %+v", i, second[i], first[i])
		}
		if i > 0 && first[i].ID <= first[i-1].ID {
			t.Fatalf("the read is not oldest first: %d then %d", first[i-1].ID, first[i].ID)
		}
	}
	bounded, err := s.QueuedWakes(ctx, 2)
	if err != nil || len(bounded) != 2 || bounded[0].ID != first[0].ID {
		t.Fatalf("QueuedWakes(2) = %+v (%v), want the two oldest", bounded, err)
	}
	if _, err := s.QueuedWakes(ctx, 0); err == nil {
		t.Fatal("a read with no bound was accepted")
	}
}

// Draining acknowledges exactly what it returned. A bounded drain returns
// the two oldest rows, acknowledged by the drainer and still kept; a second
// drain returns the one row the first left, and a third returns none. Every
// row is still in the task's history, so draining deleted nothing, and
// naming a drained row in an acknowledgement is refused because the drain
// already was one. A drain naming nobody, and one with no bound, are refused
// before anything is written.
func TestDrainingAcknowledgesWhatItReturned(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "taskaaaaaaab").ID
	var ids []int64
	for i := int64(1); i <= 3; i++ {
		ids = append(ids, surface(t, s, task, "A", i).Wake.ID)
	}
	if _, err := s.DrainWakes(ctx, 10, Acknowledger{}); !errors.Is(err, ErrNoAcknowledger) {
		t.Fatalf("a drain naming nobody = %v, want ErrNoAcknowledger", err)
	}
	if _, err := s.DrainWakes(ctx, 0, AcknowledgedByCoordinator()); err == nil {
		t.Fatal("a drain with no bound was accepted")
	}
	if left, _ := s.QueuedWakes(ctx, 10); len(left) != 3 {
		t.Fatalf("a refused drain acknowledged something: %d left", len(left))
	}

	first, err := s.DrainWakes(ctx, 2, AcknowledgedByCoordinator())
	if err != nil || len(first) != 2 || first[0].ID != ids[0] || first[1].ID != ids[1] {
		t.Fatalf("DrainWakes(2) = %+v (%v), want rows %d and %d", first, err, ids[0], ids[1])
	}
	for _, w := range first {
		if w.Queued() || w.AcknowledgedBy != Known(AcknowledgedByCoordinator()) {
			t.Fatalf("a drained row reads %+v, want it acknowledged by the coordinator", w)
		}
	}
	second, err := s.DrainWakes(ctx, 10, AcknowledgedByCoordinator())
	if err != nil || len(second) != 1 || second[0].ID != ids[2] {
		t.Fatalf("the second drain = %+v (%v), want only row %d", second, err, ids[2])
	}
	third, err := s.DrainWakes(ctx, 10, AcknowledgedByCoordinator())
	if err != nil || len(third) != 0 {
		t.Fatalf("the third drain = %+v (%v), want none", third, err)
	}

	kept, err := s.TaskWakes(ctx, task)
	if err != nil || len(kept) != 3 {
		t.Fatalf("after draining the history holds %d rows (%v), want all 3 kept", len(kept), err)
	}
	for _, w := range kept {
		if w.Queued() {
			t.Fatalf("row %d is still queued after it was drained", w.ID)
		}
	}
	var idErr *WakeIDError
	if err := s.AcknowledgeWakes(ctx, ids[:1], AcknowledgedByCoordinator()); !errors.As(err, &idErr) || !errors.Is(err, ErrWakeAcknowledged) {
		t.Fatalf("acknowledging a drained row = %v, want *WakeIDError with ErrWakeAcknowledged", err)
	}
}

// A drain is the acknowledgement, so a consumer that drained a row and
// crashed before acting on it is not handed the row again: delivery to a
// drain is at most once, which is what PRD section 8's "draining
// acknowledges a row" gives and what DrainWakes states. The crash is
// simulated by closing the store. What survives it is the row itself, kept
// and acknowledged by the drainer, so the history a reconciliation reads
// still shows that the wake surfaced.
func TestACrashAfterADrainDoesNotRedeliverAndKeepsTheRow(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/state.db"
	s := openStoreAt(t, path)
	task := seedTask(t, s, "taskaaaaaaab").ID
	surface(t, s, task, "A", 1)

	first, err := s.DrainWakes(ctx, 10, AcknowledgedByCoordinator())
	if err != nil || len(first) != 1 {
		t.Fatalf("DrainWakes = %+v (%v), want one row", first, err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s = openStoreAt(t, path)
	if again, err := s.DrainWakes(ctx, 10, AcknowledgedByCoordinator()); err != nil || len(again) != 0 {
		t.Fatalf("after the crash the drain = %+v (%v), want nothing: the first drain acknowledged it", again, err)
	}
	wakes, err := s.TaskWakes(ctx, task)
	if err != nil || len(wakes) != 1 || wakes[0].ID != first[0].ID || wakes[0].Queued() ||
		wakes[0].AcknowledgedBy != Known(AcknowledgedByCoordinator()) {
		t.Fatalf("the drained row reads back as %+v (%v), want it kept and acknowledged by the coordinator", wakes, err)
	}
}

// An acknowledgement is a batch: a subset acknowledged leaves the rest
// queued, an unknown identifier refuses the whole batch with a *WakeIDError
// naming it and writes nothing, and so does an identifier already
// acknowledged. Nobody acknowledging is refused before anything is read.
func TestAcknowledgingIsABatchThatNamesWhoAndRefusesWhatItCannot(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "taskaaaaaaab").ID
	var ids []int64
	for i := int64(1); i <= 3; i++ {
		ids = append(ids, surface(t, s, task, "A", i).Wake.ID)
	}

	if err := s.AcknowledgeWakes(ctx, ids[:1], Acknowledger{}); !errors.Is(err, ErrNoAcknowledger) {
		t.Fatalf("nobody acknowledging = %v, want ErrNoAcknowledger", err)
	}
	if err := s.AcknowledgeWakes(ctx, nil, AcknowledgedByProgram()); err == nil {
		t.Fatal("an empty batch was accepted")
	}

	err := s.AcknowledgeWakes(ctx, []int64{ids[0], 999}, AcknowledgedByProgram())
	var idErr *WakeIDError
	if !errors.As(err, &idErr) || idErr.ID != 999 || !errors.Is(err, ErrWakeUnknown) {
		t.Fatalf("a batch with an unknown id = %v, want *WakeIDError{999, ErrWakeUnknown}", err)
	}
	if left, _ := s.QueuedWakes(ctx, 10); len(left) != 3 {
		t.Fatalf("the refused batch acknowledged something: %d left", len(left))
	}

	if err := s.AcknowledgeWakes(ctx, ids[:2], AcknowledgedByProgram()); err != nil {
		t.Fatalf("acknowledging a subset: %v", err)
	}
	left, err := s.QueuedWakes(ctx, 10)
	if err != nil || len(left) != 1 || left[0].ID != ids[2] {
		t.Fatalf("after a subset the drain = %+v (%v), want only %d", left, err, ids[2])
	}

	err = s.AcknowledgeWakes(ctx, []int64{ids[2], ids[0]}, AcknowledgedByProgram())
	if !errors.As(err, &idErr) || idErr.ID != ids[0] || !errors.Is(err, ErrWakeAcknowledged) {
		t.Fatalf("a batch naming an acknowledged row = %v, want *WakeIDError{%d, ErrWakeAcknowledged}", err, ids[0])
	}
	if left, _ := s.QueuedWakes(ctx, 10); len(left) != 1 {
		t.Fatalf("the refused batch acknowledged the rest: %d left", len(left))
	}
}

// The queued count per task is what the in-flight table reads: a task with
// a queued wake counts, an acknowledged wake does not, and a task with none
// has no entry.
func TestUnacknowledgedWakesCountsQueuedRowsPerTask(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	one := seedTask(t, s, "taskaaaaaaab").ID
	two := seedTask(t, s, "taskaaaaaaac").ID
	seedTask(t, s, "taskaaaaaaad")
	surface(t, s, one, "A", 1)
	surface(t, s, one, "A", 2)
	acked := surface(t, s, two, "A", 1)
	if err := s.AcknowledgeWakes(ctx, []int64{acked.Wake.ID}, AcknowledgedByProgram()); err != nil {
		t.Fatalf("AcknowledgeWakes: %v", err)
	}
	counts, err := s.UnacknowledgedWakes(ctx)
	if err != nil {
		t.Fatalf("UnacknowledgedWakes: %v", err)
	}
	if len(counts) != 1 || counts[one] != 2 {
		t.Fatalf("UnacknowledgedWakes = %v, want {%s: 2}", counts, one)
	}
}

// The table is appended: no column but the acknowledgement may change, an
// acknowledgement is written once, and no row is deleted, all at the
// database rather than only at the accessor.
func TestAWakeRowIsAppendedAndAcknowledgedOnce(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "taskaaaaaaab").ID
	w := surface(t, s, task, "A", 1).Wake
	for name, stmt := range map[string]string{
		"rewriting the marker": `UPDATE wake SET marker = 'other' WHERE id = ?`,
		"rewriting the kind":   `UPDATE wake SET kind = 'other' WHERE id = ?`,
		"deleting the row":     `DELETE FROM wake WHERE id = ?`,
	} {
		if _, err := s.write.ExecContext(ctx, stmt, w.ID); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if err := s.AcknowledgeWakes(ctx, []int64{w.ID}, AcknowledgedByProgram()); err != nil {
		t.Fatalf("AcknowledgeWakes: %v", err)
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE wake SET acknowledged_by = 'coordinator' WHERE id = ?`, w.ID); err == nil {
		t.Error("a second acknowledgement at the database was accepted")
	}
	if _, err := s.write.ExecContext(ctx, `DELETE FROM wake WHERE id = ?`, w.ID); err == nil {
		t.Error("deleting an acknowledged row was accepted")
	}
}

// The store records a kind and a marker as words and refuses only their
// absence: what a kind means is internal/supervisor's table, and this
// package is not a second owner of it. A task that does not exist is
// ErrNotFound.
func TestAWakeIsRecordedWithoutTheStoreReadingItsKind(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "taskaaaaaaab").ID
	for name, req := range map[string]WakeRequest{
		"no kind":   {TaskID: task, Marker: "m"},
		"no marker": {TaskID: task, Kind: "hold-opened"},
		"no task":   {Kind: "hold-opened", Marker: "m"},
	} {
		if _, err := s.AppendWakeWithMarkers(ctx, incarnationOf(t, s), req, MarkerAdvance{}); err == nil {
			t.Errorf("a wake with %s was accepted", name)
		}
	}
	if _, err := s.AppendWakeWithMarkers(ctx, incarnationOf(t, s), WakeRequest{TaskID: "neveraaaaaaa", Kind: "hold-opened", Marker: "m"},
		MarkerAdvance{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a wake for a task that does not exist = %v, want ErrNotFound", err)
	}
	out, err := s.AppendWakeWithMarkers(ctx, incarnationOf(t, s), WakeRequest{TaskID: task, Kind: "a-kind-this-build-never-heard-of", Marker: "m"},
		MarkerAdvance{})
	if err != nil || out.Disposition != WakeAppended {
		t.Fatalf("a wake of a kind the store has no table for = %s (%v), want appended", out.Disposition, err)
	}
	if _, err := s.AdvanceWakeMarkers(ctx, incarnationOf(t, s), task, MarkerAdvance{EventSequence: -1}); err == nil {
		t.Error("a negative event sequence was accepted")
	}
}

// The markers are one row per task, rewritten in place, with every column
// the loop handed over read back as it was handed, and the epoch derived:
// it starts at zero with no digest, counts a first digest as a change, holds
// across an unchanged digest and a pass that only moves other columns, and
// counts a capture that failed as a change too.
func TestMarkersAreRewrittenInPlaceWithTheEpochDerived(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "taskaaaaaaab").ID
	if _, ok, err := s.WakeMarkers(ctx, task); err != nil || ok {
		t.Fatalf("a task no pass reached has markers: %v, %v", ok, err)
	}
	window := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	steps := []struct {
		advance MarkerAdvance
		epoch   int64
	}{
		{MarkerAdvance{EventSequence: 1}, 0},
		{MarkerAdvance{CaptureDigest: Known("A"), EventSequence: 2}, 1},
		{MarkerAdvance{CaptureDigest: Known("A"), EventSequence: 3, Surfaced: true, WindowStart: Known(window)}, 1},
		{MarkerAdvance{CaptureDigest: Known("A"), StallRepetitions: 2, UnobservedSince: Known(window.Add(time.Minute))}, 1},
		{MarkerAdvance{CaptureDigest: Known("A"), UnobservedSince: Known(window), GoneSurfacedAt: Known(window.Add(time.Hour))}, 1},
		{MarkerAdvance{EventSequence: 3}, 2},
		{MarkerAdvance{CaptureDigest: Known("A")}, 3},
	}
	for i, step := range steps {
		m, err := s.AdvanceWakeMarkers(ctx, incarnationOf(t, s), task, step.advance)
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		want := WakeMarkers{
			TaskID: task, CaptureDigest: step.advance.CaptureDigest, ChangeEpoch: step.epoch,
			EventSequence: step.advance.EventSequence, Surfaced: step.advance.Surfaced,
			WindowStart: step.advance.WindowStart, StallRepetitions: step.advance.StallRepetitions,
			UnobservedSince: step.advance.UnobservedSince, GoneSurfacedAt: step.advance.GoneSurfacedAt,
			UpdatedAt: m.UpdatedAt,
		}
		if m != want {
			t.Fatalf("step %d: markers = %+v, want %+v", i, m, want)
		}
		read, ok, err := s.WakeMarkers(ctx, task)
		if err != nil || !ok || read != m {
			t.Fatalf("step %d: the read back = %+v, %v, %v", i, read, ok, err)
		}
	}
}
