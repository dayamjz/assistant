//go:build unix

package service_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
	"github.com/dayamjz/assistant/internal/taskstate"
)

// These tests drive PRD section 6's steering over the service's real socket,
// against the shipped Claude Code adapter's worker: the stand-in in stream
// mode, which reads one stream-json user message per line, so a message the
// program sends is framed by the real adapter and read off the wire by the
// process. A request from the worker is a command the worker runs in a
// process group of its own, on workers_unix_test.go's terms. Every effect is
// read back from the store, or from the stand-in's own record of the lines
// it read, rather than taken from an answer.

// The stream worker's script. Each reply names what it answers, so a test
// waits on the worker's output for the line it needs. A relaunched worker is
// told the instructions, the relaunch note and the stop's note in one
// message, so the stop's exchange comes first.
func streamWorkerAgent(t *testing.T) (*agents.Catalog, *standin.Agent) {
	t.Helper()
	agent := standin.New(t, standin.Script{
		Steps: []standin.Step{{
			Match: standin.MatchReview(),
			Times: standin.Always,
			Reply: standin.Reviewed("the change reads cleanly"),
		}},
		Worker: &standin.WorkerScript{
			Exchanges: []standin.Exchange{
				// A command the test has the worker run, on launch_test.go's
				// workerAgent's terms.
				{Contains: shellMarker, Run: true},
				{Contains: "Stopped by the coordinator", Reply: "the stop note was read"},
				{Contains: "git rev-parse --show-toplevel", Reply: "the instructions were read"},
				{Contains: "stay on scope", Reply: "heard: stay on scope"},
				{Contains: "finish", Reply: "worker finished", Ends: true},
			},
		},
	}, agents.WithTerminationGrace(2*time.Second))
	return agents.NewCatalog(fixedFactory{runner: agent.Runner()}), agent
}

