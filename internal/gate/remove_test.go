package gate_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/vcs"
)

// *vcs.Repository is what gate.WorkingCopy documents as the implementation
// this product uses, so a drift in either method has to fail the build rather
// than rot into a claim nobody checks.
var _ gate.WorkingCopy = (*vcs.Repository)(nil)

// attached is a working copy that cannot give a remote up: internal/vcs's
// handle with the detaching operation hidden behind an interface that does not
// declare it. It exists so the fail-closed half of removal is still reachable
// now that the product's own working copy can detach.
type attached struct {
	remoteURL func(ctx context.Context, name string) (string, error)
	setRemote func(ctx context.Context, name, url string) error
}

func (a attached) RemoteURL(ctx context.Context, name string) (string, error) {
	return a.remoteURL(ctx, name)
}

func (a attached) SetRemote(ctx context.Context, name, url string) error {
	return a.setRemote(ctx, name, url)
}

var _ gate.WorkingCopy = attached{}

// attachedOpener opens a working copy that cannot detach.
func attachedOpener(ctx context.Context, path string) (gate.WorkingCopy, error) {
	repository, err := vcs.OpenWorktree(ctx, path)
	if err != nil {
		return nil, err
	}
	return attached{remoteURL: repository.RemoteURL, setRemote: repository.SetRemote}, nil
}

// TestTheProductWorkingCopyCanDetach fails if *vcs.Repository stops satisfying
// gate.Detacher, which is the day the paragraph in git.go stops being true and
// the day the default opener stops being enough for a removal.
func TestTheProductWorkingCopyCanDetach(t *testing.T) {
	t.Parallel()
	var repository any = (*vcs.Repository)(nil)
	if _, ok := repository.(gate.Detacher); !ok {
		t.Fatal("*vcs.Repository no longer satisfies gate.Detacher, so the default opener cannot remove a gate and git.go has to be corrected")
	}
}

