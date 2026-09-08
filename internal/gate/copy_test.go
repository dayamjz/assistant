package gate_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/vcs"
)

// gatedCopy initializes a gate for a fresh working copy, pushes its branch so
// the gate references the work, and returns the gate, the spec it was made
// from, and where a run's copy would go.
//
// Nothing here composes a path under the home the way internal/home does: the
// copy's location is the caller's, so these tests name one of their own and
// prove the operations act on whatever they are given.
func gatedCopy(t *testing.T) (*gate.Gate, gate.Spec, string, string) {
	t.Helper()
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
	head := strings.TrimSpace(rawGit(t, wc.path, "rev-parse", "HEAD"))

	return g, spec, filepath.Join(t.TempDir(), "run-copy"), head
}

// TestACopyIsALinkedWorktreeDetachedAtTheCommit is the shape PRD section 8
// asks a run's isolated copy to have.
//
// A clone would answer the same for "is the code there" while sharing no
// object store, so this asserts the two properties that distinguish a linked
// worktree: the commit checked out is the one asked for, and the gate itself
// can resolve it, which is what makes RemoveCopy's reachability question
// answerable later.
func TestACopyIsALinkedWorktreeDetachedAtTheCommit(t *testing.T) {
	g, spec, path, head := gatedCopy(t)

	if err := gate.AddCopy(ctx(t), spec, path, head, indexOptions(t)()...); err != nil {
		t.Fatalf("AddCopy: %v", err)
	}

	copyOf, err := vcs.OpenWorktree(ctx(t), path)
	if err != nil {
		t.Fatalf("opening the copy that was created: %v", err)
	}
	at, err := copyOf.ResolveCommit(ctx(t), "HEAD")
	if err != nil {
		t.Fatalf("reading the copy's head: %v", err)
	}
	if at != head {
		t.Errorf("the copy is at %s, want the commit it was created from, %s", at, head)
	}
	if _, err := copyOf.HeadBranch(ctx(t)); !errors.Is(err, vcs.ErrDetachedHead) {
		t.Errorf("the copy's head is on a branch (%v), want detached so the run moving it "+
			"changes nothing the gate advertises", err)
	}
	// The gate resolving the copy's head is what a shared object store buys,
	// and what a clone would not give.
	gateRepo, err := vcs.OpenBare(ctx(t), g.Repository())
	if err != nil {
		t.Fatalf("opening the gate repository: %v", err)
	}
	if _, err := gateRepo.ResolveCommit(ctx(t), at); err != nil {
		t.Errorf("the gate cannot resolve the copy's head, so the copy is not sharing its "+
			"object store: %v", err)
	}
}

// TestACopyWhoseWorkTheGateReferencesIsGivenBack is the ordinary end of a run.
func TestACopyWhoseWorkTheGateReferencesIsGivenBack(t *testing.T) {
	_, spec, path, head := gatedCopy(t)
	if err := gate.AddCopy(ctx(t), spec, path, head, indexOptions(t)()...); err != nil {
		t.Fatalf("AddCopy: %v", err)
	}

	if err := gate.RemoveCopy(ctx(t), spec, path, indexOptions(t)()...); err != nil {
		t.Fatalf("RemoveCopy on a copy the gate still references: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the copy is still at %s after being given back (%v)", path, err)
	}
}

