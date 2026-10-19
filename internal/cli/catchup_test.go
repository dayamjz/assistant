package cli_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// catchUpBuild is a build identity a run record accepts.
var catchUpBuild = store.Build{Version: "v0.1.0", Revision: "0123456789abcdef", Go: "go1.25.0"}

// landTask writes the records a merge the code host reported leaves: a run of
// the repository moved through to passed by a pull request read as merged,
// which writes its merge record in the same transaction, and a delivery task
// the run is attributed to. With cleared, the task's copy is then recorded as
// removed on a proof, which is what internal/taskstate resolves Complete
// from. These are written straight to the store because the code host's read
// and the cleanup decision are other tests' subjects; what is under test here
// is what the catch-up read makes of the records once they exist.
func landTask(t *testing.T, records *store.Store, repository, task, project string, cleared bool) {
	t.Helper()
	ctx := t.Context()
	runID := "run-" + task
	if _, err := records.CreateRun(ctx, store.Run{
		ID: runID, RepositoryID: repository, Branch: "landed/" + task, SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "land it", IntentSource: "push", Build: catchUpBuild, ConfigDigest: "cfg",
	}); err != nil {
		t.Fatalf("recording run %s: %v", runID, err)
	}
	if _, err := records.TransitionRun(ctx, runID, []store.RunStatus{store.RunPending}, store.RunRunning); err != nil {
		t.Fatalf("starting run %s: %v", runID, err)
	}
	if _, err := records.TransitionRunOnPullRequest(ctx, runID, []store.RunStatus{store.RunRunning}, store.RunPassed,
		store.PullRequestRead{PullRequest: 7, Head: "0123abc", End: store.PullRequestMerged}, store.HoldClosure{}); err != nil {
		t.Fatalf("ending run %s on a merge: %v", runID, err)
	}
	if _, err := records.CreateTask(ctx, store.Task{
		ID: task, Shape: store.TaskDelivery, Project: project, Mode: "headless", WorktreePath: "/worktrees/" + task,
	}, store.Disposition{}); err != nil {
		t.Fatalf("recording task %s: %v", task, err)
	}
	if err := records.SetTaskRun(ctx, task, runID); err != nil {
		t.Fatalf("attributing run %s to task %s: %v", runID, task, err)
	}
	if !cleared {
		return
	}
	if _, err := records.RecordTaskCleanup(ctx, store.TaskCleanup{TaskID: task, Proof: store.CleanupProofMergedPullRequest}, store.TaskDelivery); err != nil {
		t.Fatalf("recording the cleanup of %s: %v", task, err)
	}
	if err := records.MarkCleanupRemoved(ctx, task, time.Now().UTC()); err != nil {
		t.Fatalf("marking the removal of %s: %v", task, err)
	}
}

// fleetTask records an investigation task, independent of every other, and
// appends the history entries given, each a kind and its detail.
func fleetTask(t *testing.T, records *store.Store, id, project string, events ...[2]string) {
	t.Helper()
	if _, err := records.CreateTask(t.Context(), store.Task{
		ID: id, Shape: store.TaskInvestigation, Project: project, Mode: "headless", WorktreePath: "/worktrees/" + id,
	}, store.Disposition{}); err != nil {
		t.Fatalf("recording task %s: %v", id, err)
	}
	for _, e := range events {
		if _, err := records.AppendTaskEvent(t.Context(), id, e[0], e[1]); err != nil {
			t.Fatalf("appending %s to %s: %v", e[0], id, err)
		}
	}
}

