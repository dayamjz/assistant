package machine_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// TestDigestCountsTheResolutionAndNotTheEventItSuperseded is P8 for the
// fleet digest: a task whose newest event claims it is running, and whose
// resolution superseded that event, is counted as the resolution. A task
// resolved Running without progress is counted there and as unproven, and a
// task resolved Unknown is not counted as running. Which tasks are in flight
// is internal/supervisor's rule, including a settled task that still has an
// unacknowledged wake and a task that has none.
func TestDigestCountsTheResolutionAndNotTheEventItSuperseded(t *testing.T) {
	principles.Cite(t, principles.P8)
	principles.Cite(t, principles.P14)
	vocabtest.Control(t)

	held := machine.Task{
		Record: store.Task{ID: "digestholdaa"},
		State: taskstate.Resolution{
			State:  taskstate.Held,
			Source: taskstate.SourceHold,
			Basis:  taskstate.Basis{EventKind: "working"},
			Superseded: []taskstate.Superseded{{
				Kind: "working", Claimed: taskstate.Running, By: taskstate.SourceHold,
			}},
		},
	}
	unproven := machine.Task{
		Record: store.Task{ID: "digestworkaa"},
		State: taskstate.Resolution{
			State: taskstate.Running, Source: taskstate.SourceEvent, Progress: false,
		},
	}
	live := machine.Task{
		Record: store.Task{ID: "digestliveaa"},
		State: taskstate.Resolution{
			State: taskstate.Running, Source: taskstate.SourceProcess, Progress: true,
		},
	}
	unknown := machine.Task{
		Record: store.Task{ID: "digestunknown"},
		State: taskstate.Resolution{
			State: taskstate.Unknown, Source: taskstate.SourceProcess,
			Superseded: []taskstate.Superseded{{
				Kind: "working", Claimed: taskstate.Running, By: taskstate.SourceProcess,
			}},
		},
	}
	settled := machine.Task{
		Record: store.Task{ID: "digestsettle"},
		State:  taskstate.Resolution{State: taskstate.Complete, Source: taskstate.SourceRecord},
	}
	woken := machine.Task{
		Record: store.Task{ID: "digestwokena"},
		State:  taskstate.Resolution{State: taskstate.Failed, Source: taskstate.SourceEvent},
	}
	tasks := []machine.Task{held, unproven, live, unknown, settled, woken}
	wakes := map[string]int{"digestwokena": 1, "notintasksbb": 1}

	got := machine.DigestOf(tasks, wakes, nil, nil, 0)

	var running, unprovenCount, unknownCount int
	for _, c := range got.Counts {
		switch c.State {
		case taskstate.Running:
			running, unprovenCount = c.Count, c.Unproven
		case taskstate.Unknown:
			unknownCount = c.Count
		}
	}
	if running != 2 || unprovenCount != 1 || unknownCount != 1 {
		t.Fatalf("counts running=%d unproven=%d unknown=%d, want 2, 1, 1: %+v", running, unprovenCount, unknownCount, got.Counts)
	}
	sum := 0
	for _, c := range got.Counts {
		sum += c.Count
		if c.State != taskstate.Running && c.Unproven != 0 {
			t.Errorf("%s carries unproven %d, which only a running count does", c.State, c.Unproven)
		}
	}
	if sum != len(tasks) {
		t.Fatalf("counts sum to %d, want %d tasks", sum, len(tasks))
	}
	if len(got.Counts) < len(taskstate.States()) {
		t.Fatalf("counts has %d rows, want at least every state", len(got.Counts))
	}

	var ids []string
	proven := map[string]bool{}
	for _, item := range got.InFlight {
		ids = append(ids, item.Task)
		proven[item.Task] = item.Proven
		if item.Task == "digestworkaa" && (item.Proven || item.State != taskstate.Running) {
			t.Errorf("the unproven running task is %+v", item)
		}
		if item.Task == "digestunknown" && (item.State != taskstate.Unknown || item.Proven) {
			t.Errorf("the unknown task is %+v, which reads as running", item)
		}
		if item.Task == "notintasksbb" && (!item.Undecided || item.State != "") {
			t.Errorf("a wake with no listed task is %+v, want an undecided row with no invented state", item)
		}
	}
	if !slices.IsSorted(ids) {
		t.Fatalf("in flight is not in identifier order: %v", ids)
	}
	for _, task := range tasks {
		inFlight, decided := supervisor.TaskInFlight(task.State.State, wakes[task.Record.ID])
		listed := slices.Contains(ids, task.Record.ID)
		if decided && inFlight != listed {
			t.Errorf("%s in flight is %v under the supervisor's rule and %v in the digest", task.Record.ID, inFlight, listed)
		}
	}
	if !slices.Contains(ids, "notintasksbb") {
		t.Errorf("in flight is %v, without the wake whose task is not listed", ids)
	}
	if proven["digestliveaa"] != true || proven["digestworkaa"] != false {
		t.Fatalf("proven readings are %v, want the live task proven and the event-log one not", proven)
	}

	phrase := machine.DigestPhrase(got)
	vocabtest.Assert(t, phrase.String())
	if !slices.Contains(machine.TablePhrases(), machine.DigestPhrases()[0]) || !slices.Contains(machine.TablePhrases(), machine.DigestPhrases()[1]) {
		t.Fatal("the digest's two rows are not in the phrase table")
	}
}

