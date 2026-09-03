package gate_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/gate"
)

// TestAMovedWorkingCopyKeepsItsGate is the reattachment PRD section 5
// requires. The gate keeps its identifier, and with it everything recorded
// against that identifier, rather than being replaced by a second one at the
// hash of the new path.
func TestAMovedWorkingCopyKeepsItsGate(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home := t.TempDir()
	command, _ := recorderCommand(t, 0)

	first, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "main")

	moved := filepath.Join(filepath.Dir(wc.path), "moved")
	if err := os.Rename(wc.path, moved); err != nil {
		t.Fatalf("move the working copy: %v", err)
	}

	second, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: moved, Command: command})
	if err != nil {
		t.Fatalf("Initialize after the move: %v", err)
	}
	if !second.Reattached() {
		t.Fatal("a moved working copy was not reported as reattached")
	}
	if second.ID() != first.ID() {
		t.Fatalf("the moved working copy got identifier %q, want the original %q", second.ID(), first.ID())
	}
	if second.Repository() != first.Repository() {
		t.Fatalf("the moved working copy got gate %q, want the original %q", second.Repository(), first.Repository())
	}
	if got, want := refs(t, second.Repository()), []string{"refs/heads/main " + wc.commit}; !equal(got, want) {
		t.Fatalf("the reattached gate lost its history: %v, want %v", got, want)
	}
	if second.WorkingPath() != resolved(t, moved) {
		t.Fatalf("the reattached gate reports working path %q, want %q", second.WorkingPath(), resolved(t, moved))
	}
	if url, ok := remoteURL(t, moved, gate.RemoteName); !ok || url != first.Repository() {
		t.Fatalf("the moved working copy's %s remote is %q, want %q", gate.RemoteName, url, first.Repository())
	}

	// Reattachment is not a one-off: the moved working copy is now an
	// ordinary one, and initializing it again neither moves it nor reports a
	// second reattachment.
	third, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: moved, Command: command})
	if err != nil {
		t.Fatalf("Initialize the moved working copy again: %v", err)
	}
	if third.Reattached() {
		t.Fatal("initializing an already reattached gate reported another reattachment")
	}
	if third.Repository() != first.Repository() {
		t.Fatalf("the third initialization moved the gate to %q", third.Repository())
	}
}

// TestACopiedWorkingCopyDoesNotStealTheOriginalsGate is the other half of the
// same question. The copy carries the original's remote in its configuration,
// so the only thing that keeps it from sharing the gate is the check that the
// original is still there and still bound to it.
func TestACopiedWorkingCopyDoesNotStealTheOriginalsGate(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home := t.TempDir()
	command, _ := recorderCommand(t, 0)

	original, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "main")

	duplicate := filepath.Join(filepath.Dir(wc.path), "copy")
	copyTree(t, wc.path, duplicate)
	if url, ok := remoteURL(t, duplicate, gate.RemoteName); !ok || url != original.Repository() {
		t.Fatalf("the copy's %s remote is %q, want the original's gate %q; the fixture does not pose the question this test asks",
			gate.RemoteName, url, original.Repository())
	}

	copied, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: duplicate, Command: command})
	if err != nil {
		t.Fatalf("Initialize the copy: %v", err)
	}
	if copied.Reattached() {
		t.Fatal("the copy reported a reattachment, so it took over the original's gate")
	}
	if copied.Repository() == original.Repository() {
		t.Fatalf("the copy shares the original's gate at %q", copied.Repository())
	}
	wantID, err := gate.Identify(duplicate)
	if err != nil {
		t.Fatalf("Identify the copy: %v", err)
	}
	if copied.ID() != wantID {
		t.Fatalf("the copy's identifier is %q, want %q", copied.ID(), wantID)
	}
	if got := refs(t, copied.Repository()); len(got) != 0 {
		t.Fatalf("the copy's own gate started with references in it: %v", got)
	}

	// The original is untouched in both directions.
	if got, want := refs(t, original.Repository()), []string{"refs/heads/main " + wc.commit}; !equal(got, want) {
		t.Fatalf("the original's gate changed: %v, want %v", got, want)
	}
	if url, ok := remoteURL(t, wc.path, gate.RemoteName); !ok || url != original.Repository() {
		t.Fatalf("the original's %s remote is now %q, want %q", gate.RemoteName, url, original.Repository())
	}
}

