package taskstate

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/store"
)

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// log builds a dense history from kinds, each event carrying text, the way the
// store numbers one: from one, with no gaps.
func log(text string, kinds ...string) []store.TaskEvent {
	out := make([]store.TaskEvent, len(kinds))
	for i, k := range kinds {
		out[i] = store.TaskEvent{TaskID: "t", Sequence: int64(i + 1), Kind: k, Detail: text, At: epoch}
	}
	return out
}

func runIn(id string, status store.RunStatus, age int) store.Run {
	return store.Run{ID: id, Status: status, CreatedAt: epoch.Add(time.Duration(age) * time.Minute), UpdatedAt: epoch}
}

func resolve(events []store.TaskEvent, runs []store.Run, p Liveness) Resolution {
	return Resolve(Inputs{Task: store.Task{ID: "t"}, Runs: runs, Events: events, Process: p})
}

// Every status a run can be in has a task state, so a status added to the
// store's vocabulary fails here rather than resolving every task on it to
// unknown. The control is a status outside the vocabulary, which must resolve
// to unknown from the run and not fall through to a log that says otherwise.
func TestEveryRunStatusHasATaskState(t *testing.T) {
	for _, status := range store.RunStatuses() {
		got := resolve(log("", "started"), []store.Run{runIn("r", status, 1)}, Unobserved)
		if got.Source != SourceRun || got.State == Unknown || !got.State.Recognized() {
			t.Errorf("a %s run resolved to %s from %s", status, got.State, got.Source)
		}
	}
	got := resolve(log("", "started"), []store.Run{runIn("r", "invented", 1)}, Unobserved)
	if got.State != Unknown || got.Source != SourceRun {
		t.Fatalf("a run in an undefined status resolved to %s from %s, want unknown from the run", got.State, got.Source)
	}
}

func TestRunStatusesMapToTheLifecycle(t *testing.T) {
	want := map[store.RunStatus]State{
		store.RunPending: Validating, store.RunRunning: Validating, store.RunHeld: Held,
		store.RunPassed: Ready, store.RunFailed: Failed, store.RunTerminated: Failed,
	}
	for status, state := range want {
		if got := resolve(nil, []store.Run{runIn("r", status, 1)}, Unobserved).State; got != state {
			t.Errorf("a %s run resolved to %s, want %s", status, got, state)
		}
	}
}

// The newest run answers, whatever order the runs arrive in.
func TestTheNewestRunAnswers(t *testing.T) {
	old, fresh := runIn("old", store.RunFailed, 1), runIn("new", store.RunRunning, 2)
	for _, runs := range [][]store.Run{{old, fresh}, {fresh, old}} {
		if got := resolve(nil, runs, Unobserved); got.State != Validating || got.Basis.RunID != "new" {
			t.Fatalf("resolved %s from run %s, want validating from the newest", got.State, got.Basis.RunID)
		}
	}
}

// A held run's detail is the subject of its open hold. A resolved hold is not
// open, and is the control that the detail comes from openness.
func TestAHeldRunReportsItsOpenHold(t *testing.T) {
	held := []store.Run{runIn("r", store.RunHeld, 1)}
	open := store.Hold{Key: "k", Subject: "approve the push", RunID: store.Known("r"), OpenedAt: epoch}
	closed := store.Hold{Key: "j", Subject: "an old question", RunID: store.Known("r"),
		OpenedAt: epoch.Add(-time.Hour), Resolution: store.Known("yes")}
	got := Resolve(Inputs{Task: store.Task{ID: "t"}, Runs: held, Holds: []store.Hold{closed, open}})
	if detail := got.Detail.Or(""); detail != "approve the push" {
		t.Fatalf("detail = %q, want the open hold's subject", detail)
	}
	got = Resolve(Inputs{Task: store.Task{ID: "t"}, Runs: held, Holds: []store.Hold{closed}})
	if detail := got.Detail.Or(""); strings.Contains(detail, "old question") {
		t.Fatalf("detail %q is a resolved hold's subject", detail)
	}
}

