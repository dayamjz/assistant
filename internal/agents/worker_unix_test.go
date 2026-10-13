//go:build unix

package agents_test

import (
	"errors"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
)

// A worker leads a process group of its own, which is alive while it runs
// and empty once Close has returned, on the platform where the group can be
// inspected. That is what makes Group a thing a caller can register and
// signal rather than a number.
func TestAWorkerLeadsALiveGroupThatCloseEmpties(t *testing.T) {
	worker, err := agents.OpenWorker(t.Context(), newWorkerRunner(t), workerSpec(t))
	if err != nil {
		t.Fatalf("starting the worker: %v", err)
	}
	group := worker.Group()
	if group <= 0 {
		t.Fatalf("the worker reports group %d, want a positive identifier", group)
	}
	if err := syscall.Kill(-group, 0); err != nil {
		t.Fatalf("the group %d is not alive while the worker runs: %v", group, err)
	}
	// The group is the leader's own: the identifier names the process that
	// was started, not an inherited one.
	if pgid, err := syscall.Getpgid(group); err != nil || pgid != group {
		t.Errorf("the leader %d is in group %d (%v), want its own", group, pgid, err)
	}

	worker.Close()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(-group, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = syscall.Kill(-group, syscall.SIGKILL)
	t.Fatalf("the group %d still exists after Close returned", group)
}
