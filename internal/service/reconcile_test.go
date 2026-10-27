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

// waitSession polls the service until it holds a session for the task,
// and fails the test when launchWait runs out first.
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

// withRestartedService opens a service over o, the restart that relaunches
// task id's dead terminal as it opens, and hands body that service and the
// session the relaunch launched; the service is closed when body returns.
//
// The relaunch sweeps the task's standing copy first, inside Open, and is
// refused, and left for the next start, while any process of this host
// that the sweep cannot read, and that neither of agents.Sweeper's
// carve-outs sets aside, runs, as P6 requires. Nothing a test does orders
// against the host's processes, so one can start after
// service.RequireSweepClearsHere looked and be gone before the test looks
// again, and a wait for the session alone then failed on P6's refusal
// working (#586). So every sweep the service asks is recorded, the restart
// must have asked exactly one of the copy, and that sweep is held to
// service.SweepsAsked.UntilClear: a refusal its own answer puts on
// processes the test did not place in the copy is met by closing the
// service and opening the next, which is the next start and asks the next
// sweep, and a run of them reports the host on that helper's terms. A
// refused relaunch records nothing, so the next start reads what this one
// read. Once a sweep has cleared, the relaunch went ahead, and a session
// that does not follow fails the test: no sweep explains it.
func withRestartedService(t *testing.T, o service.Options, h *home.Home, id, unestablished string,
	body func(running serviceUnderTest, provider session.Provider, launched *session.Session)) {
	t.Helper()
	o, sweeps := service.WithSweepsRecordedForTest(o)
	copyPath, err := h.TaskCopy(id)
	if err != nil {
		t.Fatal(err)
	}
	running, closeService := openService(t, o)
	defer func() { closeService() }()
	if n := sweeps.Of(copyPath); n != 1 {
		t.Fatalf("the restart asked %d sweep(s) of the copy %s as it opened, want the relaunch's one", n, copyPath)
	}
	sweeps.UntilClear(t, unestablished, copyPath, func() {
		closeService()
		running, closeService = openService(t, o)
	})
	provider, launched := waitSession(t, running.service, id)
	body(running, provider, launched)
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

// A restart relaunches a dead terminal in place, through the service's own
// provider and nothing injected: the new service's probe answers the first
// attempt's reference Dead by identity, because the process Close ended is
// gone, so it claims a second attempt before it serves, appends the
// relaunched note and the dispatched entry, launches a worker in the
// standing copy with the instructions followed by the program's note, and
// records the new session on the second attempt. The task then reads
// Running from the process tier through the task surface, because the same
// provider holds the new session; it never reads Unknown from the dead
// terminal once the attempt exists. The terminal-gone wake standing for the
// dead session is acknowledged by the program, and a hold wake stays queued
// for the coordinator.
func TestARestartRelaunchesADeadTerminalInPlace(t *testing.T) {
	t.Parallel()
	const unestablished = "a restart relaunching a dead terminal in place"
	service.RequireSweepClearsHere(t, unestablished)
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
	var second session.Reference
	var read string
	withRestartedService(t, o, h, id, unestablished, func(running serviceUnderTest, provider session.Provider, launched *session.Session) {
		second = launched.Reference()
		waitCapture(t, provider, launched, "worker ready")
		if got := observed(t, taskState, running.client, id); !readsRunning(got) {
			t.Fatalf("after the relaunch the task resolves %+v, want Running from the process tier through its new session", got)
		}
		// The worker prints this once it has read the note's first line,
		// after recording it, so the record is complete when it is read
		// once below; it is read while the worker is up, because Close ends
		// it, and polling the record while it reads is what
		// standin.Agent.Calls says ends a worker on Windows.
		waitCapture(t, provider, launched, "the relaunch note was read")
		var workers []standin.Call
		for _, call := range agent.Calls() {
			if call.Worker {
				workers = append(workers, call)
			}
		}
		if len(workers) != 1 {
			t.Fatalf("the stand-in recorded %d workers, want the relaunched one", len(workers))
		}
		if workers[0].Dir != resolvedCopy(t, h, id) {
			t.Errorf("the relaunched worker was started in %s, want the standing copy %s", workers[0].Dir, resolvedCopy(t, h, id))
		}
		read = strings.Join(workers[0].Lines, "\n")
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

// A provider that never answers: Open returns within the bound with the
// task's terminal Unobserved, no attempt is begun and no worker started,
// across two starts. What tells the person about the silence is the
// supervisor loop's terminal-gone row, which it writes as unobservable with
// the unobserved-since marker; reconciliation writes none of its own.
//
// It does not run in parallel. It bounds how long Open takes from above, at
// the reconciliation probe's bound, and the package's other tests running
// beside it would be measuring their load rather than Open. A serial test
// runs before any parallel one resumes, so it measures an otherwise idle
// process.
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
	t.Parallel()
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
