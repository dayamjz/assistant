//go:build unix

package service_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// GAP-04 / P11, the half the product does not see: an investigation is
// launched in a copy with no remotes, and an adversarial worker that later
// adds a remote toward the subject's working copy and pushes there is not
// stopped by the product, because that push never reaches the gate's hooks.
//
// What it establishes: (1) the launch-time remote set is empty; (2) a push
// to a remote the worker added toward the subject moves the subject's
// default branch tip when that branch is not checked out, and no run was
// recorded for it, which a push the gate admitted would have started; (3)
// with the branch checked out, a second push is refused and the tip stays,
// which is git's refusal of an update to a checked-out branch.
//
// What it does not establish: anything about a push to the gate, which
// TestAnInvestigationWorkersPushToTheGateIsRefusedWhateverItCarries below
// holds to admission's rule; and any protection of the subject after
// launch, because AssertIsolated and the launch assertion are launch-time
// only, nothing re-checks the copy once the worker runs, and the tip that
// stays put is not a continuous isolation monitor in this package.
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
		if got := observed(t, taskState, running.client, id); !readsRunning(got) {
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
		if runs := runsOf(t, h, repository.ID); len(runs) != 0 {
			t.Fatalf("the push toward the subject started %d runs, so it reached the gate after all: %+v", len(runs), runs)
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

// runsOf reads every run recorded for a repository from the home's database
// through a read-only open beside the service that holds it.
func runsOf(t *testing.T, h *home.Home, repository string) []store.Run {
	t.Helper()
	records, err := store.OpenReadOnly(t.Context(), h.Database())
	if err != nil {
		t.Fatalf("opening the store read-only: %v", err)
	}
	defer func() { _ = records.Close() }()
	runs, err := records.RunsForRepository(t.Context(), repository)
	if err != nil {
		t.Fatalf("reading the runs of repository %s: %v", repository, err)
	}
	return runs
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

// An investigation task names no branch, so there is nothing its worker may
// push: every push of its that reaches the gate is refused by admission,
// before any reference moves, whatever it carries and whatever route it
// took to the gate. The pushes are made the way a real worker makes them,
// through the gate's installed hooks from a process the worker started,
// and each reads back the gate's references with git afterwards rather than
// trusting the answer. A new branch, the default branch, a tag, and a push
// through a remote the worker named assistant and pointed at the gate are
// each refused with the worker code and the reason, and not with the
// containment code, which is checked because a stage is registered and
// containment is the other refusal a push can meet. The control is the same
// tag pushed from the same copy by this test's own process, placed as a
// person's and admitted, which is what shows the refusal is about who
// pushed and not about the push. The delivery half of the control, a
// worker's push of its own branch admitted, is
// TestAWorkersPushIsHeldToItsOwnBranchForwardAndAPersonsIsNot.
//
// What this does not establish is anything about a push that never reaches
// the gate: the test above shows that one toward the subject is not seen.
func TestAnInvestigationWorkersPushToTheGateIsRefusedWhateverItCarries(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P11)
	launchedWorkerOf(t, store.TaskInvestigation, "", func(w launched) {
		copyPath := resolvedCopy(t, w.home, w.task)
		quietMaintenance(t, copyPath)
		if remotes := mustGitAs(t, w, copyPath, "remote"); remotes != "" {
			t.Fatalf("at launch the investigation copy carries remotes %q, want none", remotes)
		}
		mustGitAs(t, w, copyPath, "commit", "--quiet", "--allow-empty", "-m", "the investigation's commit")

		// A stage is registered so that containment reads the pusher's group
		// and could refuse, which is how a refusal carrying the worker code
		// and not the containment code is shown to be admission's own.
		forget := w.running.service.StageStarted("run-under-validation", "review", 424242)
		defer forget()

		before := allGateRefs(t, w.gateRepo)
		mustGitAs(t, w, copyPath, "remote", "add", gate.RemoteName, w.gateRepo)
		pushes := []struct {
			name string
			args []string
		}{
			{"a new branch", []string{"push", w.gateRepo, "HEAD:refs/heads/scout"}},
			{"the default branch", []string{"push", w.gateRepo, "HEAD:refs/heads/main"}},
			{"a tag", []string{"push", w.gateRepo, "HEAD:refs/tags/scout"}},
			{"a branch through a remote named " + gate.RemoteName, []string{"push", gate.RemoteName, "HEAD:refs/heads/scout"}},
		}
		for _, push := range pushes {
			out, err := gitAs(t, w, copyPath, push.args...)
			if err == nil {
				t.Fatalf("%s pushed by the investigation's worker was admitted:\n%s", push.name, out)
			}
			if !strings.Contains(out, string(ipc.CodeWorker)) || !strings.Contains(out, "names no branch") {
				t.Fatalf("%s pushed by the investigation's worker was refused without the worker code and reason:\n%s",
					push.name, out)
			}
			if strings.Contains(out, string(ipc.CodeContained)) {
				t.Fatalf("%s pushed by the investigation's worker met containment rather than admission's rule:\n%s",
					push.name, out)
			}
			if got := allGateRefs(t, w.gateRepo); got != before {
				t.Fatalf("%s pushed by the investigation's worker changed the gate's references:\nbefore: %s\nafter:  %s",
					push.name, before, got)
			}
		}

		// The same tag from the same copy, pushed by this process, which is
		// outside every worker group, is admitted and lands.
		out, err := tryGitIn(t, copyPath, "push", w.gateRepo, "HEAD:refs/tags/scout")
		if err != nil {
			t.Fatalf("a person's push of the same tag from the same copy was refused, so the rule reached a person:\n%s", out)
		}
		head := git(t, copyPath, "rev-parse", "HEAD")
		if got := allGateRefs(t, w.gateRepo); !strings.Contains(got, "refs/tags/scout "+head) {
			t.Fatalf("the person's push was reported admitted and the tag is not in the gate:\n%s", got)
		}
	})
}

// allGateRefs is every reference a gate repository holds with its object,
// read with git directly. Unlike gateRefs it reads outside refs/heads too,
// because the pushes here carry a tag and none of them starts a run that
// would write references of its own.
func allGateRefs(t *testing.T, repository string) string {
	t.Helper()
	return git(t, repository, "for-each-ref", "--format=%(refname) %(objectname)")
}
