package gate_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/principles"
)

// TestAMovedWorkingCopyKeepsItsGate is the reattachment PRD section 5
// requires. The gate keeps its identifier, and with it everything recorded
// against that identifier, rather than being replaced by a second one at the
// hash of the new path.
func TestAMovedWorkingCopyKeepsItsGate(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home, opts := newHome(t)
	command, _ := recorderCommand(t, 0)

	first, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command}, opts()...)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "main")

	moved := filepath.Join(filepath.Dir(wc.path), "moved")
	if err := os.Rename(wc.path, moved); err != nil {
		t.Fatalf("move the working copy: %v", err)
	}

	second, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: moved, Command: command}, opts()...)
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
	third, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: moved, Command: command}, opts()...)
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
	home, opts := newHome(t)
	command, _ := recorderCommand(t, 0)

	original, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command}, opts()...)
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

	copied, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: duplicate, Command: command}, opts()...)
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
	home, opts := newHome(t)
	command, _ := recorderCommand(t, 0)

	template := t.TempDir()
	writeScript(t, filepath.Join(template, "hooks", gate.AdmissionHook), "#!/bin/sh\nexit 0\n")
	// Git reads a backslash in a configuration value as an escape, so a
	// native Windows path written here would make every later git invocation
	// fail to parse this file instead of choosing the template.
	appendConfig(t, cfg, "[init]\n\ttemplateDir = "+filepath.ToSlash(template)+"\n")

	_, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command}, opts()...)
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
	home, opts := newHome(t)
	command, _ := recorderCommand(t, 0)

	original, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command}, opts()...)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "main")

	moved := workingCopy{path: filepath.Join(filepath.Dir(wc.path), "moved"), origin: wc.origin}
	if err := os.Rename(wc.path, moved.path); err != nil {
		t.Fatalf("move the working copy: %v", err)
	}
	reattached, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: moved.path, Command: command}, opts()...)
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

	_, err = gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: fresh, Command: command}, opts()...)
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
	home, opts := newHome(t)
	command, _ := recorderCommand(t, 0)

	original, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command}, opts()...)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "main")

	moved := filepath.Join(filepath.Dir(wc.path), "moved")
	if err := os.Rename(wc.path, moved); err != nil {
		t.Fatalf("move the working copy: %v", err)
	}
	reattached, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: moved, Command: command}, opts()...)
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

	again, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: fresh, Command: command}, opts()...)
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
	home, opts := newHome(t)
	command, _ := recorderCommand(t, 0)

	original, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command}, opts()...)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "main")

	moved := workingCopy{path: filepath.Join(filepath.Dir(wc.path), "moved"), origin: wc.origin}
	if err := os.Rename(wc.path, moved.path); err != nil {
		t.Fatalf("move the working copy: %v", err)
	}
	if _, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: moved.path, Command: command}, opts()...); err != nil {
		t.Fatalf("Initialize after the move: %v", err)
	}
	removeRecord(t, original.Repository())

	repaired, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: moved.path, Command: command}, opts()...)
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
	settled, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: moved.path, Command: command}, opts()...)
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

// TestATemplateCannotDisplaceTheAdmissionHookDuringARepair is the admission
// hook's half of the template channel, and it closes rather than refuses.
//
// A gate takes pushes from the moment its repository exists, so obtaining a
// gate leaves an admission hook in one that has none before anything else runs.
// That happens before the git invocation a repair makes, so the admission hook
// name is already occupied by a hook this package wrote when a template is
// consulted. What this test establishes is not what became of the template's
// file but what runs on a push afterwards: the gate's own admission, with the
// template still configured. The refusal below is never reached for that one
// name, so the channel is shut rather than caught.
//
// Every other hook name is still caught, because nothing occupies those; see
// TestAnUnmanagedTemplateHookArrivingDuringARepairIsRefused, which is the same
// channel at a name this package does not install.
func TestATemplateCannotDisplaceTheAdmissionHookDuringARepair(t *testing.T) {
	cfg := gitEnvironment(t)
	wc := newWorkingCopy(t)
	home, opts := newHome(t)
	command, log := recorderCommand(t, 0)
	spec := gate.Spec{Home: home, WorkingPath: wc.path, Command: command}

	g, err := gate.Initialize(ctx(t), spec, opts()...)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// The template is configured after the gate exists, so the initialization
	// that created it had nothing to refuse. Its hook admits everything, which
	// is what makes running it visible below.
	template := t.TempDir()
	injected := filepath.Join(template, "hooks", gate.AdmissionHook)
	writeScript(t, injected, "#!/bin/sh\ncat >/dev/null\nexit 0\n")
	appendConfig(t, cfg, "[init]\n\ttemplateDir = "+filepath.ToSlash(template)+"\n")

	// The damage TestInitializeRepairsADamagedGate inflicts, which is what
	// leaves the name the template would fill.
	installed := filepath.Join(g.Repository(), "hooks", gate.AdmissionHook)
	if err := os.Remove(installed); err != nil {
		t.Fatalf("remove the admission hook: %v", err)
	}

	if _, err := gate.Initialize(ctx(t), spec, opts()...); err != nil {
		t.Fatalf("Initialize with a template configured: %v", err)
	}
	// The template's hook is neither installed nor preserved into the chain.
	preserved := filepath.Join(g.Repository(), "hooks", gate.AdmissionHook+gate.CustomHookSuffix)
	if _, err := os.Stat(preserved); err == nil {
		t.Fatalf("the template's hook was preserved at %s, which chains it into admission", preserved)
	}
	if got := readFile(t, installed); got == readFile(t, injected) {
		t.Fatalf("the gate's admission hook is the template's:\n%s", got)
	}

	// What the gate runs on a push is this package's admission, with the
	// template still configured and still shut out.
	rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "main")
	admitted := false
	for _, line := range invocations(t, log) {
		if strings.HasPrefix(line, "command gate admit") {
			admitted = true
		}
	}
	if !admitted {
		t.Fatalf("the repaired gate ran no admission: %v", invocations(t, log))
	}
}

