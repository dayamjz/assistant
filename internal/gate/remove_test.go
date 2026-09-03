package gate_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/vcs"
)

// *vcs.Repository is what gate.WorkingCopy documents as the implementation
// this product uses, so a drift in either method has to fail the build rather
// than rot into a claim nobody checks.
var _ gate.WorkingCopy = (*vcs.Repository)(nil)

// detachable is *vcs.Repository plus the one operation internal/vcs does not
// carry yet. It runs the git command that operation will run, so it models
// what the real mechanism will put on the wire rather than a shape the real
// mechanism could not produce: a remote that is not there fails, exactly as
// gate.Detacher requires and as git reports it.
type detachable struct {
	*vcs.Repository
}

func (d detachable) RemoveRemote(_ context.Context, name string) error {
	if _, err := tryRawGit(d.Path(), "remote", "get-url", name); err != nil {
		return vcs.ErrRemoteNotFound
	}
	if out, err := tryRawGit(d.Path(), "remote", "remove", name); err != nil {
		return errors.New("git remote remove: " + out)
	}
	return nil
}

var _ gate.Detacher = detachable{}

func detachingOpener(ctx context.Context, path string) (gate.WorkingCopy, error) {
	repository, err := vcs.OpenWorktree(ctx, path)
	if err != nil {
		return nil, err
	}
	return detachable{Repository: repository}, nil
}

// TestRemoveRemoteIsStillTheNamedFollowUp fails on the day internal/vcs grows
// the operation gate.Detacher declares, because on that day the paragraph in
// git.go calling it a pending follow-up stops being true. The compile-time
// assertion above cannot notice, since the shim's own method shadows a
// promoted one.
func TestRemoveRemoteIsStillTheNamedFollowUp(t *testing.T) {
	t.Parallel()
	var repository any = (*vcs.Repository)(nil)
	if _, ok := repository.(gate.Detacher); ok {
		t.Fatal("*vcs.Repository now satisfies gate.Detacher, so RemoveRemote has landed in internal/vcs: git.go still calls it a pending follow-up and has to be corrected")
	}
}

// TestRemoveRefusesBeforeDeletingAnythingWhenItCannotDetach is the fail-closed
// half of removal. A gate whose repository is gone and whose remote still
// names it has not been removed, so nothing is deleted unless the whole thing
// can be.
func TestRemoveRefusesBeforeDeletingAnythingWhenItCannotDetach(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home := t.TempDir()
	command, _ := recorderCommand(t, 0)
	spec := gate.Spec{Home: home, WorkingPath: wc.path, Command: command}

	g, err := gate.Initialize(ctx(t), spec)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// The default opener is *vcs.Repository, which cannot remove a remote.
	if err := gate.Remove(ctx(t), spec); !errors.Is(err, gate.ErrDetachUnsupported) {
		t.Fatalf("Remove error = %v, want ErrDetachUnsupported", err)
	}
	if _, err := os.Stat(g.Repository()); err != nil {
		t.Fatalf("the refused removal deleted %s anyway: %v", g.Repository(), err)
	}
	if url, ok := remoteURL(t, wc.path, gate.RemoteName); !ok || url != g.Repository() {
		t.Fatalf("the refused removal changed the %s remote to %q", gate.RemoteName, url)
	}
}

