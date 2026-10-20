//go:build unix

package agents_test

import (
	"context"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
)

// TestAClosedWorkerUnderAHurryEndsTheChildItsLeaderLeftAtOnce is the forced
// stop reaching a worker's tree, and the same for a subordinate coordinator,
// which is a worker started through OpenSubordinate: the worker exits when
// Close closes its input and leaves a child in its group that ignores the
// polite signal. With a termination grace of a minute, Close would wait that
// out before the forceful signal reached the child; started on a context
// whose hurry is closed, Close returns promptly and the child is gone.
func TestAClosedWorkerUnderAHurryEndsTheChildItsLeaderLeftAtOnce(t *testing.T) {
	for name, open := range map[string]func(context.Context, agents.Runner, agents.WorkerSpec) (agents.Worker, error){
		"worker":      agents.OpenWorker,
		"subordinate": agents.OpenSubordinate,
	} {
		t.Run(name, func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "pids")
			runner, err := agents.ClaudeFactory(
				agents.WithBinary(helperBinary(t)),
				agents.WithBaseEnvironment([]string{helperPidVar + "=" + pidFile}),
				agents.WithTerminationGrace(time.Minute),
			).New(t.Context(), []string{helperModeFlag + "worker-spawn-stubborn-child"})
			if err != nil {
				t.Fatalf("building a runner over the stand-in worker: %v", err)
			}
			hurry := make(chan struct{})
			worker, err := open(agents.WithHurry(t.Context(), hurry), runner, agents.WorkerSpec{
				Dir: t.TempDir(), Instructions: "begin",
			})
			if err != nil {
				t.Fatalf("starting the worker: %v", err)
			}
			_, child := readPids(t, mustAwaitFile(t, pidFile))
			// The child sets its disposition as it starts.
			time.Sleep(500 * time.Millisecond)
			close(hurry)

			started := time.Now()
			worker.Close()
			if elapsed := time.Since(started); elapsed > promptly {
				t.Errorf("closing the hurried worker took %v, want under %v", elapsed, promptly)
			}
			assertGone(t, "the child the worker left", child)
		})
	}
}

// holdingWithin is how long a hurried tree whose leader is gone and whose
// child holds the output pipe is given to be gone: service.Shutdown's settle
// after the forceful step is a second, and this allows the reaping on top.
const holdingWithin = 3 * time.Second

// awaitLeaderGone waits until the leader the stand-in recorded has exited.
func awaitLeaderGone(t *testing.T, leader int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for alive(leader) {
		if !time.Now().Before(deadline) {
			t.Fatalf("the stand-in leader %d never exited", leader)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// goneWithin reports whether pid and the group it was in are gone within
// wait.
func goneWithin(pid, group int, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if !alive(pid) && !groupAlive(group) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// TestAHurryEndsATreeWhoseExitedLeaderLeftAChildHoldingItsOutput is the
// forced stop's last reproduction: the leader exits on its own and its child,
// which ignores the polite signal, keeps the leader's standard output open,
// so the process's Wait is held for the adapter's whole grace before any
// termination begins. Nobody cancels or closes anything; the hurry is closed,
// and the child and its group must be gone within holdingWithin, for an
// invocation, a worker and a subordinate coordinator alike, under a grace of
// a minute.
func TestAHurryEndsATreeWhoseExitedLeaderLeftAChildHoldingItsOutput(t *testing.T) {
	for _, name := range []string{"invocation", "worker", "subordinate"} {
		t.Run(name, func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "pids")
			runner, err := agents.ClaudeFactory(
				agents.WithBinary(helperBinary(t)),
				agents.WithBaseEnvironment([]string{helperPidVar + "=" + pidFile}),
				agents.WithTerminationGrace(time.Minute),
			).New(t.Context(), []string{helperModeFlag + "spawn-holding-child-and-exit"})
			if err != nil {
				t.Fatalf("building a runner over the stand-in: %v", err)
			}
			hurry := make(chan struct{})
			ctx := agents.WithHurry(t.Context(), hurry)
			ended := make(chan struct{})
			switch name {
			case "invocation":
				go func() {
					defer close(ended)
					_, _ = runner.Run(ctx, agents.PurposeReview, invocation(t, agents.ShapeText, nil))
				}()
			default:
				open := agents.OpenWorker
				if name == "subordinate" {
					open = agents.OpenSubordinate
				}
				worker, err := open(ctx, runner, agents.WorkerSpec{Dir: t.TempDir(), Instructions: "begin"})
				if err != nil {
					t.Fatalf("starting the %s: %v", name, err)
				}
				go func() { <-worker.Done(); close(ended) }()
			}
			leader, child := readPids(t, mustAwaitFile(t, pidFile))
			t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL); <-ended })
			awaitLeaderGone(t, leader)
			if !alive(child) {
				t.Fatalf("the child %d did not outlive its leader, so the case cannot be driven", child)
			}

			close(hurry)
			if !goneWithin(child, leader, holdingWithin) {
				t.Fatalf("the child %d or its group %d was still there %v after the hurry closed", child, leader, holdingWithin)
			}
			select {
			case <-ended:
			case <-time.After(holdingWithin):
				t.Fatalf("the %s had not ended %v after its tree was gone", name, holdingWithin)
			}
		})
	}
}
