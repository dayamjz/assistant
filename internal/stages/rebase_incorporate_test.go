package stages_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/safety"
)

// anchoredCommit is the commit an anchor the rebase stage wrote names.
func anchoredCommit(t *testing.T, anchor string) string {
	t.Helper()
	var record struct {
		Exists bool   `json:"exists"`
		Commit string `json:"commit"`
	}
	if err := json.Unmarshal([]byte(anchor), &record); err != nil {
		t.Fatalf("decoding the anchor %q: %v", anchor, err)
	}
	if !record.Exists {
		t.Fatalf("the anchor %q records the branch absent", anchor)
	}
	return record.Commit
}

// heldFinding is the one finding of a report holding on id.
func heldFinding(t *testing.T, report findings.Report, id string) findings.Finding {
	t.Helper()
	for _, f := range report.Normalize().Findings {
		if f.ID == id {
			if f.Action != findings.ActionAsk {
				t.Fatalf("%s is a %s finding, want an ask that holds for a person", id, f.Action)
			}
			return f
		}
	}
	t.Fatalf("the report does not hold on %s: %+v", id, report)
	return findings.Finding{}
}

// TestTheRebaseStageIncorporatesCommitsTheBranchHeldThatTheRunWasNotGiven is
// the rebase half of issue #396 at the stage, over a real upstream: the branch
// already holds a commit somebody else pushed, and the rebase stage replays the
// run's commits onto it, so the push is a fast-forward anchored on that commit
// and the commit survives on the branch.
//
// Two shapes, the ones the push stage's backstop is held to: the run's commit
// diverged from the colleague's, both on the base, and the run's commit
// standing behind it. Without the incorporation both are the push stage's
// would-discard refusal (push_basis_test.go).
func TestTheRebaseStageIncorporatesCommitsTheBranchHeldThatTheRunWasNotGiven(t *testing.T) {
	principles.Cite(t, principles.P6)
	for _, tc := range []struct {
		name   string
		behind bool
	}{
		{"diverged", false},
		{"behind", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := newPushRun(t)
			git(t, run.source, "config", "user.name", "test")
			git(t, run.source, "config", "user.email", "test@example.invalid")
			submitted := run.head
			var colleague string
			if tc.behind {
				git(t, run.source, "push", "--quiet", "origin", run.head+":refs/heads/topic")
				colleague = run.commitOnTopOfUpstream(t, "topic", "colleague.txt", "pushed on top of the run's commit\n")
			} else {
				colleague = run.commitOnUpstreamBranch(t, "topic", "colleague.txt", "pushed beside the run's commit\n")
			}

			out := run.rebase(t)
			if out.Report.Normalize().HasHeld() {
				t.Fatalf("the rebase stage held over a branch it could incorporate cleanly: %+v", out.Report)
			}
			anchor, _ := out.Writes[pipeline.KeyPushAnchor].Text()
			if got := anchoredCommit(t, anchor); got != colleague {
				t.Fatalf("the anchor names %s, want the colleague's %s, where the branch stood", got, colleague)
			}
			run.anchor = anchor
			if !isAncestor(t, run.source, colleague, run.head) {
				t.Fatalf("the rebase stage's head %s does not contain the colleague's %s", run.head, colleague)
			}
			if tc.behind && run.head != colleague {
				t.Fatalf("a run standing behind the branch was moved to %s, want the branch's %s", run.head, colleague)
			}
			if !tc.behind && (run.head == colleague || !strings.Contains(git(t, run.source, "show", "--stat", run.head), "change.go")) {
				t.Fatalf("the rebase stage's head %s does not carry the run's own change on top of %s (submitted %s)",
					run.head, colleague, submitted)
			}

			run.reviewedCommit = run.head
			report := run.mustPush(t)
			if report.HasHeld() {
				t.Fatalf("the push of a head the rebase stage incorporated the branch into held: %+v", report)
			}
			want := ": " + string(safety.KindFastForward) + ", anchored on " + colleague + " "
			if !strings.Contains(report.Summary, want) {
				t.Fatalf("the push reported %q, which does not say %q", report.Summary, want)
			}
			after := run.upstreamCommit(t, "refs/heads/topic")
			if after != run.head || !isAncestor(t, run.upstream, colleague, after) {
				t.Fatalf("the branch stands at %s, want %s containing the colleague's %s", after, run.head, colleague)
			}
		})
	}
}

