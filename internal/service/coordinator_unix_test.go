//go:build unix

package service_test

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// These tests drive the coordinator's three task methods over the service's
// real socket. The refusals are asked by a launched worker, as a command it
// runs in a process group of its own, on workers_unix_test.go's terms, so
// the credentials the service places are the kernel's; each refusal's positive
// control is the same request from this test's own process, which is in no
// worker group, and what each one did is read back from the store rather
// than taken from the answer.

// attemptsOf is the task's attempt rows.
func attemptsOf(t *testing.T, h *home.Home, id string) []store.TaskAttempt {
	t.Helper()
	attempts, err := recordsOf(t, h).TaskAttempts(t.Context(), id)
	if err != nil {
		t.Fatalf("reading the attempts of task %s: %v", id, err)
	}
	return attempts
}

// launchedAttempt waits for the task's first attempt to record a session,
// which launch.go writes after dispatch.go has appended the second check to
// the task's instructions, on the same pass: once it is recorded, the
// instructions file is final for that attempt.
func launchedAttempt(t *testing.T, h *home.Home, id string) store.TaskAttempt {
	t.Helper()
	deadline := time.Now().Add(launchWait)
	for {
		attempts := attemptsOf(t, h, id)
		if len(attempts) > 1 {
			t.Fatalf("task %s has attempts %+v, want the dispatch's alone", id, attempts)
		}
		if len(attempts) == 1 {
			if _, ok := attempts[0].SessionRef.Get(); ok {
				return attempts[0]
			}
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("task %s never recorded a launched session; its attempts are %+v", id, attempts)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// kindsOf is the task's history as a list of kinds, in sequence order.
func kindsOf(t *testing.T, h *home.Home, id string) []string {
	t.Helper()
	events := historyOf(t, h, id)
	kinds := make([]string, len(events))
	for i, e := range events {
		kinds[i] = e.Kind
	}
	return kinds
}

// task.create is the coordinator's. From the worker it is
// refused with the worker code and no task is recorded; from this process,
// in no worker group, the same request records an investigation task
// through intake, with the instructions written where intake writes them.
// A delivery request onto the branch the launched worker's task holds is
// queued behind that task by intake's floor, which is how the test knows
// the method reached Create rather than a second implementation of it, and
// a request intake cannot read is refused as invalid.
func TestTaskCreateIsTheCoordinatorsAndNotAWorkers(t *testing.T) {
	t.Parallel()
	launchedWorker(t, "task/create", func(w launched) {
		running := w.running
		investigation := machine.TaskCreateRequest{
			Shape: "investigation", Project: "one", Instructions: "Find out why the build is slow.\n",
			Repository: w.repository.ID,
		}
		before := taskCount(t, w.home)
		if _, code := callAs(t, w, ipc.MethodTaskCreate, investigation); code != ipc.CodeWorker {
			t.Fatalf("task.create from the worker answered %q, want %q", code, ipc.CodeWorker)
		}
		if got := taskCount(t, w.home); got != before {
			t.Fatalf("a refused task.create recorded a task: %d tasks, want %d", got, before)
		}

		var created machine.TaskCreated
		if err := running.client.Call(t.Context(), ipc.MethodTaskCreate, investigation, &created); err != nil {
			t.Fatalf("task.create from outside every worker group: %v", err)
		}
		if created.Record.Shape != store.TaskInvestigation || created.Cause != "independent" {
			t.Fatalf("task.create answered %+v, want an independent investigation task", created)
		}
		stored, err := recordsOf(t, w.home).Task(t.Context(), created.Record.ID)
		if err != nil || stored.Shape != store.TaskInvestigation || stored.RepositoryID.Or("") != w.repository.ID {
			t.Fatalf("the store holds %+v (%v) for the created task, want an investigation of %s", stored, err, w.repository.ID)
		}
		// The file is read once the launch record says it is final: dispatch
		// appends the second check after this answer, so a read before that
		// record is a race with the dispatch pass (#460).
		launchedAttempt(t, w.home, created.Record.ID)
		raw, err := os.ReadFile(created.Instructions)
		if err != nil {
			t.Fatalf("reading the instructions file: %v", err)
		}
		appended, ok := strings.CutPrefix(string(raw), investigation.Instructions)
		if !ok || !strings.HasPrefix(appended, "\n\n"+service.SecondCheckHeading+"\n") ||
			strings.Count(string(raw), service.SecondCheckHeading) != 1 {
			t.Fatalf("the instructions file reads %q, want the request's followed by the second check once", raw)
		}

		var queued machine.TaskCreated
		delivery := machine.TaskCreateRequest{
			Shape: "delivery", Project: "one", Instructions: "Continue on the same branch.\n",
			Repository: w.repository.ID, Branch: "task/create",
		}
		if err := running.client.Call(t.Context(), ipc.MethodTaskCreate, delivery, &queued); err != nil {
			t.Fatalf("task.create of a second delivery on the branch: %v", err)
		}
		if queued.Cause != "floor" || queued.Record.BlockerTask.Or("") != w.task {
			t.Fatalf("a delivery on a live task's branch was recorded %+v, want it queued behind task %s by the floor", queued, w.task)
		}

		var refused machine.TaskCreated
		err = running.client.Call(t.Context(), ipc.MethodTaskCreate,
			machine.TaskCreateRequest{Shape: "delivery", Project: "one", Instructions: "x", Repository: w.repository.ID}, &refused)
		if !errors.Is(err, ipc.ErrInvalidRequest) {
			t.Fatalf("a delivery naming no branch = %v, want ErrInvalidRequest", err)
		}
	})
}

// task.promote is the coordinator's, so an investigation cannot promote
// itself: the request from the investigation's own worker, naming its own
// task, is refused with the worker code and the task is still an
// investigation with no promoted entry. The same request from this process
// promotes it in place: the store holds a delivery task on the branch, the
// history carries the promoted entry the answer names, and the copy now
// has the gate as its assistant remote. Asked again, it is refused by the
// records, because the task is no longer an investigation.
func TestAnInvestigationCannotPromoteItselfAndTheCoordinatorCan(t *testing.T) {
	t.Parallel()
	launchedWorkerOf(t, store.TaskInvestigation, "", func(w launched) {
		running, task := w.running, w.task
		promotion := machine.TaskPromoteRequest{
			Task: task, Repository: w.repository.ID, Branch: "task/promoted", Intent: "Fix the slow build.",
		}
		if _, code := callAs(t, w, ipc.MethodTaskPromote, promotion); code != ipc.CodeWorker {
			t.Fatalf("task.promote from the investigation's own worker answered %q, want %q", code, ipc.CodeWorker)
		}
		if stored, err := recordsOf(t, w.home).Task(t.Context(), task); err != nil || stored.Shape != store.TaskInvestigation {
			t.Fatalf("after the refused self-promotion the task is %+v (%v), want still an investigation", stored, err)
		}
		for _, kind := range kindsOf(t, w.home, task) {
			if kind == "promoted" {
				t.Fatal("the refused self-promotion left a promoted entry in the history")
			}
		}

		var promoted machine.TaskPromoted
		if err := running.client.Call(t.Context(), ipc.MethodTaskPromote, promotion, &promoted); err != nil {
			t.Fatalf("task.promote from outside every worker group: %v", err)
		}
		stored, err := recordsOf(t, w.home).Task(t.Context(), task)
		if err != nil || stored.Shape != store.TaskDelivery || stored.Branch.Or("") != "task/promoted" {
			t.Fatalf("after the promotion the store holds %+v (%v), want a delivery task on task/promoted", stored, err)
		}
		events := historyOf(t, w.home, task)
		if last := events[len(events)-1]; last != promoted.Event || last.Kind != "promoted" {
			t.Fatalf("the history ends with %+v, want the promoted entry the answer names, %+v", last, promoted.Event)
		}
		copyPath := resolvedCopy(t, w.home, task)
		if remotes := strings.Fields(git(t, copyPath, "remote")); len(remotes) != 1 || remotes[0] != gate.RemoteName {
			t.Fatalf("the promoted copy has remotes %v, want only %s", remotes, gate.RemoteName)
		}
		if branch := git(t, copyPath, "rev-parse", "--abbrev-ref", "HEAD"); strings.TrimSpace(branch) != "task/promoted" {
			t.Fatalf("the promoted copy is on %q, want task/promoted", branch)
		}

		var again machine.TaskPromoted
		if err := running.client.Call(t.Context(), ipc.MethodTaskPromote, promotion, &again); !errors.Is(err, ipc.ErrRefused) {
			t.Fatalf("promoting a delivery task = %v, want ErrRefused", err)
		}
	})
}

// PRD principle P6 at the coordinator's relaunch: task.relaunch goes
// through the anchored relaunch reconciliation uses, and its record is that
// relaunch's. From the worker it is refused with the worker
// code; from this process, while the worker runs, it is refused by the
// records, since a live terminal is not one the table relaunches, and no
// attempt is added. Once the worker has ended and the task reads Unknown
// from its dead terminal, the same request begins attempt 2 superseding the
// first session, the history ends with the relaunched note followed by the
// dispatched entry, the launch records a session on the new attempt, and a
// second relaunch is refused with the attempts still two.
func TestTheCoordinatorRelaunchesADeadTerminalThroughTheAnchoredRelaunch(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	const unestablished = "a coordinator relaunching a dead terminal in its standing copy"
	service.RequireSweepClearsHere(t, unestablished)
	launchedWorker(t, "task/relaunch", func(w launched) {
		running, task := w.running, w.task
		request := machine.TaskRequest{Task: task}
		if _, code := callAs(t, w, ipc.MethodTaskRelaunch, request); code != ipc.CodeWorker {
			t.Fatalf("task.relaunch from the worker answered %q, want %q", code, ipc.CodeWorker)
		}
		var relaunched machine.TaskRelaunched
		if err := running.client.Call(t.Context(), ipc.MethodTaskRelaunch, request, &relaunched); !errors.Is(err, ipc.ErrRefused) {
			t.Fatalf("relaunching a live worker = %v, want ErrRefused", err)
		}
		if attempts := attemptsOf(t, w.home, task); len(attempts) != 1 {
			t.Fatalf("a refused relaunch left attempts %+v, want the first alone", attempts)
		}

		superseded := w.session.Reference().String()
		if err := w.provider.Send(t.Context(), w.session, "finish"); err != nil {
			t.Fatalf("Send: %v", err)
		}
		waitCapture(t, w.provider, w.session, "worker finished")
		deadline := time.Now().Add(launchWait)
		for {
			got := listedTask(t, running.client, task)
			if got.State == taskstate.Unknown && got.Source == taskstate.SourceProcess && got.Basis.Process == taskstate.Dead {
				break
			}
			if !time.Now().Before(deadline) {
				t.Fatalf("the ended worker's task never read Unknown from a dead terminal; it reads %+v", got)
			}
			time.Sleep(20 * time.Millisecond)
		}

		if err := running.client.Call(t.Context(), ipc.MethodTaskRelaunch, request, &relaunched); err != nil {
			service.FatalUnlessSweepsStoppedClearing(t, unestablished, "task.relaunch of a dead terminal: %v", err)
		}
		if relaunched.Task != task || relaunched.Attempt != 2 || relaunched.Superseded != superseded {
			t.Fatalf("task.relaunch answered %+v, want attempt 2 of task %s superseding %s", relaunched, task, superseded)
		}
		if kinds := kindsOf(t, w.home, task); len(kinds) < 2 ||
			kinds[len(kinds)-2] != "relaunched" || kinds[len(kinds)-1] != store.DispatchedEventKind {
			t.Fatalf("the history is %v, want it to end relaunched then dispatched", kinds)
		}
		var attempts []store.TaskAttempt
		for {
			attempts = attemptsOf(t, w.home, task)
			if len(attempts) != 2 {
				t.Fatalf("after the relaunch the task has attempts %+v, want two", attempts)
			}
			if _, ok := attempts[1].SessionRef.Get(); ok {
				break
			}
			if !time.Now().Before(deadline.Add(launchWait)) {
				t.Fatalf("the relaunched attempt never recorded a session: %+v", attempts)
			}
			time.Sleep(20 * time.Millisecond)
		}
		if ref := attempts[1].SessionRef.Or(""); ref == superseded {
			t.Fatalf("the relaunched attempt records the superseded session %s", ref)
		}
		if _, held, ok := running.service.SessionOf(task); !ok || held.Reference().String() != attempts[1].SessionRef.Or("") {
			t.Fatalf("the service holds %v (%v) for the task, want the relaunched attempt's session", held, ok)
		}

		var second machine.TaskRelaunched
		if err := running.client.Call(t.Context(), ipc.MethodTaskRelaunch, request, &second); !errors.Is(err, ipc.ErrRefused) {
			t.Fatalf("a second relaunch = %v, want ErrRefused", err)
		}
		if attempts := attemptsOf(t, w.home, task); len(attempts) != 2 {
			t.Fatalf("a refused second relaunch left attempts %+v, want two", attempts)
		}
	})
}