// TestARepositoryBornCarryingHooksIsRefused covers the route internal/vcs
// documents that it cannot close: a git template chosen by a configuration
// file this process is pointed at. A hook that arrives that way is code
// something outside this process chose to run inside the gate, so it is
// refused rather than preserved as somebody's custom hook.
func TestARepositoryBornCarryingHooksIsRefused(t *testing.T) {
	cfg := gitEnvironment(t)
	wc := newWorkingCopy(t)
	home := t.TempDir()
	command, _ := recorderCommand(t, 0)

	template := t.TempDir()
	writeScript(t, filepath.Join(template, "hooks", gate.AdmissionHook), "#!/bin/sh\nexit 0\n")
	// Git reads a backslash in a configuration value as an escape, so a
	// native Windows path written here would make every later git invocation
	// fail to parse this file instead of choosing the template.
	appendConfig(t, cfg, "[init]\n\ttemplateDir = "+filepath.ToSlash(template)+"\n")

	_, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command})
	if !errors.Is(err, gate.ErrTemplateHooks) {
		t.Fatalf("Initialize error = %v, want ErrTemplateHooks", err)
	}

	id, err := gate.Identify(wc.path)
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	repo := filepath.Join(resolved(t, home), "repos", id+".git")
	if _, err := os.Stat(repo); !os.IsNotExist(err) {
		// Leaving it would be worse than the refusal: the next attempt would
		// find the template's hook in a repository that already existed and
		// preserve it as somebody's own.
		t.Fatalf("the refused initialization left %s behind (stat error %v)", repo, err)
	}
	if url, ok := remoteURL(t, wc.path, gate.RemoteName); ok {
		t.Fatalf("the refused initialization pointed the %s remote at %q", gate.RemoteName, url)
	}
}

// TestAWorkingCopyPlacedWhereAMovedOneStoodDoesNotTakeItsGate is the other
// direction of the same claim. A gate's identifier is the hash of a path, and
// a path outlives the working copy that stood on it, so a new working copy put
// where a moved one used to be asks for the gate the moved one is still using.
// Answering that with the gate itself would hand over its references and
// rewrite the binding that everything recorded against it rests on, which is
// the loss PRD principle P6 forbids.
func TestAWorkingCopyPlacedWhereAMovedOneStoodDoesNotTakeItsGate(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home := t.TempDir()
	command, _ := recorderCommand(t, 0)

	original, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "main")

	moved := workingCopy{path: filepath.Join(filepath.Dir(wc.path), "moved"), origin: wc.origin}
	if err := os.Rename(wc.path, moved.path); err != nil {
		t.Fatalf("move the working copy: %v", err)
	}
	reattached, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: moved.path, Command: command})
	if err != nil {
		t.Fatalf("Initialize after the move: %v", err)
	}
	if !reattached.Reattached() || reattached.Repository() != original.Repository() {
		t.Fatalf("the moved working copy did not keep its gate: reattached %v at %q, want the original %q",
			reattached.Reattached(), reattached.Repository(), original.Repository())
	}

	// Somebody starts a different project at the path the move left free. It
	// has no assistant remote of its own, and it hashes to the gate the moved
	// working copy is using.
	fresh := freshWorkingCopyAt(t, wc.path)
	if id, err := gate.Identify(fresh); err != nil {
		t.Fatalf("Identify: %v", err)
	} else if id != original.ID() {
		t.Fatalf("the fresh working copy identifies as %q, not the moved copy's %q, so this test asks nothing", id, original.ID())
	}

	_, err = gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: fresh, Command: command})
	if !errors.Is(err, gate.ErrGateClaimed) {
		t.Fatalf("Initialize the fresh working copy = %v, want ErrGateClaimed", err)
	}
	// The refusal has to tell an operator meeting it on a fresh clone what it
	// found, who holds it, and who asked, or it reads as a mystery.
	for _, want := range []string{original.Repository(), reattached.WorkingPath(), resolved(t, fresh)} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not name %q: %v", want, err)
		}
	}

	// The moved working copy still has everything: its gate, its history, and
	// the ability to go on pushing into it.
	if url, ok := remoteURL(t, moved.path, gate.RemoteName); !ok || url != original.Repository() {
		t.Fatalf("the moved working copy's %s remote is %q, want %q", gate.RemoteName, url, original.Repository())
	}
	if got, want := refs(t, original.Repository()), []string{"refs/heads/main " + wc.commit}; !equal(got, want) {
		t.Fatalf("the gate's references are %v, want %v", got, want)
	}
	head := commitMore(t, moved, "after the fresh clone\n")
	rawGit(t, moved.path, "push", "--quiet", gate.RemoteName, "main")
	if got, want := refs(t, original.Repository()), []string{"refs/heads/main " + head}; !equal(got, want) {
		t.Fatalf("the moved working copy could not go on using its gate: refs %v, want %v", got, want)
	}
	if url, ok := remoteURL(t, fresh, gate.RemoteName); ok {
		t.Fatalf("the refused initialization pointed the fresh working copy's %s remote at %q", gate.RemoteName, url)
	}
}