// TestTheRebaseStageHoldsWhenTheBranchChangedAFileTheRunChanged is the
// overlapping shape: the colleague and the run change the same file. The
// rebase stage replays nothing, leaving the copy where it stood with no rebase
// in progress, and holds with an ask naming the file and the commit it
// observed. A person approving past that hold reaches the push stage's
// backstop, which refuses rather than replace the colleague's commit.
func TestTheRebaseStageHoldsWhenTheBranchChangedAFileTheRunChanged(t *testing.T) {
	principles.Cite(t, principles.P6)
	run := newPushRun(t)
	git(t, run.source, "config", "user.name", "test")
	git(t, run.source, "config", "user.email", "test@example.invalid")
	colleague := run.commitOnUpstreamBranch(t, "topic", "change.go", "package subject\n\n// the colleague's version\n")
	head := run.head

	out := run.rebase(t)
	f := heldFinding(t, out.Report, "rebase-branch-overlap")
	if got := lineAfter(f.Description, "paths: "); got != "change.go" {
		t.Fatalf("the hold names %q as overlapping, want change.go:\n%s", got, f.Description)
	}
	if got := lineAfter(f.Description, "observed: "); got != colleague {
		t.Fatalf("the hold names %q as observed, want the colleague's %s:\n%s", got, colleague, f.Description)
	}
	if got := lineAfter(f.Description, "head: "); got != head {
		t.Fatalf("the hold names %q as the head, want the run's %s:\n%s", got, head, f.Description)
	}
	if run.head != head || git(t, run.copy, "rev-parse", "HEAD") != head {
		t.Fatalf("the abandoned replay moved the run's head off %s", head)
	}
	if status := git(t, run.copy, "status"); strings.Contains(status, "rebase in progress") {
		t.Fatalf("the abandoned replay left a rebase in progress:\n%s", status)
	}
	anchor, _ := out.Writes[pipeline.KeyPushAnchor].Text()
	if got := anchoredCommit(t, anchor); got != colleague {
		t.Fatalf("the anchor names %s, want the colleague's %s", got, colleague)
	}

	run.anchor = anchor
	refused := refusedDescription(t, run.mustPush(t))
	if got := lineAfter(refused, "reason: "); got != string(safety.ReasonWouldDiscard) {
		t.Fatalf("the push past the hold was refused for %q, want %q:\n%s", got, safety.ReasonWouldDiscard, refused)
	}
	if got := run.upstreamCommit(t, "refs/heads/topic"); got != colleague {
		t.Fatalf("the branch stands at %s, and the colleague's commit %s is no longer its tip", got, colleague)
	}
}

// TestTheRebaseStageHoldsWhenTheBranchItIncorporatedIsBehindTheBase is the
// case the incorporation cannot bring up to date: the branch holds a
// colleague's commit and the base moved past what the branch contains. The
// run's commits stand on the colleague's, and bringing the base in would
// rewrite that commit, so the stage holds with an ask instead of passing as
// rebased onto current code; its head keeps the colleague's commit and the
// base's newer one is not in it.
func TestTheRebaseStageHoldsWhenTheBranchItIncorporatedIsBehindTheBase(t *testing.T) {
	principles.Cite(t, principles.P6)
	run := newPushRun(t)
	git(t, run.source, "config", "user.name", "test")
	git(t, run.source, "config", "user.email", "test@example.invalid")
	colleague := run.commitOnUpstreamBranch(t, "topic", "colleague.txt", "pushed beside the run's commit\n")
	base := run.commitOnTopOfUpstream(t, "main", "base-moved.txt", "the base moved\n")

	out := run.rebase(t)
	f := heldFinding(t, out.Report, "rebase-branch-behind-base")
	if got := lineAfter(f.Description, "observed: "); got != colleague {
		t.Fatalf("the hold names %q as observed, want the colleague's %s:\n%s", got, colleague, f.Description)
	}
	if !isAncestor(t, run.source, colleague, run.head) {
		t.Fatalf("the head %s the hold carries on from does not contain the colleague's %s", run.head, colleague)
	}
	git(t, run.source, "fetch", "--quiet", "origin", "main")
	if isAncestor(t, run.source, base, run.head) {
		t.Fatalf("the head %s contains the base's %s, so this exercises no branch behind the base", run.head, base)
	}
}

