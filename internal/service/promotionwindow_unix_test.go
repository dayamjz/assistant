//go:build unix

package service_test

import (
	"os"
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

// The gap this build leaves, pinned so its closing is seen: a reported
// investigation's copy holding nothing unlanded is removed once its worker
// has ended, as on main for a worker that exits, is stopped or is ended by a
// restart, and the window is one more way the worker ends. Past a
// one-second window over a copy exactly as its dispatch cut it from the
// default branch, the worker is ended, the cleanup removes the copy, and a
// promotion is then refused for the removed copy. #438 keeps the copy.
func TestAnUnchangedCopyIsRemovedOnceTheWindowEndsItsWorker(t *testing.T) {
	t.Parallel()
	inv := newInvestigation(t, `{"supervision_beacon_interval": "1s", "promotion_window": "1s"}`)
	git(t, inv.repository.WorkingPath, "checkout", "--quiet", "main")
	inv.serve(t, func(running serviceUnderTest) {
		inv.reported(t, running)
		inv.workerEnded(t, running)
		copyPath, err := inv.home.TaskCopy(inv.task)
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(launchWait)
		for _, err := os.Stat(copyPath); err == nil; _, err = os.Stat(copyPath) {
			if !time.Now().Before(deadline) {
				t.Fatalf("the unchanged copy still stands long after the window ended its worker; the log reads:\n%s", serviceLog(t, inv.home))
			}
			time.Sleep(20 * time.Millisecond)
		}
		// The pass that removed the copy holds the task's slot until it has
		// recorded the removal, and a promotion asked meanwhile is refused as
		// in flight, to be asked again (CI run 40645974701).
		var promoted machine.TaskPromoted
		for deadline := time.Now().Add(launchWait); ; time.Sleep(50 * time.Millisecond) {
			err = running.client.Call(t.Context(), ipc.MethodTaskPromote, machine.TaskPromoteRequest{
				Task: inv.task, Repository: inv.repository.ID, Branch: "task/window-removed", Intent: "Cache each parse.",
			}, &promoted)
			if err == nil || !strings.Contains(err.Error(), "in flight") || !time.Now().Before(deadline) {
				break
			}
		}
		if err == nil || !strings.Contains(err.Error(), "cleanup record") {
			t.Fatalf("promoting after the copy was removed answered %+v, %v; want it refused for the removed copy", promoted, err)
		}
	})
}
