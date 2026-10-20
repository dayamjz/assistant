//go:build unix

package service

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/ipc"
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
// the wake row it was sent, in the order it was sent. Each line must decode
// as a store.Wake with no field that type does not declare, and must not
// read as an ipc.Event, so a line wrapped in the envelope wakes.subscribe
// pushes, with its type, revision and payload, fails every test that reads
// what the command was sent rather than decoding to an empty row.
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
		strict := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		strict.DisallowUnknownFields()
		if err := strict.Decode(&w); err != nil || w.ID == 0 {
			t.Fatalf("the command was sent %q, which does not read as a bare wake row: %v", scanner.Text(), err)
		}
		var envelope ipc.Event
		if err := json.Unmarshal(scanner.Bytes(), &envelope); err != nil || envelope.Type != "" || envelope.Payload != nil {
			t.Fatalf("the command was sent %q, which reads as an ipc.Event (%+v, %v), want the wake row bare", scanner.Text(), envelope, err)
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

// A wake whose marker still carries bearer-shaped text after the loop's
// redaction reaches the command as one line of valid JSON. Redacting an
// encoded line can break the JSON; the sender encodes the stored row as it
// is.
func TestAWakeWithBearerShapedTextReachesTheCommandAsValidJSON(t *testing.T) {
	t.Parallel()
	h, records := loopHome(t)
	out := filepath.Join(t.TempDir(), "sent")
	configureNotify(t, h.ConfigFile(), "cat >> '"+out+"'")
	const task = "notifyjsonaa"
	plantRunning(t, records, task, "")
	key := "task/" + task + "/Authorization: Bearer abcdefghijklmnop"
	redactedKey := redact.New().Redact(key)
	holdFor(t, records, task, key)
	l := openLoop(t, h, records, nil, nil)
	defer func() { _ = l.service.Close() }()
	l.pass(t)

	w := holdWake(t, l.records(t), task, redactedKey)
	settledNotification(t, l.records(t), w.ID)
	var sent *store.Wake
	for _, row := range sentLines(t, out) {
		if row.ID == w.ID {
			sent = &row
			break
		}
	}
	if sent == nil || sent.Marker != w.Marker {
		t.Fatalf("the command was never sent wake %d as valid JSON, or the marker differs: got %+v, want marker %q", w.ID, sent, w.Marker)
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
// already carries none.
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

// A notify command whose child holds its output is ended with its whole
// process group at the bound, and the delivery returns within the bound
// the sender states, notifyBound plus notifyGrace, rather than waiting on
// the child. The line runs sleep as a child of the shell rather than in its
// place, so the child holds standard output after the shell is ended; the
// shell records its own identifier, which is its process group's. While the
// command hangs that group exists, which is the control that the command
// was given a group of its own and that the later absence is not vacuous;
// once the record settles the group is gone, so the child was ended rather
// than abandoned, and the record reads failed at the bound with no exit
// status. The delivery is measured by the record itself, from its claim to
// its settlement.
func TestANotifyCommandsChildHoldingItsOutputDoesNotExtendTheBound(t *testing.T) {
	t.Parallel()
	h, records := loopHome(t)
	pidFile := filepath.Join(t.TempDir(), "pid")
	configureNotify(t, h.ConfigFile(), `read -r row; case "$row" in *"/hold"*) echo $$ > '`+pidFile+`'; sleep 60; : ;; esac`)
	const task = "notifyholdaa"
	const bound = time.Second
	plantRunning(t, records, task, "")
	holdFor(t, records, task, "task/"+task+"/hold")
	l := openLoopHooked(t, h, records, nil, nil, nil, func(hooks *supervisionHooks) { hooks.notifyTimeout = bound })
	defer func() { _ = l.service.Close() }()
	l.pass(t)
	w := holdWake(t, l.records(t), task, "task/"+task+"/hold")

	var group int
	deadline := time.Now().Add(loopWait)
	for {
		raw, err := os.ReadFile(pidFile)
		if err == nil && strings.HasSuffix(string(raw), "\n") {
			if group, err = strconv.Atoi(strings.TrimSpace(string(raw))); err != nil {
				t.Fatalf("the command recorded %q, not a process identifier", raw)
			}
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatal("the hanging command never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(-group, 0); err != nil {
		t.Fatalf("while the command hangs its process group %d answers %v; want it to exist, or the check below shows nothing", group, err)
	}

	n := settledNotification(t, l.records(t), w.ID)
	detail, _ := n.Detail.Get()
	if n.Outcome != store.Known(store.NotificationFailed) || n.ExitCode.IsKnown() || !strings.Contains(detail, "did not exit within") {
		t.Fatalf("the notification is %+v, want failed at its bound with no exit status", n)
	}
	settled, _ := n.SettledAt.Get()
	if took := settled.Sub(n.ClaimedAt); took >= bound+notifyGrace {
		t.Fatalf("the delivery took %s from claim to settlement, want under the stated %s plus %s", took, bound, notifyGrace)
	}
	deadline = time.Now().Add(loopWait)
	for {
		err := syscall.Kill(-group, 0)
		if errors.Is(err, syscall.ESRCH) {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("process group %d still answers %v after the delivery settled; the child holding the output was left running", group, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A store that refuses every claim is tried once per recheck interval and
// not once per wake or per ring of the bell. The fault is a real SQLite
// abort of the notification insert, planted through the store. While it
// stands the loop is kicked with a new hold every few tens of milliseconds,
// so the bell rings far more often than the interval elapses, and the
// service log, where each failed pass writes its one line, is counted: the
// bell rings until two more attempts are logged, so the sender kept trying,
// and for at least the window, and the attempts are no more than the time
// that passed holds intervals, with one of slack for the attempt the count
// opened on. Once the fault is lifted the wakes are sent, which is
// the control that the refusal was the store's.
func TestARefusedClaimWaitsTheRecheckInterval(t *testing.T) {
	t.Parallel()
	h, records := loopHome(t)
	out := filepath.Join(t.TempDir(), "sent")
	configureNotify(t, h.ConfigFile(), "cat >> '"+out+"'")
	const task = "notifyclaimf"
	const recheck = 300 * time.Millisecond
	const window = 1500 * time.Millisecond
	plantRunning(t, records, task, "")
	l := openLoopHooked(t, h, records, nil, nil, nil, func(hooks *supervisionHooks) { hooks.notifyRecheck = recheck })
	defer func() { _ = l.service.Close() }()
	lift, err := l.records(t).RefuseNotificationClaimsForTest(t.Context(), t)
	if err != nil {
		t.Fatal(err)
	}
	lifted := false
	defer func() {
		if !lifted {
			_ = lift()
		}
	}()

	refusals := func() int {
		raw, err := os.ReadFile(h.ServiceLog())
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(raw), "could not claim the notification of wake")
	}
	holdFor(t, l.records(t), task, "task/"+task+"/first")
	l.pass(t)
	deadline := time.Now().Add(loopWait)
	for refusals() == 0 {
		if !time.Now().Before(deadline) {
			t.Fatal("the sender never logged a refused claim")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The bell rings until the sender has tried twice more, which is the
	// event "it kept trying" means, and for at least the window, so the
	// bell has rung many times per interval. Only the upper bound is held
	// to the clock, and to the time that actually passed: a busy machine
	// can make the sender try less often, never more.
	before := refusals()
	began := time.Now()
	end := began.Add(window)
	deadline = began.Add(loopWait)
	for i := 0; time.Now().Before(end) || refusals()-before < 2; i++ {
		if !time.Now().Before(deadline) {
			t.Fatalf("the sender tried %d times more in %s with a %s recheck, want it to keep trying", refusals()-before, loopWait, recheck)
		}
		holdFor(t, l.records(t), task, "task/"+task+"/ring"+strconv.Itoa(i))
		l.pass(t)
		time.Sleep(30 * time.Millisecond)
	}
	attempts, elapsed := refusals()-before, time.Since(began)
	if most := int(elapsed/recheck) + 1; attempts > most {
		t.Fatalf("the sender tried %d times in %s with a %s recheck, want at most %d", attempts, elapsed, recheck, most)
	}

	if err := lift(); err != nil {
		t.Fatal(err)
	}
	lifted = true
	ring := holdWake(t, l.records(t), task, "task/"+task+"/ring0")
	if n := settledNotification(t, l.records(t), ring.ID); n.Outcome != store.Known(store.NotificationSent) {
		t.Fatalf("after the fault was lifted the notification is %+v, want sent", n)
	}
}

// A wake about a domain reaches the operator's notify command too, once,
// as the domain wake's row: a delegation whose subordinate's terminal is
// dead and past relaunch_attempts surfaces subordinate-gone and the bound's
// hold-opened as domain wakes, and the command is run once for each with
// that row on standard input, naming the delegation and the domain and no
// task, each record settled sent in domain_notification. Further passes and
// a second service of the same home send neither again. A sender reading
// only the task wake table sends nothing here and fails.
func TestADomainWakeIsSentOnceToTheOperatorsCommand(t *testing.T) {
	t.Parallel()
	out := filepath.Join(t.TempDir(), "sent")
	encoded, err := json.Marshal("cat >> '" + out + "'")
	if err != nil {
		t.Fatal(err)
	}
	h, records := loopHomeConfigured(t, `{"supervision_beacon_interval": "1s", "escalation_window": "1m", "relaunch_attempts": 0, "notify": {"command": `+string(encoded)+`}}`)
	d := plantDelegation(t, records, "alpha", "sub-crashed")
	terminals := newScriptedTerminals()
	terminals.dead("sub-crashed")
	clock := &fakeClock{now: time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)}
	l := openLoop(t, h, records, terminals, clock)
	l.nextPass(t)
	rows, err := l.records(t).DelegationWakes(t.Context(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int64{}
	for _, w := range rows {
		kinds[w.Kind] = w.ID
	}
	gone, hold := kinds[string(supervisor.KindSubordinateGone)], kinds[string(supervisor.KindHoldOpened)]
	if gone == 0 || hold == 0 {
		t.Fatalf("the delegation's wakes are %+v, want subordinate-gone and hold-opened", rows)
	}
	settled := func(records *store.Store, id int64) store.Notification {
		t.Helper()
		deadline := time.Now().Add(loopWait)
		for {
			n, err := records.DomainNotification(t.Context(), id)
			switch {
			case err == nil && n.Settled():
				return n
			case err != nil && !errors.Is(err, store.ErrNotFound):
				t.Fatalf("reading the notification of domain wake %d: %v", id, err)
			}
			if !time.Now().Before(deadline) {
				t.Fatalf("the notification of domain wake %d was never settled (last read %+v, %v)", id, n, err)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	for _, id := range []int64{gone, hold} {
		if n := settled(l.records(t), id); n.Outcome != store.Known(store.NotificationSent) || n.WakeID != id {
			t.Fatalf("the notification of domain wake %d is %+v, want sent", id, n)
		}
	}
	clock.Advance(10 * time.Second)
	l.pass(t)
	l.pass(t)
	if err := l.service.Close(); err != nil && !errors.Is(err, ErrSupervisionAbandoned) {
		t.Fatalf("closing the service: %v", err)
	}
	l = openLoop(t, h, reopenRecords(t, l), terminals, clock)
	defer func() { _ = l.service.Close() }()
	l.pass(t)
	l.pass(t)

	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	times := map[int64]int{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var w store.DomainWake
		strict := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		strict.DisallowUnknownFields()
		if err := strict.Decode(&w); err != nil || w.ID == 0 || w.Delegation != d.ID || w.Domain != "alpha" {
			t.Fatalf("the command was sent %q, which does not read as a domain wake of alpha: %v", scanner.Text(), err)
		}
		if strings.Contains(scanner.Text(), `"task_id"`) {
			t.Fatalf("the domain wake's line %q names a task", scanner.Text())
		}
		times[w.ID]++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if times[gone] != 1 || times[hold] != 1 || len(times) != 2 {
		t.Fatalf("the command was sent %v, want subordinate-gone %d and hold-opened %d once each", times, gone, hold)
	}
	for _, w := range []int64{gone, hold} {
		row, err := l.records(t).DelegationWakes(t.Context(), d.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range row {
			if r.ID == w && !r.Queued() {
				t.Errorf("domain wake %d was closed by being sent", w)
			}
		}
	}
}
