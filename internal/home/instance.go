package home

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// ErrInstanceID reports an instance identity WriteInstance refuses to write:
// an empty one, or one that is not a single line, because the file is read as
// one line and a second line would be read as a second identity.
var ErrInstanceID = errors.New("home: invalid instance identity")

// instanceRenameAttempts bounds how many times WriteInstance retries the
// rename that publishes the file, and instanceRenamePoll is the pause between
// attempts. Together they bound a write at about two seconds, which outlasts
// any one read of a file this small by a wide margin and keeps a service start
// from hanging on a reader that never lets go.
const (
	instanceRenameAttempts = 100
	instanceRenamePoll     = 20 * time.Millisecond
)

// WriteInstance writes the service's instance identity to InstanceFile, which
// PRD section 8 has the service write once at start, after it holds the lock.
// It is a separate file from the lock because on Windows the locked byte is
// unreadable to any other process for as long as it is locked, and the file
// exists so that another process can read it.
//
// The write is a temporary file in the root renamed over the destination, so
// a reader sees either the previous identity or the new one and never a
// partial line. A platform may refuse a rename over a file another process
// holds open, which Windows does for a file opened without delete sharing, so
// the rename is retried a bounded number of times before the failure is
// reported; a reader that holds the file open for longer than that bound
// defeats the write, and the error says so rather than the identity being
// published somewhere else.
//
// The file is not state. It says which process wrote it and nothing about
// whether that process is still serving: PRD section 8 makes publishing an
// identifier no proof of readiness, and the lock probe is what answers whether
// any process holds the home.
func (h *Home) WriteInstance(id string) error {
	if id == "" || strings.ContainsAny(id, "\r\n") {
		return fmt.Errorf("%w: %q", ErrInstanceID, id)
	}
	tmp, err := os.CreateTemp(h.root, "instance-*.tmp")
	if err != nil {
		return fmt.Errorf("home: writing the instance file: %w", err)
	}
	name := tmp.Name()
	if _, err := tmp.WriteString(id + "\n"); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("home: writing the instance file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("home: writing the instance file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("home: writing the instance file: %w", err)
	}
	for attempt := 1; ; attempt++ {
		err = os.Rename(name, h.InstanceFile())
		if err == nil {
			return nil
		}
		if attempt == instanceRenameAttempts {
			break
		}
		time.Sleep(instanceRenamePoll)
	}
	_ = os.Remove(name)
	return fmt.Errorf("home: publishing the instance file after %d attempts: %w", instanceRenameAttempts, err)
}
