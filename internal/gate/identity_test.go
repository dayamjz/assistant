package gate_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dayamjz/assistant/internal/gate"
)

// TestIdentifyIsTheHashOfTheResolvedPath states the identifier rule outright
// rather than reading it back out of the package under test, which is the only
// way this test can fail for the defect it exists to catch.
func TestIdentifyIsTheHashOfTheResolvedPath(t *testing.T) {
	dir := t.TempDir()
	got, err := gate.Identify(dir)
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	sum := sha256.Sum256([]byte(resolved(t, dir)))
	if want := hex.EncodeToString(sum[:])[:16]; got != want {
		t.Fatalf("Identify(%q) = %q, want %q", dir, got, want)
	}
}

// TestIdentifyIsStableAndPathDerived is the pair of properties PRD section 8
// asks of the identifier: the same working path always gives the same one, and
// two working paths do not share one.
func TestIdentifyIsStableAndPathDerived(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()

	a, err := gate.Identify(first)
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	again, err := gate.Identify(first)
	if err != nil {
		t.Fatalf("Identify again: %v", err)
	}
	if a != again {
		t.Fatalf("Identify is not stable: %q then %q", a, again)
	}
	b, err := gate.Identify(second)
	if err != nil {
		t.Fatalf("Identify the second path: %v", err)
	}
	if a == b {
		t.Fatalf("two working paths share the identifier %q", a)
	}
}

// TestIdentifyAgreesAcrossASymbolicLink is why the identifier is computed from
// the resolved path. Two spellings of one directory must not be two gates.
func TestIdentifyAgreesAcrossASymbolicLink(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("this host does not allow symbolic links: %v", err)
	}

	direct, err := gate.Identify(real)
	if err != nil {
		t.Fatalf("Identify the directory: %v", err)
	}
	through, err := gate.Identify(link)
	if err != nil {
		t.Fatalf("Identify the link: %v", err)
	}
	if direct != through {
		t.Fatalf("the directory identifies as %q through its own name and %q through a link", direct, through)
	}
}

// TestIdentifyRefusesWhatItCannotResolve keeps the identifier a fact rather
// than a computation over a string nobody checked.
func TestIdentifyRefusesWhatItCannotResolve(t *testing.T) {
	if _, err := gate.Identify("relative/path"); !errors.Is(err, gate.ErrInvalidSpec) {
		t.Fatalf("Identify of a relative path = %v, want ErrInvalidSpec", err)
	}
	if _, err := gate.Identify(""); !errors.Is(err, gate.ErrInvalidSpec) {
		t.Fatalf("Identify of an empty path = %v, want ErrInvalidSpec", err)
	}
	missing := filepath.Join(t.TempDir(), "not-there")
	if _, err := gate.Identify(missing); err == nil {
		t.Fatal("Identify invented an identifier for a path that does not exist")
	}
}

// TestGateLivesWhereTheOnDiskLayoutSaysItDoes checks the one path convention
// other parts of this product will resolve for themselves.
func TestGateLivesWhereTheOnDiskLayoutSaysItDoes(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home := t.TempDir()
	command, _ := recorderCommand(t, 0)

	g, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	id, err := gate.Identify(wc.path)
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if g.ID() != id {
		t.Fatalf("the new gate reports identifier %q, want %q", g.ID(), id)
	}
	want := filepath.Join(resolved(t, home), "repos", id+".git")
	if g.Repository() != want {
		t.Fatalf("the gate is at %q, want %q", g.Repository(), want)
	}
	if g.WorkingPath() != resolved(t, wc.path) {
		t.Fatalf("the gate reports working path %q, want %q", g.WorkingPath(), resolved(t, wc.path))
	}
	if g.Reattached() {
		t.Fatal("a gate created from nothing reported a reattachment")
	}
}

// TestARecordFromAnotherVersionIsRefused keeps the binding a fact this build
// understands. Guessing at a record it cannot read is how a copy would end up
// with the original's gate.
func TestARecordFromAnotherVersionIsRefused(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home := t.TempDir()
	command, _ := recorderCommand(t, 0)
	spec := gate.Spec{Home: home, WorkingPath: wc.path, Command: command}

	g, err := gate.Initialize(ctx(t), spec)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	record := filepath.Join(g.Repository(), "assistant-gate.json")
	replaced, err := json.Marshal(map[string]any{"version": 99, "id": g.ID(), "workingPath": g.WorkingPath()})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	writeFile(t, record, string(replaced))

	// Moving the working copy is what makes initialization read the record of
	// a gate it does not already own.
	moved := filepath.Join(filepath.Dir(wc.path), "moved")
	if err := os.Rename(wc.path, moved); err != nil {
		t.Fatalf("move the working copy: %v", err)
	}
	_, err = gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: moved, Command: command})
	if !errors.Is(err, gate.ErrMalformedRecord) {
		t.Fatalf("Initialize error = %v, want ErrMalformedRecord", err)
	}
}

// TestAHomeThatDoesNotExistYetIsSpelledTheSameOnceItDoes is why the home is
// resolved as far as it exists rather than only when all of it does. Spec.Home
// is allowed not to exist yet, and the first initialization is the one that
// creates it, so a home under a symbolically linked prefix would otherwise be
// written into the working copy's remote under one spelling and looked for
// under another from the second call onwards. Removal is what meets that
// first: it refuses a repository outside the home's repository directory, and
// two spellings of one directory are not the same string.
func TestAHomeThatDoesNotExistYetIsSpelledTheSameOnceItDoes(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	command, _ := recorderCommand(t, 0)

	root := t.TempDir()
	actual := filepath.Join(root, "actual")
	if err := os.Mkdir(actual, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", actual, err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(actual, link); err != nil {
		t.Skipf("this host does not allow symbolic links: %v", err)
	}
	// The home itself is what does not exist yet, under a prefix that resolves
	// somewhere else.
	spec := gate.Spec{Home: filepath.Join(link, "assistant"), WorkingPath: wc.path, Command: command}

	first, err := gate.Initialize(ctx(t), spec)
	if err != nil {
		t.Fatalf("Initialize into a home that does not exist yet: %v", err)
	}
	second, err := gate.Initialize(ctx(t), spec)
	if err != nil {
		t.Fatalf("Initialize again once the home exists: %v", err)
	}
	if second.Repository() != first.Repository() {
		t.Fatalf("the gate is at %q on the second call and was at %q on the first", second.Repository(), first.Repository())
	}
	if url, ok := remoteURL(t, wc.path, gate.RemoteName); !ok || url != first.Repository() {
		t.Fatalf("the %s remote is %q, want %q", gate.RemoteName, url, first.Repository())
	}
	if err := gate.Remove(ctx(t), spec, gate.WithOpener(detachingOpener)); err != nil {
		t.Fatalf("Remove a gate this package created in a home it created: %v", err)
	}
}