// TestTheRebaseStageHoldsAReplayThatLeftTheRunsWorkOut is the #436 review's
// reproduction: the run's commit adds change.go, and the branch independently
// added the same content, reverted it, and then added unrelated work. The
// replay applies cleanly and git leaves the run's commit out as already
// applied, so the replayed head has no change.go. The stage holds with an ask
// naming the run's commit instead of reporting success, puts the copy back at
// the run's head, and records no new head, so the submitted work is not lost.
func TestTheRebaseStageHoldsAReplayThatLeftTheRunsWorkOut(t *testing.T) {
	principles.Cite(t, principles.P6)
	run := newPushRun(t)
	git(t, run.source, "config", "user.name", "test")
	git(t, run.source, "config", "user.email", "test@example.invalid")
	head := run.head

	other := t.TempDir()
	git(t, other, "clone", "--quiet", run.upstream, other)
	git(t, other, "checkout", "--quiet", "-b", "topic", run.baseCommit)
	// The same content under another message, so it is the same patch and
	// not the run's commit itself.
	write(t, other, "change.go", "package subject\n\n// the change under validation\n")
	git(t, other, "add", ".")
	git(t, other, "commit", "--quiet", "-m", "the same change, landed independently")
	if same := git(t, other, "rev-parse", "HEAD"); same == head {
		t.Fatal("the branch holds the run's own commit, so this exercises no dropped patch")
	}
	git(t, other, "revert", "--no-edit", "HEAD")
	write(t, other, "unrelated.txt", "unrelated work\n")
	git(t, other, "add", ".")
	git(t, other, "commit", "--quiet", "-m", "unrelated work")
	git(t, other, "push", "--quiet", "origin", "topic")
	colleague := git(t, other, "rev-parse", "HEAD")

	out := run.rebase(t)
	f := heldFinding(t, out.Report, "rebase-branch-replay-unverified")
	if got := lineAfter(f.Description, "run commits: "); got != head {
		t.Fatalf("the hold names %q as the run's commits, want %s:\n%s", got, head, f.Description)
	}
	if got := lineAfter(f.Description, "observed: "); got != colleague {
		t.Fatalf("the hold names %q as observed, want %s:\n%s", got, colleague, f.Description)
	}
	if _, moved := out.Writes[pipeline.KeyHead]; moved {
		t.Fatalf("the hold recorded a new head: %+v", out.Writes)
	}
	if got := git(t, run.copy, "rev-parse", "HEAD"); got != head {
		t.Fatalf("the copy stands at %s after the discarded replay, want the run's %s", got, head)
	}
	if got := git(t, run.copy, "show", "HEAD:change.go"); !strings.Contains(got, "the change under validation") {
		t.Fatalf("the copy's head lost the run's change.go: %q", got)
	}
}

// sharedLines is a file both sides of the merge-driver cases change, long
// enough that a rename of it with one line edited is still found as a rename.
const sharedLines = "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n"

