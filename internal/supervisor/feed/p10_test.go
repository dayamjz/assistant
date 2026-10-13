package feed_test

import (
	"os"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/supervisor/feed"
)

// P10 through the real mechanism: a real lock a second process holds, a real
// beacon file the real writer wrote, a real instance file internal/home wrote
// and a real database the read-only open reads one task in flight from. Each
// case breaks one of the facts Assess compares and the guard blocks; the
// positive control leaves every one intact and the guard allows, which is
// what shows the blocking cases block for the reason named rather than for
// something the setup got wrong.
//
// Section 13's two named cases are here: a stale beacon blocks although the
// process holding the lock is alive, and a fresh beacon blocks when the lock
// is free. The epoch case expects an epoch through Assess directly, because
// the pull guard expects none; the feeder's reading is still what it judges.
func TestWorkInFlightIsSupervisedOnlyByALiveLoopHoldingTheLock(t *testing.T) {
	principles.Cite(t, principles.P10)
	interval := 10 * time.Second
	fresh := supervisor.FreshnessFor(interval)

	cases := []struct {
		name  string
		plant func(t *testing.T, h *planted)
		want  supervisor.StopReason
	}{
		{"positive control: lock held by a live process, matching fresh beacon, one task in flight",
			func(t *testing.T, h *planted) {
				holdInAnotherProcess(t, h.Home)
				writeInstance(t, h.Home, instance)
				writeBeacon(t, h.Home, supervisor.Identity{Instance: instance, Epoch: 1}, h.now)
			}, supervisor.StopSupervised},
		{"a stale beacon blocks while the process holding the lock is alive",
			func(t *testing.T, h *planted) {
				holdInAnotherProcess(t, h.Home)
				writeInstance(t, h.Home, instance)
				writeBeacon(t, h.Home, supervisor.Identity{Instance: instance, Epoch: 1}, h.now.Add(-fresh.Window))
			}, supervisor.StopBeaconStale},
		{"a fresh beacon blocks when no process holds the lock",
			func(t *testing.T, h *planted) {
				writeInstance(t, h.Home, instance)
				writeBeacon(t, h.Home, supervisor.Identity{Instance: instance, Epoch: 1}, h.now)
			}, supervisor.StopLockFree},
		{"a beacon from another instance blocks while a process holds the lock",
			func(t *testing.T, h *planted) {
				holdInAnotherProcess(t, h.Home)
				writeInstance(t, h.Home, "fedcba9876543210")
				writeBeacon(t, h.Home, supervisor.Identity{Instance: instance, Epoch: 1}, h.now)
			}, supervisor.StopInstanceMismatch},
		{"a beacon timestamped past the tolerance blocks",
			func(t *testing.T, h *planted) {
				holdInAnotherProcess(t, h.Home)
				writeInstance(t, h.Home, instance)
				writeBeacon(t, h.Home, supervisor.Identity{Instance: instance, Epoch: 1}, h.now.Add(fresh.Ahead+time.Second))
			}, supervisor.StopBeaconAhead},
		{"no beacon file blocks",
			func(t *testing.T, h *planted) {
				holdInAnotherProcess(t, h.Home)
				writeInstance(t, h.Home, instance)
			}, supervisor.StopNoBeacon},
		{"a beacon that does not decode blocks",
			func(t *testing.T, h *planted) {
				holdInAnotherProcess(t, h.Home)
				writeInstance(t, h.Home, instance)
				if err := os.WriteFile(h.Beacon(), []byte("not a record"), 0o600); err != nil {
					t.Fatal(err)
				}
			}, supervisor.StopNoBeacon},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := &planted{Home: createdHome(t), now: time.Now()}
			oneTaskInFlight(t, h.Home)
			c.plant(t, h)
			got := feed.Guard(t.Context(), h.Home, fresh, h.now)
			if got.Reason != c.want {
				t.Fatalf("the guard answered %s, want %s", got.Reason, c.want)
			}
			if got.Allowed() != (c.want == supervisor.StopSupervised) {
				t.Fatalf("Allowed() = %v beside %s", got.Allowed(), got.Reason)
			}
		})
	}

	// The epoch: the same live home read through the feeder and judged by
	// Assess against an incarnation the hook expects. The beacon's own
	// epoch allows, and another blocks, so the fact travels through the file.
	t.Run("a beacon from another loop incarnation blocks the hook that expects one", func(t *testing.T) {
		h := &planted{Home: createdHome(t), now: time.Now()}
		oneTaskInFlight(t, h.Home)
		holdInAnotherProcess(t, h.Home)
		writeInstance(t, h.Home, instance)
		writeBeacon(t, h.Home, supervisor.Identity{Instance: instance, Epoch: 4}, h.now)
		obs, err := feed.Observe(t.Context(), h.Home)
		if err != nil {
			t.Fatalf("observing a live home: %v", err)
		}
		if got := supervisor.Assess(obs.InFlight, obs.Beacon, obs.Lock, supervisor.Identity{Instance: instance, Epoch: 4}, fresh, h.now); got.Reason != supervisor.StopSupervised {
			t.Fatalf("the beacon's own epoch answered %s, want supervised", got.Reason)
		}
		if got := supervisor.Assess(obs.InFlight, obs.Beacon, obs.Lock, supervisor.Identity{Instance: instance, Epoch: 5}, fresh, h.now); got.Reason != supervisor.StopEpochMismatch {
			t.Fatalf("another epoch answered %s, want epoch-mismatch", got.Reason)
		}
	})

	// With nothing in flight no supervision is owed, whatever the files say,
	// so the in-flight read is what the other cases rest on and not a
	// constant.
	t.Run("nothing in flight allows with no service at all", func(t *testing.T) {
		h := createdHome(t)
		withDatabase(t, h, nil)
		if got := feed.Guard(t.Context(), h, fresh, time.Now()); got.Reason != supervisor.StopNothingInFlight {
			t.Fatalf("an empty fleet answered %s, want nothing-in-flight", got.Reason)
		}
	})
}

// planted is a created home with the instant its beacon is judged at.
type planted struct {
	*home.Home
	now time.Time
}
