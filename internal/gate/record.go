package gate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// recordName is the file inside a gate repository that records which working
// copy the gate belongs to. It sits in the repository directory rather than
// beside it so that the binding travels with the repository and disappears
// with it.
const recordName = "assistant-gate.json"

// recordVersion is the shape of the record this build writes. A record from a
// future version is refused rather than half-understood: the binding it
// carries is what decides whether a gate is reattached or left alone, and
// guessing at it is how a copy steals the original's gate.
//
// A build before this one wrote one more field, recording that a binding had
// been established over a gate that carried no record. That question is now
// asked of the ownership index instead of remembered, so the field is ignored
// where it is still on disk. Ignoring a field this build does not read is not a
// change of shape, and bumping the version over it would make every gate a
// previous build wrote unreadable, which is a worse answer than reading them.
const recordVersion = 1

// record is a gate's own account of what it is bound to. Per PRD principle
// P14 it is the owner of that binding: the identifier in a gate's directory
// name is where the identifier came from, and this is what it means now.
type record struct {
	// Version is recordVersion.
	Version int `json:"version"`
	// ID is the gate's identifier, which is also its directory name without
	// the .git suffix. It is written so that a repository found on disk can
	// be checked against the name it is filed under.
	ID string `json:"id"`
	// WorkingPath is the resolved absolute path of the working copy this gate
	// belongs to. It is rewritten whenever initialization reattaches the gate
	// to a working copy that moved.
	//
	// It is the gate's own account of its working copy rather than the owner of
	// the ownership question. What it buys is that a gate found on disk names a
	// working copy when the home's database has been lost or replaced, which is
	// exactly what the ownership index cannot do. Neither is believed alone;
	// see held.claimants.
	WorkingPath string `json:"workingPath"`
}

// recordPath is where a gate repository keeps its record.
func recordPath(repo string) string {
	return filepath.Join(repo, recordName)
}

// readRecord reads a gate's record. It reports whether the record exists
// separately from whether it could be read, because a repository with no
// record yet is an ordinary state during repair while a record that will not
// parse is a refusal.
func readRecord(repo string) (rec record, exists bool, err error) {
	data, err := os.ReadFile(recordPath(repo))
	if err != nil {
		if os.IsNotExist(err) {
			return record{}, false, nil
		}
		return record{}, false, malformedRecord(repo, "%s: %w", recordPath(repo), err)
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return record{}, true, malformedRecord(repo, "%s: %w", recordPath(repo), err)
	}
	if rec.Version != recordVersion {
		return record{}, true, malformedRecord(repo, "%s: version %d, this build writes version %d",
			recordPath(repo), rec.Version, recordVersion)
	}
	if rec.ID == "" || rec.WorkingPath == "" {
		return record{}, true, malformedRecord(repo, "%s: identifier or working path is empty", recordPath(repo))
	}
	return rec, true, nil
}

// malformedRecord builds every ErrMalformedRecord this package returns.
//
// It exists so that the step that gets an operator out of one is attached by
// the constructor rather than by each producer remembering to attach it. Two
// producers did not, while errors.go and doc.go both promised that every one of
// them named the step that succeeds, and a message with no action is a dead end
// an operator resolves by deleting the gate by hand.
func malformedRecord(repo, format string, args ...any) error {
	// The detail is built with fmt.Errorf rather than fmt.Sprintf so that a
	// producer reporting an underlying failure can wrap it with %w and have the
	// chain survive being wrapped again here.
	return fmt.Errorf("%w: %w%s", ErrMalformedRecord, fmt.Errorf(format, args...), recordRepair(repo))
}

// recordRepair is the step that gets an operator out of a record this build
// cannot read, and malformedRecord appends it to every refusal that reports
// one.
//
// Both operations refuse on such a record and detaching does not help, because
// the gate is also found by the working copy's own path hash, with no remote
// involved. So the only step that succeeds is removing the file, and a refusal
// that did not name it is one an operator resolves by deleting the gate, which
// is the loss every guard here exists to prevent.
func recordRepair(repo string) string {
	return fmt.Sprintf("; nothing but the binding is in that file, so removing %s leaves the gate and everything "+
		"it holds, and initializing the working copy it belongs to then writes a record this build can read",
		recordPath(repo))
}

// writeRecord replaces a gate's record. The replacement is a rename over a
// file written alongside it, so a reader either sees the record as it was or
// the record as it now is, never half of either.
func writeRecord(repo string, rec record) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("gate: encoding record for %s: %w", repo, err)
	}
	return replaceFile(recordPath(repo), append(data, '\n'), 0o644)
}

// replaceFile writes content to path through a temporary file in the same
// directory and renames it into place.
func replaceFile(path string, content []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("gate: creating a temporary file in %s: %w", dir, err)
	}
	name := tmp.Name()
	// The rename below is what makes this succeed, so the temporary file is
	// removed on every path that does not reach it. Its own removal failing
	// leaves a stray file and nothing worse, so it does not become the error
	// this function reports.
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("gate: writing %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("gate: closing %s: %w", name, err)
	}
	if err := os.Chmod(name, mode); err != nil {
		return fmt.Errorf("gate: setting the mode of %s: %w", name, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("gate: renaming %s into place at %s: %w", name, path, err)
	}
	return nil
}
