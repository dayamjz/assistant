package tasks

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// An independent task is dispatched, and a task claiming an overlap is queued
// behind the task it names, whatever that task's branch. Both carry an
// identifier internal/home's rule accepts, the copy path home.TaskCopy
// reserves, and the instructions the coordinator gave.
func TestIntakeDispatchesTheIndependentAndQueuesTheClaimed(t *testing.T) {
	f := newFixture(t)
	first := f.create(t, delivery(""))
	if first.Cause != CauseIndependent || first.Task.BlockerTask.IsKnown() {
		t.Fatalf("an independent task was recorded as %+v", first)
	}
	if got := f.resolve(t, first.Task.ID); got.State != taskstate.Dispatched || got.Source != taskstate.SourceEvent {
		t.Fatalf("an independent task resolves %s from %s, want dispatched from its intake", got.State, got.Source)
	}
	if err := home.ValidTaskID(first.Task.ID); err != nil {
		t.Fatalf("intake minted %q: %v", first.Task.ID, err)
	}
	if want, _ := f.home.TaskCopy(first.Task.ID); first.Task.WorktreePath != want {
		t.Fatalf("the copy path is %q, want the reservation %q", first.Task.WorktreePath, want)
	}
	if got := f.instructionsOf(t, first.Task.ID); got != "do the thing" {
		t.Fatalf("the instructions on disk are %q", got)
	}

	// The claim names a task on another branch: no floor applies, and the
	// claim alone queues it.
	req := delivery(first.Task.ID)
	req.Branch = "another"
	second := f.create(t, req)
	if second.Cause != CauseClaimed || second.Task.BlockerTask.Or("") != first.Task.ID {
		t.Fatalf("a claimed overlap was recorded as %+v", second)
	}
	if second.BlockerState != taskstate.Dispatched {
		t.Fatalf("the blocker's state was reported as %q, want what the resolver said", second.BlockerState)
	}
	if got := f.resolve(t, second.Task.ID); got.State != taskstate.Queued || got.Source != taskstate.SourceEvent {
		t.Fatalf("a queued task resolves %s from %s", got.State, got.Source)
	}
}

