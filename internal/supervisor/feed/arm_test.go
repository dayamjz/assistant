package feed_test

import (
	"context"
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
		arm, err := feed.VerifyArm(t.Context(), h, armInterval, time.Now)
		if err != nil || arm != (supervisor.Arm{Outcome: supervisor.ArmAttached, Sequence: b.Sequence}) {
			t.Fatalf("VerifyArm = (%+v, %v), want attached at sequence %d", arm, err, b.Sequence)
		}
	})
	t.Run("started by a first beacon written during the wait", func(t *testing.T) {
		t.Parallel()
		h := createdHome(t)
		holdInAnotherProcess(t, h)
		writeInstance(t, h, instance)
		go func() {
			time.Sleep(supervisor.FirstBeaconWait(armInterval) / 2)
			w, err := feed.OpenWriter(h.Beacon())
			if err != nil {
				t.Error(err)
				return
			}
			defer func() { _ = w.Close() }()
			if _, err := w.Write(id, time.Now()); err != nil {
				t.Error(err)
			}
		}()
		arm, err := feed.VerifyArm(t.Context(), h, armInterval, time.Now)
		if err != nil || arm != (supervisor.Arm{Outcome: supervisor.ArmStarted, Sequence: 1}) {
			t.Fatalf("VerifyArm = (%+v, %v), want started at sequence 1", arm, err)
		}
	})
	t.Run("the service down, at once", func(t *testing.T) {
		t.Parallel()
		h := createdHome(t)
		writeInstance(t, h, instance)
		writeBeacon(t, h, id, time.Now())
		began := time.Now()
		arm, err := feed.VerifyArm(t.Context(), h, armInterval, time.Now)
		if err != nil || arm != (supervisor.Arm{Outcome: supervisor.ArmFailed, Reason: supervisor.ArmServiceDown}) {
			t.Fatalf("VerifyArm = (%+v, %v), want failed: service down", arm, err)
		}
		if time.Since(began) >= supervisor.FirstBeaconWait(armInterval) {
			t.Fatal("a free lock was waited on, although waiting cannot make a service appear")
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