// TestRemoveRefusesBeforeDeletingAnythingWhenItCannotDetach is the fail-closed
// half of removal. A gate whose repository is gone and whose remote still
// names it has not been removed, so nothing is deleted unless the whole thing
// can be.
func TestRemoveRefusesBeforeDeletingAnythingWhenItCannotDetach(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home, opts := newHome(t)
	command, _ := recorderCommand(t, 0)
	spec := gate.Spec{Home: home, WorkingPath: wc.path, Command: command}

	g, err := gate.Initialize(ctx(t), spec, opts()...)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// A working copy that cannot give the remote up leaves the gate half
	// removed, so nothing is removed at all.
	if err := gate.Remove(ctx(t), spec, opts(gate.WithOpener(attachedOpener))...); !errors.Is(err, gate.ErrDetachUnsupported) {
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
	home, opts := newHome(t)
	command, log := recorderCommand(t, 0)
	spec := gate.Spec{Home: home, WorkingPath: wc.path, Command: command}

	g, err := gate.Initialize(ctx(t), spec, opts()...)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	originURL, _ := remoteURL(t, wc.path, "origin")

	if err := gate.Remove(ctx(t), spec, opts()...); err != nil {
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
	home, opts := newHome(t)
	command, _ := recorderCommand(t, 0)
	spec := gate.Spec{Home: home, WorkingPath: wc.path, Command: command}

	if _, err := gate.Initialize(ctx(t), spec, opts()...); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := gate.Remove(ctx(t), spec, opts()...); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := gate.Remove(ctx(t), spec, opts()...); !errors.Is(err, gate.ErrNoGate) {
		t.Fatalf("Remove again = %v, want ErrNoGate", err)
	}
}

// TestRemoveRefusesAPathOutsideTheHomesRepositories is what stands between
// this package and deleting a directory only because a remote pointed at it.
func TestRemoveRefusesAPathOutsideTheHomesRepositories(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home, opts := newHome(t)
	command, _ := recorderCommand(t, 0)
	spec := gate.Spec{Home: home, WorkingPath: wc.path, Command: command}

	if _, err := gate.Initialize(ctx(t), spec, opts()...); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	rawGit(t, wc.path, "remote", "set-url", gate.RemoteName, wc.origin)

	if err := gate.Remove(ctx(t), spec, opts()...); !errors.Is(err, gate.ErrNotAGate) {
		t.Fatalf("Remove error = %v, want ErrNotAGate", err)
	}
	if _, err := os.Stat(wc.origin); err != nil {
		t.Fatalf("the refused removal deleted origin: %v", err)
	}
	if _, ok := remoteURL(t, wc.path, gate.RemoteName); !ok {
		t.Fatal("the refused removal gave up the remote")
	}

	// Every refusal here owes the reader a step that succeeds from where they
	// stand, and this is the one state where an initialization is not it: the
	// path the remote names is not a gate of this home, so there is no binding
	// to rebuild over it.
	err := gate.Remove(ctx(t), spec, opts()...)
	if !namesDetaching(err) {
		t.Fatalf("the refusal names no step the reader can take: %v", err)
	}

	// And that step completes.
	rawGit(t, wc.path, "remote", "remove", gate.RemoteName)
	own, err := gate.Initialize(ctx(t), spec, opts()...)
	if err != nil {
		t.Fatalf("the step the refusal names did not complete: %v", err)
	}
	if err := gate.Remove(ctx(t), spec, opts()...); err != nil {
		t.Fatalf("the gate that step gave it cannot be removed: %v", err)
	}
	if _, err := os.Stat(own.Repository()); !os.IsNotExist(err) {
		t.Fatalf("the gate survived removal (stat error %v)", err)
	}
	if _, err := os.Stat(wc.origin); err != nil {
		t.Fatalf("origin was deleted along the way: %v", err)
	}
}

// TestRemoveRefusesARepositoryCarryingNoGateRecord is the same guard for a
// path that is in the right place but cannot be identified as a gate. The
// record is what says a directory is one, per PRD principle P14.
func TestRemoveRefusesARepositoryCarryingNoGateRecord(t *testing.T) {
	principles.Cite(t, principles.P14)
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home, opts := newHome(t)
	command, _ := recorderCommand(t, 0)
	spec := gate.Spec{Home: home, WorkingPath: wc.path, Command: command}

	g, err := gate.Initialize(ctx(t), spec, opts()...)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	removeRecord(t, g.Repository())

	if err := gate.Remove(ctx(t), spec, opts()...); !errors.Is(err, gate.ErrNotAGate) {
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

	err = gate.Remove(ctx(t), gate.Spec{Home: home, WorkingPath: duplicate}, opts()...)
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
	if err := gate.Remove(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path}, opts()...); err != nil {
		t.Fatalf("Remove from the working copy the gate belongs to: %v", err)
	}
	if _, statErr := os.Stat(original.Repository()); !os.IsNotExist(statErr) {
		t.Fatalf("the gate survived removal by its own working copy (stat error %v)", statErr)
	}
}

// TestACopyDoesNotAdoptARecordlessGate is the sequence the recordless adoption
// used to open. A gate that lost its record is adopted rather than abandoned,
// and nothing inside such a gate says whose it is, so a copy of a gated project
// holding the inherited remote used to be able to take it over and only be
// stopped at the deletion afterwards, by a mark recorded at the adoption.
//
// The home's index answers the question the gate could not. The original is
// still there and still bound to the gate, and that is enumerable whether the
// gate carries a record or not, so the copy is not offered the gate at all and
// gets one of its own. Nothing is taken over and nothing has to be remembered
// about how a binding was reached.
func TestACopyDoesNotAdoptARecordlessGate(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home, opts := newHome(t)
	command, _ := recorderCommand(t, 0)

	original, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command}, opts()...)
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
	// The gate loses its record, which used to leave the copy's inherited
	// remote as the only thing saying whose gate this is.
	removeRecord(t, original.Repository())

	own, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: duplicate, Command: command}, opts()...)
	if err != nil {
		t.Fatalf("Initialize the copy: %v", err)
	}
	if own.Repository() == original.Repository() {
		t.Fatalf("the copy took the recordless gate at %q instead of getting one of its own", own.Repository())
	}
	if id, err := gate.Identify(duplicate); err != nil {
		t.Fatalf("Identify: %v", err)
	} else if own.ID() != id {
		t.Fatalf("the copy got gate %q, want the one its own path hashes to, %q", own.ID(), id)
	}

	// The property that has to hold: the original's history is still there and
	// still the original's.
	if got, want := refs(t, original.Repository()), []string{"refs/heads/main " + wc.commit}; !equal(got, want) {
		t.Fatalf("the gate holds %v, want the original's history %v", got, want)
	}
	if url, ok := remoteURL(t, wc.path, gate.RemoteName); !ok || url != original.Repository() {
		t.Fatalf("the original's %s remote is %q, want %q", gate.RemoteName, url, original.Repository())
	}

	// And the original is not refused: nothing took its gate, so initializing
	// writes the record back and the removal after that succeeds.
	back, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command}, opts()...)
	if err != nil {
		t.Fatalf("Initialize the original: %v", err)
	}
	if back.Repository() != original.Repository() {
		t.Fatalf("the original got %q, want its own gate at %q", back.Repository(), original.Repository())
	}
	if err := gate.Remove(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path}, opts()...); err != nil {
		t.Fatalf("the original cannot remove its own gate: %v", err)
	}
}