// TestTheRebaseStageHoldsWhenAMergeDriverCouldDecideTheRunsChange is the
// #436 reviews' merge-driver reproductions. The base holds shared.txt, the
// run changes its fifth line, and the branch changes the same line another
// way, with a merge attribute in force on the branch's side or on both. A
// driver that keeps the branch's side decides the replay and git's merge
// alike, so both agree on a tree without the run's change; union keeps both
// sides' lines without a conflict, which drops the line the run deleted. The
// driver is named directly, through merge.default, under the names check-attr
// uses for its own states (set, unset, unspecified), and on the destination
// of a rename the branch made. Every case holds before any replay on the
// overlap naming shared.txt, records no new head, and leaves the copy at the
// run's head with its own shared.txt.
func TestTheRebaseStageHoldsWhenAMergeDriverCouldDecideTheRunsChange(t *testing.T) {
	principles.Cite(t, principles.P6)
	keepRun := strings.Replace(sharedLines, "five\n", "five, the run's\n", 1)
	deleteRun := strings.Replace(sharedLines, "five\n", "", 1)
	for _, tc := range []struct {
		name string
		// drivers are configured merge drivers, name to command.
		drivers map[string]string
		// def is merge.default, empty for none.
		def                   string
		runAttrs, branchAttrs string
		// branchPath is where the branch's version of shared.txt lives.
		branchPath string
		mine       string
	}{
		{"configured driver keeping the branch", map[string]string{"keepbranch": "true"}, "",
			"shared.txt merge=keepbranch\n", "shared.txt merge=keepbranch\n", "shared.txt", keepRun},
		{"merge.default naming that driver", map[string]string{"keepbranch": "true"}, "keepbranch",
			"", "", "shared.txt", keepRun},
		{"a driver named set", map[string]string{"set": "true"}, "",
			"shared.txt merge=set\n", "shared.txt merge=set\n", "shared.txt", keepRun},
		{"a driver named unset", map[string]string{"unset": "true"}, "",
			"shared.txt merge=unset\n", "shared.txt merge=unset\n", "shared.txt", keepRun},
		{"a driver named unspecified", map[string]string{"unspecified": "true"}, "",
			"shared.txt merge=unspecified\n", "shared.txt merge=unspecified\n", "shared.txt", keepRun},
		{"the driver on a rename's destination", map[string]string{"keepbranch": "true"}, "",
			"", "renamed.txt merge=keepbranch\n", "renamed.txt", keepRun},
		{"built-in union", nil, "", "shared.txt merge=union\n", "shared.txt merge=union\n", "shared.txt", deleteRun},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := newPushRun(t)
			git(t, run.source, "config", "user.name", "test")
			git(t, run.source, "config", "user.email", "test@example.invalid")
			for name, command := range tc.drivers {
				git(t, run.source, "config", "merge."+name+".driver", command)
			}
			if tc.def != "" {
				git(t, run.source, "config", "merge.default", tc.def)
			}
			// A base of its own holding shared.txt, which the run then builds on.
			git(t, run.copy, "checkout", "--quiet", "--detach", run.baseCommit)
			write(t, run.copy, "shared.txt", sharedLines)
			git(t, run.copy, "add", ".")
			git(t, run.copy, "commit", "--quiet", "-m", "a base holding shared.txt")
			run.baseCommit = git(t, run.copy, "rev-parse", "HEAD")
			git(t, run.copy, "push", "--quiet", "origin", "HEAD:refs/heads/main")
			if tc.runAttrs != "" {
				write(t, run.copy, ".gitattributes", tc.runAttrs)
			}
			write(t, run.copy, "shared.txt", tc.mine)
			git(t, run.copy, "add", ".")
			git(t, run.copy, "commit", "--quiet", "-m", "the run's change to shared.txt")
			run.head = git(t, run.copy, "rev-parse", "HEAD")
			run.submitted = run.head

			other := t.TempDir()
			git(t, other, "clone", "--quiet", run.upstream, other)
			git(t, other, "checkout", "--quiet", "-b", "topic", run.baseCommit)
			if tc.branchPath != "shared.txt" {
				git(t, other, "mv", "shared.txt", tc.branchPath)
			}
			if tc.branchAttrs != "" {
				write(t, other, ".gitattributes", tc.branchAttrs)
			}
			write(t, other, tc.branchPath, strings.Replace(sharedLines, "five\n", "five, the branch's\n", 1))
			git(t, other, "add", "-A")
			git(t, other, "commit", "--quiet", "-m", "the branch's change to shared.txt")
			git(t, other, "push", "--quiet", "origin", "topic")
			colleague := git(t, other, "rev-parse", "HEAD")

			out := run.rebase(t)
			f := heldFinding(t, out.Report, "rebase-branch-overlap")
			if !strings.Contains(f.Description, "shared.txt") {
				t.Fatalf("the hold does not name shared.txt:\n%s", f.Description)
			}
			if got := lineAfter(f.Description, "observed: "); got != colleague {
				t.Fatalf("the hold names %q as observed, want %s:\n%s", got, colleague, f.Description)
			}
			if _, moved := out.Writes[pipeline.KeyHead]; moved {
				t.Fatalf("the hold recorded a new head: %+v", out.Writes)
			}
			if got := git(t, run.copy, "rev-parse", "HEAD"); got != run.head {
				t.Fatalf("the copy stands at %s, want the run's %s", got, run.head)
			}
			if got := git(t, run.copy, "show", "HEAD:shared.txt"); got != strings.TrimSpace(tc.mine) {
				t.Fatalf("the copy's shared.txt is %q, want the run's", got)
			}
		})
	}
}

// onNewBase moves the run onto a base of its own that adds files, pushed as
// the upstream's main, so the run's commit and the branch's can both change
// what the base holds. It leaves the copy at that base, for the run's commit.
func (r *pushRun) onNewBase(t *testing.T, files map[string]string) {
	t.Helper()
	git(t, r.source, "config", "user.name", "test")
	git(t, r.source, "config", "user.email", "test@example.invalid")
	git(t, r.copy, "checkout", "--quiet", "--detach", r.baseCommit)
	for name, content := range files {
		write(t, r.copy, name, content)
	}
	git(t, r.copy, "add", ".")
	git(t, r.copy, "commit", "--quiet", "-m", "a base of the run's own")
	r.baseCommit = git(t, r.copy, "rev-parse", "HEAD")
	git(t, r.copy, "push", "--quiet", "origin", "HEAD:refs/heads/main")
}