// streamWorker records and dispatches a delivery task, serves a service
// over the stream worker with the configuration given, and hands the body
// the running service, the task, the worker, and the stand-in.
func streamWorker(t *testing.T, branch, config string, body func(w launched, agent *standin.Agent)) {
	t.Helper()
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	quietMaintenance(t, subject)
	repository := recordRepository(t, h, subject)
	quietMaintenance(t, gateRepositoryOf(t, h, subject))
	if err := os.WriteFile(h.ConfigFile(), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	id := recordDispatchedTask(t, h, repository, store.TaskDelivery, branch)
	o := options(t, h)
	catalog, agent := streamWorkerAgent(t)
	o.Catalog = catalog
	withServiceOptions(t, o, func(running serviceUnderTest) {
		provider, session := launchedSession(t, running.service, id)
		waitCapture(t, provider, session, "the instructions were read")
		body(launched{running: running, home: h, repository: repository, gateRepo: gateRepositoryOf(t, h, subject),
			task: id, group: session.Reference().PID, provider: provider, session: session}, agent)
	})
}

// steerConfig is the home's configuration for these tests: the beacon at its
// floor, and relaunch_attempts at one, so the second relaunch a stop asks
// for meets the bound.
const steerConfig = `{"supervision_beacon_interval": "1s", "relaunch_attempts": 1}` + "\n"

// messagesOf is the task's message records, oldest first.
func messagesOf(t *testing.T, w launched) []store.Message {
	t.Helper()
	got, err := recordsOf(t, w.home).TaskMessages(t.Context(), w.task)
	if err != nil {
		t.Fatalf("reading the messages of task %s: %v", w.task, err)
	}
	return got
}

// linesCarrying counts the lines the worker process read that carry text,
// from the stand-in's own record of its input.
func linesCarrying(t *testing.T, agent *standin.Agent, text string) int {
	t.Helper()
	n := 0
	for _, call := range agent.Calls() {
		if !call.Worker {
			continue
		}
		if !call.Stream {
			t.Fatalf("the worker read %d lines in line mode, want stream mode", len(call.Lines))
		}
		for _, line := range call.Lines {
			if strings.Contains(line, text) {
				n++
			}
		}
	}
	return n
}

// PRD section 6's message, and P8 for its acknowledgement. task.message is
// the coordinator's: from the worker it is refused with the
// worker code and no record is written; a request the program cannot carry
// whole, empty or over the fixed limit, is refused before anything is
// recorded. From this process, in no worker group, the record is appended
// and the text is written to the worker as one stream-json user message,
// which the worker answers; the record says written and the coordinator
// sent it, and the worker read the text exactly once. The worker
// acknowledges through task.event, and the store holds the acknowledgement
// on the record naming the event the history gained in the same write; an
// acknowledgement repeated, of a message that is not one, or from a caller
// in no worker group is refused, and the history does not grow.
func TestAMessageIsRecordedWrittenAndAcknowledgedFromTheRecord(t *testing.T) {
	principles.Cite(t, principles.P8)
	streamWorker(t, "task/message", steerConfig, func(w launched, agent *standin.Agent) {
		running, task := w.running, w.task
		request := machine.TaskMessageRequest{Task: task, Text: "stay on scope: the parser only"}

		if _, code := callAs(t, w, ipc.MethodTaskMessage, request); code != ipc.CodeWorker {
			t.Fatalf("task.message from the worker answered %q, want %q", code, ipc.CodeWorker)
		}
		for _, bad := range []machine.TaskMessageRequest{
			{Task: task, Text: "   "},
			{Task: task, Text: strings.Repeat("x", machine.MaxMessageBytes+1)},
			{Text: "no task"},
		} {
			var refused machine.TaskMessaged
			if err := running.client.Call(t.Context(), ipc.MethodTaskMessage, bad, &refused); !errors.Is(err, ipc.ErrInvalidRequest) {
				t.Fatalf("a message of %d bytes to %q = %v, want ErrInvalidRequest", len(bad.Text), bad.Task, err)
			}
		}
		if got := messagesOf(t, w); len(got) != 0 {
			t.Fatalf("refused requests recorded messages: %+v", got)
		}

		var sent machine.TaskMessaged
		if err := running.client.Call(t.Context(), ipc.MethodTaskMessage, request, &sent); err != nil {
			t.Fatalf("task.message from outside every worker group: %v", err)
		}
		if sent.Delivery != store.DeliveryWritten || sent.SentBy != store.SentByCoordinator().String() || sent.Message.Attempt != 1 {
			t.Fatalf("task.message answered %+v, want attempt 1's message written and sent by the coordinator", sent)
		}
		waitCapture(t, w.provider, w.session, "heard: stay on scope")
		stored := messagesOf(t, w)
		if len(stored) != 1 || stored[0].Delivery() != store.DeliveryWritten || stored[0].Acknowledged() ||
			stored[0].SentBy != store.SentByCoordinator() || stored[0].Text != request.Text {
			t.Fatalf("the record holds %+v, want one written, unacknowledged message from the coordinator", stored)
		}
		if n := linesCarrying(t, agent, request.Text); n != 1 {
			t.Fatalf("the worker read the message on %d lines, want exactly one", n)
		}

		before := historyOf(t, w.home, task)
		id := strconv.FormatInt(stored[0].ID, 10)
		answer, code := callAs(t, w, ipc.MethodTaskEvent,
			machine.TaskEventRequest{Kind: store.MessageAcknowledgedEventKind, Detail: id})
		if code != "" {
			t.Fatalf("the worker's acknowledgement was refused with %q", code)
		}
		var appended machine.Appended
		if err := json.Unmarshal(answer, &appended); err != nil {
			t.Fatalf("decoding the acknowledgement's answer %s: %v", answer, err)
		}
		history := historyOf(t, w.home, task)
		if len(history) != len(before)+1 || history[len(history)-1] != appended.Event ||
			appended.Event.Kind != store.MessageAcknowledgedEventKind || appended.Event.Detail != id {
			t.Fatalf("the history is %+v and the answer %+v, want one event more, the acknowledgement naming message %s",
				history, appended, id)
		}
		acked := messagesOf(t, w)[0]
		if !acked.Acknowledged() || acked.AcknowledgedEvent.Or(0) != appended.Event.Sequence {
			t.Fatalf("the record reads %+v, want it acknowledged by event %d", acked, appended.Event.Sequence)
		}

		for _, refused := range []struct {
			name   string
			detail string
		}{
			{"the same message again", id},
			{"a message that does not exist", "999"},
			{"a detail that names no message", "the parser one"},
		} {
			if _, code := callAs(t, w, ipc.MethodTaskEvent,
				machine.TaskEventRequest{Kind: store.MessageAcknowledgedEventKind, Detail: refused.detail}); code != ipc.CodeInvalidRequest {
				t.Errorf("acknowledging %s answered %q, want %q", refused.name, code, ipc.CodeInvalidRequest)
			}
		}
		var outside machine.Appended
		if err := running.client.Call(t.Context(), ipc.MethodTaskEvent,
			machine.TaskEventRequest{Kind: store.MessageAcknowledgedEventKind, Detail: id}, &outside); !errors.Is(err, ipc.ErrNotWorker) {
			t.Fatalf("an acknowledgement from a peer in no worker group = %v, want ErrNotWorker", err)
		}
		if after := historyOf(t, w.home, task); len(after) != len(history) {
			t.Fatalf("refused acknowledgements grew the history to %d events from %d", len(after), len(history))
		}
	})
}

// PRD principle P6 at a stop: task.stop is the coordinator's and refused
// from the worker with the worker still running. From this process
// it ends the worker, leaves the copy as the worker left it, and answers
// only once the probe reads the terminal dead, with a hold keyed by the
// stopped attempt; the task then reads Held from the hold tier over its
// dead terminal, so the coordinator's own task.relaunch is refused and adds
// no attempt. A stop asked again finds the same hold. A message to the
// stopped worker is recorded refused and nothing resends it.
//
// The hold's retry, with a note, relaunches through the one anchored
// relaunch: attempt 2 supersedes the stopped session, the new worker reads
// the note and the text of every message the stopped attempt never
// acknowledged, and a second answer finds no hold. The relaunch counts
// toward relaunch_attempts, here one: stopping attempt 2 and answering
// retry begins nothing, registers the bound's hold for attempt 2 and says
// so, and the task reads Held on that hold with its attempts still two.
func TestAStopIsProvenDeadThenHeldAndItsRetryIsTheAnchoredRelaunch(t *testing.T) {
	principles.Cite(t, principles.P6)
	service.RequireSweepClearsHere(t, "a relaunch after a stop in the stopped worker's copy")
	streamWorker(t, "task/stop", steerConfig, func(w launched, agent *standin.Agent) {
		running, task := w.running, w.task
		request := machine.TaskRequest{Task: task}

		if _, code := callAs(t, w, ipc.MethodTaskStop, request); code != ipc.CodeWorker {
			t.Fatalf("task.stop from the worker answered %q, want %q", code, ipc.CodeWorker)
		}
		if got := listedTask(t, running.client, task); !readsRunning(got) {
			t.Fatalf("after the refused stop the task reads %+v, want still running", got)
		}

		var unread machine.TaskMessaged
		if err := running.client.Call(t.Context(), ipc.MethodTaskMessage,
			machine.TaskMessageRequest{Task: task, Text: "stay on scope and keep the tests"}, &unread); err != nil {
			t.Fatalf("messaging the worker: %v", err)
		}
		waitCapture(t, w.provider, w.session, "heard: stay on scope")
		copyPath := resolvedCopy(t, w.home, task)
		work := filepath.Join(copyPath, "half-done.txt")
		if err := os.WriteFile(work, []byte("the worker's unfinished change\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		stoppedRef := w.session.Reference().String()
		var stopped machine.TaskStopped
		if err := running.client.Call(t.Context(), ipc.MethodTaskStop, request, &stopped); err != nil {
			t.Fatalf("task.stop from outside every worker group: %v", err)
		}
		if stopped.Task != task || stopped.Attempt != 1 || stopped.Session != stoppedRef || !stopped.Hold.Open() ||
			stopped.Hold.TaskID.Or("") != task {
			t.Fatalf("task.stop answered %+v, want attempt 1's session %s stopped with an open hold naming the task", stopped, stoppedRef)
		}
		if obs := w.provider.Probe(t.Context(), stoppedRef); obs.Liveness != taskstate.Dead {
			t.Fatalf("the stopped session probes %+v, want dead", obs)
		}
		if got, err := os.ReadFile(work); err != nil || string(got) != "the worker's unfinished change\n" {
			t.Fatalf("the stop touched the copy: %q (%v)", got, err)
		}
		held := listedTask(t, running.client, task)
		if held.State != taskstate.Held || held.Source != taskstate.SourceHold || held.Basis.Hold != stopped.Hold.Key ||
			held.Basis.Process != taskstate.Dead {
			t.Fatalf("the stopped task reads %+v, want Held from the stop's hold over a dead terminal", held)
		}
		var again machine.TaskStopped
		if err := running.client.Call(t.Context(), ipc.MethodTaskStop, request, &again); err != nil || again.Hold.Key != stopped.Hold.Key {
			t.Fatalf("a second stop answered %+v (%v), want the same hold %s", again, err, stopped.Hold.Key)
		}
		var relaunched machine.TaskRelaunched
		if err := running.client.Call(t.Context(), ipc.MethodTaskRelaunch, request, &relaunched); !errors.Is(err, ipc.ErrRefused) {
			t.Fatalf("relaunching a stopped task = %v, want ErrRefused: a stop is undone only by its hold's answer", err)
		}
		if attempts := attemptsOf(t, w.home, task); len(attempts) != 1 {
			t.Fatalf("after the refused relaunch the task has attempts %+v, want one", attempts)
		}

		var late machine.TaskMessaged
		err := running.client.Call(t.Context(), ipc.MethodTaskMessage,
			machine.TaskMessageRequest{Task: task, Text: "one more thing for the parser"}, &late)
		if !errors.Is(err, ipc.ErrRefused) {
			t.Fatalf("messaging a stopped worker = %v, want ErrRefused", err)
		}
		messages := messagesOf(t, w)
		if len(messages) != 2 || messages[1].Delivery() != store.DeliveryRefused || messages[1].Refusal.Or("") == "" {
			t.Fatalf("the records are %+v, want the second message recorded refused with its reason", messages)
		}

		var badNote machine.TaskAnswered
		if err := running.client.Call(t.Context(), ipc.MethodTaskRespond,
			machine.TaskRespondRequest{Task: task, Answer: machine.AnswerStop, Note: "a note on a stop"}, &badNote); !errors.Is(err, ipc.ErrInvalidRequest) {
			t.Fatalf("a note with a stop answer = %v, want ErrInvalidRequest", err)
		}
		var answered machine.TaskAnswered
		if err := running.client.Call(t.Context(), ipc.MethodTaskRespond,
			machine.TaskRespondRequest{Task: task, Answer: machine.AnswerRetry, Note: "narrow it to the parser"}, &answered); err != nil {
			t.Fatalf("answering the stop's hold retry: %v", err)
		}
		if answered.Effect != machine.AnswerRelaunched || answered.Relaunch == nil ||
			answered.Relaunch.Attempt != 2 || answered.Relaunch.Superseded != stoppedRef || answered.Hold.Key != stopped.Hold.Key {
			t.Fatalf("the retry answered %+v, want attempt 2 relaunched over %s on hold %s", answered, stoppedRef, stopped.Hold.Key)
		}
		if kinds := kindsOf(t, w.home, task); len(kinds) < 2 ||
			kinds[len(kinds)-2] != "relaunched" || kinds[len(kinds)-1] != store.DispatchedEventKind {
			t.Fatalf("the history is %v, want it to end relaunched then dispatched", kinds)
		}
		provider, relaunchedSession := waitRelaunched(t, running, w.home, task, stoppedRef)
		waitCapture(t, provider, relaunchedSession, "the stop note was read")
		// Each text is counted on the lines the workers read: the message
		// written to the stopped worker once there and once in the relaunch,
		// the refused one and the note only in the relaunch.
		for text, want := range map[string]int{
			"stay on scope and keep the tests": 2,
			"one more thing for the parser":    1,
			"narrow it to the parser":          1,
		} {
			if n := linesCarrying(t, agent, text); n != want {
				t.Errorf("%q reached the workers on %d lines, want %d", text, n, want)
			}
		}
		if got, err := os.ReadFile(work); err != nil || string(got) != "the worker's unfinished change\n" {
			t.Fatalf("the relaunch did not keep the copy: %q (%v)", got, err)
		}
		var second machine.TaskAnswered
		if err := running.client.Call(t.Context(), ipc.MethodTaskRespond,
			machine.TaskRespondRequest{Task: task, Answer: machine.AnswerRetry}, &second); !errors.Is(err, ipc.ErrRefused) {
			t.Fatalf("a second answer = %v, want ErrRefused with no hold open", err)
		}

		// The relaunch counted: the bound is one, so a stop of attempt 2
		// answered retry is past it.
		var stoppedAgain machine.TaskStopped
		if err := running.client.Call(t.Context(), ipc.MethodTaskStop, request, &stoppedAgain); err != nil || stoppedAgain.Attempt != 2 {
			t.Fatalf("stopping attempt 2 answered %+v (%v)", stoppedAgain, err)
		}
		var atBound machine.TaskAnswered
		if err := running.client.Call(t.Context(), ipc.MethodTaskRespond,
			machine.TaskRespondRequest{Task: task, Answer: machine.AnswerRetry}, &atBound); err != nil {
			t.Fatalf("answering attempt 2's stop retry: %v", err)
		}
		if atBound.Effect != machine.AnswerRetryAtBound || atBound.Relaunch != nil || !strings.Contains(atBound.NotRelaunched, "relaunch-bound/2") {
			t.Fatalf("the retry past the bound answered %+v, want the bound's hold for attempt 2 named and nothing begun", atBound)
		}
		if attempts := attemptsOf(t, w.home, task); len(attempts) != 2 {
			t.Fatalf("past the bound the task has attempts %+v, want two", attempts)
		}
		if got := listedTask(t, running.client, task); got.State != taskstate.Held || !strings.Contains(got.Basis.Hold, "relaunch-bound/2") {
			t.Fatalf("past the bound the task reads %+v, want Held on the bound's hold", got)
		}
	})
}

// waitRelaunched waits for the task's second attempt to record a session
// other than the superseded one, and returns the provider and the session
// the service holds for it.
func waitRelaunched(t *testing.T, running serviceUnderTest, h *home.Home, task, superseded string) (session.Provider, *session.Session) {
	t.Helper()
	deadline := time.Now().Add(launchWait)
	for {
		attempts := attemptsOf(t, h, task)
		if len(attempts) != 2 {
			t.Fatalf("after the relaunch the task has attempts %+v, want two", attempts)
		}
		if ref, ok := attempts[1].SessionRef.Get(); ok {
			if ref == superseded {
				t.Fatalf("the relaunched attempt records the superseded session %s", ref)
			}
			provider, held, ok := running.service.SessionOf(task)
			if !ok || held.Reference().String() != ref {
				t.Fatalf("the service holds %v (%v) for the task, want the relaunched attempt's session %s", held, ok, ref)
			}
			return provider, held
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the relaunched attempt never recorded a session: %+v", attempts)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// PRD section 6's "interrupt is not offered": task.interrupt is refused from
// the worker with the worker code, as every coordinator method is,
// and from this process with the records' refusal naming the capability no
// adapter declares, while the same adapter's declaration of worker
// sessions, which the stream worker runs on, shows the refusal is read off
// a declaration and not off an adapter that declares nothing. The worker
// runs on after both: nothing was interrupted.
func TestAnInterruptIsRefusedAsUndeclared(t *testing.T) {
	streamWorker(t, "task/interrupt", steerConfig, func(w launched, agent *standin.Agent) {
		running, task := w.running, w.task
		request := machine.TaskRequest{Task: task}
		if _, code := callAs(t, w, ipc.MethodTaskInterrupt, request); code != ipc.CodeWorker {
			t.Fatalf("task.interrupt from the worker answered %q, want %q", code, ipc.CodeWorker)
		}
		var out struct{}
		err := running.client.Call(t.Context(), ipc.MethodTaskInterrupt, request, &out)
		if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), "interrupt") || !strings.Contains(err.Error(), "has not declared") {
			t.Fatalf("task.interrupt = %v, want ErrRefused naming the undeclared interrupt capability", err)
		}
		declared := agent.Runner().Capabilities()
		if !declared.Has(agents.CapabilityWorkerSessions) || declared.Has(agents.Capability("interrupt")) {
			t.Fatalf("the worker's adapter declares %s, want worker sessions and no interrupt", declared)
		}
		if got := listedTask(t, running.client, task); !readsRunning(got) {
			t.Fatalf("after the refused interrupt the task reads %+v, want still running", got)
		}
	})
}