// TestACopyCannotRemoveTheOriginalsRecordlessGate is the destructive half of the
// sequence above, asked directly rather than after an adoption. A copy that
// never initializes still holds the inherited remote, and a removal run there
// names the original's gate. It is refused, and the gate a removal is refused
// over is left exactly as it was, remote included.
func TestACopyCannotRemoveTheOriginalsRecordlessGate(t *testing.T) {
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
	removeRecord(t, original.Repository())

	err = gate.Remove(ctx(t), gate.Spec{Home: home, WorkingPath: duplicate}, opts()...)
	if !errors.Is(err, gate.ErrGateClaimed) {
		t.Fatalf("Remove from the copy = %v, want ErrGateClaimed", err)
	}
	for _, want := range []string{original.Repository(), resolved(t, wc.path), resolved(t, duplicate)} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not name %q: %v", want, err)
		}
	}
	if got, want := refs(t, original.Repository()), []string{"refs/heads/main " + wc.commit}; !equal(got, want) {
		t.Fatalf("the gate holds %v, want the original's history %v", got, want)
	}
	if url, ok := remoteURL(t, duplicate, gate.RemoteName); !ok || url != original.Repository() {
		t.Fatalf("the refused removal changed the copy's %s remote to %q; it refuses without removing anything",
			gate.RemoteName, url)
	}
}

// TestAGateWhoseRecordWasLostAndRebuiltIsRemovable is the boundary of the
// refusals above. Removal refuses a gate that carries no record and a gate
// another working copy is bound to, and neither is what a working copy whose
// own gate lost its record has: it repairs the record by initializing, nobody
// else is bound, and the removal after that ejects normally.
//
// This is the promise recordlessRefusal makes, checked rather than assumed.
func TestAGateWhoseRecordWasLostAndRebuiltIsRemovable(t *testing.T) {
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
	removeRecord(t, g.Repository())
	if _, err := gate.Initialize(ctx(t), spec, opts()...); err != nil {
		t.Fatalf("Initialize to rebuild the record: %v", err)
	}

	if err := gate.Remove(ctx(t), spec, opts()...); err != nil {
		t.Fatalf("Remove a gate the working copy's own path hashes to: %v", err)
	}
	if _, err := os.Stat(g.Repository()); !os.IsNotExist(err) {
		t.Fatalf("the gate survived removal (stat error %v)", err)
	}
}