// The floor: a claim of independence on a live task's branch is queued behind
// that task regardless, and an Unknown task counts as live. The positive
// control is the same claim on a branch whose task has cleared, which is
// dispatched.
func TestTheFloorQueuesBehindALiveTaskWhateverTheClaim(t *testing.T) {
	f := newFixture(t)
	blocker := f.create(t, delivery(""))

	got := f.create(t, delivery(""))
	if got.Cause != CauseFloor || got.Task.BlockerTask.Or("") != blocker.Task.ID {
		t.Fatalf("a second task on a live task's branch was recorded as %+v, want queued by the floor behind %s",
			got, blocker.Task.ID)
	}
	if got.BlockerState != taskstate.Dispatched {
		t.Fatalf("the floor reported the blocker as %q", got.BlockerState)
	}

	// Unknown counts as live: a blocker whose history ends on a note resolves
	// Unknown, and the floor still queues behind it. The newest live task on
	// the branch is the one chosen, which is the queued one, not the blocker.
	if _, err := f.store.AppendTaskEvent(context.Background(), blocker.Task.ID, "progress-note", "x"); err != nil {
		t.Fatalf("AppendTaskEvent: %v", err)
	}
	if state := f.resolve(t, blocker.Task.ID).State; state != taskstate.Unknown {
		t.Fatalf("the blocker resolves %s, so the Unknown case is not reached", state)
	}
	third := f.create(t, delivery(""))
	if third.Cause != CauseFloor || third.Task.BlockerTask.Or("") != got.Task.ID {
		t.Fatalf("with an Unknown task and a Queued task on the branch the floor recorded %+v, want queued behind the newest live, %s",
			third, got.Task.ID)
	}

	// The control: another branch whose only task has cleared. An attempt
	// makes the run the task's, and a failed run clears it.
	req := delivery("")
	req.Branch = "cleared"
	ended := f.create(t, req)
	f.beginAttempt(t, ended.Task.ID)
	run, err := f.store.CreateRun(context.Background(), store.Run{
		ID: "run-cleared", RepositoryID: repo, Branch: "cleared", SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "validate", IntentSource: "push", ConfigDigest: "cfg",
		Build: store.Build{Version: "v0", Revision: "0123456789abcdef", Go: "go1.25.0"},
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if _, err := f.store.TransitionRun(context.Background(), run.ID, []store.RunStatus{store.RunPending}, store.RunFailed); err != nil {
		t.Fatalf("TransitionRun: %v", err)
	}
	if state := f.resolve(t, ended.Task.ID).State; !Clears(state) {
		t.Fatalf("the control's task resolves %s, which does not clear", state)
	}
	after := f.create(t, req)
	if after.Cause != CauseIndependent {
		t.Fatalf("a task on a cleared task's branch was recorded as %+v, want dispatched", after)
	}
}

// A branch no task has worked on that exists in the gate, or has a run, is a
// person's and is refused with a *BranchTakenError. The same branch once a
// task names it is the floor's, not a refusal.
func TestIntakeRefusesAPersonsBranch(t *testing.T) {
	f := newFixture(t)
	f.inGate[repo+"\x00"+branch] = true
	var taken *BranchTakenError
	_, err := f.queue.Create(context.Background(), delivery(""))
	if !errors.As(err, &taken) || !errors.Is(err, ErrBranchTaken) || taken.Held != HeldByGate || taken.Branch != branch {
		t.Fatalf("a branch in the gate = %v, want *BranchTakenError held by the gate", err)
	}
	if !IsRefusal(err) {
		t.Fatalf("a taken branch is not reported as a refusal: %v", err)
	}
	delete(f.inGate, repo+"\x00"+branch)

	f.runOn(t, "run-person", store.RunPassed)
	_, err = f.queue.Create(context.Background(), delivery(""))
	if !errors.As(err, &taken) || taken.Held != HeldByRun || taken.Run != "run-person" {
		t.Fatalf("a branch with a run = %v, want *BranchTakenError naming the run", err)
	}
	if tasks, _ := f.store.Tasks(context.Background()); len(tasks) != 0 {
		t.Fatalf("a refused intake recorded %d tasks", len(tasks))
	}

	// A different branch is neither held nor refused.
	req := delivery("")
	req.Branch = "free"
	free := f.create(t, req)
	// And once a task names a branch, a run and a gate ref on it are the
	// task's own and the floor decides.
	f.inGate[repo+"\x00free"] = true
	again := f.create(t, req)
	if again.Cause != CauseFloor || again.Task.BlockerTask.Or("") != free.Task.ID {
		t.Fatalf("a task's own branch with a gate ref was recorded as %+v, want the floor", again)
	}
}

// A malformed request is refused with ErrRequest before anything is read or
// written: an unknown shape, an investigation task naming a branch, a
// delivery task naming none, empty instructions, an unknown repository, an
// unknown overlap.
func TestIntakeRefusesAMalformedRequest(t *testing.T) {
	f := newFixture(t)
	cases := map[string]Request{
		"shape":                {Shape: "chore", Project: "one", Instructions: "x", Repository: repo, Branch: branch},
		"investigation+branch": {Shape: "investigation", Project: "one", Instructions: "x", Branch: branch},
		"delivery-branch":      {Shape: "delivery", Project: "one", Instructions: "x", Repository: repo},
		"delivery-repository":  {Shape: "delivery", Project: "one", Instructions: "x", Branch: branch},
		"instructions":         {Shape: "delivery", Project: "one", Repository: repo, Branch: branch},
		"project":              {Shape: "delivery", Instructions: "x", Repository: repo, Branch: branch},
		"branch-option":        {Shape: "delivery", Project: "one", Instructions: "x", Repository: repo, Branch: "--force"},
	}
	for name, req := range cases {
		if _, err := f.queue.Create(context.Background(), req); !errors.Is(err, ErrRequest) {
			t.Errorf("%s: Create = %v, want ErrRequest", name, err)
		}
	}
	if _, err := f.queue.Create(context.Background(), Request{
		Shape: "delivery", Project: "one", Instructions: "x", Repository: "nowhere", Branch: branch,
	}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("an unknown repository = %v, want store.ErrNotFound", err)
	}
	if _, err := f.queue.Create(context.Background(), delivery("neveraaaaaaa")); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("an unknown overlap = %v, want store.ErrNotFound", err)
	}
	if tasks, _ := f.store.Tasks(context.Background()); len(tasks) != 0 {
		t.Fatalf("a refused intake recorded %d tasks", len(tasks))
	}
}

// An investigation task takes no exclusion, names no branch, may name a
// repository, and is dispatched or queued on its claim alone.
func TestAnInvestigationTaskIsOutsideTheFloor(t *testing.T) {
	f := newFixture(t)
	held := f.create(t, delivery(""))
	before := f.exclusion.taken
	got := f.create(t, Request{Shape: "investigation", Project: "one", Instructions: "look", Repository: repo})
	if f.exclusion.taken != before {
		t.Fatal("an investigation intake took a branch exclusion")
	}
	if got.Cause != CauseIndependent || got.Task.Branch.IsKnown() || got.Task.RepositoryID.Or("") != repo {
		t.Fatalf("an investigation task was recorded as %+v", got)
	}
	if got.Task.Shape != store.TaskInvestigation {
		t.Fatalf("the shape was recorded as %q", got.Task.Shape)
	}
	queued := f.create(t, Request{Shape: "investigation", Project: "one", Instructions: "look", Overlaps: held.Task.ID})
	if queued.Cause != CauseClaimed || queued.Task.RepositoryID.IsKnown() {
		t.Fatalf("an investigation task claiming an overlap was recorded as %+v", queued)
	}
}

// Two concurrent intakes on one branch, under the race detector, held at the
// point between their read and their write. Under the exclusion the second
// cannot reach that point until the first has written, so one dispatches and
// the other is queued behind it. With the exclusion replaced by nothing, both
// read an empty branch and both decide to dispatch, and the store's anchor
// then refuses one with ErrTaskMoved. The positive control is the shape P6
// forbids, an anchor read immediately before the write: it lands two
// dispatched tasks on one branch.
func TestTwoIntakesOnOneBranchDispatchOne(t *testing.T) {
	principles.Cite(t, principles.P6)
	ctx := context.Background()

	t.Run("under the exclusion", func(t *testing.T) {
		f := newFixture(t)
		results := race(func() error { _, err := f.queue.Create(ctx, delivery("")); return err })
		for _, err := range results {
			if err != nil {
				t.Fatalf("an intake under the exclusion failed: %v", err)
			}
		}
		dispatched, queued := f.countDispositions(t)
		if dispatched != 1 || queued != 1 {
			t.Fatalf("%d dispatched and %d queued, want one of each", dispatched, queued)
		}
	})

	t.Run("without the exclusion", func(t *testing.T) {
		f := newFixture(t)
		f.queue.hold = func(context.Context, string, string) (func(), error) { return func() {}, nil }
		f.queue.betweenReadAndWrite = barrier(2)
		results := race(func() error { _, err := f.queue.Create(ctx, delivery("")); return err })
		var moved, landed int
		for _, err := range results {
			switch {
			case err == nil:
				landed++
			case errors.Is(err, store.ErrTaskMoved):
				moved++
			default:
				t.Fatalf("an intake failed for a reason other than the anchor: %v", err)
			}
		}
		if landed != 1 || moved != 1 {
			t.Fatalf("%d landed and %d were refused as moved, want one of each", landed, moved)
		}
		if dispatched, queued := f.countDispositions(t); dispatched != 1 || queued != 0 {
			t.Fatalf("%d dispatched and %d queued after the race", dispatched, queued)
		}
	})

	t.Run("control: an anchor read at the write", func(t *testing.T) {
		f := newFixture(t)
		arrive := barrier(2)
		var writing sync.Mutex
		results := race(func() error {
			resolved, err := f.queue.reader.All(ctx)
			if err != nil {
				return err
			}
			for _, r := range resolved {
				if Live(r.State.State) {
					return errors.New("the branch already had a live task")
				}
			}
			arrive()
			// The tip, read immediately before the write under a lock that
			// covers the write and not the decision, always matches.
			writing.Lock()
			defer writing.Unlock()
			tasks, err := f.store.Tasks(ctx)
			if err != nil {
				return err
			}
			var observed int64
			for _, task := range tasks {
				observed = max(observed, task.Row)
			}
			id, err := home.NewTaskID()
			if err != nil {
				return err
			}
			copyPath, _ := f.home.TaskCopy(id)
			_, err = f.store.CreateTask(ctx, store.Task{
				ID: id, Shape: store.TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: copyPath,
				RepositoryID: store.Known(repo), Branch: store.Known(branch),
			}, store.Disposition{Observed: observed})
			return err
		})
		for _, err := range results {
			if err != nil {
				t.Fatalf("the control failed: %v", err)
			}
		}
		if dispatched, queued := f.countDispositions(t); dispatched != 2 || queued != 0 {
			t.Fatalf("the control produced %d dispatched and %d queued, so it discriminates nothing", dispatched, queued)
		}
	})
}

// countDispositions reads every task's state through the resolver and counts
// the dispatched and the queued.
func (f *fixture) countDispositions(t *testing.T) (dispatched, queued int) {
	t.Helper()
	resolved, err := f.queue.reader.All(context.Background())
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	for _, r := range resolved {
		switch r.State.State {
		case taskstate.Dispatched:
			dispatched++
		case taskstate.Queued:
			queued++
		default:
			t.Fatalf("task %s resolves %s", r.Record.ID, r.State.State)
		}
	}
	return dispatched, queued
}

// A queued successor resolves Queued while its blocker's run is running: the
// blocker's attempt makes the run the blocker's, and the successor has no
// attempt to be attributed by. This is internal/store's attribution test
// repeated here as the consumer whose release depends on it.
func TestTheQueuedSuccessorIsNotAttributedTheBlockersRun(t *testing.T) {
	f := newFixture(t)
	blocker := f.create(t, delivery(""))
	successor := f.create(t, delivery(""))
	f.beginAttempt(t, blocker.Task.ID)
	f.runOn(t, "run-blocker", store.RunRunning)

	if got := f.resolve(t, blocker.Task.ID); got.State != taskstate.Validating || got.Source != taskstate.SourceRun {
		t.Fatalf("the blocker resolves %s from %s, want validating from its run", got.State, got.Source)
	}
	if got := f.resolve(t, successor.Task.ID); got.State != taskstate.Queued || got.Source != taskstate.SourceEvent {
		t.Fatalf("the successor resolves %s from %s, want queued from its intake", got.State, got.Source)
	}
	outcomes, err := f.queue.Release(context.Background())
	if err != nil || len(outcomes) != 1 || outcomes[0].Result != Waiting || outcomes[0].BlockerState != taskstate.Validating {
		t.Fatalf("Release = %+v (%v), want the successor waiting on a validating blocker", outcomes, err)
	}
}
