package cli_test

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// readyTask writes a run of the repository moved through to passed with no
// merge record beside it, and a delivery task the run is attributed to,
// which internal/taskstate resolves Ready from the run tier: validated and
// not merged.
func readyTask(t *testing.T, records *store.Store, repository, task, project string) {
	t.Helper()
	ctx := t.Context()
	runID := "run-" + task
	if _, err := records.CreateRun(ctx, store.Run{
		ID: runID, RepositoryID: repository, Branch: "ready/" + task, SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "ready it", IntentSource: "push", Build: catchUpBuild, ConfigDigest: "cfg",
	}); err != nil {
		t.Fatalf("recording run %s: %v", runID, err)
	}
	for _, step := range [][2]store.RunStatus{{store.RunPending, store.RunRunning}, {store.RunRunning, store.RunPassed}} {
		if _, err := records.TransitionRun(ctx, runID, []store.RunStatus{step[0]}, step[1]); err != nil {
			t.Fatalf("moving run %s to %s: %v", runID, step[1], err)
		}
	}
	if _, err := records.CreateTask(ctx, store.Task{
		ID: task, Shape: store.TaskDelivery, Project: project, Mode: "headless", WorktreePath: "/worktrees/" + task,
	}, store.Disposition{}); err != nil {
		t.Fatalf("recording task %s: %v", task, err)
	}
	if err := records.SetTaskRun(ctx, task, runID); err != nil {
		t.Fatalf("attributing run %s to task %s: %v", runID, task, err)
	}
}

// encodedDigest drives assistant --json watch --once and returns the read
// with the digest its document carries under digest, failing the test when
// the document carries none.
func encodedDigest(t *testing.T, h *home.Home, dir string, extra ...string) (machine.CatchUp, machine.Digest) {
	t.Helper()
	read, documents := catchUpOnce(t, h, dir, extra...)
	var carried struct {
		Digest *machine.Digest `json:"digest"`
	}
	if err := json.Unmarshal([]byte(documents[0]), &carried); err != nil {
		t.Fatal(err)
	}
	if carried.Digest == nil {
		t.Fatalf("the read carries no digest:\n%s", documents[0])
	}
	return read, *carried.Digest
}

// digestLines drives the person's assistant watch --once until two
// structured reads taken around it count the same, so the human read is
// known to be of that digest rather than of a moment the supervisor loop
// moved between, and returns its lines with that digest.
func digestLines(t *testing.T, h *home.Home, dir string) ([]string, machine.Digest) {
	t.Helper()
	for range 10 {
		_, before := encodedDigest(t, h, dir)
		plain := run(t, h, dir, "watch", "--once")
		if plain.code != machine.ExitOK {
			t.Fatalf("assistant watch --once exited %s:\n%s%s", plain.code, plain.stdout, plain.stderr)
		}
		if _, after := encodedDigest(t, h, dir); after == before {
			return strings.Split(strings.TrimRight(plain.stdout, "\n"), "\n"), before
		}
	}
	t.Fatal("the read never held still for two structured reads around a human one")
	return nil, machine.Digest{}
}

// placement is where PRD section 9's catching up puts a task of one state
// when nothing else about it is in play: under way is section 6's
// work-in-flight definition, stopped is Failed and Unknown, waiting is an
// open hold, and landed counts the records the terminal tier derived the
// state from. It is written from the PRD's rows rather than read off
// internal/supervisor's table, so the two can disagree and fail here.
type placement struct {
	waiting, underWay, stopped bool
	landed                     int
}

var digestOracle = map[taskstate.State]placement{
	taskstate.Queued:     {underWay: true},
	taskstate.Dispatched: {underWay: true},
	taskstate.Running:    {underWay: true},
	taskstate.Held:       {waiting: true},
	taskstate.Paused:     {underWay: true},
	taskstate.Validating: {underWay: true},
	taskstate.Ready:      {},
	taskstate.Landed:     {landed: 1},
	taskstate.Reported:   {},
	taskstate.Complete:   {landed: 2},
	taskstate.Failed:     {stopped: true},
	taskstate.Unknown:    {underWay: true, stopped: true},
}