// TestAGateWhoseWorkingCopyIsGoneIsStillAdoptable is the boundary of the
// refusal above. A gate nobody is bound to any more is a gate to repair, not
// one to refuse, so a working copy at the identifier it is filed under takes
// it over with its history rather than being turned away.
//
// The gate's record has to name a path other than the one initializing, or the
// refusal is never reached in the first place and this proves nothing. So the
// working copy is moved and reattached first, which leaves the record naming
// the moved path, and only then is that path deleted.
func TestAGateWhoseWorkingCopyIsGoneIsStillAdoptable(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home := t.TempDir()
	command, _ := recorderCommand(t, 0)

	original, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "main")

	moved := filepath.Join(filepath.Dir(wc.path), "moved")
	if err := os.Rename(wc.path, moved); err != nil {
		t.Fatalf("move the working copy: %v", err)
	}
	reattached, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: moved, Command: command})
	if err != nil {
		t.Fatalf("Initialize after the move: %v", err)
	}
	if reattached.Repository() != original.Repository() || reattached.WorkingPath() == original.WorkingPath() {
		t.Fatalf("the gate at %q now records %q; this test needs it to record a path other than %q",
			reattached.Repository(), reattached.WorkingPath(), original.WorkingPath())
	}

	// The working copy the gate now records is deleted outright, which leaves
	// the gate recording a path nothing stands on.
	if err := os.RemoveAll(moved); err != nil {
		t.Fatalf("remove the working copy: %v", err)
	}
	fresh := freshWorkingCopyAt(t, wc.path)

	again, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: fresh, Command: command})
	if err != nil {
		t.Fatalf("Initialize a working copy at the freed path: %v", err)
	}
	if again.Repository() != original.Repository() {
		t.Fatalf("the gate moved to %q, want %q", again.Repository(), original.Repository())
	}
	if got, want := refs(t, again.Repository()), []string{"refs/heads/main " + wc.commit}; !equal(got, want) {
		t.Fatalf("adopting the freed gate lost its history: %v, want %v", got, want)
	}
}

// TestAReattachedGateThatLostItsRecordIsRepairedNotAbandoned covers the damage
// TestInitializeRepairsADamagedGate inflicts, on a gate that has been
// reattached. The gate is then no longer filed under the hash of the path its
// working copy is at, so the record is not what makes it findable, and losing
// the record must not make initialization start over with an empty gate and
// leave every reference recorded against the old identifier unreachable.
func TestAReattachedGateThatLostItsRecordIsRepairedNotAbandoned(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home := t.TempDir()
	command, _ := recorderCommand(t, 0)

	original, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "main")

	moved := workingCopy{path: filepath.Join(filepath.Dir(wc.path), "moved"), origin: wc.origin}
	if err := os.Rename(wc.path, moved.path); err != nil {
		t.Fatalf("move the working copy: %v", err)
	}
	if _, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: moved.path, Command: command}); err != nil {
		t.Fatalf("Initialize after the move: %v", err)
	}
	if err := os.Remove(filepath.Join(original.Repository(), "assistant-gate.json")); err != nil {
		t.Fatalf("remove the record: %v", err)
	}

	repaired, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: moved.path, Command: command})
	if err != nil {
		t.Fatalf("Initialize a reattached gate that lost its record: %v", err)
	}
	if repaired.Repository() != original.Repository() {
		t.Fatalf("the gate moved to %q, abandoning %q and everything recorded against it",
			repaired.Repository(), original.Repository())
	}
	if repaired.ID() != original.ID() {
		t.Fatalf("the repaired gate identifies as %q, want the identifier it was created with, %q",
			repaired.ID(), original.ID())
	}
	if got, want := refs(t, repaired.Repository()), []string{"refs/heads/main " + wc.commit}; !equal(got, want) {
		t.Fatalf("the repaired gate lost its history: %v, want %v", got, want)
	}
	if url, ok := remoteURL(t, moved.path, gate.RemoteName); !ok || url != original.Repository() {
		t.Fatalf("the %s remote is %q, want %q", gate.RemoteName, url, original.Repository())
	}

	// Repaired means usable, and the record is back, so a second run is the
	// ordinary case rather than another reattachment.
	head := commitMore(t, moved, "after the repair\n")
	rawGit(t, moved.path, "push", "--quiet", gate.RemoteName, "main")
	if got, want := refs(t, repaired.Repository()), []string{"refs/heads/main " + head}; !equal(got, want) {
		t.Fatalf("the repaired gate refused a push: refs %v, want %v", got, want)
	}
	settled, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: moved.path, Command: command})
	if err != nil {
		t.Fatalf("Initialize again: %v", err)
	}
	if settled.Reattached() {
		t.Fatal("the gate reported another reattachment, so the record it was given does not name its working copy")
	}
}

// freshWorkingCopyAt starts an unrelated working copy at path, with no
// assistant remote of its own, and returns the path.
func freshWorkingCopyAt(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	rawGit(t, path, "init", "--quiet", ".")
	writeFile(t, filepath.Join(path, "unrelated.txt"), "another project\n")
	rawGit(t, path, "add", "-A")
	rawGit(t, path, "commit", "--quiet", "-m", "another project")
	if url, ok := remoteURL(t, path, gate.RemoteName); ok {
		t.Fatalf("the fresh working copy at %s already names a gate at %q", path, url)
	}
	return path
}