// TestRemoveLeavesTheWorkingCopyUsableAndOriginIntact is the acceptance
// criterion for removal: what is taken away is the gate and nothing else.
func TestRemoveLeavesTheWorkingCopyUsableAndOriginIntact(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home := t.TempDir()
	command, log := recorderCommand(t, 0)
	spec := gate.Spec{Home: home, WorkingPath: wc.path, Command: command}

	g, err := gate.Initialize(ctx(t), spec)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	originURL, _ := remoteURL(t, wc.path, "origin")

	if err := gate.Remove(ctx(t), spec, gate.WithOpener(detachingOpener)); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if _, err := os.Stat(g.Repository()); !os.IsNotExist(err) {
		t.Fatalf("the gate repository survived removal (stat error %v)", err)
	}
	if url, ok := remoteURL(t, wc.path, gate.RemoteName); ok {
		t.Fatalf("the %s remote survived removal, pointing at %q", gate.RemoteName, url)
	}
	if url, ok := remoteURL(t, wc.path, "origin"); !ok || url != originURL {
		t.Fatalf("origin is now %q, want %q", url, originURL)
	}

	// Usable means usable: the working copy still commits and still pushes to
	// origin, and nothing is left that could start a run.
	head := commitMore(t, wc, "after removal\n")
	rawGit(t, wc.path, "push", "--quiet", "origin", "main")
	if got, want := refs(t, wc.origin), []string{"refs/heads/main " + head}; !equal(got, want) {
		t.Fatalf("origin refs after the push = %v, want %v", got, want)
	}
	if got := invocations(t, log); len(got) != 0 {
		t.Fatalf("something was still invoked after removal: %v", got)
	}
}

// TestRemoveIsSafeToRepeat covers the second call, which has nothing left to
// remove and must say so rather than reaching for something else.
func TestRemoveIsSafeToRepeat(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home := t.TempDir()
	command, _ := recorderCommand(t, 0)
	spec := gate.Spec{Home: home, WorkingPath: wc.path, Command: command}

	if _, err := gate.Initialize(ctx(t), spec); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := gate.Remove(ctx(t), spec, gate.WithOpener(detachingOpener)); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := gate.Remove(ctx(t), spec, gate.WithOpener(detachingOpener)); !errors.Is(err, gate.ErrNoGate) {
		t.Fatalf("Remove again = %v, want ErrNoGate", err)
	}
}

// TestRemoveRefusesAPathOutsideTheHomesRepositories is what stands between
// this package and deleting a directory only because a remote pointed at it.
func TestRemoveRefusesAPathOutsideTheHomesRepositories(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home := t.TempDir()
	command, _ := recorderCommand(t, 0)
	spec := gate.Spec{Home: home, WorkingPath: wc.path, Command: command}

	if _, err := gate.Initialize(ctx(t), spec); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	rawGit(t, wc.path, "remote", "set-url", gate.RemoteName, wc.origin)

	if err := gate.Remove(ctx(t), spec, gate.WithOpener(detachingOpener)); !errors.Is(err, gate.ErrNotAGate) {
		t.Fatalf("Remove error = %v, want ErrNotAGate", err)
	}
	if _, err := os.Stat(wc.origin); err != nil {
		t.Fatalf("the refused removal deleted origin: %v", err)
	}
	if _, ok := remoteURL(t, wc.path, gate.RemoteName); !ok {
		t.Fatal("the refused removal gave up the remote")
	}
}

// TestRemoveRefusesARepositoryCarryingNoGateRecord is the same guard for a
// path that is in the right place but cannot be identified as a gate. The
// record is what says a directory is one, per PRD principle P14.
func TestRemoveRefusesARepositoryCarryingNoGateRecord(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home := t.TempDir()
	command, _ := recorderCommand(t, 0)
	spec := gate.Spec{Home: home, WorkingPath: wc.path, Command: command}

	g, err := gate.Initialize(ctx(t), spec)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := os.Remove(filepath.Join(g.Repository(), "assistant-gate.json")); err != nil {
		t.Fatalf("remove the record: %v", err)
	}

	if err := gate.Remove(ctx(t), spec, gate.WithOpener(detachingOpener)); !errors.Is(err, gate.ErrNotAGate) {
		t.Fatalf("Remove error = %v, want ErrNotAGate", err)
	}
	if _, err := os.Stat(g.Repository()); err != nil {
		t.Fatalf("the refused removal deleted the repository: %v", err)
	}
	if _, ok := remoteURL(t, wc.path, gate.RemoteName); !ok {
		t.Fatal("the refused removal gave up the remote")
	}
}

