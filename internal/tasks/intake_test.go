package tasks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
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
	for _, bad := range []string{"a~b", "a^b", "a:b", "a?b", "a*b", "a[b", "a\\b", "a@{b", "/a", "a/", ".a", "a.lock", "a\x01b", "a b",
		"@", "a//b", "a/.b", "a./b", "a.lock/b", "a."} {
		req := delivery("")
		req.Branch = bad
		if _, err := f.queue.Create(context.Background(), req); !errors.Is(err, ErrRequest) {
			t.Errorf("branch %q: Create = %v, want ErrRequest", bad, err)
		}
	}
	if tasks, _ := f.store.Tasks(context.Background()); len(tasks) != 0 {
		t.Fatalf("a refused intake recorded %d tasks", len(tasks))
	}
	// An unknown overlap is refused on a live branch too, where the floor
	// would otherwise have decided without looking at the claim.
	f.create(t, delivery(""))
	if _, err := f.queue.Create(context.Background(), delivery("neveraaaaaaa")); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("an unknown overlap on a live branch = %v, want store.ErrNotFound", err)
	}
	if tasks, _ := f.store.Tasks(context.Background()); len(tasks) != 1 {
		t.Fatalf("a refused intake on a live branch recorded a task: %d tasks", len(tasks))
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
		// The first caller is held between its read and its write until the
		// second has asked for the exclusion, so the second is queued at the
		// exclusion while the first's decision is still unwritten: only the
		// exclusion keeps the second from reading an empty branch too.
		f.exclusion.expect, f.exclusion.arrived = 2, make(chan struct{})
		f.queue.betweenReadAndWrite = func() { <-f.exclusion.arrived }
		var causes [2]Cause
		var i int
		results := race(func() error {
			got, err := f.queue.Create(ctx, delivery(""))
			if err != nil {
				return err
			}
			f.exclusion.mu.Lock()
			causes[i], i = got.Cause, i+1
			f.exclusion.mu.Unlock()
			return nil
		})
		for _, err := range results {
			if err != nil {
				t.Fatalf("an intake under the exclusion failed: %v", err)
			}
		}
		if (causes != [2]Cause{CauseIndependent, CauseFloor}) && (causes != [2]Cause{CauseFloor, CauseIndependent}) {
			t.Fatalf("the two intakes were %v, want one independent and one queued by the floor", causes)
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

// A task names a configured worker role, and that name is what the record
// holds. review, fix, and a name no role has are refused before a task exists.
func TestIntakeRecordsAWorkerRoleAndRefusesTheOthers(t *testing.T) {
	f := newFixture(t)
	f.queue.roles = config.Roles{"pages": {Agent: "claude"}}
	req := delivery("")
	req.Role = "review"
	if _, err := f.queue.Create(context.Background(), req); !errors.Is(err, ErrRequest) {
		t.Fatalf("a task naming review was created: %v", err)
	}
	req.Role = "missing"
	if _, err := f.queue.Create(context.Background(), req); !errors.Is(err, ErrRequest) {
		t.Fatalf("a task naming an unknown role was created: %v", err)
	}
	req.Branch = "task/pages"
	req.Role = "pages"
	got := f.create(t, req)
	if got.Task.Role != "pages" {
		t.Fatalf("the task records role %q, want pages", got.Task.Role)
	}
}

// An intake's instructions file is written whole: a write that fails
// partway leaves nothing under the file's name and no partial file that a
// later read takes for it, and CompleteIntake then writes the file for the
// row the stop left, while leaving a file that stands as it is. Without the
// whole write a partial file under the name is taken for the instructions.
func TestIntakeWritesItsInstructionsWholeAndCompleteIntakeFillsAGap(t *testing.T) {
	principles.Cite(t, principles.P6)
	f := newFixture(t)
	ctx := context.Background()
	stopped := errors.New("the service stopped inside the intake")
	if err := f.queue.InterruptAfterRecordForTest(t, func() error { return stopped }); err != nil {
		t.Fatal(err)
	}
	req := delivery("")
	req.Branch = "task/stopped"
	if _, err := f.queue.Create(ctx, req); !errors.Is(err, stopped) {
		t.Fatalf("the interrupted intake = %v; want the stop", err)
	}
	all, err := f.store.Tasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	for _, task := range all {
		if task.Branch.Or("") == "task/stopped" {
			id = task.ID
		}
	}
	if id == "" {
		t.Fatal("the interrupted intake wrote no row")
	}
	path, err := f.home.TaskInstructions(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the interrupted intake left a file under the instructions' name: %v", err)
	}
	// The whole write itself: a path whose directory refuses the rename
	// leaves nothing under the name, and a successful one leaves the whole
	// content and no file beside it.
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeWhole(filepath.Join(dir, "missing", "x"), []byte("x")); err == nil {
		t.Fatal("a write into a directory that is not there succeeded")
	}
	if err := writeWhole(path, []byte("whole\n")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != filepath.Base(path) {
			t.Fatalf("the whole write left %s beside the file", e.Name())
		}
	}
	// A second write over the same path goes through the same one partial
	// file and leaves nothing beside it either.
	if err := writeWhole(path, []byte("whole again\n")); err != nil {
		t.Fatal(err)
	}
	if again, err := os.ReadDir(dir); err != nil || len(again) != 1 {
		t.Fatalf("a second whole write left %d entries (%v)", len(again), err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	// A stop between the partial file's write and its rename: the partial
	// file stands under its one fixed name and nothing under the
	// instructions' name.
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), partialPrefix(path)+"-stopped"), []byte("Do the wo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.queue.InterruptAfterRecordForTest(t, nil); err != nil {
		t.Fatal(err)
	}
	completed, err := f.queue.CompleteIntake(ctx, id, "Do the work.\n")
	if err != nil || completed != path {
		t.Fatalf("CompleteIntake = %q, %v; want the file written at %s", completed, err, path)
	}
	if text, err := os.ReadFile(path); err != nil || string(text) != "Do the work.\n" {
		t.Fatalf("the completed file reads %q, %v", text, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), partialPrefix(path)+"-stopped")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("CompleteIntake left the partial file beside the instructions: %v", err)
	}
	// A partial file a stop left beside a whole one is removed too.
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), partialPrefix(path)+"-stopped"), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.queue.CompleteIntake(ctx, id, "Do the work.\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), partialPrefix(path)+"-stopped")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("CompleteIntake left a stale partial file beside a whole one: %v", err)
	}
	if _, err := f.queue.CompleteIntake(ctx, id, "something else\n"); err != nil {
		t.Fatal(err)
	}
	if text, _ := os.ReadFile(path); string(text) != "Do the work.\n" {
		t.Fatalf("CompleteIntake rewrote a file that stood: %q", text)
	}
	if _, err := f.queue.CompleteIntake(ctx, "tasknobodyaa", "x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("CompleteIntake of a task with no row = %v; want ErrNotFound", err)
	}
}

