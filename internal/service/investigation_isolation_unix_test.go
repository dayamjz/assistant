//go:build unix

package service_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

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
