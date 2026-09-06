package home

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DefaultLogBytes is the bound on the service log when a caller names none. It
// is large enough to hold a long run of lifecycle lines and small enough that
// a home never grows without limit on its own.
const DefaultLogBytes = 1 << 20

// Log is the bounded lifecycle log PRD section 8 puts at logs/service.log.
//
// Rotation truncates in place: when the file passes its bound, the most recent
// half is moved to the front and the file is truncated to it, through the same
// descriptor. A process or a child holding that descriptor therefore keeps
// writing to the bounded file rather than to an orphaned inode, which is the
// property the PRD names and the reason this is not a rename-and-reopen.
//
// What that costs is the oldest half of the log at every rotation, which is
// what a bounded log costs by definition. What it buys is that the bound is
// real: no second file grows in place of the first.
//
// A Log is safe for concurrent use.
type Log struct {
	mu    sync.Mutex
	file  *os.File
	limit int64
	size  int64
}

// OpenLog opens the home's service log, creating it and its directory if they
// are not there, and appending to whatever is already in it.
//
// limit bounds the file in bytes; zero means DefaultLogBytes. A limit below
// two bytes is raised to DefaultLogBytes, because a bound that cannot hold the
// half a rotation keeps is not a bound this can honour.
func (h *Home) OpenLog(limit int64) (*Log, error) {
	if limit < 2 {
		limit = DefaultLogBytes
	}
	if err := os.MkdirAll(filepath.Dir(h.ServiceLog()), 0o700); err != nil {
		return nil, fmt.Errorf("home: creating the log directory: %w", err)
	}
	file, err := os.OpenFile(h.ServiceLog(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("home: opening %s: %w", h.ServiceLog(), err)
	}
	size, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("home: opening %s: %w", h.ServiceLog(), err)
	}
	return &Log{file: file, limit: limit, size: size}, nil
}

// Printf writes one timestamped line. A failure to write is not returned: a
// lifecycle log that cannot be written is not a reason to stop serving, and a
// caller that could act on the failure would have nowhere better to report it.
func (l *Log) Printf(format string, args ...any) {
	line := time.Now().UTC().Format(time.RFC3339) + " " + fmt.Sprintf(format, args...) + "\n"
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return
	}
	n, err := l.file.WriteAt([]byte(line), l.size)
	if err != nil {
		return
	}
	l.size += int64(n)
	if l.size > l.limit {
		l.rotate()
	}
}

// rotate keeps the most recent half of the bound and truncates the file to it,
// through the descriptor already open. It is called with the lock held.
//
// A rotation that cannot be completed leaves the file as it was and the size
// unchanged, so the next write appends rather than overwriting what is there.
func (l *Log) rotate() {
	keep := l.limit / 2
	if keep <= 0 || l.size <= keep {
		return
	}
	tail := make([]byte, keep)
	if _, err := l.file.ReadAt(tail, l.size-keep); err != nil {
		return
	}
	// Start at the first whole line in the tail, so a rotation never leaves a
	// half line at the top of the file for a reader to misparse.
	start := 0
	for i, b := range tail {
		if b == '\n' {
			start = i + 1
			break
		}
	}
	tail = tail[start:]
	if _, err := l.file.WriteAt(tail, 0); err != nil {
		return
	}
	if err := l.file.Truncate(int64(len(tail))); err != nil {
		return
	}
	l.size = int64(len(tail))
}

// Close closes the log. Writes after it are dropped rather than failing, on
// the same terms as a write that could not reach the disk.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	return file.Close()
}
