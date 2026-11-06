//go:build unix

package service_test

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
)

// longTurn is the message the Claude Code terminal worker below answers
// with a turn far longer than any test waits, so only an interrupt ends it.
const longTurn = "work long on the task"

// claudeTerminalWorker records and dispatches a delivery task, serves a
// service whose operator chose the tmux provider over the Claude Code
// adapter driving the stand-in's interactive mode, which models the CLI's
// screen, and hands the body the launched worker and the stand-in once the
// worker answered its instructions.
func claudeTerminalWorker(t *testing.T, body func(w launched, agent *standin.Agent)) {
	t.Helper()
	standin.Tmux(t)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	quietMaintenance(t, subject)
	repository := recordRepository(t, h, subject)
	quietMaintenance(t, gateRepositoryOf(t, h, subject))
	if err := os.WriteFile(h.ConfigFile(), []byte(tmuxGlobal+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := recordDispatchedTask(t, h, repository, store.TaskDelivery, "task/interrupt")
	agent := standin.New(t, standin.Script{
		Steps: []standin.Step{{
			Match: standin.MatchReview(),
			Times: standin.Always,
			Reply: standin.Reviewed("the change reads cleanly"),
		}},
		Worker: &standin.WorkerScript{
			Exchanges: []standin.Exchange{
				{Contains: longTurn, Reply: "the long work finished", Turn: time.Hour, Progress: "the long work begins"},
				{Contains: "git rev-parse --show-toplevel", Reply: "the instructions were read"},
				{Contains: "status", Reply: "heard: status"},
			},
		},
	}, agents.WithTerminationGrace(2*time.Second),
		agents.WithBaseEnvironment(append(os.Environ(), agents.ClaudeKeyVariable+"=standin-placeholder-key-0123456789abcdefghij")))
	o := options(t, h)
	o.Catalog = agents.NewCatalog(fixedFactory{runner: agent.Runner()}, cursorFactory{runner: agent.Runner()})
	withServiceOptions(t, o, func(running serviceUnderTest) {
		provider, session := launchedSession(t, running.service, id)
		waitCapture(t, provider, session, "the instructions were read")
		body(launched{running: running, home: h, repository: repository, task: id, group: session.Reference().PID,
			provider: provider, session: session}, agent)
	})
}

// taskInterrupted is task.interrupt's answer as the wire carries it, read
// here by field name so this test states what the answer must say.
type taskInterrupted struct {
	Task        string `json:"task"`
	Attempt     int64  `json:"attempt"`
	Session     string `json:"session"`
	Interrupted bool   `json:"interrupted"`
}

// interruptCall is task.interrupt from this process, the coordinator.
func interruptCall(t *testing.T, w launched) (taskInterrupted, error) {
	t.Helper()
	var raw json.RawMessage
	err := w.running.client.Call(t.Context(), ipc.MethodTaskInterrupt, machine.TaskRequest{Task: w.task}, &raw)
	var got taskInterrupted
	if err == nil {
		if derr := json.Unmarshal(raw, &got); derr != nil {
			t.Fatalf("task.interrupt answered %s, which does not decode: %v", raw, derr)
		}
	}
	return got, err
}

// awaitCall waits until the stand-in's one interactive worker recorded a
// call satisfying ok, and returns it.
func awaitCall(t *testing.T, agent *standin.Agent, ok func(standin.Call) bool) standin.Call {
	t.Helper()
	deadline := time.Now().Add(launchWait)
	for {
		for _, call := range agent.Calls() {
			if call.Interactive && ok(call) {
				return call
			}
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the interactive worker never recorded what was waited for; it recorded %+v", agent.Calls())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// PRD section 6's interrupt, end to end through the service under the tmux
// provider with the Claude Code adapter: a worker running a long turn is
// interrupted by the coordinator's task.interrupt, which answers that a
// turn was ended; the worker is still alive, its task still Running, the
// turn was dropped without a reply, and the next message is answered. An
// interrupt with no turn running answers so and presses nothing, and a
// task.interrupt from the worker's own terminal server is refused with the
// worker code.
func TestAnInterruptEndsTheTurnAndKeepsTheWorker(t *testing.T) {
	t.Parallel()
	claudeTerminalWorker(t, func(w launched, agent *standin.Agent) {
		var sent machine.TaskMessaged
		if err := w.running.client.Call(t.Context(), ipc.MethodTaskMessage,
			machine.TaskMessageRequest{Task: w.task, Text: longTurn}, &sent); err != nil {
			t.Fatalf("task.message: %v", err)
		}
		awaitCall(t, agent, func(c standin.Call) bool { return slices.ContainsFunc(c.Lines, containing(longTurn)) })
		waitCapture(t, w.provider, w.session, "esc to interrupt")

		// From a window of the worker's own server, which the service places
		// as the worker, so the turn running in the pane is not waited on.
		if _, code := callFromWindow(t, w, ipc.MethodTaskInterrupt, machine.TaskRequest{Task: w.task}); code != ipc.CodeWorker {
			t.Fatalf("task.interrupt from the worker's server answered %q, want %q", code, ipc.CodeWorker)
		}
		got, err := interruptCall(t, w)
		if err != nil {
			t.Fatalf("task.interrupt of a worker running a long turn: %v", err)
		}
		if got.Task != w.task || got.Session != w.session.Reference().String() || got.Attempt < 1 || !got.Interrupted {
			t.Fatalf("task.interrupt answered %+v, want task %s, its session %s, an attempt, and a turn interrupted",
				got, w.task, w.session.Reference())
		}
		call := awaitCall(t, agent, func(c standin.Call) bool { return len(c.Interrupted) > 0 })
		if !slices.ContainsFunc(call.Interrupted, containing(longTurn)) || call.IdleEscapes != 0 {
			t.Fatalf("the worker recorded %q interrupted and %d idle Escape keys, want the long turn and none",
				call.Interrupted, call.IdleEscapes)
		}
		if got := observed(t, listedTask, w.running.client, w.task); !readsRunning(got) {
			t.Fatalf("after the interrupt the task reads %+v, want still Running", got)
		}

		if err := w.running.client.Call(t.Context(), ipc.MethodTaskMessage,
			machine.TaskMessageRequest{Task: w.task, Text: "status, please"}, &sent); err != nil {
			t.Fatalf("task.message after the interrupt: %v", err)
		}
		if sent.Delivery != store.DeliveryWritten {
			t.Fatalf("the message after the interrupt was answered %+v, want written", sent)
		}
		screen := waitCapture(t, w.provider, w.session, "heard: status")
		if strings.Contains(screen.Text, "the long work finished") {
			t.Fatalf("the interrupted turn was answered anyway:\n%s", screen.Text)
		}

		idle, err := interruptCall(t, w)
		if err != nil || idle.Interrupted {
			t.Fatalf("task.interrupt with no turn running answered %+v (%v), want no turn interrupted", idle, err)
		}
		if call := awaitCall(t, agent, answered(3)); call.IdleEscapes != 0 {
			t.Fatalf("an interrupt with no turn running pressed Escape %d times", call.IdleEscapes)
		}
	})
}

// containing is a predicate on a line that carries part.
func containing(part string) func(string) bool {
	return func(line string) bool { return strings.Contains(line, part) }
}

// answered is a predicate on a call that has answered at least n messages.
func answered(n int) func(standin.Call) bool {
	return func(c standin.Call) bool { return len(c.Lines) >= n }
}

// Under the tmux provider an adapter that declares no terminal interrupt,
// the scripted one, whose typing reads no screen, is refused with the
// capability named and nothing typed, and its worker runs on.
func TestAnInterruptOfAnAdapterThatDeclaresNoneIsRefused(t *testing.T) {
	t.Parallel()
	launchedTmuxWorker(t, "task/no-interrupt", func(w launched) {
		_, err := interruptCall(t, w)
		if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), string(agents.CapabilityTerminalInterrupt)) ||
			!strings.Contains(err.Error(), "has not declared") {
			t.Fatalf("task.interrupt = %v, want ErrRefused naming the undeclared %s", err, agents.CapabilityTerminalInterrupt)
		}
		if got := observed(t, listedTask, w.running.client, w.task); !readsRunning(got) {
			t.Fatalf("after the refused interrupt the task reads %+v, want still running", got)
		}
	})
}