// TestTheRemedyEachRefusalNamesActuallyCompletes is the property a refusal has
// to have beyond being correct: it must leave the operator somewhere. A refusal
// that pointed at a removal which then refused again would be a dead end, and a
// dead end next to a destructive act is what makes somebody delete a directory
// by hand and lose the history every guard here protects.
//
// The two refusals a working copy meets over somebody else's gate are checked
// together, because what each one owes an operator is the same debt and the
// side that gets forgotten is the one nobody walked.
func TestTheRemedyEachRefusalNamesActuallyCompletes(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home, opts := newHome(t)
	command, _ := recorderCommand(t, 0)

	original, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command}, opts()...)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "main")

	// A copy inherits the remote, so a removal run there names a gate that is
	// not its own and is refused.
	duplicate := filepath.Join(filepath.Dir(wc.path), "copy")
	copyTree(t, wc.path, duplicate)
	refused := gate.Remove(ctx(t), gate.Spec{Home: home, WorkingPath: duplicate}, opts()...)
	if !errors.Is(refused, gate.ErrGateClaimed) {
		t.Fatalf("Remove from the copy = %v, want ErrGateClaimed", refused)
	}
	if !namesDetaching(refused) {
		t.Fatalf("the copy is not told to detach, which is the only step that succeeds from there: %v", refused)
	}

	// The step the message names. Nothing refuses this, and initializing
	// afterwards gives the copy a gate of its own that it can remove.
	rawGit(t, duplicate, "remote", "remove", gate.RemoteName)
	own, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: duplicate, Command: command}, opts()...)
	if err != nil {
		t.Fatalf("the remedy ErrGateClaimed names did not complete: %v", err)
	}
	if own.Repository() == original.Repository() {
		t.Fatalf("the detached copy took the original's gate at %q", own.Repository())
	}
	if err := gate.Remove(ctx(t), gate.Spec{Home: home, WorkingPath: duplicate}, opts()...); err != nil {
		t.Fatalf("the copy cannot remove the gate it was given: %v", err)
	}

	// The other refusal: the original's gate loses its record, so a removal
	// there refuses too, and the initialization it names completes.
	removeRecord(t, original.Repository())
	recordless := gate.Remove(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path}, opts()...)
	if !errors.Is(recordless, gate.ErrNotAGate) {
		t.Fatalf("Remove with the record gone = %v, want ErrNotAGate", recordless)
	}
	if !namesInitializing(recordless, resolved(t, wc.path)) {
		t.Fatalf("the working copy holding a recordless gate is not told to initialize: %v", recordless)
	}
	if _, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command}, opts()...); err != nil {
		t.Fatalf("the remedy ErrNotAGate names did not complete: %v", err)
	}
	if got, want := refs(t, original.Repository()), []string{"refs/heads/main " + wc.commit}; !equal(got, want) {
		t.Fatalf("the repair lost the gate's history: %v, want %v", got, want)
	}
	if err := gate.Remove(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path}, opts()...); err != nil {
		t.Fatalf("the original still cannot remove its own gate: %v", err)
	}
	if _, err := os.Stat(original.Repository()); !os.IsNotExist(err) {
		t.Fatalf("the gate survived removal (stat error %v)", err)
	}
}

// TestRemoveTellsAWorkingCopyWhoseGateIsGoneWhatToDo is the first of the two
// states a removal cannot act on and must not conflate: a remote naming a path
// that holds nothing. Initializing gives the working copy a gate of its own,
// which is what the refusal names, and the removal after it succeeds.
func TestRemoveTellsAWorkingCopyWhoseGateIsGoneWhatToDo(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home, opts := newHome(t)
	command, _ := recorderCommand(t, 0)
	spec := gate.Spec{Home: home, WorkingPath: wc.path, Command: command}

	g, err := gate.Initialize(ctx(t), spec, opts()...)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := os.RemoveAll(g.Repository()); err != nil {
		t.Fatalf("delete the gate: %v", err)
	}

	err = gate.Remove(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path}, opts()...)
	if !errors.Is(err, gate.ErrNotAGate) {
		t.Fatalf("Remove with the gate deleted = %v, want ErrNotAGate", err)
	}
	if !strings.Contains(err.Error(), g.Repository()) {
		t.Fatalf("the refusal does not name the path it looked at: %v", err)
	}

	// The remedy it names completes.
	if _, err := gate.Initialize(ctx(t), spec, opts()...); err != nil {
		t.Fatalf("the remedy ErrNotAGate names did not complete: %v", err)
	}
	if err := gate.Remove(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path}, opts()...); err != nil {
		t.Fatalf("Remove after the initialization the refusal named: %v", err)
	}
}

