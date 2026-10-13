package feed

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/dayamjz/assistant/internal/supervisor"
)

// Writer rewrites one beacon file in place. It keeps the file open for its
// lifetime, so every write is one WriteAt of the whole record at offset zero
// and the file is never truncated, renamed or recreated between writes. It
// is safe for concurrent use, though its one intended caller is a loop.
type Writer struct {
	mu       sync.Mutex
	file     *os.File
	sequence uint64
}

// OpenWriter opens the beacon at path for writing, creating it when it is not
// there and leaving whatever record it holds until the first Write. The
// sequence starts at zero, so the first Write is sequence one; a writer
// opened over an earlier writer's file restarts the sequence, which is why
// the epoch travels beside it.
func OpenWriter(path string) (*Writer, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("feed: opening the beacon for writing: %w", err)
	}
	return &Writer{file: file}, nil
}

// Write records that the loop with identity id was live at now, as the next
// sequence, and returns the beacon as written. The sequence advances only on
// a write that was handed to the file whole; a record that could not be
// encoded or written leaves it where it was.
func (w *Writer) Write(id supervisor.Identity, now time.Time) (supervisor.Beacon, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return supervisor.Beacon{}, errors.New("feed: the beacon writer is closed")
	}
	b := supervisor.Beacon{Identity: id, Sequence: w.sequence + 1, WrittenAt: now.UTC()}
	raw, err := supervisor.EncodeBeacon(b)
	if err != nil {
		return supervisor.Beacon{}, err
	}
	if _, err := w.file.WriteAt(raw, 0); err != nil {
		return supervisor.Beacon{}, fmt.Errorf("feed: writing the beacon: %w", err)
	}
	w.sequence = b.Sequence
	return b, nil
}

// Close releases the file. A second call reports nothing and does nothing.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	file := w.file
	w.file = nil
	if err := file.Close(); err != nil {
		return fmt.Errorf("feed: closing the beacon: %w", err)
	}
	return nil
}

// ReadBeacon reads the beacon at path. A read that does not decode is read
// once more, because the writer rewrites in place and a read can straddle two
// writes; a second read that does not decode either is answered as the error
// it failed with, which wraps supervisor.ErrBeaconUnreadable for a record of
// the wrong shape and the operating system's error for a file that could not
// be read at all. A caller feeding supervisor.Assess passes the zero Beacon
// on any error, which that function blocks on.
func ReadBeacon(path string) (supervisor.Beacon, error) {
	return readBeacon(func() ([]byte, error) { return os.ReadFile(path) })
}

// readBeacon is ReadBeacon over any read, which is what lets a test hand it a
// torn read followed by a whole one and show the retry rather than assert it.
func readBeacon(read func() ([]byte, error)) (supervisor.Beacon, error) {
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		raw, err := read()
		if err != nil {
			last = fmt.Errorf("feed: reading the beacon: %w", err)
			continue
		}
		b, err := supervisor.DecodeBeacon(raw)
		if err == nil {
			return b, nil
		}
		last = err
	}
	return supervisor.Beacon{}, last
}
