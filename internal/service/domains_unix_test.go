//go:build unix

package service_test

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// These tests drive PRD section 9's subordinate coordinator over the
// service's real socket, on workers_unix_test.go's terms: the subordinate
// is the stand-in in line mode, started by the service's own
// domain.delegate through the same provider a worker is started through,
// and every request from it is a command the test sends it as a line,
// which it runs through a shell in a process group it does not lead. The
// credentials the service places are then those of a process the
// subordinate started, which the kernel attributes and nothing here
// states. The coordinator's control is this test's own process, in no
// held set, which is how a person's shell is placed.

// delegated is what delegateDomain hands a body: the domain, the running
// service, and the provider and session the service holds for the
// subordinate, as a launched so the worker helpers drive it.
type delegated struct {
	domain string
	as     launched
}

// delegateDomain delegates a domain from this test's process, which is the
// coordinator's control, waits for the service to hold the subordinate's
// session and for its greeting, and returns what the helpers drive it
// through. The repository and gate are the launched worker's, so a task the
// subordinate creates can name them.
func delegateDomain(t *testing.T, w launched, domain, posture string) delegated {
	t.Helper()
	var answer machine.Delegated
	if err := w.running.client.Call(t.Context(), ipc.MethodDomainDelegate, machine.DomainDelegateRequest{
		Domain: domain, Delivery: posture, Instructions: "Coordinate the " + domain + " work.\n",
	}, &answer); err != nil {
		t.Fatalf("domain.delegate from outside every held set: %v", err)
	}
	if answer.Record.Domain != domain || !answer.Record.Standing() || answer.Attempt != 1 {
		t.Fatalf("domain.delegate answered %+v, want a standing delegation of %s with attempt 1", answer, domain)
	}
	if raw, err := os.ReadFile(answer.Instructions); err != nil || !strings.HasPrefix(string(raw), "Coordinate the "+domain) {
		t.Fatalf("the instructions file reads %q (%v), want the request's text", raw, err)
	}
	var host session.Host
	var sub *session.Session
	deadline := time.Now().Add(launchWait)
	for {
		var ok bool
		if host, sub, ok = w.running.service.SubordinateSessionOf(domain); ok {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the service never held a session for the subordinate coordinator of domain %s", domain)
		}
		time.Sleep(20 * time.Millisecond)
	}
	waitCapture(t, host, sub, "worker ready")
	ref := sub.Reference()
	if !ref.Subordinate() || ref.Domain != domain {
		t.Fatalf("the subordinate's reference is %+v, want one naming domain %s", ref, domain)
	}
	attempts, err := recordsOf(t, w.home).DelegationAttempts(t.Context(), answer.Record.ID)
	if err != nil || len(attempts) != 1 || attempts[0].SessionRef.Or("") != ref.String() {
		t.Fatalf("the delegation's attempts are %+v (%v), want the one attempt naming session %s", attempts, err, ref)
	}
	return delegated{domain: domain, as: launched{
		running: w.running, home: w.home, repository: w.repository, gateRepo: w.gateRepo,
		group: ref.PID, provider: host, session: sub,
	}}
}

// createdBy has the subordinate create a task and returns the answer.
func createdBy(t *testing.T, d delegated, request machine.TaskCreateRequest) machine.TaskCreated {
	t.Helper()
	answer, code := callAs(t, d.as, ipc.MethodTaskCreate, request)
	if code != "" {
		t.Fatalf("task.create from the subordinate was refused with %q", code)
	}
	var created machine.TaskCreated
	if err := json.Unmarshal(answer, &created); err != nil {
		t.Fatalf("the answer does not decode: %v\n%s", err, answer)
	}
	return created
}