// TestDigestBoundsOpenRecordsAndDropsWhatWasClosed is the digest's bound and
// its refusal to repeat a hold that has been resolved or a wake that has been
// acknowledged. Text on the way out is redacted, and the caller's records are
// left as they were. The digest carries no answer.
func TestDigestBoundsOpenRecordsAndDropsWhatWasClosed(t *testing.T) {
	principles.Cite(t, principles.P8)
	vocabtest.Control(t)

	var tasks []machine.Task
	for i := range machine.DigestLimit + 2 {
		tasks = append(tasks, machine.Task{
			Record: store.Task{ID: boundID(i)},
			State:  taskstate.Resolution{State: taskstate.Running, Progress: false},
		})
	}
	subject := "confirm https://pat:supersecret@example.invalid/a.git"
	detail := "see https://user:othersecret@example.invalid/b.git"
	open := store.Hold{Key: "open", Subject: subject, Detail: detail, OpenedAt: time.Unix(10, 0).UTC()}
	closed := store.Hold{
		Key: "closed", Subject: subject, OpenedAt: time.Unix(1, 0).UTC(),
		Resolution: store.Known("answered"),
	}
	holds := []store.Hold{closed, open}
	queued := store.Wake{ID: 1, TaskID: boundID(0), Kind: "hold-opened", Marker: "queued"}
	acked := store.Wake{
		ID: 2, TaskID: boundID(1), Kind: "hold-opened", Marker: "acked",
		HandledAt: store.Known(time.Unix(2, 0).UTC()),
	}
	wakes := []store.Wake{acked, queued}

	got := machine.DigestOf(tasks, nil, holds, wakes, 0)

	if holds[1].Subject != subject || holds[1].Detail != detail {
		t.Fatal("composing the digest rewrote the caller's hold")
	}
	if wakes[0].Marker != "acked" {
		t.Fatal("composing the digest rewrote the caller's wake")
	}
	if len(got.InFlight) != machine.DigestLimit || got.InFlightMore != 2 {
		t.Fatalf("in flight shows %d and %d more, want %d and 2", len(got.InFlight), got.InFlightMore, machine.DigestLimit)
	}
	for i, item := range got.InFlight {
		if item.Task != boundID(i) {
			t.Errorf("in flight[%d] is %s, want %s", i, item.Task, boundID(i))
		}
	}
	if len(got.Holds) != 1 || got.Holds[0].Key != "open" || got.HoldsMore != 0 {
		t.Fatalf("holds are %+v (%d more), want the open one", got.Holds, got.HoldsMore)
	}
	if got.Holds[0].Subject != "confirm https://"+redact.Marker+"@example.invalid/a.git" {
		t.Fatalf("hold subject is %q, want the credential removed", got.Holds[0].Subject)
	}
	if got.Holds[0].Detail != "see https://"+redact.Marker+"@example.invalid/b.git" {
		t.Fatalf("hold detail is %q, want the credential removed", got.Holds[0].Detail)
	}
	if len(got.Wakes) != 1 || got.Wakes[0].ID != 1 || got.WakesMore != 0 {
		t.Fatalf("wakes are %+v (%d more), want the unacknowledged one", got.Wakes, got.WakesMore)
	}

	var extra []store.Wake
	for i := int64(1); i <= int64(machine.DigestLimit)+3; i++ {
		extra = append(extra, store.Wake{ID: i, TaskID: boundID(0), Kind: "hold-opened"})
	}
	bounded := machine.DigestOf(nil, nil, nil, extra, len(extra)+4)
	if len(bounded.Wakes) != machine.DigestLimit || bounded.WakesMore != 4+3 {
		t.Fatalf("wakes show %d and %d more, want %d and %d", len(bounded.Wakes), bounded.WakesMore, machine.DigestLimit, 7)
	}

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"answer", "args", "effect"} {
		if _, ok := doc[key]; ok {
			t.Errorf("the digest carries %q, which would be a way to act on it", key)
		}
	}
	text := string(raw)
	if strings.Contains(text, "supersecret") || strings.Contains(text, "othersecret") {
		t.Fatalf("the digest still carries a credential: %s", text)
	}
	vocabtest.Assert(t, machine.DigestPhrase(got).String())
}

func boundID(i int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz234567"
	return "digestbnd" + string([]byte{alphabet[i], 'a', 'a'})
}