// TestAnUnmanagedTemplateHookArrivingDuringARepairIsRefused is the same channel
// at a hook name this package does not install. An update hook decides
// per-reference acceptance by its exit status, so a template that lands one in
// the gate gives the configuration a vote on admission, which is what PRD
// principle P7 takes away from the pushed-from side.
func TestAnUnmanagedTemplateHookArrivingDuringARepairIsRefused(t *testing.T) {
	principles.Cite(t, principles.P7)
	cfg := gitEnvironment(t)
	wc := newWorkingCopy(t)
	home, opts := newHome(t)
	command, _ := recorderCommand(t, 0)
	spec := gate.Spec{Home: home, WorkingPath: wc.path, Command: command}

	g, err := gate.Initialize(ctx(t), spec, opts()...)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	template := t.TempDir()
	writeScript(t, filepath.Join(template, "hooks", "update"), "#!/bin/sh\nexit 1\n")
	appendConfig(t, cfg, "[init]\n\ttemplateDir = "+filepath.ToSlash(template)+"\n")

	if _, err := gate.Initialize(ctx(t), spec, opts()...); !errors.Is(err, gate.ErrTemplateHooks) {
		t.Fatalf("Initialize error = %v, want ErrTemplateHooks", err)
	}
	for _, name := range activeHookNames(t, g.Repository()) {
		if name == "update" {
			t.Fatal("the template's update hook was left in the gate, so it votes on every push")
		}
	}

	// The gate the refusal left behind is the one it had: its own two hooks,
	// and a push still reaches them.
	rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "main")
	if got, want := refs(t, g.Repository()), []string{"refs/heads/main " + wc.commit}; !equal(got, want) {
		t.Fatalf("the gate holds %v, want %v", got, want)
	}
}