// waitRunning polls the fleet through the coordinator's client until the
// task reads Running from its live terminal.
func waitRunning(t *testing.T, w launched, task string) {
	t.Helper()
	deadline := time.Now().Add(launchWait)
	for !readsRunning(listedTask(t, w.running.client, task)) {
		if !time.Now().Before(deadline) {
			t.Fatalf("task %s never read running; it reads %+v", task, listedTask(t, w.running.client, task))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// finishWorker ends a task's worker through the handle the service holds
// for it, and waits until the task reads Unknown from its dead terminal.
func finishWorker(t *testing.T, w launched, task string) {
	t.Helper()
	provider, sess := launchedSession(t, w.running.service, task)
	if err := provider.Send(t.Context(), sess, "finish"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitCapture(t, provider, sess, "worker finished")
	deadline := time.Now().Add(launchWait)
	for {
		got := listedTask(t, w.running.client, task)
		if got.State == taskstate.Unknown && got.Source == taskstate.SourceProcess && got.Basis.Process == taskstate.Dead {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the ended worker's task never read Unknown from a dead terminal; it reads %+v", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The grant: a subordinate coordinator holds the coordinator's commands
// over its domain and nothing else, decided from its placement and never
// from the request. From the launched worker both delegation methods are
// refused as a worker; from this process, the coordinator's control, a
// domain is delegated and its subordinate started. From the subordinate a
// task is created with its domain written from the placement, which no
// request field names, and with the delegation's posture, while one asking
// for a posture of its own is refused; the fleet it lists is its domain's
// tasks alone; a task outside its domain is refused by name on every
// task-naming method it holds; it messages, peeks at, relaunches and stops
// its own domain's worker, recorded as its own member; and every method
// section 9 refuses a subordinate is refused with the subordinate code, the
// two decisions it hands up each recorded as an escalation and the rest
// recorded nowhere.
func TestASubordinateHoldsTheCoordinatorsCommandsOverItsDomainAndNothingElse(t *testing.T) {
	t.Parallel()
	service.RequireSweepClearsHere(t, "a subordinate relaunching a dead terminal in its domain")
	launchedWorker(t, "task/base", func(w launched) {
		delegation := machine.DomainDelegateRequest{Domain: "alpha", Delivery: "gated", Instructions: "x"}
		for _, method := range []ipc.Method{ipc.MethodDomainDelegate, ipc.MethodDomainEnd} {
			if _, code := callAs(t, w, method, delegation); code != ipc.CodeWorker {
				t.Fatalf("%s from the worker answered %q, want %q", method, code, ipc.CodeWorker)
			}
		}
		if _, _, err := recordsOf(t, w.home).Delegation(t.Context(), "alpha"); err != nil {
			t.Fatal(err)
		} else if _, found, _ := recordsOf(t, w.home).Delegation(t.Context(), "alpha"); found {
			t.Fatal("the refused delegation was recorded")
		}

		d := delegateDomain(t, w, "alpha", "gated")
		var again machine.Delegated
		err := w.running.client.Call(t.Context(), ipc.MethodDomainDelegate, delegation, &again)
		if !errors.Is(err, ipc.ErrRefused) {
			t.Fatalf("delegating a standing domain again = %v, want ErrRefused", err)
		}

		// The task is the domain's by placement and takes the delegation's
		// posture; a posture of its own is refused before intake reads it.
		if _, code := callAs(t, d.as, ipc.MethodTaskCreate, machine.TaskCreateRequest{
			Shape: "delivery", Project: "alpha", Instructions: "x", Repository: w.repository.ID, Branch: "task/alpha-d", Delivery: "local",
		}); code != ipc.CodeInvalidRequest {
			t.Fatalf("a subordinate's task.create naming a posture answered %q, want %q", code, ipc.CodeInvalidRequest)
		}
		delivery := createdBy(t, d, machine.TaskCreateRequest{
			Shape: "delivery", Project: "alpha delivery", Instructions: "Deliver it.\n", Repository: w.repository.ID, Branch: "task/alpha-d",
		})
		if delivery.Record.Domain != "alpha" || delivery.Record.Delivery != store.Known(config.DeliveryGated) {
			t.Fatalf("the subordinate's delivery task was recorded %+v, want domain alpha under the delegation's gated posture", delivery.Record)
		}
		investigation := createdBy(t, d, machine.TaskCreateRequest{
			Shape: "investigation", Project: "alpha look", Instructions: "Look into it.\n", Repository: w.repository.ID,
		})
		own := investigation.Record.ID
		if investigation.Record.Domain != "alpha" || investigation.Record.Delivery.IsKnown() {
			t.Fatalf("the subordinate's investigation was recorded %+v, want domain alpha and no posture", investigation.Record)
		}
		if stored, err := recordsOf(t, w.home).Task(t.Context(), own); err != nil || stored.Domain != "alpha" {
			t.Fatalf("the store holds %+v (%v) for the created task, want domain alpha", stored, err)
		}

		// The listing is the domain's alone, and a task outside it is
		// refused by name; the coordinator's own listing is the whole fleet.
		answer, code := callAs(t, d.as, ipc.MethodTasksList, struct{}{})
		if code != "" {
			t.Fatalf("tasks.list from the subordinate was refused with %q", code)
		}
		var listedBySub machine.Tasks
		if err := json.Unmarshal(answer, &listedBySub); err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, task := range listedBySub.Tasks {
			ids = append(ids, task.Record.ID)
			if task.Record.Domain != "alpha" {
				t.Fatalf("the subordinate's listing carries task %s, which is %s", task.Record.ID, task.Record.Domain)
			}
		}
		if slices.Contains(ids, w.task) || !slices.Contains(ids, own) || !slices.Contains(ids, delivery.Record.ID) {
			t.Fatalf("the subordinate's listing is %v, want its domain's two tasks and not %s", ids, w.task)
		}
		if n := len(listedTasks(t, w).Tasks); n != 3 {
			t.Fatalf("the coordinator's listing has %d tasks, want all three", n)
		}
		if _, code := callAs(t, d.as, ipc.MethodTaskGet, machine.TaskRequest{Task: w.task}); code != ipc.CodeOutsideDomain {
			t.Fatalf("task.get of a task outside the domain answered %q, want %q", code, ipc.CodeOutsideDomain)
		}
		if _, code := callAs(t, d.as, ipc.MethodTaskGet, machine.TaskRequest{Task: own}); code != "" {
			t.Fatalf("task.get of the domain's own task was refused with %q", code)
		}
		for _, method := range []ipc.Method{ipc.MethodTaskMessage, ipc.MethodTaskStop, ipc.MethodTaskPeek, ipc.MethodTaskRelaunch, ipc.MethodTaskRelay} {
			params := any(machine.TaskRequest{Task: w.task})
			if method == ipc.MethodTaskMessage {
				params = machine.TaskMessageRequest{Task: w.task, Text: "hello"}
			}
			if _, code := callAs(t, d.as, method, params); code != ipc.CodeOutsideDomain {
				t.Fatalf("%s naming a task outside the domain answered %q, want %q", method, code, ipc.CodeOutsideDomain)
			}
		}

		// The steering methods over its own domain's worker, recorded as
		// the subordinate's member.
		waitRunning(t, w, own)
		answer, code = callAs(t, d.as, ipc.MethodTaskMessage, machine.TaskMessageRequest{Task: own, Text: "status"})
		if code != "" {
			t.Fatalf("task.message from the subordinate was refused with %q", code)
		}
		var messaged machine.TaskMessaged
		if err := json.Unmarshal(answer, &messaged); err != nil {
			t.Fatal(err)
		}
		if messaged.Delivery != store.DeliveryWritten || messaged.SentBy != "subordinate of domain alpha" {
			t.Fatalf("task.message answered %+v, want it written and sent by the subordinate of domain alpha", messaged)
		}
		if stored, err := recordsOf(t, w.home).TaskMessage(t.Context(), messaged.Message.ID); err != nil || stored.SentBy != store.SentBySubordinate("alpha") {
			t.Fatalf("the message record reads %+v (%v), want it sent by the subordinate's member", stored, err)
		}
		if _, code := callAs(t, d.as, ipc.MethodTaskPeek, machine.TaskRequest{Task: own}); code != "" {
			t.Fatalf("task.peek from the subordinate was refused with %q", code)
		}

		// The decisions it hands up: each refused as a subordinate and
		// recorded as an escalation about the domain's task, with its
		// words; the same asks about a task outside the domain record
		// nothing.
		before := escalationsOf(t, w.home, own)
		if _, code := callAs(t, d.as, ipc.MethodTaskPromote, machine.TaskPromoteRequest{
			Task: own, Repository: w.repository.ID, Branch: "task/alpha-p", Intent: "ship the fix",
		}); code != ipc.CodeSubordinate {
			t.Fatalf("task.promote from the subordinate answered %q, want %q", code, ipc.CodeSubordinate)
		}
		if _, code := callAs(t, d.as, ipc.MethodTaskRespond, machine.TaskRespondRequest{Task: own, Answer: "retry", Note: "go again"}); code != ipc.CodeSubordinate {
			t.Fatalf("task.respond from the subordinate answered %q, want %q", code, ipc.CodeSubordinate)
		}
		after := escalationsOf(t, w.home, own)
		if len(after) != len(before)+2 || !strings.Contains(after[len(after)-2].Text, "ship the fix") || !strings.Contains(after[len(after)-1].Text, "go again") {
			t.Fatalf("the escalations about task %s are %+v, want two more carrying the asks' words", own, after)
		}
		if stored, err := recordsOf(t, w.home).Task(t.Context(), own); err != nil || stored.Shape != store.TaskInvestigation {
			t.Fatalf("the refused promotion changed the task: %+v (%v)", stored, err)
		}
		if _, code := callAs(t, d.as, ipc.MethodTaskPromote, machine.TaskPromoteRequest{
			Task: w.task, Repository: w.repository.ID, Branch: "task/base-p", Intent: "not mine",
		}); code != ipc.CodeSubordinate {
			t.Fatalf("task.promote of a task outside the domain answered %q, want %q", code, ipc.CodeSubordinate)
		}
		if outside := escalationsOf(t, w.home, w.task); len(outside) != 0 {
			t.Fatalf("a refused ask about a task outside the domain was recorded as an escalation: %+v", outside)
		}

		// Every other refusal section 9 lists, with nothing recorded.
		refusals := []struct {
			method ipc.Method
			params any
		}{
			{ipc.MethodCatchUpMark, machine.MarkSeenRequest{Records: []store.SeenRecord{{Kind: store.SeenMerge, Key: "x"}}}},
			{ipc.MethodRunStart, machine.StartRequest{}},
			{ipc.MethodRunRerun, machine.RerunRequest{}},
			{ipc.MethodRunCancel, machine.CancelRequest{Run: "nothing"}},
			{ipc.MethodRunRespond, machine.RespondRequest{Run: "nothing", Answer: "x"}},
			{ipc.MethodBranchSync, machine.SyncRequest{}},
			{ipc.MethodServiceStop, machine.LifecycleRequest{}},
			{ipc.MethodServiceRestart, machine.LifecycleRequest{}},
			{ipc.MethodDomainDelegate, machine.DomainDelegateRequest{Domain: "beta", Delivery: "gated", Instructions: "x"}},
			{ipc.MethodDomainEnd, machine.DomainEndRequest{Domain: "alpha"}},
			{ipc.MethodTaskInterrupt, machine.TaskRequest{Task: own}},
			{ipc.MethodWakesSubscribe, struct{}{}},
		}
		for _, r := range refusals {
			if _, code := callAs(t, d.as, r.method, r.params); code != ipc.CodeSubordinate {
				t.Fatalf("%s from the subordinate answered %q, want %q", r.method, code, ipc.CodeSubordinate)
			}
		}
		if latest, found, err := recordsOf(t, w.home).Delegation(t.Context(), "alpha"); err != nil || !found || !latest.Standing() {
			t.Fatalf("after the refused domain.end the delegation reads %+v (%v, %v), want it standing", latest, found, err)
		}
		if _, found, _ := recordsOf(t, w.home).Delegation(t.Context(), "beta"); found {
			t.Fatal("the refused delegation of beta was recorded")
		}
		if _, code := callAs(t, d.as, ipc.MethodSupervisionAssert, struct{}{}); code != "" {
			t.Fatalf("supervision.assert from the subordinate was refused with %q", code)
		}

		// The relaunch and the stop over its own domain's worker.
		finishWorker(t, w, own)
		answer, code = callAs(t, d.as, ipc.MethodTaskRelaunch, machine.TaskRequest{Task: own})
		if code != "" {
			t.Fatalf("task.relaunch from the subordinate was refused with %q", code)
		}
		var relaunched machine.TaskRelaunched
		if err := json.Unmarshal(answer, &relaunched); err != nil {
			t.Fatal(err)
		}
		if relaunched.Attempt != 2 {
			t.Fatalf("task.relaunch answered %+v, want attempt 2", relaunched)
		}
		waitRunning(t, w, own)
		answer, code = callAs(t, d.as, ipc.MethodTaskStop, machine.TaskRequest{Task: own})
		if code != "" {
			t.Fatalf("task.stop from the subordinate was refused with %q", code)
		}
		var stopped machine.TaskStopped
		if err := json.Unmarshal(answer, &stopped); err != nil {
			t.Fatal(err)
		}
		if stopped.Attempt != 2 || stopped.Hold.Key == "" || !strings.Contains(stopped.Hold.Detail, "subordinate of domain alpha") {
			t.Fatalf("task.stop answered %+v, want attempt 2 held with the subordinate named", stopped)
		}
	})
}

// listedTasks is tasks.list from the coordinator's client.
func listedTasks(t *testing.T, w launched) machine.Tasks {
	t.Helper()
	var listed machine.Tasks
	if err := w.running.client.Call(t.Context(), ipc.MethodTasksList, struct{}{}, &listed); err != nil {
		t.Fatalf("listing the tasks: %v", err)
	}
	return listed
}

// escalationsOf reads a task's escalation records.
func escalationsOf(t *testing.T, h *home.Home, task string) []store.Escalation {
	t.Helper()
	out, err := recordsOf(t, h).TaskEscalations(t.Context(), task)
	if err != nil {
		t.Fatalf("reading the escalations about task %s: %v", task, err)
	}
	return out
}

// drainedBy drains the queue as the subordinate and returns the rows.
func drainedBy(t *testing.T, d delegated) machine.Wakes {
	t.Helper()
	answer, code := callAs(t, d.as, ipc.MethodWakesList, machine.WakesRequest{})
	if code != "" {
		t.Fatalf("wakes.list from the subordinate was refused with %q", code)
	}
	var out machine.Wakes
	if err := json.Unmarshal(answer, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// wakeOf is the identifier of the task's queued wake of the kind, or zero.
func wakeOf(w machine.Wakes, task string, kind supervisor.Kind) int64 {
	for _, wake := range w.Wakes {
		if wake.TaskID == task && wake.Kind == string(kind) {
			return wake.ID
		}
	}
	return 0
}

// P14 at the queue, over the real loop and the real socket: a wake reaches
// exactly one coordinator, decided by its task's domain and its kind at the
// drain and stored on no row. The subordinate's own task's worker is ended,
// which the loop surfaces as the actable terminal-gone, and the subordinate
// asks a promotion of it, which the loop raises as the decision-class
// escalation; the coordinator's launched worker is ended too. The
// subordinate's drain hands out its domain's terminal-gone alone, recorded
// as handed to its member, and the coordinator's drain hands out the
// escalation and the undelegated task's terminal-gone and not the
// subordinate's row; a wait armed by the coordinator answers nothing of the
// subordinate's row either. The subordinate acknowledges its row as its
// member, is refused the escalation as a kind the first coordinator acts
// on, and is refused the undelegated task's row as outside its domain.
// A second task of the domain is ended the same way, and its terminal-gone
// is handed to the subordinate alone while the delegation stands. Then the
// delegation is ended from the coordinator's control, which ends the
// subordinate's session, and the next drain by the coordinator is offered
// the domain's rows: the second task's row, handed to the subordinate and
// never acknowledged, is handed to the coordinator.
func TestAWakeReachesExactlyOneCoordinatorByItsTasksDomainAndKind(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P14)
	launchedWorker(t, "task/routed", func(w launched) {
		d := delegateDomain(t, w, "alpha", "gated")
		own := createdBy(t, d, machine.TaskCreateRequest{
			Shape: "investigation", Project: "alpha look", Instructions: "Look into it.\n", Repository: w.repository.ID,
		}).Record.ID
		waitRunning(t, w, own)
		if _, code := callAs(t, d.as, ipc.MethodTaskPromote, machine.TaskPromoteRequest{
			Task: own, Repository: w.repository.ID, Branch: "task/alpha-p", Intent: "ship it",
		}); code != ipc.CodeSubordinate {
			t.Fatalf("task.promote from the subordinate answered %q, want %q", code, ipc.CodeSubordinate)
		}
		finishWorker(t, w, own)
		finishWorker(t, w, w.task)

		// The subordinate is offered its domain's actable row and the
		// coordinator everything else; polled until the loop has surfaced
		// all three.
		var gone, escalated, base int64
		deadline := time.Now().Add(60 * time.Second)
		for gone == 0 || escalated == 0 || base == 0 {
			if !time.Now().Before(deadline) {
				t.Fatalf("the loop surfaced gone=%d escalated=%d base=%d within the bound, want all three", gone, escalated, base)
			}
			sub := drainedBy(t, d)
			for _, wake := range sub.Wakes {
				if wake.TaskID != own || wake.Kind != string(supervisor.KindTerminalGone) {
					t.Fatalf("the subordinate's drain handed out %+v, want only its domain's terminal-gone", wake)
				}
				gone = wake.ID
			}
			top := mustList(t, w.running.client)
			for _, wake := range top.Wakes {
				if wake.TaskID == own && wake.Kind == string(supervisor.KindTerminalGone) {
					t.Fatalf("the coordinator's drain handed out the subordinate's row %+v", wake)
				}
			}
			if id := wakeOf(top, own, supervisor.KindSubordinateEscalated); id != 0 {
				escalated = id
			}
			if id := wakeOf(top, w.task, supervisor.KindTerminalGone); id != 0 {
				base = id
			}
			time.Sleep(100 * time.Millisecond)
		}
		if row := wakeRow(t, recordsOf(t, w.home), own, gone); row.HandedOutTo != store.Known(store.AcknowledgedBySubordinate("alpha")) {
			t.Fatalf("the records hold %+v for the subordinate's row, want it handed to the subordinate's member", row)
		}
		if waited := mustWait(t, w.running.client, 1); listed(waited, gone) {
			t.Fatalf("a wait armed by the coordinator answered the subordinate's row: %+v", waited)
		}

		// The subordinate closes its own row and no other.
		if _, code := callAs(t, d.as, ipc.MethodWakesAcknowledge, machine.AcknowledgeRequest{Wakes: []int64{base}}); code != ipc.CodeOutsideDomain {
			t.Fatalf("acknowledging the undelegated task's row answered %q, want %q", code, ipc.CodeOutsideDomain)
		}
		if _, code := callAs(t, d.as, ipc.MethodWakesAcknowledge, machine.AcknowledgeRequest{Wakes: []int64{escalated}}); code != ipc.CodeSubordinate {
			t.Fatalf("acknowledging the escalation answered %q, want %q", code, ipc.CodeSubordinate)
		}
		if _, code := callAs(t, d.as, ipc.MethodWakesAcknowledge, machine.AcknowledgeRequest{Wakes: []int64{gone}}); code != "" {
			t.Fatalf("the subordinate's acknowledgement was refused with %q", code)
		}
		if row := wakeRow(t, recordsOf(t, w.home), own, gone); row.Queued() || row.AcknowledgedBy != store.Known(store.AcknowledgedBySubordinate("alpha")) {
			t.Fatalf("the records hold %+v, want the row closed by the subordinate's member", row)
		}
		for _, left := range []struct {
			task string
			id   int64
		}{{own, escalated}, {w.task, base}} {
			if row := wakeRow(t, recordsOf(t, w.home), left.task, left.id); !row.Queued() {
				t.Fatalf("wake %d was closed by a refused acknowledgement: %+v", left.id, row)
			}
		}

		// A second task of the domain, whose worker is ended too: its
		// terminal-gone is handed to the subordinate while the delegation
		// stands and left unacknowledged, and the coordinator's drain does
		// not offer it. It needs no sweep, which a relaunch would, so the
		// step runs on a host whose sweep refuses to clear.
		second := createdBy(t, d, machine.TaskCreateRequest{
			Shape: "investigation", Project: "alpha second look", Instructions: "Look again.\n", Repository: w.repository.ID,
		}).Record.ID
		waitRunning(t, w, second)
		finishWorker(t, w, second)
		var kept int64
		deadline = time.Now().Add(60 * time.Second)
		for kept == 0 {
			if !time.Now().Before(deadline) {
				t.Fatal("the loop surfaced no terminal-gone for the second task of the domain")
			}
			kept = wakeOf(drainedBy(t, d), second, supervisor.KindTerminalGone)
			if wakeOf(mustList(t, w.running.client), second, supervisor.KindTerminalGone) != 0 {
				t.Fatal("the coordinator's drain handed out the second task's row while the delegation stood")
			}
			time.Sleep(100 * time.Millisecond)
		}

		// After the end, the domain's rows are the coordinator's: the next
		// drain hands out the row the subordinate was handed and never
		// acknowledged, recorded as handed to the coordinator.
		var ended machine.DelegationEnded
		if err := w.running.client.Call(t.Context(), ipc.MethodDomainEnd, machine.DomainEndRequest{Domain: "alpha"}, &ended); err != nil {
			t.Fatalf("domain.end: %v", err)
		}
		if ended.Record.Standing() || !ended.SessionEnded {
			t.Fatalf("domain.end answered %+v, want the delegation ended and the session ended", ended)
		}
		if _, _, held := w.running.service.SubordinateSessionOf("alpha"); held {
			t.Fatal("the service still holds the ended subordinate's session")
		}
		if top := mustList(t, w.running.client); !handedOut(top, kept, 2) {
			t.Fatalf("after the end the coordinator's drain = %+v, want the domain's row %d handed out a second time", top, kept)
		}
		if row := wakeRow(t, recordsOf(t, w.home), second, kept); row.HandedOutTo != store.Known(store.AcknowledgedByCoordinator()) || !row.Queued() {
			t.Fatalf("after the end the domain's row reads %+v, want it handed to the coordinator and still queued", row)
		}
	})
}

// A restart re-probes a standing delegation's session on the same terms as
// a task's: the first service is closed, which ends the subordinate it
// started, so its recorded reference probes Dead by identity; the second
// service, with relaunch_attempts at one, relaunches the subordinate as
// attempt 2 with a session of its own and holds it; the third, finding the
// delegation relaunched once already, starts nothing and leaves the
// delegation standing, which ownership never transferring means. The
// delegation's record is read back from the store at each step rather than
// taken from any answer.
func TestADeadSubordinateIsRelaunchedOnRestartUnderTheBound(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	quietMaintenance(t, subject)
	repository := recordRepository(t, h, subject)
	quietMaintenance(t, gateRepositoryOf(t, h, subject))
	if err := os.WriteFile(h.ConfigFile(), []byte(`{"supervision_beacon_interval": "1s", "relaunch_attempts": 1}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var delegation store.Delegation
	var first string
	o, _ := workerOptions(t, h)
	withServiceOptions(t, o, func(running serviceUnderTest) {
		w := launched{running: running, home: h, repository: repository}
		d := delegateDomain(t, w, "alpha", "gated")
		first = d.as.session.Reference().String()
		latest, found, err := recordsOf(t, h).Delegation(t.Context(), "alpha")
		if err != nil || !found {
			t.Fatal(err)
		}
		delegation = latest
	})
	o, _ = workerOptions(t, h)
	withServiceOptions(t, o, func(running serviceUnderTest) {
		host, sub, held := running.service.SubordinateSessionOf("alpha")
		deadline := time.Now().Add(launchWait)
		for !held {
			if !time.Now().Before(deadline) {
				t.Fatal("the second service never relaunched the dead subordinate")
			}
			time.Sleep(20 * time.Millisecond)
			host, sub, held = running.service.SubordinateSessionOf("alpha")
		}
		waitCapture(t, host, sub, "worker ready")
		attempts, err := recordsOf(t, h).DelegationAttempts(t.Context(), delegation.ID)
		if err != nil || len(attempts) != 2 || attempts[1].SessionRef.Or("") != sub.Reference().String() || attempts[1].SessionRef.Or("") == first {
			t.Fatalf("after the restart the attempts are %+v (%v), want a second naming the relaunched session", attempts, err)
		}
		if latest, _, err := recordsOf(t, h).Delegation(t.Context(), "alpha"); err != nil || latest.ID != delegation.ID || !latest.Standing() {
			t.Fatalf("after the restart the delegation reads %+v (%v), want the same row standing", latest, err)
		}
	})
	o, _ = workerOptions(t, h)
	withServiceOptions(t, o, func(running serviceUnderTest) {
		// Past the bound the third service relaunches nothing and holds
		// the delegation for a person instead: the hold is read from its
		// row, keyed by the attempt that reached the bound (P8), and a
		// retry answered on it through task.respond given the domain
		// begins the subordinate once more as attempt 3, which spends the
		// answer.
		principles.Cite(t, principles.P8)
		key := "domain/alpha/relaunch-bound/2"
		deadline := time.Now().Add(launchWait)
		for {
			if hold, err := recordsOf(t, h).Hold(t.Context(), key); err == nil && hold.Open() {
				break
			}
			if !time.Now().Before(deadline) {
				t.Fatalf("the third service never registered the bound's hold %s", key)
			}
			time.Sleep(20 * time.Millisecond)
		}
		if _, _, held := running.service.SubordinateSessionOf("alpha"); held {
			t.Fatal("the third service relaunched the subordinate past the bound")
		}
		attempts, err := recordsOf(t, h).DelegationAttempts(t.Context(), delegation.ID)
		if err != nil || len(attempts) != 2 {
			t.Fatalf("past the bound the attempts are %+v (%v), want still two", attempts, err)
		}
		if latest, _, err := recordsOf(t, h).Delegation(t.Context(), "alpha"); err != nil || !latest.Standing() {
			t.Fatalf("past the bound the delegation reads %+v (%v), want it still standing", latest, err)
		}
		var answered machine.TaskAnswered
		if err := running.client.Call(t.Context(), ipc.MethodTaskRespond, machine.TaskRespondRequest{Domain: "alpha", Answer: machine.AnswerRetry}, &answered); err != nil {
			t.Fatalf("answering the domain's hold: %v", err)
		}
		if answered.Domain != "alpha" || answered.Effect != machine.AnswerRelaunched || answered.Relaunch == nil || answered.Relaunch.Attempt != 3 ||
			answered.Hold.Key != key || answered.Hold.Open() {
			t.Fatalf("the retry answered %+v, want attempt 3 begun on the closed hold %s", answered, key)
		}
		host, sub, held := running.service.SubordinateSessionOf("alpha")
		deadline = time.Now().Add(launchWait)
		for !held {
			if !time.Now().Before(deadline) {
				t.Fatal("the retry never started the subordinate again")
			}
			time.Sleep(20 * time.Millisecond)
			host, sub, held = running.service.SubordinateSessionOf("alpha")
		}
		waitCapture(t, host, sub, "worker ready")
		attempts, err = recordsOf(t, h).DelegationAttempts(t.Context(), delegation.ID)
		if err != nil || len(attempts) != 3 || attempts[2].SessionRef.Or("") != sub.Reference().String() {
			t.Fatalf("after the retry the attempts are %+v (%v), want a third naming the new session", attempts, err)
		}
		var again machine.TaskAnswered
		if err := running.client.Call(t.Context(), ipc.MethodTaskRespond, machine.TaskRespondRequest{Domain: "alpha", Answer: machine.AnswerRetry}, &again); !errors.Is(err, ipc.ErrRefused) {
			t.Fatalf("a second retry on a domain holding on nothing = %v, want ErrRefused", err)
		}
	})
}

// A standing delegation whose subordinate's terminal is gone is told to the
// first coordinator on the queue's second shape, and never to a
// subordinate: with relaunch_attempts at zero, alpha's subordinate is ended
// through the handle the service observes it by, and the loop surfaces
// subordinate-gone and, the delegation being past the bound at once, the
// bound's hold as a hold-opened domain wake, both read back from the
// records (P8). The coordinator's drain hands both out under their own
// numbering, beta's live subordinate drains none, a subordinate is refused
// closing one, and the coordinator closes both. Then the hold is answered
// stop through task.respond given the domain, which ends the delegation by
// the answer, read off the delegation's row, and the subordinate it would
// have begun again is not.
func TestAGoneSubordinateReachesTheCoordinatorOnTheSecondShapeAndAStopEndsTheDelegation(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P8)
	launchedWorkerConfigured(t, store.TaskDelivery, "task/domain-gone", store.Unknown[config.Delivery](),
		`{"supervision_beacon_interval": "1s", "relaunch_attempts": 0}`, func(w launched) {
			alpha := delegateDomain(t, w, "alpha", "gated")
			beta := delegateDomain(t, w, "beta", "gated")
			delegation, _, err := recordsOf(t, w.home).Delegation(t.Context(), "alpha")
			if err != nil {
				t.Fatal(err)
			}
			if err := alpha.as.provider.Close(alpha.as.session); err != nil {
				t.Fatalf("ending alpha's subordinate: %v", err)
			}
			key := "domain/alpha/relaunch-bound/1"
			var gone, hold int64
			deadline := time.Now().Add(60 * time.Second)
			for gone == 0 || hold == 0 {
				if !time.Now().Before(deadline) {
					t.Fatalf("the loop surfaced gone=%d hold=%d within the bound, want both", gone, hold)
				}
				for _, dw := range mustList(t, w.running.client).DomainWakes {
					if dw.Domain != "alpha" {
						t.Fatalf("the coordinator's drain handed out a domain wake about %s: %+v", dw.Domain, dw)
					}
					switch dw.Kind {
					case string(supervisor.KindSubordinateGone):
						gone = dw.ID
					case string(supervisor.KindHoldOpened):
						hold = dw.ID
					}
				}
				time.Sleep(100 * time.Millisecond)
			}
			if h, err := recordsOf(t, w.home).Hold(t.Context(), key); err != nil || !h.Open() {
				t.Fatalf("the bound's hold reads %+v (%v), want it open", h, err)
			}
			rows, err := recordsOf(t, w.home).DelegationWakes(t.Context(), delegation.ID)
			if err != nil || len(rows) < 2 {
				t.Fatalf("the delegation's wakes are %+v (%v), want the two", rows, err)
			}
			for _, row := range rows {
				if row.HandedOutTo != store.Known(store.AcknowledgedByCoordinator()) || !row.Queued() {
					t.Fatalf("domain wake %+v was handed to %v, want the coordinator and still queued", row, row.HandedOutTo)
				}
			}
			if drained := drainedBy(t, beta); len(drained.DomainWakes) != 0 {
				t.Fatalf("beta's subordinate was handed a wake about a domain: %+v", drained.DomainWakes)
			}
			if _, code := callAs(t, beta.as, ipc.MethodWakesAcknowledge, machine.AcknowledgeRequest{DomainWakes: []int64{gone}}); code != ipc.CodeSubordinate {
				t.Fatalf("a subordinate acknowledging a domain wake answered %q, want %q", code, ipc.CodeSubordinate)
			}
			var closed machine.Acknowledged
			if err := w.running.client.Call(t.Context(), ipc.MethodWakesAcknowledge, machine.AcknowledgeRequest{DomainWakes: []int64{gone, hold}}, &closed); err != nil {
				t.Fatalf("the coordinator's acknowledgement: %v", err)
			}
			if listed := mustList(t, w.running.client); len(listed.DomainWakes) != 0 {
				t.Fatalf("after the acknowledgement the drain handed out %+v", listed.DomainWakes)
			}
			if _, code := callAs(t, beta.as, ipc.MethodTaskRespond, machine.TaskRespondRequest{Domain: "alpha", Answer: machine.AnswerStop}); code != ipc.CodeSubordinate {
				t.Fatalf("a subordinate answering a domain's hold answered %q, want %q", code, ipc.CodeSubordinate)
			}
			var answered machine.TaskAnswered
			if err := w.running.client.Call(t.Context(), ipc.MethodTaskRespond, machine.TaskRespondRequest{Domain: "alpha", Answer: machine.AnswerStop, Note: "take it back"}, &answered); err != nil {
				t.Fatalf("answering the domain's hold: %v", err)
			}
			if answered.Domain != "alpha" || answered.Effect != machine.AnswerStopped || answered.Hold.Key != key || answered.Hold.ResolutionNote.Or("") != "take it back" {
				t.Fatalf("the stop answered %+v, want the delegation stopped on hold %s with the note", answered, key)
			}
			ended, _, err := recordsOf(t, w.home).Delegation(t.Context(), "alpha")
			if err != nil || ended.Standing() || ended.EndedBy != store.Known(store.EndedByAnswer()) {
				t.Fatalf("after the stop the delegation reads %+v (%v), want it ended by the answer", ended, err)
			}
			if _, _, held := w.running.service.SubordinateSessionOf("alpha"); held {
				t.Fatal("the service still holds a session for the ended domain")
			}
		})
}
