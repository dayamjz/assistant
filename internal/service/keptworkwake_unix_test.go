//go:build unix

package service_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/supervisor/feed"
)

// keptWorkWakeWait bounds each wait on the running service's timed loop,
// whose beacon interval the test configures as one second.
const keptWorkWakeWait = 30 * time.Second

// PRD principle P15's "kept work is never silent" and section 6's "it
// surfaces at once ... through the private channel when one is
// configured", for an entry no task names, end to end through the real
// socket, the real supervisor loop at its interval and the operator's real
// notify command: a person's push starts a run whose copy is kept because
// its commit landed nowhere. The loop raises the entry as exactly one
// kept-work wake, about the entry and under its project; the notify
// command is run once with that row; the coordinator's drain hands it out;
// and acknowledging it closes the wake and leaves the entry open, listed
// by status. Later passes raise nothing more. On #442's head the drain
// carries no such row and the command is sent nothing.
func TestAPersonsKeptRunRaisesOneKeptWorkWakeThatReachesTheChannelOnce(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	sent := filepath.Join(t.TempDir(), "sent")
	command, err := json.Marshal("cat >> '" + sent + "'")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.ConfigFile(), []byte(`{"supervision_beacon_interval": "1s", "notify": {"command": `+string(command)+`}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	withService(t, h, func(under serviceUnderTest) {
		started := startRunSkipping(t, under.client, subject, afterIntent()...)
		run := runByID(t, under.client, started.Record.ID)
		if run.Cleanup == nil || !run.Cleanup.Kept() {
			t.Fatalf("the run's copy reads %+v, want it kept, so that there is kept work to raise", run.Cleanup)
		}
		status := statusOf(t, under.client, subject)
		if len(status.KeptWork) != 1 || status.KeptWork[0].RunID.Or("") != run.Record.ID || status.KeptWork[0].TaskID.IsKnown() {
			t.Fatalf("status lists kept work %+v, want the person's run's one entry, which no task names", status.KeptWork)
		}
		entry := status.KeptWork[0]

		var drained machine.Wakes
		deadline := time.Now().Add(keptWorkWakeWait)
		for {
			drained = mustList(t, under.client)
			if len(drained.KeptWorkWakes) > 0 {
				break
			}
			if !time.Now().Before(deadline) {
				t.Fatalf("no drain handed out a kept-work wake about entry %d; the last one was %+v", entry.ID, drained)
			}
			time.Sleep(50 * time.Millisecond)
		}
		if len(drained.KeptWorkWakes) != 1 {
			t.Fatalf("the drain handed out kept-work wakes %+v, want exactly one", drained.KeptWorkWakes)
		}
		wake := drained.KeptWorkWakes[0]
		if wake.KeptWork != entry.ID || wake.Kind != string(supervisor.KindKeptWork) || wake.Project != store.Known(repository.ID) || wake.Branch != entry.Branch ||
			!wake.HandedOut() || wake.Handouts != store.Known(int64(1)) {
			t.Fatalf("the drained wake is %+v, want entry %d's kept-work wake under project %s, handed out once", wake, entry.ID, repository.ID)
		}
		for _, w := range drained.Wakes {
			if w.Kind == string(supervisor.KindKeptWork) {
				t.Fatalf("the entry was raised on a task's wake too: %+v", w)
			}
		}

		lines := sentKeptWorkWakes(t, sent, 1)
		if len(lines) != 1 || lines[0].ID != wake.ID || lines[0].KeptWork != entry.ID || lines[0].Project != store.Known(repository.ID) {
			t.Fatalf("the notify command was sent %+v, want kept-work wake %d once", lines, wake.ID)
		}
		notified, err := recordsOf(t, h).KeptWorkNotification(t.Context(), wake.ID)
		if err != nil || notified.Outcome != store.Known(store.NotificationSent) {
			t.Fatalf("the wake's notification record is %+v, %v; want it settled sent", notified, err)
		}

		var acknowledged machine.Acknowledged
		if err := under.client.Call(t.Context(), ipc.MethodWakesAcknowledge, machine.AcknowledgeRequest{KeptWorkWakes: []int64{wake.ID}}, &acknowledged); err != nil {
			t.Fatalf("acknowledging the kept-work wake: %v", err)
		}
		if len(acknowledged.KeptWorkWakes) != 1 || acknowledged.KeptWorkWakes[0] != wake.ID {
			t.Fatalf("the acknowledgement answered %+v, want kept-work wake %d closed", acknowledged, wake.ID)
		}
		awaitPassesAfter(t, h, 2)
		rows, err := recordsOf(t, h).KeptWorkWakes(t.Context())
		if err != nil || len(rows) != 1 || rows[0].Queued() || rows[0].AcknowledgedBy != store.Known(store.AcknowledgedByCoordinator()) {
			t.Fatalf("after the acknowledgement and two passes the kept-work wakes are %+v, %v; want the one, closed by the coordinator", rows, err)
		}
		if again := mustList(t, under.client); len(again.KeptWorkWakes) != 0 {
			t.Fatalf("a drain after the acknowledgement handed out %+v, want nothing", again.KeptWorkWakes)
		}
		if after := statusOf(t, under.client, subject); len(after.KeptWork) != 1 || after.KeptWork[0].ID != entry.ID {
			t.Fatalf("after the wake was acknowledged status lists %+v, want entry %d still open, since a wake answers nothing", after.KeptWork, entry.ID)
		}
		if lines := sentKeptWorkWakes(t, sent, 1); len(lines) != 1 {
			t.Fatalf("the notify command was sent %+v by the end, want the one wake once", lines)
		}
	})
}

// sentKeptWorkWakes waits until the notify command has appended at least
// want lines to out and returns every line, each decoded strictly as the
// bare kept-work wake row: a line naming a task, or one wrapped in the
// stream's envelope, fails the test.
func sentKeptWorkWakes(t *testing.T, out string, want int) []store.KeptWorkWake {
	t.Helper()
	deadline := time.Now().Add(keptWorkWakeWait)
	for {
		text, err := os.ReadFile(out)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		var rows []store.KeptWorkWake
		scanner := bufio.NewScanner(bytes.NewReader(text))
		for scanner.Scan() {
			var w store.KeptWorkWake
			strict := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
			strict.DisallowUnknownFields()
			if err := strict.Decode(&w); err != nil || w.ID == 0 || strings.Contains(scanner.Text(), `"task_id"`) {
				t.Fatalf("the notify command was sent %q, which does not read as a bare kept-work wake row: %v", scanner.Text(), err)
			}
			rows = append(rows, w)
		}
		if len(rows) >= want {
			return rows
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the notify command was sent %d kept-work wake(s), want %d: %q", len(rows), want, text)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// awaitPassesAfter waits until the loop has written its beacon n more times
// than when it was called. The loop writes the beacon at each tick before
// that tick's pass, so two more writes mean at least one whole pass began
// after the call.
func awaitPassesAfter(t *testing.T, h *home.Home, n uint64) {
	t.Helper()
	first, err := feed.ReadBeacon(h.Beacon())
	if err != nil {
		t.Fatalf("reading the beacon: %v", err)
	}
	deadline := time.Now().Add(keptWorkWakeWait)
	for {
		now, err := feed.ReadBeacon(h.Beacon())
		if err == nil && now.Epoch == first.Epoch && now.Sequence >= first.Sequence+n {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the loop did not write its beacon %d more times (from %+v, now %+v, %v)", n, first, now, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