// TestRemoveFromACopyRefusesAndLeavesTheOriginalsGate is the ownership check on
// the destructive side. A copied project directory carries the original's
// configuration, so its assistant remote names a gate that is not its own, and
// a removal that trusted that remote would delete the original's repository:
// every reference in it, and everything recorded against its identifier. That
// is the one loss in this package nothing can undo.
func TestRemoveFromACopyRefusesAndLeavesTheOriginalsGate(t *testing.T) {
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

	err = gate.Remove(ctx(t), gate.Spec{Home: home, WorkingPath: duplicate}, gate.WithOpener(detachingOpener))
	if !errors.Is(err, gate.ErrGateClaimed) {
		t.Fatalf("Remove from the copy = %v, want ErrGateClaimed", err)
	}
	// An operator meeting this on a copy has to be told what was found, who
	// holds it, and who asked, or the refusal reads as a mystery.
	for _, want := range []string{original.Repository(), original.WorkingPath(), resolved(t, duplicate)} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not name %q: %v", want, err)
		}
	}

	// The original keeps its gate, its references, and its remote.
	if _, statErr := os.Stat(original.Repository()); statErr != nil {
		t.Fatalf("the refused removal deleted the original's gate: %v", statErr)
	}
	if got, want := refs(t, original.Repository()), []string{"refs/heads/main " + wc.commit}; !equal(got, want) {
		t.Fatalf("the original's gate holds %v, want %v", got, want)
	}
	if url, ok := remoteURL(t, wc.path, gate.RemoteName); !ok || url != original.Repository() {
		t.Fatalf("the original's %s remote is %q, want %q", gate.RemoteName, url, original.Repository())
	}
	if url, ok := remoteURL(t, duplicate, gate.RemoteName); !ok || url != original.Repository() {
		t.Fatalf("the refused removal changed the copy's %s remote to %q; it refuses without removing anything", gate.RemoteName, url)
	}

	// The original can still remove its own gate, so the guard refuses the
	// copy rather than removal in general.
	if err := gate.Remove(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path}, gate.WithOpener(detachingOpener)); err != nil {
		t.Fatalf("Remove from the working copy the gate belongs to: %v", err)
	}
	if _, statErr := os.Stat(original.Repository()); !os.IsNotExist(statErr) {
		t.Fatalf("the gate survived removal by its own working copy (stat error %v)", statErr)
	}
}

// TestACopyThatAdoptedARecordlessGateCannotDeleteIt is the sequence the
// recordless adoption opens and has to close. A gate that lost its record is
// adopted rather than abandoned, and nothing on disk afterwards can tell a copy
// that took one over from a working copy that moved and lost the same file. So
// the adoption is recorded as resting on a remote rather than on a record, and
// the act that cannot be undone refuses on that. Without the refusal the
// adoption would manufacture the very evidence the removal reads.
func TestACopyThatAdoptedARecordlessGateCannotDeleteIt(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home := t.TempDir()
	command, _ := recorderCommand(t, 0)

	original, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "main")

	// The project directory is copied, so the copy inherits the original's
	// configuration and with it the remote naming the original's gate.
	duplicate := filepath.Join(filepath.Dir(wc.path), "copy")
	copyTree(t, wc.path, duplicate)
	if url, ok := remoteURL(t, duplicate, gate.RemoteName); !ok || url != original.Repository() {
		t.Fatalf("the copy's %s remote is %q, want the original's gate %q; the fixture does not pose the question this test asks",
			gate.RemoteName, url, original.Repository())
	}
	// The gate loses its record, which is the damage that leaves the copy's
	// inherited remote as the only thing saying whose gate this is.
	if err := os.Remove(filepath.Join(original.Repository(), "assistant-gate.json")); err != nil {
		t.Fatalf("remove the record: %v", err)
	}

	adopted, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: duplicate, Command: command})
	if err != nil {
		t.Fatalf("Initialize the copy: %v", err)
	}
	if adopted.Repository() != original.Repository() {
		t.Fatalf("the copy got gate %q, not the recordless one at %q; this test needs the adoption to happen",
			adopted.Repository(), original.Repository())
	}

	err = gate.Remove(ctx(t), gate.Spec{Home: home, WorkingPath: duplicate}, gate.WithOpener(detachingOpener))
	if !errors.Is(err, gate.ErrGateBindingInferred) {
		t.Fatalf("Remove from the copy that adopted a recordless gate = %v, want ErrGateBindingInferred", err)
	}
	for _, want := range []string{original.Repository(), resolved(t, duplicate)} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not name %q: %v", want, err)
		}
	}

	// The property that has to hold: the original's history is still there.
	if got, want := refs(t, original.Repository()), []string{"refs/heads/main " + wc.commit}; !equal(got, want) {
		t.Fatalf("the gate holds %v, want the original's history %v", got, want)
	}
	if url, ok := remoteURL(t, duplicate, gate.RemoteName); !ok || url != original.Repository() {
		t.Fatalf("the refused removal changed the copy's %s remote to %q; it refuses without removing anything",
			gate.RemoteName, url)
	}

	// An inferred binding stays inferred: initializing again is the ordinary
	// case, and it must not launder the binding into one a removal accepts.
	if _, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: duplicate, Command: command}); err != nil {
		t.Fatalf("Initialize the copy again: %v", err)
	}
	if err := gate.Remove(ctx(t), gate.Spec{Home: home, WorkingPath: duplicate}, gate.WithOpener(detachingOpener)); !errors.Is(err, gate.ErrGateBindingInferred) {
		t.Fatalf("Remove after a second initialization = %v, want ErrGateBindingInferred", err)
	}
	if got, want := refs(t, original.Repository()), []string{"refs/heads/main " + wc.commit}; !equal(got, want) {
		t.Fatalf("the gate holds %v, want the original's history %v", got, want)
	}

	// And the original is refused loudly rather than losing anything quietly,
	// which is the other half of what doc.go claims about this trade.
	if _, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command}); !errors.Is(err, gate.ErrGateClaimed) {
		t.Fatalf("Initialize the original = %v, want ErrGateClaimed", err)
	}
}

