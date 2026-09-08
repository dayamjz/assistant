package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/machine"
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
//
// The shape driven here is the one the product will actually produce. A commit
// made in the copy that no reference in the gate contains is what the rebase
// stage leaves behind, so a run ended between rebase and push meets exactly
// this, and this is the test that says somebody is told about it.
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

// TestARunWhoseCopyCannotBeMadeIsFailedRatherThanLeftRunning holds the seam to
// the ending it owes.
//
// runs.Start moves the record to running before the copy is made, so a
// creation that fails and returns only an error leaves a run recorded as
// running with no copy and nothing advancing it. That is the stall PRD section
// 9 calls worse than an error, and carryOn cannot reach it because no segment
// ever began.
//
// The copy is made impossible by putting a file where this repository's copies
// have to live, so nothing can be created beneath it. The run's identifier is
// minted inside create, so the obstruction is the parent rather than the
// copy's own path.
func TestARunWhoseCopyCannotBeMadeIsFailedRatherThanLeftRunning(t *testing.T) {
	held := newHeldService(t)
	perRepository := filepath.Dir(held.service.home.Worktree("subject", "unused"))
	if err := os.MkdirAll(filepath.Dir(perRepository), 0o700); err != nil {
		t.Fatalf("making the worktrees root: %v", err)
	}
	if err := os.WriteFile(perRepository, []byte("not a directory\n"), 0o600); err != nil {
		t.Fatalf("blocking the copy's parent: %v", err)
	}

	if _, err := held.service.start(t.Context(), machine.StartRequest{
		Working:        machine.Working{WorkingPath: held.subject.workingPath},
		Intent:         "a run whose copy cannot be made",
		IntentSupplied: true,
	}); err == nil {
		t.Fatal("start reported success though the isolated copy could not be made")
	}

	records, err := held.service.store.RunsForRepository(t.Context(), "subject")
	if err != nil {
		t.Fatalf("reading the repository's runs: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("the repository holds %d runs, want the one that was attempted", len(records))
	}
	if got := records[0].Status; got == store.RunRunning || got == store.RunPending {
		t.Fatalf("the run is recorded as %s with no copy and nothing advancing it, which is the "+
			"stall a person cannot tell from work in progress", got)
	}
}

// TestTheBareCommandAttachesToTheRunOfARewrittenBranch is PRD section 9's
// promise that the bare command attaches to the branch's active run, held
// against the history rewrite that used to defeat it.
//
// Every invocation puts the branch in the gate before it decides create from
// attach, so the attach path takes the branch too. That is only safe because
// gate.TakeBranch writes a reference internal/gate owns: a take that moved the
// gate's own copy of the branch would be refused as a non-fast-forward here,
// and the command would then neither start a run nor report the one already
// running.
//
// The rewrite is an amend, which is the ordinary way a branch stops
// fast-forwarding in this product: a fix round rewrites a commit.
func TestTheBareCommandAttachesToTheRunOfARewrittenBranch(t *testing.T) {
	held := newHeldService(t)
	record := held.begin(t)
	<-held.inside

	rawGit(t, held.subject.workingPath, "commit", "--quiet", "--amend", "-m", "rewritten after the run began")
	rewritten := rawGit(t, held.subject.workingPath, "rev-parse", "HEAD")
	if rewritten == held.subject.head {
		t.Fatal("the amend did not move the branch, so a take would still fast-forward")
	}

	attached, err := held.service.start(t.Context(), machine.StartRequest{
		Working: machine.Working{WorkingPath: held.subject.workingPath},
	})
	if err != nil {
		t.Fatalf("the bare command could not attach to the run this branch already has: %v", err)
	}
	if attached.Record.ID != record.ID {
		t.Errorf("attached to run %s, want the run the branch already had, %s", attached.Record.ID, record.ID)
	}
	if attached.Record.SubmittedHead != held.subject.head {
		t.Errorf("the attached run now validates %s, want the commit it was started against, %s",
			attached.Record.SubmittedHead, held.subject.head)
	}
}

// TestTheBareCommandStartsARunOnARewrittenBranch is the create half of the
// same case, and it is the one the attach test above cannot reach.
//
// A branch whose run has finished and whose history was then rewritten has no
// active run to attach to, so the command must create one - against the commit
// standing in the working copy now, not the one the previous run submitted.
// This is what a take into the gate's own copy of the branch made impossible:
// the fetch was refused as a non-fast-forward and the command had no route at
// all.
func TestTheBareCommandStartsARunOnARewrittenBranch(t *testing.T) {
	held := newHeldService(t)
	first := held.begin(t)
	<-held.inside
	held.let()
	held.awaitSegment(t)
	if _, err := held.service.cancel(t.Context(), machine.CancelRequest{Run: first.ID}); err != nil {
		t.Fatalf("ending the first run: %v", err)
	}

	if err := os.WriteFile(filepath.Join(held.subject.workingPath, "rewritten.txt"), []byte("after\n"), 0o600); err != nil {
		t.Fatalf("writing the rewritten content: %v", err)
	}
	rawGit(t, held.subject.workingPath, "add", "-A")
	rawGit(t, held.subject.workingPath, "commit", "--quiet", "--amend", "-m", "rewritten after the run ended")
	rewritten := rawGit(t, held.subject.workingPath, "rev-parse", "HEAD")
	if rewritten == held.subject.head {
		t.Fatal("the amend did not move the branch, so a take would still fast-forward")
	}

	started, err := held.service.start(t.Context(), machine.StartRequest{
		Working:        machine.Working{WorkingPath: held.subject.workingPath},
		Intent:         "validate the rewritten branch",
		IntentSupplied: true,
	})
	if err != nil {
		t.Fatalf("the bare command could not start a run on a locally rewritten branch: %v", err)
	}
	if started.Record.ID == first.ID {
		t.Fatal("the command attached to the finished run rather than starting a new one")
	}
	if started.Record.SubmittedHead != rewritten {
		t.Errorf("the new run validates %s, want the commit the branch now stands at, %s",
			started.Record.SubmittedHead, rewritten)
	}
}

// TestRecoveryDoesNotReportTheCopyOfAHeldRunAsKept keeps the surface the
// reclaim's refusal is carried on worth reading.
//
// reclaimCopy takes no error back to its caller, so the service log is the
// whole of what a refusal amounts to. A held run keeping its copy is the
// designed state - it is waiting on an answer the copy resumes into - so
// asking about one at every open would put a "was kept" line on that log for
// every held run every time the service starts, and the one line that means
// work would have been lost would be read as another of those.
//
// The positive control is in the same run of recovery: a finished run whose
// copy genuinely cannot be given back still reaches the log, so a recovery
// that stopped reporting anything fails here rather than passing.
func TestRecoveryDoesNotReportTheCopyOfAHeldRunAsKept(t *testing.T) {
	held := newHeldService(t)
	built, err := held.service.driverFor(t.Context())
	if err != nil {
		t.Fatalf("building the driver: %v", err)
	}

	waiting, err := held.service.create(t.Context(), run{
		repository: "subject",
		branch:     "waiting",
		head:       held.subject.head,
		intent:     "waiting on a person",
		source:     intentSourceSupplied,
		supplied:   true,
	})
	if err != nil {
		t.Fatalf("recording the run that waits: %v", err)
	}
	if _, err := built.runs.Start(t.Context(), waiting.ID); err != nil {
		t.Fatalf("starting the run that waits: %v", err)
	}
	if _, err := built.runs.Hold(t.Context(), waiting.ID); err != nil {
		t.Fatalf("holding the run that waits: %v", err)
	}
	makeCopy(t, held, waiting.ID)

	finished, err := held.service.create(t.Context(), run{
		repository: "subject",
		branch:     "finished",
		head:       held.subject.head,
		intent:     "finished with work only its copy holds",
		source:     intentSourceSupplied,
		supplied:   true,
	})
	if err != nil {
		t.Fatalf("recording the finished run: %v", err)
	}
	if _, err := built.runs.Terminate(t.Context(), finished.ID); err != nil {
		t.Fatalf("ending the finished run: %v", err)
	}
	strandedIn := makeCopy(t, held, finished.ID)
	if err := os.WriteFile(filepath.Join(strandedIn, "only-here.txt"), []byte("work\n"), 0o600); err != nil {
		t.Fatalf("writing work into the finished run's copy: %v", err)
	}
	rawGit(t, strandedIn, "add", "-A")
	rawGit(t, strandedIn, "commit", "--quiet", "-m", "work only this copy references")

	held.service.recover(t.Context())

	logged := readServiceLog(t, held.service)
	if strings.Contains(logged, waiting.ID) {
		t.Errorf("recovery reported the copy of a held run, which is the copy that is meant to "+
			"stay:\n%s", logged)
	}
	if !strings.Contains(logged, finished.ID) {
		t.Errorf("recovery did not report the copy it could not give back, so this test would "+
			"pass against a recovery that reports nothing:\n%s", logged)
	}
}

// makeCopy gives a run the isolated copy it would have been given, so that
// what recovery decides about it is decided about a directory that is there.
func makeCopy(t *testing.T, held *heldService, runID string) string {
	t.Helper()
	path := held.service.home.Worktree("subject", runID)
	spec := gate.Spec{Home: held.service.home.Root(), WorkingPath: held.subject.workingPath}
	if err := gate.AddCopy(t.Context(), spec, path, held.subject.head, gate.WithIndex(held.service.store)); err != nil {
		t.Fatalf("creating the copy for run %s: %v", runID, err)
	}
	return path
}
