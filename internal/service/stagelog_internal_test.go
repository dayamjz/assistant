package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readTail bounds what one read of a log returns: a file longer than the
// bound comes back as its end, starting after the first line break inside
// the bound so no read opens on half a line, with the bytes before it counted
// as omitted, including that half line. A file within the bound comes back
// whole with nothing omitted, and a file that is not there is reported as not
// there rather than as an empty log.
func TestReadTailReturnsTheEndOfALogFromALineBoundary(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "review.log")
	var lines []string
	for i := 0; i < 50; i++ {
		lines = append(lines, strings.Repeat("x", 9)) // ten bytes with its newline
	}
	whole := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(whole), 0o600); err != nil {
		t.Fatal(err)
	}

	tail, size, omitted, err := readTail(path, 25)
	if err != nil {
		t.Fatalf("readTail: %v", err)
	}
	if size != int64(len(whole)) {
		t.Errorf("size %d, want %d", size, len(whole))
	}
	if tail != "xxxxxxxxx\nxxxxxxxxx\n" || omitted != int64(len(whole))-20 {
		t.Errorf("the tail is %q with %d omitted, want the last two whole lines and %d omitted", tail, omitted, len(whole)-20)
	}
	if int64(len(tail))+omitted != size {
		t.Errorf("the tail and what it omits come to %d bytes, the file holds %d", int64(len(tail))+omitted, size)
	}

	tail, _, omitted, err = readTail(path, 1<<20)
	if err != nil || tail != whole || omitted != 0 {
		t.Errorf("a file within the bound read back as %d bytes with %d omitted (%v), want it whole", len(tail), omitted, err)
	}

	if _, _, _, err := readTail(filepath.Join(t.TempDir(), "absent.log"), 25); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a log that is not there read as %v, want it reported as not existing", err)
	}
}
