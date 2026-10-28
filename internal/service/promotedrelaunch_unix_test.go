//go:build unix

package service_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// These tests drive a promotion of a reported investigation through the
// real service and the shipped Claude Code adapter's worker, the stand-in in
// stream mode: the report is filed by the worker running the call itself,
// and what a relaunched worker starts from is what the stand-in recorded
// reading.

// filedReport is the report the investigation's worker files. It raises no
// decision, so the task resolves Reported from its report tier, and its
// summary is the text a relaunched worker must have been begun with.
const (
	filedReport   = `{"summary":"The parser re-reads every file it is given","findings":[{"severity":"info","action":"note","description":"Cache each parse."}]}`
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
// filedReport, and returns the worker.
func (inv investigation) reported(t *testing.T, running serviceUnderTest) launched {
	t.Helper()
	return inv.reportedAfter(t, running, nil)
}

// reportedAfter is reported with before run once the worker has read its
// instructions and before it files the report.
func (inv investigation) reportedAfter(t *testing.T, running serviceUnderTest, before func()) launched {
	t.Helper()
	provider, session := waitSession(t, running.service, inv.task)
	waitCapture(t, provider, session, "the instructions were read")
	if before != nil {
		before()
	}
	w := launched{running: running, home: inv.home, repository: inv.repository, task: inv.task,
		group: session.Reference().PID, provider: provider, session: session}
	fileReportAsWorker(t, w)
	// Under a one-second promotion_window the worker may already be ended
	// by the first read, so either terminal reading is the report tier's
	// answer.
	waitState(t, running.client, inv.task, "read Reported from its report", func(r taskstate.Resolution) bool {
		return reportedFromTheReport(taskstate.Live)(r) || reportedFromTheReport(taskstate.Dead)(r)
	})
	return w
}

// fileReportAsWorker has the task's worker file filedReport over its route,
// as task.event's report kind, and does not wait to read the worker's answer
// back. Under a short promotion_window the stored report starts the window,
// and the window's end can end the worker before that answer is read (CI run
// 40615559323 read the capture after the session was closed). Whether the
// report was stored is read from the task's resolution instead, by the
// waitState that follows in reportedAfter; a refused report never resolves
// Reported and fails there.
func fileReportAsWorker(t *testing.T, w launched) {
	t.Helper()
	body, err := json.Marshal(machine.TaskEventRequest{Kind: store.ReportReadyEventKind, Detail: filedReport})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "params.json")
	if err := os.WriteFile(file, body, 0o600); err != nil {
		t.Fatal(err)
	}
	command := fmt.Sprintf(": call %d; %s \"$(cat %s)\"", shellCalls.Add(1),
		shellQuote(testCommand(t), callVerb, w.running.service.Socket(), string(ipc.MethodTaskEvent)), shellQuote(file))
	if err := w.provider.Send(t.Context(), w.session, shellMarker+command); err != nil {
		t.Fatalf("sending the worker its report: %v", err)
	}
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

// waitKept waits for the task's copy to be one open kept work entry, and
// fails the test on a second.
func (inv investigation) waitKept(t *testing.T) store.KeptWork {
	t.Helper()
	deadline := time.Now().Add(launchWait)
	for {
		open, _ := inv.keptWork(t)
		if len(open) > 1 {
			t.Fatalf("the copy is %d kept work entries, want one: %+v", len(open), open)
		}
		if len(open) == 1 {
			return open[0]
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the copy never became a kept work entry; the log reads:\n%s", serviceLog(t, inv.home))
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

// PRD section 6: a later promotion relaunches a worker in the reported
// investigation's copy, with the stored report as its starting point. A
// restart between the report and the promotion ends the worker, as every
// stop does. The copy holds the worker's commit, so the next service's
// cleanup refuses it as unlanded and keeps it as one kept work entry, and
// the restarted service launches nothing for the investigation through
// several passes. A promotion there finds the worker Dead and relaunches
// in the same copy, begun with the stored report, which it reads from the
// report row and not from anything the first service held (P8). The flip
// answers the copy's entry carry on, naming the promoted task. Without the
// relaunch the promotion answers only that its brief reached nobody.
func TestAPromotionAfterARestartRelaunchesTheReportedInvestigationFromTheRecord(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P8)
	service.RequireSweepClearsHere(t, "a promotion relaunching the reported investigation after a restart")
	inv := newInvestigation(t, `{"supervision_beacon_interval": "1s"}`)
	var ended string
	inv.serve(t, func(running serviceUnderTest) {
		ended = inv.reportedAfter(t, running, func() { inv.commitInCopy(t) }).session.Reference().String()
	})
	homeIsFree(t, inv.home)

	inv.serve(t, func(running serviceUnderTest) {
		waitState(t, running.client, inv.task, "read Reported after the restart", reportedFromTheReport(taskstate.Dead))
		entry := inv.waitKept(t)
		passes()
		if _, _, held := running.service.SessionOf(inv.task); held {
			t.Fatal("the restarted service launched a worker for the reported investigation")
		}
		inv.copyStands(t, "after a restart")
		inv.relaunchedFromTheReport(t, running, inv.promote(t, running, "task/promoted-restarted"), ended)
		answered, err := recordsOf(t, inv.home).KeptWorkEntry(t.Context(), entry.ID)
		if err != nil || answered.Open() || answered.Answer.Or("") != store.KeptCarryOn || answered.CarriedOnTask.Or("") != inv.task {
			t.Fatalf("after the promotion the copy's entry reads %+v (%v), want it answered carry on by task %s", answered, err, inv.task)
		}
	})
}

// An oversized brief does not hide the reading a promotion relaunches on.
// With the worker live, an intent past the message limit is refused as
// TooLarge, nothing is sent and nothing relaunched; with the worker ended
// by a restart, the same intent still relaunches a worker in the copy from
// the report, begun with the brief through its instructions.
func TestAnOversizedBriefStillLetsAPromotionRelaunch(t *testing.T) {
	t.Parallel()
	service.RequireSweepClearsHere(t, "a promotion with an oversized brief relaunching the reported investigation")
	intent := "Cache each parse. " + strings.Repeat("x", machine.MaxMessageBytes)

	live := newInvestigation(t, `{"supervision_beacon_interval": "1s"}`)
	live.serve(t, func(running serviceUnderTest) {
		live.reported(t, running)
		promoted := live.promoteWith(t, running, "task/brief-too-large", intent)
		if !promoted.Brief.TooLarge || promoted.Brief.Message != nil || promoted.Relaunch != nil {
			t.Fatalf("an oversized brief to a live worker answered %+v, want it refused as too large and no relaunch", promoted.Brief)
		}
	})

	ended := newInvestigation(t, `{"supervision_beacon_interval": "1s"}`)
	var ref string
	ended.serve(t, func(running serviceUnderTest) {
		ref = ended.reportedAfter(t, running, func() { ended.commitInCopy(t) }).session.Reference().String()
	})
	homeIsFree(t, ended.home)
	ended.serve(t, func(running serviceUnderTest) {
		waitState(t, running.client, ended.task, "read Reported with its worker ended", reportedFromTheReport(taskstate.Dead))
		ended.waitKept(t)
		promoted := ended.promoteWith(t, running, "task/brief-too-large", intent)
		if promoted.Brief.TooLarge {
			t.Fatalf("an oversized brief to an ended worker answered %+v, want the relaunch decided instead", promoted.Brief)
		}
		ended.relaunchedFromTheReport(t, running, promoted, ref)
	})
}

// A promotion's answer claims no more than the relaunch did: the relaunch
// claims its attempt and the launch follows the answer, as task.relaunch's
// does, so a launch refused after the claim must not have been announced as
// a worker begun. The investigation is reported and its worker ended by a
// restart; the next service's worker role names a context window its agent
// does not declare, so the promotion claims attempt 2 and routing then
// refuses its launch, the task resolving Failed with no session on that
// attempt. The answer names the attempt and says whether its worker began
// shows in the listing, and its brief says the same.
func TestAPromotionsRelaunchIsAnnouncedAsClaimedNotAsBegun(t *testing.T) {
	t.Parallel()
	service.RequireSweepClearsHere(t, "a promotion relaunching the reported investigation after a restart")
	inv := newInvestigation(t, `{"supervision_beacon_interval": "1s"}`)
	inv.serve(t, func(running serviceUnderTest) {
		inv.reportedAfter(t, running, func() { inv.commitInCopy(t) })
	})
	homeIsFree(t, inv.home)
	refusing := fmt.Sprintf(`{"supervision_beacon_interval": "1s", "roles": {"worker": {"agent": %q, "provider": "anthropic", "type": "haiku", "version": "4.5", "context": "1m"}}}`, inv.agent.Runner().Name())
	if err := os.WriteFile(inv.home.ConfigFile(), []byte(refusing+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inv.serve(t, func(running serviceUnderTest) {
		waitState(t, running.client, inv.task, "read Reported after the restart", reportedFromTheReport(taskstate.Dead))
		inv.waitKept(t)
		promoted := inv.promote(t, running, "task/promoted-unlaunched")
		if promoted.Relaunch == nil || promoted.Relaunch.Attempt != 2 {
			t.Fatalf("the promotion answered %+v, want attempt 2 claimed for a relaunch", promoted)
		}
		waitState(t, running.client, inv.task, "fail on its refused launch", func(r taskstate.Resolution) bool {
			return r.State == taskstate.Failed
		})
		if attempts := attemptsOf(t, inv.home, inv.task); len(attempts) != 2 || attempts[1].SessionRef.IsKnown() {
			t.Fatalf("the task has attempts %+v, want attempt 2 claimed with no worker started", attempts)
		}
		row := machine.TaskPromotedPhrase(promoted)
		if strings.Contains(row.Consequence, "was begun") || !strings.Contains(row.Consequence, "whether it began shows in the listing") {
			t.Fatalf("a promotion whose relaunch was only claimed is phrased %q, want it to defer to the listing", row.Consequence)
		}
		if strings.Contains(promoted.Brief.Unsent, "is begun") || !strings.Contains(promoted.Brief.Unsent, "attempt 2") {
			t.Fatalf("the brief of a relaunch only claimed reads %q, want it to name attempt 2 without saying a worker began", promoted.Brief.Unsent)
		}
	})
}

// The order between a promotion's answer and its relaunch's launch is not
// one the service keeps: the launch is started in the background before the
// answer is returned, so it can finish first. Holding the answer back until
// attempt 2's worker is recorded, the answer still says only that the
// attempt was claimed, and nothing about the launch coming after it.
func TestAPromotionsAnswerClaimsNoOrderAgainstItsRelaunchsLaunch(t *testing.T) {
	t.Parallel()
	service.RequireSweepClearsHere(t, "a promotion relaunching the reported investigation after a restart")
	inv := newInvestigation(t, `{"supervision_beacon_interval": "1s"}`)
	inv.serve(t, func(running serviceUnderTest) {
		inv.reportedAfter(t, running, func() { inv.commitInCopy(t) })
	})
	homeIsFree(t, inv.home)
	inv.serve(t, func(running serviceUnderTest) {
		waitState(t, running.client, inv.task, "read Reported after the restart", reportedFromTheReport(taskstate.Dead))
		inv.waitKept(t)
		launched := false
		running.service.SetAfterPromotedLaunchStartedForTest(func(task string) {
			deadline := time.Now().Add(launchWait)
			for time.Now().Before(deadline) {
				if attempts := attemptsOf(t, inv.home, task); len(attempts) == 2 && attempts[1].SessionRef.IsKnown() {
					launched = true
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
		promoted := inv.promote(t, running, "task/promoted-launched-first")
		if !launched || promoted.Relaunch == nil || promoted.Relaunch.Attempt != 2 {
			t.Fatalf("the relaunch's worker was recorded before the answer: %v; the answer is %+v, want attempt 2 claimed", launched, promoted)
		}
		row := machine.TaskPromotedPhrase(promoted)
		for _, said := range []string{row.Consequence, promoted.Brief.Unsent} {
			if strings.Contains(said, "comes before the worker") || strings.Contains(said, "follows this answer") {
				t.Errorf("an answer returned after its relaunch's launch says the launch comes after it: %q", said)
			}
		}
		if !strings.Contains(row.Consequence, "whether it began shows in the listing") {
			t.Errorf("the answer is phrased %q, want it to leave whether the worker began to the listing", row.Consequence)
		}
	})
}
