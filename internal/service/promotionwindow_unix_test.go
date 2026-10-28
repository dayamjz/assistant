//go:build unix

package service_test

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// These tests drive PRD section 6's promotion_window through the real
// service and the shipped Claude Code adapter's worker, on
// promotedrelaunch_unix_test.go's investigation: the report is filed by the
// worker running the call itself, and the window is read by the
// supervisor's pass.

// workerEnded waits until the service holds no session for the task, which
// is how the window's end through the held session shows.
func (inv investigation) workerEnded(t *testing.T, running serviceUnderTest) {
	t.Helper()
	deadline := time.Now().Add(launchWait)
	for {
		if _, _, held := running.service.SessionOf(inv.task); !held {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatal("the reported worker was still held long past its promotion window")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// PRD section 6's kept work, inside the window: a reported investigation's
// worker under the default-length promotion_window is still held and live
// after several supervisor passes, and a promotion then continues that same
// worker, sending it the brief and relaunching nothing. This is the control
// for the test below: a window that ended the worker at once, or that was
// not read at all, fails here.
func TestAReportedWorkerInsideThePromotionWindowIsKeptAndContinued(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	inv := newInvestigation(t, `{"supervision_beacon_interval": "1s", "promotion_window": "1h"}`)
	inv.serve(t, func(running serviceUnderTest) {
		w := inv.reported(t, running)
		passes()
		if _, held, ok := running.service.SessionOf(inv.task); !ok || held.Reference() != w.session.Reference() {
			t.Fatal("the service no longer holds the reported worker inside its promotion window")
		}
		waitProbe(t, w.provider, w.session.Reference().String(), taskstate.Live)

		promoted := inv.promote(t, running, "task/window-kept")
		if !promoted.Brief.Delivered() || promoted.Relaunch != nil {
			t.Fatalf("the promotion inside the window answered %+v, want the brief sent to the live worker and no relaunch", promoted)
		}
		waitCapture(t, w.provider, w.session, briefHeard)
		if got := attemptsOf(t, inv.home, inv.task); len(got) != 1 {
			t.Fatalf("the task has %d attempts after the promotion, want the one that wrote the report", len(got))
		}
	})
}

// PRD section 6's promotion_window, past the window: under a one-second
// window the supervisor's pass ends the reported worker through the session
// the service holds, so the service no longer holds it and its reference
// probes Dead, and the task stays Reported from its report. The copy holds
// the worker's commit, so the cleanup the pass then asks refuses it as
// unlanded and keeps it as one kept work entry (P15), and it stands through
// several more passes. A promotion then relaunches a worker in that same
// copy, through the anchored relaunch, begun with the stored report, and
// its flip answers the entry carry on. With the window ignored the worker
// is still held long past it.
func TestAReportedWorkerPastThePromotionWindowIsEndedAndAPromotionRelaunchesItFromTheReport(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	service.RequireSweepClearsHere(t, "a promotion relaunching the reported investigation in its copy")
	inv := newInvestigation(t, `{"supervision_beacon_interval": "1s", "promotion_window": "1s"}`)
	inv.serve(t, func(running serviceUnderTest) {
		w := inv.reportedAfter(t, running, func() { inv.commitInCopy(t) })
		ended := w.session.Reference().String()
		inv.workerEnded(t, running)
		waitProbe(t, w.provider, ended, taskstate.Dead)
		waitState(t, running.client, inv.task, "read Reported with its worker ended", reportedFromTheReport(taskstate.Dead))
		entry := inv.waitKept(t)
		passes()
		inv.copyStands(t, "after its worker was ended for the window")
		if got := attemptsOf(t, inv.home, inv.task); len(got) != 1 {
			t.Fatalf("ending the worker for the window began %d attempts, want none past the first", len(got)-1)
		}

		inv.relaunchedFromTheReport(t, running, inv.promote(t, running, "task/window-passed"), ended)
		answered, err := recordsOf(t, inv.home).KeptWorkEntry(t.Context(), entry.ID)
		if err != nil || answered.Open() || answered.Answer.Or("") != store.KeptCarryOn {
			t.Fatalf("after the promotion the copy's entry reads %+v (%v), want it answered carry on", answered, err)
		}
	})
}

// The control for the test above on the promotion's side: a task promoted
// inside a five-second promotion_window is a delivery task by the time the
// window would have passed, so the pass's read of the window again under
// the task's slot finds nothing to end, and its worker is still held and
// continued past the window's end, with no kept work entry for its copy.
func TestAPromotionInsideThePromotionWindowKeepsItsWorker(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	inv := newInvestigation(t, `{"supervision_beacon_interval": "1s", "promotion_window": "5s"}`)
	inv.serve(t, func(running serviceUnderTest) {
		w := inv.reportedAfter(t, running, func() { inv.commitInCopy(t) })
		promoted := inv.promote(t, running, "task/window-promoted")
		if !promoted.Brief.Delivered() || promoted.Relaunch != nil {
			t.Fatalf("the promotion inside the window answered %+v, want the brief sent to the live worker", promoted)
		}
		passes()
		time.Sleep(3 * time.Second) // past the window's end, with a pass after it
		if _, held, ok := running.service.SessionOf(inv.task); !ok || held.Reference() != w.session.Reference() {
			t.Fatal("the window ended the worker of a task promoted inside it")
		}
		if open, wakes := inv.keptWork(t); len(open) != 0 || len(wakes) != 0 {
			t.Fatalf("a task promoted inside the window is kept work %+v with wakes %+v, want neither", open, wakes)
		}
	})
}

// P15 for a worker an earlier service left: the first service files the
// report under a one-hour window and is then left the way SIGKILL leaves it,
// its worker running and no service holding a handle to it. The next service,
// whose operator has shortened the window to a second, holds no session, so
// it ends the survivor through agents.EndGroup on the identity the attempt's
// recorded reference carries: the process is gone, the task reads Reported
// over a dead terminal, the log names the verified identity, and the copy,
// which holds the worker's commit, stands as kept work.
func TestAReportedWorkerAnEarlierServiceLeftIsEndedPastThePromotionWindow(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	inv := newInvestigation(t, `{"supervision_beacon_interval": "1s", "promotion_window": "1h"}`)
	var survivor session.Reference
	inv.serve(t, func(running serviceUnderTest) {
		survivor = inv.reportedAfter(t, running, func() { inv.commitInCopy(t) }).session.Reference()
		running.service.AbandonSessionsForTest(t)
	})
	homeIsFree(t, inv.home)
	identity := agents.Identity{PID: survivor.PID, Start: survivor.Start}
	t.Cleanup(func() { _, _ = agents.EndGroup(identity, time.Second) })
	if presence, err := identity.Presence(); err != nil || presence != agents.PresencePresent {
		t.Fatalf("the worker reads %q (%v) after its service closed, want it still running, or there is no survivor", presence, err)
	}
	if err := os.WriteFile(inv.home.ConfigFile(), []byte(`{"supervision_beacon_interval": "1s", "promotion_window": "1s"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	inv.serve(t, func(running serviceUnderTest) {
		deadline := time.Now().Add(launchWait)
		for {
			presence, err := identity.Presence()
			if err == nil && presence != agents.PresencePresent {
				break
			}
			if !time.Now().Before(deadline) {
				t.Fatalf("the survivor still reads %q (%v) long past its promotion window", presence, err)
			}
			time.Sleep(20 * time.Millisecond)
		}
		waitState(t, running.client, inv.task, "read Reported over its ended survivor", reportedFromTheReport(taskstate.Dead))
		if log := serviceLog(t, inv.home); !strings.Contains(log, "by its verified identity") {
			t.Fatalf("the service log does not say the survivor was ended by its verified identity:\n%s", log)
		}
		inv.copyStands(t, "after the survivor was ended for the window")
	})
}

// PRD section 6's "its copy is kept either way": past a one-second
// promotion_window over a copy exactly as its dispatch cut it from the
// default branch, the pass ends the worker and the copy still stands
// through several more passes, which are where a reported investigation's
// copy used to be cleaned up. It holds nothing the default branch lacks, so
// it is no kept work entry, and a promotion relaunches a worker in it from
// the stored report. The tests below are the same window over a copy that
// holds a commit.
func TestAnUnchangedCopyPastThePromotionWindowIsKeptForAPromotion(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	service.RequireSweepClearsHere(t, "a promotion relaunching the reported investigation in its unchanged copy")
	inv := newInvestigation(t, `{"supervision_beacon_interval": "1s", "promotion_window": "1s"}`)
	// The copy is a clone of the primary checkout, so with the checkout on
	// its default branch the copy dispatch cuts holds nothing that branch
	// lacks.
	git(t, inv.repository.WorkingPath, "checkout", "--quiet", "main")
	inv.serve(t, func(running serviceUnderTest) {
		w := inv.reported(t, running)
		ended := w.session.Reference().String()
		inv.workerEnded(t, running)
		waitProbe(t, w.provider, ended, taskstate.Dead)
		waitLog(t, inv.home, "lacks, so no kept work entry is recorded")
		passes()
		inv.copyStands(t, "after its worker was ended for the window")
		if open, wakes := inv.keptWork(t); len(open) != 0 || len(wakes) != 0 {
			t.Fatalf("an unchanged copy kept past the window is kept work %+v with wakes %+v, want neither", open, wakes)
		}
		inv.relaunchedFromTheReport(t, running, inv.promote(t, running, "task/window-unchanged"), ended)
	})
}

// The same copy kept across a restart: the stopping service ends the
// worker it held, and the next service, inside a one-hour window, reads the
// worker gone and keeps the unchanged copy rather than cleaning it up, with
// no entry since it holds nothing the default branch lacks. A promotion
// there relaunches in the same copy from the report row.
func TestARestartKeepsAnUnchangedReportedCopyForAPromotion(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	service.RequireSweepClearsHere(t, "a promotion relaunching the reported investigation after a restart")
	inv := newInvestigation(t, `{"supervision_beacon_interval": "1s", "promotion_window": "1h"}`)
	git(t, inv.repository.WorkingPath, "checkout", "--quiet", "main")
	var ended string
	inv.serve(t, func(running serviceUnderTest) {
		ended = inv.reported(t, running).session.Reference().String()
	})
	homeIsFree(t, inv.home)

	inv.serve(t, func(running serviceUnderTest) {
		waitState(t, running.client, inv.task, "read Reported after the restart", reportedFromTheReport(taskstate.Dead))
		waitLog(t, inv.home, "lacks, so no kept work entry is recorded")
		passes()
		inv.copyStands(t, "after a restart")
		if open, _ := inv.keptWork(t); len(open) != 0 {
			t.Fatalf("an unchanged copy is kept work %+v after the restart, want none", open)
		}
		inv.relaunchedFromTheReport(t, running, inv.promote(t, running, "task/window-restarted"), ended)
	})
}

// P15's "kept work is never silent", inside the window: the worker commits
// in its copy and files its report under a one-hour promotion_window, and a
// restart ends it. The next service reads the worker gone long before the
// window passes and keeps the copy at once as one kept work entry, for the
// worker that ended inside the window, which holds a delivery task created
// in the same project. Waiting for the window instead, the copy sat with no
// entry for up to the window and the new task dispatched.
func TestAWorkerGoneInsideThePromotionWindowLeavesItsCopyAsKeptWorkAtOnce(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	inv := newInvestigation(t, `{"supervision_beacon_interval": "1s", "promotion_window": "1h"}`)
	var head string
	inv.serve(t, func(running serviceUnderTest) {
		inv.reportedAfter(t, running, func() { head = inv.commitInCopy(t) })
	})
	homeIsFree(t, inv.home)

	inv.serve(t, func(running serviceUnderTest) {
		entry, _ := inv.waitKeptWake(t)
		if entry.TaskID.Or("") != inv.task || entry.Reason != tasks.KeptPromotionWorkerEnded || !slices.Contains(entry.Holds, head) {
			t.Fatalf("the entry = %+v, want task %s's copy, kept for a worker ended inside the window, holding %s", entry, inv.task, head)
		}
		var created machine.TaskCreated
		if err := running.client.Call(t.Context(), ipc.MethodTaskCreate, machine.TaskCreateRequest{
			Shape: "delivery", Project: "one", Instructions: "Do the work.\n", Mode: "interactive",
			Repository: inv.repository.ID, Branch: "task/inside-the-window",
		}, &created); err != nil {
			t.Fatalf("creating a task in the investigation's project: %v", err)
		}
		waitLog(t, inv.home, "task "+created.Record.ID+" waits: ")
		passes()
		if got := attemptsOf(t, inv.home, created.Record.ID); len(got) != 0 {
			t.Fatalf("with entry %d open, task %s was dispatched with attempts %+v", entry.ID, created.Record.ID, got)
		}
		if open, wakes := inv.keptWork(t); len(open) != 1 || open[0].ID != entry.ID || len(wakes) != 1 {
			t.Fatalf("later passes left kept work %+v and kept-work wakes %+v, want entry %d raised once", open, wakes, entry.ID)
		}
	})
}

// waitKeptWake waits for the task's one open kept work entry and its one
// kept-work wake, and fails the test on a second of either.
func (inv investigation) waitKeptWake(t *testing.T) (store.KeptWork, store.Wake) {
	t.Helper()
	deadline := time.Now().Add(launchWait)
	for {
		open, wakes := inv.keptWork(t)
		if len(open) > 1 || len(wakes) > 1 {
			t.Fatalf("the window kept %d entries and raised %d kept-work wakes, want one of each: %+v %+v", len(open), len(wakes), open, wakes)
		}
		if len(open) == 1 && len(wakes) == 1 {
			return open[0], wakes[0]
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the task has kept work %+v and kept-work wakes %+v, want one of each; the log reads:\n%s",
				open, wakes, serviceLog(t, inv.home))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// PRD principle P15 and section 6's kept work, past the window over a copy
// that holds work: the worker commits in its copy before filing its report,
// and under a one-second promotion_window the supervisor's pass ends the
// worker and records the kept copy as exactly one kept work entry, about the
// task, for the window, holding that commit. The loop raises it as exactly
// one kept-work wake through several more passes, the catch-up read lists
// it, and the copy stands. A restart after the window writes no second entry
// and raises no second wake, and the restarted service did ask: its log says
// the copy is already that entry. Without endWindow's keepWork the poll for
// the entry fails.
func TestAReportedCopyHoldingWorkPastThePromotionWindowIsOneKeptWorkEntry(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	inv := newInvestigation(t, `{"supervision_beacon_interval": "1s", "promotion_window": "1s"}`)
	var entry store.KeptWork
	inv.serve(t, func(running serviceUnderTest) {
		var head string
		inv.reportedAfter(t, running, func() { head = inv.commitInCopy(t) })
		inv.workerEnded(t, running)
		var wake store.Wake
		entry, wake = inv.waitKeptWake(t)
		if entry.TaskID.Or("") != inv.task || entry.RunID.IsKnown() || entry.Reason != tasks.KeptPromotionWindow ||
			!slices.Contains(entry.Holds, head) || entry.Answered() {
			t.Fatalf("the entry = %+v, want task %s's unanswered copy, kept for the window, holding %s", entry, inv.task, head)
		}
		if wake.Marker != "kept-work:"+strconv.FormatInt(entry.ID, 10) {
			t.Fatalf("the kept-work wake %+v does not name entry %d", wake, entry.ID)
		}
		passes()
		if open, wakes := inv.keptWork(t); len(open) != 1 || open[0].ID != entry.ID || len(wakes) != 1 {
			t.Fatalf("later passes left kept work %+v and kept-work wakes %+v, want entry %d raised once", open, wakes, entry.ID)
		}
		var read machine.CatchUp
		if err := running.client.Call(t.Context(), ipc.MethodCatchUpRead, nil, &read); err != nil {
			t.Fatalf("catchup.read: %v", err)
		}
		if len(read.Kept) != 1 || read.Kept[0].ID != entry.ID {
			t.Fatalf("the catch-up read lists kept work %+v, want entry %d", read.Kept, entry.ID)
		}
		inv.copyStands(t, "after it was kept for the window")
	})
	homeIsFree(t, inv.home)

	inv.serve(t, func(running serviceUnderTest) {
		waitState(t, running.client, inv.task, "read Reported after the restart", reportedFromTheReport(taskstate.Dead))
		deadline := time.Now().Add(launchWait)
		for !strings.Contains(serviceLog(t, inv.home), fmt.Sprintf("is already kept work entry %d", entry.ID)) {
			if !time.Now().Before(deadline) {
				t.Fatalf("the restarted service never asked the window's keep again; the log reads:\n%s", serviceLog(t, inv.home))
			}
			time.Sleep(20 * time.Millisecond)
		}
		passes()
		if open, wakes := inv.keptWork(t); len(open) != 1 || open[0].ID != entry.ID || len(wakes) != 1 {
			t.Fatalf("after the restart the task has kept work %+v and kept-work wakes %+v, want entry %d raised once", open, wakes, entry.ID)
		}
	})
}

// PRD section 6's "kept work is answered before new task work starts" and
// P15, for the entry the window writes: past a one-second promotion_window
// over a copy holding a commit, a delivery task created in the same project
// is not dispatched. It has no attempt through several passes, the service
// log says it waits, and task.get names the window's entry. Promoting the
// investigation answers the entry carry on, naming the investigation itself
// as the task that carries it on, relaunches a worker in the copy from the
// stored report, and the held task then dispatches. At ed85b34, before main's
// dispatch hold was merged, the held task dispatched at once with the entry
// open; without endWindow's keep there is no entry to wait for.
func TestTheEntryAPromotionWindowWritesHoldsANewTaskUntilThePromotionAnswersIt(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	service.RequireSweepClearsHere(t, "a promotion relaunching the held investigation in its copy")
	inv := newInvestigation(t, `{"supervision_beacon_interval": "1s", "promotion_window": "1s"}`)
	inv.serve(t, func(running serviceUnderTest) {
		ctx := t.Context()
		w := inv.reportedAfter(t, running, func() { inv.commitInCopy(t) })
		ended := w.session.Reference().String()
		inv.workerEnded(t, running)
		entry, _ := inv.waitKeptWake(t)

		var created machine.TaskCreated
		if err := running.client.Call(ctx, ipc.MethodTaskCreate, machine.TaskCreateRequest{
			Shape: "delivery", Project: "one", Instructions: "Do the work.\n", Mode: "interactive",
			Repository: inv.repository.ID, Branch: "task/after-the-window",
		}, &created); err != nil {
			t.Fatalf("creating a task in the investigation's project: %v", err)
		}
		held := created.Record.ID
		waitLog(t, inv.home, "task "+held+" waits: ")
		passes()
		if got := attemptsOf(t, inv.home, held); len(got) != 0 {
			t.Fatalf("with the window's entry %d open, task %s was dispatched with attempts %+v", entry.ID, held, got)
		}
		var task machine.Task
		if err := running.client.Call(ctx, ipc.MethodTaskGet, machine.TaskRequest{Task: held}, &task); err != nil {
			t.Fatal(err)
		}
		if len(task.KeptWork) != 1 || task.KeptWork[0].ID != entry.ID {
			t.Fatalf("the held task names kept work %+v, want the window's entry %d", task.KeptWork, entry.ID)
		}

		inv.relaunchedFromTheReport(t, running, inv.promote(t, running, "task/window-held"), ended)
		answered, err := recordsOf(t, inv.home).KeptWorkEntry(ctx, entry.ID)
		if err != nil || answered.Open() || answered.Answer.Or("") != store.KeptCarryOn || answered.CarriedOnTask.Or("") != inv.task {
			t.Fatalf("after the promotion the window's entry reads %+v (%v), want it answered carry on by task %s", answered, err, inv.task)
		}
		waitAttempts(t, inv.home, held)
	})
}

// The same, for a worker that exits by itself inside the window, with no
// stop and no restart: this service still holds its session when it exits,
// so only the probe says the worker is gone. Its copy is kept at once as one kept work
// entry for the worker that ended inside the window, and a delivery task
// created in the project waits on it. Keeping only a worker the service no
// longer held left this copy silent until the window passed.
func TestAWorkerThatExitsInsideThePromotionWindowLeavesItsCopyAsKeptWorkAtOnce(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	inv := newInvestigation(t, `{"supervision_beacon_interval": "1s", "promotion_window": "1h"}`)
	inv.serve(t, func(running serviceUnderTest) {
		var head string
		w := inv.reportedAfter(t, running, func() { head = inv.commitInCopy(t) })
		if err := w.provider.Send(t.Context(), w.session, "finish"); err != nil {
			t.Fatalf("telling the worker to finish: %v", err)
		}
		waitProbe(t, w.provider, w.session.Reference().String(), taskstate.Dead)
		entry, _ := inv.waitKeptWake(t)
		if entry.TaskID.Or("") != inv.task || entry.Reason != tasks.KeptPromotionWorkerEnded || !slices.Contains(entry.Holds, head) {
			t.Fatalf("the entry = %+v, want task %s's copy, kept for a worker ended inside the window, holding %s", entry, inv.task, head)
		}
		var created machine.TaskCreated
		if err := running.client.Call(t.Context(), ipc.MethodTaskCreate, machine.TaskCreateRequest{
			Shape: "delivery", Project: "one", Instructions: "Do the work.\n", Mode: "interactive",
			Repository: inv.repository.ID, Branch: "task/after-the-exit",
		}, &created); err != nil {
			t.Fatalf("creating a task in the investigation's project: %v", err)
		}
		waitLog(t, inv.home, "task "+created.Record.ID+" waits: ")
		if got := attemptsOf(t, inv.home, created.Record.ID); len(got) != 0 {
			t.Fatalf("with entry %d open, task %s was dispatched with attempts %+v", entry.ID, created.Record.ID, got)
		}
	})
}

// The window's end is taken at a pass under the task's slot, so it waits for
// the slot like a stop or a relaunch does, with no bound of its own: while
// a promotion, a stop, a relaunch or a dispatch of the task holds the slot,
// every pass skips the task. Here the slot is held from before the report is
// filed, the pass runs several times past a one-second window, and the
// worker is still held; once the slot is released, the next pass ends it.
func TestAWindowsEndWaitsForTheTasksSlot(t *testing.T) {
	t.Parallel()
	inv := newInvestigation(t, `{"supervision_beacon_interval": "1s", "promotion_window": "1s"}`)
	inv.serve(t, func(running serviceUnderTest) {
		var release func()
		w := inv.reportedAfter(t, running, func() { release = running.service.ClaimSlotForTest(t, inv.task) })
		passes()
		if _, held, ok := running.service.SessionOf(inv.task); !ok || held.Reference() != w.session.Reference() {
			release()
			t.Fatal("the window ended the worker while its task's slot was held")
		}
		release()
		inv.workerEnded(t, running)
	})
}

// A worker that exits by itself inside the window leaves this service
// holding its dead session, and the keep of its copy settles the window's
// memory for that reference. The dead session is released all the same,
// so neither the handle nor its output stays held once the window has
// passed: past a five-second window, after the copy is kept for the worker
// that ended inside it, the service holds no session for the task.
func TestADeadSessionIsReleasedOnceItsCopyIsKeptInsideTheWindow(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	inv := newInvestigation(t, `{"supervision_beacon_interval": "1s", "promotion_window": "5s"}`)
	inv.serve(t, func(running serviceUnderTest) {
		w := inv.reportedAfter(t, running, func() { inv.commitInCopy(t) })
		if err := w.provider.Send(t.Context(), w.session, "finish"); err != nil {
			t.Fatalf("telling the worker to finish: %v", err)
		}
		waitProbe(t, w.provider, w.session.Reference().String(), taskstate.Dead)
		entry, _ := inv.waitKeptWake(t)
		if entry.Reason != tasks.KeptPromotionWorkerEnded {
			t.Fatalf("the entry = %+v, want it kept for a worker that ended inside the window", entry)
		}
		time.Sleep(5 * time.Second) // past the window's end
		passes()
		if _, _, held := running.service.SessionOf(inv.task); held {
			t.Fatal("past the window the service still holds the dead session of a worker whose copy was kept")
		}
	})
}
