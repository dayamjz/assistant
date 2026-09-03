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
	WorkingPath string `json:"workingPath"`
	// Adopted reports that this binding was established by taking over a gate
	// that carried no record, so nothing but the working copy's own remote
	// ever said the gate was its, and a copy of a gated project inherits that
	// remote. It is the difference between a binding a record established and
	// one this package inferred, which is a difference no later state of the
	// gate can show, so it is carried into every record an initialization
	// reached through that remote writes afterwards.
	//
	// One thing does clear it: an initialization whose own path hashes to the
	// gate, which is the evidence that creates a gate in the first place and
	// evidence a copy cannot produce, because a copy stands elsewhere and
	// hashes elsewhere. That is the difference between a fact about the past
	// and a fact about now, and only the second one this field stands for.
	//
	// Removal refuses on it, because deleting a gate is the one act here that
	// cannot be undone and an inferred binding is not enough to justify it.
	Adopted bool `json:"adopted,omitempty"`
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
		return record{}, false, fmt.Errorf("%w: %s: %w", ErrMalformedRecord, recordPath(repo), err)
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return record{}, true, fmt.Errorf("%w: %s: %w", ErrMalformedRecord, recordPath(repo), err)
	}
	if rec.Version != recordVersion {
		return record{}, true, fmt.Errorf("%w: %s: version %d, this build writes version %d",
			ErrMalformedRecord, recordPath(repo), rec.Version, recordVersion)
	}
	if rec.ID == "" || rec.WorkingPath == "" {
		return record{}, true, fmt.Errorf("%w: %s: identifier or working path is empty",
			ErrMalformedRecord, recordPath(repo))
	}
	return rec, true, nil
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
