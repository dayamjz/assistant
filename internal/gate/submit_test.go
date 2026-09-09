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

// TestTakingTheSameCommitTwiceAnchorsItOnceAndCleansItsStaging is the
// idempotence half of the create-only anchor, driven as two whole takes of one
// commit - the interleaving that used to carry the race, since a second take
// once aimed a fetch at the name the first had already written.
//
// Both takes must report the same commit taken, the anchor must hold exactly
// that commit, and no staging reference may remain: the staging namespace is
// scaffolding each take gives back, and a take that leaked one per invocation
// would grow the gate by a reference per command rather than per commit.
func TestTakingTheSameCommitTwiceAnchorsItOnceAndCleansItsStaging(t *testing.T) {
	principles.Cite(t, principles.P1)
	g, spec, opts, wc := takeable(t)

	first, err := gate.TakeBranch(ctx(t), spec, "main", opts()...)
	if err != nil {
		t.Fatalf("taking the branch: %v", err)
	}
	second, err := gate.TakeBranch(ctx(t), spec, "main", opts()...)
	if err != nil {
		t.Fatalf("taking the same commit again: %v", err)
	}
	if first != wc.commit || second != wc.commit {
		t.Fatalf("the takes report %s then %s, want the branch's commit %s both times",
			first, second, wc.commit)
	}

	gateRepo, err := vcs.OpenBare(ctx(t), g.Repository())
	if err != nil {
		t.Fatalf("opening the gate repository: %v", err)
	}
	anchored, err := gateRepo.ResolveCommit(ctx(t), "refs/assistant/submitted/"+wc.commit)
	if err != nil {
		t.Fatalf("reading the anchor the takes wrote: %v", err)
	}
	if anchored != wc.commit {
		t.Fatalf("the anchor holds %s, want the commit it is named for, %s", anchored, wc.commit)
	}
	staged, err := gateRepo.ListRefs(ctx(t), "refs/assistant/incoming/")
	if err != nil {
		t.Fatalf("listing the staging namespace: %v", err)
	}
	if len(staged) != 0 {
		t.Errorf("%d staging reference(s) remain after the takes completed, want none: %v",
			len(staged), staged)
	}
}

// TestAForeignAnchorIsRefusedRatherThanRewritten is the create-only half, and
// it is the positive control for the race the staged take removes.
//
// The hazard the old design could produce was a commit-keyed name pointing at
// some other commit - written when the branch moved between the read that
// chose the name and the fetch aimed at it. The staged take cannot produce
// that state, so the test plants it directly, the way a counterfeit models a
// state the mechanism no longer reaches, and holds the take to the two things
// that keep the state from spreading: a typed refusal rather than success,
// and the standing reference untouched rather than rewritten - it may be the
// reachability keeping another run's work alive.
//
// The discriminating assertion is the reference staying put. With CreateRef's
// must-not-exist guard removed the take overwrites the foreign reference and
// reports success, which was watched: both halves of this went red under that
// mutation.
func TestAForeignAnchorIsRefusedRatherThanRewritten(t *testing.T) {
	principles.Cite(t, principles.P6)
	g, spec, opts, wc := takeable(t)

	// A commit the working copy holds and the branch does not stand on, so
	// the planted reference disagrees with its name the way the raced write
	// did.
	other := strings.TrimSpace(rawGit(t, wc.path, "commit-tree", "-m", "elsewhere", "HEAD^{tree}"))
	if other == wc.commit {
		t.Fatal("the planted commit equals the branch's, so the reference would not disagree")
	}
	rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, other+":refs/assistant/planted/carrier")
	rawGit(t, g.Repository(), "update-ref", "refs/assistant/submitted/"+wc.commit, other)

	_, err := gate.TakeBranch(ctx(t), spec, "main", opts()...)
	if !errors.Is(err, gate.ErrForeignAnchor) {
		t.Fatalf("TakeBranch = %v, want ErrForeignAnchor", err)
	}

	gateRepo, err := vcs.OpenBare(ctx(t), g.Repository())
	if err != nil {
		t.Fatalf("opening the gate repository: %v", err)
	}
	standing, err := gateRepo.ResolveCommit(ctx(t), "refs/assistant/submitted/"+wc.commit)
	if err != nil {
		t.Fatalf("reading the reference the take was refused over: %v", err)
	}
	if standing != other {
		t.Errorf("the refused take moved the standing reference to %s, want it untouched at %s",
			standing, other)
	}
	// The refusal gives its own staging reference back, or every retry
	// against the foreign anchor would leak one.
	staged, err := gateRepo.ListRefs(ctx(t), "refs/assistant/incoming/")
	if err != nil {
		t.Fatalf("listing the staging namespace: %v", err)
	}
	if len(staged) != 0 {
		t.Errorf("%d staging reference(s) remain after the refusal, want none: %v", len(staged), staged)
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
