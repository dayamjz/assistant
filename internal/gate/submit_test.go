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

// takeable is a working copy with a gate, ready for TakeBranch: nothing has
// been pushed, because taking is what puts the branch in the gate.
func takeable(t *testing.T) (*gate.Gate, gate.Spec, func(...gate.Option) []gate.Option, workingCopy) {
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
	return g, spec, opts, wc
}

// TestABranchRewrittenLocallyIsStillTaken is the case a take into
// refs/heads/<branch> cannot serve.
//
// Rewriting a commit is what a fix round does to a branch, so a branch whose
// second take is not a descendant of its first is ordinary rather than exotic.
// A take that moved the gate's own copy of the branch would be refused by git
// for exactly that, leaving PRD section 9's bare command with no way to start
// a run on the branch at all.
//
// The reference is named for the commit it holds, so the second take adds a
// name rather than moving one, and the third assertion here is that: the
// earlier commit is still contained by a reference in the gate afterwards.
// That is the question RemoveCopy asks of a copy's head, asked the same way,
// so it fails against an anchor keyed by branch, which the second take would
// have moved off the first commit.
func TestABranchRewrittenLocallyIsStillTaken(t *testing.T) {
	principles.Cite(t, principles.P1)
	g, spec, opts, wc := takeable(t)

	first, err := gate.TakeBranch(ctx(t), spec, "main", opts()...)
	if err != nil {
		t.Fatalf("taking the branch the first time: %v", err)
	}
	if first != wc.commit {
		t.Fatalf("the first take reports %s, want the branch's commit %s", first, wc.commit)
	}

	writeFile(t, filepath.Join(wc.path, "file.txt"), "rewritten\n")
	rawGit(t, wc.path, "add", "-A")
	rawGit(t, wc.path, "commit", "--quiet", "--amend", "-m", "rewritten")
	rewritten := strings.TrimSpace(rawGit(t, wc.path, "rev-parse", "HEAD"))
	if rewritten == first {
		t.Fatal("the amend did not move the branch, so the second take would fast-forward")
	}
	if _, err := tryRawGit(wc.path, "merge-base", "--is-ancestor", first, rewritten); err == nil {
		t.Fatal("the rewritten commit still descends from the first, so this checks nothing")
	}

	second, err := gate.TakeBranch(ctx(t), spec, "main", opts()...)
	if err != nil {
		t.Fatalf("taking the branch after it was rewritten locally: %v", err)
	}
	if second != rewritten {
		t.Fatalf("the second take reports %s, want the rewritten commit %s", second, rewritten)
	}

	// The commit the take reports is the one a run validates, so the copy made
	// from it has to stand there and not at what the gate last held.
	path := filepath.Join(t.TempDir(), "run-copy")
	if err := gate.AddCopy(ctx(t), spec, path, second, opts()...); err != nil {
		t.Fatalf("creating a copy from the rewritten commit: %v", err)
	}
	copyOf, err := vcs.OpenWorktree(ctx(t), path)
	if err != nil {
		t.Fatalf("opening the copy: %v", err)
	}
	at, err := copyOf.ResolveCommit(ctx(t), "HEAD")
	if err != nil {
		t.Fatalf("reading the copy's head: %v", err)
	}
	if at != rewritten {
		t.Errorf("the copy stands at %s, want the newly taken commit %s", at, rewritten)
	}

	gateRepo, err := vcs.OpenBare(ctx(t), g.Repository())
	if err != nil {
		t.Fatalf("opening the gate repository: %v", err)
	}
	if !someRefContains(t, gateRepo, first) {
		t.Errorf("no reference in the gate contains %s after the branch was taken again: the "+
			"second take moved the anchor the first commit had rather than adding one", first)
	}
	if !someRefContains(t, gateRepo, rewritten) {
		t.Errorf("no reference in the gate contains %s, so the assertion above would pass "+
			"against a take that anchors nothing", rewritten)
	}
}

// someRefContains asks the gate the question copyWorkIsReferenced asks: is
// this commit contained by any reference here. It is asked that way rather
// than by looking a name up, so what is pinned is the reachability a copy
// rests on rather than the naming scheme that happens to provide it.
func someRefContains(t *testing.T, gateRepo *vcs.Repository, commit string) bool {
	t.Helper()
	refs, err := gateRepo.ListRefs(ctx(t))
	if err != nil {
		t.Fatalf("listing the gate's references: %v", err)
	}
	for _, ref := range refs {
		contains, err := gateRepo.IsAncestor(ctx(t), commit, ref.Commit)
		if err != nil {
			t.Fatalf("asking whether %s contains %s: %v", ref.Name, commit, err)
		}
		if contains {
			return true
		}
	}
	return false
}

