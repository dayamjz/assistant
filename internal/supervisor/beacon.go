package supervisor

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"time"
)

// The two multiples the supervision durations derive from the one configured
// row, config.KeySupervisionBeaconInterval. Neither is a row of its own: a
// staleness window set apart from the interval it is a multiple of would let
// an operator make a healthy loop read stale, or a stopped one read live, by
// setting the two inconsistently. Both are chosen, not measured.
const (
	// StalenessMultiple is how many intervals may pass without a beacon
	// before the loop is read as stopped. Three absorbs one missed write and
	// one late one; a fourth miss is a loop that is not writing.
	StalenessMultiple = 3
	// FirstBeaconWaitMultiple is how many intervals arm verification waits
	// for a loop's first beacon. It is longer than the staleness window
	// because a starting loop has recovery to do before its first pass.
	FirstBeaconWaitMultiple = 5
)

// BeaconAheadTolerance is how far past the reader's clock a beacon's
// timestamp may sit and still be read. Two processes on one machine share a
// clock, so the tolerance covers the resolution of a wall-clock step between
// the write and the read and nothing larger; a beacon further ahead is a
// clock set back, under which a wedged loop would read fresh until the skew
// elapsed, and is refused instead. Chosen, not measured.
const BeaconAheadTolerance = 30 * time.Second

// FreshnessFor derives the Freshness Assess judges a beacon by from the
// configured interval: the staleness window is StalenessMultiple intervals,
// and the ahead tolerance is BeaconAheadTolerance. It is the one place the
// interval becomes a window, so the two cannot be configured apart.
func FreshnessFor(interval time.Duration) Freshness {
	return Freshness{Window: StalenessMultiple * interval, Ahead: BeaconAheadTolerance}
}

// FirstBeaconWait is how long arm verification waits for a loop's first
// beacon at the configured interval: FirstBeaconWaitMultiple intervals.
func FirstBeaconWait(interval time.Duration) time.Duration {
	return FirstBeaconWaitMultiple * interval
}

// ErrBeaconUnreadable reports a beacon record that does not decode: the wrong
// length, a checksum that does not match, or an instance that is not one
// line. A torn read, where a reader saw part of one write and part of the
// next, is one of these, and the reader in supervisor/feed retries once
// before answering it.
var ErrBeaconUnreadable = errors.New("supervisor: the beacon does not decode")

// The record's layout. Every field is fixed-size so the file is one write of
// BeaconSize bytes rewritten in place, and a reader that sees fewer bytes, or
// more, has not seen a record.
const (
	// BeaconSize is the record's length in bytes, which is also what the
	// length field holds.
	BeaconSize = beaconInstanceOffset + BeaconInstanceSize + 8 + 8 + 8
	// BeaconInstanceSize is the room the instance identity has. A longer one
	// does not fit and is refused by EncodeBeacon; a shorter one is padded
	// with zero bytes, which the identity may not contain.
	BeaconInstanceSize = 64

	beaconLengthOffset   = 0
	beaconChecksumOffset = 2
	beaconInstanceOffset = 6
	beaconEpochOffset    = beaconInstanceOffset + BeaconInstanceSize
	beaconSequenceOffset = beaconEpochOffset + 8
	beaconWrittenOffset  = beaconSequenceOffset + 8
)

// EncodeBeacon lays a beacon out as the fixed-size record the file holds: a
// length, a checksum over everything after it, the instance identity padded
// to BeaconInstanceSize, the epoch, the sequence, and the write time as
// nanoseconds since the Unix epoch in UTC. It refuses an instance that is
// empty, that does not fit, or that holds a zero byte, because the padding is
// zero bytes and the decoder stops at the first.
func EncodeBeacon(b Beacon) ([]byte, error) {
	if b.Instance == "" {
		return nil, fmt.Errorf("supervisor: encoding a beacon with no instance")
	}
	if len(b.Instance) > BeaconInstanceSize {
		return nil, fmt.Errorf("supervisor: encoding a beacon: the instance %q is longer than %d bytes", b.Instance, BeaconInstanceSize)
	}
	for i := 0; i < len(b.Instance); i++ {
		if b.Instance[i] == 0 {
			return nil, fmt.Errorf("supervisor: encoding a beacon: the instance holds a zero byte")
		}
	}
	out := make([]byte, BeaconSize)
	binary.LittleEndian.PutUint16(out[beaconLengthOffset:], BeaconSize)
	copy(out[beaconInstanceOffset:], b.Instance)
	binary.LittleEndian.PutUint64(out[beaconEpochOffset:], b.Epoch)
	binary.LittleEndian.PutUint64(out[beaconSequenceOffset:], b.Sequence)
	binary.LittleEndian.PutUint64(out[beaconWrittenOffset:], uint64(b.WrittenAt.UnixNano()))
	binary.LittleEndian.PutUint32(out[beaconChecksumOffset:], crc32.ChecksumIEEE(out[beaconInstanceOffset:]))
	return out, nil
}

// DecodeBeacon reads a record EncodeBeacon laid out. Anything that is not
// exactly one such record, in length or in checksum, is ErrBeaconUnreadable,
// which is what a torn read, an empty file and a file of another shape all
// come back as; the caller decides whether to read again. A record that
// decodes is one that was written whole by EncodeBeacon, to within the
// checksum's strength, which is a 32-bit CRC and not a signature: nothing
// here establishes who wrote it.
func DecodeBeacon(raw []byte) (Beacon, error) {
	if len(raw) != BeaconSize {
		return Beacon{}, fmt.Errorf("%w: %d bytes, not %d", ErrBeaconUnreadable, len(raw), BeaconSize)
	}
	if got := binary.LittleEndian.Uint16(raw[beaconLengthOffset:]); got != BeaconSize {
		return Beacon{}, fmt.Errorf("%w: the length field says %d, not %d", ErrBeaconUnreadable, got, BeaconSize)
	}
	want := binary.LittleEndian.Uint32(raw[beaconChecksumOffset:])
	if got := crc32.ChecksumIEEE(raw[beaconInstanceOffset:]); got != want {
		return Beacon{}, fmt.Errorf("%w: the checksum does not match", ErrBeaconUnreadable)
	}
	instance := raw[beaconInstanceOffset : beaconInstanceOffset+BeaconInstanceSize]
	end := 0
	for end < len(instance) && instance[end] != 0 {
		end++
	}
	if end == 0 {
		return Beacon{}, fmt.Errorf("%w: the instance is empty", ErrBeaconUnreadable)
	}
	nanos := int64(binary.LittleEndian.Uint64(raw[beaconWrittenOffset:]))
	return Beacon{
		Identity: Identity{
			Instance: string(instance[:end]),
			Epoch:    binary.LittleEndian.Uint64(raw[beaconEpochOffset:]),
		},
		Sequence:  binary.LittleEndian.Uint64(raw[beaconSequenceOffset:]),
		WrittenAt: time.Unix(0, nanos).UTC(),
	}, nil
}
