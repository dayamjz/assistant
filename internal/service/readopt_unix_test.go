//go:build unix

package service_test

import (
	"os"
	"os/exec"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/store"
)

// A restart of the service with three live tmux-hosted workers relaunches
// none of them: the restart verb releases each worker rather than ending it,
// its tmux server goes on running, and the next service re-adopts it from the
// attempt's recorded session once the liveness probe verifies the pane and
// the server on its recorded socket. Each task then still has its one attempt,
// so there are zero relaunch records, the same process answers under the same
// reference, and a message sent through the new service reaches it and is
// answered. That is #763's acceptance, and P15's owner for a terminal-hosted
// worker: its task's record and its own server, not the service process. The
// second service is closed with a plain stop, which ends what it adopted.
func TestARestartReadoptsThreeLiveTmuxWorkersAndRelaunchesNone(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	tmux := standin.Tmux(t)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	if err := os.WriteFile(h.ConfigFile(), []byte(tmuxGlobal+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	subject := newSubject(t)
	quietMaintenance(t, subject)
	repository := recordRepository(t, h, subject)
	ids := []string{
		recordDispatchedTask(t, h, repository, store.TaskDelivery, "task/readopt-one"),
		recordDispatchedTask(t, h, repository, store.TaskDelivery, "task/readopt-two"),
		recordDispatchedTask(t, h, repository, store.TaskDelivery, "task/readopt-three"),
	}

	first := map[string]session.Reference{}
	o, _ := workerOptions(t, h)
	running, closeFirst := openService(t, o)
	for _, id := range ids {
		provider, launched := launchedSession(t, running.service, id)
		waitCapture(t, provider, launched, "worker ready")
		first[id] = launched.Reference()
		// Whatever this test leaves standing is ended here, by the socket
		// its own service gave the worker, so a failure below leaks nothing.
		socket := socketOf(t, provider, launched)
		t.Cleanup(func() { _ = exec.Command(tmux, "-S", socket, "kill-server").Run() })
	}
	var answer machine.Lifecycle
	if err := running.client.Call(t.Context(), ipc.MethodServiceRestart, machine.LifecycleRequest{}, &answer); err != nil {
		t.Fatalf("asking the service to restart: %v", err)
	}
	closeFirst()
	homeIsFree(t, h)
	for id, ref := range first {
		if presence, err := (agents.Identity{PID: ref.PID, Start: ref.Start}).Presence(); err != nil || presence != agents.PresencePresent {
			t.Fatalf("task %s's worker reads %q (%v) after the restart closed its service, want it still running", id, presence, err)
		}
	}

	o, _ = workerOptions(t, h)
	withServiceOptions(t, o, func(running serviceUnderTest) {
		for _, id := range ids {
			provider, held, ok := running.service.SessionOf(id)
			if !ok {
				t.Fatalf("the restarted service holds no session for task %s, so its worker was not re-adopted", id)
			}
			if _, tmux := provider.(*session.Tmux); !tmux {
				t.Fatalf("task %s's worker is held through %T, want the tmux provider", id, provider)
			}
			if held.Reference() != first[id] {
				t.Fatalf("task %s runs in session %s after the restart, want the same worker %s", id, held.Reference(), first[id])
			}
			if got := observed(t, taskState, running.client, id); !readsRunning(got) {
				t.Fatalf("task %s resolves %+v after the restart, want Running through its re-adopted session", id, got)
			}
			if err := provider.Send(t.Context(), held, "status"); err != nil {
				t.Fatalf("sending to task %s's re-adopted worker: %v", id, err)
			}
			waitCapture(t, provider, held, "still working on it")
		}
	})
	for _, id := range ids {
		if attempts := attemptsOf(t, h, id); len(attempts) != 1 {
			t.Fatalf("task %s has attempts %+v after the restart, want its one launch and zero relaunch records", id, attempts)
		}
	}
}

// A worker that outlives a service stopped hard, with no restart asked for,
// is held again the same way: the service that died let nothing go, and its
// attempt's record still names the worker and its server, so the next
// service verifies and holds it, the task reads Running through it, and no
// relaunch is recorded. A plain stop of that service then ends it, as P15's
// stop half ends every worker a stop that is not a restart finds held.
func TestASurvivorOfAHardStopIsHeldAgainAndAPlainStopEndsIt(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	h, id := tmuxHome(t, "task/readopt-survivor")
	o, _ := workerOptions(t, h)
	var first session.Reference
	withServiceOptions(t, o, func(running serviceUnderTest) {
		provider, launched := launchedSession(t, running.service, id)
		waitCapture(t, provider, launched, "worker ready")
		first = launched.Reference()
		socket := socketOf(t, provider, launched)
		t.Cleanup(func() { _ = exec.Command(standin.Tmux(t), "-S", socket, "kill-server").Run() })
		running.service.AbandonSessionsForTest(t)
	})
	homeIsFree(t, h)

	o, _ = workerOptions(t, h)
	withServiceOptions(t, o, func(running serviceUnderTest) {
		_, held := launchedSession(t, running.service, id)
		if held.Reference() != first {
			t.Fatalf("the survivor is held as %s, want %s", held.Reference(), first)
		}
		if got := observed(t, taskState, running.client, id); !readsRunning(got) {
			t.Fatalf("the survivor's task resolves %+v, want Running through the session held again", got)
		}
	})
	if attempts := attemptsOf(t, h, id); len(attempts) != 1 {
		t.Fatalf("the task has attempts %+v, want its one launch and no relaunch", attempts)
	}
	if presence, err := (agents.Identity{PID: first.PID, Start: first.Start}).Presence(); err != nil || presence == agents.PresencePresent {
		t.Fatalf("the worker reads %q (%v) after a plain stop of the service holding it, want it ended", presence, err)
	}
}
