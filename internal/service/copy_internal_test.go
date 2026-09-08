package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// TestARunCannotBeRecordedAgainstACommitTheGateDoesNotHold is the invariant
// this change exists to make unrepresentable: the gate holds every commit
// under validation.
//
// It is asserted at create rather than at either caller because create is
// where a run record is written and all three paths that write one reach it -
// the bare command, a rerun, and a gate push. A check at the callers would be
// one a fourth path could be written without, which is how the two paths that
// exist today came to disagree.
//
// The commit used is a real one the gate genuinely does not have, made in the
// working copy after the branch was taken, rather than a string of zeroes: a
// well-formed commit that simply never reached the gate is the case that
// actually occurs, and a malformed one would be refused by anything.
func TestARunCannotBeRecordedAgainstACommitTheGateDoesNotHold(t *testing.T) {
	principles.Cite(t, principles.P1)
	held := newHeldService(t)

	if err := os.WriteFile(filepath.Join(held.subject.workingPath, "later.txt"), []byte("after\n"), 0o600); err != nil {
		t.Fatalf("writing a later file: %v", err)
	}
	rawGit(t, held.subject.workingPath, "add", "-A")
	rawGit(t, held.subject.workingPath, "commit", "--quiet", "-m", "never pushed to the gate")
	unpushed := rawGit(t, held.subject.workingPath, "rev-parse", "HEAD")
	if unpushed == held.subject.head {
		t.Fatal("the later commit is the one the gate already holds, so this checks nothing")
	}

	_, err := held.service.create(t.Context(), run{
		repository: "subject",
		branch:     "main",
		head:       unpushed,
		intent:     "validate a commit the gate never received",
		source:     intentSourceSupplied,
		supplied:   true,
	})
	if !errors.Is(err, ErrHeadNotInGate) {
		t.Fatalf("create = %v, want ErrHeadNotInGate", err)
	}
	if !strings.Contains(err.Error(), unpushed) {
		t.Errorf("the refusal does not name the commit it refused (%s): %v", unpushed, err)
	}
}

// TestARunIsRecordedAgainstACommitTheGateHolds is the other half, and without
// it the test above would pass against a create that refused everything.
func TestARunIsRecordedAgainstACommitTheGateHolds(t *testing.T) {
	held := newHeldService(t)

	record, err := held.service.create(t.Context(), run{
		repository: "subject",
		branch:     "main",
		head:       held.subject.head,
		intent:     "validate the commit the gate took",
		source:     intentSourceSupplied,
		supplied:   true,
	})
	if err != nil {
		t.Fatalf("create against the commit the gate holds: %v", err)
	}
	if record.SubmittedHead != held.subject.head {
		t.Errorf("the record validates %s, want the commit the gate holds, %s",
			record.SubmittedHead, held.subject.head)
	}
}

// TestARefusedReclaimReachesTheServiceLog is what keeps reclaimCopy's refusal
// from being worth nothing.
//
// reclaimCopy takes no error back to its caller: handling the refusal means
// the copy stays and somebody is told. That is only true if the telling
// arrives somewhere a person reads, so this drives a real refusal - a copy
// holding a commit no reference in the gate contains - and reads it back off
// the service log on disk.
//
// A refusal reported into something nobody reads is indistinguishable from no
// refusal at all, which is why this asserts the message arrived rather than
// asserting that reclaimCopy was called.
func TestARefusedReclaimReachesTheServiceLog(t *testing.T) {
	principles.Cite(t, principles.P6)
	held := newHeldService(t)
	record := store.Run{
		ID:            "run-with-unreferenced-work",
		RepositoryID:  "subject",
		Branch:        "main",
		SubmittedHead: held.subject.head,
		Status:        store.RunPassed,
	}

	path := held.service.home.Worktree(record.RepositoryID, record.ID)
	spec := gate.Spec{Home: held.service.home.Root(), WorkingPath: held.subject.workingPath}
	if err := gate.AddCopy(t.Context(), spec, path, held.subject.head, gate.WithIndex(held.service.store)); err != nil {
		t.Fatalf("creating the copy to be refused: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "only-here.txt"), []byte("work\n"), 0o600); err != nil {
		t.Fatalf("writing work into the copy: %v", err)
	}
	rawGit(t, path, "add", "-A")
	rawGit(t, path, "commit", "--quiet", "-m", "work only this copy references")
	stranded := rawGit(t, path, "rev-parse", "HEAD")

	held.service.reclaimCopy(t.Context(), record)

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the copy was removed despite holding work nothing else references: %v", err)
	}
	logged := readServiceLog(t, held.service)
	if !strings.Contains(logged, record.ID) {
		t.Errorf("the service log does not name the run whose copy was kept:\n%s", logged)
	}
	if !strings.Contains(logged, stranded) {
		t.Errorf("the service log does not name the commit that would have been lost (%s):\n%s",
			stranded, logged)
	}
}

// TestTheCopyOfARunStillRunningIsKept is the refusal reclaimCopy asks itself,
// as opposed to the ones internal/gate asks.
//
// A copy belongs to its run for as long as any move leads out of the status it
// is in, so a run that is still going keeps its copy even when everything
// about the copy would allow removal.
func TestTheCopyOfARunStillRunningIsKept(t *testing.T) {
	held := newHeldService(t)
	record := store.Run{
		ID:            "run-still-going",
		RepositoryID:  "subject",
		Branch:        "main",
		SubmittedHead: held.subject.head,
		Status:        store.RunRunning,
	}
	path := held.service.home.Worktree(record.RepositoryID, record.ID)
	spec := gate.Spec{Home: held.service.home.Root(), WorkingPath: held.subject.workingPath}
	if err := gate.AddCopy(t.Context(), spec, path, held.subject.head, gate.WithIndex(held.service.store)); err != nil {
		t.Fatalf("creating the copy: %v", err)
	}

	held.service.reclaimCopy(t.Context(), record)

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the copy of a run that is still running was removed: %v", err)
	}
	if logged := readServiceLog(t, held.service); !strings.Contains(logged, record.ID) {
		t.Errorf("the service log does not say the copy was kept:\n%s", logged)
	}
}

// readServiceLog reads what the service has written to the log PRD section 8
// places in the home, which is the surface reclaimCopy reports onto.
func readServiceLog(t *testing.T, s *Service) string {
	t.Helper()
	content, err := os.ReadFile(s.home.ServiceLog())
	if err != nil {
		t.Fatalf("reading the service log: %v", err)
	}
	return string(content)
}