// TestAGateWhoseRepositoryIsGoneIsNotReportedAsReattached is the difference
// between a gate that lost its record and a gate that is not there. The first
// still holds everything its runs recorded; the second holds nothing, and
// treating the two alike would create an empty repository at the old
// identifier and call it a move that kept its history.
//
// The sequence is the one this package's own refusals steer an operator into:
// a gate this package will not delete is left for the operator to delete by
// hand, and this is what the next initialization has to make of that.
func TestAGateWhoseRepositoryIsGoneIsNotReportedAsReattached(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home, opts := newHome(t)
	command, _ := recorderCommand(t, 0)

	original, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command}, opts()...)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "main")

	moved := workingCopy{path: filepath.Join(filepath.Dir(wc.path), "moved"), origin: wc.origin}
	if err := os.Rename(wc.path, moved.path); err != nil {
		t.Fatalf("move the working copy: %v", err)
	}
	spec := gate.Spec{Home: home, WorkingPath: moved.path, Command: command}
	if reattached, err := gate.Initialize(ctx(t), spec, opts()...); err != nil {
		t.Fatalf("Initialize after the move: %v", err)
	} else if !reattached.Reattached() || reattached.Repository() != original.Repository() {
		t.Fatalf("the moved working copy did not keep its gate; this test needs the remote to name %q",
			original.Repository())
	}

	// The operator deletes the gate directory, leaving the remote naming a
	// path that holds nothing.
	if err := os.RemoveAll(original.Repository()); err != nil {
		t.Fatalf("delete the gate: %v", err)
	}

	again, err := gate.Initialize(ctx(t), spec, opts()...)
	if err != nil {
		t.Fatalf("Initialize after the gate was deleted: %v", err)
	}
	if again.Reattached() {
		t.Fatal("a gate created from nothing was reported as a reattachment, which tells the operator their run history is intact")
	}
	wantID, err := gate.Identify(moved.path)
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if again.ID() != wantID {
		t.Fatalf("the new gate is filed under %q, want the current path's identifier %q", again.ID(), wantID)
	}
	if again.Repository() == original.Repository() {
		t.Fatalf("the new gate took the deleted gate's place at %q", again.Repository())
	}
	if got := refs(t, again.Repository()); len(got) != 0 {
		t.Fatalf("a gate created from nothing already holds %v", got)
	}
	if url, ok := remoteURL(t, moved.path, gate.RemoteName); !ok || url != again.Repository() {
		t.Fatalf("the %s remote is %q, want the new gate %q", gate.RemoteName, url, again.Repository())
	}

	// A gate created from nothing is an ordinary gate: it takes a push and it
	// can be removed, rather than being stamped with a binding nothing
	// established.
	head := commitMore(t, moved, "after the deletion\n")
	rawGit(t, moved.path, "push", "--quiet", gate.RemoteName, "main")
	if got, want := refs(t, again.Repository()), []string{"refs/heads/main " + head}; !equal(got, want) {
		t.Fatalf("the new gate holds %v, want %v", got, want)
	}
	if err := gate.Remove(ctx(t), gate.Spec{Home: home, WorkingPath: moved.path}, opts(gate.WithOpener(detachingOpener))...); err != nil {
		t.Fatalf("Remove a gate this package created from nothing: %v", err)
	}
}

// TestAWorkingCopyThatMovesAwayAndBackKeepsItsGate walks a working copy out of
// the path its gate is named for and back again, over a gate that has also lost
// its record, so that the only thing connecting the two on the way out is the
// remote and on the way back is the path hash. Each initialization has to land
// on the same gate, or the history recorded against its identifier is stranded,
// and the working copy standing where the gate is named for has to be able to
// eject it: a gate its own owner can never remove is a directory somebody
// deletes by hand.
func TestAWorkingCopyThatMovesAwayAndBackKeepsItsGate(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home, opts := newHome(t)
	command, _ := recorderCommand(t, 0)

	original, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command}, opts()...)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "main")
	removeRecord(t, original.Repository())

	// Away. The gate is reached through the remote alone, and it is kept.
	moved := filepath.Join(filepath.Dir(wc.path), "moved")
	if err := os.Rename(wc.path, moved); err != nil {
		t.Fatalf("move the working copy: %v", err)
	}
	away, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: moved, Command: command}, opts()...)
	if err != nil {
		t.Fatalf("Initialize after the move: %v", err)
	}
	if away.Repository() != original.Repository() || !away.Reattached() {
		t.Fatalf("the moved working copy got %q (reattached %v), want the gate at %q carried across the move",
			away.Repository(), away.Reattached(), original.Repository())
	}

	// And back, so the working copy stands at the path the gate is named for.
	if err := os.Rename(moved, wc.path); err != nil {
		t.Fatalf("move the working copy back: %v", err)
	}
	back, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command}, opts()...)
	if err != nil {
		t.Fatalf("Initialize back at the original path: %v", err)
	}
	if back.Repository() != original.Repository() {
		t.Fatalf("the gate moved to %q, want %q", back.Repository(), original.Repository())
	}
	if got, want := refs(t, back.Repository()), []string{"refs/heads/main " + wc.commit}; !equal(got, want) {
		t.Fatalf("the gate holds %v, want its history %v", got, want)
	}

	if err := gate.Remove(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path}, opts(gate.WithOpener(detachingOpener))...); err != nil {
		t.Fatalf("Remove from the working copy the gate is named for: %v", err)
	}
	if _, err := os.Stat(original.Repository()); !os.IsNotExist(err) {
		t.Fatalf("the gate survived removal (stat error %v)", err)
	}
}