// TestAGateBoundByAnOrdinaryInitializationIsStillRemovable is the boundary of
// the refusal above. Only a binding that came from taking over a recordless
// gate is refused, so a gate whose record was written when the working copy's
// own path hashed to it, including one whose record was lost and rebuilt from
// that same hash, ejects normally.
func TestAGateBoundByAnOrdinaryInitializationIsStillRemovable(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home := t.TempDir()
	command, _ := recorderCommand(t, 0)
	spec := gate.Spec{Home: home, WorkingPath: wc.path, Command: command}

	g, err := gate.Initialize(ctx(t), spec)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "main")
	if err := os.Remove(filepath.Join(g.Repository(), "assistant-gate.json")); err != nil {
		t.Fatalf("remove the record: %v", err)
	}
	if _, err := gate.Initialize(ctx(t), spec); err != nil {
		t.Fatalf("Initialize to rebuild the record: %v", err)
	}

	if err := gate.Remove(ctx(t), spec, gate.WithOpener(detachingOpener)); err != nil {
		t.Fatalf("Remove a gate the working copy's own path hashes to: %v", err)
	}
	if _, err := os.Stat(g.Repository()); !os.IsNotExist(err) {
		t.Fatalf("the gate survived removal (stat error %v)", err)
	}
}

// TestTheRemedyEachRefusalNamesActuallyCompletes is the property a refusal has
// to have beyond being correct: it must leave the operator somewhere. Both
// refusals here name detaching, by dropping the assistant remote, as the step
// that always succeeds, because no guard refuses a detachment. A refusal that
// pointed at a removal which then refused again would be a dead end, and a dead
// end next to a destructive act is what makes somebody delete a directory by
// hand and lose the history every guard here protects.
func TestTheRemedyEachRefusalNamesActuallyCompletes(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home := t.TempDir()
	command, _ := recorderCommand(t, 0)

	original, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "main")

	// A copy inherits the remote, the record is lost, and the copy takes the
	// gate over on the strength of that remote alone.
	duplicate := filepath.Join(filepath.Dir(wc.path), "copy")
	copyTree(t, wc.path, duplicate)
	if err := os.Remove(filepath.Join(original.Repository(), "assistant-gate.json")); err != nil {
		t.Fatalf("remove the record: %v", err)
	}
	if adopted, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: duplicate, Command: command}); err != nil {
		t.Fatalf("Initialize the copy: %v", err)
	} else if adopted.Repository() != original.Repository() {
		t.Fatalf("the copy got %q, not the recordless gate at %q; this test needs the adoption to happen",
			adopted.Repository(), original.Repository())
	}

	// The copy is refused a removal, and the original is refused an
	// initialization. Both refusals fire, which is what earlier rounds
	// established; what this test adds is that neither is a dead end.
	if err := gate.Remove(ctx(t), gate.Spec{Home: home, WorkingPath: duplicate}, gate.WithOpener(detachingOpener)); !errors.Is(err, gate.ErrGateBindingInferred) {
		t.Fatalf("Remove from the copy = %v, want ErrGateBindingInferred", err)
	}
	if _, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command}); !errors.Is(err, gate.ErrGateClaimed) {
		t.Fatalf("Initialize the original = %v, want ErrGateClaimed", err)
	}

	// The step both messages name: detach the working copy that is holding the
	// gate. Nothing refuses this.
	rawGit(t, duplicate, "remote", "remove", gate.RemoteName)

	// The original's remedy completes: it takes its gate back, with its
	// history, and can then remove it.
	back, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command})
	if err != nil {
		t.Fatalf("the remedy ErrGateClaimed names did not complete: %v", err)
	}
	if back.Repository() != original.Repository() {
		t.Fatalf("the original got %q, want its gate back at %q", back.Repository(), original.Repository())
	}
	if got, want := refs(t, back.Repository()), []string{"refs/heads/main " + wc.commit}; !equal(got, want) {
		t.Fatalf("the gate holds %v, want the original's history %v", got, want)
	}
	if err := gate.Remove(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path}, gate.WithOpener(detachingOpener)); err != nil {
		t.Fatalf("the original still cannot remove its own gate: %v", err)
	}

	// And the copy's remedy completes too: detached, it initializes into a
	// gate of its own and can remove that.
	own, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: duplicate, Command: command})
	if err != nil {
		t.Fatalf("the remedy ErrGateBindingInferred names did not complete: %v", err)
	}
	if own.Repository() == original.Repository() {
		t.Fatalf("the copy took the original's gate again at %q", own.Repository())
	}
	if err := gate.Remove(ctx(t), gate.Spec{Home: home, WorkingPath: duplicate}, gate.WithOpener(detachingOpener)); err != nil {
		t.Fatalf("the copy cannot remove the gate it was given: %v", err)
	}
}