// Intake's file writes never go through a link. The reviewer's case: a row
// with no instructions file and a link at the old fixed partial name to a
// file holding uncommitted work; CompleteIntake writes its own file under
// the instructions' name, removes the link as a name, and leaves the
// target's contents and the target itself untouched. A regular partial file
// planted beside is removed. And an instructions file that stands as a link
// is refused with the typed error, its target untouched and nothing renamed
// onto it. Without the exclusive create the link's target is overwritten;
// without the link check the linked instructions are accepted.
func TestIntakeWritesNeverGoThroughALink(t *testing.T) {
	principles.Cite(t, principles.P6)
	f := newFixture(t)
	ctx := context.Background()
	stopped := errors.New("the service stopped inside the intake")
	if err := f.queue.InterruptAfterRecordForTest(t, func() error { return stopped }); err != nil {
		t.Fatal(err)
	}
	req := delivery("")
	req.Branch = "task/linked"
	if _, err := f.queue.Create(ctx, req); !errors.Is(err, stopped) {
		t.Fatalf("the interrupted intake = %v; want the stop", err)
	}
	if err := f.queue.InterruptAfterRecordForTest(t, nil); err != nil {
		t.Fatal(err)
	}
	all, err := f.store.Tasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	for _, task := range all {
		if task.Branch.Or("") == "task/linked" {
			id = task.ID
		}
	}
	path, err := f.home.TaskInstructions(id)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(t.TempDir(), "uncommitted.txt")
	if err := os.WriteFile(work, []byte("uncommitted work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	planted := filepath.Join(dir, "."+filepath.Base(path)+".partial")
	if err := os.Symlink(work, planted); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, "."+filepath.Base(path)+".partial-stale")
	if err := os.WriteFile(stale, []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := f.queue.CompleteIntake(ctx, id, "Do the work.\n"); err != nil {
		t.Fatalf("CompleteIntake: %v", err)
	}
	if text, err := os.ReadFile(work); err != nil || string(text) != "uncommitted work\n" {
		t.Fatalf("the link's target reads %q, %v; want it untouched", text, err)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		t.Fatalf("the instructions stand as %v, %v; want a file of their own", info, err)
	}
	if text, err := os.ReadFile(path); err != nil || string(text) != "Do the work.\n" {
		t.Fatalf("the instructions read %q, %v", text, err)
	}
	for _, p := range []string{planted, stale} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s was left beside the instructions: %v", filepath.Base(p), err)
		}
	}

	// The instructions file itself a link: refused, the target untouched.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(work, path); err != nil {
		t.Fatal(err)
	}
	if _, err := f.queue.CompleteIntake(ctx, id, "Do the work.\n"); !errors.Is(err, ErrInstructionsLink) {
		t.Fatalf("CompleteIntake over a linked instructions file = %v; want ErrInstructionsLink", err)
	}
	if err := writeWhole(path, []byte("through the link\n")); !errors.Is(err, ErrInstructionsLink) {
		t.Fatalf("writeWhole over a linked path = %v; want ErrInstructionsLink", err)
	}
	if text, err := os.ReadFile(work); err != nil || string(text) != "uncommitted work\n" {
		t.Fatalf("the link's target reads %q, %v; want it untouched", text, err)
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the refused write replaced the link: %v, %v", info, err)
	}
}

