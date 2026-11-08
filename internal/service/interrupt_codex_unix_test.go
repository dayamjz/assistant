//go:build unix

package service_test

import (
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
)

// PRD section 6's interrupt end to end through the service under the pipe
// provider with the Codex adapter: a worker running a long turn is
// interrupted by task.interrupt, stays Running and Live, drops that turn,
// and answers the next message. An idle interrupt presses nothing. A call
// from the worker itself is covered by TestAnInterruptOnAPipeIsRefused
// (Claude stream): Codex's app-server declines the test's shell command,
// so that half is not re-asked here.
func TestACodexPipeInterruptEndsTheTurnAndKeepsTheWorker(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)
	quietMaintenance(t, subject)
	repository := recordRepository(t, h, subject)
	id := recordDispatchedTask(t, h, repository, store.TaskDelivery, "task/interrupt-codex")
	const long = "work long on the task"
	agent := standin.NewCodex(t, standin.Script{Worker: &standin.WorkerScript{
		Exchanges: []standin.Exchange{
			{Contains: "git rev-parse --show-toplevel", Reply: "the instructions were read"},
			{Contains: long, Reply: "the long work finished", Turn: time.Hour},
			{Contains: "status", Reply: "heard: status"},
		},
	}}, agents.WithCodexTerminationGrace(2*time.Second))
	o := options(t, h)
	o.Catalog = agents.NewCatalog(fixedFactory{runner: agent.Runner()}, cursorFactory{runner: agent.Runner()})
	withServiceOptions(t, o, func(running serviceUnderTest) {
		provider, session := launchedSession(t, running.service, id)
		waitCapture(t, provider, session, "the instructions were read")
		w := launched{running: running, home: h, repository: repository, task: id,
			group: session.Reference().PID, provider: provider, session: session}
		var sent machine.TaskMessaged
		if err := running.client.Call(t.Context(), ipc.MethodTaskMessage,
			machine.TaskMessageRequest{Task: id, Text: long}, &sent); err != nil {
			t.Fatalf("task.message long turn: %v", err)
		}
		got, err := interruptCall(t, w)
		if err != nil || !got.Interrupted {
			t.Fatalf("task.interrupt = %+v, %v, want interrupted", got, err)
		}
		if observed := observed(t, listedTask, running.client, id); !readsRunning(observed) {
			t.Fatalf("after interrupt the task reads %+v, want running", observed)
		}
		call := workerCall(t, agent)
		if len(call.Interrupted) != 1 || !strings.Contains(call.Interrupted[0], long) {
			t.Fatalf("stand-in recorded Interrupted=%v, want the long turn once", call.Interrupted)
		}
		if err := running.client.Call(t.Context(), ipc.MethodTaskMessage,
			machine.TaskMessageRequest{Task: id, Text: "status?"}, &sent); err != nil {
			t.Fatalf("task.message after interrupt: %v", err)
		}
		waitCapture(t, provider, session, "heard: status")
		idle, err := interruptCall(t, w)
		if err != nil || idle.Interrupted {
			t.Fatalf("idle interrupt = %+v, %v, want interrupted false", idle, err)
		}
	})
}