// commitRun commits what the copy holds as the run's own commit and makes it
// the run's head and submitted commit.
func (r *pushRun) commitRun(t *testing.T, message string) {
	t.Helper()
	git(t, r.copy, "add", "-A")
	git(t, r.copy, "commit", "--quiet", "-m", message)
	r.head = git(t, r.copy, "rev-parse", "HEAD")
	r.submitted = r.head
}

// branchFromBase clones the upstream, starts topic at the run's base, runs
// change there, pushes topic and returns its tip.
func (r *pushRun) branchFromBase(t *testing.T, change func(dir string)) string {
	t.Helper()
	other := t.TempDir()
	git(t, other, "clone", "--quiet", r.upstream, other)
	git(t, other, "checkout", "--quiet", "-b", "topic", r.baseCommit)
	change(other)
	git(t, other, "push", "--quiet", "origin", "topic")
	return git(t, other, "rev-parse", "HEAD")
}

// heldOn runs the rebase stage and asserts it held on the finding id
// naming path and the observed commit, recorded no new head, and left
// the copy at the run's head.
func (r *pushRun) heldOn(t *testing.T, id, path, observed string) {
	t.Helper()
	head := r.head
	out := r.rebase(t)
	f := heldFinding(t, out.Report, id)
	if !strings.Contains(f.Description, path) {
		t.Fatalf("the hold does not name %s:\n%s", path, f.Description)
	}
	if got := lineAfter(f.Description, "observed: "); got != observed {
		t.Fatalf("the hold names %q as observed, want %s:\n%s", got, observed, f.Description)
	}
	if _, moved := out.Writes[pipeline.KeyHead]; moved {
		t.Fatalf("the hold recorded a new head: %+v", out.Writes)
	}
	if got := git(t, r.copy, "rev-parse", "HEAD"); got != head {
		t.Fatalf("the copy stands at %s, want the run's %s", got, head)
	}
}

// TestTheRebaseStageHoldsAReplayARepeatedBlockWouldHide is the third #436
// review's first reproduction. blocks.txt holds two blocks with the same three
// lines of context on each side; the run changes old to RUN in the first, the
// second already reads RUN, and the branch changes the first to BRANCH under a
// driver that keeps the branch's side. The replay drops the run's edit, and
// the run's patch still matches the second block, which a check asking only
// whether the patch matches somewhere would take as the edit standing. The
// stage holds naming blocks.txt.
func TestTheRebaseStageHoldsAReplayARepeatedBlockWouldHide(t *testing.T) {
	principles.Cite(t, principles.P6)
	block := func(middle string) string { return "a1\na2\na3\n" + middle + "\na4\na5\na6\n" }
	run := newPushRun(t)
	git(t, run.source, "config", "merge.keepbranch.driver", "true")
	attributes := "blocks.txt merge=keepbranch\n"
	run.onNewBase(t, map[string]string{
		"blocks.txt": block("old") + "--\n" + block("RUN"), ".gitattributes": attributes,
	})
	write(t, run.copy, "blocks.txt", block("RUN")+"--\n"+block("RUN"))
	run.commitRun(t, "the run's change to the first block")
	colleague := run.branchFromBase(t, func(dir string) {
		write(t, dir, "blocks.txt", block("BRANCH")+"--\n"+block("RUN"))
		git(t, dir, "commit", "--quiet", "-am", "the branch's change to the first block")
	})
	run.heldOn(t, "rebase-branch-overlap", "blocks.txt", colleague)
}

// executable makes name in dir executable and stages it, so the working tree
// and the index agree on the mode.
func executable(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.Chmod(filepath.Join(dir, name), 0o755); err != nil {
		t.Fatalf("making %s executable: %v", name, err)
	}
	git(t, dir, "add", name)
}

