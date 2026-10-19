package service_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// codexWorkerOptions is options over a Codex adapter reading the stand-in's
// app-server, as launch_codex_test.go builds it. A Codex worker's
// instructions, and a relaunch's note after them, arrive as one turn, so the
// note is answered before the instructions' last line: the first exchange
// that matches a turn answers it.
func codexWorkerOptions(t *testing.T, h *home.Home) (service.Options, *standin.Agent) {
	t.Helper()
	agent := standin.NewCodex(t, standin.Script{Worker: &standin.WorkerScript{
		Exchanges: []standin.Exchange{
			{Contains: "This is attempt", Reply: "the relaunch note was read"},
			{Contains: "git rev-parse --show-toplevel", Reply: "the second check was read"},
		},
	}}, agents.WithCodexTerminationGrace(2*time.Second))
	o := options(t, h)
	o.Catalog = agents.NewCatalog(fixedFactory{runner: agent.Runner()}, cursorFactory{runner: agent.Runner()})
	return o, agent
}

// A restart over a Codex worker reconciles the task from the record and
// the probe, the way it does a Claude worker's: the first service launches
// the app-server and its Close ends it, so the new service's probe answers
// the first attempt's reference Dead by identity, and reconcile.go's table
// relaunches it in place before the service serves. The second attempt
// carries a new session, the history ends relaunched then dispatched, the
// relaunched app-server is started in the standing copy and is given the
// instructions with the program's note in its first turn, and the task
// reads Running from the process tier through that session.
func TestARestartRelaunchesADeadCodexWorkerInPlace(t *testing.T) {
	t.Parallel()
	service.RequireSweepClearsHere(t, "a restart relaunching a dead Codex worker in place")
	h := newHome(t)
	subject := newSubject(t)
	quietMaintenance(t, subject)
	repository := recordRepository(t, h, subject)
	id := recordDispatchedTask(t, h, repository, store.TaskDelivery, "task/one")

	var first session.Reference
	var ended session.Provider
	o, _ := codexWorkerOptions(t, h)
	withServiceOptions(t, o, func(running serviceUnderTest) {
		provider, launched := launchedSession(t, running.service, id)
		ended, first = provider, launched.Reference()
		waitCapture(t, provider, launched, "the second check was read")
	})
	homeIsFree(t, h)
	if probe := ended.Probe(t.Context(), first.String()); probe.Liveness != taskstate.Dead {
		t.Fatalf("after the first service closed the Codex worker probes %+v, want Dead", probe)
	}

	o, agent := codexWorkerOptions(t, h)
	var second session.Reference
	var call standin.Call
	withServiceOptions(t, o, func(running serviceUnderTest) {
		provider, launched := waitSession(t, running.service, id)
		second = launched.Reference()
		waitCapture(t, provider, launched, "the relaunch note was read")
		if got := taskState(t, running.client, id); !readsRunning(got) {
			t.Fatalf("after the relaunch the task resolves %+v, want Running from the process tier through its new session", got)
		}
		call = workerCall(t, agent)
	})
	if first == second {
		t.Fatalf("the relaunch reused the first attempt's reference %s", first)
	}
	if call.Dir != resolvedCopy(t, h, id) {
		t.Errorf("the relaunched app-server was started in %s, want the standing copy %s", call.Dir, resolvedCopy(t, h, id))
	}
	if !slices.Contains(call.Args, "app-server") {
		t.Errorf("the relaunched worker ran %q, want the Codex app-server", call.Args)
	}
	if len(call.Lines) == 0 || !strings.Contains(call.Lines[0], "git rev-parse --show-toplevel") || !strings.Contains(call.Lines[0], "This is attempt") {
		t.Fatalf("the relaunched worker's first turn did not carry the instructions and the note: %q", call.Lines)
	}

	records := openRecords(t, h)
	defer func() { _ = records.Close() }()
	attempts, err := records.TaskAttempts(t.Context(), id)
	if err != nil || len(attempts) != 2 {
		t.Fatalf("the task has attempts %+v (%v), want the launch's and the relaunch's", attempts, err)
	}
	if attempts[0].SessionRef.Or("") != first.String() || attempts[1].SessionRef.Or("") != second.String() {
		t.Fatalf("the attempts carry %+v and %+v, want %s then %s", attempts[0].SessionRef, attempts[1].SessionRef, first, second)
	}
	events, err := records.TaskEvents(t.Context(), id, 0, 0)
	if err != nil || len(events) < 2 {
		t.Fatalf("the history is %+v (%v)", events, err)
	}
	note, moved := events[len(events)-2], events[len(events)-1]
	if note.Kind != "relaunched" || moved.Kind != store.DispatchedEventKind {
		t.Fatalf("the history ends %s then %s, want relaunched then dispatched", note.Kind, moved.Kind)
	}
	if !strings.Contains(note.Detail, first.String()) {
		t.Fatalf("the relaunched note says %q, want the session it found gone", note.Detail)
	}
}
