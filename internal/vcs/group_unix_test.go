//go:build unix

package vcs_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/vcs"
)

// hangLimit is how long a test waits for a call to a git that never exits
// before failing it. It is well past every deadline these tests set plus the
// two graces procgroup.Run may spend after one, and well short of the hour
// the stand-in would otherwise wait.
const hangLimit = 20 * time.Second

// hangingDiff opens a repository over a stand-in git whose diff never exits
// and leaves a grandchild in its group holding its pipes, calls Diff, and
// returns the two processes the stand-in recorded, how long the call took and
// what it answered. The call runs under a context with callerDeadline, or,
// when that is zero, under context.WithoutCancel, which carries no deadline;
// the context is made after the open so its deadline is spent on Diff alone.
// A call still waiting at hangLimit fails the test, and cleanup ends both
// processes either way.
func hangingDiff(t *testing.T, callerDeadline time.Duration, opts ...vcs.Option) ([]int, time.Duration, error) {
	t.Helper()
	gitEnvironment(t)
	_, exe := useFakeGit(t)
	pids := trackStandIns(t)
	t.Setenv(fakeGitHangOn, "diff")
	repo, err := vcs.OpenBare(ctx(t), fakeBareDir(t), append([]vcs.Option{vcs.WithGitBinary(exe)}, opts...)...)
	if err != nil {
		t.Fatalf("OpenBare against the stand-in git: %v", err)
	}
	call := context.WithoutCancel(context.Background())
	if callerDeadline > 0 {
		var cancel context.CancelFunc
		call, cancel = context.WithTimeout(context.Background(), callerDeadline)
		defer cancel()
	}
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := repo.Diff(call, "a", "b")
		done <- err
	}()
	select {
	case err = <-done:
	case <-time.After(hangLimit):
		t.Fatalf("Diff against a git that never exits had not returned after %s", hangLimit)
	}
	elapsed := time.Since(start)
	recorded := pids()
	if len(recorded) != 2 {
		t.Fatalf("the stand-in recorded %d processes, want the git and its grandchild (the call answered %v)", len(recorded), err)
	}
	return recorded, elapsed, err
}

// requireGone fails the test for each process in pids still running a
// moment after the call that started it returned. Each would otherwise wait
// for the test's release, so a process still here is one nothing ended.
func requireGone(t *testing.T, pids []int) {
	t.Helper()
	for _, pid := range pids {
		if !gone(pid, 2*time.Second) {
			t.Errorf("process %d is still running after the invocation that started it returned", pid)
		}
	}
}

// A git that never exits, called under a context with no deadline of its
// own, which is what context.WithoutCancel gives the deferred cleanups in
// internal/service, is ended at the handle's invocation deadline together
// with the grandchild it left in its group, and the call returns naming the
// deadline. That is P15's "an external program the service invokes runs
// under a deadline, and when the deadline passes it is ended together with
// everything it started".
func TestAGitThatNeverExitsIsEndedWithItsGroupAtTheInvocationDeadline(t *testing.T) {
	principles.Cite(t, principles.P15)
	const deadline = 5 * time.Second
	pids, elapsed, err := hangingDiff(t, 0, vcs.WithInvocationDeadline(deadline))
	var cmdErr *vcs.CommandError
	if !errors.As(err, &cmdErr) {
		t.Fatalf("Diff = %v; want a *CommandError", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("the error does not name the deadline that ended the call: %v", err)
	}
	if elapsed < deadline {
		t.Errorf("Diff returned after %s, before its %s deadline", elapsed, deadline)
	}
	requireGone(t, pids)
}

// A caller's own deadline is the one an invocation runs under, and its
// passing ends the group the same way: the invocation deadline only fills in
// where a caller has none, so an hour of it does not keep the call going.
func TestACallersDeadlineEndsTheGroupAndIsNotReplaced(t *testing.T) {
	principles.Cite(t, principles.P15)
	pids, _, err := hangingDiff(t, 5*time.Second, vcs.WithInvocationDeadline(time.Hour))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Diff = %v; want the caller's deadline", err)
	}
	requireGone(t, pids)
}

// A git that exits at once while a grandchild it left in its process group
// still holds its pipes is answered as soon as git exits, and the grandchild
// does not outlive the call: the group is ended before git is reaped, which
// closes the pipes the grandchild held.
func TestAGitThatExitsLeavingADescendantInItsGroupEndsTheDescendant(t *testing.T) {
	principles.Cite(t, principles.P15)
	gitEnvironment(t)
	_, exe := useFakeGit(t)
	pids := trackStandIns(t)
	t.Setenv(fakeGitHoldOn, "diff")
	repo, err := vcs.OpenBare(ctx(t), fakeBareDir(t), vcs.WithGitBinary(exe))
	if err != nil {
		t.Fatalf("OpenBare against the stand-in git: %v", err)
	}
	if _, err := repo.Diff(ctx(t), "a", "b"); err != nil {
		t.Errorf("Diff against a git that exited 0 = %v; want its answer", err)
	}
	recorded := pids()
	if len(recorded) != 1 {
		t.Fatalf("the stand-in recorded %d processes, want its one grandchild", len(recorded))
	}
	requireGone(t, recorded)
}
