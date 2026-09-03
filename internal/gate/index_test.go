package gate_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/store"
)

// TestEveryOperationRefusesWithoutAnOwnershipIndex is the requirement stated as
// a refusal rather than as a default. Whether another working copy is bound to a
// gate is what decides whether one may be adopted or deleted, and an operation
// that could not ask would have to guess. A guess that goes the permissive way
// deletes somebody's history, so there is no default to fall back to.
//
// The refusal comes before anything is opened, so it is also the one refusal
// here that touches nothing at all.
func TestEveryOperationRefusesWithoutAnOwnershipIndex(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home, opts := newHome(t)
	command, _ := recorderCommand(t, 0)
	spec := gate.Spec{Home: home, WorkingPath: wc.path, Command: command}

	if _, err := gate.Initialize(ctx(t), spec); !errors.Is(err, gate.ErrNoIndex) {
		t.Fatalf("Initialize without an index = %v, want ErrNoIndex", err)
	}
	if err := gate.Remove(ctx(t), spec, gate.WithOpener(detachingOpener)); !errors.Is(err, gate.ErrNoIndex) {
		t.Fatalf("Remove without an index = %v, want ErrNoIndex", err)
	}
	if _, err := os.Stat(filepath.Join(home, "repos")); !os.IsNotExist(err) {
		t.Fatalf("the refused operations created a repositories directory (stat error %v)", err)
	}
	if url, ok := remoteURL(t, wc.path, gate.RemoteName); ok {
		t.Fatalf("the refused operations pointed the %s remote at %q", gate.RemoteName, url)
	}

	// The same calls with an index succeed, so what is checked above is the
	// missing index rather than a fixture that could not have worked.
	if _, err := gate.Initialize(ctx(t), spec, opts()...); err != nil {
		t.Fatalf("Initialize with an index: %v", err)
	}
	if err := gate.Remove(ctx(t), spec, opts(gate.WithOpener(detachingOpener))...); err != nil {
		t.Fatalf("Remove with an index: %v", err)
	}
}

// TestInitializeRecordsTheBindingAndRemoveGivesItUp is the index's own contract
// as an operation on a gate leaves it.
//
// Both halves are checked in one test on purpose. A binding written and never
// given up leaves the home saying a deleted gate is still somebody's, and a
// binding given up but never written leaves the next working copy to land on a
// path with nothing to be refused by. Which of the two is forgotten is the
// question this package has got wrong before.
func TestInitializeRecordsTheBindingAndRemoveGivesItUp(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home, index, opts := homeWithIndex(t)
	command, _ := recorderCommand(t, 0)
	spec := gate.Spec{Home: home, WorkingPath: wc.path, Command: command}

	g, err := gate.Initialize(ctx(t), spec, opts()...)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if got, want := boundWorkingPaths(t, index, g.ID()), []string{g.WorkingPath()}; !equal(got, want) {
		t.Fatalf("the index records %v as bound to the gate, want %v", got, want)
	}

	// Initializing again is the ordinary repeated case and leaves one binding,
	// not two.
	if _, err := gate.Initialize(ctx(t), spec, opts()...); err != nil {
		t.Fatalf("Initialize again: %v", err)
	}
	if got, want := boundWorkingPaths(t, index, g.ID()), []string{g.WorkingPath()}; !equal(got, want) {
		t.Fatalf("a repeated initialization left %v bound, want %v", got, want)
	}

	if err := gate.Remove(ctx(t), spec, opts(gate.WithOpener(detachingOpener))...); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got := boundWorkingPaths(t, index, g.ID()); len(got) != 0 {
		t.Fatalf("the removed gate is still recorded as bound to %v", got)
	}
}

