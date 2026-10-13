package service_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
)

// quietMaintenance turns git's automatic maintenance off for every repository
// the test and the service under it create, so no git a test started outlives
// its temporary directory (#119). The raw git helper here insulates each
// invocation from the machine's configuration with an absent file, so the
// repositories the test makes are configured one by one; the service's own
// git runs under internal/vcs, which keeps GIT_CONFIG_GLOBAL, so the clone it
// cuts reads this file.
func quietMaintenance(t *testing.T, repositories ...string) {
	t.Helper()
	for _, dir := range repositories {
		git(t, dir, "config", "gc.auto", "0")
		git(t, dir, "config", "maintenance.auto", "false")
	}
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, []byte("[gc]\n\tauto = 0\n[maintenance]\n\tauto = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
}

// refsOf is every reference in a repository with its commit, as git lists
// them, for telling whether a working copy was touched.
func refsOf(t *testing.T, dir string) string {
	t.Helper()
	return git(t, dir, "for-each-ref", "--format=%(refname) %(objectname)")
}

// The one behaviour change to the run path: a commit that exists only in a
// task's clone is pushed to the gate, and the run that push starts cuts its
// copy from the gate with the working copy neither holding the commit nor
// being asked for it. Before the fetch moved, ensureCopy asked the working
// copy for the commit, and the run went on only because git writes a
// reference to an object the repository already holds without transferring
// it; what this reads is the reference create wrote from the gate's own
// object, which is what the copy now rests on. The rest of the run path is
// unchanged, and
// TestARunIsGivenAnIsolatedCopyAndGivesItBack reads that: the copy and the
// run's reference are reclaimed when the run settles.
func TestAPushOfACommitOnlyInATaskCloneStartsARunWithoutTouchingTheWorkingCopy(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	quietMaintenance(t, subject)
	repository := recordRepository(t, h, subject)
	gatePath := gateRepositoryOf(t, h, subject)
	gateID := func() string {
		records := openRecords(t, h)
		defer func() { _ = records.Close() }()
		binding, err := records.GateBinding(t.Context(), subject)
		if err != nil {
			t.Fatal(err)
		}
		return binding.GateID
	}()

	// A clone standing in for a task's copy, with a commit of its own that
	// the working copy never sees, landed in the gate the way a push lands
	// it: the branch's reference and the objects behind it, and nothing
	// else. Pushing through the hooks would start the run through this test
	// binary's gate verbs, which internal/cli's tests drive; here the
	// notification is called directly with what such a push reports.
	clone := filepath.Join(t.TempDir(), "task-clone")
	git(t, filepath.Dir(clone), "clone", "--quiet", "--branch", "work", subject, clone)
	quietMaintenance(t, clone)
	if err := os.WriteFile(filepath.Join(clone, "only-here.txt"), []byte("a commit the working copy never holds\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, clone, "add", "-A")
	git(t, clone, "commit", "--quiet", "-m", "made in the task's clone")
	onlyInClone := git(t, clone, "rev-parse", "HEAD")
	old := git(t, subject, "rev-parse", "HEAD")
	git(t, gatePath, "fetch", "--quiet", clone, "+refs/heads/work:refs/heads/work")
	if out, err := exec.Command("git", "-C", subject, "cat-file", "-e", onlyInClone).CombinedOutput(); err == nil {
		t.Fatalf("the working copy already holds %s, so this test would show nothing: %s", onlyInClone, out)
	}
	primaryBefore := refsOf(t, subject)

	var runID string
	withService(t, h, func(running serviceUnderTest) {
		var out machine.Notification
		if err := running.client.Call(t.Context(), ipc.MethodGateNotify, machine.GateRequest{
			Gate: gateID, Updates: []gate.RefUpdate{{Old: old, New: onlyInClone, Ref: "refs/heads/work"}},
		}, &out); err != nil {
			t.Fatalf("notifying the push: %v", err)
		}
		if len(out.Started) != 1 || out.Started[0].Head != onlyInClone {
			t.Fatalf("the push started %+v, want one run of %s", out.Started, onlyInClone)
		}
		runID = out.Started[0].Run
		// The record precedes the reference: the run row was created and the
		// reference written at the pushed object, with no fetch from anywhere.
		if got := git(t, gatePath, "rev-parse", "refs/assistant/runs/"+runID); got != onlyInClone {
			t.Fatalf("the gate holds the run's reference at %s, want the pushed commit %s", got, onlyInClone)
		}
		run := awaitDecision(t, running.client, runID)
		holdingAt(t, run, holdingStage(t, 0))
		copyPath := h.Worktree(repository.ID, runID)
		if head := git(t, copyPath, "rev-parse", "HEAD"); head != onlyInClone {
			t.Fatalf("the run's copy stands at %s, want the pushed commit %s", head, onlyInClone)
		}
		if got := refsOf(t, subject); got != primaryBefore {
			t.Fatalf("the working copy's references changed:\n%s\nwere:\n%s", got, primaryBefore)
		}
		if out, err := exec.Command("git", "-C", subject, "cat-file", "-e", onlyInClone).CombinedOutput(); err == nil {
			t.Fatalf("the working copy came to hold %s: %s", onlyInClone, out)
		}
	})
}

// A run started from a working copy still records a commit the gate has never
// seen: the start fetches it into the gate under the run's reference at
// creation, and the copy is cut from there.
func TestAStartFetchesTheWorkingCopysCommitIntoTheGateAtCreation(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	quietMaintenance(t, subject)
	repository := recordRepository(t, h, subject)
	gatePath := gateRepositoryOf(t, h, subject)
	head := git(t, subject, "rev-parse", "HEAD")
	if out, err := exec.Command("git", "-C", gatePath, "cat-file", "-e", head).CombinedOutput(); err == nil {
		t.Fatalf("the gate already holds %s before any run, so this test would show nothing: %s", head, out)
	}
	withService(t, h, func(running serviceUnderTest) {
		started := startRun(t, running.client, subject)
		holdingAt(t, started, holdingStage(t, 0))
		if got := git(t, gatePath, "rev-parse", "refs/assistant/runs/"+started.Record.ID); got != head {
			t.Fatalf("the gate holds the run's reference at %s, want the working copy's %s", got, head)
		}
		if got := git(t, h.Worktree(repository.ID, started.Record.ID), "rev-parse", "HEAD"); got != head {
			t.Fatalf("the copy stands at %s, want %s", got, head)
		}
	})
}

// awaitDecision reads a run started on a goroutine of the service's until it
// reaches a decision, or fails after a bound.
func awaitDecision(t *testing.T, client *ipc.Client, runID string) machine.Run {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var run machine.Run
		if err := client.Call(t.Context(), ipc.MethodRunGet, machine.RunRequest{Run: runID}, &run); err != nil {
			t.Fatalf("reading run %s: %v", runID, err)
		}
		if run.Outcome == machine.OutcomeDecision {
			return run
		}
		if run.Outcome != machine.OutcomeExecuting {
			t.Fatalf("run %s reports %s while this waits for a decision: %+v", runID, run.Outcome, run)
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s did not reach a decision in time: %+v", runID, run)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
