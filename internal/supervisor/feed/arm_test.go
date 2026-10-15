package feed_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/supervisor/feed"
)

// armInterval is short enough that the first-beacon wait is a fraction of a
// second and long enough that the looks inside it are not one.
const armInterval = 40 * time.Millisecond

// VerifyArm over the real files and lock: attached to a loop already writing,
// started when the first beacon lands during the wait, and each failure for
// its named reason when the wait ends. The sequence on each answer is the
// beacon's.
func TestVerifyArmAnswersFromTheFilesAndTheLock(t *testing.T) {
	t.Parallel()
	id := supervisor.Identity{Instance: instance, Epoch: 1}

	t.Run("attached to a loop that was already live", func(t *testing.T) {
		t.Parallel()
		h := createdHome(t)
		holdInAnotherProcess(t, h)
		writeInstance(t, h, instance)
		b := writeBeacon(t, h, id, time.Now())
		// The looks are judged at the instant the beacon was written, so
		// it is fresh however long this process takes to get to them; at
		// the wall clock a loaded machine could age it past the window
		// first and answer stale for a beacon this test wrote live.
		at := func() time.Time { return b.WrittenAt }
		arm, err := feed.VerifyArm(t.Context(), h, armInterval, at)
		if err != nil || arm != (supervisor.Arm{Outcome: supervisor.ArmAttached, Sequence: b.Sequence}) {
			t.Fatalf("VerifyArm = (%+v, %v), want attached at sequence %d", arm, err, b.Sequence)
		}
	})
	t.Run("started by a first beacon written during the wait", func(t *testing.T) {
		t.Parallel()
		h := createdHome(t)
		holdInAnotherProcess(t, h)
		writeInstance(t, h, instance)
		// The beacon is written once VerifyArm has read the clock for its
		// first look, which is after it read what was there before it
		// asked, so the write lands during the wait by construction rather
		// than by a sleep a loaded machine can outlast. The clock stands
		// still at the instant the beacon is written, so the wait never
		// runs out and the beacon is fresh whenever it is looked at: the
		// only way out is seeing it, or the bound below if it never is.
		at := time.Now()
		looked := make(chan struct{})
		reads := 0
		clock := func() time.Time {
			if reads++; reads == 2 {
				close(looked)
			}
			return at
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		wrote := make(chan struct{})
		go func() {
			defer close(wrote)
			select {
			case <-looked:
			case <-ctx.Done():
				return
			}
			w, err := feed.OpenWriter(h.Beacon())
			if err != nil {
				t.Error(err)
				return
			}
			defer func() { _ = w.Close() }()
			if _, err := w.Write(id, at); err != nil {
				t.Error(err)
			}
		}()
		arm, err := feed.VerifyArm(ctx, h, armInterval, clock)
		cancel()
		<-wrote
		if err != nil || arm != (supervisor.Arm{Outcome: supervisor.ArmStarted, Sequence: 1}) {
			t.Fatalf("VerifyArm = (%+v, %v), want started at sequence 1", arm, err)
		}
	})
	t.Run("the service down, at once", func(t *testing.T) {
		t.Parallel()
		h := createdHome(t)
		writeInstance(t, h, instance)
		writeBeacon(t, h, id, time.Now())
		// At an hour's interval the pause between looks is
		// FirstBeaconWait/20, fifteen minutes, so a VerifyArm that waited on
		// a free lock at all would still be paused when the bound ends it,
		// and would answer no beacon with the context's error. Answering
		// service down without an error is answering before any pause.
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		arm, err := feed.VerifyArm(ctx, h, time.Hour, time.Now)
		if err != nil || arm != (supervisor.Arm{Outcome: supervisor.ArmFailed, Reason: supervisor.ArmServiceDown}) {
			t.Fatalf("VerifyArm = (%+v, %v), want failed: service down, answered without waiting, "+
				"although waiting cannot make a service appear", arm, err)
		}
	})
	t.Run("no beacon within the wait", func(t *testing.T) {
		t.Parallel()
		h := createdHome(t)
		holdInAnotherProcess(t, h)
		writeInstance(t, h, instance)
		arm, err := feed.VerifyArm(t.Context(), h, armInterval, time.Now)
		if err != nil || arm != (supervisor.Arm{Outcome: supervisor.ArmFailed, Reason: supervisor.ArmNoBeacon}) {
			t.Fatalf("VerifyArm = (%+v, %v), want failed: no beacon", arm, err)
		}
	})
	t.Run("a beacon under another instance", func(t *testing.T) {
		t.Parallel()
		h := createdHome(t)
		holdInAnotherProcess(t, h)
		writeInstance(t, h, "fedcba9876543210")
		writeBeacon(t, h, id, time.Now())
		arm, err := feed.VerifyArm(t.Context(), h, armInterval, time.Now)
		if err != nil || arm != (supervisor.Arm{Outcome: supervisor.ArmFailed, Reason: supervisor.ArmInstanceMismatch, Sequence: 1}) {
			t.Fatalf("VerifyArm = (%+v, %v), want failed: instance mismatch", arm, err)
		}
	})
	t.Run("a loop that stopped writing", func(t *testing.T) {
		t.Parallel()
		h := createdHome(t)
		holdInAnotherProcess(t, h)
		writeInstance(t, h, instance)
		writeBeacon(t, h, id, time.Now().Add(-time.Hour))
		arm, err := feed.VerifyArm(t.Context(), h, armInterval, time.Now)
		if err != nil || arm != (supervisor.Arm{Outcome: supervisor.ArmFailed, Reason: supervisor.ArmLoopStale, Sequence: 1}) {
			t.Fatalf("VerifyArm = (%+v, %v), want failed: loop stale", arm, err)
		}
	})
	t.Run("a context that ends first is no beacon with the context's error", func(t *testing.T) {
		t.Parallel()
		h := createdHome(t)
		holdInAnotherProcess(t, h)
		writeInstance(t, h, instance)
		ctx, cancel := context.WithTimeout(t.Context(), armInterval)
		defer cancel()
		arm, err := feed.VerifyArm(ctx, h, time.Minute, time.Now)
		if err == nil || arm.Reason != supervisor.ArmNoBeacon {
			t.Fatalf("VerifyArm = (%+v, %v), want no beacon with the context's error", arm, err)
		}
	})
	t.Run("a context already ended is no beacon with its error, not the service down", func(t *testing.T) {
		t.Parallel()
		h := createdHome(t)
		holdInAnotherProcess(t, h)
		writeInstance(t, h, instance)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		arm, err := feed.VerifyArm(ctx, h, armInterval, time.Now)
		if !errors.Is(err, context.Canceled) || arm != (supervisor.Arm{Outcome: supervisor.ArmFailed, Reason: supervisor.ArmNoBeacon}) {
			t.Fatalf("VerifyArm = (%+v, %v), want failed: no beacon with the context's error, "+
				"since a lock probed under an ended context was not read", arm, err)
		}
	})
	t.Run("a beacon file that does not decode is no beacon", func(t *testing.T) {
		t.Parallel()
		h := createdHome(t)
		holdInAnotherProcess(t, h)
		writeInstance(t, h, instance)
		if err := os.WriteFile(h.Beacon(), []byte("torn"), 0o600); err != nil {
			t.Fatal(err)
		}
		arm, err := feed.VerifyArm(t.Context(), h, armInterval, time.Now)
		if err != nil || arm.Reason != supervisor.ArmNoBeacon {
			t.Fatalf("VerifyArm = (%+v, %v), want failed: no beacon", arm, err)
		}
	})
}
