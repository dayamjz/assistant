//go:build unix

package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
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

// The service pushes a queued wake to the coordinator and never to a
// worker, the push takes nothing from the queue, a drain hands the row out
// without closing it, and the row comes back until the coordinator
// acknowledges it: delivery is at least once, decided by attachment and not
// by a clock.
//
// From the worker, both the wait and the wake stream are refused as a
// worker, on wakes.list's terms. From outside every worker group the wake
// stream opens with its gap, and an open event stream is held beside it on
// the same connection. The worker is then ended through the handle the
// service observes it by, and the wake stream is pushed the task's
// terminal-gone wake, which the loop surfaced and nothing here wrote, with
// the row's identifier as its revision; the open event stream carries no
// wake. The pushed row is read back from the records and is still queued,
// so the push was a notice and not the drain (P8: what the coordinator was
// told is not the record of what is true now).
//
// While the stream is held the coordinator is attached: a drain hands the
// row out, recorded as handed to the coordinator and still queued, and a
// second drain does not hand it out again under it. The stream is then
// closed without acknowledging, which is the coordinator going away, and a
// drain is handed the row a second time. The service is then closed and a
// second one opened on the same home, which is the restart: a wait armed
// against it answers at once with the same row, still not acknowledged, a
// third handout. Acknowledging it is accepted and recorded as the
// coordinator's, and after that no wait returns it and acknowledging it
// again is refused, which is the positive control: an acknowledged row does
// not come back.
//
// The second service is configured with relaunch_attempts at zero. A
// restart's reconciliation otherwise relaunches the task and, as the
// program, acknowledges the wakes about the session the relaunch replaced,
// which is relaunch.go's rule and would take the very row this follows out
// of the queue before the wait could drain it; at zero the task holds for a
// person instead and the row stays queued.
//
// It cites P9, because the coordinator is woken by the wake rather than by
// asking again, and P8.
func TestAWakeIsHandedOutUntilTheCoordinatorAcknowledgesIt(t *testing.T) {
	principles.Cite(t, principles.P9)
	principles.Cite(t, principles.P8)
	var h *home.Home
	var task string
	var gone int64
	launchedWorker(t, "task/push", func(w launched) {
		h, task = w.home, w.task
		for _, method := range []ipc.Method{ipc.MethodWakesWait, ipc.MethodWakesSubscribe} {
			if _, code := callAs(t, w, method, machine.WakeWaitRequest{TimeoutSeconds: 1}); code != ipc.CodeWorker {
				t.Fatalf("%s from the worker answered %q, want %q", method, code, ipc.CodeWorker)
			}
		}

		ctx := t.Context()
		pushes, err := w.running.client.SubscribeTo(ctx, ipc.MethodWakesSubscribe, nil)
		if err != nil {
			t.Fatalf("opening the wake stream: %v", err)
		}
		defer pushes.Close()
		open, err := w.running.client.Subscribe(ctx, nil)
		if err != nil {
			t.Fatalf("opening the event stream: %v", err)
		}
		defer open.Close()
		if first := recvWithin(t, pushes, 10*time.Second); first.Type != ipc.TypeGap {
			t.Fatalf("the wake stream opened with %+v, want the opening gap", first)
		}

		if err := w.provider.Send(ctx, w.session, "finish"); err != nil {
			t.Fatalf("Send: %v", err)
		}
		waitCapture(t, w.provider, w.session, "worker finished")
		for gone == 0 {
			event := recvWithin(t, pushes, 60*time.Second)
			if event.Type != ipc.TypeWakeQueued {
				continue
			}
			var row store.Wake
			if err := json.Unmarshal(event.Payload, &row); err != nil {
				t.Fatalf("a pushed wake does not read as a wake row: %v", err)
			}
			if row.TaskID == task && row.Kind == string(supervisor.KindTerminalGone) {
				if event.Revision != uint64(row.ID) || !row.Queued() {
					t.Fatalf("the pushed wake is %+v at revision %d, want its own identifier as the revision and the row queued", row, event.Revision)
				}
				gone = row.ID
			}
		}
		if row := wakeRow(t, recordsOf(t, h), task, gone); !row.Queued() || row.HandedOutAt.IsKnown() {
			t.Fatalf("after the push the records hold %+v, want wake %d still queued and not handed out: a push is not a drain", row, gone)
		}
		for {
			quiet, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
			event, err := open.Recv(quiet)
			cancel()
			if err != nil {
				break
			}
			if event.Type == ipc.TypeWakeQueued {
				t.Fatalf("the open event stream carried a wake: %+v", event)
			}
		}

		if !handedOut(mustList(t, w.running.client), gone, 1) {
			t.Fatalf("a drain while the stream is held did not hand out wake %d", gone)
		}
		if row := wakeRow(t, recordsOf(t, h), task, gone); !row.Queued() || row.HandedOutTo != store.Known(store.AcknowledgedByCoordinator()) {
			t.Fatalf("after the drain the records hold %+v, want wake %d handed to the coordinator and still queued", row, gone)
		}
		if again := mustList(t, w.running.client); listed(again, gone) {
			t.Fatalf("a second drain while the coordinator is attached handed wake %d out again: %+v", gone, again)
		}

		pushes.Close()
		deadline := time.Now().Add(30 * time.Second)
		for !handedOut(mustList(t, w.running.client), gone, 2) {
			if !time.Now().Before(deadline) {
				t.Fatalf("wake %d was never handed out again after the coordinator's stream closed", gone)
			}
			time.Sleep(50 * time.Millisecond)
		}
	})

	if err := os.WriteFile(h.ConfigFile(), []byte(`{"supervision_beacon_interval": "1s", "relaunch_attempts": 0}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	o, _ := workerOptions(t, h)
	withServiceOptions(t, o, func(running serviceUnderTest) {
		began := time.Now()
		restarted := mustWait(t, running.client, 60)
		if !handedOut(restarted, gone, 3) || time.Since(began) > 30*time.Second {
			t.Fatalf("a wait armed after the restart answered %+v after %s, want wake %d handed out again at once", restarted, time.Since(began), gone)
		}
		var acknowledged machine.Acknowledged
		if err := running.client.Call(t.Context(), ipc.MethodWakesAcknowledge, machine.AcknowledgeRequest{Wakes: []int64{gone}}, &acknowledged); err != nil {
			t.Fatalf("acknowledging the handed-out wake: %v", err)
		}
		if row := wakeRow(t, recordsOf(t, h), task, gone); row.Queued() || row.AcknowledgedBy != store.Known(store.AcknowledgedByCoordinator()) {
			t.Fatalf("after the acknowledgement the records hold %+v, want wake %d closed by the coordinator", row, gone)
		}
		if after := mustWait(t, running.client, 1); listed(after, gone) {
			t.Fatalf("wake %d was acknowledged and a wait returned it again: %+v", gone, after)
		}
		err := running.client.Call(t.Context(), ipc.MethodWakesAcknowledge, machine.AcknowledgeRequest{Wakes: []int64{gone}}, &acknowledged)
		if !errors.Is(err, ipc.ErrInvalidRequest) {
			t.Fatalf("acknowledging an acknowledged wake again = %v (%+v), want ErrInvalidRequest", err, acknowledged)
		}
	})
}

// The control for the push: a coordinator that never holds the wake stream
// and only polls wakes.list still gets the wake, because the queue rather
// than the push is what carries it. The worker is ended, and the list is
// polled until it returns the task's terminal-gone wake, handed out and
// still queued. A coordinator that holds no stream is not attached, so the
// next list hands it out again; once it is acknowledged, no list returns it.
//
// It cites P9: absence of a listener does not make a crashed worker
// invisible.
func TestAWakeReachesACoordinatorThatOnlyPolls(t *testing.T) {
	principles.Cite(t, principles.P9)
	launchedWorker(t, "task/poll", func(w launched) {
		if err := w.provider.Send(t.Context(), w.session, "finish"); err != nil {
			t.Fatalf("Send: %v", err)
		}
		waitCapture(t, w.provider, w.session, "worker finished")
		var gone int64
		deadline := time.Now().Add(60 * time.Second)
		for gone == 0 {
			for _, wake := range mustList(t, w.running.client).Wakes {
				if !wake.HandedOut() {
					t.Fatalf("wakes.list returned %+v, want every row it lists handed out and still queued", wake)
				}
				if wake.TaskID == w.task && wake.Kind == string(supervisor.KindTerminalGone) {
					gone = wake.ID
				}
			}
			if gone == 0 && !time.Now().Before(deadline) {
				t.Fatalf("polling never returned a terminal-gone wake for task %s", w.task)
			}
			time.Sleep(100 * time.Millisecond)
		}
		if again := mustList(t, w.running.client); !listed(again, gone) {
			t.Fatalf("wake %d was not acknowledged and the next list, with no stream held, did not hand it out again: %+v", gone, again)
		}
		var acknowledged machine.Acknowledged
		if err := w.running.client.Call(t.Context(), ipc.MethodWakesAcknowledge, machine.AcknowledgeRequest{Wakes: []int64{gone}}, &acknowledged); err != nil {
			t.Fatalf("acknowledging: %v", err)
		}
		if after := mustList(t, w.running.client); listed(after, gone) {
			t.Fatalf("wake %d was acknowledged and the next list returned it again: %+v", gone, after)
		}
	})
}

// mustList is one wakes.list, failing the test unless it answered.
func mustList(t *testing.T, client *ipc.Client) machine.Wakes {
	t.Helper()
	var out machine.Wakes
	if err := client.Call(t.Context(), ipc.MethodWakesList, machine.WakesRequest{}, &out); err != nil {
		t.Fatalf("wakes.list: %v", err)
	}
	return out
}

// listed reports whether an answer carries the wake.
func listed(w machine.Wakes, id int64) bool {
	for _, wake := range w.Wakes {
		if wake.ID == id {
			return true
		}
	}
	return false
}

// handedOut reports whether an answer carries the wake still queued and
// handed out for the given time.
func handedOut(w machine.Wakes, id, handouts int64) bool {
	for _, wake := range w.Wakes {
		if wake.ID == id {
			return wake.HandedOut() && wake.Handouts == store.Known(handouts)
		}
	}
	return false
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

// recvWithin is the next event on the stream, failing the test when none
// arrives within wait.
func recvWithin(t *testing.T, stream *ipc.Stream, wait time.Duration) ipc.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), wait)
	defer cancel()
	event, err := stream.Recv(ctx)
	if err != nil {
		t.Fatalf("no event within %s: %v", wait, err)
	}
	return event
}

// wakeRow is the task's wake with the identifier, read from the records.
func wakeRow(t *testing.T, records *store.Store, task string, id int64) store.Wake {
	t.Helper()
	wakes, err := records.TaskWakes(t.Context(), task)
	if err != nil {
		t.Fatalf("reading the wakes of task %s: %v", task, err)
	}
	for _, w := range wakes {
		if w.ID == id {
			return w
		}
	}
	t.Fatalf("task %s has no wake %d: %+v", task, id, wakes)
	return store.Wake{}
}
