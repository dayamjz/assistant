package supervisor_test

import (
	"errors"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/supervisor"
)

// A beacon survives the record whole: every field comes back, the time to the
// nanosecond in UTC, and the record is exactly the size the layout declares.
func TestABeaconRoundTripsThroughTheRecord(t *testing.T) {
	t.Parallel()
	want := supervisor.Beacon{
		Identity:  supervisor.Identity{Instance: "0123456789abcdef", Epoch: 7},
		Sequence:  42,
		WrittenAt: time.Date(2026, 10, 13, 12, 0, 0, 123456789, time.UTC),
	}
	raw, err := supervisor.EncodeBeacon(want)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if len(raw) != supervisor.BeaconSize {
		t.Fatalf("the record is %d bytes, want %d", len(raw), supervisor.BeaconSize)
	}
	got, err := supervisor.DecodeBeacon(raw)
	if err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if got != want {
		t.Fatalf("decoded %+v, want %+v", got, want)
	}
}

// A record that is not one whole write is unreadable: short, long, a flipped
// byte under the checksum, a length field that lies, and nothing at all.
// Each is ErrBeaconUnreadable and never a beacon with some of the fields.
func TestARecordNotWrittenWholeIsUnreadable(t *testing.T) {
	t.Parallel()
	good, err := supervisor.EncodeBeacon(supervisor.Beacon{
		Identity: supervisor.Identity{Instance: "abc", Epoch: 1}, Sequence: 1, WrittenAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	flipped := append([]byte(nil), good...)
	flipped[len(flipped)-1] ^= 0xff
	lying := append([]byte(nil), good...)
	lying[0] ^= 0x01
	for name, raw := range map[string][]byte{
		"short":   good[:len(good)-1],
		"long":    append(append([]byte(nil), good...), 0),
		"flipped": flipped,
		"length":  lying,
		"empty":   nil,
	} {
		if _, err := supervisor.DecodeBeacon(raw); !errors.Is(err, supervisor.ErrBeaconUnreadable) {
			t.Errorf("%s: decoded with %v, want ErrBeaconUnreadable", name, err)
		}
	}
	// An instance the record cannot hold is refused at the encoder rather
	// than silently truncated into another identity.
	for name, instance := range map[string]string{
		"empty": "", "zero byte": "a\x00b", "too long": string(make([]byte, supervisor.BeaconInstanceSize+1)),
	} {
		if _, err := supervisor.EncodeBeacon(supervisor.Beacon{Identity: supervisor.Identity{Instance: instance}}); err == nil {
			t.Errorf("%s: an instance of %q was encoded", name, instance)
		}
	}
}

// The two windows derive from the one interval by the named multiples, so
// configuring the interval configures both and neither can be set apart.
func TestFreshnessDerivesFromTheOneInterval(t *testing.T) {
	t.Parallel()
	interval := 10 * time.Second
	fresh := supervisor.FreshnessFor(interval)
	if fresh.Window != supervisor.StalenessMultiple*interval {
		t.Errorf("Window = %v, want %d intervals", fresh.Window, supervisor.StalenessMultiple)
	}
	if fresh.Ahead != supervisor.BeaconAheadTolerance {
		t.Errorf("Ahead = %v, want the tolerance constant", fresh.Ahead)
	}
	if got := supervisor.FirstBeaconWait(interval); got != supervisor.FirstBeaconWaitMultiple*interval {
		t.Errorf("FirstBeaconWait = %v, want %d intervals", got, supervisor.FirstBeaconWaitMultiple)
	}
	if supervisor.FirstBeaconWaitMultiple <= supervisor.StalenessMultiple {
		t.Error("the first-beacon wait is not longer than the staleness window, so a loop still recovering would read stale before it could write")
	}
}

// StopReasons is the whole set Assess can answer with, in the order it checks
// them, so a reader deriving a vocabulary from it misses none.
func TestStopReasonsIsTheWholeSet(t *testing.T) {
	t.Parallel()
	seen := map[supervisor.StopReason]bool{}
	for _, r := range supervisor.StopReasons() {
		seen[r] = true
	}
	for _, r := range []supervisor.StopReason{
		supervisor.StopNothingInFlight, supervisor.StopSupervised, supervisor.StopInFlightUnknown,
		supervisor.StopLockUnknown, supervisor.StopLockFree, supervisor.StopNoBeacon,
		supervisor.StopInstanceMismatch, supervisor.StopEpochMismatch, supervisor.StopBeaconAhead,
		supervisor.StopBeaconStale,
	} {
		if !seen[r] {
			t.Errorf("%s is missing from StopReasons", r)
		}
	}
	if len(seen) != 10 {
		t.Errorf("StopReasons has %d distinct reasons, want 10", len(seen))
	}
}