// TestARecordlessGateNamesAnInitializationThatCompletes is the second of those
// two states: a repository whose record is gone. It still holds its references,
// so a removal will not delete it on the strength of a remote, and the action
// the refusal names has to be one that actually gets the reader out.
//
// It is one action for every asker now, and that is the point of the test. The
// refusal is reached only after this operation has established that no other
// working copy is bound to the gate, so the initialization it names writes the
// record back and the removal after it succeeds, whether the asker is the
// working copy the gate is filed under or one that has moved. The earlier
// arrangement had to tell those two apart, because for one of them the
// initialization it would have named led to a second refusal.
func TestARecordlessGateNamesAnInitializationThatCompletes(t *testing.T) {
	for _, c := range []struct {
		name string
		// move is where the working copy stands when it asks. Returning the
		// path it started at leaves it where its gate is filed under.
		move func(t *testing.T, wc workingCopy) string
	}{
		{
			name: "asked by the working copy the gate is filed under",
			move: func(_ *testing.T, wc workingCopy) string { return wc.path },
		},
		{
			name: "asked by a working copy that moved",
			move: func(t *testing.T, wc workingCopy) string {
				moved := filepath.Join(filepath.Dir(wc.path), "moved")
				if err := os.Rename(wc.path, moved); err != nil {
					t.Fatalf("move the working copy: %v", err)
				}
				return moved
			},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			gitEnvironment(t)
			wc := newWorkingCopy(t)
			home, opts := newHome(t)
			command, _ := recorderCommand(t, 0)

			g, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command}, opts()...)
			if err != nil {
				t.Fatalf("Initialize: %v", err)
			}
			rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "main")
			removeRecord(t, g.Repository())
			asking := c.move(t, wc)

			refused := gate.Remove(ctx(t), gate.Spec{Home: home, WorkingPath: asking}, opts()...)
			if !errors.Is(refused, gate.ErrNotAGate) {
				t.Fatalf("Remove with the record gone = %v, want ErrNotAGate", refused)
			}
			for _, want := range []string{g.Repository(), resolved(t, asking)} {
				if !strings.Contains(refused.Error(), want) {
					t.Fatalf("the refusal does not name %q: %v", want, refused)
				}
			}
			// Which action the refusal instructs is the contract this test
			// exists for, and the refusal text is this package's
			// operator-facing output, so it is read rather than inferred.
			if !namesInitializing(refused, resolved(t, asking)) {
				t.Fatalf("the reader is not told to initialize, which is the step that rebuilds the record "+
					"and opens the removal: %v", refused)
			}

			// The refusal left the gate exactly as it was.
			if got, want := refs(t, g.Repository()), []string{"refs/heads/main " + wc.commit}; !equal(got, want) {
				t.Fatalf("the refused removal disturbed the gate: refs %v, want %v", got, want)
			}

			// And the action it named completes, keeping the gate's identifier
			// and its history rather than starting over with an empty one.
			repaired, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: asking, Command: command}, opts()...)
			if err != nil {
				t.Fatalf("the remedy the refusal names did not complete: %v", err)
			}
			if repaired.Repository() != g.Repository() {
				t.Fatalf("the repair moved the gate to %q, want %q", repaired.Repository(), g.Repository())
			}
			if got, want := refs(t, repaired.Repository()), []string{"refs/heads/main " + wc.commit}; !equal(got, want) {
				t.Fatalf("the repair lost the gate's history: %v, want %v", got, want)
			}
			if err := gate.Remove(ctx(t), gate.Spec{Home: home, WorkingPath: asking}, opts()...); err != nil {
				t.Fatalf("the removal after the initialization the refusal named: %v", err)
			}
			if _, err := os.Stat(g.Repository()); !os.IsNotExist(err) {
				t.Fatalf("the gate survived removal (stat error %v)", err)
			}
		})
	}
}

