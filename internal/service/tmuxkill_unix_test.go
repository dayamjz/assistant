//go:build unix

package service_test

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// A tmux worker whose server or service dies hard, end to end: what the
// probe reads, what reconciliation does with it, and that the task is told
// about rather than left. #301 (b) asked for both halves. The server killed
// outright takes the pane's process with it, so the terminal reads Dead,
// through the handle while the service that launched it is up and by the
// start-time read after a restart, and the restart relaunches in place. A
// service that dies and leaves its worker running is the other half: the
// next service holds no handle, so the terminal reads Unobserved, nothing
// relaunches beside a worker that may still be writing, and the supervisor
// loop surfaces it as terminal gone with the unobservable case at once,
// then as dead once the worker is gone, and only a restart after that
// relaunches it. "Unobserved forever" is therefore never silent: it is a
// queued wake for a person on every pass window, and the relaunch waits on
// proof of absence.

// serverOf is the identity of the tmux server hosting the launched session,
// read off the worker set the provider produces and checked against the
// pane process's own parent, so the process the test kills is the server
// and not the worker.
func serverOf(t *testing.T, provider session.Provider, launched *session.Session) agents.Identity {
	t.Helper()
	host, ok := provider.(session.Host)
	if !ok {
		t.Fatalf("the provider %T is not a session.Host", provider)
	}
	m, ok := host.Workers().Member(launched.Reference().PID)
	if !ok || m.Host == (agents.Identity{}) {
		t.Fatalf("the worker set answers %+v, %v for the launched worker, want a membership naming its host", m, ok)
	}
	pane, found, err := agents.ReadLineage(launched.Reference().PID)
	if err != nil || !found || pane.Parent != m.Host.PID {
		t.Fatalf("the pane's parent is %d (found %v, %v) and the membership's host is %d", pane.Parent, found, err, m.Host.PID)
	}
	return m.Host
}

// socketOf is the launched session's tmux socket, read off the attach
// command.
func socketOf(t *testing.T, provider session.Provider, launched *session.Session) string {
	t.Helper()
	host := provider.(session.Host)
	attach, err := host.AttachReference(launched.Reference().String())
	if err != nil {
		t.Fatalf("AttachReference: %v", err)
	}
	for i, arg := range attach {
		if arg == "-S" && i+1 < len(attach) {
			return attach[i+1]
		}
	}
	t.Fatalf("the attach command %q names no socket", attach)
	return ""
}

