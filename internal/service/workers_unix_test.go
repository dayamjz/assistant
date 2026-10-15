//go:build unix

package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
)

// These tests make requests the way a real worker makes them: the worker
// runs them. The worker is the stand-in in worker mode, launched by the
// service for a dispatched task exactly as launch_test.go has it launched,
// and a request from it is a command the test sends the worker as a line,
// which the worker runs through a shell of its own, in a process group the
// worker does not lead. That is the shape #225 observed of a command the
// Claude Code CLI's Bash tool runs, and every helper here fails the test if
// the command did not run in it. The request is a git push from the task's
// copy, whose hooks reach the service through the gate as a person's would,
// or this binary making one call. The credentials the service reads are
// then those of a process the worker started, which the kernel attributes
// and nothing here states, and the service places it by walking that
// process's parent chain to the worker. A membership written by hand
// reaches nothing in any of them.

// shellCalls numbers the commands sent to workers in this binary, so each
// command's answer can be told from every other's however alike the
// commands are.
var shellCalls atomic.Int64

// asWorker has the launched task's worker run command and returns what the
// worker reported of it. The test fails if the command ran in the worker's
// own process group, or in none the worker could read, since a request from
// there is not the shape these tests exist to exercise.
func asWorker(t *testing.T, w launched, command string) standin.Ran {
	t.Helper()
	command = fmt.Sprintf(": call %d; %s", shellCalls.Add(1), command)
	if err := w.provider.Send(t.Context(), w.session, shellMarker+command); err != nil {
		t.Fatalf("sending the worker a command: %v", err)
	}
	deadline := time.Now().Add(90 * time.Second)
	for {
		got, err := w.provider.Capture(w.session)
		if err != nil {
			t.Fatalf("Capture: %v", err)
		}
		for _, line := range strings.Split(got.Text, "\n") {
			// A line-mode worker prints the Ran as a line of its own, and a
			// stream-mode worker as the text of a result line.
			text := line
			var turn struct {
				Type   string `json:"type"`
				Result string `json:"result"`
			}
			if json.Unmarshal([]byte(line), &turn) == nil && turn.Type == "result" {
				text = turn.Result
			}
			var ran standin.Ran
			if json.Unmarshal([]byte(text), &ran) != nil || ran.Command != command {
				continue
			}
			if ran.Group <= 0 || ran.Group == w.group {
				t.Fatalf("the worker ran %q in group %d, and its own group is %d: not the shape a real worker's command has",
					command, ran.Group, w.group)
			}
			return ran
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the worker never answered %q; its output is:\n%s", command, got.Text)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// shellQuote quotes each word for a POSIX shell and joins them.
func shellQuote(words ...string) string {
	quoted := make([]string, len(words))
	for i, word := range words {
		quoted[i] = "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}

// gitAs has the worker run git in dir and returns its output and an error
// when git failed, for a push whose admission is the point. The identity a
// commit needs is stated on the command line, since a CI runner has none.
func gitAs(t *testing.T, w launched, dir string, args ...string) (string, error) {
	t.Helper()
	ran := asWorker(t, w, "cd "+shellQuote(dir)+" && "+
		"GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.invalid "+
		"GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.invalid "+
		shellQuote(append([]string{"git"}, args...)...))
	out := ran.Stdout + ran.Stderr
	if ran.Exit != 0 {
		return out, fmt.Errorf("git exited with status %d", ran.Exit)
	}
	return out, nil
}

// callAs has the worker make one call on the service's socket through
// callMain, and returns the answer or the wire code the call was refused
// with. The parameters reach the command through a file rather than on the
// line the worker reads, so a test counting the lines that carry a text
// counts what the program wrote to the worker and not what it asked the
// worker to run.
func callAs(t *testing.T, w launched, method ipc.Method, params any) (json.RawMessage, ipc.Code) {
	t.Helper()
	body, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "params.json")
	if err := os.WriteFile(file, body, 0o600); err != nil {
		t.Fatal(err)
	}
	ran := asWorker(t, w, shellQuote(testCommand(t), callVerb, w.running.service.Socket(), string(method))+
		` "$(cat `+shellQuote(file)+`)"`)
	switch ran.Exit {
	case 0:
		return json.RawMessage(ran.Stdout), ""
	case 1:
		return nil, ipc.Code(ran.Stderr)
	default:
		t.Fatalf("calling %s from the worker: %+v", method, ran)
		return nil, ""
	}
}

// launchedWorker records and dispatches a delivery task on branch, serves a
// worker-capable service for it, and hands the body the running service, the
// task and the group its worker leads. The home's configuration puts the
// beacon interval at its floor so the loop passes, and arm verification
// waits, in seconds.
func launchedWorker(t *testing.T, branch string, body func(w launched)) {
	t.Helper()
	launchedWorkerOf(t, store.TaskDelivery, branch, body)
}

// launchedWorkerOf is launchedWorker for a task of either shape; an
// investigation task names no branch, so branch is empty for one.
func launchedWorkerOf(t *testing.T, shape store.TaskShape, branch string, body func(w launched)) {
	t.Helper()
	launchedWorkerUnder(t, shape, branch, store.Unknown[config.Delivery](), body)
}

// launchedWorkerUnder is launchedWorkerOf for a task recorded under a
// delivery posture, as intake records one.
func launchedWorkerUnder(t *testing.T, shape store.TaskShape, branch string, posture store.Optional[config.Delivery],
	body func(w launched)) {
	t.Helper()
	launchedWorkerConfigured(t, shape, branch, posture, `{"supervision_beacon_interval": "1s"}`, body)
}

// launchedWorkerConfigured is launchedWorkerUnder with the home's global
// configuration document stated, so a test can launch the worker under
// another session provider and nothing else about the launch differs.
func launchedWorkerConfigured(t *testing.T, shape store.TaskShape, branch string, posture store.Optional[config.Delivery],
	global string, body func(w launched)) {
	t.Helper()
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	quietMaintenance(t, subject)
	repository := recordRepository(t, h, subject)
	quietMaintenance(t, gateRepositoryOf(t, h, subject))
	if err := os.WriteFile(h.ConfigFile(), []byte(global+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := recordDispatchedTaskUnder(t, h, repository, shape, branch, posture)
	gateRepo := gateRepositoryOf(t, h, subject)
	o, _ := workerOptions(t, h)
	withServiceOptions(t, o, func(running serviceUnderTest) {
		provider, session := launchedSession(t, running.service, id)
		waitCapture(t, provider, session, "worker ready")
		body(launched{running: running, home: h, repository: repository, gateRepo: gateRepo, task: id, group: session.Reference().PID,
			provider: provider, session: session})
	})
}

// launched is what launchedWorker hands its body: the running service, the
// home, the repository and gate it serves, the task, the process group the
// task's worker leads, and the provider and session the service holds for
// it, so a body can end the worker through the same handle the service
// observes it through.
type launched struct {
	running    serviceUnderTest
	home       *home.Home
	repository store.Repository
	gateRepo   string
	task       string
	group      int
	provider   session.Provider
	session    *session.Session
}

// A worker's push is admitted to its own branch and refused everywhere
// else, decided from the pushing process's credentials and never from the
// push: the same git, from the same copy, run by the worker in a process
// group of its own, creates the task's branch and is admitted while a stage is registered, so
// containment was asked and did not place it; pushes the branch under
// another name, deletes it, and rewinds it, and each is refused before any
// reference moves; and a person's push of the same branch, from outside
// every worker group, is held to nothing new, rewinding it included. The
// gate's references are read with git after each push rather than taken from
// the answer.
func TestAWorkersPushIsHeldToItsOwnBranchForwardAndAPersonsIsNot(t *testing.T) {
	launchedWorker(t, "task/one", func(w launched) {
		running, task, gateRepo := w.running, w.task, w.gateRepo
		h := running.service
		copyPath := resolvedCopy(t, w.home, task)
		quietMaintenance(t, copyPath)
		if err := os.WriteFile(filepath.Join(copyPath, "work.txt"), []byte("the worker's change\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		mustGitAs(t, w, copyPath, "add", "-A")
		mustGitAs(t, w, copyPath, "commit", "--quiet", "-m", "the worker's change")
		head := mustGitAs(t, w, copyPath, "rev-parse", "HEAD")

		// A stage is registered, so admission's containment check reads the
		// pusher's group rather than answering no without looking, and the
		// worker's group is not the one registered.
		forget := h.StageStarted("run-under-validation", "review", 424242)
		defer forget()

		out, err := gitAs(t, w, copyPath, "push", gate.RemoteName, "task/one")
		if err != nil {
			t.Fatalf("the worker's push of its own branch was refused:\n%s", out)
		}
		if strings.Contains(out, string(ipc.CodeContained)) {
			t.Fatalf("the worker's push met containment:\n%s", out)
		}
		if !strings.Contains(gateRefs(t, gateRepo), "refs/heads/task/one "+head) {
			t.Fatalf("the admitted push did not move the gate's branch to %s:\n%s", head, gateRefs(t, gateRepo))
		}
		before := gateRefs(t, gateRepo)

		refused := []struct {
			name string
			args []string
			why  string
		}{
			{"a push outside its branch", []string{"push", gate.RemoteName, "task/one:refs/heads/other"}, "is not that branch"},
			{"a deletion", []string{"push", gate.RemoteName, ":task/one"}, "deletes it"},
		}
		for _, push := range refused {
			out, err := gitAs(t, w, copyPath, push.args...)
			if err == nil {
				t.Fatalf("%s by the worker was admitted:\n%s", push.name, out)
			}
			if !strings.Contains(out, push.why) {
				t.Fatalf("%s by the worker was refused without saying why:\n%s", push.name, out)
			}
			if got := gateRefs(t, gateRepo); got != before {
				t.Fatalf("%s changed the gate's references:\nbefore: %s\nafter:  %s", push.name, before, got)
			}
		}
		mustGitAs(t, w, copyPath, "commit", "--quiet", "--amend", "-m", "the worker's change, rewritten")
		out, err = gitAs(t, w, copyPath, "push", "--force", gate.RemoteName, "task/one")
		if err == nil {
			t.Fatalf("a rewind by the worker was admitted:\n%s", out)
		}
		if !strings.Contains(out, "does not descend") {
			t.Fatalf("the rewind was refused without saying why:\n%s", out)
		}
		if got := gateRefs(t, gateRepo); got != before {
			t.Fatalf("the refused rewind changed the gate's references:\nbefore: %s\nafter:  %s", before, got)
		}

		// A person, pushing from the primary checkout outside every worker
		// group, rewinds the same branch and is admitted: the rule is about
		// who is pushing and holds nobody else to it.
		primary := w.repository.WorkingPath
		git(t, primary, "branch", "task/one")
		out, err = tryGitIn(t, primary, "push", "--force", gate.RemoteName, "task/one")
		if err != nil {
			t.Fatalf("a person's rewind of the branch was refused, so the worker rule reached a person:\n%s", out)
		}
		if got := gateRefs(t, gateRepo); got == before {
			t.Fatalf("the person's push was reported admitted and moved nothing:\n%s", got)
		}
	})
}

// A worker's event is appended to its own task's history, which the request
// never names, and the entry is answered back as the store holds it; a kind
// the program writes is refused by name, and so is a kind the table has no
// row for; and the same request from a peer in no worker group is refused
// as one. The history read back afterwards is the store's own.
func TestAWorkerAppendsToItsOwnHistoryAndNoOneElseCan(t *testing.T) {
	launchedWorker(t, "task/two", func(w launched) {
		running, task := w.running, w.task
		answer, code := callAs(t, w, ipc.MethodTaskEvent, machine.TaskEventRequest{Kind: "working", Detail: "on it"})
		if code != "" {
			t.Fatalf("the worker's event was refused with %q", code)
		}
		var appended machine.Appended
		if err := json.Unmarshal(answer, &appended); err != nil {
			t.Fatalf("the answer does not decode: %v\n%s", err, answer)
		}
		if appended.Event.TaskID != task || appended.Event.Kind != "working" || appended.Event.Detail != "on it" || appended.Event.Sequence < 2 {
			t.Fatalf("the worker's event was recorded as %+v, want a working entry of task %s after its opening", appended.Event, task)
		}
		if _, code := callAs(t, w, ipc.MethodTaskEvent, machine.TaskEventRequest{Kind: store.DispatchedEventKind}); code != ipc.CodeWorker {
			t.Fatalf("the worker appended %q, refused with %q, want %q", store.DispatchedEventKind, code, ipc.CodeWorker)
		}
		if _, code := callAs(t, w, ipc.MethodTaskEvent, machine.TaskEventRequest{Kind: "promoted-myself"}); code != ipc.CodeInvalidRequest {
			t.Fatalf("an unrecognized kind was refused with %q, want %q", code, ipc.CodeInvalidRequest)
		}

		var fromOutside machine.Appended
		err := running.client.Call(t.Context(), ipc.MethodTaskEvent, machine.TaskEventRequest{Kind: "working"}, &fromOutside)
		if !errors.Is(err, ipc.ErrNotWorker) {
			t.Fatalf("task.event from a peer in no worker group = %v, want ErrNotWorker", err)
		}

		events := historyOf(t, w.home, task)
		last := events[len(events)-1]
		if last != appended.Event || last.Kind != "working" {
			t.Fatalf("the task's history ends with %+v, want the worker's one entry %+v", last, appended.Event)
		}
		for _, e := range events[:len(events)-1] {
			if e.Kind == "working" {
				t.Fatalf("the history carries a second working entry: %+v", events)
			}
		}
	})
}

// An investigation's report arrives as the worker's task.event of the
// report kind, with the report as the request's detail, and is written
// through the report's own record rather than appended. A report that
// cannot be read is refused as an invalid request and nothing is stored or
// appended. A readable one is stored, its ask finding and its finding
// naming no action are registered as holds (P3), and the history gains one
// report entry, the one the answer names, with an empty detail: the report
// is the stored row and not the entry. A second report is refused and the
// first stands, and the same request from a peer in no worker group is
// refused as one. A delivery task's worker filing a report is refused as an
// invalid request, since only an investigation files one.
func TestAWorkersReportIsStoredThroughItsRecordAndOnlyAnInvestigationFilesOne(t *testing.T) {
	principles.Cite(t, principles.P3)
	launchedWorkerOf(t, store.TaskInvestigation, "", func(w launched) {
		running, task := w.running, w.task
		before := historyOf(t, w.home, task)
		if _, code := callAs(t, w, ipc.MethodTaskEvent,
			machine.TaskEventRequest{Kind: store.ReportReadyEventKind, Detail: "nothing here is a report"}); code != ipc.CodeInvalidRequest {
			t.Fatalf("an unreadable report was refused with %q, want %q", code, ipc.CodeInvalidRequest)
		}
		if _, err := recordsOf(t, w.home).TaskReport(t.Context(), task); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("an unreadable report was stored: %v", err)
		}
		if after := historyOf(t, w.home, task); len(after) != len(before) {
			t.Fatalf("an unreadable report grew the history from %d to %d entries", len(before), len(after))
		}

		report := `{"summary":"looked","findings":[` +
			`{"severity":"warning","action":"ask","description":"Should the old path be kept?"},` +
			`{"severity":"warning","description":"Is the cache keyed right?"},` +
			`{"severity":"info","action":"note","description":"The tests are thin here."}]}`
		answer, code := callAs(t, w, ipc.MethodTaskEvent, machine.TaskEventRequest{Kind: store.ReportReadyEventKind, Detail: report})
		if code != "" {
			t.Fatalf("the worker's report was refused with %q", code)
		}
		var appended machine.Appended
		if err := json.Unmarshal(answer, &appended); err != nil {
			t.Fatalf("the answer does not decode: %v\n%s", err, answer)
		}
		stored, err := recordsOf(t, w.home).TaskReport(t.Context(), task)
		if err != nil || stored.Body != report || len(stored.HoldKeys) != 2 {
			t.Fatalf("the report was stored as %+v (%v), want the body and two decisions", stored, err)
		}
		for _, key := range stored.HoldKeys {
			if h, err := recordsOf(t, w.home).Hold(t.Context(), key); err != nil || !h.Open() || h.TaskID.Or("") != task {
				t.Fatalf("hold %s = %+v, %v; want open and naming the task", key, h, err)
			}
		}
		history := historyOf(t, w.home, task)
		if len(history) != len(before)+1 || history[len(history)-1] != appended.Event ||
			appended.Event.Kind != store.ReportReadyEventKind || appended.Event.Detail != "" {
			t.Fatalf("the history is %+v and the answer %+v, want one report entry more with no detail", history, appended)
		}

		if _, code := callAs(t, w, ipc.MethodTaskEvent,
			machine.TaskEventRequest{Kind: store.ReportReadyEventKind, Detail: `{"summary":"again"}`}); code != ipc.CodeInvalidRequest {
			t.Fatalf("a second report was refused with %q, want %q", code, ipc.CodeInvalidRequest)
		}
		var outside machine.Appended
		if err := running.client.Call(t.Context(), ipc.MethodTaskEvent,
			machine.TaskEventRequest{Kind: store.ReportReadyEventKind, Detail: report}, &outside); !errors.Is(err, ipc.ErrNotWorker) {
			t.Fatalf("a report from a peer in no worker group = %v, want ErrNotWorker", err)
		}
		if again, err := recordsOf(t, w.home).TaskReport(t.Context(), task); err != nil || again.Body != report {
			t.Fatalf("the refused reports disturbed the first: %+v, %v", again, err)
		}
		if after := historyOf(t, w.home, task); len(after) != len(history) {
			t.Fatalf("refused reports grew the history to %d entries from %d", len(after), len(history))
		}
	})
	launchedWorker(t, "task/report", func(w launched) {
		if _, code := callAs(t, w, ipc.MethodTaskEvent,
			machine.TaskEventRequest{Kind: store.ReportReadyEventKind, Detail: `{"summary":"looked"}`}); code != ipc.CodeInvalidRequest {
			t.Fatalf("a delivery task's report was refused with %q, want %q", code, ipc.CodeInvalidRequest)
		}
		if _, err := recordsOf(t, w.home).TaskReport(t.Context(), w.task); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("a delivery task's report was stored: %v", err)
		}
	})
}

// The queue and arm verification are the coordinator's. A drain from outside
// every worker group returns what the loop surfaced and hands it out,
// recorded as handed to the coordinator, the derived acknowledger, and
// still queued; acknowledging it is accepted and recorded as the
// coordinator's, after which the next drain does not return it and
// acknowledging it again is refused;
// arm verification answers from the real beacon the service's own loop
// writes; and every one of them, the wake stream included, is refused from
// the worker. The wake drained is one the service's supervisor loop
// surfaced for the launched task, not one written for the test: the loop
// observes the worker through the service's own provider, so a live worker
// is absorbed on its handle's evidence and nothing is queued for it; the
// test ends the worker through that handle, and the loop's next pass
// surfaces its terminal as gone. The refusals from the worker are
// asked first, while the worker is still live.
func TestTheQueueAndArmVerificationAreTheCoordinatorsAndNotAWorkers(t *testing.T) {
	launchedWorker(t, "task/three", func(w launched) {
		running, task := w.running, w.task
		for _, method := range []ipc.Method{ipc.MethodWakesList, ipc.MethodWakesPeek, ipc.MethodWakesAcknowledge, ipc.MethodWakesSubscribe, ipc.MethodSupervisionAssert} {
			params := any(struct{}{})
			if method == ipc.MethodWakesAcknowledge {
				params = machine.AcknowledgeRequest{Wakes: []int64{1}}
			}
			if _, code := callAs(t, w, method, params); code != ipc.CodeWorker {
				t.Fatalf("%s from the worker answered %q, want %q", method, code, ipc.CodeWorker)
			}
		}

		if err := w.provider.Send(t.Context(), w.session, "finish"); err != nil {
			t.Fatalf("Send: %v", err)
		}
		waitCapture(t, w.provider, w.session, "worker finished")

		var queued machine.Wakes
		deadline := time.Now().Add(30 * time.Second)
		for len(queued.Wakes) == 0 {
			if !time.Now().Before(deadline) {
				t.Fatal("the loop surfaced no wake for the launched task after its worker finished")
			}
			if err := running.client.Call(t.Context(), ipc.MethodWakesList, machine.WakesRequest{}, &queued); err != nil {
				t.Fatalf("wakes.list: %v", err)
			}
			time.Sleep(50 * time.Millisecond)
		}
		ids := make([]int64, 0, len(queued.Wakes))
		for _, w := range queued.Wakes {
			if w.TaskID != task || !w.HandedOut() {
				t.Fatalf("the drain returned %+v, want a wake of task %s handed out and still queued", w, task)
			}
			ids = append(ids, w.ID)
		}
		records := recordsOf(t, w.home)
		for _, id := range ids {
			if row := wakeRow(t, records, task, id); row.HandedOutTo != store.Known(store.AcknowledgedByCoordinator()) || !row.Queued() {
				t.Fatalf("the drained wake reads %+v in the records, want it handed to the coordinator and still queued", row)
			}
		}
		var closed machine.Acknowledged
		if err := running.client.Call(t.Context(), ipc.MethodWakesAcknowledge, machine.AcknowledgeRequest{Wakes: ids}, &closed); err != nil {
			t.Fatalf("acknowledging the handed-out wakes: %v", err)
		}
		for _, id := range ids {
			if row := wakeRow(t, records, task, id); row.Queued() || row.AcknowledgedBy != store.Known(store.AcknowledgedByCoordinator()) {
				t.Fatalf("the acknowledged wake reads %+v in the records, want it closed by the coordinator", row)
			}
		}
		var again machine.Wakes
		if err := running.client.Call(t.Context(), ipc.MethodWakesList, machine.WakesRequest{}, &again); err != nil {
			t.Fatalf("wakes.list: %v", err)
		}
		for _, w := range again.Wakes {
			for _, id := range ids {
				if w.ID == id {
					t.Fatalf("wake %d was acknowledged and the next drain returned it again", id)
				}
			}
		}
		var acknowledged machine.Acknowledged
		err := running.client.Call(t.Context(), ipc.MethodWakesAcknowledge, machine.AcknowledgeRequest{Wakes: ids[:1]}, &acknowledged)
		if !errors.Is(err, ipc.ErrInvalidRequest) {
			t.Fatalf("acknowledging an acknowledged wake again = %v, want ErrInvalidRequest", err)
		}

		var arm supervisor.Arm
		if err := running.client.Call(t.Context(), ipc.MethodSupervisionAssert, struct{}{}, &arm); err != nil {
			t.Fatalf("supervision.assert: %v", err)
		}
		if arm.Outcome == supervisor.ArmFailed || arm.Sequence == 0 {
			t.Fatalf("arm verification over the running service's own beacon answered %+v", arm)
		}
	})
}

// The seen mark PRD section 9's catching up reads is moved by the person or
// the coordinator and never by a task's worker: catchup.mark from the
// worker is refused as a worker and writes nothing, while the open
// read answers it; the same mark from outside every worker group, naming
// the same landed record, is taken, which is the positive control that the
// refusal is the worker relation's and not the request's.
func TestTheSeenMarkIsNotAWorkers(t *testing.T) {
	launchedWorker(t, "task/four", func(w launched) {
		running := w.running
		records, err := store.Open(t.Context(), w.home.Database(), store.WithRedactor(redact.New()))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = records.Close() }()
		const landed = "run-landed-for-the-mark"
		if _, err := records.CreateRun(t.Context(), store.Run{
			ID: landed, RepositoryID: w.repository.ID, Branch: "landed", SubmittedHead: "aaaa", Base: "bbbb",
			Intent: "land it", IntentSource: "push", Build: store.Build{Version: "v0.1.0", Revision: "0123456789abcdef", Go: "go1.25.0"},
			ConfigDigest: "cfg",
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := records.TransitionRun(t.Context(), landed, []store.RunStatus{store.RunPending}, store.RunRunning); err != nil {
			t.Fatal(err)
		}
		if _, err := records.TransitionRunOnPullRequest(t.Context(), landed, []store.RunStatus{store.RunRunning}, store.RunPassed,
			store.PullRequestRead{PullRequest: 7, Head: "0123abc", End: store.PullRequestMerged}, store.HoldClosure{}); err != nil {
			t.Fatal(err)
		}
		mark := machine.MarkSeenRequest{Records: []store.SeenRecord{{Kind: store.SeenMerge, Key: landed}}}

		if _, code := callAs(t, w, ipc.MethodCatchUpMark, mark); code != ipc.CodeWorker {
			t.Fatalf("catchup.mark from the worker answered %q, want %q", code, ipc.CodeWorker)
		}
		if marks, err := records.SeenMarks(t.Context()); err != nil || len(marks) != 0 {
			t.Fatalf("the refused mark wrote %+v (%v)", marks, err)
		}
		if _, code := callAs(t, w, ipc.MethodCatchUpRead, struct{}{}); code != "" {
			t.Fatalf("catchup.read from the worker was refused with %q, and it is open", code)
		}

		var marked machine.Marked
		if err := running.client.Call(t.Context(), ipc.MethodCatchUpMark, mark, &marked); err != nil {
			t.Fatalf("catchup.mark from outside every worker group: %v", err)
		}
		marks, err := records.SeenMarks(t.Context())
		if err != nil || len(marks) != 1 || marks[0].SeenRecord != mark.Records[0] || len(marked.Marks) != 1 {
			t.Fatalf("the mark from outside left %+v (%v) and answered %+v, want the one record", marks, err, marked)
		}
	})
}

// mustGitAs is gitAs failing the test on an error.
func mustGitAs(t *testing.T, w launched, dir string, args ...string) string {
	t.Helper()
	out, err := gitAs(t, w, dir, args...)
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(out)
}

// tryGitIn runs git from this test's own process and returns its output and
// error, for a push whose admission as a person's is the point.
func tryGitIn(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	cmd.WaitDelay = 5 * time.Second
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=gc.auto", "GIT_CONFIG_VALUE_0=0",
		"GIT_CONFIG_KEY_1=maintenance.auto", "GIT_CONFIG_VALUE_1=false",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.invalid",
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// gateRefs is every branch a gate repository holds with its object, read
// with git directly. It reads refs/heads only, because those are the
// references a push names and the ones admission decides about; the run an
// admitted push starts writes references of its own under refs/assistant and
// refs/remotes on the service's goroutine, at a moment this test does not
// control, so comparing those would read the run's progress as the push's
// doing.
func gateRefs(t *testing.T, repository string) string {
	t.Helper()
	return git(t, repository, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
}

// historyOf reads a task's history from the home's database through a
// read-only open beside the service that holds it, so what is asserted is
// the record and not the answer the handler carried.
func historyOf(t *testing.T, h *home.Home, task string) []store.TaskEvent {
	t.Helper()
	records, err := store.OpenReadOnly(t.Context(), h.Database())
	if err != nil {
		t.Fatalf("opening the store read-only: %v", err)
	}
	defer func() { _ = records.Close() }()
	events, err := records.TaskEvents(t.Context(), task, 0, 0)
	if err != nil {
		t.Fatalf("reading the history of task %s: %v", task, err)
	}
	return events
}

// The worker-set relation and the containment relation are two owners of
// two facts: a worker's group is placed by the first and not the second,
// and the same group registered as a stage is placed by the second. Cited
// for P14 because the test is about one fact having one owner, with the
// control being the registration that makes the other relation answer.
func TestAWorkerIsPlacedByTheWorkerSetAndNotByContainment(t *testing.T) {
	principles.Cite(t, principles.P14)
	launchedWorker(t, "task/four", func(w launched) {
		task, group := w.task, w.group
		s := w.running.service
		forget := s.StageStarted("run-under-validation", "review", 424242)
		defer forget()
		creds := ipc.Credentials{PID: group}
		m, ok, err := s.WorkerOf(creds)
		if err != nil || !ok || m != (session.Membership{Task: task, Group: group}) {
			t.Fatalf("WorkerOf(the worker's group) = %+v, %v, %v; want task %s", m, ok, err, task)
		}
		contained, err := s.Contained(context.Background(), creds)
		if err != nil || contained.Contained {
			t.Fatalf("the worker's group is contained: %+v, %v", contained, err)
		}
		release := s.StageStarted("run-under-validation", "fix", group)
		contained, err = s.Contained(context.Background(), creds)
		release()
		if err != nil || !contained.Contained {
			t.Fatalf("the control registration did not contain the worker's group: %+v, %v", contained, err)
		}
		if _, ok, err := s.WorkerOf(ipc.Credentials{PID: os.Getpid()}); err != nil || ok {
			t.Fatalf("this test process is placed in a worker group: %v, %v", ok, err)
		}
	})
}

// Answering a task's hold is the coordinator's and not a worker's: a hold
// naming the launched task, the shape an investigation's report registers,
// is refused to the task's own worker with ErrWorker and stays open, which is what keeps an investigation from
// answering its own decision and a worker from retrying itself past its
// relaunch bound. The same answer from outside every worker group closes
// it, recorded with the resolver the surface derives and with nothing the
// request said.
func TestATasksHoldIsAnsweredByTheCoordinatorAndNotItsWorker(t *testing.T) {
	launchedWorker(t, "task/four", func(w launched) {
		records := openRecords(t, w.home)
		defer func() { _ = records.Close() }()
		hold, err := records.RegisterHold(t.Context(), store.Hold{
			Key: "task/" + w.task + "/finding/one", TaskID: store.Known(w.task),
			Subject: "whether the cache may be dropped", Detail: "the report asks before dropping it",
		})
		if err != nil {
			t.Fatal(err)
		}
		request := machine.TaskRespondRequest{Task: w.task, Answer: "approve"}
		if _, code := callAs(t, w, ipc.MethodTaskRespond, request); code != ipc.CodeWorker {
			t.Fatalf("task.respond from the task's own worker answered %q, want %q", code, ipc.CodeWorker)
		}
		if got, err := records.Hold(t.Context(), hold.Key); err != nil || !got.Open() {
			t.Fatalf("after the worker's refused answer the hold is %+v (%v), want it open", got, err)
		}
		var answered machine.TaskAnswered
		if err := w.running.client.Call(t.Context(), ipc.MethodTaskRespond, request, &answered); err != nil {
			t.Fatalf("task.respond from outside every worker group: %v", err)
		}
		got, err := records.Hold(t.Context(), hold.Key)
		if err != nil || got.Resolution.Or("") != "approve" || got.ResolvedBy.Or(store.Resolver{}) != (ipc.Request{}).HoldResolver() {
			t.Fatalf("the coordinator's answer left the hold %+v (%v), want it resolved approve by the derived resolver", got, err)
		}
		if answered.Hold.Key != hold.Key || answered.Effect != machine.AnswerRecorded {
			t.Fatalf("the answer reports %+v, want hold %s recorded", answered, hold.Key)
		}
	})
}

// Answering a task's run through the task is the coordinator's on the same
// terms as answering the task's own hold: run.respond selecting the run by
// the task is refused to the task's own worker with ErrWorker, decided from
// its process group before any record is read. The control is the same
// request from outside every worker group, which gets past that refusal to
// the records and is refused there instead, because the task's branch has
// no run under way: a different code, so the worker's refusal is shown to
// be about who asked and not about what was asked.
func TestATasksRunIsNotAnsweredThroughTheTaskByItsWorker(t *testing.T) {
	launchedWorker(t, "task/four", func(w launched) {
		request := machine.RespondRequest{Task: w.task, Answer: "approved"}
		if _, code := callAs(t, w, ipc.MethodRunRespond, request); code != ipc.CodeWorker {
			t.Fatalf("run.respond given the task from its own worker answered %q, want %q", code, ipc.CodeWorker)
		}
		var run machine.Run
		err := w.running.client.Call(t.Context(), ipc.MethodRunRespond, request, &run)
		if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), machine.PhraseTaskNothingWaiting.String()) {
			t.Fatalf("run.respond given the task from outside every worker group: %v, want the refusal that nothing waits", err)
		}
	})
}
