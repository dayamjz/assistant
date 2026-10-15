//go:build unix

package service_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/supervisor"
)

// waited is one wakes.wait answered: the rows, how long the call took, and
// the error it ended with.
type waited struct {
	wakes   machine.Wakes
	elapsed time.Duration
	err     error
}

// waitOn arms wakes.wait over the client on a goroutine of its own and
// returns the channel its one answer arrives on, so a test can act while the
// wait blocks.
func waitOn(ctx context.Context, client *ipc.Client, seconds int) <-chan waited {
	answered := make(chan waited, 1)
	go func() {
		began := time.Now()
		var out machine.Wakes
		err := client.Call(ctx, ipc.MethodWakesWait, machine.WakeWaitRequest{TimeoutSeconds: seconds}, &out)
		answered <- waited{wakes: out, elapsed: time.Since(began), err: err}
	}()
	return answered
}

// The wait on an idle fleet blocks for its bound and answers with no wakes.
// The home has no task, so the supervisor loop has nothing to surface and
// the queue stays empty for the whole bound; the answer is the bound
// elapsing, which the call's own duration shows, and not a read that
// returned at once. An out-of-range bound is refused as an invalid request
// before anything blocks, at both ends of the range and past a duration's
// reach.
//
// It cites P9: the coordinator's side of "supervision costs nothing until
// something is actionable" is a wait that does not return while nothing is.
func TestTheWaitOnAnIdleFleetAnswersEmptyAtItsBound(t *testing.T) {
	principles.Cite(t, principles.P9)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	withService(t, h, func(running serviceUnderTest) {
		got := <-waitOn(t.Context(), running.client, 1)
		if got.err != nil {
			t.Fatalf("wakes.wait on an idle fleet: %v", got.err)
		}
		if len(got.wakes.Wakes) != 0 {
			t.Fatalf("wakes.wait on an idle fleet answered %+v, want no wakes", got.wakes)
		}
		if got.elapsed < time.Second {
			t.Fatalf("wakes.wait with a one-second bound answered after %s, before its bound", got.elapsed)
		}
		for _, seconds := range []int{-1, int(machine.MaxWakeWaitTimeout/time.Second) + 1, 1 << 30} {
			began := time.Now()
			var out machine.Wakes
			err := running.client.Call(t.Context(), ipc.MethodWakesWait, machine.WakeWaitRequest{TimeoutSeconds: seconds}, &out)
			if !errors.Is(err, ipc.ErrInvalidRequest) {
				t.Fatalf("wakes.wait with a bound of %d seconds = %v, want ErrInvalidRequest", seconds, err)
			}
			if time.Since(began) >= time.Second {
				t.Fatalf("wakes.wait with a bound of %d seconds blocked for %s before it was refused", seconds, time.Since(began))
			}
		}
	})
}

