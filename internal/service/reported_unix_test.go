//go:build unix

package service_test

import (
	"os"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// wakesOf reads a task's wake rows, queued or acknowledged, of one kind.
func wakesOf(t *testing.T, h *home.Home, task string, kind supervisor.Kind) []store.Wake {
	t.Helper()
	wakes, err := recordsOf(t, h).TaskWakes(t.Context(), task)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.Wake
	for _, w := range wakes {
		if w.Kind == string(kind) {
			out = append(out, w)
		}
	}
	return out
}

// waitState polls the task surface until the task's resolution satisfies
// want, and returns it.
func waitState(t *testing.T, client *ipc.Client, task, what string, want func(taskstate.Resolution) bool) taskstate.Resolution {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		got := taskState(t, client, task)
		if want(got) {
			return got
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("task %s never %s; it resolves %+v", task, what, got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// reportedFromTheReport is a reported investigation as PRD section 6's
// report tier resolves it: Reported from the stored report, with the
// terminal as it was read.
func reportedFromTheReport(process taskstate.Liveness) func(taskstate.Resolution) bool {
	return func(r taskstate.Resolution) bool {
		return r.State == taskstate.Reported && r.Source == taskstate.SourceReport && r.Basis.ReportStored &&
			r.Basis.Process == process
	}
}

// An investigation whose worker files its report reads Reported, from the
// stored report, while its worker is live and after the worker has ended,
// and the supervisor loop raises no terminal-gone wake for it; a control
// investigation in the same service that never reported and whose worker
// ended reads Unknown from its dead terminal and does raise one. Both
// workers are the stand-in, launched by the service, and the report is
// filed by the reporting worker running the call itself, so the row the
// state rests on is the one the service's own path wrote (P8). The reported
// task is watched when its worker ends: its report raised an ask, the loop
// queued a hold-opened wake for it, and the ask is answered without that
// wake being acknowledged, so the queue keeps the task under the loop and
// what keeps its terminal unread is the terminal table and not the task
// falling out of the watched set. The control's terminal-gone wake is
// awaited after the reported worker has already read Dead, so the pass
// that surfaced it observed both dead terminals.
//
// A restart then reconciles both: the control is relaunched in place, a
// second attempt and a session for it, and the reported investigation is
// left with its one attempt and no session.
func TestAReportedInvestigationOutranksItsTerminal(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P8)
	service.RequireSweepClearsHere(t, "a restart relaunching the control investigation in place")
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	quietMaintenance(t, subject)
	repository := recordRepository(t, h, subject)
	quietMaintenance(t, gateRepositoryOf(t, h, subject))
	if err := os.WriteFile(h.ConfigFile(), []byte(`{"supervision_beacon_interval": "1s"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reporting := recordDispatchedTask(t, h, repository, store.TaskInvestigation, "")
	control := recordDispatchedTask(t, h, repository, store.TaskInvestigation, "")

	o, _ := workerOptions(t, h)
	withServiceOptions(t, o, func(running serviceUnderTest) {
		workerOf := func(id string) launched {
			provider, session := waitSession(t, running.service, id)
			waitCapture(t, provider, session, "worker ready")
			return launched{running: running, home: h, repository: repository, task: id, group: session.Reference().PID,
				provider: provider, session: session}
		}
		reporter, crasher := workerOf(reporting), workerOf(control)

		report := `{"summary":"looked","findings":[{"severity":"warning","action":"ask","description":"Should the old path be kept?"}]}`
		if _, code := callAs(t, reporter, ipc.MethodTaskEvent, machine.TaskEventRequest{Kind: store.ReportReadyEventKind, Detail: report}); code != "" {
			t.Fatalf("the worker's report was refused with %q", code)
		}
		deadline := time.Now().Add(30 * time.Second)
		for len(wakesOf(t, h, reporting, supervisor.KindHoldOpened)) == 0 {
			if !time.Now().Before(deadline) {
				t.Fatal("the loop queued no hold-opened wake for the report's ask")
			}
			time.Sleep(20 * time.Millisecond)
		}
		var answered machine.TaskAnswered
		if err := running.client.Call(t.Context(), ipc.MethodTaskRespond,
			machine.TaskRespondRequest{Task: reporting, Answer: "approve"}, &answered); err != nil {
			t.Fatalf("answering the report's ask: %v", err)
		}
		for _, w := range wakesOf(t, h, reporting, supervisor.KindHoldOpened) {
			if !w.Queued() {
				t.Fatalf("the hold-opened wake was acknowledged, so the task would not be watched: %+v", w)
			}
		}

		waitState(t, running.client, reporting, "read Reported from its report with its worker live", reportedFromTheReport(taskstate.Live))
		if got := observed(t, taskState, running.client, control); !readsRunning(got) {
			t.Fatalf("the control investigation resolves %+v while its worker runs, want Running from the process tier", got)
		}

		if err := reporter.provider.Send(t.Context(), reporter.session, "finish"); err != nil {
			t.Fatalf("Send: %v", err)
		}
		waitCapture(t, reporter.provider, reporter.session, "worker finished")
		waitState(t, running.client, reporting, "read Reported from its report with its worker dead", reportedFromTheReport(taskstate.Dead))

		if err := crasher.provider.Send(t.Context(), crasher.session, "finish"); err != nil {
			t.Fatalf("Send: %v", err)
		}
		waitCapture(t, crasher.provider, crasher.session, "worker finished")
		got := waitState(t, running.client, control, "read Unknown from its dead terminal", func(r taskstate.Resolution) bool {
			return r.State == taskstate.Unknown && r.Source == taskstate.SourceProcess && r.Basis.Process == taskstate.Dead
		})
		if got.Basis.ReportStored {
			t.Fatalf("the control investigation's basis says a report is stored: %+v", got.Basis)
		}
		deadline = time.Now().Add(30 * time.Second)
		for len(wakesOf(t, h, control, supervisor.KindTerminalGone)) == 0 {
			if !time.Now().Before(deadline) {
				t.Fatal("the loop raised no terminal-gone wake for the control investigation whose worker ended")
			}
			time.Sleep(20 * time.Millisecond)
		}
		if gone := wakesOf(t, h, reporting, supervisor.KindTerminalGone); len(gone) != 0 {
			t.Fatalf("the loop raised terminal-gone for the reported investigation: %+v", gone)
		}
		waitState(t, running.client, reporting, "stayed Reported once both terminals were dead", reportedFromTheReport(taskstate.Dead))
	})
	homeIsFree(t, h)

	o, _ = workerOptions(t, h)
	withServiceOptions(t, o, func(running serviceUnderTest) {
		waitSession(t, running.service, control)
		if _, _, held := running.service.SessionOf(reporting); held {
			t.Fatal("the restart launched a worker for the reported investigation")
		}
	})
	records := openRecords(t, h)
	defer func() { _ = records.Close() }()
	if attempts, err := records.TaskAttempts(t.Context(), control); err != nil || len(attempts) != 2 {
		t.Fatalf("the control investigation has attempts %+v (%v), want the launch's and the relaunch's", attempts, err)
	}
	if attempts, err := records.TaskAttempts(t.Context(), reporting); err != nil || len(attempts) != 1 {
		t.Fatalf("the reported investigation has attempts %+v (%v), want its one launch", attempts, err)
	}
}
