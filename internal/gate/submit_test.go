package gate_test

import (
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
// What makes the forced update safe is where it writes, so that is what this
// pins: the take is asserted through what a caller can observe of it - the
// commit it reports, what the gate then resolves, and where a copy made from
// it stands - rather than through the refspec it built.
func TestABranchRewrittenLocallyIsStillTaken(t *testing.T) {
	principles.Cite(t, principles.P1)
	_, spec, opts, wc := takeable(t)

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
}

// TestATakenBranchDoesNotMoveTheGatesOwnBranch is the other half of the same
// decision, and it is what keeps the force above safe.
//
// The take writes a reference internal/gate owns. If it wrote the gate's copy
// of the branch instead, the leading plus would be a force over a reference a
// push moves, which is the history loss P6 forbids. Nothing in the product
// reads a gate branch, so only a test can say the take leaves it alone.
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
			"forced a reference a push moves", branch, pushed)
	}
	// The take is still visible in the gate, under the namespace it owns, or
	// the assertion above would pass against a take that did nothing.
	if _, err := gateRepo.ResolveCommit(ctx(t), rewritten); err != nil {
		t.Errorf("the gate does not hold the commit the take reported (%s): %v", rewritten, err)
	}
}
