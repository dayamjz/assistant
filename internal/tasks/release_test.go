package tasks

import (
	"context"
	"errors"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// queuedPair creates a blocker with an attempt and a successor queued behind
// it by the floor, and returns both identifiers.
func queuedPair(t *testing.T, f *fixture) (blocker, successor string) {
	t.Helper()
	b := f.create(t, delivery(""))
	s := f.create(t, delivery(""))
	if s.Cause != CauseFloor || s.Task.BlockerTask.Or("") != b.Task.ID {
		t.Fatalf("the successor was recorded as %+v", s)
	}
	f.beginAttempt(t, b.Task.ID)
	return b.Task.ID, s.Task.ID
}

// release runs Release and returns the one outcome for the task named.
func (f *fixture) release(t *testing.T, task string) Outcome {
	t.Helper()
	outcomes, err := f.queue.Release(context.Background())
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	for _, o := range outcomes {
		if o.Task == task {
			return o
		}
	}
	t.Fatalf("Release considered %+v and not %s", outcomes, task)
	return Outcome{}
}

// The clear condition. A blocker at Ready holds its successor, and so does a
// merge record on its run, because nothing in this build resolves Landed from
// it; that is #107's tier, and this is the test that flips when it lands.
// Failed and Reported clear, and a release is idempotent: a second call finds
// nothing to do.
func TestReleaseClearsOnFailedAndReportedAndNotOnReady(t *testing.T) {
	ctx := context.Background()

	t.Run("ready holds", func(t *testing.T) {
		f := newFixture(t)
		blocker, successor := queuedPair(t, f)
		f.runOn(t, "run-1", store.RunPassed)
		if state := f.resolve(t, blocker).State; state != taskstate.Ready {
			t.Fatalf("the blocker resolves %s, want ready", state)
		}
		if got := f.release(t, successor); got.Result != Waiting || got.BlockerState != taskstate.Ready {
			t.Fatalf("a successor of a Ready blocker got %+v, want waiting", got)
		}
	})

	t.Run("a merge record does not clear yet", func(t *testing.T) {
		f := newFixture(t)
		blocker, successor := queuedPair(t, f)
		f.runOn(t, "run-1", store.RunRunning)
		if _, err := f.store.TransitionRunOnPullRequest(ctx, "run-1", []store.RunStatus{store.RunRunning}, store.RunPassed,
			store.PullRequestRead{PullRequest: 7, Head: "aaaa", End: store.PullRequestMerged}, store.HoldClosure{}); err != nil {
			t.Fatalf("recording the merge: %v", err)
		}
		if _, ended, err := f.store.MergeRecord(ctx, "run-1"); err != nil || !ended {
			t.Fatalf("the merge record was not written: %v", err)
		}
		// The resolver has no Landed yet, so the blocker is Ready and holds.
		// When #107 reads the record this becomes Landed and the successor
		// is released; both halves of that are the change to make here.
		if state := f.resolve(t, blocker).State; state != taskstate.Ready {
			t.Fatalf("the blocker with a merge record resolves %s; if that is landed, Clears and this test both move", state)
		}
		if got := f.release(t, successor); got.Result != Waiting {
			t.Fatalf("a successor of a merged-but-not-yet-Landed blocker got %+v, want waiting", got)
		}
	})

	t.Run("failed clears", func(t *testing.T) {
		f := newFixture(t)
		blocker, successor := queuedPair(t, f)
		f.runOn(t, "run-1", store.RunFailed)
		got := f.release(t, successor)
		if got.Result != Released || got.Blocker != blocker || got.BlockerState != taskstate.Failed ||
			got.Event.Kind != store.DispatchedEventKind {
			t.Fatalf("a successor of a Failed blocker got %+v, want released", got)
		}
		if state := f.resolve(t, successor); state.State != taskstate.Dispatched || state.Source != taskstate.SourceEvent {
			t.Fatalf("the released successor resolves %s from %s", state.State, state.Source)
		}
		// Idempotent: the successor has moved on and is not considered.
		if outcomes, err := f.queue.Release(ctx); err != nil || len(outcomes) != 0 {
			t.Fatalf("a second release did %+v (%v), want nothing", outcomes, err)
		}
		if n := f.dispatchedEvents(t, successor); n != 1 {
			t.Fatalf("the successor has %d dispatched entries", n)
		}
	})

	t.Run("reported clears", func(t *testing.T) {
		f := newFixture(t)
		blocker := f.create(t, Request{Shape: "investigation", Project: "one", Instructions: "look"})
		successor := f.create(t, Request{Shape: "investigation", Project: "one", Instructions: "look", Overlaps: blocker.Task.ID})
		if _, err := f.store.AppendTaskEvent(ctx, blocker.Task.ID, "report-ready", "done"); err != nil {
			t.Fatalf("AppendTaskEvent: %v", err)
		}
		if got := f.release(t, successor.Task.ID); got.Result != Released || got.BlockerState != taskstate.Reported {
			t.Fatalf("a successor of a Reported blocker got %+v, want released", got)
		}
	})
}

// A task whose history says queued but whose state a higher tier decided is
// refused with an *OutrankedError, and nothing is appended. The refusal is
// read off what the resolver reported as superseded, so this test, like the
// package, never reads the history's tail to learn the state; the history is
// read only afterwards, to count that nothing was written.
func TestReleaseRefusesATaskAHigherTierDecided(t *testing.T) {
	principles.Cite(t, principles.P8)
	ctx := context.Background()
	f := newFixture(t)
	blocker, successor := queuedPair(t, f)
	f.runOn(t, "run-1", store.RunFailed)
	// A run the successor names directly is attributed to it, so the run
	// tier decides its state and the queued entry is superseded.
	if _, err := f.store.CreateRun(ctx, store.Run{
		ID: "run-own", RepositoryID: repo, Branch: "elsewhere", SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "validate", IntentSource: "push", ConfigDigest: "cfg",
		Build: store.Build{Version: "v0", Revision: "0123456789abcdef", Go: "go1.25.0"},
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := f.store.SetTaskRun(ctx, successor, "run-own"); err != nil {
		t.Fatalf("SetTaskRun: %v", err)
	}
	if state := f.resolve(t, successor); state.Source != taskstate.SourceRun {
		t.Fatalf("the successor resolves from %s, so the case is not reached", state.Source)
	}
	got := f.release(t, successor)
	var outranked *OutrankedError
	if got.Result != Refused || !errors.As(got.Err, &outranked) || !errors.Is(got.Err, ErrOutranked) {
		t.Fatalf("got %+v, want refused with *OutrankedError", got)
	}
	if outranked.Task != successor || outranked.Source != taskstate.SourceRun || outranked.State != taskstate.Validating {
		t.Fatalf("the refusal names %+v", outranked)
	}
	if n := f.dispatchedEvents(t, successor); n != 0 {
		t.Fatalf("the refused release appended %d dispatched entries", n)
	}
	_ = blocker
}

// Two concurrent releases of one queued task, under the race detector, held
// between their read and their append. Exactly one dispatched entry is
// written and the loser's outcome is ErrTaskMoved, because the append is
// anchored to the sequence each read. The positive control appends the same
// entry unanchored and produces two.
func TestTwoReleasesOfOneTaskDispatchOnce(t *testing.T) {
	principles.Cite(t, principles.P6)
	ctx := context.Background()

	t.Run("anchored", func(t *testing.T) {
		f := newFixture(t)
		_, successor := queuedPair(t, f)
		f.runOn(t, "run-1", store.RunFailed)
		f.queue.betweenReadAndWrite = barrier(2)
		var outcomes [2]Outcome
		var i int
		results := race(func() error {
			got, err := f.queue.Release(ctx)
			if err != nil {
				return err
			}
			if len(got) != 1 {
				return errors.New("a release considered the wrong number of tasks")
			}
			f.exclusion.mu.Lock()
			outcomes[i], i = got[0], i+1
			f.exclusion.mu.Unlock()
			return nil
		})
		for _, err := range results {
			if err != nil {
				t.Fatalf("a release failed: %v", err)
			}
		}
		var released, moved int
		for _, o := range outcomes {
			switch {
			case o.Result == Released:
				released++
			case o.Result == Refused && errors.Is(o.Err, store.ErrTaskMoved):
				moved++
			default:
				t.Fatalf("a release ended as %+v", o)
			}
		}
		if released != 1 || moved != 1 {
			t.Fatalf("%d released and %d refused as moved, want one of each", released, moved)
		}
		if n := f.dispatchedEvents(t, successor); n != 1 {
			t.Fatalf("the task has %d dispatched entries, want one", n)
		}
	})

	t.Run("control: unanchored", func(t *testing.T) {
		f := newFixture(t)
		_, successor := queuedPair(t, f)
		f.runOn(t, "run-1", store.RunFailed)
		arrive := barrier(2)
		results := race(func() error {
			resolved, err := f.queue.reader.Task(ctx, successor)
			if err != nil {
				return err
			}
			if resolved.State != taskstate.Queued {
				return errors.New("the task was not queued")
			}
			arrive()
			_, err = f.store.AppendTaskEvent(ctx, successor, store.DispatchedEventKind, "unanchored")
			return err
		})
		for _, err := range results {
			if err != nil {
				t.Fatalf("the control failed: %v", err)
			}
		}
		if n := f.dispatchedEvents(t, successor); n != 2 {
			t.Fatalf("the control wrote %d dispatched entries, so it discriminates nothing", n)
		}
	})
}

// New refuses a missing dependency rather than building a queue that panics
// on first use.
func TestNewRefusesAMissingDependency(t *testing.T) {
	f := newFixture(t)
	whole := Deps{Home: f.home, Store: f.store, Reader: taskstate.NewReader(f.store, nil, nil),
		Exclude: f.exclusion.hold, BranchInGate: f.queue.inGate}
	if _, err := New(whole); err != nil {
		t.Fatalf("New with every dependency: %v", err)
	}
	for name, d := range map[string]Deps{
		"home":      {Store: whole.Store, Reader: whole.Reader, Exclude: whole.Exclude, BranchInGate: whole.BranchInGate},
		"store":     {Home: whole.Home, Reader: whole.Reader, Exclude: whole.Exclude, BranchInGate: whole.BranchInGate},
		"reader":    {Home: whole.Home, Store: whole.Store, Exclude: whole.Exclude, BranchInGate: whole.BranchInGate},
		"exclusion": {Home: whole.Home, Store: whole.Store, Reader: whole.Reader, BranchInGate: whole.BranchInGate},
		"gate":      {Home: whole.Home, Store: whole.Store, Reader: whole.Reader, Exclude: whole.Exclude},
	} {
		if _, err := New(d); !errors.Is(err, ErrIncomplete) {
			t.Errorf("New without the %s = %v, want ErrIncomplete", name, err)
		}
	}
}

// A claim of overlap and the floor both apply: the task claims Z, on another
// branch, while live Y holds its own branch. One blocker is recorded, the
// live claim, and the floor is re-run at release, so the task waits on
// whichever of the two is still live and is dispatched only once both have
// cleared.
func TestReleaseWaitsOnBothTheClaimAndTheFloor(t *testing.T) {
	ctx := context.Background()
	failOn := func(t *testing.T, f *fixture, id, branch string) {
		t.Helper()
		if _, err := f.store.CreateRun(ctx, store.Run{
			ID: id, RepositoryID: repo, Branch: branch, SubmittedHead: "aaaa", Base: "bbbb",
			Intent: "validate", IntentSource: "push", ConfigDigest: "cfg",
			Build: store.Build{Version: "v0", Revision: "0123456789abcdef", Go: "go1.25.0"},
		}); err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if _, err := f.store.TransitionRun(ctx, id, []store.RunStatus{store.RunPending}, store.RunFailed); err != nil {
			t.Fatalf("TransitionRun: %v", err)
		}
	}
	setUp := func(t *testing.T) (f *fixture, y, z, task string) {
		t.Helper()
		f = newFixture(t)
		yc := f.create(t, delivery(""))
		other := delivery("")
		other.Branch = "other"
		zc := f.create(t, other)
		f.beginAttempt(t, yc.Task.ID)
		if _, err := f.store.BeginTaskAttempt(ctx, zc.Task.ID, 0); err != nil {
			t.Fatalf("BeginTaskAttempt: %v", err)
		}
		got := f.create(t, delivery(zc.Task.ID))
		if got.Cause != CauseClaimed || got.Task.BlockerTask.Or("") != zc.Task.ID {
			t.Fatalf("the task was recorded as %+v, want queued behind the live claim", got)
		}
		return f, yc.Task.ID, zc.Task.ID, got.Task.ID
	}

	t.Run("claim cleared, floor live", func(t *testing.T) {
		f, y, z, task := setUp(t)
		failOn(t, f, "run-z", "other")
		if got := f.release(t, task); got.Result != Waiting || got.Holding != y || got.BlockerState != taskstate.Failed {
			t.Fatalf("with the claim cleared and the branch held got %+v, want waiting on %s", got, y)
		}
		failOn(t, f, "run-y", branch)
		if got := f.release(t, task); got.Result != Released {
			t.Fatalf("with both cleared got %+v, want released", got)
		}
		_ = z
	})

	t.Run("floor cleared, claim live", func(t *testing.T) {
		f, y, z, task := setUp(t)
		failOn(t, f, "run-y", branch)
		if got := f.release(t, task); got.Result != Waiting || got.Holding != z || got.HoldingState != taskstate.Dispatched {
			t.Fatalf("with the branch free and the claim live got %+v, want waiting on %s", got, z)
		}
		failOn(t, f, "run-z", "other")
		if got := f.release(t, task); got.Result != Released {
			t.Fatalf("with both cleared got %+v, want released", got)
		}
		_ = y
	})

	// Two tasks queued on one branch do not hold each other: once the
	// blocker clears the first is released and the second waits on it.
	t.Run("a queued task holds nothing", func(t *testing.T) {
		f := newFixture(t)
		blocker, first := queuedPair(t, f)
		second := f.create(t, delivery(""))
		if second.Task.BlockerTask.Or("") != first {
			t.Fatalf("the second was queued behind %q, want the first, %s", second.Task.BlockerTask.Or(""), first)
		}
		f.runOn(t, "run-1", store.RunFailed)
		if got := f.release(t, first); got.Result != Released {
			t.Fatalf("the first got %+v, want released past the queued second", got)
		}
		if got := f.release(t, second.Task.ID); got.Result != Waiting || got.Holding != first {
			t.Fatalf("the second got %+v, want waiting on the now-dispatched first", got)
		}
		_ = blocker
	})
}