// TestAMovedWorkingCopyIsRecordedWhereItNowStands is what makes a moved working
// copy findable at all. The gate keeps the identifier it was created with, so
// nothing about the gate's name says where its working copy went; the binding
// written at the new path is the only thing that does.
//
// The binding at the path the working copy left is kept rather than pruned. It
// is not believed on its own, because every candidate is checked against the
// working copy actually standing there, and a working copy that is briefly
// unreadable is exactly the one whose evidence must not be thrown away for
// looking stale.
func TestAMovedWorkingCopyIsRecordedWhereItNowStands(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home, index, opts := homeWithIndex(t)
	command, _ := recorderCommand(t, 0)

	original, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command}, opts()...)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	moved := filepath.Join(filepath.Dir(wc.path), "moved")
	if err := os.Rename(wc.path, moved); err != nil {
		t.Fatalf("move the working copy: %v", err)
	}
	reattached, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: moved, Command: command}, opts()...)
	if err != nil {
		t.Fatalf("Initialize after the move: %v", err)
	}
	if reattached.ID() != original.ID() {
		t.Fatalf("the gate is filed under %q after the move, want %q", reattached.ID(), original.ID())
	}

	want := []string{original.WorkingPath(), reattached.WorkingPath()}
	sort.Strings(want)
	if got := boundWorkingPaths(t, index, original.ID()); !equal(got, want) {
		t.Fatalf("the index records %v as bound to the gate, want %v", got, want)
	}
}

// unbindableIndex is the ownership index with its unbind broken and nothing
// else changed. The two accessors a resolution reads go to the live store, and
// the write a removal makes goes to a second store of the same kind that has
// been closed, so the failure it produces is one internal/store actually
// returns rather than a shape invented here.
type unbindableIndex struct {
	*store.Store
	shut *store.Store
}

func (i unbindableIndex) UnbindGate(ctx context.Context, workingPath string) error {
	return i.shut.UnbindGate(ctx, workingPath)
}

var _ gate.Index = unbindableIndex{}

// TestAFailedUnbindLeavesAReattachedGateFindable is PRD principle P6 at the one
// sequence where a removal can lose a repository without deleting it.
//
// A reattached gate is filed under the identifier its working copy's path
// hashed to before the move, so the working copy's assistant remote is the only
// handle on it. A removal that gave that remote up and then failed to unbind
// would leave the repository named by nothing, and nothing in this package
// scans the home for a gate nobody names, so the history in it would be
// unreachable.
func TestAFailedUnbindLeavesAReattachedGateFindable(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home, index, opts := homeWithIndex(t)
	command, _ := recorderCommand(t, 0)

	original, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command}, opts()...)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	moved := filepath.Join(filepath.Dir(wc.path), "moved")
	if err := os.Rename(wc.path, moved); err != nil {
		t.Fatalf("move the working copy: %v", err)
	}
	spec := gate.Spec{Home: home, WorkingPath: moved, Command: command}
	reattached, err := gate.Initialize(ctx(t), spec, opts()...)
	if err != nil {
		t.Fatalf("Initialize after the move: %v", err)
	}
	if !reattached.Reattached() || reattached.ID() != original.ID() {
		t.Fatalf("the gate after the move is %q reattached=%v, want %q reattached=true",
			reattached.ID(), reattached.Reattached(), original.ID())
	}
	// The history the gate holds, which is what a lost repository loses.
	rawGit(t, moved, "push", "--quiet", gate.RemoteName, "main")
	recorded := refs(t, reattached.Repository())

	shut := openIndex(t)
	if err := shut.Close(); err != nil {
		t.Fatalf("close the second index: %v", err)
	}
	broken := unbindableIndex{Store: index, shut: shut}
	if err := gate.Remove(ctx(t), spec, gate.WithIndex(broken), gate.WithOpener(detachingOpener)); err == nil {
		t.Fatal("Remove reported success over an index that cannot unbind")
	}

	// The gate is still there and the working copy still names it, so the
	// removal can be run again once the index works.
	if _, err := os.Stat(reattached.Repository()); err != nil {
		t.Fatalf("the failed removal deleted %s anyway: %v", reattached.Repository(), err)
	}
	if url, ok := remoteURL(t, moved, gate.RemoteName); !ok || url != reattached.Repository() {
		t.Fatalf("the %s remote of the moved working copy is %q, want %q; the gate is now named by nothing",
			gate.RemoteName, url, reattached.Repository())
	}

	// Initializing again finds the same gate through that remote rather than
	// creating an empty one at the identifier this path hashes to.
	again, err := gate.Initialize(ctx(t), spec, opts()...)
	if err != nil {
		t.Fatalf("Initialize after the failed removal: %v", err)
	}
	if again.ID() != original.ID() {
		t.Fatalf("the gate is now %q, want %q: the failed removal orphaned the original", again.ID(), original.ID())
	}
	if got := refs(t, again.Repository()); !equal(got, recorded) {
		t.Fatalf("the gate holds %v, want %v", got, recorded)
	}
}
