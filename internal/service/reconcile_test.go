package service_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// pipeTerminals is a Terminals over a provider of this home built on the
// stand-in's runner. It holds no session, so it answers every reference by
// identity alone, which is exactly what a restarted service's own provider
// answers before it launches anything: Dead for a process that is gone,
// Unobserved for one that is present and unheld, and Unobserved for another
// home's reference. It captures nothing by reference.
type pipeTerminals struct{ pipe *session.Pipe }

func terminalsOver(h *home.Home, agent *standin.Agent) pipeTerminals {
	return pipeTerminals{pipe: session.NewPipe(h, agent.Runner())}
}

func (p pipeTerminals) Probe(ctx context.Context, ref string) session.Observation {
	return p.pipe.Probe(ctx, ref)
}

func (p pipeTerminals) Capture(context.Context, string) (session.Capture, error) {
	return session.Capture{}, errors.New("this double captures nothing by reference")
}

// blockingTerminals is a provider that never answers a probe until the
// context ends, and then answers Unobserved: the shape a provider that
// hangs takes behind a bound.
type blockingTerminals struct{}

func (blockingTerminals) Probe(ctx context.Context, _ string) session.Observation {
	<-ctx.Done()
	return session.Observation{Liveness: taskstate.Unobserved, Reason: session.ReasonUnreadable, Detail: ctx.Err().Error()}
}

func (blockingTerminals) Capture(context.Context, string) (session.Capture, error) {
	return session.Capture{}, errors.New("never reached: nothing probes live")
}

// waitWorkers polls the stand-in's record until n workers have been
// started, and returns them in the order they were recorded.
func waitWorkers(t *testing.T, agent *standin.Agent, n int) []standin.Call {
	t.Helper()
	deadline := time.Now().Add(launchWait)
	for {
		var workers []standin.Call
		for _, call := range agent.Calls() {
			if call.Worker {
				workers = append(workers, call)
			}
		}
		if len(workers) >= n {
			return workers
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the stand-in recorded %d workers, want %d", len(workers), n)
		}
		time.Sleep(time.Millisecond)
	}
}

// waitSession polls the service until it holds a session for the task.
func waitSession(t *testing.T, running *service.Service, id string) (session.Provider, *session.Session) {
	t.Helper()
	deadline := time.Now().Add(launchWait)
	for {
		if provider, launched, ok := running.SessionOf(id); ok {
			return provider, launched
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the service never held a session for task %s", id)
		}
		time.Sleep(time.Millisecond)
	}
}

// launchedAndEnded runs one service over the task, which dispatches and
// launches it, and closes the service, which ends the worker; the first
// attempt's reference then probes Dead by identity.
func launchedAndEnded(t *testing.T, h *home.Home, id string) session.Reference {
	t.Helper()
	var first session.Reference
	o, _ := workerOptions(t, h)
	withServiceOptions(t, o, func(running serviceUnderTest) {
		provider, launched := launchedSession(t, running.service, id)
		first = launched.Reference()
		waitCapture(t, provider, launched, "worker ready")
	})
	homeIsFree(t, h)
	return first
}

