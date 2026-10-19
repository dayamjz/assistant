package cli_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// TestTasksListingCarriesAFleetDigestAndAuthorizesNothing is the fleet digest
// on the tasks listing, which is where it lives because section 9 names no
// command and no flag for one.
//
// What it establishes is that the digest is the resolved states on the same
// answer, not the newest history entry (P8): a task whose newest event says
// the worker is at work, and that an open hold outranks, is counted as
// waiting on a person. A task resolved running from that event, with no
// terminal observed live, is counted as unproven and the person's line says
// it is not read as succeeding. An open hold's text is redacted. A resolved
// hold and an acknowledged wake are absent, and listing does not close the
// open hold or acknowledge the queued wake, including across a restart of
// the service. The person's lines carry the digest's phrase and no task
// identifier (P13). assistant digest and tasks --digest are refused.
//
// The checkout is the suite's temporary repository. Nothing is pushed.
func TestTasksListingCarriesAFleetDigestAndAuthorizesNothing(t *testing.T) {
	principles.Cite(t, principles.P8)
	principles.Cite(t, principles.P13)
	principles.Cite(t, principles.P14)
	vocabtest.Control(t)

	h := newHome(t)
	subject := newSubject(t)
	if got := run(t, h, subject, "digest"); got.code != machine.ExitUsage {
		t.Fatalf("assistant digest exited %s, want incorrect usage; section 9 names no such command", got.code)
	}
	if got := run(t, h, subject, "tasks", "--digest"); got.code != machine.ExitUsage {
		t.Fatalf("assistant tasks --digest exited %s, want incorrect usage; section 9 names no such flag", got.code)
	}

	const (
		working = "digestworkaa"
		failed  = "digestfailaa"
		heldID  = "digestholdaa"
		acked   = "digestackaaa"
		openKey = "task/digestholdaa/ask"
		shutKey = "task/digestackaaa/closed"
	)
	subjectText := "confirm https://pat:supersecret@example.invalid/a.git"
	detailText := "see https://user:othersecret@example.invalid/b.git"

	records := openStore(t, h)
	fleetTask(t, records, working, "the worker", [2]string{"working", "still going"})
	fleetTask(t, records, failed, "the build", [2]string{"failed", "the build broke"})
	fleetTask(t, records, heldID, "the question", [2]string{"working", "still going"})
	fleetTask(t, records, acked, "the notice")
	if _, err := records.RegisterHold(t.Context(), store.Hold{
		Key: openKey, TaskID: store.Known(heldID), Subject: subjectText, Detail: detailText,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := records.RegisterHold(t.Context(), store.Hold{
		Key: shutKey, TaskID: store.Known(acked), Subject: subjectText,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := records.ResolveHold(t.Context(), shutKey, "answered", store.ResolvedByProgram()); err != nil {
		t.Fatal(err)
	}
	inc, err := records.BeginIncarnation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	queued, err := records.AppendWakeWithMarkers(t.Context(), inc, store.WakeRequest{
		TaskID: failed, Kind: "hold-opened", Marker: "digest-queued",
	}, store.MarkerAdvance{})
	if err != nil {
		t.Fatal(err)
	}
	acknowledged, err := records.AppendWakeWithMarkers(t.Context(), inc, store.WakeRequest{
		TaskID: acked, Kind: "hold-opened", Marker: "digest-acked",
	}, store.MarkerAdvance{})
	if err != nil {
		t.Fatal(err)
	}
	if err := records.AcknowledgeWakes(t.Context(), []int64{acknowledged.Wake.ID}, store.AcknowledgedByProgram()); err != nil {
		t.Fatal(err)
	}
	if err := records.Close(); err != nil {
		t.Fatal(err)
	}

	stop := serveUntilStopped(t, h)
	first := listedTasks(t, h, subject)
	assertDigest(t, first, working, failed, heldID, openKey, shutKey)
	plain := run(t, h, subject, "tasks")
	if plain.code != machine.ExitOK {
		t.Fatalf("assistant tasks exited %s:\n%s%s", plain.code, plain.stdout, plain.stderr)
	}
	for _, want := range []string{"piece(s) of fleet work are on record", "starts nothing and answers nothing", "not read as succeeding"} {
		if !strings.Contains(plain.stdout, want) {
			t.Errorf("the listing does not say %q:\n%s", want, plain.stdout)
		}
	}
	for _, id := range []string{working, failed, heldID, acked, "supersecret", "othersecret"} {
		if strings.Contains(plain.stdout, id) {
			t.Errorf("the person-facing listing carries %q:\n%s", id, plain.stdout)
		}
	}
	for _, line := range strings.Split(strings.TrimRight(plain.stdout, "\n"), "\n") {
		if line == "" {
			continue
		}
		vocabtest.Assert(t, line)
	}
	vocabtest.Assert(t, machine.DigestPhrase(first.Digest).String())

	check := openStore(t, h)
	still, err := check.Hold(t.Context(), openKey)
	if err != nil || !still.Open() {
		t.Fatalf("listing resolved the hold: %+v (%v)", still, err)
	}
	if err := check.Close(); err != nil {
		t.Fatal(err)
	}
	stop()

	serve(t, h)
	second := listedTasks(t, h, subject)
	assertDigest(t, second, working, failed, heldID, openKey, shutKey)
	for _, id := range []string{working, failed, heldID, acked} {
		if taskByID(t, first, id).State.State != taskByID(t, second, id).State.State {
			t.Fatalf("%s was %s before the restart and %s after", id,
				taskByID(t, first, id).State.State, taskByID(t, second, id).State.State)
		}
	}

	records = openStore(t, h)
	t.Cleanup(func() { _ = records.Close() })
	open, err := records.Hold(t.Context(), openKey)
	if err != nil || !open.Open() {
		t.Fatalf("the open hold after two listings and a restart is %+v (%v)", open, err)
	}
	shut, err := records.Hold(t.Context(), shutKey)
	if err != nil || shut.Open() {
		t.Fatalf("the resolved hold is %+v (%v)", shut, err)
	}
	wakes, err := records.QueuedWakes(t.Context(), 100)
	if err != nil {
		t.Fatal(err)
	}
	var sawQueued, sawAcked bool
	for _, w := range wakes {
		if w.ID == queued.Wake.ID {
			sawQueued = true
		}
		if w.ID == acknowledged.Wake.ID {
			sawAcked = true
		}
	}
	if !sawQueued || sawAcked {
		t.Fatalf("queued wakes after listing are %+v, want %d still queued and %d not", wakes, queued.Wake.ID, acknowledged.Wake.ID)
	}
}

func listedTasks(t *testing.T, h *home.Home, subject string) machine.Tasks {
	t.Helper()
	got := run(t, h, subject, "--json", "tasks")
	if got.code != machine.ExitOK {
		t.Fatalf("assistant --json tasks exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	var listed machine.Tasks
	if err := json.Unmarshal([]byte(got.stdout), &listed); err != nil {
		t.Fatalf("the listing is not a document: %v\n%s", err, got.stdout)
	}
	if listed.Digest.Counts == nil {
		t.Fatalf("the listing has no digest:\n%s", got.stdout)
	}
	return listed
}

func assertDigest(t *testing.T, listed machine.Tasks, working, failed, heldID, openKey, shutKey string) {
	t.Helper()
	held := taskByID(t, listed, heldID)
	if held.State.State != taskstate.Held || held.State.Source != taskstate.SourceHold {
		t.Fatalf("the held task is %s from %s, want held from the hold", held.State.State, held.State.Source)
	}
	if held.State.Basis.EventKind != "working" {
		t.Fatalf("the held task's newest event is %q, want working, so the digest is not shown to ignore an event it never saw", held.State.Basis.EventKind)
	}
	work := taskByID(t, listed, working)
	if work.State.State != taskstate.Running || work.State.Progress {
		t.Fatalf("the working task is %+v, want running without a live terminal", work.State)
	}
	var running, unproven int
	for _, c := range listed.Digest.Counts {
		if c.State == taskstate.Running {
			running, unproven = c.Count, c.Unproven
		}
	}
	wantRunning := 0
	for _, task := range listed.Tasks {
		if task.State.State == taskstate.Running {
			wantRunning++
		}
	}
	if wantRunning == 0 || running != wantRunning || unproven < 1 {
		t.Fatalf("running count %d unproven %d, with %d tasks resolved running", running, unproven, wantRunning)
	}
	if !slices.ContainsFunc(listed.Digest.InFlight, func(item machine.DigestTask) bool {
		return item.Task == working && item.State == taskstate.Running && !item.Proven
	}) {
		t.Fatalf("in flight is %+v, without the unproven running task", listed.Digest.InFlight)
	}
	if !slices.ContainsFunc(listed.Digest.InFlight, func(item machine.DigestTask) bool {
		return item.Task == failed && item.State == taskstate.Failed
	}) {
		t.Fatalf("in flight is %+v, without the failed task an unacknowledged wake keeps there", listed.Digest.InFlight)
	}

	var sawOpen, sawShut bool
	for _, h := range listed.Digest.Holds {
		if h.Key == shutKey {
			sawShut = true
		}
		if h.Key == openKey {
			sawOpen = true
			if !strings.Contains(h.Subject, redact.Marker) || strings.Contains(h.Subject, "supersecret") || strings.Contains(h.Detail, "othersecret") {
				t.Fatalf("the open hold's text is %q / %q", h.Subject, h.Detail)
			}
		}
	}
	if !sawOpen || sawShut {
		t.Fatalf("holds are %+v, want %s and not %s", listed.Digest.Holds, openKey, shutKey)
	}
	var sawQueued, sawAcked bool
	for _, w := range listed.Digest.Wakes {
		switch w.Marker {
		case "digest-queued":
			sawQueued = true
		case "digest-acked":
			sawAcked = true
		}
	}
	if !sawQueued || sawAcked {
		t.Fatalf("wakes are %+v, want the queued marker and not the acknowledged one", listed.Digest.Wakes)
	}
	raw, err := json.Marshal(listed.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "supersecret") || strings.Contains(string(raw), "othersecret") {
		t.Fatalf("the digest still carries a credential: %s", raw)
	}
}

func taskByID(t *testing.T, listed machine.Tasks, id string) machine.Task {
	t.Helper()
	for _, task := range listed.Tasks {
		if task.Record.ID == id {
			return task
		}
	}
	t.Fatalf("the listing has no task %s", id)
	return machine.Task{}
}
