//go:build unix

package service_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/store"
)

// tmuxGlobal is the home's configuration that puts the service's workers in
// tmux, with the beacon interval launchedWorker uses.
const tmuxGlobal = `{"supervision_beacon_interval": "1s", "session_provider": "tmux"}`

// launchedTmuxWorker is launchedWorker with the operator's file choosing the
// tmux provider, which is the only difference: the task, the copy, the
// dispatch and the launch are the same path.
func launchedTmuxWorker(t *testing.T, branch string, body func(w launched)) {
	t.Helper()
	standin.Tmux(t)
	launchedWorkerConfigured(t, store.TaskDelivery, branch, store.Unknown[config.Delivery](), tmuxGlobal, body)
}

// workerSocket is the socket of the launched worker's tmux server, read off
// the attach command the service's provider answers.
func workerSocket(t *testing.T, w launched) string {
	t.Helper()
	host, ok := w.provider.(session.Host)
	if !ok {
		t.Fatalf("the service's provider %T is not a session.Host", w.provider)
	}
	attach, err := host.AttachReference(w.session.Reference().String())
	if err != nil {
		t.Fatalf("AttachReference: %v", err)
	}
	i := slices.Index(attach, "-S")
	if i < 0 || i+1 >= len(attach) {
		t.Fatalf("the attach command %q names no socket", attach)
	}
	return attach[i+1]
}

