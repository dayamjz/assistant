package service_test

import (
	"context"
	"os"
	"path/filepath"
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

// launchWait bounds every poll in these tests. A worker answers on its own
// schedule, and a test synchronizes on what the provider's Capture shows and
// on what the stand-in recorded, never on a file it polls (#124).
const launchWait = 30 * time.Second

// workerAgent is scriptedAgent over the one adapter in this build that
// declares worker sessions, agents.ScriptedFactory through
// standin.NewWorker, so a service resolving "auto" reaches an agent a task's
// worker can be started on. The review step is the same one scriptedAgent
// carries, for the same reason; the worker greets, answers a status line,
// and otherwise lingers until the service ends it.
func workerAgent(t *testing.T) (*agents.Catalog, *standin.Agent) {
	t.Helper()
	agent := standin.NewWorker(t, standin.Script{
		Steps: []standin.Step{{
			Match: standin.MatchReview(),
			Times: standin.Always,
			Reply: standin.Reviewed("the change reads cleanly"),
		}},
		Worker: &standin.WorkerScript{
			Greeting:  "worker ready",
			Exchanges: []standin.Exchange{{Contains: "status", Reply: "still working on it"}},
		},
	}, agents.WithTerminationGrace(2*time.Second))
	return agents.NewCatalog(fixedFactory{runner: agent.Runner()}), agent
}

// workerOptions is options with the worker-capable agent in place of the
// shipped adapter's stand-in.
func workerOptions(t *testing.T, h *home.Home) (service.Options, *standin.Agent) {
	t.Helper()
	o := options(t, h)
	catalog, agent := workerAgent(t)
	o.Catalog = catalog
	return o, agent
}

// waitCapture polls the provider's Capture until the text carries want.
func waitCapture(t *testing.T, p session.Provider, s *session.Session, want string) session.Capture {
	t.Helper()
	deadline := time.Now().Add(launchWait)
	for {
		got, err := p.Capture(s)
		if err != nil {
			t.Fatalf("Capture: %v", err)
		}
		if strings.Contains(got.Text, want) {
			return got
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the capture never carried %q; it is:\n%s", want, got.Text)
		}
		time.Sleep(time.Millisecond)
	}
}

// waitWorkerLines polls the stand-in's record until its one worker has read
// a line carrying want, and returns that call. The worker rewrites its
// record as each line arrives, so a line is waited for rather than read once.
func waitWorkerLines(t *testing.T, agent *standin.Agent, want string) standin.Call {
	t.Helper()
	deadline := time.Now().Add(launchWait)
	for {
		var workers []standin.Call
		for _, call := range agent.Calls() {
			if call.Worker {
				workers = append(workers, call)
			}
		}
		if len(workers) > 1 {
			t.Fatalf("the stand-in recorded %d workers, want one: %s", len(workers), workers)
		}
		if len(workers) == 1 {
			for _, line := range workers[0].Lines {
				if strings.Contains(line, want) {
					return workers[0]
				}
			}
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("no worker read a line carrying %q; the record is %s", want, workers)
		}
		time.Sleep(time.Millisecond)
	}
}

// launchedSession is the session the service holds for a task, or a fatal
// failure.
func launchedSession(t *testing.T, running *service.Service, id string) (session.Provider, *session.Session) {
	t.Helper()
	provider, launched, ok := running.SessionOf(id)
	if !ok {
		t.Fatalf("the service holds no session for task %s", id)
	}
	return provider, launched
}

// resolvedCopy is the task's copy path as the proof resolves it, with every
// symbolic link followed, which is the directory a worker is started in.
func resolvedCopy(t *testing.T, h *home.Home, id string) string {
	t.Helper()
	copyPath, err := h.TaskCopy(id)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// A dispatched delivery task is launched: the service holds a session for
// it whose capture shows the worker's greeting, the attempt the dispatch
// began carries the session's reference, the reference decodes to this home
// and this task, the worker was started in the proven copy and read exactly
// the instructions file, second check included. What the launch does not
// move is the task's resolved state: no probe is wired into the service's
// reader, so the task still resolves Dispatched from the record. And Close
// ends the worker: the reference probes Dead afterwards.
func TestADispatchedDeliveryTaskIsLaunched(t *testing.T) {
	h := newHome(t)
	subject := newSubject(t)
	quietMaintenance(t, subject)
	repository := recordRepository(t, h, subject)
	id := recordDispatchedTask(t, h, repository, store.TaskDelivery, "task/one")
	o, agent := workerOptions(t, h)

	var ref session.Reference
	var provider session.Provider
	withServiceOptions(t, o, func(running serviceUnderTest) {
		var launched *session.Session
		provider, launched = launchedSession(t, running.service, id)
		ref = launched.Reference()
		got := waitCapture(t, provider, launched, "worker ready")
		if got.Digest != session.Digest(got.Text) {
			t.Errorf("the capture's digest is not the digest of its text: %+v", got)
		}
		if err := provider.Send(t.Context(), launched, "status?"); err != nil {
			t.Fatalf("Send: %v", err)
		}
		waitCapture(t, provider, launched, "still working on it")
		if probe := provider.Probe(t.Context(), ref.String()); probe.Liveness != taskstate.Live {
			t.Fatalf("the launched session probes %+v, want Live through the service's handle", probe)
		}
		if got := taskState(t, running.client, id); got.State != taskstate.Dispatched || got.Source != taskstate.SourceEvent {
			t.Fatalf("after the launch the task resolves %s from %s; the probe is not wired into the reader, "+
				"so the record's Dispatched is the answer", got.State, got.Source)
		}
		call := waitWorkerLines(t, agent, "git rev-parse --show-toplevel")
		if call.Dir != resolvedCopy(t, h, id) {
			t.Errorf("the worker was started in %s, want the proven copy %s", call.Dir, resolvedCopy(t, h, id))
		}
	})

	if ref.Home != session.HomeMarker(h) || ref.Task != id || ref.PID <= 0 {
		t.Fatalf("the session's reference is %+v, want this home, task %s and a process", ref, id)
	}
	records := openRecords(t, h)
	defer func() { _ = records.Close() }()
	attempts, err := records.TaskAttempts(t.Context(), id)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("the task has attempts %+v (%v), want exactly the dispatch's", attempts, err)
	}
	recorded, ok := attempts[0].SessionRef.Get()
	if !ok || recorded != ref.String() {
		t.Fatalf("the attempt carries session %+v, want the launched session's reference %s", attempts[0].SessionRef, ref)
	}
	decoded, err := session.ParseReference(recorded)
	if err != nil || decoded != ref {
		t.Fatalf("the recorded reference decodes to %+v (%v), want %+v", decoded, err, ref)
	}
	// The worker read the instructions file first, line for line: the
	// coordinator's text intake wrote and the second check dispatch
	// appended, which is the file's last line; what follows it in the
	// record is the status line this test sent.
	instructions, _ := h.TaskInstructions(id)
	raw, err := os.ReadFile(instructions)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	call := waitWorkerLines(t, agent, "git rev-parse --show-toplevel")
	var read []string
	for _, line := range call.Lines {
		read = append(read, line)
		if strings.Contains(line, "git rev-parse --show-toplevel") {
			break
		}
	}
	if got := strings.Join(read, "\n") + "\n"; got != text {
		t.Fatalf("the worker read:\n%s\nwant the instructions file:\n%s", got, text)
	}
	if !strings.Contains(text, "Do the work the coordinator asked for.") {
		t.Fatalf("the instructions lost the coordinator's text:\n%s", text)
	}
	probe := provider.Probe(t.Context(), ref.String())
	if probe.Liveness != taskstate.Dead {
		t.Fatalf("after Close the session probes %+v, want Dead: the service did not end its worker", probe)
	}
}

// An investigation task is launched the same way, in its detached copy.
func TestADispatchedInvestigationTaskIsLaunched(t *testing.T) {
	h := newHome(t)
	subject := newSubject(t)
	quietMaintenance(t, subject)
	repository := recordRepository(t, h, subject)
	id := recordDispatchedTask(t, h, repository, store.TaskInvestigation, "")
	o, agent := workerOptions(t, h)

	var ref session.Reference
	withServiceOptions(t, o, func(running serviceUnderTest) {
		provider, launched := launchedSession(t, running.service, id)
		ref = launched.Reference()
		waitCapture(t, provider, launched, "worker ready")
		if got := taskState(t, running.client, id); got.State != taskstate.Dispatched {
			t.Fatalf("after the launch the task resolves %s", got.State)
		}
		call := waitWorkerLines(t, agent, "git rev-parse --show-toplevel")
		if call.Dir != resolvedCopy(t, h, id) {
			t.Errorf("the worker was started in %s, want the proven copy %s", call.Dir, resolvedCopy(t, h, id))
		}
	})

	if ref.Home != session.HomeMarker(h) || ref.Task != id {
		t.Fatalf("the session's reference is %+v, want this home and task %s", ref, id)
	}
	records := openRecords(t, h)
	defer func() { _ = records.Close() }()
	attempts, err := records.TaskAttempts(t.Context(), id)
	if err != nil || len(attempts) != 1 || attempts[0].SessionRef.Or("") != ref.String() {
		t.Fatalf("the task has attempts %+v (%v), want one carrying %s", attempts, err, ref)
	}
}

// A service resolved to an adapter that has not declared worker sessions,
// which the shipped adapter's stand-in is, dispatches the task and launches
// nothing: the task stays Dispatched with its attempt standing and no
// session recorded on it, and the service holds no session. The typed
// refusal behind that is launch_internal_test.go's to show; this is the
// record's view of a fleet over such an adapter.
func TestAnAdapterWithoutWorkerSessionsDispatchesAndNeverLaunches(t *testing.T) {
	h := newHome(t)
	subject := newSubject(t)
	quietMaintenance(t, subject)
	repository := recordRepository(t, h, subject)
	id := recordDispatchedTask(t, h, repository, store.TaskDelivery, "task/one")

	withService(t, h, func(running serviceUnderTest) {
		if got := taskState(t, running.client, id); got.State != taskstate.Dispatched || got.Source != taskstate.SourceEvent {
			t.Fatalf("the task resolves %s from %s, want Dispatched from the record", got.State, got.Source)
		}
		if _, _, ok := running.service.SessionOf(id); ok {
			t.Fatal("the service holds a session for a task its adapter cannot start a worker for")
		}
	})

	records := openRecords(t, h)
	defer func() { _ = records.Close() }()
	attempts, err := records.TaskAttempts(t.Context(), id)
	if err != nil || len(attempts) != 1 || attempts[0].SessionRef.IsKnown() {
		t.Fatalf("the task has attempts %+v (%v), want the dispatch's one with no session", attempts, err)
	}
	if task, err := records.Task(t.Context(), id); err != nil || !task.StartSnapshot.IsKnown() {
		t.Fatalf("the dispatch did not reach proven: %+v, %v", task, err)
	}
}

// A restart relaunches: the new service holds no slot and no session, so it
// dispatches the task again, which is a new attempt over the standing copy,
// and launches that attempt with a new reference. The first attempt keeps
// its own reference, which the new service's provider probes by identity
// alone and never Live: here Dead, because the first service's Close ended
// its worker, and Unobserved had it survived. The task still resolves
// Dispatched throughout.
func TestARestartRelaunchesTheTaskAsANewAttempt(t *testing.T) {
	h := newHome(t)
	subject := newSubject(t)
	quietMaintenance(t, subject)
	repository := recordRepository(t, h, subject)
	id := recordDispatchedTask(t, h, repository, store.TaskDelivery, "task/one")

	var first session.Reference
	o, _ := workerOptions(t, h)
	withServiceOptions(t, o, func(running serviceUnderTest) {
		provider, launched := launchedSession(t, running.service, id)
		first = launched.Reference()
		waitCapture(t, provider, launched, "worker ready")
	})
	homeIsFree(t, h)

	var second session.Reference
	o, _ = workerOptions(t, h)
	withServiceOptions(t, o, func(running serviceUnderTest) {
		provider, launched := launchedSession(t, running.service, id)
		second = launched.Reference()
		waitCapture(t, provider, launched, "worker ready")
		if got := taskState(t, running.client, id); got.State != taskstate.Dispatched {
			t.Fatalf("after the relaunch the task resolves %s", got.State)
		}
		probe := provider.Probe(context.Background(), first.String())
		if probe.Liveness == taskstate.Live {
			t.Fatalf("the new provider probes the first attempt's reference %+v, which it holds no handle for", probe)
		}
		if probe.Liveness != taskstate.Dead {
			t.Fatalf("the first service's Close ended its worker, so its reference should probe Dead; it probes %+v", probe)
		}
	})
	if first == second {
		t.Fatalf("the relaunch reused the first attempt's reference %s", first)
	}

	records := openRecords(t, h)
	defer func() { _ = records.Close() }()
	attempts, err := records.TaskAttempts(t.Context(), id)
	if err != nil || len(attempts) != 2 {
		t.Fatalf("the task has attempts %+v (%v), want one per service", attempts, err)
	}
	if attempts[0].SessionRef.Or("") != first.String() || attempts[1].SessionRef.Or("") != second.String() {
		t.Fatalf("the attempts carry %+v and %+v, want %s then %s",
			attempts[0].SessionRef, attempts[1].SessionRef, first, second)
	}
}
