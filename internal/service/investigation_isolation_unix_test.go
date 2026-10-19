//go:build unix

package service_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// planningWorkbookSource is the person's planning workbook. Tests copy it
// and never commit into this checkout or push it.
const planningWorkbookSource = "/Users/joelzambrano/Projects/Quincenera"

// GAP-04 / P11: an investigation is launched in a copy with no remotes.
// An adversarial worker that later adds a remote and pushes toward the
// subject's protected default branch is exercised here.
//
// Residual (match PRD honesty): AssertIsolated and the second launch
// assertion are launch-time only. Nothing re-checks the copy after the
// worker starts. When the subject's protected branch is not checked out,
// that push can move its tip; the product does not continuously prevent
// it. This test therefore (1) asserts the launch-time remote set, (2)
// shows the residual by moving the tip when the branch is free to update,
// then (3) checks the tip out on the subject so a second push is refused
// by git and the tip stays put — that tip-unchanged half is the subject's
// checked-out-branch refusal, not a continuous isolation monitor in this
// package.
func TestAnInvestigationWorkerCannotRelyOnRuntimeIsolationAgainstTheSubject(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P11)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	quietMaintenance(t, subject)
	repository := recordRepository(t, h, subject)
	quietMaintenance(t, gateRepositoryOf(t, h, subject))
	id := recordDispatchedTask(t, h, repository, store.TaskInvestigation, "")
	protectedTip := git(t, subject, "rev-parse", "refs/heads/main")
	// Subject tests stand on a work branch; main is free to receive a push.
	if branch := git(t, subject, "rev-parse", "--abbrev-ref", "HEAD"); branch == "main" {
		t.Fatalf("the subject is on main, so the residual push below would not demonstrate a free tip")
	}

	o, _ := workerOptions(t, h)
	withServiceOptions(t, o, func(running serviceUnderTest) {
		provider, session := waitSession(t, running.service, id)
		waitCapture(t, provider, session, "worker ready")
		if got := taskState(t, running.client, id); !readsRunning(got) {
			t.Fatalf("the investigation resolves %+v, want Running", got)
		}

		copyPath := resolvedCopy(t, h, id)
		if remotes := git(t, copyPath, "remote"); remotes != "" {
			t.Fatalf("at launch the investigation copy carries remotes %q, want none", remotes)
		}

		if out, err := gitAllowFail(t, copyPath, "remote", "add", "evil", subject); err != nil {
			t.Fatalf("remote add toward the subject: %v\n%s", out, err)
		}
		git(t, copyPath, "commit", "--allow-empty", "-m", "adversarial investigation commit")
		if out, err := gitAllowFail(t, copyPath, "push", "evil", "HEAD:refs/heads/main"); err != nil {
			t.Fatalf("the residual push into a free protected tip was refused, so the gap is not demonstrated:\n%s", out)
		}
		moved := git(t, subject, "rev-parse", "refs/heads/main")
		if moved == protectedTip {
			t.Fatal("the residual push left the protected tip unchanged, so the gap is not demonstrated")
		}

		// With the protected branch checked out, a further push is refused
		// by git and the tip stays. That is not continuous product isolation.
		git(t, subject, "checkout", "--quiet", "main")
		git(t, copyPath, "commit", "--allow-empty", "-m", "second adversarial commit")
		out, err := gitAllowFail(t, copyPath, "push", "evil", "HEAD:refs/heads/main")
		if err == nil {
			t.Fatalf("push into the checked-out protected tip succeeded:\n%s", out)
		}
		if tip := git(t, subject, "rev-parse", "refs/heads/main"); tip != moved {
			t.Fatalf("the checked-out protected tip moved from %s to %s", moved, tip)
		}
	})
}