// TestTheRebaseStageHoldsAReplayThatDroppedAModeChange is the third review's
// second reproduction: the run makes tool.sh executable, and the branch made
// the same change, reverted it and added unrelated work, so the replay leaves
// the run's commit out and tool.sh is 100644 again. The trees differ at
// tool.sh's mode, and the stage holds naming it.
func TestTheRebaseStageHoldsAReplayThatDroppedAModeChange(t *testing.T) {
	principles.Cite(t, principles.P6)
	run := newPushRun(t)
	run.onNewBase(t, map[string]string{"tool.sh": "echo tool\n"})
	executable(t, run.copy, "tool.sh")
	git(t, run.copy, "commit", "--quiet", "-m", "make tool.sh executable")
	run.head = git(t, run.copy, "rev-parse", "HEAD")
	run.submitted = run.head
	colleague := run.branchFromBase(t, func(dir string) {
		executable(t, dir, "tool.sh")
		git(t, dir, "commit", "--quiet", "-m", "the same mode change, landed independently")
		git(t, dir, "revert", "--no-edit", "HEAD")
		write(t, dir, "unrelated.txt", "unrelated work\n")
		git(t, dir, "add", "unrelated.txt")
		git(t, dir, "commit", "--quiet", "-m", "unrelated work")
	})
	run.heldOn(t, "rebase-branch-replay-unverified", "tool.sh", colleague)
	if mode := git(t, run.copy, "ls-tree", "HEAD", "tool.sh"); !strings.HasPrefix(mode, "100755 ") {
		t.Fatalf("the copy's head lost the run's mode change: %s", mode)
	}
}

// TestTheRebaseStageHoldsAnEditTheApplyBackendWouldRelocate is the fourth
// #436 review's reproduction: under rebase.backend=apply, with two interior
// blocks holding old behind the same three lines of context, the run changes
// the first to RUN and the branch changes it to BRANCH. The apply backend
// relocates the run's edit to the second block, and a check applying the run's
// hunks by context makes the same relocation and agrees. The branch touched
// the file the run changed, so the stage holds before any replay, naming it.
func TestTheRebaseStageHoldsAnEditTheApplyBackendWouldRelocate(t *testing.T) {
	principles.Cite(t, principles.P6)
	block := func(middle string) string { return "c1\nc2\nc3\n" + middle + "\nc4\nc5\nc6\n" }
	content := func(first string) string { return "top\n" + block(first) + "middle\n" + block("old") + "bottom\n" }
	run := newPushRun(t)
	git(t, run.source, "config", "rebase.backend", "apply")
	run.onNewBase(t, map[string]string{"blocks.txt": content("old")})
	write(t, run.copy, "blocks.txt", content("RUN"))
	run.commitRun(t, "the run's change to the first block")
	colleague := run.branchFromBase(t, func(dir string) {
		write(t, dir, "blocks.txt", content("BRANCH"))
		git(t, dir, "commit", "--quiet", "-am", "the branch's change to the first block")
	})
	run.heldOn(t, "rebase-branch-overlap", "blocks.txt", colleague)
	if got := git(t, run.copy, "show", "HEAD:blocks.txt"); got != strings.TrimSpace(content("RUN")) {
		t.Fatalf("the copy's blocks.txt is %q, want the run's", got)
	}
}

// TestTheRebaseStageIncorporatesABranchThatChangedOtherFiles is the positive
// case of the path-disjoint rule: the branch changed a file the base held and
// added another, and the run added a file of its own. The run's commit is
// replayed onto the branch, and the head holds the branch's change, its new
// file and the run's file, with nothing held.
func TestTheRebaseStageIncorporatesABranchThatChangedOtherFiles(t *testing.T) {
	principles.Cite(t, principles.P6)
	run := newPushRun(t)
	git(t, run.source, "config", "user.name", "test")
	git(t, run.source, "config", "user.email", "test@example.invalid")
	colleague := run.branchFromBase(t, func(dir string) {
		write(t, dir, "base.go", "package subject\n\n// the branch's edit\n")
		write(t, dir, "other.txt", "the branch's new file\n")
		git(t, dir, "add", ".")
		git(t, dir, "commit", "--quiet", "-m", "the branch's work elsewhere")
	})

	out := run.rebase(t)
	if out.Report.Normalize().HasHeld() {
		t.Fatalf("the rebase stage held over a branch that changed only other files: %+v", out.Report)
	}
	if !isAncestor(t, run.source, colleague, run.head) {
		t.Fatalf("the head %s does not contain the branch's %s", run.head, colleague)
	}
	for file, want := range map[string]string{
		"base.go": "the branch's edit", "other.txt": "the branch's new file", "change.go": "the change under validation",
	} {
		if got := git(t, run.copy, "show", run.head+":"+file); !strings.Contains(got, want) {
			t.Fatalf("the head's %s is %q, want it to hold %q", file, got, want)
		}
	}
}