// TestACopyHoldingWorkTheGateDoesNotReferenceIsRefused is the P6 refusal this
// operation exists for.
//
// The copy is checked out detached, so a commit made in it is referenced by
// the worktree's own head and by nothing else. Removing it would leave that
// work referenced by nothing, which is the loss the refusal prevents.
func TestACopyHoldingWorkTheGateDoesNotReferenceIsRefused(t *testing.T) {
	principles.Cite(t, principles.P6)
	_, spec, path, head := gatedCopy(t)
	if err := gate.AddCopy(ctx(t), spec, path, head, indexOptions(t)()...); err != nil {
		t.Fatalf("AddCopy: %v", err)
	}
	writeFile(t, filepath.Join(path, "worked.txt"), "work done inside the copy\n")
	rawGit(t, path, "add", "worked.txt")
	rawGit(t, path, "commit", "--quiet", "-m", "work only this copy references")
	made := strings.TrimSpace(rawGit(t, path, "rev-parse", "HEAD"))

	err := gate.RemoveCopy(ctx(t), spec, path, indexOptions(t)()...)
	if !errors.Is(err, gate.ErrWorkUnreachable) {
		t.Fatalf("RemoveCopy = %v, want ErrWorkUnreachable", err)
	}
	if !strings.Contains(err.Error(), made) {
		t.Errorf("the refusal does not name the commit that would be lost (%s): %v", made, err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("the copy was removed despite the refusal: %v", statErr)
	}
}

// TestACopyHoldingUncommittedWorkIsRefusedByGit is the fourth refusal, and it
// is git's rather than this package's.
//
// It is also the test that fails if RemoveCopy is ever changed to
// RemoveWorktreeDiscardingChanges: that variant removes a worktree holding
// modified or untracked files and loses them, which is the one behaviour this
// operation must never have. That was checked by making the swap and watching
// this fail.
func TestACopyHoldingUncommittedWorkIsRefusedByGit(t *testing.T) {
	principles.Cite(t, principles.P6)
	_, spec, path, head := gatedCopy(t)
	if err := gate.AddCopy(ctx(t), spec, path, head, indexOptions(t)()...); err != nil {
		t.Fatalf("AddCopy: %v", err)
	}
	uncommitted := filepath.Join(path, "in-progress.txt")
	writeFile(t, uncommitted, "work that was never committed\n")

	if err := gate.RemoveCopy(ctx(t), spec, path, indexOptions(t)()...); err == nil {
		t.Fatal("RemoveCopy removed a copy holding uncommitted work, so the discarding " +
			"variant is in use and that work is gone")
	}
	if _, err := os.Stat(uncommitted); err != nil {
		t.Errorf("the uncommitted work is gone: %v", err)
	}
}

// TestAReferenceOnANonCommitDoesNotMakeReclaimUnanswerable keeps one odd
// reference in the gate from wedging every reclaim of the repository.
//
// A tag on a blob is pushable - admission reads reference update lines, and
// object types are nobody's to check - and vcs.Ref.Commit carries it unpeeled,
// so the reachability walk meets an object that does not resolve to a commit.
// Treating that as "could not be determined" would refuse this reclaim and
// every later one, logged on every service open, for as long as the tag
// stands. A reference that does not reach a commit cannot contain one, so the
// walk passes it over and the answer stays the honest refusal: the work is
// unreachable, and pushing it is what makes the copy removable.
//
// The copy holds a commit no reference contains, so the walk reaches the odd
// tag rather than returning at a containing reference sorted before it; the
// assertion discriminates because before the skip existed this returned the
// unverifiable error instead of ErrWorkUnreachable.
func TestAReferenceOnANonCommitDoesNotMakeReclaimUnanswerable(t *testing.T) {
	_, spec, path, head := gatedCopy(t)

	writeFile(t, filepath.Join(spec.WorkingPath, "odd.txt"), "not a commit\n")
	blob := strings.TrimSpace(rawGit(t, spec.WorkingPath, "hash-object", "-w", "odd.txt"))
	rawGit(t, spec.WorkingPath, "tag", "-a", "-m", "a tag on a blob", "oddity", blob)
	rawGit(t, spec.WorkingPath, "push", "--quiet", gate.RemoteName, "refs/tags/oddity")

	if err := gate.AddCopy(ctx(t), spec, path, head, indexOptions(t)()...); err != nil {
		t.Fatalf("AddCopy: %v", err)
	}
	writeFile(t, filepath.Join(path, "worked.txt"), "work only this copy references\n")
	rawGit(t, path, "add", "worked.txt")
	rawGit(t, path, "commit", "--quiet", "-m", "unreferenced work")

	err := gate.RemoveCopy(ctx(t), spec, path, indexOptions(t)()...)
	if !errors.Is(err, gate.ErrWorkUnreachable) {
		t.Fatalf("RemoveCopy = %v, want ErrWorkUnreachable: a reference that does not reach a "+
			"commit made the walk unanswerable instead of being passed over", err)
	}
}

// TestGivingBackACopyThatIsNotThereSucceeds keeps the refusals meaningful.
//
// Recovery gives back copies a dead service left, and a run whose copy was
// already removed is the outcome the caller wanted. Reporting that as a
// data-loss refusal would teach a reader to step past the one refusal that
// means it.
func TestGivingBackACopyThatIsNotThereSucceeds(t *testing.T) {
	_, spec, path, _ := gatedCopy(t)

	if err := gate.RemoveCopy(ctx(t), spec, path, indexOptions(t)()...); err != nil {
		t.Fatalf("RemoveCopy on a copy that is not there: %v", err)
	}
}