// TestEveryRemovalRefusalLeavesTheGateRefusingPushes is the sibling of
// TestEveryEarlyRefusalLeavesTheGateRefusingPushes, and it exists because the
// invariant doc.go states is written without qualification: a gate never
// accepts a push that admission has not seen.
//
// A removal cannot create a gate with no admission hook, so this was for a
// while an argument rather than a mechanism, and an argument is what the next
// operation added here would not inherit. Both operations now obtain their gate
// the same way and the seal comes with it, so this test is the same question
// asked on the other side.
func TestEveryRemovalRefusalLeavesTheGateRefusingPushes(t *testing.T) {
	gitEnvironment(t)
	command, _ := recorderCommand(t, 0)

	cases := []struct {
		name string
		// refuse damages the state so that a removal from wc refuses, and
		// returns the error wanted and the options the removal takes.
		refuse func(t *testing.T, home string, wc workingCopy, g *gate.Gate,
			opts func(...gate.Option) []gate.Option) ([]gate.Option, error)
	}{
		{
			name: "the working copy cannot detach",
			refuse: func(_ *testing.T, _ string, _ workingCopy, _ *gate.Gate,
				opts func(...gate.Option) []gate.Option) ([]gate.Option, error) {
				// A working copy that cannot give the remote up is refused
				// after the gate has been obtained. The product's own working
				// copy can, so this is the one case that needs an opener.
				return opts(gate.WithOpener(attachedOpener)), gate.ErrDetachUnsupported
			},
		},
		{
			name: "the gate carries no record",
			refuse: func(t *testing.T, _ string, _ workingCopy, g *gate.Gate,
				opts func(...gate.Option) []gate.Option) ([]gate.Option, error) {
				removeRecord(t, g.Repository())
				return opts(), gate.ErrNotAGate
			},
		},
		{
			name: "the record cannot be read",
			refuse: func(t *testing.T, _ string, _ workingCopy, g *gate.Gate,
				opts func(...gate.Option) []gate.Option) ([]gate.Option, error) {
				writeFile(t, filepath.Join(g.Repository(), recordName),
					fmt.Sprintf(`{"version":99,"id":%q,"workingPath":%q}`, g.ID(), g.WorkingPath()))
				return opts(), gate.ErrMalformedRecord
			},
		},
		{
			name: "another working copy is bound to the gate",
			refuse: func(t *testing.T, _ string, wc workingCopy, g *gate.Gate,
				opts func(...gate.Option) []gate.Option) ([]gate.Option, error) {
				duplicate := filepath.Join(filepath.Dir(wc.path), "copy")
				copyTree(t, wc.path, duplicate)
				writeFile(t, filepath.Join(g.Repository(), recordName),
					fmt.Sprintf(`{"version":1,"id":%q,"workingPath":%q}`, g.ID(), resolved(t, duplicate)))
				return opts(), gate.ErrGateClaimed
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wc := newWorkingCopy(t)
			home, opts := newHome(t)
			g, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command}, opts()...)
			if err != nil {
				t.Fatalf("Initialize: %v", err)
			}
			rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "main")

			// The damage TestInitializeRepairsADamagedGate inflicts, which
			// this package treats as an ordinary thing to repair.
			if err := os.Remove(filepath.Join(g.Repository(), "hooks", gate.AdmissionHook)); err != nil {
				t.Fatalf("remove the admission hook: %v", err)
			}

			removeOpts, want := c.refuse(t, home, wc, g, opts)
			if err := gate.Remove(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path}, removeOpts...); !errors.Is(err, want) {
				t.Fatalf("Remove error = %v, want %v", err, want)
			}

			// The gate is still there, and it takes no push now.
			commitMore(t, wc, "after the refusal\n")
			before := refs(t, g.Repository())
			out, pushErr := tryRawGit(wc.path, "push", gate.RemoteName, "main")
			if pushErr == nil {
				t.Fatalf("the gate accepted a push after a refused removal:\n%s", out)
			}
			if got := refs(t, g.Repository()); !equal(got, before) {
				t.Fatalf("the refused push moved references in the gate: %v, want %v", got, before)
			}
		})
	}
}

// namesInitializing and namesDetaching read which recovery a refusal instructs.
// A refusal's text is this package's operator-facing output and the action it
// names is part of that contract, so a test may read it; what these do not do
// is treat the rest of the sentence as a contract, which is why they look for
// the instructed action alone.
func namesInitializing(err error, workingPath string) bool {
	return strings.Contains(err.Error(), "initializing "+workingPath)
}

// namesRemoving reads whether a refusal instructs removing a particular file,
// which is the action ErrMalformedRecord has to name for the working copy the
// gate is filed under, because for that reader it is the only one that opens
// either operation from that state.
func namesRemoving(err error, path string) bool {
	return strings.Contains(strings.ToLower(err.Error()), "removing "+strings.ToLower(path))
}

func namesDetaching(err error) bool {
	// Lowercased because the same instruction opens a sentence in one refusal
	// and sits mid-sentence in another, and which it is says nothing about
	// whether the action was named.
	return strings.Contains(strings.ToLower(err.Error()), "removing the "+gate.RemoteName+" remote here")
}

