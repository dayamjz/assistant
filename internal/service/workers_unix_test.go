//go:build unix

package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
)

// These tests make requests from inside a real worker's process group. The
// worker is the stand-in in worker mode, launched by the service for a
// dispatched task exactly as launch_test.go has it launched, and a request
// from it is a child process this test places in the worker's group with
// setpgid before it runs: a git push from the task's copy, whose hooks reach
// the service through the gate as a person's would, or this binary making
// one call. The credentials the service reads are then that child's, which
// the kernel attributes and nothing here states, and the group they resolve
// to is the one the real adapter started the worker in. A membership
// written by hand reaches nothing in any of them.

// inGroup is a command placed in a process group, which has to be one a
// process in this session leads.
func inGroup(ctx context.Context, group int, dir, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: group}
	cmd.WaitDelay = 5 * time.Second
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.invalid",
	)
	return cmd
}

// gitAs runs git in dir from inside a process group and returns its output
// and error, for a push whose admission is the point.
func gitAs(t *testing.T, group int, dir string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	out, err := inGroup(ctx, group, dir, "git", args...).CombinedOutput()
	return string(out), err
}

// callAs makes one call on the service's socket from a process inside a
// group, through callMain, and returns the answer or the wire code the call
// was refused with.
func callAs(t *testing.T, group int, running *service.Service, method ipc.Method, params any) (json.RawMessage, ipc.Code) {
	t.Helper()
	body, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	cmd := inGroup(ctx, group, t.TempDir(), testCommand(t), callVerb, running.Socket(), string(method), string(body))
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return json.RawMessage(stdout.String()), ""
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return nil, ipc.Code(stderr.String())
	default:
		t.Fatalf("calling %s from group %d: %v\n%s%s", method, group, err, stdout.String(), stderr.String())
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
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	quietMaintenance(t, subject)
	repository := recordRepository(t, h, subject)
	quietMaintenance(t, gateRepositoryOf(t, h, subject))
	if err := os.WriteFile(h.ConfigFile(), []byte(`{"supervision_beacon_interval": "1s"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := recordDispatchedTask(t, h, repository, shape, branch)
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
// push: the same git, from the same copy, in the worker's process group,
// creates the task's branch and is admitted while a stage is registered, so
// containment was asked and did not place it; pushes the branch under
// another name, deletes it, and rewinds it, and each is refused before any
// reference moves; and a person's push of the same branch, from outside
// every worker group, is held to nothing new, rewinding it included. The
// gate's references are read with git after each push rather than taken from
// the answer.
func TestAWorkersPushIsHeldToItsOwnBranchForwardAndAPersonsIsNot(t *testing.T) {
	launchedWorker(t, "task/one", func(w launched) {
		running, task, group, gateRepo := w.running, w.task, w.group, w.gateRepo
		h := running.service
		copyPath := resolvedCopy(t, w.home, task)
		quietMaintenance(t, copyPath)
		if err := os.WriteFile(filepath.Join(copyPath, "work.txt"), []byte("the worker's change\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		mustGitAs(t, group, copyPath, "add", "-A")
		mustGitAs(t, group, copyPath, "commit", "--quiet", "-m", "the worker's change")
		head := mustGitAs(t, group, copyPath, "rev-parse", "HEAD")

		// A stage is registered, so admission's containment check reads the
		// pusher's group rather than answering no without looking, and the
		// worker's group is not the one registered.
		forget := h.StageStarted("run-under-validation", "review", 424242)
		defer forget()

		out, err := gitAs(t, group, copyPath, "push", gate.RemoteName, "task/one")
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
			out, err := gitAs(t, group, copyPath, push.args...)
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
		mustGitAs(t, group, copyPath, "commit", "--quiet", "--amend", "-m", "the worker's change, rewritten")
		out, err = gitAs(t, group, copyPath, "push", "--force", gate.RemoteName, "task/one")
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
		running, task, group := w.running, w.task, w.group
		answer, code := callAs(t, group, running.service, ipc.MethodTaskEvent, machine.TaskEventRequest{Kind: "working", Detail: "on it"})
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
		if _, code := callAs(t, group, running.service, ipc.MethodTaskEvent, machine.TaskEventRequest{Kind: store.DispatchedEventKind}); code != ipc.CodeWorker {
			t.Fatalf("the worker appended %q, refused with %q, want %q", store.DispatchedEventKind, code, ipc.CodeWorker)
		}
		if _, code := callAs(t, group, running.service, ipc.MethodTaskEvent, machine.TaskEventRequest{Kind: "promoted-myself"}); code != ipc.CodeInvalidRequest {
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

// The queue and arm verification are the coordinator's. A drain from outside
// every worker group returns what the loop surfaced, the acknowledgement
// records the coordinator as the derived acknowledger and takes the rows out
// of the next drain, arm verification answers from the real beacon the
// service's own loop writes, and every one of the three is refused from the
// worker's group. The wake drained is one the service's supervisor loop
// surfaced for the launched task, not one written for the test: the loop
// observes the worker through the service's own provider, so a live worker
// is absorbed on its handle's evidence and nothing is queued for it; the
// test ends the worker through that handle, and the loop's next pass
// surfaces its terminal as gone. The refusals from the worker's group are
// asked first, while the group still leads a live worker.
func TestTheQueueAndArmVerificationAreTheCoordinatorsAndNotAWorkers(t *testing.T) {
	launchedWorker(t, "task/three", func(w launched) {
		running, task, group := w.running, w.task, w.group
		for _, method := range []ipc.Method{ipc.MethodWakesList, ipc.MethodWakesAcknowledge, ipc.MethodSupervisionAssert} {
			params := any(struct{}{})
			if method == ipc.MethodWakesAcknowledge {
				params = machine.AcknowledgeRequest{Wakes: []int64{1}}
			}
			if _, code := callAs(t, group, running.service, method, params); code != ipc.CodeWorker {
				t.Fatalf("%s from the worker's group answered %q, want %q", method, code, ipc.CodeWorker)
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
			if w.TaskID != task || !w.Queued() {
				t.Fatalf("the drain returned %+v, want a queued wake of task %s", w, task)
			}
			ids = append(ids, w.ID)
		}
		var again machine.Wakes
		if err := running.client.Call(t.Context(), ipc.MethodWakesList, machine.WakesRequest{}, &again); err != nil || len(again.Wakes) < len(ids) {
			t.Fatalf("a second drain without an acknowledgement returned %+v (%v), want the same rows again", again, err)
		}

		var acknowledged machine.Acknowledged
		if err := running.client.Call(t.Context(), ipc.MethodWakesAcknowledge, machine.AcknowledgeRequest{Wakes: ids}, &acknowledged); err != nil {
			t.Fatalf("wakes.acknowledge: %v", err)
		}
		if acknowledged.By != store.AcknowledgedByCoordinator().String() {
			t.Fatalf("the acknowledgement records %q, want the coordinator", acknowledged.By)
		}
		var after machine.Wakes
		if err := running.client.Call(t.Context(), ipc.MethodWakesList, machine.WakesRequest{}, &after); err != nil {
			t.Fatal(err)
		}
		for _, w := range after.Wakes {
			for _, id := range ids {
				if w.ID == id {
					t.Fatalf("wake %d was acknowledged and is still drained", id)
				}
			}
		}
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

// mustGitAs is gitAs failing the test on an error.
func mustGitAs(t *testing.T, group int, dir string, args ...string) string {
	t.Helper()
	out, err := gitAs(t, group, dir, args...)
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
// is refused to the task's own worker from its process group with
// ErrWorker and stays open, which is what keeps an investigation from
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
		if _, code := callAs(t, w.group, w.running.service, ipc.MethodTaskRespond, request); code != ipc.CodeWorker {
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
