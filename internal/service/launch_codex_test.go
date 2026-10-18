package service_test

import (
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// A Codex pipe worker is what the service starts for a delivery task and
// for an investigation: the session comes up, a later message is answered,
// the capture is the worker's reply, and closing the service ends it.
func TestACodexWorkerLaunchesForDeliveryAndInvestigation(t *testing.T) {
	for _, shape := range []struct {
		name, branch string
		shape        store.TaskShape
	}{
		{"delivery", "task/one", store.TaskDelivery},
		{"investigation", "", store.TaskInvestigation},
	} {
		t.Run(shape.name, func(t *testing.T) {
			h := newHome(t)
			subject := newSubject(t)
			quietMaintenance(t, subject)
			repository := recordRepository(t, h, subject)
			id := recordDispatchedTask(t, h, repository, shape.shape, shape.branch)
			agent := standin.NewCodex(t, standin.Script{Worker: &standin.WorkerScript{
				Exchanges: []standin.Exchange{
					{Contains: "git rev-parse --show-toplevel", Reply: "the second check was read"},
					{Contains: "status", Reply: "still working on it"},
				},
			}}, agents.WithCodexTerminationGrace(2*time.Second))
			o := options(t, h)
			o.Catalog = agents.NewCatalog(fixedFactory{runner: agent.Runner()}, cursorFactory{runner: agent.Runner()})
			var provider session.Provider
			var ref session.Reference
			withServiceOptions(t, o, func(running serviceUnderTest) {
				var launched *session.Session
				provider, launched = launchedSession(t, running.service, id)
				ref = launched.Reference()
				waitCapture(t, provider, launched, "the second check was read")
				if got := taskState(t, running.client, id); !readsRunning(got) {
					t.Fatalf("after the launch the task resolves %+v, want Running from the process tier", got)
				}
				if err := provider.Send(t.Context(), launched, "status?"); err != nil {
					t.Fatalf("Send: %v", err)
				}
				got := waitCapture(t, provider, launched, "still working on it")
				if strings.Contains(got.Text, "status?") {
					t.Fatalf("the capture carries the line sent:\n%s", got.Text)
				}
				call := workerCall(t, agent)
				if call.Dir != resolvedCopy(t, h, id) {
					t.Errorf("the worker was started in %s, want the proven copy %s", call.Dir, resolvedCopy(t, h, id))
				}
				if !strings.Contains(call.Lines[0], "git rev-parse --show-toplevel") {
					t.Fatalf("the worker's first turn did not carry the instructions:\n%s", call.Lines[0])
				}
			})
			if ref.Task != id {
				t.Fatalf("the session's reference is %+v, want task %s", ref, id)
			}
			if probe := provider.Probe(t.Context(), ref.String()); probe.Liveness != taskstate.Dead {
				t.Fatalf("after the service closed the session probes %+v, want Dead", probe)
			}
		})
	}
}
