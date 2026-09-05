package fixture

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ManifestName is the file WriteManifest writes, relative to the build root.
// It is what a harness in another process reads: the scenarios on disk, the
// catalog of what is planted in them, and the questions this package declined
// to answer.
const ManifestName = "manifest.json"

// WriteManifest renders the fixture as JSON at ManifestName under its root and
// returns the path it wrote.
//
// The manifest is a report of a build rather than an input to one. Everything
// in it is derived from the build that produced it, so a harness that reads a
// manifest whose scenarios have been removed is reading about a fixture that
// is not there; ReadManifest checks the version and nothing else, because
// nothing else can be checked from the file alone.
func (f *Fixture) WriteManifest() (string, error) {
	path := filepath.Join(f.Root, ManifestName)
	body, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return "", fmt.Errorf("fixture: rendering the manifest: %w", err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o644); err != nil {
		return "", fmt.Errorf("fixture: writing %s: %w", path, err)
	}
	return path, nil
}

// ReadManifest reads a manifest written by WriteManifest. It refuses a
// manifest of another version rather than decoding what it can, because a
// harness acting on a partially understood catalog would report conditions it
// did not check.
func ReadManifest(path string) (*Fixture, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("fixture: reading %s: %w", path, err)
	}
	var f Fixture
	if err := json.Unmarshal(body, &f); err != nil {
		return nil, fmt.Errorf("fixture: %s is not a manifest this package wrote: %w", path, err)
	}
	if f.Version != manifestVersion {
		return nil, fmt.Errorf("fixture: %s is manifest version %d and this package writes version %d",
			path, f.Version, manifestVersion)
	}
	return &f, nil
}

// Scenario returns the scenario with this name, and reports whether there is
// one.
func (f *Fixture) Scenario(name ScenarioName) (Scenario, bool) {
	for _, s := range f.Scenarios {
		if s.Name == name {
			return s, true
		}
	}
	return Scenario{}, false
}

// Condition returns the condition with this identifier, and reports whether
// there is one.
func (f *Fixture) Condition(id ID) (Condition, bool) {
	for _, c := range f.Conditions {
		if c.ID == id {
			return c, true
		}
	}
	return Condition{}, false
}

// Fired returns the tripwire identifiers a scenario recorded, in the order
// they were recorded, and reports whether the tripwire file exists at all. A
// scenario in which nothing planted was executed has no tripwire file, so the
// absence is the expected answer and is reported as one rather than as a
// missing-file error.
func (s Scenario) Fired() ([]string, bool, error) {
	body, err := os.ReadFile(s.Tripwire)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("fixture: reading the tripwire file %s: %w", s.Tripwire, err)
	}
	var fired []string
	for _, line := range strings.Split(string(body), "\n") {
		if line != "" {
			fired = append(fired, line)
		}
	}
	return fired, true, nil
}