// A restart with the probe wired relaunches a dead terminal in place: the
// new service reads the first attempt's reference as Dead, claims a second
// attempt before it serves, appends the relaunched note and the dispatched
// entry, launches a worker in the standing copy with the instructions
// followed by the program's note, and records the new session on the second
// attempt. The task reads Dispatched from its new attempt through the task
// surface, whose probe is the test's and holds no session; it never reads
// Unknown from the dead terminal once the attempt exists. The terminal-gone
// wake standing for the dead session is acknowledged by the program, and a
// hold wake stays queued for the coordinator.
func TestARestartWithAProbeRelaunchesADeadTerminalInPlace(t *testing.T) {
	h := newHome(t)
	subject := newSubject(t)
	quietMaintenance(t, subject)
	repository := recordRepository(t, h, subject)
	id := recordDispatchedTask(t, h, repository, store.TaskDelivery, "task/one")
	first := launchedAndEnded(t, h, id)

	// What the supervisor loop would have queued about the dead session,
	// composed the way supervise.go composes a terminal-gone marker and
	// written under an incarnation as the loop writes, and one wake that is
	// not about it.
	records := openRecords(t, h)
	inc, err := records.BeginIncarnation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	gone, err := records.AppendWakeWithMarkers(t.Context(), inc, store.WakeRequest{
		TaskID: id, Kind: string(supervisor.KindTerminalGone),
		Marker: "terminal-gone:dead:" + first.String() + ":" + time.Now().UTC().Format(time.RFC3339Nano),
	}, store.MarkerAdvance{})
	if err != nil {
		t.Fatal(err)
	}
	hold, err := records.AppendWakeWithMarkers(t.Context(), inc, store.WakeRequest{
		TaskID: id, Kind: string(supervisor.KindHoldOpened), Marker: "hold:one",
	}, store.MarkerAdvance{})
	if err != nil {
		t.Fatal(err)
	}
	_ = records.Close()

	o, agent := workerOptions(t, h)
	o.Terminals = terminalsOver(h, agent)
	var second session.Reference
	var read string
	withServiceOptions(t, o, func(running serviceUnderTest) {
		provider, launched := waitSession(t, running.service, id)
		second = launched.Reference()
		waitCapture(t, provider, launched, "worker ready")
		if got := taskState(t, running.client, id); got.State != taskstate.Dispatched || got.Source != taskstate.SourceEvent {
			t.Fatalf("after the relaunch the task resolves %s from %s, want Dispatched from its new attempt", got.State, got.Source)
		}
		workers := waitWorkers(t, agent, 1)
		if workers[0].Dir != resolvedCopy(t, h, id) {
			t.Errorf("the relaunched worker was started in %s, want the standing copy %s", workers[0].Dir, resolvedCopy(t, h, id))
		}
		// Read while the worker is up: Close ends it, and a worker that is
		// gone reads nothing more.
		read = strings.Join(waitWorkerLines(t, agent, "This is attempt 2 of this task").Lines, "\n")
	})
	if first == second {
		t.Fatalf("the relaunch reused the first attempt's reference %s", first)
	}

	records = openRecords(t, h)
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
	if !strings.Contains(note.Detail, "attempt 2") || !strings.Contains(note.Detail, first.String()) {
		t.Fatalf("the relaunched note says %q, want the attempt and the session it found gone", note.Detail)
	}
	wakes, err := records.TaskWakes(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range wakes {
		switch w.ID {
		case gone.Wake.ID:
			if w.Queued() || w.AcknowledgedBy.Or(store.Acknowledger{}) != store.AcknowledgedByProgram() {
				t.Errorf("the terminal-gone wake for the dead session is %+v, want acknowledged by the program", w)
			}
		case hold.Wake.ID:
			if !w.Queued() {
				t.Errorf("the hold wake was acknowledged: %+v", w)
			}
		}
	}
	// The worker read the instructions file, second check included, and
	// then the note, in that order.
	instructions, _ := h.TaskInstructions(id)
	raw, err := os.ReadFile(instructions)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(read+"\n", string(raw)) {
		t.Fatalf("the worker read:\n%s\nwant it to begin with the instructions file:\n%s", read, raw)
	}
	if strings.Contains(string(raw), "Relaunched by the program") {
		t.Fatal("the note was written into the instructions file")
	}
}

// A restart without a probe leaves a launched task alone: nothing reads
// its terminal as dead, so reconciliation relaunches nothing, and dispatch
// passes over a task whose latest attempt already names a session rather
// than starting a second worker beside one it cannot see. The task still
// has its one attempt, the new service holds no session, and the task reads
// Dispatched from the record, which is PRD section 11's ambiguous liveness
// left alone.
func TestARestartWithoutAProbeLeavesALaunchedTaskAlone(t *testing.T) {
	h := newHome(t)
	subject := newSubject(t)
	quietMaintenance(t, subject)
	repository := recordRepository(t, h, subject)
	id := recordDispatchedTask(t, h, repository, store.TaskDelivery, "task/one")
	first := launchedAndEnded(t, h, id)

	o, agent := workerOptions(t, h)
	withServiceOptions(t, o, func(running serviceUnderTest) {
		if got := taskState(t, running.client, id); got.State != taskstate.Dispatched || got.Basis.Process != taskstate.Unobserved {
			t.Fatalf("the task resolves %+v, want Dispatched from the record with its terminal unobserved", got)
		}
		if _, _, ok := running.service.SessionOf(id); ok {
			t.Fatal("the restarted service launched a worker for a task whose terminal it cannot read")
		}
		for _, call := range agent.Calls() {
			if call.Worker {
				t.Fatalf("a worker was started: %s", call)
			}
		}
	})
	records := openRecords(t, h)
	defer func() { _ = records.Close() }()
	attempts, err := records.TaskAttempts(t.Context(), id)
	if err != nil || len(attempts) != 1 || attempts[0].SessionRef.Or("") != first.String() {
		t.Fatalf("the task has attempts %+v (%v), want only the first with its session", attempts, err)
	}
}

// A provider that never answers: Open returns within the bound with the
// task's terminal Unobserved, no attempt is begun and no worker started,
// across two starts. What tells the person about the silence is the
// supervisor loop's terminal-gone row, which it writes as unobservable with
// the unobserved-since marker; reconciliation writes none of its own.
func TestABlockingProviderLeavesOpenWithinTheBoundAndRelaunchesNothing(t *testing.T) {
	h := newHome(t)
	subject := newSubject(t)
	quietMaintenance(t, subject)
	repository := recordRepository(t, h, subject)
	id := recordDispatchedTask(t, h, repository, store.TaskDelivery, "task/one")
	first := launchedAndEnded(t, h, id)
	// A short beacon interval, so the loop's own bounded probes and Close's
	// grace are short too; the loop is not what this test is about.
	if err := os.WriteFile(h.ConfigFile(), []byte(`{"supervision_beacon_interval": "1s"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for pass := 1; pass <= 2; pass++ {
		o, agent := workerOptions(t, h)
		o.Terminals = blockingTerminals{}
		started := time.Now()
		withServiceOptions(t, o, func(running serviceUnderTest) {
			if elapsed := time.Since(started); elapsed > service.ReconcileProbeBound {
				t.Fatalf("pass %d: Open took %s behind a blocking provider, over the bound %s", pass, elapsed, service.ReconcileProbeBound)
			}
			if got := taskState(t, running.client, id); got.Basis.Process != taskstate.Unobserved || got.State != taskstate.Dispatched {
				t.Fatalf("pass %d: the task resolves %+v, want Dispatched with its terminal unobserved", pass, got)
			}
			if _, _, ok := running.service.SessionOf(id); ok {
				t.Fatalf("pass %d: the service launched a worker on an unobserved terminal", pass)
			}
			for _, call := range agent.Calls() {
				if call.Worker {
					t.Fatalf("pass %d: a worker was started: %s", pass, call)
				}
			}
		})
		homeIsFree(t, h)
	}
	records := openRecords(t, h)
	defer func() { _ = records.Close() }()
	attempts, err := records.TaskAttempts(t.Context(), id)
	if err != nil || len(attempts) != 1 || attempts[0].SessionRef.Or("") != first.String() {
		t.Fatalf("an unobserved terminal was relaunched: %+v (%v)", attempts, err)
	}
}

// A Queued task at start goes through Release: its blocker reported while
// no service watched, so the service that opens next releases it before it
// serves, and the task reads Dispatched through the first request.
func TestAQueuedTaskAtStartIsReleased(t *testing.T) {
	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	blocker, successor := queuedBehindABlocker(t, h, repository)
	records := openRecords(t, h)
	if _, err := records.AppendTaskEvent(t.Context(), blocker, "report-ready", "the blocker's worker delivered"); err != nil {
		t.Fatal(err)
	}
	_ = records.Close()

	withService(t, h, func(running serviceUnderTest) {
		if got := taskState(t, running.client, blocker); got.State != taskstate.Reported {
			t.Fatalf("the blocker resolves %s, want Reported", got.State)
		}
		if got := taskState(t, running.client, successor); got.State != taskstate.Dispatched || got.Source != taskstate.SourceEvent {
			t.Fatalf("the successor resolves %s from %s, want Dispatched by the release at open", got.State, got.Source)
		}
	})
}