// catchUpOnce drives assistant --json watch --once and decodes its one
// document, failing the test unless the command printed exactly that and
// returned on its own.
func catchUpOnce(t *testing.T, h *home.Home, dir string, extra ...string) (machine.CatchUp, []string) {
	t.Helper()
	got := run(t, h, dir, append([]string{"--json", "watch", "--once"}, extra...)...)
	if got.code != machine.ExitOK {
		t.Fatalf("assistant watch --once exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	documents := strings.Split(strings.TrimRight(got.stdout, "\n"), "\n")
	var read machine.CatchUp
	if err := json.Unmarshal([]byte(documents[0]), &read); err != nil {
		t.Fatalf("the first document is not the catch-up read: %v\n%s", err, got.stdout)
	}
	return read, documents
}

// taskIDs is the identifiers of tasks, in order.
func taskIDs(tasks []machine.Task) []string {
	out := make([]string, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, task.Record.ID)
	}
	return out
}

// TestWatchCatchesUpOnEveryPartThroughTheSurface drives PRD section 9's
// catching up through cli.Run against a real service, over a home whose
// records put something in every part: a run held at a decision its own
// stage raised and a hold an investigation's report would register, landed
// records past the read's bound, work under way, and tasks that resolve
// Failed and Unknown.
//
// What it establishes, part by part, is that each is read from the record
// that owns its fact (P8): the held run is the run the start answered, with
// its checkpoint's decision and the answer path from its own checkout; the
// hold carries the task it names and the task-given answer; the landed part
// is the merge and cleanup records the terminal tier derived Landed and
// Complete from, newest first, bounded with the rest counted; under way is
// every task the in-flight rule counts; stopped is every task resolved
// Failed or Unknown. A read with --once marks nothing and returns. And the
// person's rendering is the same model (P14): every task and run the
// document lists is on a line of the human read with its own phrase, and
// every line is held to P13's derived forbidden set (P13).
func TestWatchCatchesUpOnEveryPartThroughTheSurface(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P8)
	principles.Cite(t, principles.P13)
	principles.Cite(t, principles.P14)
	vocabtest.Control(t)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)
	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	standingOnChange(t, subject, "a-change")
	held := decodeRun(t, run(t, h, subject, "--json", "--intent", "a change held for a decision").stdout)
	if held.Outcome != machine.OutcomeDecision || held.Decision == nil {
		t.Fatalf("the run did not stop at a decision: %+v", held)
	}

	records := openStore(t, h)
	t.Cleanup(func() { _ = records.Close() })
	repository, err := records.RepositoryAt(t.Context(), subject)
	if err != nil {
		t.Fatal(err)
	}
	fleetTask(t, records, "reportheldaa", "the audit")
	if _, err := records.RegisterHold(t.Context(), store.Hold{
		Key: "task/reportheldaa/report/1", TaskID: store.Known("reportheldaa"), Subject: "whether the audit's finding is real",
	}); err != nil {
		t.Fatal(err)
	}
	fleetTask(t, records, "workingtaska", "the parser", [2]string{"working", "on it"})
	fleetTask(t, records, "failedtaskaa", "the indexer", [2]string{"failed", "the build broke"})
	fleetTask(t, records, "relaunchedaa", "the docs", [2]string{"relaunched", "attempt 2 in the same copy"})
	const landedCount = machine.LandedLimit + 2
	for i := range landedCount {
		landTask(t, records, repository.ID, "landedtasks"+string(rune('a'+i)), fmt.Sprintf("merged work %d", i), false)
	}
	landTask(t, records, repository.ID, "clearedtasks", "the cleared work", true)

	read, documents := catchUpOnce(t, h, subject)
	if len(documents) != 1 {
		t.Fatalf("watch --once wrote %d documents, want the read alone:\n%s", len(documents), strings.Join(documents, "\n"))
	}

	// Waiting on you: the held run first, because it held before the hold
	// was registered, then the hold, each with the answer that closes it.
	if len(read.Waiting) != 2 {
		t.Fatalf("waiting on you lists %d decisions, want the held run and the hold: %+v", len(read.Waiting), read.Waiting)
	}
	first, second := read.Waiting[0], read.Waiting[1]
	if first.Run == nil || first.Run.Record.ID != held.Record.ID || first.Run.Decision == nil ||
		!slices.Equal(first.Run.Decision.Options, held.Decision.Options) {
		t.Fatalf("the first decision is %+v, want the held run with its checkpoint's options", first)
	}
	if want := machine.RunAnswering(subject, "a-change"); !slices.Equal(first.Answer.Args, want.Args) ||
		first.Answer.WorkingPath != want.WorkingPath || first.Answer.Branch != want.Branch {
		t.Fatalf("the held run is answered with %+v, want %+v", first.Answer, want)
	}
	if second.Hold == nil || second.Hold.Key != "task/reportheldaa/report/1" || second.Task == nil || second.Task.Record.ID != "reportheldaa" {
		t.Fatalf("the second decision is %+v, want the hold with the task it names", second)
	}
	if !slices.Equal(second.Answer.Args, machine.TaskAnswering("reportheldaa").Args) {
		t.Fatalf("the hold is answered with %+v, want the task-given answer", second.Answer)
	}

	// Landed: the bound's worth, newest first, and the rest counted.
	marks, err := records.SeenMarks(t.Context())
	if err != nil || len(marks) != 0 {
		t.Fatalf("a read marked %+v (%v); a read alone marks nothing", marks, err)
	}
	unseen := landedCount + 2 // every merge, the cleared task's merge, and its cleanup
	if len(read.Landed) != machine.LandedLimit || read.MoreLanded != unseen-machine.LandedLimit {
		t.Fatalf("landed shows %d and counts %d more, want %d and %d", len(read.Landed), read.MoreLanded,
			machine.LandedLimit, unseen-machine.LandedLimit)
	}
	for i := 1; i < len(read.Landed); i++ {
		if read.Landed[i].At().After(read.Landed[i-1].At()) {
			t.Fatalf("landed is not newest first at %d: %v after %v", i, read.Landed[i].At(), read.Landed[i-1].At())
		}
	}
	if top := read.Landed[0]; top.Task.Record.ID != "clearedtasks" || top.Cleanup == nil || top.Task.State.State != taskstate.Complete {
		t.Fatalf("the newest landed record is %+v, want the cleared task's cleanup record", top)
	}
	for _, l := range read.Landed {
		if l.Merge != nil && (l.Merge.End != store.PullRequestMerged || l.Merge.Run != "run-"+l.Task.Record.ID) {
			t.Fatalf("a landed merge record is %+v, not the merge of task %s's run", l.Merge, l.Task.Record.ID)
		}
	}

	// Under way and stopped, never filtered.
	under := taskIDs(read.UnderWay.Tasks)
	for _, id := range []string{"workingtaska", "relaunchedaa"} {
		if !slices.Contains(under, id) {
			t.Errorf("under way lists %v, without %s", under, id)
		}
	}
	stopped := taskIDs(read.Stopped)
	if !slices.Contains(stopped, "failedtaskaa") || !slices.Contains(stopped, "relaunchedaa") || slices.Contains(stopped, "workingtaska") {
		t.Errorf("stopped without a decision lists %v, want the failed and the unknown task and not the working one", stopped)
	}
	for _, task := range read.Stopped {
		if s := task.State.State; s != taskstate.Failed && s != taskstate.Unknown {
			t.Errorf("stopped lists task %s, which resolves %s", task.Record.ID, s)
		}
	}

	// The person's rendering of the same read.
	plain := run(t, h, subject, "watch", "--once")
	if plain.code != machine.ExitOK {
		t.Fatalf("assistant watch --once exited %s:\n%s%s", plain.code, plain.stdout, plain.stderr)
	}
	t.Logf("assistant watch --once:\n%s", plain.stdout)
	for _, line := range strings.Split(strings.TrimRight(plain.stdout, "\n"), "\n") {
		vocabtest.Assert(t, line)
	}
	for _, want := range []string{
		"a-change: " + machine.DecisionPhrase(*held.Decision).String(),
		"the audit: " + machine.HoldPhrase(*second.Hold).String(),
		machine.LandedMorePhrase(read.MoreLanded).String(),
		"the cleared work: " + machine.LandedPhrase(read.Landed[0]).String(),
	} {
		if !strings.Contains(plain.stdout, want+"\n") {
			t.Errorf("the human read does not carry %q", want)
		}
	}
	for _, task := range slices.Concat(read.UnderWay.Tasks, read.Stopped) {
		if want := task.Record.Project + ": " + machine.TaskPhrase(task).String(); !strings.Contains(plain.stdout, want+"\n") {
			t.Errorf("the human read does not carry the line for task %s: %q", task.Record.ID, want)
		}
	}
	for _, id := range []string{held.Record.ID, "reportheldaa", "clearedtasks", "run-clearedtasks"} {
		if strings.Contains(plain.stdout, id) {
			t.Errorf("the human read carries the identifier %s", id)
		}
	}
}