// callFromWindow has a shell in a new window of the worker's own tmux server
// make one call on the service's socket, the way a person who opened a
// window there would, and returns the answer or the wire code it was refused
// with. The window is the server's child and the worker's sibling.
func callFromWindow(t *testing.T, w launched, method ipc.Method, params any) (json.RawMessage, ipc.Code) {
	t.Helper()
	body, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "params.json")
	if err := os.WriteFile(file, body, 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut, code := filepath.Join(dir, "out"), filepath.Join(dir, "err"), filepath.Join(dir, "code")
	script := shellQuote(testCommand(t), callVerb, w.running.service.Socket(), string(method)) +
		` "$(cat ` + shellQuote(file) + `)" > ` + shellQuote(out) + ` 2> ` + shellQuote(errOut) +
		`; echo $? > ` + shellQuote(code+".tmp") + ` && mv ` + shellQuote(code+".tmp") + ` ` + shellQuote(code)
	if raw, err := exec.Command(standin.Tmux(t), "-S", workerSocket(t, w), "new-window", "-d", "--",
		"/bin/sh", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("opening a window in the worker's server: %v: %s", err, raw)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		raw, err := os.ReadFile(code)
		if err == nil {
			status, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
			stdout, _ := os.ReadFile(out)
			stderr, _ := os.ReadFile(errOut)
			switch status {
			case 0:
				return stdout, ""
			case 1:
				return nil, ipc.Code(strings.TrimSpace(string(stderr)))
			default:
				t.Fatalf("calling %s from a window of the worker's server exited %d: %s", method, status, stderr)
			}
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the call from a window of the worker's server never finished")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Under the tmux provider the service places a worker's request as the
// worker's, decided from the socket's credentials and the worker set the
// tmux provider produces, though the worker is the tmux server's child and
// not this service's: a call the worker runs in a process group of its own
// appends to its own task's history and is refused the coordinator's queue.
// A shell a person opens in a new window of the same server is placed as no
// worker: its task.event is refused as not a worker's, and the queue answers
// it. That pair is what makes registering the pane's process, and not the
// server, the right membership: the window is the server's child too.
func TestUnderTmuxTheServicePlacesTheWorkerAndNotAPersonsWindow(t *testing.T) {
	launchedTmuxWorker(t, "task/tmux-one", func(w launched) {
		if _, ok := w.provider.(*session.Tmux); !ok {
			t.Fatalf("the service launched through %T, want the tmux provider the operator's file chose", w.provider)
		}
		lineage, found, err := agents.ReadLineage(w.group)
		if err != nil || !found {
			t.Fatalf("reading the worker's lineage: %v, %v", found, err)
		}
		if lineage.Parent == os.Getpid() {
			t.Fatal("the worker is the service's child, so this is the pipe provider's shape and not tmux's")
		}

		answer, code := callAs(t, w, ipc.MethodTaskEvent, machine.TaskEventRequest{Kind: "working", Detail: "in tmux"})
		if code != "" {
			t.Fatalf("the tmux-hosted worker's event was refused with %q", code)
		}
		var appended machine.Appended
		if err := json.Unmarshal(answer, &appended); err != nil {
			t.Fatalf("the answer does not decode: %v\n%s", err, answer)
		}
		if appended.Event.TaskID != w.task || appended.Event.Detail != "in tmux" {
			t.Fatalf("the worker's event was recorded as %+v, want an entry of task %s", appended.Event, w.task)
		}
		if _, code := callAs(t, w, ipc.MethodWakesList, struct{}{}); code != ipc.CodeWorker {
			t.Fatalf("wakes.list from the tmux-hosted worker answered %q, want %q", code, ipc.CodeWorker)
		}

		if _, code := callFromWindow(t, w, ipc.MethodTaskEvent, machine.TaskEventRequest{Kind: "working"}); code != ipc.CodeNotWorker {
			t.Fatalf("task.event from a person's window on the worker's server answered %q, want %q", code, ipc.CodeNotWorker)
		}
		if _, code := callFromWindow(t, w, ipc.MethodWakesList, struct{}{}); code != "" {
			t.Fatalf("wakes.list from a person's window on the worker's server was refused with %q", code)
		}
	})
}

// A restart under the tmux provider reconciles a task the way it reconciles
// one under the pipe provider, because what reconciliation decides from is
// the probe and the probe's rule is the providers' one shared rule: the
// first service's worker is ended with it, so the new service's tmux
// provider answers the first attempt's reference Dead by identity, claims a
// second attempt, and relaunches in the standing copy, in a tmux server of
// its own, with a new reference, through which the task then reads Running.
func TestARestartUnderTmuxRelaunchesADeadTerminalInPlace(t *testing.T) {
	service.RequireSweepClearsHere(t, "a restart under tmux relaunching a dead terminal in place")
	standin.Tmux(t)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	if err := os.WriteFile(h.ConfigFile(), []byte(tmuxGlobal+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	subject := newSubject(t)
	quietMaintenance(t, subject)
	repository := recordRepository(t, h, subject)
	id := recordDispatchedTask(t, h, repository, store.TaskDelivery, "task/tmux-two")
	first := launchedAndEnded(t, h, id)

	o, _ := workerOptions(t, h)
	var second session.Reference
	withServiceOptions(t, o, func(running serviceUnderTest) {
		provider, launched := waitSession(t, running.service, id)
		if _, ok := provider.(*session.Tmux); !ok {
			t.Fatalf("the restarted service relaunched through %T, want the tmux provider", provider)
		}
		second = launched.Reference()
		waitCapture(t, provider, launched, "worker ready")
		if got := taskState(t, running.client, id); !readsRunning(got) {
			t.Fatalf("after the relaunch the task resolves %+v, want Running through its new tmux session", got)
		}
	})
	if first == second {
		t.Fatalf("the relaunch reused the first attempt's reference %s", first)
	}
	records := openRecords(t, h)
	defer func() { _ = records.Close() }()
	attempts, err := records.TaskAttempts(t.Context(), id)
	if err != nil || len(attempts) != 2 {
		t.Fatalf("the task has attempts %+v (%v), want the launch's and the relaunch's", attempts, err)
	}
	if attempts[0].SessionRef.Or("") != first.String() || attempts[1].SessionRef.Or("") != second.String() {
		t.Fatalf("the attempts carry %+v and %+v, want %s then %s", attempts[0].SessionRef, attempts[1].SessionRef, first, second)
	}
	events, err := records.TaskEvents(t.Context(), id, 0, 0)
	if err != nil || len(events) < 2 || events[len(events)-2].Kind != "relaunched" {
		t.Fatalf("the history is %+v (%v), want it to end with the relaunch", events, err)
	}
}

// task.peek under the tmux provider: the coordinator reads the worker's
// recent output with the command a person attaches with, redacted before it
// is abbreviated and bounded, within machine.MaxPeekBytes and with what an
// abbreviation cut counted;
// the worker itself is refused the read, and a person's window on the
// worker's own server is served it, which is the placement rule above
// applied to one more coordinator method.
func TestUnderTmuxAPeekIsTheCoordinatorsRedactedAndBounded(t *testing.T) {
	launchedTmuxWorker(t, "task/tmux-three", func(w launched) {
		const secret = "hunter2-not-for-you"
		// The credential is printed last, so the bound keeps it and only the
		// redaction can take it out.
		ran := asWorker(t, w, `seq 1 6000; printf 'pushing to https://someone:`+secret+`@example.invalid/repo\n'`)
		if !strings.Contains(ran.Stdout, secret) {
			t.Fatalf("the worker's command did not print the credential, so redaction is not what is tested: %+v", ran)
		}

		var peeked machine.TaskPeek
		if err := w.running.client.Call(t.Context(), ipc.MethodTaskPeek, machine.TaskRequest{Task: w.task}, &peeked); err != nil {
			t.Fatalf("task.peek: %v", err)
		}
		if peeked.Task != w.task || peeked.Session != w.session.Reference().String() || peeked.Liveness != "live" {
			t.Fatalf("task.peek answered %+v, want the live session %s of task %s", peeked, w.session.Reference(), w.task)
		}
		if strings.Contains(peeked.Output, secret) {
			t.Fatal("task.peek answered the worker's output with the credential in it")
		}
		if !strings.Contains(peeked.Output, "example.invalid") {
			t.Fatalf("task.peek's output does not carry the line the credential was in, so its absence proves nothing; it ends:\n%s",
				peeked.Output[max(0, len(peeked.Output)-400):])
		}
		// The command's answer is one line of many kilobytes, so what bounds
		// it is the abbreviation, which says what it let go.
		cut := false
		for _, e := range peeked.Events {
			cut = cut || strings.HasSuffix(e.Text, "bytes more)")
		}
		if len(peeked.Output) > machine.MaxPeekBytes || !cut {
			t.Fatalf("task.peek answered %d bytes with no event saying what it cut, want at most %d and the cut counted",
				len(peeked.Output), machine.MaxPeekBytes)
		}
		host := w.provider.(session.Host)
		raw, err := host.CaptureReference(t.Context(), w.session.Reference().String())
		if err != nil || !strings.Contains(raw.Text, secret) {
			t.Fatalf("the provider's own capture does not carry the credential (%v), so the peek's lacking it is not redaction's doing", err)
		}
		attach, err := host.AttachReference(w.session.Reference().String())
		if err != nil || !slices.Equal(peeked.Attach, attach) {
			t.Fatalf("task.peek names attach command %q, want the provider's %q (%v)", peeked.Attach, attach, err)
		}

		if _, code := callAs(t, w, ipc.MethodTaskPeek, machine.TaskRequest{Task: w.task}); code != ipc.CodeWorker {
			t.Fatalf("task.peek from the worker answered %q, want %q", code, ipc.CodeWorker)
		}
		if _, code := callFromWindow(t, w, ipc.MethodTaskPeek, machine.TaskRequest{Task: w.task}); code != "" {
			t.Fatalf("task.peek from a person's window on the worker's server was refused with %q", code)
		}
	})
}
