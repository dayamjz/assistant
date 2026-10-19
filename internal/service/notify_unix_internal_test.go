//go:build unix

package service

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
)

// configureNotify writes the operator's file with the loop's configuration
// and, when command is not empty, the notify command.
func configureNotify(t *testing.T, path, command string) {
	t.Helper()
	doc := strings.TrimSuffix(strings.TrimSpace(loopConfig), "}")
	if command != "" {
		encoded, err := json.Marshal(command)
		if err != nil {
			t.Fatal(err)
		}
		doc += `, "notify": {"command": ` + string(encoded) + `}`
	}
	if err := os.WriteFile(path, []byte(doc+"}\n"), 0o600); err != nil {
		t.Fatalf("writing the configuration: %v", err)
	}
}

// holdFor registers an open hold naming the task, which the loop's next pass
// surfaces as a hold-opened wake marked by the hold's key.
func holdFor(t *testing.T, records *store.Store, task, key string) {
	t.Helper()
	if _, err := records.RegisterHold(t.Context(), store.Hold{Key: key, TaskID: store.Known(task), Subject: "a decision", Detail: "which"}); err != nil {
		t.Fatalf("registering hold %s: %v", key, err)
	}
}

// holdWake is the queued hold-opened wake the loop surfaced for the hold,
// failing the test when the loop has not.
func holdWake(t *testing.T, records *store.Store, task, key string) store.Wake {
	t.Helper()
	for _, w := range wakesOfKind(t, records, task, supervisor.KindHoldOpened) {
		if w.Marker == "hold:"+key {
			return w
		}
	}
	t.Fatalf("the loop surfaced no hold-opened wake for hold %s", key)
	return store.Wake{}
}