// TestTheSeenMarkMovesOnlyOnAnExplicitMarkOfWhatWasShown is section 9's rule
// for the one filtered part, through cli.Run and the protocol against a real
// service. A read alone marks nothing, however often it is taken; the mark
// names exactly the records the read showed, so the records past the bound
// stay unseen and come into the next read, and a record that lands between a
// read and its mark is not named by the mark and is still shown. The mark's
// record is a set of keys and never a time (P8): the record that landed
// after the read is newer than every mark and is shown because it has no
// row, which a time-based filter would read the same way, so the converse is
// checked too - a record older than the mark that the mark did not name is
// still shown. The positive control is the mark itself: what it named
// disappears from the next read, so the assertions that a read changes
// nothing are not passed by a mark that does nothing either.
func TestTheSeenMarkMovesOnlyOnAnExplicitMarkOfWhatWasShown(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P8)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)
	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	records := openStore(t, h)
	t.Cleanup(func() { _ = records.Close() })
	repository, err := records.RepositoryAt(t.Context(), subject)
	if err != nil {
		t.Fatal(err)
	}
	fleetTask(t, records, "workingtaska", "the parser", [2]string{"working", "on it"})
	for i := range machine.LandedLimit + 2 {
		landTask(t, records, repository.ID, "landedtasks"+string(rune('a'+i)), fmt.Sprintf("merged work %d", i), false)
	}

	before, _ := catchUpOnce(t, h, subject)
	again, _ := catchUpOnce(t, h, subject)
	if !slices.Equal(before.Shown(), again.Shown()) || again.MoreLanded != 2 {
		t.Fatalf("a second read shows %v and %d more, want what the first showed, %v and 2 more",
			again.Shown(), again.MoreLanded, before.Shown())
	}
	if marks, err := records.SeenMarks(t.Context()); err != nil || len(marks) != 0 {
		t.Fatalf("two reads left the seen mark at %+v (%v), want nothing", marks, err)
	}

	// Marking through the surface names exactly what that read showed.
	marking, documents := catchUpOnce(t, h, subject, "--mark-seen")
	if len(documents) != 2 {
		t.Fatalf("watch --once --mark-seen wrote %d documents, want the read and the mark", len(documents))
	}
	var marked machine.Marked
	if err := json.Unmarshal([]byte(documents[1]), &marked); err != nil {
		t.Fatalf("the second document is not the mark: %v\n%s", err, documents[1])
	}
	var named []store.SeenRecord
	for _, m := range marked.Marks {
		named = append(named, m.SeenRecord)
	}
	if !slices.Equal(named, marking.Shown()) || len(named) != machine.LandedLimit {
		t.Fatalf("the mark named %v, want exactly the %d the read showed: %v", named, machine.LandedLimit, marking.Shown())
	}
	rest, _ := catchUpOnce(t, h, subject)
	if len(rest.Landed) != 2 || rest.MoreLanded != 0 {
		t.Fatalf("after the mark the read shows %d and %d more, want the 2 the bound held back", len(rest.Landed), rest.MoreLanded)
	}
	for _, l := range rest.Landed {
		if slices.Contains(named, l.Record()) {
			t.Fatalf("a marked record is still shown: %+v", l.Record())
		}
	}
	if len(rest.UnderWay.Tasks) == 0 || len(again.UnderWay.Tasks) != len(rest.UnderWay.Tasks) {
		t.Fatalf("the mark changed what is under way: %v, then %v", taskIDs(again.UnderWay.Tasks), taskIDs(rest.UnderWay.Tasks))
	}

	// A record that lands between a read and its mark is not named by it.
	client, err := ipc.Dial(t.Context(), h.Socket(), ipc.ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	var read machine.CatchUp
	if err := client.Call(t.Context(), ipc.MethodCatchUpRead, nil, &read); err != nil {
		t.Fatal(err)
	}
	landTask(t, records, repository.ID, "latelandedaa", "the late work", false)
	var lateMark machine.Marked
	if err := client.Call(t.Context(), ipc.MethodCatchUpMark, machine.MarkSeenRequest{Records: read.Shown()}, &lateMark); err != nil {
		t.Fatalf("marking what the read showed: %v", err)
	}
	after, _ := catchUpOnce(t, h, subject)
	late := store.SeenRecord{Kind: store.SeenMerge, Key: "run-latelandedaa"}
	if !slices.Equal(after.Shown(), []store.SeenRecord{late}) {
		t.Fatalf("after marking what the read showed, the next read shows %v, want only the record that landed after it", after.Shown())
	}

	// An older record the mark did not name stays unseen whatever the
	// mark's time: a record landed, a mark made of something else, and the
	// older record still shown.
	landTask(t, records, repository.ID, "olderlandeda", "the older work", false)
	landTask(t, records, repository.ID, "newerlandeda", "the newer work", false)
	newer := store.SeenRecord{Kind: store.SeenMerge, Key: "run-newerlandeda"}
	if err := client.Call(t.Context(), ipc.MethodCatchUpMark, machine.MarkSeenRequest{Records: []store.SeenRecord{newer}}, &lateMark); err != nil {
		t.Fatal(err)
	}
	last, _ := catchUpOnce(t, h, subject)
	older := store.SeenRecord{Kind: store.SeenMerge, Key: "run-olderlandeda"}
	if !slices.Contains(last.Shown(), older) || slices.Contains(last.Shown(), newer) {
		t.Fatalf("after marking only the newer record the read shows %v, want the older one and not the newer", last.Shown())
	}

	// A mark naming what is not landed is refused and writes nothing.
	err = client.Call(t.Context(), ipc.MethodCatchUpMark, machine.MarkSeenRequest{
		Records: []store.SeenRecord{older, {Kind: store.SeenMerge, Key: "run-nowhere"}}}, &lateMark)
	if !errors.Is(err, ipc.ErrInvalidRequest) {
		t.Fatalf("a mark naming a record that never landed = %v, want ErrInvalidRequest", err)
	}
	if still, _ := catchUpOnce(t, h, subject); !slices.Contains(still.Shown(), older) {
		t.Fatalf("a refused mark wrote the record beside the refused one: %v", still.Shown())
	}
}

