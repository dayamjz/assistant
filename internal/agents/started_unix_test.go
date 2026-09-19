//go:build unix

package agents_test

import (
	"errors"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
)

// TestStartedIsToldWhileTheGroupIsAlive establishes, where the platform can
// answer it, that the identifier Started is told names a process group that
// exists at that moment: a signal probe of the group succeeds inside the
// callback. It then waits for the same probe to fail after Run has returned,
// which is the tree being gone rather than merely the invocation being over.
func TestStartedIsToldWhileTheGroupIsAlive(t *testing.T) {
	runner := newRunner(t)
	var mu sync.Mutex
	var pgid int
	var aliveWhenTold error
	inv := invocation(t, agents.ShapeText, map[string]string{
		helperModeVar:   "envelope",
		helperResultVar: "alive",
	})
	inv.Started = func(got int) func() {
		mu.Lock()
		defer mu.Unlock()
		pgid = got
		aliveWhenTold = syscall.Kill(-got, 0)
		return func() {}
	}

	if _, err := runner.Run(t.Context(), agents.PurposeIntent, inv); err != nil {
		t.Fatalf("a well-formed invocation failed: %v", err)
	}
	mu.Lock()
	group, alive := pgid, aliveWhenTold
	mu.Unlock()
	if group <= 0 {
		t.Fatalf("Started was told the group %d, want a positive identifier", group)
	}
	if alive != nil {
		t.Fatalf("the group %d was not alive when Started was told: %v", group, alive)
	}
	// The leader has been reaped by Run itself; anything else the group held
	// is reparented and reaped on the system's schedule, so gone is polled
	// rather than asserted at an instant.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(-group, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the group %d still exists after Run returned", group)
}
