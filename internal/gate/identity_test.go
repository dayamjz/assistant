package gate_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	home, opts := newHome(t)
	command, _ := recorderCommand(t, 0)

	g, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command}, opts()...)
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
	home, opts := newHome(t)
	command, _ := recorderCommand(t, 0)
	spec := gate.Spec{Home: home, WorkingPath: wc.path, Command: command}

	g, err := gate.Initialize(ctx(t), spec, opts()...)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	record := filepath.Join(g.Repository(), recordName)
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
	_, err = gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: moved, Command: command}, opts()...)
	if !errors.Is(err, gate.ErrMalformedRecord) {
		t.Fatalf("Initialize error = %v, want ErrMalformedRecord", err)
	}
}

// TestARecordThisBuildCannotReadNamesAWayOut is the other half of that
// refusal. A record this build will not read shuts both operations, and
// detaching does not open either, because the gate is also found by the
// working copy's own path hash with no remote involved. So the step the
// refusal names is the only one there is, and a refusal that named nothing
// would leave deleting the gate as the way out, which is the loss every guard
// here exists to prevent.
func TestARecordThisBuildCannotReadNamesAWayOut(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home, opts := newHome(t)
	command, _ := recorderCommand(t, 0)
	spec := gate.Spec{Home: home, WorkingPath: wc.path, Command: command}

	g, err := gate.Initialize(ctx(t), spec, opts()...)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "main")

	record := filepath.Join(g.Repository(), recordName)
	replaced, err := json.Marshal(map[string]any{"version": 99, "id": g.ID(), "workingPath": g.WorkingPath()})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	writeFile(t, record, string(replaced))

	// Both operations are shut, which is what makes the named step the only
	// one, and detaching does not open either.
	if _, err := gate.Initialize(ctx(t), spec, opts()...); !errors.Is(err, gate.ErrMalformedRecord) {
		t.Fatalf("Initialize error = %v, want ErrMalformedRecord", err)
	}
	refused := gate.Remove(ctx(t), spec, opts(gate.WithOpener(detachingOpener))...)
	if !errors.Is(refused, gate.ErrMalformedRecord) {
		t.Fatalf("Remove error = %v, want ErrMalformedRecord", refused)
	}
	if !namesRemoving(refused, record) {
		t.Fatalf("the refusal does not name removing %s, which is the only step that gets anywhere from here: %v",
			record, refused)
	}

	// The step it names completes, and the gate keeps everything it holds.
	if err := os.Remove(record); err != nil {
		t.Fatalf("remove the record: %v", err)
	}
	repaired, err := gate.Initialize(ctx(t), spec, opts()...)
	if err != nil {
		t.Fatalf("the step the refusal names did not complete: %v", err)
	}
	if repaired.Repository() != g.Repository() {
		t.Fatalf("the repair moved the gate to %q, want %q", repaired.Repository(), g.Repository())
	}
	if got, want := refs(t, g.Repository()), []string{"refs/heads/main " + wc.commit}; !equal(got, want) {
		t.Fatalf("the repaired gate holds %v, want its history %v", got, want)
	}
	if err := gate.Remove(ctx(t), spec, opts(gate.WithOpener(detachingOpener))...); err != nil {
		t.Fatalf("Remove after the repair the refusal named: %v", err)
	}
}

// TestARecordFiledUnderTheWrongIdentifierNamesAWayOut is the second producer of
// that refusal, and it is here because it did not always name a way out.
//
// A record whose identifier is not the one the gate is filed under is a record
// this build will not act on: the identifier is what the run history is
// recorded against, and a gate that disagrees with itself about which one it is
// cannot be adopted or deleted on. What it owes the reader is the same step the
// unreadable record owes, and every producer now goes through one constructor
// that attaches it rather than each remembering to.
func TestARecordFiledUnderTheWrongIdentifierNamesAWayOut(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home, opts := newHome(t)
	command, _ := recorderCommand(t, 0)
	spec := gate.Spec{Home: home, WorkingPath: wc.path, Command: command}

	g, err := gate.Initialize(ctx(t), spec, opts()...)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "main")

	// A record this build reads in full, whose identifier is not the one the
	// directory it sits in is named for.
	record := filepath.Join(g.Repository(), recordName)
	writeFile(t, record, fmt.Sprintf(`{"version":1,"id":%q,"workingPath":%q}`, "0f0f0f0f0f0f0f0f", g.WorkingPath()))

	refusedInit := func() error {
		_, err := gate.Initialize(ctx(t), spec, opts()...)
		return err
	}()
	refusedRemove := gate.Remove(ctx(t), spec, opts(gate.WithOpener(detachingOpener))...)
	for name, refused := range map[string]error{"Initialize": refusedInit, "Remove": refusedRemove} {
		if !errors.Is(refused, gate.ErrMalformedRecord) {
			t.Fatalf("%s error = %v, want ErrMalformedRecord", name, refused)
		}
		if !namesRemoving(refused, record) {
			t.Fatalf("the %s refusal does not name removing %s, which is the only step that gets anywhere "+
				"from here: %v", name, record, refused)
		}
	}

	// The step both name completes, and the gate keeps everything it holds.
	if err := os.Remove(record); err != nil {
		t.Fatalf("remove the record: %v", err)
	}
	repaired, err := gate.Initialize(ctx(t), spec, opts()...)
	if err != nil {
		t.Fatalf("the step the refusals name did not complete: %v", err)
	}
	if repaired.Repository() != g.Repository() {
		t.Fatalf("the repair moved the gate to %q, want %q", repaired.Repository(), g.Repository())
	}
	if got, want := refs(t, g.Repository()), []string{"refs/heads/main " + wc.commit}; !equal(got, want) {
		t.Fatalf("the repaired gate holds %v, want its history %v", got, want)
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

	opts := indexOptions(t)
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

	first, err := gate.Initialize(ctx(t), spec, opts()...)
	if err != nil {
		t.Fatalf("Initialize into a home that does not exist yet: %v", err)
	}
	second, err := gate.Initialize(ctx(t), spec, opts()...)
	if err != nil {
		t.Fatalf("Initialize again once the home exists: %v", err)
	}
	if second.Repository() != first.Repository() {
		t.Fatalf("the gate is at %q on the second call and was at %q on the first", second.Repository(), first.Repository())
	}
	if url, ok := remoteURL(t, wc.path, gate.RemoteName); !ok || url != first.Repository() {
		t.Fatalf("the %s remote is %q, want %q", gate.RemoteName, url, first.Repository())
	}
	if err := gate.Remove(ctx(t), spec, opts(gate.WithOpener(detachingOpener))...); err != nil {
		t.Fatalf("Remove a gate this package created in a home it created: %v", err)
	}
}
