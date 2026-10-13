package home

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// instanceTempName is the os.CreateTemp pattern a write builds the file
// under, and instanceTempPattern is the glob that finds every file so named,
// which is how a write sweeps what a crashed predecessor left.
const (
	instanceTempName    = "instance-*.tmp"
	instanceTempPattern = "instance-*.tmp"
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
// any process holds the home. A caller that cannot write it still refuses to
// start, which is the strict stance: an instance file naming some earlier
// process beside a lock this one holds is the reading a liveness check must
// never be handed, and the retry bound of about two seconds is what a start
// may spend on it.
//
// Temporary files an earlier write left behind, by a crash between making one
// and renaming it, are removed first. That sweep rests on the lock: the one
// process that may write this file is the one holding the home, so a
// temporary file not this write's is nobody's. A crash in the same window of
// this write leaves one until the next write; nothing reads it as the
// identity, since only the renamed file is.
func (h *Home) WriteInstance(id string) error {
	if id == "" || strings.ContainsAny(id, "\r\n") {
		return fmt.Errorf("%w: %q", ErrInstanceID, id)
	}
	if stale, err := filepath.Glob(filepath.Join(h.root, instanceTempPattern)); err == nil {
		for _, path := range stale {
			_ = os.Remove(path)
		}
	}
	tmp, err := os.CreateTemp(h.root, instanceTempName)
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
