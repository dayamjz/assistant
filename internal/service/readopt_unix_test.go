//go:build unix

package service_test

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/store"
)

// defaultProviderGlobal is the home's configuration with no session
// provider named, so the default decides.
const defaultProviderGlobal = `{"supervision_beacon_interval": "1s"}`

// A restart of the service with three live workers under the default
// provider, which hosts each in tmux where tmux is installed, relaunches
// none of them: the restart verb releases each worker rather than ending it,
// its tmux server goes on running, and the next service re-adopts it from the
// attempt's recorded session once re-adoption verifies the pane and the server
// on its recorded socket (PRD section 11's step 5, session.Tmux.Readopt), a
// check the liveness probe does not make. Each task then still has its one attempt,
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
	if err := os.WriteFile(h.ConfigFile(), []byte(defaultProviderGlobal+"\n"), 0o600); err != nil {
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
	o.PipeByDefault = false
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
	if !answer.Accepted || !answer.Restarting {
		t.Fatalf("the restart was answered %+v, want it accepted as a restart", answer)
	}
	awaitRestarting(t, running.service)
	closeFirst()
	homeIsFree(t, h)
	for id, ref := range first {
		if presence, err := (agents.Identity{PID: ref.PID, Start: ref.Start}).Presence(); err != nil || presence != agents.PresencePresent {
			t.Fatalf("task %s's worker reads %q (%v) after the restart closed its service, want it still running", id, presence, err)
		}
	}

	o, _ = workerOptions(t, h)
	o.PipeByDefault = false
	withServiceOptions(t, o, func(running serviceUnderTest) {
		for _, id := range ids {
			provider, held, ok := running.service.SessionOf(id)
			if !ok {
				t.Fatalf("the restarted service holds no session for task %s, so its worker was not re-adopted", id)
			}
			if _, tmux := provider.(*session.Tmux); !tmux {
				t.Fatalf("task %s's worker is held through %T, want the tmux provider the default chose", id, provider)
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

// A restart whose workers' tmux servers have stopped answering is held back
// by them once, not once per worker: the re-adoptions are bounded, and the
// service does not serve until reconciliation is done, so asking them one
// after another would cost a bound per worker before the first readiness
// answer, past the wait a restart gives its successor. Four workers outlive
// a hard stop and their servers are then stopped, so each pane is present
// and its server never answers. The next service must open within three of
// the one bound, where one after another takes at least four; it holds none
// of the workers, since none verifies, and relaunches none, since each is
// present. The positive control is that each was asked: opening took at
// least the one bound, and the log says of every worker that its server
// did not answer, so a restart that skipped re-adoption does not pass here.
//
// It does not run in parallel: it bounds how long Open takes from above,
// and the package's other tests beside it would be measuring their load.
func TestARestartBehindTmuxServersThatDoNotAnswerWaitsOnThemOnce(t *testing.T) {
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
		recordDispatchedTask(t, h, repository, store.TaskDelivery, "task/unanswered-one"),
		recordDispatchedTask(t, h, repository, store.TaskDelivery, "task/unanswered-two"),
		recordDispatchedTask(t, h, repository, store.TaskDelivery, "task/unanswered-three"),
		recordDispatchedTask(t, h, repository, store.TaskDelivery, "task/unanswered-four"),
	}
	var servers []int
	o, _ := workerOptions(t, h)
	withServiceOptions(t, o, func(running serviceUnderTest) {
		for _, id := range ids {
			provider, launched := launchedSession(t, running.service, id)
			waitCapture(t, provider, launched, "worker ready")
			socket := socketOf(t, provider, launched)
			t.Cleanup(func() { _ = exec.Command(tmux, "-S", socket, "kill-server").Run() })
			server := launched.Host()
			if server.PID <= 0 {
				t.Fatalf("task %s's session names no server process: %+v", id, server)
			}
			servers = append(servers, server.PID)
		}
		running.service.AbandonSessionsForTest(t)
	})
	homeIsFree(t, h)
	for _, pid := range servers {
		if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
			t.Fatalf("stopping tmux server %d: %v", pid, err)
		}
		// Registered after the server's kill-server, so it runs first and
		// the server can answer that.
		t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGCONT) })
	}

	o, _ = workerOptions(t, h)
	began := time.Now()
	running, closeSecond := openService(t, o)
	took := time.Since(began)
	defer closeSecond()
	t.Logf("the service behind %d tmux servers that do not answer opened in %v", len(servers), took)
	if limit := time.Duration(len(servers)-1) * service.ReadoptBound; took >= limit {
		t.Fatalf("the service behind %d tmux servers that do not answer took %v to open, want under %v: the re-adoptions waited one after another",
			len(servers), took, limit)
	}
	if took < service.ReadoptBound {
		t.Fatalf("the service opened in %v, under the %v a server that does not answer costs, so it asked none", took, service.ReadoptBound)
	}
	log := serviceLog(t, h)
	for _, id := range ids {
		asked := false
		for _, line := range strings.Split(log, "\n") {
			if strings.Contains(line, "task "+id+"'s worker in terminal session") &&
				strings.Contains(line, "was not held again") && strings.Contains(line, "did not answer with its pane") {
				asked = true
				break
			}
		}
		if !asked {
			t.Fatalf("the service log does not say task %s's server was asked and did not answer:\n%s", id, log)
		}
	}
	for _, id := range ids {
		if _, _, held := running.service.SessionOf(id); held {
			t.Fatalf("task %s's worker is held although its server never answered", id)
		}
		if attempts := attemptsOf(t, h, id); len(attempts) != 1 {
			t.Fatalf("task %s has attempts %+v, want its one launch and no relaunch of a worker still present", id, attempts)
		}
	}
}

// awaitRestarting waits for the service to take the restart it answered.
// The restart verb stops the service only once its answer is written, so a
// caller that closes the service as soon as it reads the answer can close
// it first, as a plain stop, which ends every worker; the service's own
// loop closes only after Serve returns, which the stop is what ends.
func awaitRestarting(t *testing.T, s *service.Service) {
	t.Helper()
	deadline := time.Now().Add(launchWait)
	for !s.Restarting() {
		if !time.Now().Before(deadline) {
			t.Fatalf("the service answered the restart and had not taken it %v later", launchWait)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