// TestRemoveTellsAWorkingCopyWhoseGateIsGoneWhatToDo is the first of the two
// states a removal cannot act on and must not conflate: a remote naming a path
// that holds nothing. Initializing gives the working copy a gate of its own,
// which is what the refusal names, and the removal after it succeeds.
func TestRemoveTellsAWorkingCopyWhoseGateIsGoneWhatToDo(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home := t.TempDir()
	command, _ := recorderCommand(t, 0)
	spec := gate.Spec{Home: home, WorkingPath: wc.path, Command: command}

	g, err := gate.Initialize(ctx(t), spec)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := os.RemoveAll(g.Repository()); err != nil {
		t.Fatalf("delete the gate: %v", err)
	}

	err = gate.Remove(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path}, gate.WithOpener(detachingOpener))
	if !errors.Is(err, gate.ErrNotAGate) {
		t.Fatalf("Remove with the gate deleted = %v, want ErrNotAGate", err)
	}
	if !strings.Contains(err.Error(), g.Repository()) {
		t.Fatalf("the refusal does not name the path it looked at: %v", err)
	}

	// The remedy it names completes.
	if _, err := gate.Initialize(ctx(t), spec); err != nil {
		t.Fatalf("the remedy ErrNotAGate names did not complete: %v", err)
	}
	if err := gate.Remove(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path}, gate.WithOpener(detachingOpener)); err != nil {
		t.Fatalf("Remove after the initialization the refusal named: %v", err)
	}
}