// TestASecondTakeLeavesAnEarlierRunsCopyReclaimable is the same property
// driven the way the product meets it, and it is the regression this test
// file exists for.
//
// A run started by the bare command has no push behind it, so the reference
// the take wrote is the only thing in the gate containing its submitted
// commit: git does not list a linked worktree's head among a repository's
// references, so the copy's own head anchors nothing. An anchor keyed by
// branch is moved off that commit by the next take, and the run's copy can
// then never be given back - RemoveCopy refuses with ErrWorkUnreachable, and
// recovery reports it again on every service open.
//
// Naming the commit is what fixes it, so this asserts the outcome rather than
// the name: after a second take of a rewritten branch, the first run's copy is
// still reclaimable.
func TestASecondTakeLeavesAnEarlierRunsCopyReclaimable(t *testing.T) {
	principles.Cite(t, principles.P6)
	_, spec, opts, wc := takeable(t)

	first, err := gate.TakeBranch(ctx(t), spec, "main", opts()...)
	if err != nil {
		t.Fatalf("taking the branch for the first run: %v", err)
	}
	path := filepath.Join(t.TempDir(), "run-copy")
	if err := gate.AddCopy(ctx(t), spec, path, first, opts()...); err != nil {
		t.Fatalf("creating the first run's copy: %v", err)
	}

	writeFile(t, filepath.Join(wc.path, "file.txt"), "rewritten\n")
	rawGit(t, wc.path, "add", "-A")
	rawGit(t, wc.path, "commit", "--quiet", "--amend", "-m", "rewritten")
	rewritten := strings.TrimSpace(rawGit(t, wc.path, "rev-parse", "HEAD"))
	if _, err := tryRawGit(wc.path, "merge-base", "--is-ancestor", first, rewritten); err == nil {
		t.Fatal("the rewritten commit still descends from the first, so this checks nothing")
	}
	if _, err := gate.TakeBranch(ctx(t), spec, "main", opts()...); err != nil {
		t.Fatalf("taking the rewritten branch: %v", err)
	}

	if err := gate.RemoveCopy(ctx(t), spec, path, opts()...); err != nil {
		t.Fatalf("the first run's copy could not be given back after the branch was taken "+
			"again: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the copy at %s is still there after a reclaim that reported success: %v", path, err)
	}
}

// TestATakenBranchDoesNotMoveTheGatesOwnBranch is the other half of the same
// decision.
//
// The take writes a reference internal/gate owns and never a branch, so a
// branch in the gate moves only where a push moves it and P6's answer there is
// git's, unchanged. Nothing in the product reads a gate branch, so only a test
// can say the take leaves it alone.
func TestATakenBranchDoesNotMoveTheGatesOwnBranch(t *testing.T) {
	principles.Cite(t, principles.P6)
	g, spec, opts, wc := takeable(t)

	rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "main")
	pushed := strings.TrimSpace(rawGit(t, wc.path, "rev-parse", "HEAD"))

	writeFile(t, filepath.Join(wc.path, "file.txt"), "rewritten\n")
	rawGit(t, wc.path, "add", "-A")
	rawGit(t, wc.path, "commit", "--quiet", "--amend", "-m", "rewritten")
	rewritten := strings.TrimSpace(rawGit(t, wc.path, "rev-parse", "HEAD"))

	taken, err := gate.TakeBranch(ctx(t), spec, "main", opts()...)
	if err != nil {
		t.Fatalf("taking the rewritten branch: %v", err)
	}
	if taken != rewritten {
		t.Fatalf("the take reports %s, want the rewritten commit %s", taken, rewritten)
	}

	gateRepo, err := vcs.OpenBare(ctx(t), g.Repository())
	if err != nil {
		t.Fatalf("opening the gate repository: %v", err)
	}
	branch, err := gateRepo.ResolveCommit(ctx(t), "refs/heads/main")
	if err != nil {
		t.Fatalf("reading the gate's own branch: %v", err)
	}
	if branch != pushed {
		t.Errorf("the gate's refs/heads/main stands at %s, want the pushed commit %s: the take "+
			"wrote a reference a push moves", branch, pushed)
	}
	// The take is still visible in the gate, under the namespace it owns, or
	// the assertion above would pass against a take that did nothing.
	if _, err := gateRepo.ResolveCommit(ctx(t), rewritten); err != nil {
		t.Errorf("the gate does not hold the commit the take reported (%s): %v", rewritten, err)
	}
}