// TestWatchOpensWithADigestOfEveryPartThroughTheSurface is the fleet digest
// through cli.Run against a real service, over a home holding a task in
// every state taskstate.States enumerates, built from the records that
// state resolves from, so a state added there fails here until it is
// placed. Two wakes are queued and one acknowledged before the service
// opens, because a wake is written under a supervisor incarnation and the
// test must not begin one under a live loop.
//
// What it establishes is that the digest is a composition of the read and
// nothing else. The counts the structured answer carries under digest are
// the read's own parts in counts, and each part holds what the oracle says
// for each state. They are counted from the resolved state, not from the
// newest history entry (P8): the held task's newest entry says its worker
// is at work, and it is counted as waiting on the person and not as under
// way. A queued wake keeps the failed task under way and the read hands it
// out to nobody; the acknowledged wake keeps nothing. The task whose worker
// said it is at work with no terminal observed is counted unconfirmed, and
// its line is the row for a worker not seen at its work, never the row for
// one seen at it. Marking what landed as seen takes it out of the next
// digest, and a read after that counts the same twice, so a repeated read
// does not bring back what was marked. Every line a person reads is held to
// P13's derived forbidden set and names no task or run (P13).
func TestWatchOpensWithADigestOfEveryPartThroughTheSurface(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P8)
	principles.Cite(t, principles.P13)
	vocabtest.Control(t)
	h := newHome(t)
	subject := newSubject(t)

	ids := make(map[taskstate.State]string, len(digestOracle))
	for i, state := range taskstate.States() {
		if _, placed := digestOracle[state]; !placed {
			t.Fatalf("%s has no placement in the oracle; place it from PRD section 9's rows", state)
		}
		ids[state] = "digesttaska" + string(rune('a'+i))
	}

	// The wakes, and the two tasks they are about, before the service.
	records := openStore(t, h)
	fleetTask(t, records, ids[taskstate.Failed], "the indexer", [2]string{"failed", "the build broke"})
	fleetTask(t, records, ids[taskstate.Reported], "the audit", [2]string{store.ReportReadyEventKind, "filed"})
	inc, err := records.BeginIncarnation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	queued, err := records.AppendWakeWithMarkers(t.Context(), inc, store.WakeRequest{
		TaskID: ids[taskstate.Failed], Kind: "hold-opened", Marker: "digest-queued",
	}, store.MarkerAdvance{})
	if err != nil {
		t.Fatal(err)
	}
	handled, err := records.AppendWakeWithMarkers(t.Context(), inc, store.WakeRequest{
		TaskID: ids[taskstate.Reported], Kind: "hold-opened", Marker: "digest-handled",
	}, store.MarkerAdvance{})
	if err != nil {
		t.Fatal(err)
	}
	if err := records.AcknowledgeWakes(t.Context(), []int64{handled.Wake.ID}, store.AcknowledgedByProgram()); err != nil {
		t.Fatal(err)
	}
	if err := records.Close(); err != nil {
		t.Fatal(err)
	}

	serve(t, h)
	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	records = openStore(t, h)
	t.Cleanup(func() { _ = records.Close() })
	repository, err := records.RepositoryAt(t.Context(), subject)
	if err != nil {
		t.Fatal(err)
	}
	fleetTask(t, records, ids[taskstate.Dispatched], "the launch", [2]string{store.DispatchedEventKind, "attempt 1"})
	// An independent task is dispatched as it is recorded, so the queued
	// one is queued behind the dispatched one, which is what keeps it there.
	if _, err := records.CreateTask(t.Context(), store.Task{
		ID: ids[taskstate.Queued], Shape: store.TaskInvestigation, Project: "the queue", Mode: "headless",
		WorktreePath: "/worktrees/" + ids[taskstate.Queued],
	}, store.Disposition{Blocker: store.Known(ids[taskstate.Dispatched])}); err != nil {
		t.Fatal(err)
	}
	fleetTask(t, records, ids[taskstate.Running], "the parser", [2]string{"working", "on it"})
	fleetTask(t, records, ids[taskstate.Held], "the question", [2]string{"working", "still on it"})
	if _, err := records.RegisterHold(t.Context(), store.Hold{
		Key: "task/" + ids[taskstate.Held] + "/ask", TaskID: store.Known(ids[taskstate.Held]), Subject: "whether to go on",
	}); err != nil {
		t.Fatal(err)
	}
	fleetTask(t, records, ids[taskstate.Paused], "the sign-off", [2]string{"waiting-external", "a sign-off"})
	fleetTask(t, records, ids[taskstate.Validating], "the gate", [2]string{"validation-started", "pushed"})
	readyTask(t, records, repository.ID, ids[taskstate.Ready], "the ready work")
	landTask(t, records, repository.ID, ids[taskstate.Landed], "the merged work", false)
	landTask(t, records, repository.ID, ids[taskstate.Complete], "the cleared work", true)
	fleetTask(t, records, ids[taskstate.Unknown], "the docs", [2]string{"relaunched", "attempt 2"})

	read, carried := encodedDigest(t, h, subject)
	// What the supervisor loop queued by the time of the read may keep any
	// task under way, which is the in-flight rule and not a placement, so
	// the wakes unacknowledged once the read is back excuse exactly that.
	excused, err := records.UnacknowledgedWakes(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if carried != read.Digest() {
		t.Fatalf("the document carries %+v and its parts count %+v", carried, read.Digest())
	}
	byID := map[string]machine.Task{}
	for _, task := range slices.Concat(read.UnderWay.Tasks, read.Stopped) {
		byID[task.Record.ID] = task
	}
	under, stopped := taskIDs(read.UnderWay.Tasks), taskIDs(read.Stopped)
	var waiting []string
	for _, w := range read.Waiting {
		if w.Task != nil {
			waiting = append(waiting, w.Task.Record.ID)
		}
	}
	landed := map[string]int{}
	for _, l := range read.Landed {
		landed[l.Task.Record.ID]++
	}
	for _, state := range taskstate.States() {
		id, want := ids[state], digestOracle[state]
		wantUnder := want.underWay || state == taskstate.Failed // the queued wake
		if got := slices.Contains(under, id); got != wantUnder && (!got || excused[id] == 0) {
			t.Errorf("%s (%s): under way %t, want %t", id, state, got, wantUnder)
		}
		if got := slices.Contains(stopped, id); got != want.stopped {
			t.Errorf("%s (%s): stopped %t, want %t", id, state, got, want.stopped)
		}
		if got := slices.Contains(waiting, id); got != want.waiting {
			t.Errorf("%s (%s): waiting %t, want %t", id, state, got, want.waiting)
		}
		if landed[id] != want.landed {
			t.Errorf("%s (%s): %d landed record(s), want %d", id, state, landed[id], want.landed)
		}
		if task, listed := byID[id]; listed && task.State.State != state {
			t.Errorf("%s resolves %s, want %s; the fixture did not build the state it names", id, task.State.State, state)
		}
	}
	if held := byID[ids[taskstate.Held]]; held.Record.ID != "" && excused[held.Record.ID] == 0 {
		t.Errorf("the held task, whose newest entry says its worker is at work, is listed as %s", held.State.State)
	}
	if read.MoreLanded != 0 || len(read.UnderWay.Runs) != 0 || len(read.Waiting) != 1 {
		t.Fatalf("the read holds %d more landed, %d runs and %d decisions, want 0, 0 and 1", read.MoreLanded, len(read.UnderWay.Runs), len(read.Waiting))
	}
	wantDigest := machine.Digest{Waiting: 1, Landed: 3, UnderWay: len(under), Unconfirmed: 1, Stopped: 2}
	if carried != wantDigest {
		t.Fatalf("the digest is %+v, want %+v", carried, wantDigest)
	}
	if running := byID[ids[taskstate.Running]]; running.State.State != taskstate.Running || running.State.Progress {
		t.Fatalf("the working task resolves %+v, want Running with no live terminal behind it", running.State)
	}

	// The read handed the queued wake to nobody and acknowledged nothing.
	wakes, err := records.QueuedWakes(t.Context(), 100)
	if err != nil {
		t.Fatal(err)
	}
	still := slices.IndexFunc(wakes, func(w store.Wake) bool { return w.ID == queued.Wake.ID })
	if still < 0 || wakes[still].HandedOut() {
		t.Fatalf("after the read the queued wake is %+v in %+v, want queued and handed out to nobody", still, wakes)
	}
	if slices.ContainsFunc(wakes, func(w store.Wake) bool { return w.ID == handled.Wake.ID }) {
		t.Fatalf("the acknowledged wake is queued again: %+v", wakes)
	}

	// The person's read: the digest's row first, the same counts.
	lines, shown := digestLines(t, h, subject)
	t.Logf("assistant watch --once:\n%s", strings.Join(lines, "\n"))
	for _, line := range lines {
		vocabtest.Assert(t, line)
	}
	if !slices.Contains(lines, machine.DigestPhrase(shown).String()) {
		t.Errorf("the human read does not carry the digest's row %q", machine.DigestPhrase(shown))
	}
	running := byID[ids[taskstate.Running]]
	atWork := machine.TaskPhrase(machine.Task{State: taskstate.Resolution{State: taskstate.Running, Source: taskstate.SourceProcess, Progress: true}})
	if want := "the parser: " + machine.TaskPhrase(running).String(); !slices.Contains(lines, want) {
		t.Errorf("the human read does not carry %q", want)
	}
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, atWork.Outcome) {
		t.Errorf("the human read says a worker is at its work, and no terminal was observed live:\n%s", joined)
	}
	for _, id := range slices.Sorted(maps.Values(ids)) {
		if strings.Contains(joined, id) {
			t.Errorf("the human read carries the identifier %s", id)
		}
	}

	// Marking what landed takes it out of the next digest, and a repeated
	// read does not bring it back.
	_, before := encodedDigest(t, h, subject, "--mark-seen")
	if before.Landed != 3 {
		t.Fatalf("the read that marked counted %d landed, want 3", before.Landed)
	}
	for range 2 {
		after, counted := encodedDigest(t, h, subject)
		if counted.Landed != 0 || len(after.Landed) != 0 {
			t.Fatalf("after the mark the digest counts %d landed and the read shows %d, want none", counted.Landed, len(after.Landed))
		}
		if counted.Waiting != 1 || counted.Stopped != 2 || counted.Unconfirmed != 1 {
			t.Fatalf("the mark moved a part it does not filter: %+v", counted)
		}
	}
}