// TestRemoveTellsARecordlessGateApartByWhoIsAsking is the second of those two
// states, and the one where the answer depends on who is asking. A repository
// whose record is gone still holds its references, and whether initializing
// makes a removal possible turns on a fact the reader does not have: an
// initialization by the working copy the gate is filed under writes the record
// back on path-hash evidence, and one by any other working copy takes the gate
// over on the strength of a remote alone, which a removal refuses in turn.
//
// So the refusal has to tell those two apart. Naming one remedy for both sends
// a moved working copy to a removal that refuses it, which is the dead end
// doc.go says a refusal here must never create.
func TestRemoveTellsARecordlessGateApartByWhoIsAsking(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home := t.TempDir()
	command, _ := recorderCommand(t, 0)
	spec := gate.Spec{Home: home, WorkingPath: wc.path, Command: command}

	g, err := gate.Initialize(ctx(t), spec)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "main")
	record := filepath.Join(g.Repository(), "assistant-gate.json")
	if err := os.Remove(record); err != nil {
		t.Fatalf("remove the record: %v", err)
	}

	// Asked by the working copy the gate is filed under, the remedy is an
	// initialization, and it completes.
	filedUnder := gate.Remove(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path}, gate.WithOpener(detachingOpener))
	if !errors.Is(filedUnder, gate.ErrNotAGate) {
		t.Fatalf("Remove with the record gone = %v, want ErrNotAGate", filedUnder)
	}

	// The same gate, now asked by a working copy that moved, which is the
	// state an initialization cannot resolve.
	moved := workingCopy{path: filepath.Join(filepath.Dir(wc.path), "moved"), origin: wc.origin}
	if err := os.Rename(wc.path, moved.path); err != nil {
		t.Fatalf("move the working copy: %v", err)
	}
	elsewhere := gate.Remove(ctx(t), gate.Spec{Home: home, WorkingPath: moved.path}, gate.WithOpener(detachingOpener))
	if !errors.Is(elsewhere, gate.ErrNotAGate) {
		t.Fatalf("Remove from the moved working copy = %v, want ErrNotAGate", elsewhere)
	}
	for _, want := range []string{g.Repository(), resolved(t, moved.path)} {
		if !strings.Contains(elsewhere.Error(), want) {
			t.Fatalf("the refusal does not name %q: %v", want, elsewhere)
		}
	}
	// The refusal is the operator-facing output of this package, and these two
	// readers need different instructions, so one message for both is the
	// defect rather than a wording choice.
	if elsewhere.Error() == strings.ReplaceAll(filedUnder.Error(), g.WorkingPath(), resolved(t, moved.path)) {
		t.Fatalf("a working copy the gate is not filed under is told what the one it is filed under is told: %v", elsewhere)
	}

	// The state the message must not send the reader into: initializing here
	// binds the gate on the remote alone, and the removal after it refuses.
	if _, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: moved.path, Command: command}); err != nil {
		t.Fatalf("Initialize the moved working copy: %v", err)
	}
	if err := gate.Remove(ctx(t), gate.Spec{Home: home, WorkingPath: moved.path}, gate.WithOpener(detachingOpener)); !errors.Is(err, gate.ErrGateBindingInferred) {
		t.Fatalf("Remove after that initialization = %v, want ErrGateBindingInferred; "+
			"if this now succeeds the refusal above may name it again", err)
	}

	// The route the message does name completes, and the gate's history is
	// still there afterwards rather than deleted by a guess.
	rawGit(t, moved.path, "remote", "remove", gate.RemoteName)
	own, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: moved.path, Command: command})
	if err != nil {
		t.Fatalf("the route the refusal names did not complete: %v", err)
	}
	if own.Repository() == g.Repository() {
		t.Fatalf("the detached working copy took the recordless gate again at %q", own.Repository())
	}
	if err := gate.Remove(ctx(t), gate.Spec{Home: home, WorkingPath: moved.path}, gate.WithOpener(detachingOpener)); err != nil {
		t.Fatalf("the gate the route gave it cannot be removed: %v", err)
	}
	if got, want := refs(t, g.Repository()), []string{"refs/heads/main " + wc.commit}; !equal(got, want) {
		t.Fatalf("the recordless gate holds %v, want its history %v", got, want)
	}
}