// Only the chosen run's own open hold supplies the detail: an older attributed
// run's hold, which is older and so would win a scan of every hold, and a hold
// naming only the task are both passed over. The control is the same hold
// naming the newest run, which is then chosen.
func TestAHeldRunsDetailIsItsOwnHoldsAndNoOthers(t *testing.T) {
	runs := []store.Run{runIn("old", store.RunHeld, 1), runIn("new", store.RunHeld, 2)}
	oldHold := store.Hold{Key: "a", Subject: "the old run's question", RunID: store.Known("old"), OpenedAt: epoch}
	taskHold := store.Hold{Key: "b", Subject: "a task question", TaskID: store.Known("t"), OpenedAt: epoch}
	newHold := store.Hold{Key: "c", Subject: "the new run's question", RunID: store.Known("new"), OpenedAt: epoch.Add(time.Hour)}

	got := Resolve(Inputs{Task: store.Task{ID: "t"}, Runs: runs, Holds: []store.Hold{oldHold, taskHold, newHold}})
	if d := got.Detail.Or(""); d != "the new run's question" {
		t.Fatalf("detail = %q, want the newest run's own hold", d)
	}
	got = Resolve(Inputs{Task: store.Task{ID: "t"}, Runs: runs, Holds: []store.Hold{oldHold, taskHold}})
	if d := got.Detail.Or(""); strings.Contains(d, "question") {
		t.Fatalf("detail %q names a hold that is not the chosen run's", d)
	}
}

// The process tier, and what it does and does not outrank.
func TestTheProcessTier(t *testing.T) {
	stale := log("", "started")
	finished := []store.Run{runIn("r", store.RunPassed, 1)}
	cases := []struct {
		name   string
		events []store.TaskEvent
		runs   []store.Run
		proc   Liveness
		state  State
		source Source
	}{
		{"live terminal, no run", log("", "queued"), nil, Live, Running, SourceProcess},
		{"dead terminal, stale started event", stale, nil, Dead, Unknown, SourceProcess},
		{"dead terminal, finished run: the run outranks", stale, finished, Dead, Ready, SourceRun},
		{"live terminal, finished run: the run outranks", stale, finished, Live, Ready, SourceRun},
		{"unobserved terminal falls to the log", stale, nil, Unobserved, Running, SourceEvent},
		{"an unknown liveness value is unobserved", stale, nil, Liveness("???"), Running, SourceEvent},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolve(c.events, c.runs, c.proc)
			if got.State != c.state || got.Source != c.source {
				t.Fatalf("resolved %s from %s, want %s from %s", got.State, got.Source, c.state, c.source)
			}
		})
	}
}

// Each recognized kind resolves to what its row says when it is the newest
// event and nothing outranks it; the roles that claim nothing, and a kind with
// no row, resolve to unknown from no source, never to the state before them.
func TestTheLogTierAnswersOnlyFromARecognizedState(t *testing.T) {
	for _, row := range Kinds() {
		got := resolve(log("", "started", row.Kind), nil, Unobserved)
		switch row.Role {
		case RoleState, RoleDecisionOpen:
			if !row.State.Recognized() || got.State != row.State || got.Source != SourceEvent {
				t.Errorf("kind %q resolved to %s from %s, want %s from the event", row.Kind, got.State, got.Source, row.State)
			}
		default:
			if got.State != Unknown || got.Source != SourceNone {
				t.Errorf("kind %q (%s) resolved to %s from %s, want unknown from none", row.Kind, row.Role, got.State, got.Source)
			}
		}
	}
	got := resolve(log("", "started", "mystery"), nil, Unobserved)
	if got.State != Unknown || got.Source != SourceNone {
		t.Fatalf("an unrecognized newest kind resolved to %s from %s, want unknown from none", got.State, got.Source)
	}
	if got := resolve(nil, nil, Unobserved); got.State != Unknown || got.Source != SourceNone {
		t.Fatalf("an empty log resolved to %s from %s", got.State, got.Source)
	}
}

func TestEveryEventKindIsDefinedOnce(t *testing.T) {
	seen := map[string]bool{}
	for _, row := range Kinds() {
		if seen[row.Kind] {
			t.Errorf("kind %q has two rows", row.Kind)
		}
		seen[row.Kind] = true
		if (row.Role == RoleState || row.Role == RoleDecisionOpen) != (row.State != "") {
			t.Errorf("kind %q: role %s and state %q disagree about whether it claims one", row.Kind, row.Role, row.State)
		}
	}
	if _, ok := Lookup(store.OpeningEventKind); !ok {
		t.Fatalf("the store's opening event kind %q has no row, so a new task resolves to nothing", store.OpeningEventKind)
	}
	if _, ok := Lookup("not-a-kind"); ok {
		t.Fatal("Lookup found a row for a kind nobody defined")
	}
}