// A note is appended to the instructions once and whole: the first append
// writes it after a blank line and a second leaves the file as it is; a
// note an earlier append left partial is completed, not taken for the note
// and not written twice; an earlier section under the same heading, another
// note, is no reason not to write this one. A write that fails leaves the
// file as it was, so the next call writes the note: the positive control
// for the failure is a directory that refuses the write, after which the
// file still reads as before and no partial is left beside it. A note not
// beginning with its heading, and a path standing as a symbolic link, are
// refused.
func TestANoteIsAppendedToTheInstructionsOnceAndWhole(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	const heading = "## A note, written by the program"
	const note = heading + "\n\nWhat the worker is told, in full.\n"
	const written = "What the coordinator wrote.\n"
	dir := t.TempDir()
	path := filepath.Join(dir, "instructions.md")
	read := func() string {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	write := func(text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(written)
	for range 2 {
		if err := AppendNote(path, heading, note); err != nil {
			t.Fatal(err)
		}
		if got := read(); got != written+"\n\n"+note {
			t.Fatalf("after the append the file reads %q; want the note once", got)
		}
	}
	for _, cut := range []int{len("\n\n" + heading), len("\n\n"+note) - 1} {
		write(written + ("\n\n" + note)[:cut])
		if err := AppendNote(path, heading, note); err != nil {
			t.Fatal(err)
		}
		if got := read(); got != written+"\n\n"+note {
			t.Fatalf("a note left %d bytes long was completed to %q; want the note once, whole", cut, got)
		}
	}
	earlier := "\n\n" + heading + "\n\nAn earlier task's note.\n"
	write(written + earlier)
	if err := AppendNote(path, heading, note); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != written+earlier+"\n\n"+note {
		t.Fatalf("with an earlier note under the heading the file reads %q; want this note after it", got)
	}

	write(written)
	if os.Geteuid() != 0 {
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		err := AppendNote(path, heading, note)
		if chErr := os.Chmod(dir, 0o700); chErr != nil {
			t.Fatal(chErr)
		}
		if err == nil {
			t.Fatal("an append into a directory that refuses the write reported success")
		}
		if got := read(); got != written {
			t.Fatalf("after the failed append the file reads %q; want it as it was", got)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 1 {
			t.Fatalf("the failed append left %d entries beside the file", len(entries)-1)
		}
		if err := AppendNote(path, heading, note); err != nil || read() != written+"\n\n"+note {
			t.Fatalf("the next append = %v, the file %q; want the note written", err, read())
		}
	} else {
		t.Log("running as root, a directory's mode refuses no write, so a failed append is not exercised here")
	}

	if err := AppendNote(path, heading, "Not under its heading.\n"); err == nil {
		t.Fatal("a note not beginning with its heading was appended")
	}
	link := filepath.Join(dir, "link.md")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err := AppendNote(link, heading, note); !errors.Is(err, ErrInstructionsLink) {
		t.Fatalf("an append through a link = %v; want ErrInstructionsLink", err)
	}
}