// settledNotification waits for the wake's notification record to carry an
// outcome and returns it.
func settledNotification(t *testing.T, records *store.Store, wake int64) store.Notification {
	t.Helper()
	deadline := time.Now().Add(loopWait)
	for {
		n, err := records.Notification(t.Context(), wake)
		switch {
		case err == nil && n.Settled():
			return n
		case err != nil && !errors.Is(err, store.ErrNotFound):
			t.Fatalf("reading the notification of wake %d: %v", wake, err)
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the notification of wake %d was never settled (last read %+v, %v)", wake, n, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// sentLines is every line the command appended to out, each read back as
// the wake row it was sent, in the order it was sent.
func sentLines(t *testing.T, out string) []store.Wake {
	t.Helper()
	f, err := os.Open(out)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var rows []store.Wake
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var w store.Wake
		if err := json.Unmarshal(scanner.Bytes(), &w); err != nil {
			t.Fatalf("the command was sent %q, which does not read as a wake row: %v", scanner.Text(), err)
		}
		rows = append(rows, w)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return rows
}

// reopenRecords opens the home's records again, for a test that plants
// something between two services of the same home.
func reopenRecords(t *testing.T, l *loopSubject) *store.Store {
	t.Helper()
	records, err := store.Open(t.Context(), l.home.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	return records
}

// Each wake the loop appends is sent to the operator's notify command
// exactly once, as the row the coordinator is offered. Two holds naming a
// task surface two hold-opened wakes, and the command, which appends what it
// reads to a file, is run once for each with that wake's row on standard
// input; each record reads sent with exit status zero. Further passes send
// nothing again, and neither does a second service of the same home, which
// drains the record rather than remembering what it rang for: the third
// hold, registered while no service runs, is the one row the second
// service sends, and it arrives after nothing older was resent, because the
// sender offers the oldest pending wake first. Every wake stays queued for
// the coordinator, because sending one is not acknowledging it.
func TestEachWakeIsSentOnceToTheOperatorsCommand(t *testing.T) {
	t.Parallel()
	h, records := loopHome(t)
	out := filepath.Join(t.TempDir(), "sent")
	configureNotify(t, h.ConfigFile(), "cat >> '"+out+"'")
	const task = "notifysendaa"
	plantRunning(t, records, task, "")
	holdFor(t, records, task, "task/"+task+"/one")
	holdFor(t, records, task, "task/"+task+"/two")

	l := openLoop(t, h, records, nil, nil)
	l.pass(t)
	first := holdWake(t, l.records(t), task, "task/"+task+"/one")
	second := holdWake(t, l.records(t), task, "task/"+task+"/two")
	queued, err := l.records(t).QueuedWakes(t.Context(), 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range queued {
		n := settledNotification(t, l.records(t), w.ID)
		if n.Outcome != store.Known(store.NotificationSent) || n.ExitCode != store.Known(0) {
			t.Fatalf("the notification of wake %d is %+v, want sent with exit status 0", w.ID, n)
		}
	}
	l.pass(t)
	l.pass(t)
	if err := l.service.Close(); err != nil && !errors.Is(err, ErrSupervisionAbandoned) {
		t.Fatalf("closing the service: %v", err)
	}

	records = reopenRecords(t, l)
	holdFor(t, records, task, "task/"+task+"/three")
	l = openLoop(t, h, records, nil, nil)
	defer func() { _ = l.service.Close() }()
	l.pass(t)
	third := holdWake(t, l.records(t), task, "task/"+task+"/three")
	settledNotification(t, l.records(t), third.ID)

	sent := sentLines(t, out)
	times := map[int64]int{}
	for _, w := range sent {
		times[w.ID]++
	}
	all, err := l.records(t).QueuedWakes(t.Context(), 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range all {
		if times[w.ID] != 1 {
			t.Errorf("wake %d (%s %s) was sent %d times, want once", w.ID, w.Kind, w.Marker, times[w.ID])
		}
	}
	if len(sent) != len(all) {
		t.Errorf("the command was sent %d rows for %d wakes", len(sent), len(all))
	}
	if last := sent[len(sent)-1]; last.ID != third.ID || last.TaskID != task || last.Marker != third.Marker {
		t.Errorf("the last row sent is %+v, want wake %d, the one the second service surfaced", last, third.ID)
	}
	for _, id := range []int64{first.ID, second.ID, third.ID} {
		if row := wakeByID(t, l.records(t), task, id); !row.Queued() {
			t.Errorf("wake %d is %+v after it was sent, want it still queued for the coordinator", id, row)
		}
	}
}

// wakeByID is the task's wake with the identifier.
func wakeByID(t *testing.T, records *store.Store, task string, id int64) store.Wake {
	t.Helper()
	all, err := records.TaskWakes(t.Context(), task)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range all {
		if w.ID == id {
			return w
		}
	}
	t.Fatalf("task %s has no wake %d", task, id)
	return store.Wake{}
}

// A command that fails is recorded as failed, with its own exit status and
// the end of what it printed, and one that hangs is ended at its bound and
// recorded as failed without an exit status of its own; neither holds up
// the loop. The hanging command is still running when the loop is kicked,
// and the pass is reported while its notification is claimed and unsettled,
// so the loop did not wait on it. A failed wake is not sent again.
func TestAFailingNotifyCommandIsRecordedAndDoesNotHoldTheLoop(t *testing.T) {
	t.Parallel()
	h, records := loopHome(t)
	started := filepath.Join(t.TempDir(), "started")
	configureNotify(t, h.ConfigFile(), `read -r row; case "$row" in *"/hang"*) : > '`+started+`'; exec sleep 30;; esac; echo channel refused; exit 4`)
	const task = "notifyfailaa"
	plantRunning(t, records, task, "")
	holdFor(t, records, task, "task/"+task+"/fail")
	l := openLoopHooked(t, h, records, nil, nil, nil, func(hooks *supervisionHooks) { hooks.notifyTimeout = 3 * time.Second })
	defer func() { _ = l.service.Close() }()
	l.pass(t)

	failing := holdWake(t, l.records(t), task, "task/"+task+"/fail")
	n := settledNotification(t, l.records(t), failing.ID)
	detail, _ := n.Detail.Get()
	if n.Outcome != store.Known(store.NotificationFailed) || n.ExitCode != store.Known(4) || !strings.Contains(detail, "channel refused") {
		t.Fatalf("the failing command's notification is %+v, want failed with exit status 4 and its output", n)
	}

	acknowledgeOthers(t, l.records(t))
	holdFor(t, l.records(t), task, "task/"+task+"/hang")
	l.pass(t)
	hanging := holdWake(t, l.records(t), task, "task/"+task+"/hang")
	deadline := time.Now().Add(loopWait)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatal("the hanging command never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	l.pass(t)
	if claimed, err := l.records(t).Notification(t.Context(), hanging.ID); err != nil || claimed.Settled() {
		t.Fatalf("while the command hangs its notification is %+v, %v; want it claimed and unsettled after the loop's pass", claimed, err)
	}
	ended := settledNotification(t, l.records(t), hanging.ID)
	detail, _ = ended.Detail.Get()
	if ended.Outcome != store.Known(store.NotificationFailed) || ended.ExitCode.IsKnown() || !strings.Contains(detail, "did not exit within") {
		t.Fatalf("the hanging command's notification is %+v, want failed at its bound with no exit status", ended)
	}
	if again, err := l.records(t).Notification(t.Context(), failing.ID); err != nil || again.Outcome != store.Known(store.NotificationFailed) || again.SettledAt != n.SettledAt {
		t.Fatalf("the failed wake's record is now %+v, %v; want the first settlement standing", again, err)
	}
}

// acknowledgeOthers closes every queued wake that is not a hold-opened one,
// so what a later pass appends is the test's own.
func acknowledgeOthers(t *testing.T, records *store.Store) {
	t.Helper()
	queued, err := records.QueuedWakes(t.Context(), 100)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, w := range queued {
		if w.Kind != string(supervisor.KindHoldOpened) {
			ids = append(ids, w.ID)
		}
	}
	if len(ids) > 0 {
		if err := records.AcknowledgeWakes(t.Context(), ids, store.AcknowledgedByCoordinator()); err != nil {
			t.Fatal(err)
		}
	}
}

// A credential in what a wake is about never reaches the command. The
// hold's key carries a URL with a password, and the hold record keeps the
// key as it was registered, which is the control: the credential was in
// the facts the loop read. The loop surfaces the hold with the key in its
// marker, and the command, which appends what it reads to a file, reads the
// rest of the marker and not the password.
//
// What removes it in this build is the loop, which passes every marker
// through internal/redact before it is written, so the row the sender reads
// already carries none; the sender's own pass over the line is the same
// rule at the boundary the text leaves the home, and no row the loop can
// write reaches it with a shape the first pass left, so nothing here shows
// that second pass removing anything on its own.
func TestACredentialInAWakeNeverReachesTheCommand(t *testing.T) {
	t.Parallel()
	h, records := loopHome(t)
	out := filepath.Join(t.TempDir(), "sent")
	configureNotify(t, h.ConfigFile(), "cat >> '"+out+"'")
	const task = "notifyredact"
	key := "task/" + task + "/https://operator:hunter2@example.test/hook"
	plantRunning(t, records, task, "")
	holdFor(t, records, task, key)
	l := openLoop(t, h, records, nil, nil)
	defer func() { _ = l.service.Close() }()
	l.pass(t)

	if hold, err := l.records(t).Hold(t.Context(), key); err != nil || !strings.Contains(hold.Key, "hunter2") {
		t.Fatalf("the hold record is %+v, %v; want its key as registered, credential included", hold, err)
	}
	w := holdWake(t, l.records(t), task, redact.New().Redact(key))
	settledNotification(t, l.records(t), w.ID)
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "hunter2") || !strings.Contains(string(raw), "example.test/hook") {
		t.Fatalf("the command read %q, want the marker without its password", raw)
	}
}

// A home whose operator names no notify command runs nothing and records
// nothing: the loop surfaces the wake, it stays queued, and it has no
// notification record. The control is the same home's next service with
// the command named, which sends that very wake, still queued, from the
// record, so the absence was the key's and not a sender that had not got
// round to it.
func TestNoNotifyCommandRunsNothing(t *testing.T) {
	t.Parallel()
	h, records := loopHome(t)
	const task = "notifynoneaa"
	plantRunning(t, records, task, "")
	holdFor(t, records, task, "task/"+task+"/one")
	l := openLoop(t, h, records, nil, nil)
	l.pass(t)
	w := holdWake(t, l.records(t), task, "task/"+task+"/one")
	time.Sleep(time.Second)
	l.pass(t)
	if n, err := l.records(t).Notification(t.Context(), w.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("with no notify command the wake's notification is %+v, %v; want none", n, err)
	}
	if err := l.service.Close(); err != nil && !errors.Is(err, ErrSupervisionAbandoned) {
		t.Fatalf("closing the service: %v", err)
	}

	out := filepath.Join(t.TempDir(), "sent")
	configureNotify(t, h.ConfigFile(), "cat >> '"+out+"'")
	l = openLoop(t, h, reopenRecords(t, l), nil, nil)
	defer func() { _ = l.service.Close() }()
	if n := settledNotification(t, l.records(t), w.ID); n.Outcome != store.Known(store.NotificationSent) {
		t.Fatalf("once the command is named the wake's notification is %+v, want sent", n)
	}
	found := false
	for _, row := range sentLines(t, out) {
		found = found || row.ID == w.ID
	}
	if !found {
		t.Fatalf("the command was never sent wake %d", w.ID)
	}
}