// TestASealThatFailsIsReportedEvenBehindARefusal is the other half of the
// invariant a refusal already carries. Obtaining a gate seals one that has no
// admission hook, and errors.go states that as the one write every refusal
// makes, so a refusal returned alone is a statement that the seal happened.
//
// The arms are the same refusal over the same damage, differing only in
// whether the seal can be written. The first is what makes the others mean
// something: it shows the refusal is reached and the seal is what closes the
// gate, so the others are not passing over a fixture that never sealed at all.
//
// The two failing arms take the seal down at different steps, because which
// step fails is a host's choice rather than this package's. Where the hooks
// directory is a file, one host refuses to inspect the hook name below it and
// another reports that name as merely absent and refuses to create the
// directory instead; the second arm reaches that later step wherever it runs,
// by putting a link to nothing where the directory belongs, so the step is
// asked for the hook's name on a host where the first arm never gets there.
// What the operator is owed is the same from both, so both are asked for it.
func TestASealThatFailsIsReportedEvenBehindARefusal(t *testing.T) {
	gitEnvironment(t)
	command, _ := recorderCommand(t, 0)

	cases := []struct {
		name string
		// skip declines an arm whose damage this host cannot inflict.
		skip func(t *testing.T)
		// damage takes the gate's admission hook away, and decides whether a
		// seal can be written in its place.
		damage    func(t *testing.T, repo string)
		wantSeal  bool
		wantNamed bool
	}{
		{
			name: "the seal is written",
			damage: func(t *testing.T, repo string) {
				if err := os.Remove(filepath.Join(repo, "hooks", gate.AdmissionHook)); err != nil {
					t.Fatalf("remove the admission hook: %v", err)
				}
			},
			wantSeal: true,
		},
		{
			name: "the hooks directory is a file",
			damage: func(t *testing.T, repo string) {
				dir := filepath.Join(repo, "hooks")
				if err := os.RemoveAll(dir); err != nil {
					t.Fatalf("remove %s: %v", dir, err)
				}
				// A file where the hooks directory belongs leaves the seal
				// nowhere to write: the name it would fill has no directory
				// above it.
				writeFile(t, dir, "not a directory\n")
			},
			wantNamed: true,
		},
		{
			name: "the hooks directory cannot be created",
			skip: func(t *testing.T) {
				if runtime.GOOS == "windows" {
					t.Skip("creating a symbolic link needs a privilege this process may not hold on this host, and the arm above already takes the seal down here")
				}
			},
			damage: func(t *testing.T, repo string) {
				dir := filepath.Join(repo, "hooks")
				if err := os.RemoveAll(dir); err != nil {
					t.Fatalf("remove %s: %v", dir, err)
				}
				// A link to nothing is the arrangement that gets the seal past
				// inspecting the hook, which resolves to nothing and reads as
				// absent, and stops it at creating the directory, which cannot
				// be created over the link. That is the order the arm above
				// takes on a host that reports a name under a file as merely
				// absent, and the step that fails there names no hook of its
				// own.
				if err := os.Symlink(filepath.Join(repo, "nowhere"), dir); err != nil {
					t.Fatalf("link %s: %v", dir, err)
				}
			},
			wantNamed: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.skip != nil {
				c.skip(t)
			}
			wc := newWorkingCopy(t)
			home, opts := newHome(t)
			spec := gate.Spec{Home: home, WorkingPath: wc.path, Command: command}
			g, err := gate.Initialize(ctx(t), spec, opts()...)
			if err != nil {
				t.Fatalf("Initialize: %v", err)
			}

			// A copy of the working copy inherits the remote, so the gate is
			// somebody else's and the removal refuses.
			duplicate := filepath.Join(filepath.Dir(wc.path), "copy")
			copyTree(t, wc.path, duplicate)
			writeFile(t, filepath.Join(g.Repository(), recordName),
				fmt.Sprintf(`{"version":1,"id":%q,"workingPath":%q}`, g.ID(), resolved(t, duplicate)))
			c.damage(t, g.Repository())

			err = gate.Remove(ctx(t), spec, opts()...)
			if !errors.Is(err, gate.ErrGateClaimed) {
				t.Fatalf("Remove error = %v, want ErrGateClaimed", err)
			}
			admission := filepath.Join(g.Repository(), "hooks", gate.AdmissionHook)
			if named := strings.Contains(err.Error(), admission); named != c.wantNamed {
				t.Fatalf("the refusal names %s = %v, want %v; the error was %v",
					admission, named, c.wantNamed, err)
			}
			if _, statErr := os.Stat(admission); c.wantSeal != (statErr == nil) {
				t.Fatalf("an admission hook at %s = %v, want %v", admission, statErr == nil, c.wantSeal)
			}
		})
	}
}