// GAP-04: an investigation of a real planning workbook does not push, merge,
// or move that workbook's protected branch, and the worker cannot authorize
// its own promotion. The subject is a temporary copy. The original checkout
// is read at the start and again at the end, and nothing in the test commits
// there or pushes it.
//
// The push the worker attempts is the one the gate admits: the copy is born
// with no remotes, the worker adds the gate, and admission refuses because
// an investigation names no branch. The gate's branch tips and the copy's
// protected branch stay where they were, and no run is recorded, so nothing
// can merge. A push the worker makes to some other remote is the residual
// investigation_isolation's other test states: the gate does not see it.
func TestAnInvestigationOfThePlanningWorkbookCannotPushMergeOrMoveItsProtectedBranch(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P11)
	requiresIdentifiedPeer(t)
	workbook := copyPlanningWorkbook(t)
	quietMaintenance(t, workbook)
	protected := git(t, workbook, "rev-parse", "refs/heads/main")

	h := newHome(t)
	repository := recordRepository(t, h, workbook)
	quietMaintenance(t, gateRepositoryOf(t, h, workbook))
	id := recordDispatchedTask(t, h, repository, store.TaskInvestigation, "")

	o, _ := workerOptions(t, h)
	withServiceOptions(t, o, func(running serviceUnderTest) {
		provider, session := waitSession(t, running.service, id)
		waitCapture(t, provider, session, "worker ready")
		w := launched{running: running, home: h, repository: repository, gateRepo: gateRepositoryOf(t, h, workbook),
			task: id, group: session.Reference().PID, provider: provider, session: session}

		copyPath := resolvedCopy(t, h, id)
		if remotes := git(t, copyPath, "remote"); remotes != "" {
			t.Fatalf("at launch the investigation copy carries remotes %q, want none", remotes)
		}
		before := gateRefs(t, w.gateRepo)
		if out, err := gitAs(t, w, copyPath, "remote", "add", gate.RemoteName, w.gateRepo); err != nil {
			t.Fatalf("adding the gate remote in the investigation copy: %v\n%s", err, out)
		}
		for _, refspec := range []string{"HEAD:refs/heads/main", "HEAD:refs/heads/task/from-the-investigation"} {
			out, err := gitAs(t, w, copyPath, "push", gate.RemoteName, refspec)
			if err == nil {
				t.Fatalf("the investigation's push %s was admitted:\n%s", refspec, out)
			}
			if !strings.Contains(out, "names no branch") {
				t.Fatalf("the investigation's push %s was refused without saying it names no branch:\n%s", refspec, out)
			}
		}
		if got := gateRefs(t, w.gateRepo); got != before {
			t.Fatalf("the refused pushes changed the gate's branches:\nbefore: %s\nafter:  %s", before, got)
		}
		if got := git(t, workbook, "rev-parse", "refs/heads/main"); got != protected {
			t.Fatalf("the workbook copy's protected branch moved from %s to %s", protected, got)
		}
		runs, err := recordsOf(t, h).RunsForRepository(t.Context(), repository.ID)
		if err != nil || len(runs) != 0 {
			t.Fatalf("the investigation recorded runs %+v (%v), want none that could merge", runs, err)
		}

		if _, code := callAs(t, w, ipc.MethodTaskPromote, machine.TaskPromoteRequest{
			Task: id, Repository: repository.ID, Branch: "task/from-the-investigation", Intent: "Deliver a finding.",
		}); code != ipc.CodeWorker {
			t.Fatalf("task.promote from the investigation's worker answered %q, want %q", code, ipc.CodeWorker)
		}
		stored, err := recordsOf(t, h).Task(t.Context(), id)
		if err != nil || stored.Shape != store.TaskInvestigation || stored.Branch.IsKnown() {
			t.Fatalf("after the refused promotion the task is %+v (%v), want still an investigation with no branch", stored, err)
		}
		if got := git(t, workbook, "rev-parse", "refs/heads/main"); got != protected {
			t.Fatalf("the refused promotion moved the protected branch from %s to %s", protected, got)
		}
	})
}

// copyPlanningWorkbook clones the planning workbook into a temporary
// directory and points that copy's origin at a local bare repository.
// The source checkout is only read. A clone's default origin is the source
// path, so that remote is removed before anything else can push to it.
func copyPlanningWorkbook(t *testing.T) string {
	t.Helper()
	info, err := os.Stat(planningWorkbookSource)
	if err != nil || !info.IsDir() {
		t.Skipf("the planning workbook is not at %s", planningWorkbookSource)
	}
	head := git(t, planningWorkbookSource, "rev-parse", "HEAD")
	porcelain := git(t, planningWorkbookSource, "status", "--porcelain")
	root := t.TempDir()
	workbook := filepath.Join(root, "workbook")
	upstream := filepath.Join(root, "upstream.git")
	clone := exec.Command("git", "clone", "--quiet", "--no-hardlinks", planningWorkbookSource, workbook)
	clone.Env = append(os.Environ(),
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=gc.auto", "GIT_CONFIG_VALUE_0=0",
		"GIT_CONFIG_KEY_1=maintenance.auto", "GIT_CONFIG_VALUE_1=false",
	)
	if out, err := clone.CombinedOutput(); err != nil {
		t.Fatalf("copying the planning workbook: %v\n%s", err, out)
	}
	git(t, workbook, "remote", "remove", "origin")
	git(t, workbook, "clone", "--quiet", "--bare", "--no-hardlinks", ".", upstream)
	git(t, workbook, "remote", "add", "origin", upstream)
	// The path a repository record is filed under is the resolved one, which
	// is what the gate binds and what the service compares against.
	resolved, err := filepath.EvalSymlinks(workbook)
	if err != nil {
		t.Fatalf("resolving the workbook copy: %v", err)
	}
	t.Cleanup(func() {
		if got := git(t, planningWorkbookSource, "rev-parse", "HEAD"); got != head {
			t.Errorf("the planning workbook HEAD moved from %s to %s", head, got)
		}
		if got := git(t, planningWorkbookSource, "status", "--porcelain"); got != porcelain {
			t.Errorf("the planning workbook checkout changed:\nbefore: %q\nafter:  %q", porcelain, got)
		}
	})
	return resolved
}

// gitAllowFail runs git without failing the test on a non-zero exit, for an
// adversarial push whose success or refusal is the observation.
func gitAllowFail(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+filepath.Join(dir, ".gitconfig-absent"),
		"GIT_CONFIG_SYSTEM="+filepath.Join(dir, ".gitconfig-absent"),
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=gc.auto", "GIT_CONFIG_VALUE_0=0",
		"GIT_CONFIG_KEY_1=maintenance.auto", "GIT_CONFIG_VALUE_1=false",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.invalid",
	)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