// TestAProjectOnAFreedPathTakesTheGateThatPathHashesTo pins the residual gap in
// identity that doc.go names, and pins that both halves of it behave the same.
//
// A working copy that moves without initializing anywhere afterwards leaves
// nothing behind saying where it went: not in the gate, whose record still
// names the path it left, and not in the ownership index, whose row is against
// that same path. The next project to stand there hashes to the same identifier
// and, from everything this package can read, is that working copy. So it takes
// the gate.
//
// What this test is for is that the two states are alike. A gate carrying a
// record and a gate that lost one are the same question here, and the earlier
// arrangement answered them differently, refusing the second while the first,
// which is the ordinary state, went through. An exception in one arm is the
// part that would have shipped.
//
// TestAMovedWorkingCopyIsProtectedOnceItHasInitializedWhereItStands is what
// closes this, and it is the reason the gap is a gap rather than a defect: one
// initialization at the new path puts the moved working copy in the index, and
// from then on the project on the freed path is refused.
func TestAProjectOnAFreedPathTakesTheGateThatPathHashesTo(t *testing.T) {
	for _, c := range []struct {
		name   string
		damage func(t *testing.T, repo string)
	}{
		{name: "the gate keeps its record", damage: func(*testing.T, string) {}},
		{name: "the gate lost its record", damage: removeRecord},
	} {
		t.Run(c.name, func(t *testing.T) {
			gitEnvironment(t)
			wc := newWorkingCopy(t)
			home, opts := newHome(t)
			command, _ := recorderCommand(t, 0)

			original, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command}, opts()...)
			if err != nil {
				t.Fatalf("Initialize: %v", err)
			}
			rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "main")

			// The working copy moves and does not initialize at its new path,
			// so nothing in this home learns where it went.
			moved := workingCopy{path: filepath.Join(filepath.Dir(wc.path), "moved"), origin: wc.origin}
			if err := os.Rename(wc.path, moved.path); err != nil {
				t.Fatalf("move the working copy: %v", err)
			}
			c.damage(t, original.Repository())

			fresh := freshWorkingCopyAt(t, wc.path)
			took, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: fresh, Command: command}, opts()...)
			if err != nil {
				t.Fatalf("Initialize the project on the freed path: %v", err)
			}
			if took.Repository() != original.Repository() {
				t.Fatalf("the project got %q, not the gate its path hashes to at %q",
					took.Repository(), original.Repository())
			}
			if err := gate.Remove(ctx(t), gate.Spec{Home: home, WorkingPath: fresh}, opts(gate.WithOpener(detachingOpener))...); err != nil {
				t.Fatalf("Remove from the project on the freed path: %v", err)
			}
		})
	}
}

// TestAMovedWorkingCopyIsProtectedOnceItHasInitializedWhereItStands is what the
// ownership index buys over the gate's own record, stated as the one sequence
// where the record cannot answer and the index can.
//
// The gate's record is deleted, so nothing inside the gate says whose it is.
// The moved working copy is still there and still points at the gate, and the
// home recorded that binding when it initialized at its new path. A project
// landing on the path the move freed is refused on the strength of that record
// alone.
func TestAMovedWorkingCopyIsProtectedOnceItHasInitializedWhereItStands(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home, opts := newHome(t)
	command, _ := recorderCommand(t, 0)

	original, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command}, opts()...)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "main")

	moved := workingCopy{path: filepath.Join(filepath.Dir(wc.path), "moved"), origin: wc.origin}
	if err := os.Rename(wc.path, moved.path); err != nil {
		t.Fatalf("move the working copy: %v", err)
	}
	if _, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: moved.path, Command: command}, opts()...); err != nil {
		t.Fatalf("Initialize after the move: %v", err)
	}
	// Everything inside the gate that could say whose it is goes away, which
	// leaves the index as the only record of the binding.
	removeRecord(t, original.Repository())

	fresh := freshWorkingCopyAt(t, wc.path)
	_, err = gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: fresh, Command: command}, opts()...)
	if !errors.Is(err, gate.ErrGateClaimed) {
		t.Fatalf("Initialize the project on the freed path = %v, want ErrGateClaimed", err)
	}
	for _, want := range []string{original.Repository(), resolved(t, moved.path), resolved(t, fresh)} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not name %q: %v", want, err)
		}
	}

	// The moved working copy still has its gate, its history, and the ability
	// to go on pushing into it.
	head := commitMore(t, moved, "after the fresh clone\n")
	rawGit(t, moved.path, "push", "--quiet", gate.RemoteName, "main")
	if got, want := refs(t, original.Repository()), []string{"refs/heads/main " + head}; !equal(got, want) {
		t.Fatalf("the moved working copy could not go on using its gate: refs %v, want %v", got, want)
	}
	if url, ok := remoteURL(t, fresh, gate.RemoteName); ok {
		t.Fatalf("the refused initialization pointed the fresh working copy's %s remote at %q", gate.RemoteName, url)
	}
}