// TestWatchOverAnEmptyHomeSaysItIsQuiet is the digest over a home with no
// work at all: every count is zero in the structured answer, and the
// person reads the quiet row and each part's empty row, every line held to
// P13's derived forbidden set. The control is the first test's read, whose
// digest is not the quiet row.
func TestWatchOverAnEmptyHomeSaysItIsQuiet(t *testing.T) {
	t.Parallel()
	vocabtest.Control(t)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)
	if _, carried := encodedDigest(t, h, subject); carried != (machine.Digest{}) {
		t.Fatalf("an empty home's digest is %+v, want zero", carried)
	}
	lines, shown := digestLines(t, h, subject)
	if shown != (machine.Digest{}) {
		t.Fatalf("an empty home's digest is %+v, want zero", shown)
	}
	if len(lines) == 0 || lines[0] != machine.DigestPhrase(machine.Digest{}).String() {
		t.Fatalf("an empty home's read opens with %q, want the quiet row", lines)
	}
	waiting, landed, underWay, stopped := machine.CatchUpPhrases(machine.CatchUp{})
	for _, row := range []machine.Phrase{waiting, landed, underWay, stopped} {
		if !slices.Contains(lines, row.String()) {
			t.Errorf("an empty home's read does not carry %q", row)
		}
	}
	for _, line := range lines {
		vocabtest.Assert(t, line)
	}
}