// The wait wakes the coordinator on the wake the supervisor loop surfaces
// when a worker's terminal dies, keeps returning it until it is
// acknowledged, survives a service restart, and is the coordinator's.
//
// From the worker, the wait is refused as a worker, on wakes.list's terms.
// From outside every worker group it is armed while the worker is live and
// has not answered a second later; the worker is then ended through the
// handle the service observes it by, and the answers the wait gives from
// then on carry the task's terminal-gone wake, which the loop surfaced and
// nothing here wrote. A wait armed again without an acknowledgement answers
// at once with the same rows, which is at-least-once delivery. The service
// is then closed and a second one opened on the same home: a wait armed
// against it answers at once with the terminal-gone row still queued, so
// re-arming after a restart loses nothing. Once what it returned is
// acknowledged, a wait does not return those rows again.
//
// The second service is configured with relaunch_attempts at zero. A
// restart's reconciliation otherwise relaunches the task and, as the
// program, acknowledges the wakes about the session the relaunch replaced,
// which is relaunch.go's rule and would take the very row this follows out
// of the queue before the wait could read it; at zero the task holds for a
// person instead and the row stays queued.
func TestTheWaitReturnsATerminalGoneWakeUntilItIsAcknowledged(t *testing.T) {
	principles.Cite(t, principles.P9)
	var h *home.Home
	var task string
	var gone int64
	launchedWorker(t, "task/wait", func(w launched) {
		h, task = w.home, w.task
		if _, code := callAs(t, w, ipc.MethodWakesWait, machine.WakeWaitRequest{TimeoutSeconds: 1}); code != ipc.CodeWorker {
			t.Fatalf("wakes.wait from the worker answered %q, want %q", code, ipc.CodeWorker)
		}

		armed := waitOn(t.Context(), w.running.client, 60)
		select {
		case early := <-armed:
			t.Fatalf("the wait answered while the worker was live: %+v", early)
		case <-time.After(time.Second):
		}
		if err := w.provider.Send(t.Context(), w.session, "finish"); err != nil {
			t.Fatalf("Send: %v", err)
		}
		waitCapture(t, w.provider, w.session, "worker finished")

		// The first answer is the wait that was armed before the terminal
		// died. Should the loop surface another of the task's wakes first,
		// that row is returned too and stays queued, so a wait armed again
		// answers at once; the terminal-gone row is what this waits for.
		deadline := time.Now().Add(60 * time.Second)
		for {
			got := <-armed
			if got.err != nil {
				t.Fatalf("wakes.wait: %v", got.err)
			}
			if gone = terminalGoneOf(got.wakes, task); gone != 0 {
				break
			}
			if !time.Now().Before(deadline) {
				t.Fatalf("no wait answered with a terminal-gone wake for task %s; the last answered %+v", task, got.wakes)
			}
			armed = waitOn(t.Context(), w.running.client, 60)
		}
		began := time.Now()
		again := mustWait(t, w.running.client, 60)
		if !holdsAll(again, []int64{gone}) || time.Since(began) > 30*time.Second {
			t.Fatalf("a wait armed again without an acknowledgement answered %+v after %s, want wake %d again at once",
				again, time.Since(began), gone)
		}
	})

	if err := os.WriteFile(h.ConfigFile(), []byte(`{"supervision_beacon_interval": "1s", "relaunch_attempts": 0}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	o, _ := workerOptions(t, h)
	withServiceOptions(t, o, func(running serviceUnderTest) {
		began := time.Now()
		restarted := mustWait(t, running.client, 60)
		if !holdsAll(restarted, []int64{gone}) || time.Since(began) > 30*time.Second {
			t.Fatalf("a wait armed after the restart answered %+v after %s, want wake %d at once", restarted, time.Since(began), gone)
		}
		handled := idsOf(restarted)
		var acknowledged machine.Acknowledged
		if err := running.client.Call(t.Context(), ipc.MethodWakesAcknowledge, machine.AcknowledgeRequest{Wakes: handled}, &acknowledged); err != nil {
			t.Fatalf("wakes.acknowledge: %v", err)
		}
		after := mustWait(t, running.client, 1)
		for _, wake := range after.Wakes {
			for _, id := range handled {
				if wake.ID == id {
					t.Fatalf("wake %d was acknowledged and a wait returned it again: %+v", id, after)
				}
			}
		}
	})
}

// mustWait is one wakes.wait with the bound given, failing the test unless
// it answered.
func mustWait(t *testing.T, client *ipc.Client, seconds int) machine.Wakes {
	t.Helper()
	got := <-waitOn(t.Context(), client, seconds)
	if got.err != nil {
		t.Fatalf("wakes.wait: %v", got.err)
	}
	return got.wakes
}

// terminalGoneOf is the identifier of a queued terminal-gone wake of the
// task among the rows, or zero when there is none.
func terminalGoneOf(w machine.Wakes, task string) int64 {
	for _, wake := range w.Wakes {
		if wake.TaskID == task && wake.Kind == string(supervisor.KindTerminalGone) && wake.Queued() {
			return wake.ID
		}
	}
	return 0
}

// idsOf is the identifiers of the rows.
func idsOf(w machine.Wakes) []int64 {
	out := make([]int64, 0, len(w.Wakes))
	for _, wake := range w.Wakes {
		out = append(out, wake.ID)
	}
	return out
}

// holdsAll reports whether every one of ids is a queued row of the answer.
func holdsAll(w machine.Wakes, ids []int64) bool {
	for _, id := range ids {
		found := false
		for _, wake := range w.Wakes {
			found = found || (wake.ID == id && wake.Queued())
		}
		if !found {
			return false
		}
	}
	return len(ids) > 0
}
