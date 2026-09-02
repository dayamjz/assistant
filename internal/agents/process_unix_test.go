//go:build unix

package agents_test

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
)

// These assert what "an invocation owns its process tree" buys, on the
// platform where the group can be inspected. The stand-in agent starts a child
// that stays in the invocation's process group and both then wait for two
// minutes, so a process still alive after the invocation returned is a process
// this package failed to end rather than one that happened to finish.

// readPids reads the process identifiers the stand-in agent recorded: its own
// first, then its child's.
func readPids(t *testing.T, content []byte) (int, int) {
	t.Helper()
	fields := strings.Fields(string(content))
	if len(fields) != 2 {
		t.Fatalf("the stand-in agent recorded %q, want two process identifiers", content)
	}
	leader, err := strconv.Atoi(fields[0])
	if err != nil {
		t.Fatalf("unreadable leader identifier %q: %v", fields[0], err)
	}
	child, err := strconv.Atoi(fields[1])
	if err != nil {
		t.Fatalf("unreadable child identifier %q: %v", fields[1], err)
	}
	return leader, child
}

// alive reports whether a process this test could signal still exists. Signal
// zero performs the checks without delivering anything, and a process that is
// gone answers ESRCH.
func alive(pid int) bool {
	return syscall.Kill(pid, 0) != syscall.ESRCH
}

// assertGone fails unless the process has ended. It allows a short settling
// window because a process that has been signalled is reaped by the system
// rather than instantly.
func assertGone(t *testing.T, what string, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Leave nothing running behind a failed test.
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Errorf("%s (process %d) survived the invocation that started it", what, pid)
}

func TestACancelledInvocationLeavesNoSurvivingChildProcess(t *testing.T) {
	for _, mode := range []string{"spawn", "spawn-stubborn"} {
		t.Run(mode, func(t *testing.T) {
			runner := newRunner(t)
			inv, pidFile := spawningInvocation(t, mode)

			ctx, spawned := cancelOnceSpawned(t, pidFile)

			started := time.Now()
			if _, err := runner.Run(ctx, agents.PurposeReview, inv); err == nil {
				t.Fatal("the cancelled invocation reported success")
			}
			spawned()
			// The invocation was ended rather than waited out, including in
			// the mode where the polite signal is ignored and only the
			// escalation ends it.
			if elapsed := time.Since(started); elapsed > promptly {
				t.Errorf("the cancelled invocation took %v to return, want under %v", elapsed, promptly)
			}

			// Run has returned, so this package has already done everything it
			// is going to do about these processes. Both the agent and the
			// child it started must be gone, including in the mode where both
			// ignore the polite signal and only the forceful one ends them.
			leader, child := readPids(t, mustAwaitFile(t, pidFile))
			assertGone(t, "the agent", leader)
			assertGone(t, "the child the agent started", child)
		})
	}
}

// The same ownership on the completion path. The agent starts a child, then
// finishes successfully and leaves it running: nothing is cancelled and
// nothing failed, so the child survives unless the invocation ends the tree it
// owns after the agent is gone.
func TestAFinishedInvocationLeavesNoSurvivingChildProcess(t *testing.T) {
	runner := newRunner(t)
	inv, pidFile := spawningInvocation(t, "spawn-and-exit")

	got, err := runner.Run(t.Context(), agents.PurposeFix, inv)
	if err != nil {
		t.Fatalf("the invocation failed: %v", err)
	}
	if got.Text != "finished, and left something running" {
		t.Fatalf("the agent's result did not come back: %q", got.Text)
	}

	_, child := readPids(t, mustAwaitFile(t, pidFile))
	assertGone(t, "the child the agent started", child)
}

// The two guards above would pass for the wrong reason if the stand-in agent's
// tree simply ended on its own. This is the control: the same tree, started
// outside any invocation, is still running after the window those tests allow,
// so their evidence is termination rather than coincidence. This test is what
// has to end it.
func TestTheStandInAgentsTreeSurvivesWithoutAnInvocation(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pids")
	cmd := exec.Command(helperBinary(t))
	cmd.Env = []string{helperModeVar + "=spawn", helperPidVar + "=" + pidFile}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the stand-in agent directly: %v", err)
	}
	leader, child := readPids(t, mustAwaitFile(t, pidFile))
	t.Cleanup(func() {
		_ = syscall.Kill(child, syscall.SIGKILL)
		_ = syscall.Kill(leader, syscall.SIGKILL)
		_ = cmd.Wait()
	})

	// Longer than the settling window assertGone allows, so "still alive" here
	// and "gone" there cannot both be explained by timing.
	time.Sleep(10 * time.Second)
	if !alive(leader) {
		t.Error("the stand-in agent ended on its own, so the termination tests prove nothing")
	}
	if !alive(child) {
		t.Error("the child ended on its own, so the termination tests prove nothing")
	}
}

// An agent the system ends with a signal reported no status of its own, so it
// is a process failure rather than an exit, and the cause has to survive: a
// killed agent whose error says only "exit" with no code and no cause cannot
// be told apart from one that crashed, which is the difference a person
// diagnosing a run needs most.
func TestASignalledAgentIsAProcessFailureThatSaysWhatEndedIt(t *testing.T) {
	rec := &recorder{}
	runner := newRunner(t, agents.WithRecorder(rec))
	inv := invocation(t, agents.ShapeText, map[string]string{helperModeVar: "self-kill"})

	_, err := runner.Run(t.Context(), agents.PurposeReview, inv)
	var refusal *agents.InvocationError
	if !errors.As(err, &refusal) {
		t.Fatalf("refusal is not an *InvocationError: %v", err)
	}
	if refusal.Failure != agents.FailureProcess {
		t.Errorf("failure category is %q, want %q", refusal.Failure, agents.FailureProcess)
	}
	var exit *exec.ExitError
	if !errors.As(refusal.Err, &exit) {
		t.Fatalf("the refusal carries %v as its cause, want the error the process ended with", refusal.Err)
	}
	// The stand-in agent was signalled rather than exiting, which is the case
	// this is about: there is no status to report and the code is -1.
	if exit.ProcessState.Exited() {
		t.Error("the agent reported an exit status, so this is not the signalled path")
	}
	if refusal.ExitCode >= 0 {
		t.Errorf("exit code is %d, want the -1 that stands for no status", refusal.ExitCode)
	}
	// What the reader is left with names what happened rather than being a
	// bare category with nothing after it.
	if rendered := refusal.Error(); !strings.Contains(rendered, "signal") {
		t.Errorf("the refusal reads %q, which does not say what ended the agent", rendered)
	}
	if len(rec.records) != 1 || rec.records[0].Failure != agents.FailureProcess {
		t.Errorf("records are %+v, want one recording the process failure", rec.records)
	}
}
