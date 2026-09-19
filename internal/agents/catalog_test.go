package agents_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
)

// The catalog option is only worth having if it reaches every adapter the
// default catalog ships, so this resolves each one by name from a catalog
// built with a recorder, runs one invocation against a stand-in on the PATH,
// and requires the recorder to have received that adapter's record. An
// adapter the option skipped would run and record nothing, which is exactly
// the silent gap the option exists to close; the stand-in answers with an
// empty output no adapter can read, so the record each delivery proves is
// also one only the recorder could have carried, a result having none.
func TestDefaultCatalogHandsEveryAdapterTheRecorder(t *testing.T) {
	names := agents.DefaultCatalog().Names()
	if len(names) == 0 {
		t.Fatal("the default catalog ships no adapters")
	}
	dir := t.TempDir()
	for _, name := range names {
		installStandinAs(t, dir, name)
	}
	t.Setenv("PATH", dir)

	rec := &recorder{}
	catalog := agents.DefaultCatalog(agents.WithCatalogRecorder(rec))
	if got := catalog.Names(); !slices.Equal(got, names) {
		t.Fatalf("the catalog with a recorder ships %v, want the same adapters %v", got, names)
	}

	for _, name := range names {
		before := len(rec.records)
		resolution, err := agents.Resolve(t.Context(),
			[]string{name + " " + helperModeFlag + "raw"}, catalog)
		if err != nil {
			t.Fatalf("resolving %s over the stand-in: %v", name, err)
		}
		_, err = resolution.Runner.Run(t.Context(), agents.PurposeTest,
			invocation(t, agents.ShapeText, nil))
		var refusal *agents.InvocationError
		if err != nil && !errors.As(err, &refusal) {
			t.Fatalf("running %s over the stand-in: %v", name, err)
		}
		if len(rec.records) != before+1 {
			t.Fatalf("after invoking %s the recorder holds %d records, want %d: the catalog did not hand it the recorder",
				name, len(rec.records), before+1)
		}
		if got := rec.records[before].Agent; got != name {
			t.Errorf("the record names agent %q, want %q", got, name)
		}
	}
}

// installStandinAs copies this test binary into dir under an adapter's
// binary name, so a PATH lookup of that name finds the stand-in agent. It is
// a copy rather than a link because the point is what an adapter resolves
// and executes, on every platform the suite runs on.
func installStandinAs(t *testing.T, dir, name string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locating the test binary: %v", err)
	}
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	src, err := os.Open(self)
	if err != nil {
		t.Fatalf("opening the test binary: %v", err)
	}
	defer src.Close()
	dst, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatalf("installing the stand-in as %s: %v", name, err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatalf("installing the stand-in as %s: %v", name, err)
	}
	if err := dst.Close(); err != nil {
		t.Fatalf("installing the stand-in as %s: %v", name, err)
	}
}
