package supervisor_test

import (
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/supervisor"
)

// ArmOf over every shape a look can take: the lock decides at once, a fresh
// matching beacon settles as attached or started by its sequence against the
// one that was there at the ask, and everything else waits for the wait to be
// over and then names what was found.
func TestArmOfDecidesEachLook(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	fresh := supervisor.FreshnessFor(10 * time.Second)
	held := home.LockReading{State: home.LockHeld}
	beacon := func(instance string, seq uint64, age time.Duration) supervisor.Beacon {
		return supervisor.Beacon{Identity: supervisor.Identity{Instance: instance}, Sequence: seq, WrittenAt: now.Add(-age)}
	}
	obs := func(lock home.LockReading, instance string, b supervisor.Beacon) supervisor.Observation {
		return supervisor.Observation{Lock: lock, Instance: instance, Beacon: b}
	}
	cases := []struct {
		name     string
		asked    supervisor.Beacon
		obs      supervisor.Observation
		waitOver bool
		want     supervisor.Arm
		settled  bool
	}{
		{"lock free is the service down, at once",
			supervisor.Beacon{}, obs(home.LockReading{State: home.LockFree}, "svc", beacon("svc", 1, 0)), false,
			supervisor.Arm{Outcome: supervisor.ArmFailed, Reason: supervisor.ArmServiceDown}, true},
		{"lock unknown is the service down",
			supervisor.Beacon{}, obs(home.LockReading{}, "svc", beacon("svc", 1, 0)), true,
			supervisor.Arm{Outcome: supervisor.ArmFailed, Reason: supervisor.ArmServiceDown}, true},
		{"no instance file is the service down",
			supervisor.Beacon{}, obs(held, "", beacon("svc", 1, 0)), true,
			supervisor.Arm{Outcome: supervisor.ArmFailed, Reason: supervisor.ArmServiceDown}, true},
		{"a fresh beacon already there at the ask is attached",
			beacon("svc", 3, time.Second), obs(held, "svc", beacon("svc", 3, time.Second)), false,
			supervisor.Arm{Outcome: supervisor.ArmAttached, Sequence: 3}, true},
		{"a fresh beacon past the asked sequence is started",
			beacon("svc", 3, time.Second), obs(held, "svc", beacon("svc", 4, 0)), false,
			supervisor.Arm{Outcome: supervisor.ArmStarted, Sequence: 4}, true},
		{"a fresh beacon where none was at the ask is started",
			supervisor.Beacon{}, obs(held, "svc", beacon("svc", 1, 0)), false,
			supervisor.Arm{Outcome: supervisor.ArmStarted, Sequence: 1}, true},
		{"a fresh beacon after an earlier instance's is started, whatever the sequence",
			beacon("old", 9, time.Second), obs(held, "svc", beacon("svc", 1, 0)), false,
			supervisor.Arm{Outcome: supervisor.ArmStarted, Sequence: 1}, true},
		{"a stale matching beacon waits",
			supervisor.Beacon{}, obs(held, "svc", beacon("svc", 1, time.Minute)), false,
			supervisor.Arm{}, false},
		{"a stale matching beacon is the loop stale once the wait is over",
			supervisor.Beacon{}, obs(held, "svc", beacon("svc", 1, time.Minute)), true,
			supervisor.Arm{Outcome: supervisor.ArmFailed, Reason: supervisor.ArmLoopStale, Sequence: 1}, true},
		{"a beacon ahead past the tolerance is not fresh",
			supervisor.Beacon{}, obs(held, "svc", beacon("svc", 1, -(supervisor.BeaconAheadTolerance+time.Second))), true,
			supervisor.Arm{Outcome: supervisor.ArmFailed, Reason: supervisor.ArmLoopStale, Sequence: 1}, true},
		{"another instance's beacon waits",
			supervisor.Beacon{}, obs(held, "svc", beacon("old", 1, 0)), false,
			supervisor.Arm{}, false},
		{"another instance's beacon is a mismatch once the wait is over",
			supervisor.Beacon{}, obs(held, "svc", beacon("old", 1, 0)), true,
			supervisor.Arm{Outcome: supervisor.ArmFailed, Reason: supervisor.ArmInstanceMismatch, Sequence: 1}, true},
		{"no beacon waits",
			supervisor.Beacon{}, obs(held, "svc", supervisor.Beacon{}), false,
			supervisor.Arm{}, false},
		{"no beacon is no beacon once the wait is over",
			supervisor.Beacon{}, obs(held, "svc", supervisor.Beacon{}), true,
			supervisor.Arm{Outcome: supervisor.ArmFailed, Reason: supervisor.ArmNoBeacon}, true},
	}
	for _, c := range cases {
		got, settled := supervisor.ArmOf(c.asked, c.obs, fresh, now, c.waitOver)
		if got != c.want || settled != c.settled {
			t.Errorf("%s: ArmOf = (%+v, %v), want (%+v, %v)", c.name, got, settled, c.want, c.settled)
		}
	}
}

// GuardOf expects the instance file's identity and the beacon's own epoch, so
// a beacon under the file's instance passes whatever its epoch, and one under
// another instance, or with no instance file to compare against, blocks.
func TestGuardOfExpectsTheInstanceFileAndTheBeaconsOwnEpoch(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	fresh := supervisor.FreshnessFor(10 * time.Second)
	live := supervisor.Observation{
		InFlight: supervisor.InFlightSome,
		Lock:     home.LockReading{State: home.LockHeld},
		Instance: "svc",
		Beacon:   supervisor.Beacon{Identity: supervisor.Identity{Instance: "svc", Epoch: 9}, Sequence: 1, WrittenAt: now},
	}
	if got := supervisor.GuardOf(live, fresh, now); got.Reason != supervisor.StopSupervised {
		t.Errorf("a live observation under epoch 9 with no epoch expected is %s, want supervised", got.Reason)
	}
	other := live
	other.Beacon.Instance = "old"
	if got := supervisor.GuardOf(other, fresh, now); got.Reason != supervisor.StopInstanceMismatch {
		t.Errorf("a beacon under another instance is %s, want instance-mismatch", got.Reason)
	}
	none := live
	none.Instance = ""
	if got := supervisor.GuardOf(none, fresh, now); got.Allowed() {
		t.Errorf("no instance file is %s, want blocked", got.Reason)
	}
	if got := supervisor.GuardOf(supervisor.Observation{}, fresh, now); got.Allowed() {
		t.Errorf("the zero observation is %s, want blocked", got.Reason)
	}
}
