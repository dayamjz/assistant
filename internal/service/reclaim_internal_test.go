package service

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// A cancelled run's record moves immediately and its isolated copy does not:
// the copy outlives the segment still executing a stage body in it, and is
// given back once that segment ends.
//
// The two halves are one promise each. The record moving before cancel
// answers is what the status path owes a person: cancelling does not block on
// a segment that may be wedged. The copy waiting is what the segment is owed:
// a worktree removed from under a body mid-read is PRD section 11's reaping
// hazard arriving through the filesystem, and nothing in this build reaps.
//
// The body ignores its context until the test lets it out, which is what
// makes the window deterministic rather than raced: a body between
// cancellation points has not seen the signal, and the middle assertions run
// while it provably has not. The final assertion is the liveness half - a
// reclaim deferred to a moment that never comes would pass the middle ones -
// and awaitSegment returns only after the segment's own carryOn has run, so
// the check does not race the removal it looks for.
//
// P6 is cited because the direction this pins is lost work: a reclaim riding
// the status move removes the directory while a body is inside it.
func TestACancelledRunsCopyOutlivesItsSegment(t *testing.T) {
	principles.Cite(t, principles.P6)

	held := newHeldService(t)
	held.ignoreCancel = true
	record := held.begin(t)

	path := held.service.home.Worktree(record.RepositoryID, record.ID)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the run's isolated copy is not standing while its segment is inside a stage body: %v", err)
	}

	if _, err := held.service.cancel(t.Context(), machine.CancelRequest{Run: record.ID}); err != nil {
		t.Fatalf("ending the run: %v", err)
	}
	if got := held.status(t, record.ID); got != store.RunTerminated {
		t.Fatalf("the cancelled run is recorded as %s before its segment has ended, want terminated", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the copy was given back while a segment was still executing a stage body in it: %v", err)
	}

	held.let()
	held.awaitSegment(t)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the copy of the cancelled run was not given back once its segment ended: %v", err)
	}
}

// A superseded run gets the same treatment through the same seam: the push
// moves the displaced record immediately, and the displaced run's copy
// outlives the segment still executing in it.
//
// claimPush is driven directly, the way its own caller reaches it, because it
// is the one place a push displaces a run and it ends that run through the
// same endAndReclaim a cancel uses. Fixing one caller and leaving its twin is
// the easy defect here, so the twin has a control of its own.
//
// The reclaim at the end also stands on the take being commit-keyed: the
// superseding take anchors the new commit under a new name, so the reference
// containing the displaced copy's head is still in the gate and the removal
// is not refused for unreachable work.
func TestASupersededRunsCopyOutlivesItsSegment(t *testing.T) {
	principles.Cite(t, principles.P6)

	held := newHeldService(t)
	held.ignoreCancel = true
	record := held.begin(t)
	path := held.service.home.Worktree(record.RepositoryID, record.ID)

	// The push's commit has to be one the gate holds, so the subject's branch
	// is advanced and taken, which is the state a new push leaves behind.
	if err := os.WriteFile(filepath.Join(held.subject.workingPath, "next.txt"), []byte("next\n"), 0o600); err != nil {
		t.Fatalf("advancing the subject: %v", err)
	}
	rawGit(t, held.subject.workingPath, "add", "-A")
	rawGit(t, held.subject.workingPath, "commit", "--quiet", "-m", "the push that supersedes")
	spec := gate.Spec{Home: held.service.home.Root(), WorkingPath: held.subject.workingPath}
	newHead, err := gate.TakeBranch(t.Context(), spec, "main", gate.WithIndex(held.service.store))
	if err != nil {
		t.Fatalf("taking the advanced branch: %v", err)
	}

	built, err := held.service.driverFor(t.Context())
	if err != nil {
		t.Fatalf("building the driver: %v", err)
	}
	fresh, superseded, err := held.service.claimPush(t.Context(), built, record.RepositoryID, record.Branch, newHead, "")
	if err != nil {
		t.Fatalf("the push could not displace the run: %v", err)
	}
	if superseded != record.ID {
		t.Fatalf("the push displaced %q, want the branch's run %s", superseded, record.ID)
	}
	if fresh.ID == record.ID {
		t.Fatal("the push reused the displaced run's record")
	}

	if got := held.status(t, record.ID); got != store.RunTerminated {
		t.Fatalf("the displaced run is recorded as %s before its segment has ended, want terminated", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the displaced run's copy was given back while a segment was still executing in it: %v", err)
	}

	held.let()
	held.awaitSegment(t)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the displaced run's copy was not given back once its segment ended: %v", err)
	}
}
