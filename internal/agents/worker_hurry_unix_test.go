//go:build unix

package agents_test

import (
	"context"
	"path/filepath"
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