// TestARunBeingAdvancedIsUnderWay is the under-way part's other half: a run a
// segment is advancing right now, held inside a stage body so the read
// overlaps it for certain, is listed under way as the run's own answer with
// its advancing fact, and the human read says it is being validated now on
// its branch. The control is the same run once the body is let go and the
// run stops at its decision: it is no longer under way and is waiting on the
// person instead.
func TestARunBeingAdvancedIsUnderWay(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P8)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	inside := make(chan struct{})
	release := make(chan struct{})
	var entered, released sync.Once
	let := func() { released.Do(func() { close(release) }) }
	serveHeldAtIntent(t, h, inside, release, &entered)
	t.Cleanup(let)
	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	standingOnChange(t, subject, "a-change")
	started := startInBackground(t, h, subject, "--json", "--intent", "a change being validated")
	select {
	case <-inside:
	case <-time.After(30 * time.Second):
		t.Fatal("the start never reached the stage body")
	}

	read, _ := catchUpOnce(t, h, subject)
	if len(read.UnderWay.Runs) != 1 || !read.UnderWay.Runs[0].Advancing || read.UnderWay.Runs[0].Record.Branch != "a-change" {
		t.Fatalf("under way lists runs %+v, want the one being advanced", read.UnderWay.Runs)
	}
	plain := run(t, h, subject, "watch", "--once")
	if want := "a-change: " + read.UnderWay.Runs[0].Phrase().String(); !strings.Contains(plain.stdout, want+"\n") {
		t.Fatalf("the human read does not carry %q:\n%s", want, plain.stdout)
	}

	let()
	var answered invocation
	select {
	case answered = <-started:
	case <-time.After(60 * time.Second):
		t.Fatal("the start did not answer once the body was let go")
	}
	if decodeRun(t, answered.stdout).Outcome != machine.OutcomeDecision {
		t.Fatalf("the run did not stop at its decision:\n%s", answered.stdout)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	for {
		after, _ := catchUpOnce(t, h, subject)
		if len(after.UnderWay.Runs) == 0 && len(after.Waiting) == 1 && after.Waiting[0].Run != nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("once it stopped, the run reads under way %+v and waiting %+v", after.UnderWay.Runs, after.Waiting)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
