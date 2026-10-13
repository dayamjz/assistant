package feed_test

import (
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/supervisor/feed"
)

// The writer rewrites in place and the reader reads whole: with a reader
// looping while the writer loops, every read that decodes is a record the
// writer wrote, under the writer's instance with a sequence no earlier read
// exceeded, and a read that does not decode is answered unreadable and never
// as a record with some other field. The file is never recreated, so the
// reader's handle on any platform the module builds for is always on the
// writer's file.
func TestAReaderLoopingBesideTheWriterSeesWholeRecordsOrNone(t *testing.T) {
	t.Parallel()
	h := createdHome(t)
	w, err := feed.OpenWriter(h.Beacon())
	if err != nil {
		t.Fatalf("opening the writer: %v", err)
	}
	defer func() { _ = w.Close() }()
	id := supervisor.Identity{Instance: instance, Epoch: 2}
	if _, err := w.Write(id, time.Now()); err != nil {
		t.Fatalf("the first write: %v", err)
	}

	const writes = 2000
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < writes; i++ {
			if _, err := w.Write(id, time.Now()); err != nil {
				t.Errorf("write %d: %v", i, err)
				return
			}
		}
	}()
	var decoded, unreadable int
	var last uint64
	for i := 0; i < writes; i++ {
		b, err := feed.ReadBeacon(h.Beacon())
		if err != nil {
			if !errors.Is(err, supervisor.ErrBeaconUnreadable) {
				t.Fatalf("read %d failed with %v, which is not a torn read", i, err)
			}
			unreadable++
			continue
		}
		decoded++
		if b.Identity != id {
			t.Fatalf("read %d decoded identity %+v, want %+v", i, b.Identity, id)
		}
		if b.Sequence < last {
			t.Fatalf("read %d decoded sequence %d after %d", i, b.Sequence, last)
		}
		last = b.Sequence
	}
	wg.Wait()
	if decoded == 0 {
		t.Fatal("no read decoded, so the reader never saw the writer's records")
	}
	t.Logf("%d reads decoded, %d were torn", decoded, unreadable)
	final, err := feed.ReadBeacon(h.Beacon())
	if err != nil || final.Sequence != writes+1 {
		t.Fatalf("after the writer stopped the beacon reads (%+v, %v), want sequence %d", final, err, writes+1)
	}
}

// The retry is one and it is real: a torn read followed by a whole one is the
// whole one, and two torn reads are unreadable. The reads are a sequence the
// test hands over, because a torn read on a real file is not something a
// test can arrange to order.
func TestTheReaderRetriesOnceOnATornRead(t *testing.T) {
	t.Parallel()
	whole, err := supervisor.EncodeBeacon(supervisor.Beacon{
		Identity: supervisor.Identity{Instance: instance}, Sequence: 1, WrittenAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	torn := whole[:len(whole)/2]
	sequence := func(reads ...[]byte) func() ([]byte, error) {
		return func() ([]byte, error) {
			if len(reads) == 0 {
				t.Fatal("the reader read more than twice")
			}
			next := reads[0]
			reads = reads[1:]
			return next, nil
		}
	}
	if b, err := feed.ReadBeaconWith(sequence(torn, whole)); err != nil || b.Sequence != 1 {
		t.Fatalf("a torn read then a whole one answered (%+v, %v), want the whole record", b, err)
	}
	if _, err := feed.ReadBeaconWith(sequence(torn, torn)); !errors.Is(err, supervisor.ErrBeaconUnreadable) {
		t.Fatalf("two torn reads answered %v, want ErrBeaconUnreadable", err)
	}
	if _, err := feed.ReadBeaconWith(sequence(whole, torn)); err != nil {
		t.Fatalf("a whole read was read again: %v", err)
	}
	// A file that is not there is the operating system's error, not a torn
	// read, and it is still answered after two attempts rather than one.
	h := createdHome(t)
	if _, err := feed.ReadBeacon(h.Beacon()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a missing beacon answered %v, want the not-exist error", err)
	}
}

// A closed writer writes nothing and says so; a write that could not be
// encoded does not advance the sequence.
func TestTheWriterRefusesAfterCloseAndHoldsItsSequenceOnARefusedWrite(t *testing.T) {
	t.Parallel()
	h := createdHome(t)
	w, err := feed.OpenWriter(h.Beacon())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(supervisor.Identity{}, time.Now()); err == nil {
		t.Fatal("an empty instance was written")
	}
	b, err := w.Write(supervisor.Identity{Instance: instance}, time.Now())
	if err != nil || b.Sequence != 1 {
		t.Fatalf("the first write that encodes is (%+v, %v), want sequence 1", b, err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("a second close: %v", err)
	}
	if _, err := w.Write(supervisor.Identity{Instance: instance}, time.Now()); err == nil {
		t.Fatal("a closed writer wrote")
	}
}
