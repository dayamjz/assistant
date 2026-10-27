//go:build unix

package service_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/tasks"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// These tests drive PRD section 6's promotion_window through the real
// service and the shipped Claude Code adapter's worker, the stand-in in
// stream mode: the report is filed by the worker running the call itself,
// the window is read by the supervisor's pass, and what a relaunched worker
// starts from is what the stand-in recorded reading.

// windowReport is the report the investigation's worker files. It raises no
// decision, so the task resolves Reported from its report tier, and its
// summary is the text a relaunched worker must have been begun with.
const (
	windowReport  = `{"summary":"The parser re-reads every file it is given","findings":[{"severity":"info","action":"note","description":"Cache each parse."}]}`
	reportSummary = "The parser re-reads every file it is given"
	fromReport    = "started from the report"
)

// investigation is a reported investigation's world: the home, the
// repository, the task, and the stand-in every worker of it runs as.
type investigation struct {
	home       *home.Home
	repository store.Repository
	task       string
	agent      *standin.Agent
}

// newInvestigation records a dispatched investigation under the global
// configuration global, with a stand-in worker that answers its
// instructions, a brief, and a first turn carrying the stored report.
func newInvestigation(t *testing.T, global string) investigation {
	t.Helper()
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	quietMaintenance(t, subject)
	repository := recordRepository(t, h, subject)
	quietMaintenance(t, gateRepositoryOf(t, h, subject))
	if err := os.WriteFile(h.ConfigFile(), []byte(global+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := recordDispatchedTask(t, h, repository, store.TaskInvestigation, "")
	agent := standin.New(t, standin.Script{
		Steps: []standin.Step{{Match: standin.MatchReview(), Times: standin.Always, Reply: standin.Reviewed("the change reads cleanly")}},
		Worker: &standin.WorkerScript{Exchanges: []standin.Exchange{
			{Contains: "## The investigation's report", Reply: fromReport},
			{Contains: "now a delivery task", Reply: briefHeard},
			{Contains: shellMarker, Run: true},
			{Contains: "git rev-parse --show-toplevel", Reply: "the instructions were read"},
			{Contains: "finish", Reply: "worker finished", Ends: true},
		}},
	}, agents.WithTerminationGrace(2*time.Second))
	return investigation{home: h, repository: repository, task: id, agent: agent}
}

// serve runs one service over the investigation's home, on
// withServiceOptions' terms.
func (inv investigation) serve(t *testing.T, body func(serviceUnderTest)) {
	t.Helper()
	o := options(t, inv.home)
	o.Catalog = agents.NewCatalog(fixedFactory{runner: inv.agent.Runner()}, cursorFactory{runner: inv.agent.Runner()})
	withServiceOptions(t, o, body)
}

// reported waits for the service's first worker of the task and has it file
// windowReport, and returns the worker.
func (inv investigation) reported(t *testing.T, running serviceUnderTest) launched {
	t.Helper()
	return inv.reportedAfter(t, running, nil)
}

// reportedAfter is reported with before run once the worker has read its
// instructions and before it files the report, which is where the window
// starts.
func (inv investigation) reportedAfter(t *testing.T, running serviceUnderTest, before func()) launched {
	t.Helper()
	provider, session := waitSession(t, running.service, inv.task)
	waitCapture(t, provider, session, "the instructions were read")
	if before != nil {
		before()
	}
	w := launched{running: running, home: inv.home, repository: inv.repository, task: inv.task,
		group: session.Reference().PID, provider: provider, session: session}
	if _, code := callAs(t, w, ipc.MethodTaskEvent, machine.TaskEventRequest{Kind: store.ReportReadyEventKind, Detail: windowReport}); code != "" {
		t.Fatalf("the worker's report was refused with %q", code)
	}
	// Under a one-second window the worker may already be ended by the
	// first read, so either terminal reading is the report tier's answer.
	waitState(t, running.client, inv.task, "read Reported from its report", func(r taskstate.Resolution) bool {
		return reportedFromTheReport(taskstate.Live)(r) || reportedFromTheReport(taskstate.Dead)(r)
	})
	return w
}

// promote promotes the task from this process, outside every worker group.
func (inv investigation) promote(t *testing.T, running serviceUnderTest, branch string) machine.TaskPromoted {
	t.Helper()
	return inv.promoteWith(t, running, branch, "Cache each parse.")
}

// promoteWith is promote with the intent stated.
func (inv investigation) promoteWith(t *testing.T, running serviceUnderTest, branch, intent string) machine.TaskPromoted {
	t.Helper()
	var promoted machine.TaskPromoted
	if err := running.client.Call(t.Context(), ipc.MethodTaskPromote, machine.TaskPromoteRequest{
		Task: inv.task, Repository: inv.repository.ID, Branch: branch, Intent: intent,
	}, &promoted); err != nil {
		t.Fatalf("task.promote: %v", err)
	}
	return promoted
}

// copyStands fails the test unless the task's copy is still a directory.
func (inv investigation) copyStands(t *testing.T, when string) {
	t.Helper()
	copyPath, err := inv.home.TaskCopy(inv.task)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(copyPath); err != nil || !info.IsDir() {
		t.Fatalf("the investigation's copy is gone %s: %v", when, err)
	}
}

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

// commitInCopy makes a commit in the task's copy that no remote has, the
// work a worker leaves there, and returns it.
func (inv investigation) commitInCopy(t *testing.T) string {
	t.Helper()
	copyPath := resolvedCopy(t, inv.home, inv.task)
	if err := os.WriteFile(filepath.Join(copyPath, "finding.txt"), []byte("what the worker found\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, copyPath, "add", "finding.txt")
	git(t, copyPath, "commit", "--quiet", "-m", "the worker's own commit")
	return git(t, copyPath, "rev-parse", "HEAD")
}

// keptWork is the open kept work entries and the task's kept-work wakes, read
// from the home's records through a reader of its own.
func (inv investigation) keptWork(t *testing.T) ([]store.KeptWork, []store.Wake) {
	t.Helper()
	records, err := store.OpenReadOnly(t.Context(), inv.home.Database())
	if err != nil {
		t.Fatalf("opening the records: %v", err)
	}
	defer func() { _ = records.Close() }()
	open, err := records.OpenKeptWork(t.Context())
	if err != nil {
		t.Fatalf("listing the kept work: %v", err)
	}
	all, err := records.TaskWakes(t.Context(), inv.task)
	if err != nil {
		t.Fatalf("listing the task's wakes: %v", err)
	}
	var wakes []store.Wake
	for _, w := range all {
		if w.Kind == string(supervisor.KindKeptWork) {
			wakes = append(wakes, w)
		}
	}
	return open, wakes
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
			t.Fatalf("past the window the task has kept work %+v and kept-work wakes %+v, want one of each; the log reads:\n%s",
				open, wakes, serviceLog(t, inv.home))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// passes lets the supervisor's pass run several times at its 1s interval.
func passes() { time.Sleep(3500 * time.Millisecond) }

// relaunchedFromTheReport checks a promotion that relaunched: attempt 2 was
// begun superseding the ended session, the service holds a new session for
// it, and that worker, started in the task's own copy, read the stored
// report in its first turn and answered it.
func (inv investigation) relaunchedFromTheReport(t *testing.T, running serviceUnderTest, promoted machine.TaskPromoted, ended string) {
	t.Helper()
	if promoted.Record.Shape != store.TaskDelivery || promoted.Relaunch == nil || promoted.Relaunch.Attempt != 2 ||
		promoted.Relaunch.Superseded != ended || !strings.Contains(promoted.Brief.Unsent, "stored report") {
		t.Fatalf("the promotion of an investigation whose worker ended answered %+v, want a relaunch of attempt 2 from the report", promoted)
	}
	if row := machine.TaskPromotedPhrase(promoted); row == machine.TaskPromotedPhrase(machine.TaskPromoted{}) {
		t.Fatalf("a promotion that relaunched reads as one whose brief reached nobody: %q", row)
	}
	deadline := time.Now().Add(launchWait)
	for {
		if provider, session, ok := running.service.SessionOf(inv.task); ok && session.Reference().String() != ended {
			waitCapture(t, provider, session, fromReport)
			break
		}
		if !time.Now().Before(deadline) {
			service.FatalUnlessSweepsStoppedClearing(t, "the promotion's relaunch", "the service never held a new session for task %s", inv.task)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if n := linesCarrying(t, inv.agent, reportSummary); n != 1 {
		t.Fatalf("the stored report reached a worker on %d lines, want the relaunched worker's first turn alone", n)
	}
	want := resolvedCopy(t, inv.home, inv.task)
	for _, call := range inv.agent.Calls() {
		if call.Worker && len(call.Lines) > 0 && strings.Contains(call.Lines[0], reportSummary) {
			if got, _ := filepath.EvalSymlinks(call.Dir); got != want {
				t.Fatalf("the relaunched worker ran in %s, want the investigation's own copy %s", got, want)
			}
			return
		}
	}
	t.Fatal("no worker's first turn carried the stored report")
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

// PRD section 6's kept work, past the window, and P15: under a one-second
// promotion_window the supervisor's pass ends the reported worker through
// the session the service holds, so the service no longer holds it and its
// reference probes Dead; the task stays Reported from its report and its
// copy stands through several more passes, which are where a reported
// investigation's copy used to be cleaned up. A promotion then relaunches a
// worker in that same copy, through the anchored relaunch, begun with the
// stored report as its starting point.
func TestAReportedWorkerPastThePromotionWindowIsEndedAndAPromotionRelaunchesItFromTheReport(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	service.RequireSweepClearsHere(t, "a promotion relaunching the reported investigation in its copy")
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
		waitState(t, running.client, inv.task, "read Reported with its worker ended", reportedFromTheReport(taskstate.Dead))
		passes()
		inv.copyStands(t, "after its worker was ended for the window")
		if got := attemptsOf(t, inv.home, inv.task); len(got) != 1 {
			t.Fatalf("ending the worker for the window began %d attempts, want none past the first", len(got)-1)
		}
		// The copy is exactly as its dispatch cut it from the default
		// branch, so it holds nothing that branch lacks and is no kept work
		// entry; the test below is the same window over a copy that holds a
		// commit.
		if open, wakes := inv.keptWork(t); len(open) != 0 || len(wakes) != 0 {
			t.Fatalf("an unchanged copy kept past the window is kept work %+v with wakes %+v, want neither", open, wakes)
		}
		if log := serviceLog(t, inv.home); !strings.Contains(log, "lacks, so no kept work entry is recorded") {
			t.Fatalf("the service log does not say the unchanged copy was read and recorded as no kept work:\n%s", log)
		}

		inv.relaunchedFromTheReport(t, running, inv.promote(t, running, "task/window-passed"), ended)
	})
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

// The control for the test above: the same copy holding the same commit,
// promoted inside a five-second promotion_window, continues its worker and
// is never kept for the window, so no pass past the window's end records an
// entry or ends the worker.
func TestAPromotionInsideThePromotionWindowKeepsNoWork(t *testing.T) {
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

// A restart between the report and the promotion: the stopping service ends
// the worker it held, as every stop does, and the next service reads the
// reported investigation from its records alone. It neither relaunches the
// investigation nor removes its copy through several passes, though its
// terminal reads Dead, and a promotion there relaunches a worker in the same
// copy with the stored report, which it reads from the report row and not
// from anything the first service held.
func TestAPromotionAfterARestartRelaunchesTheReportedInvestigationFromTheRecord(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P8)
	service.RequireSweepClearsHere(t, "a promotion relaunching the reported investigation after a restart")
	inv := newInvestigation(t, `{"supervision_beacon_interval": "1s", "promotion_window": "1h"}`)
	var ended string
	inv.serve(t, func(running serviceUnderTest) {
		ended = inv.reported(t, running).session.Reference().String()
	})
	homeIsFree(t, inv.home)

	inv.serve(t, func(running serviceUnderTest) {
		waitState(t, running.client, inv.task, "read Reported after the restart", reportedFromTheReport(taskstate.Dead))
		passes()
		if _, _, held := running.service.SessionOf(inv.task); held {
			t.Fatal("the restarted service launched a worker for the reported investigation")
		}
		inv.copyStands(t, "after a restart")
		inv.relaunchedFromTheReport(t, running, inv.promote(t, running, "task/window-restarted"), ended)
	})
}

// P15 for a worker an earlier service left: the first service files the
// report under a one-hour window and is then left the way SIGKILL leaves it,
// its worker running and no service holding a handle to it. The next service,
// whose operator has shortened the window to a second, holds no session, so
// it ends the survivor through agents.EndGroup on the identity the attempt's
// recorded reference carries: the process is gone, the task reads Reported
// over a dead terminal, the log names the verified identity, and the copy
// stands.
func TestAReportedWorkerAnEarlierServiceLeftIsEndedPastThePromotionWindow(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	inv := newInvestigation(t, `{"supervision_beacon_interval": "1s", "promotion_window": "1h"}`)
	var survivor session.Reference
	inv.serve(t, func(running serviceUnderTest) {
		survivor = inv.reported(t, running).session.Reference()
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

// An oversized brief does not hide the reading a promotion relaunches on.
// With the worker live, an intent past the message limit is refused as
// TooLarge, nothing is sent and nothing relaunched; with the worker ended
// for the window, the same intent still relaunches a worker in the copy from
// the report, begun with the brief through its instructions.
func TestAnOversizedBriefStillLetsAPromotionRelaunch(t *testing.T) {
	t.Parallel()
	service.RequireSweepClearsHere(t, "a promotion with an oversized brief relaunching the reported investigation")
	intent := "Cache each parse. " + strings.Repeat("x", machine.MaxMessageBytes)

	live := newInvestigation(t, `{"supervision_beacon_interval": "1s", "promotion_window": "1h"}`)
	live.serve(t, func(running serviceUnderTest) {
		live.reported(t, running)
		promoted := live.promoteWith(t, running, "task/brief-too-large", intent)
		if !promoted.Brief.TooLarge || promoted.Brief.Message != nil || promoted.Relaunch != nil {
			t.Fatalf("an oversized brief to a live worker answered %+v, want it refused as too large and no relaunch", promoted.Brief)
		}
	})

	ended := newInvestigation(t, `{"supervision_beacon_interval": "1s", "promotion_window": "1s"}`)
	ended.serve(t, func(running serviceUnderTest) {
		ref := ended.reported(t, running).session.Reference().String()
		ended.workerEnded(t, running)
		waitState(t, running.client, ended.task, "read Reported with its worker ended", reportedFromTheReport(taskstate.Dead))
		passes() // the window's end holds the task's slot until its goroutine returns
		promoted := ended.promoteWith(t, running, "task/brief-too-large", intent)
		if promoted.Brief.TooLarge {
			t.Fatalf("an oversized brief to an ended worker answered %+v, want the relaunch decided instead", promoted.Brief)
		}
		ended.relaunchedFromTheReport(t, running, promoted, ref)
	})
}
