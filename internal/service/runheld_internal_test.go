package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
)

// A task's run moving to held wakes the coordinator, though it registers no
// hold record: the run of the task's own branch, created after the task's
// attempt began, is moved to held through the store's transition, which is
// the move internal/runs' Hold makes when a stage halts, and a wait the
// coordinator armed before the loop's pass answers with a hold-opened wake
// naming the task, keyed by the run and its first move into held. The
// controls: once the coordinator acknowledges it and an answer releases the
// run, a pass while it runs again surfaces no hold, so the detection reads
// the run's status and not that it ever held; and when a later stage holds
// it again, that is a second hold and a second wake, keyed by the second
// move.
//
// It cites P8, because the hold is read off the run's record and its count
// of moves into held, never off a history, and P9, because a run moved to
// held is one of the two kinds PRD section 5 has surface always.
func TestATasksRunMovingToHeldWakesTheCoordinator(t *testing.T) {
	principles.Cite(t, principles.P8)
	principles.Cite(t, principles.P9)
	h, records := loopHome(t)
	ctx := t.Context()
	if _, err := records.UpsertRepository(ctx, store.Repository{
		ID: "subject", WorkingPath: "/checkouts/one", UpstreamURL: "https://example.test/one.git", DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("recording the repository: %v", err)
	}
	const task = "runheldtaskk"
	if _, err := records.CreateTask(ctx, store.Task{
		ID: task, Shape: store.TaskDelivery, Project: "one", Mode: "headless", WorktreePath: "/w/" + task,
		RepositoryID: store.Known("subject"), Branch: store.Known("task/held"),
	}, store.Disposition{}); err != nil {
		t.Fatalf("creating the task: %v", err)
	}
	if _, err := records.AppendTaskEvent(ctx, task, "started", ""); err != nil {
		t.Fatalf("starting the task: %v", err)
	}
	if _, err := records.BeginTaskAttempt(ctx, task, 0); err != nil {
		t.Fatalf("beginning the attempt: %v", err)
	}
	build, _ := store.CurrentBuild()
	run, err := records.CreateRun(ctx, store.Run{
		ID: "run-of-task", RepositoryID: "subject", Branch: "task/held", SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "validate", IntentSource: "push", Build: build, ConfigDigest: "cfg",
	})
	if err != nil {
		t.Fatalf("creating the run: %v", err)
	}
	if _, err := records.TransitionRun(ctx, run.ID, []store.RunStatus{store.RunPending}, store.RunRunning); err != nil {
		t.Fatalf("starting the run: %v", err)
	}

	l := openLoop(t, h, records, nil, nil)
	served := make(chan error, 1)
	go func() { served <- l.service.Serve(context.Background()) }()
	defer func() {
		_ = l.service.Close()
		<-served
	}()
	client, err := ipc.Dial(ctx, l.service.Socket(), ipc.ClientConfig{})
	if err != nil {
		t.Fatalf("dialing the service: %v", err)
	}
	defer func() { _ = client.Close() }()

	l.pass(t)
	if got := wakesOfKind(t, l.records(t), task, supervisor.KindHoldOpened); len(got) != 0 {
		t.Fatalf("a running run surfaced a hold: %+v", got)
	}

	first := holdWokenBy(t, l, client, run.ID, task, 1)
	if err := l.records(t).AcknowledgeWakes(ctx, []int64{first.ID}, store.AcknowledgedByCoordinator()); err != nil {
		t.Fatalf("acknowledging: %v", err)
	}
	if _, err := l.records(t).TransitionRun(ctx, run.ID, []store.RunStatus{store.RunHeld}, store.RunRunning); err != nil {
		t.Fatalf("releasing the run: %v", err)
	}
	l.pass(t)
	if got := wakesOfKind(t, l.records(t), task, supervisor.KindHoldOpened); len(got) != 1 {
		t.Fatalf("a released run surfaced another hold: %+v", got)
	}
	holdWokenBy(t, l, client, run.ID, task, 2)
}

// holdWokenBy moves the run to held, closes whatever else the queue holds,
// makes the loop take a pass, and returns the hold-opened wake a
// coordinator's wait is handed, failing unless it names the task and the
// run's move into held number moves. Every other row a wait answers with
// is acknowledged, the way a coordinator acts on it, so the next wait is
// answered by what the pass appended.
func holdWokenBy(t *testing.T, l *loopSubject, client *ipc.Client, runID, task string, moves int64) store.Wake {
	t.Helper()
	ctx := t.Context()
	held, err := l.records(t).TransitionRun(ctx, runID, []store.RunStatus{store.RunRunning}, store.RunHeld)
	if err != nil || held.HeldMoves != store.Known(moves) {
		t.Fatalf("moving the run to held: %+v, %v", held.HeldMoves, err)
	}
	acknowledgeQueued(t, l.records(t))
	l.pass(t)
	want := fmt.Sprintf("run-held:%s:%d", runID, moves)
	deadline := time.Now().Add(loopWait)
	for time.Now().Before(deadline) {
		var out machine.Wakes
		if err := client.Call(ctx, ipc.MethodWakesWait, machine.WakeWaitRequest{TimeoutSeconds: 5}, &out); err != nil {
			t.Fatalf("wakes.wait: %v", err)
		}
		var others []int64
		for _, w := range out.Wakes {
			if w.TaskID == task && w.Kind == string(supervisor.KindHoldOpened) && w.Marker == want {
				return w
			}
			others = append(others, w.ID)
		}
		if len(others) > 0 {
			if err := l.records(t).AcknowledgeWakes(ctx, others, store.AcknowledgedByCoordinator()); err != nil {
				t.Fatalf("acknowledging: %v", err)
			}
		}
	}
	t.Fatalf("no wait was answered with a hold-opened wake of task %s marked %s", task, want)
	return store.Wake{}
}

// acknowledgeQueued closes every queued row, as a coordinator that acted on
// all of them would.
func acknowledgeQueued(t *testing.T, records *store.Store) {
	t.Helper()
	queued, err := records.QueuedWakes(t.Context(), machine.DefaultWakesLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) == 0 {
		return
	}
	ids := make([]int64, len(queued))
	for i, w := range queued {
		ids[i] = w.ID
	}
	if err := records.AcknowledgeWakes(t.Context(), ids, store.AcknowledgedByCoordinator()); err != nil {
		t.Fatal(err)
	}
}

// Creating a task wakes nobody for the program's own entries: the intake's
// queued entry and the dispatch's dispatched entry, which CreateTask appends
// for an independent task, are detected and absorbed as routine, and the
// queue holds no wake for either. The positive control is a worker's note
// appended after them, an event with no decision verb that is not routine,
// which the next pass surfaces for want of evidence.
//
// It cites P9: what is absorbed is absorbed on the entry's own meaning, and
// an entry that does not carry one still surfaces.
func TestCreatingATaskQueuesNoWakeForItsOwnEntries(t *testing.T) {
	principles.Cite(t, principles.P9)
	h, records := loopHome(t)
	ctx := t.Context()
	const task = "createdtaskk"
	if _, err := records.CreateTask(ctx, store.Task{
		ID: task, Shape: store.TaskInvestigation, Project: "one", Mode: "headless", WorktreePath: "/w/" + task,
	}, store.Disposition{}); err != nil {
		t.Fatalf("creating the task: %v", err)
	}
	if _, err := records.BeginTaskAttempt(ctx, task, 0); err != nil {
		t.Fatalf("beginning the attempt: %v", err)
	}
	l := openLoop(t, h, records, nil, nil)
	defer func() { _ = l.service.Close() }()

	first := l.freshPass(t)
	events := detectionsOf(first, task, supervisor.KindPlainEvent)
	if len(events) != 2 {
		t.Fatalf("the pass detected %+v, want the queued and dispatched entries", events)
	}
	for _, d := range events {
		if d.Verdict.Level != supervisor.Absorb || d.Verdict.Reason != supervisor.ReasonRoutine {
			t.Fatalf("the program's own entry %s was %+v, want absorbed as routine", d.Marker, d.Verdict)
		}
	}
	if rows := wakesOfKind(t, l.records(t), task, supervisor.KindPlainEvent); len(rows) != 0 {
		t.Fatalf("creating a task queued %+v, want no wake for its own entries", rows)
	}

	if _, err := l.records(t).AppendTaskEvent(ctx, task, "progress-note", "read the code"); err != nil {
		t.Fatalf("appending the worker's note: %v", err)
	}
	// A pass already under way when the note was appended may report
	// first, so passes are taken until one has read it.
	var rows []store.Wake
	for i := 0; i < 5 && len(rows) == 0; i++ {
		l.pass(t)
		rows = wakesOfKind(t, l.records(t), task, supervisor.KindPlainEvent)
	}
	if len(rows) != 1 || rows[0].Marker != "event:3" {
		t.Fatalf("after the worker's note the queue holds %+v, want one wake for event 3", rows)
	}
}