// The text of an event never reaches the detail, whichever tier answers.
func TestEventTextNeverReachesTheDetail(t *testing.T) {
	const secret = "SENTINEL-worker-words"
	for _, kinds := range [][]string{
		{"started", "decision-open"}, {"started", "decision-closed"}, {"started", "progress-note"}, {"started", "mystery"}, {"started"},
	} {
		for _, runs := range [][]store.Run{nil, {runIn("r", store.RunPassed, 1)}} {
			if d := resolve(log(secret, kinds...), runs, Unobserved).Detail.Or(""); strings.Contains(d, secret) {
				t.Errorf("log %v, runs %d: detail %q carries an event's text", kinds, len(runs), d)
			}
		}
	}
}

// Superseded is read from the whole log: a decision closed since it opened is
// not superseded, one still open is, and the newest event is when it claims a
// state the answer differs from.
func TestSupersededReadsTheWholeLog(t *testing.T) {
	passed := []store.Run{runIn("r", store.RunPassed, 1)}
	seqs := func(r Resolution) []int64 {
		var out []int64
		for _, s := range r.Superseded {
			out = append(out, s.Sequence)
		}
		return out
	}
	cases := []struct {
		name  string
		kinds []string
		want  []int64
	}{
		{"open at the tail", []string{"started", "handed-off", "decision-open"}, []int64{3}},
		{"closed, then a note", []string{"decision-open", "decision-closed", "progress-note"}, nil},
		{"one closed, a later one open, then a note", []string{"decision-open", "decision-closed", "decision-open", "progress-note"}, []int64{3}},
		{"open, a note, still open", []string{"decision-open", "progress-note"}, []int64{1}},
		{"a state at the tail that differs", []string{"decision-open", "decision-closed", "started"}, []int64{3}},
		{"a report at the tail the run outranks", []string{"handed-off", "report-ready"}, []int64{2}},
		{"unrecognized kinds claim nothing", []string{"mystery", "decision-closed"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := seqs(resolve(log("", c.kinds...), passed, Unobserved))
			if !slices.Equal(got, c.want) {
				t.Fatalf("superseded = %v, want %v", got, c.want)
			}
		})
	}
	// A run whose state is the claim does not supersede it.
	failed := []store.Run{runIn("r", store.RunFailed, 1)}
	if got := resolve(log("", "started", "failed"), failed, Unobserved); len(got.Superseded) != 0 {
		t.Fatalf("a failed run superseded the failed event it agrees with: %+v", got.Superseded)
	}
	held := []store.Run{runIn("r", store.RunHeld, 1)}
	if got := resolve(log("", "decision-open"), held, Unobserved); len(got.Superseded) != 0 {
		t.Fatalf("a held run superseded the open decision it agrees with: %+v", got.Superseded)
	}
	// Nothing is superseded when the log itself decided.
	if got := resolve(log("", "decision-open"), nil, Unobserved); got.State != Held || got.Source != SourceEvent || got.Superseded != nil {
		t.Fatalf("the log's own answer was marked superseded: %+v", got)
	}
}

// Resolve is a function of what it is handed, not of the order it is handed it.
func TestResolveIsIndependentOfInputOrder(t *testing.T) {
	events := log("", "started", "decision-open", "decision-closed", "decision-open")
	runs := []store.Run{runIn("a", store.RunFailed, 1), runIn("b", store.RunPassed, 2)}
	want := resolve(events, runs, Unobserved)
	reversedEvents := slices.Clone(events)
	slices.Reverse(reversedEvents)
	reversedRuns := slices.Clone(runs)
	slices.Reverse(reversedRuns)
	if got := resolve(reversedEvents, reversedRuns, Unobserved); !reflect.DeepEqual(got, want) {
		t.Fatalf("reordering the inputs changed the answer:\n%+v\n%+v", got, want)
	}
	if events[0].Sequence != 1 {
		t.Fatal("Resolve reordered its caller's events")
	}
}

func TestTheStateSetIsClosed(t *testing.T) {
	for _, s := range States() {
		if !s.Recognized() {
			t.Errorf("%q is in States and not Recognized", s)
		}
	}
	if State("landed").Recognized() || State("complete").Recognized() {
		t.Fatal("a state this build cannot establish is declared")
	}
	got := States()
	got[0] = "tampered"
	if States()[0] == "tampered" {
		t.Fatal("States returned its own storage")
	}
}
