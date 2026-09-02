package agents_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
)

// promptly bounds how long an invocation may take to return after its context
// ended. It is far below helperHold, so an invocation that returned within it
// was ended rather than waited out.
const promptly = 20 * time.Second

// sweeper records the directories the identity-based sweep seam was asked
// about.
type sweeper struct {
	mu   sync.Mutex
	dirs []string
}

func (s *sweeper) SweepUnder(_ context.Context, dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dirs = append(s.dirs, dir)
}

func (s *sweeper) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.dirs...)
}

// spawningInvocation runs the stand-in agent in a mode that starts a child of
// its own and records both process identifiers where the test can read them.
func spawningInvocation(t *testing.T, mode string) (agents.Invocation, string) {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "pids")
	inv := invocation(t, agents.ShapeText, map[string]string{
		helperModeVar: mode,
		helperPidVar:  pidFile,
	})
	return inv, pidFile
}

// awaitFile waits for the stand-in agent to record its process identifiers,
// which is how a test knows the tree exists before it cancels. It returns an
// error rather than failing the test, because the tests call it from a
// goroutine they started, where a t.Fatalf would exit that goroutine only:
// the cancellation it guards would never happen, the invocation would run for
// another helperHold, and a poll that expired after the test had returned
// would panic the whole binary instead of failing one test.
func awaitFile(path string) ([]byte, error) {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if content, err := os.ReadFile(path); err == nil && len(content) > 0 {
			return content, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil, fmt.Errorf("the stand-in agent never recorded its process identifiers at %s", path)
}

// mustAwaitFile is awaitFile on the test's own goroutine, where failing is
// allowed.
func mustAwaitFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := awaitFile(path)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return content
}

// cancelOnceSpawned cancels ctx as soon as the stand-in agent has recorded its
// process identifiers, and cancels it anyway if it never does, so a broken
// stand-in ends the invocation rather than leaving the test waiting on it. The
// returned function reports what the wait saw and must be called from the test
// goroutine after the invocation has returned.
func cancelOnceSpawned(t *testing.T, pidFile string) (context.Context, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	waited := make(chan error, 1)
	go func() {
		_, err := awaitFile(pidFile)
		waited <- err
		cancel()
	}()
	return ctx, func() {
		t.Helper()
		if err := <-waited; err != nil {
			t.Fatalf("%v", err)
		}
	}
}

func TestACancelledInvocationIsATypedRefusal(t *testing.T) {
	rec := &recorder{}
	runner := newRunner(t, agents.WithRecorder(rec))
	inv, pidFile := spawningInvocation(t, "spawn")

	ctx, spawned := cancelOnceSpawned(t, pidFile)

	started := time.Now()
	_, err := runner.Run(ctx, agents.PurposeReview, inv)
	spawned()
	// Cancellation ends the invocation rather than waiting for the agent to
	// finish on its own, which it would not do for another helperHold.
	if elapsed := time.Since(started); elapsed > promptly {
		t.Errorf("the cancelled invocation took %v to return, want under %v", elapsed, promptly)
	}
	var refusal *agents.InvocationError
	if !errors.As(err, &refusal) {
		t.Fatalf("refusal is not an *InvocationError: %v", err)
	}
	if refusal.Failure != agents.FailureCancelled {
		t.Errorf("failure category is %q, want %q", refusal.Failure, agents.FailureCancelled)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("refusal does not carry the context's error: %v", err)
	}
	if len(rec.records) != 1 || rec.records[0].Failure != agents.FailureCancelled {
		t.Errorf("records are %+v, want one recording the cancellation", rec.records)
	}
}

func TestATimedOutInvocationIsATypedRefusal(t *testing.T) {
	runner := newRunner(t)
	inv, _ := spawningInvocation(t, "spawn")

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	_, err := runner.Run(ctx, agents.PurposeChecks, inv)
	var refusal *agents.InvocationError
	if !errors.As(err, &refusal) {
		t.Fatalf("refusal is not an *InvocationError: %v", err)
	}
	if refusal.Failure != agents.FailureTimeout {
		t.Errorf("failure category is %q, want %q", refusal.Failure, agents.FailureTimeout)
	}
}

// The sweep seam is the answer to a descendant that leaves the process group,
// so it is asked about every invocation, including one that succeeded and one
// that was cancelled, and it is asked about the working directory rather than
// anything derived from a command line.
func TestTheSweepSeamIsAskedAboutEveryInvocation(t *testing.T) {
	sweep := &sweeper{}
	runner := newRunner(t, agents.WithSweep(sweep))

	done := invocation(t, agents.ShapeText, map[string]string{
		helperModeVar:   "envelope",
		helperResultVar: "finished",
	})
	if _, err := runner.Run(t.Context(), agents.PurposeReview, done); err != nil {
		t.Fatalf("invocation failed: %v", err)
	}

	cancelled, pidFile := spawningInvocation(t, "spawn")
	ctx, spawned := cancelOnceSpawned(t, pidFile)
	if _, err := runner.Run(ctx, agents.PurposeFix, cancelled); err == nil {
		t.Fatal("the cancelled invocation reported success")
	}
	spawned()

	got := sweep.seen()
	want := []string{done.Dir, cancelled.Dir}
	if len(got) != len(want) {
		t.Fatalf("the sweep was asked about %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("the sweep was asked about %q, want %q", got[i], want[i])
		}
	}
}