// waitGone polls the task's terminal-gone wakes until one carries the case
// and reference, composed as supervise.go composes a marker, and returns it.
func waitGone(t *testing.T, h *home.Home, task, gone string, ref session.Reference) store.Wake {
	t.Helper()
	prefix := "terminal-gone:" + gone + ":" + ref.String() + ":"
	deadline := time.Now().Add(60 * time.Second)
	for {
		for _, w := range wakesOf(t, h, task, supervisor.KindTerminalGone) {
			if strings.HasPrefix(w.Marker, prefix) {
				return w
			}
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the loop raised no terminal-gone wake with case %q about %s; it raised %+v", gone, ref,
				wakesOf(t, h, task, supervisor.KindTerminalGone))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// tmuxHome is a home whose operator's file chooses tmux, with a dispatched
// delivery task on branch recorded in it.
func tmuxHome(t *testing.T, branch string) (*home.Home, string) {
	t.Helper()
	standin.Tmux(t)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	if err := os.WriteFile(h.ConfigFile(), []byte(tmuxGlobal+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	subject := newSubject(t)
	quietMaintenance(t, subject)
	repository := recordRepository(t, h, subject)
	return h, recordDispatchedTask(t, h, repository, store.TaskDelivery, branch)
}

// The worker's tmux server is killed outright, SIGKILL to the server and not
// to a client or the window, while the service that launched it is up. The
// pane loses its terminal and the agent exits, so the handle's poll reads
// the process gone: the probe answers Dead through the handle, the task
// resolves Unknown from its dead terminal, the loop surfaces terminal gone
// with the dead case, and the socket is removed. After the service closes,
// a provider holding no handle answers the same reference Dead by the
// start-time read, which is what the next service's reconciliation decides
// on: it relaunches in the standing copy, in a server of its own, and
// acknowledges the dead session's wake as the program.
func TestUnderTmuxAServerKilledOutrightReadsDeadAndARestartRelaunches(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P9)
	const unestablished = "a restart under tmux relaunching after its server was killed"
	service.RequireSweepClearsHere(t, unestablished)
	h, id := tmuxHome(t, "task/tmux-killed")

	o, agent := workerOptions(t, h)
	var first session.Reference
	var socket string
	withServiceOptions(t, o, func(running serviceUnderTest) {
		provider, launched := launchedSession(t, running.service, id)
		first = launched.Reference()
		waitCapture(t, provider, launched, "worker ready")
		server := serverOf(t, provider, launched)
		socket = socketOf(t, provider, launched)
		if err := syscall.Kill(server.PID, syscall.SIGKILL); err != nil {
			t.Fatalf("killing the tmux server %d: %v", server.PID, err)
		}
		// The server's parent reaps it in its own time, so its absence is
		// waited for rather than read at once.
		deadline := time.Now().Add(30 * time.Second)
		for {
			presence, err := server.Presence()
			if err == nil && presence != agents.PresencePresent {
				break
			}
			if !time.Now().Before(deadline) {
				t.Fatalf("the tmux server still reads %q (%v) after SIGKILL", presence, err)
			}
			time.Sleep(20 * time.Millisecond)
		}

		obs := waitProbe(t, provider, first.String(), taskstate.Dead)
		if obs.Reason != session.ReasonExited {
			t.Fatalf("the killed server's worker probes %+v, want Dead through the handle, exited", obs)
		}
		waitState(t, running.client, id, "read Unknown from its dead terminal", func(r taskstate.Resolution) bool {
			return r.State == taskstate.Unknown && r.Source == taskstate.SourceProcess && r.Basis.Process == taskstate.Dead
		})
		gone := waitGone(t, h, id, "dead", first)
		if !gone.Queued() {
			t.Fatalf("the dead case's wake is %+v, want it queued for the coordinator", gone)
		}
		deadline = time.Now().Add(30 * time.Second)
		for {
			if _, err := os.Lstat(socket); errors.Is(err, os.ErrNotExist) {
				break
			}
			if !time.Now().Before(deadline) {
				t.Fatalf("the socket %s is still there after the server was killed and the worker read gone", socket)
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
	homeIsFree(t, h)

	// A provider holding no handle: the same reference, by identity.
	if got := session.NewPipe(h, agent.Runner()).Probe(t.Context(), first.String()); got.Liveness != taskstate.Dead || got.Reason != session.ReasonAbsent {
		t.Fatalf("after the service closed the reference probes %+v, want Dead by the start-time read, absent", got)
	}

	o, _ = workerOptions(t, h)
	var second session.Reference
	withServiceOptions(t, o, func(running serviceUnderTest) {
		provider, launched := waitRelaunchedSession(t, running.service, id, unestablished)
		if _, ok := provider.(*session.Tmux); !ok {
			t.Fatalf("the restarted service relaunched through %T, want the tmux provider", provider)
		}
		second = launched.Reference()
		waitCapture(t, provider, launched, "worker ready")
		if got := observed(t, taskState, running.client, id); !readsRunning(got) {
			t.Fatalf("after the relaunch the task resolves %+v, want Running through its new tmux session", got)
		}
	})
	if first == second {
		t.Fatalf("the relaunch reused the first attempt's reference %s", first)
	}
	attempts := attemptsOf(t, h, id)
	if len(attempts) != 2 || attempts[0].SessionRef.Or("") != first.String() || attempts[1].SessionRef.Or("") != second.String() {
		t.Fatalf("the task has attempts %+v, want the launch's then the relaunch's", attempts)
	}
	if gone := waitGone(t, h, id, "dead", first); gone.Queued() || gone.AcknowledgedBy.Or(store.Acknowledger{}) != store.AcknowledgedByProgram() {
		t.Fatalf("the dead session's wake is %+v after the relaunch, want acknowledged by the program", gone)
	}
}

// The service dies and its worker does not: the next service holds no
// handle to the surviving tmux session, so its probe answers Unobserved by
// identity, reconciliation leaves the task alone rather than starting a
// second worker beside one it cannot see, and the loop surfaces it at once
// as terminal gone with the unobservable case, a queued wake for a person
// that no later pass re-reads as absorbed. The survivor is produced by
// forgetting the first service's sessions before it closes, which is what
// SIGKILL of the service leaves, and it stays Unobserved for as long as it
// stands: there is no re-adoption, because the new service cannot capture,
// type into or attach to a terminal it did not create. Once a person ends
// the survivor's server the pane's process goes with it, the same service
// reads the reference Dead and surfaces the dead case, and nothing is
// relaunched until the next start, which then does.
func TestUnderTmuxASurvivorOfTheServiceReadsUnobservedAndIsSurfacedNotRelaunched(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P9)
	const unestablished = "a restart under tmux relaunching once a survivor has gone"
	service.RequireSweepClearsHere(t, unestablished)
	h, id := tmuxHome(t, "task/tmux-survivor")

	o, agent := workerOptions(t, h)
	var first session.Reference
	var socket string
	withServiceOptions(t, o, func(running serviceUnderTest) {
		provider, launched := launchedSession(t, running.service, id)
		first = launched.Reference()
		waitCapture(t, provider, launched, "worker ready")
		socket = socketOf(t, provider, launched)
		running.service.AbandonSessionsForTest(t)
	})
	homeIsFree(t, h)
	t.Cleanup(func() { _ = exec.Command(standin.Tmux(t), "-S", socket, "kill-server").Run() })
	if presence, err := (agents.Identity{PID: first.PID, Start: first.Start}).Presence(); err != nil || presence != agents.PresencePresent {
		t.Fatalf("the worker reads %q (%v) after its service closed, want it still running, or there is no survivor", presence, err)
	}
	if got := session.NewPipe(h, agent.Runner()).Probe(t.Context(), first.String()); got.Liveness != taskstate.Unobserved || got.Reason != session.ReasonUnheld {
		t.Fatalf("the survivor probes %+v from a provider holding no handle, want Unobserved, unheld", got)
	}

	o, _ = workerOptions(t, h)
	withServiceOptions(t, o, func(running serviceUnderTest) {
		if _, _, ok := running.service.SessionOf(id); ok {
			t.Fatal("the restarted service holds a session for the survivor, so it launched a second worker beside it")
		}
		waitState(t, running.client, id, "read its terminal Unobserved", func(r taskstate.Resolution) bool {
			return r.Basis.Process == taskstate.Unobserved
		})
		unobservable := waitGone(t, h, id, "unobservable", first)
		if !unobservable.Queued() {
			t.Fatalf("the unobservable case's wake is %+v, want it queued for the coordinator", unobservable)
		}
		if attempts := attemptsOf(t, h, id); len(attempts) != 1 {
			t.Fatalf("the task has attempts %+v while its survivor stands, want the one launch and no relaunch", attempts)
		}
		if _, _, ok := running.service.SessionOf(id); ok {
			t.Fatal("the service launched a worker beside the survivor after surfacing it")
		}

		// A person ends the survivor's server; the pane's process goes with
		// its terminal, and the same service reads the change.
		if out, err := exec.Command(standin.Tmux(t), "-S", socket, "kill-server").CombinedOutput(); err != nil {
			t.Fatalf("ending the survivor's server: %v: %s", err, out)
		}
		waitState(t, running.client, id, "read its terminal Dead", func(r taskstate.Resolution) bool {
			return r.Basis.Process == taskstate.Dead
		})
		dead := waitGone(t, h, id, "dead", first)
		if !dead.Queued() {
			t.Fatalf("the dead case's wake is %+v, want it queued for the coordinator", dead)
		}
		if attempts := attemptsOf(t, h, id); len(attempts) != 1 {
			t.Fatalf("the task has attempts %+v before any restart, want no relaunch outside reconciliation", attempts)
		}
	})

	o, _ = workerOptions(t, h)
	withServiceOptions(t, o, func(running serviceUnderTest) {
		provider, launched := waitRelaunchedSession(t, running.service, id, unestablished)
		waitCapture(t, provider, launched, "worker ready")
		if launched.Reference() == first {
			t.Fatalf("the relaunch reused the first attempt's reference %s", first)
		}
	})
	if attempts := attemptsOf(t, h, id); len(attempts) != 2 {
		t.Fatalf("the task has attempts %+v after the restart, want the launch's and the relaunch's", attempts)
	}
}
