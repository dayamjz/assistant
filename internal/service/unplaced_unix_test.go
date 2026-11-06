//go:build unix

package service_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
)

// unplaced is the wire code a coordinator method refuses an orphan with.
const unplaced = ipc.Code("unplaced")

// detach is `setsid -f` for a platform that ships no setsid command: it
// forks, the parent exits at once, and the child starts a session of its
// own, with no controlling terminal, and runs the rest of its arguments.
// What the child runs is then the kernel's to reparent, the shape a worker
// that detaches a command of its own leaves behind.
const detach = `perl -MPOSIX -e 'exit 0 if fork; POSIX::setsid() >= 0 or die "setsid: $!"; exec @ARGV or die "exec: $!"'`

// orphanedCall is one call on the service's socket from a process the
// kernel has reparented: start runs the script it is handed from somewhere
// that then lets go of it, and the script records its own identifier, waits
// to be released, and makes the call. The process is read back off the
// kernel before it is released, and the test fails unless its chain has no
// terminal's foreground on it and reaches none of above, so the shape is the
// one under test and not one the walk would place anyway. It returns the
// wire code the call was refused with, and the refusal's message, or an
// empty code for an answer.
func orphanedCall(t *testing.T, socket string, method ipc.Method, params any, above []int, start func(script string)) (ipc.Code, string) {
	t.Helper()
	body, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file, pid, release := filepath.Join(dir, "params.json"), filepath.Join(dir, "pid"), filepath.Join(dir, "release")
	out, errOut, code := filepath.Join(dir, "out"), filepath.Join(dir, "err"), filepath.Join(dir, "code")
	if err := os.WriteFile(file, body, 0o600); err != nil {
		t.Fatal(err)
	}
	script := "echo $$ > " + shellQuote(pid+".tmp") + " && mv " + shellQuote(pid+".tmp") + " " + shellQuote(pid) + "; " +
		standin.HeldWait(t, release) + " && " +
		shellQuote(testCommand(t), callVerb, socket, string(method)) + ` "$(cat ` + shellQuote(file) + `)" > ` +
		shellQuote(out) + " 2> " + shellQuote(errOut) +
		"; echo $? > " + shellQuote(code+".tmp") + " && mv " + shellQuote(code+".tmp") + " " + shellQuote(code)
	start(script)
	orphan := readNumber(t, pid)
	awaitOffChain(t, orphan, append(above, os.Getpid()))
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	status := awaitNumber(t, code)
	stdout, _ := os.ReadFile(out)
	stderr, _ := os.ReadFile(errOut)
	switch status {
	case 0:
		return "", string(stdout)
	case 1:
		var wire ipc.Error
		if err := json.Unmarshal(stderr, &wire); err != nil || wire.Code == "" {
			t.Fatalf("%s exited as refused and its standard error is not a wire error with a code (%v): %q", method, err, stderr)
		}
		t.Logf("%s from the orphan was refused with %q: %s", method, wire.Code, wire.Message)
		return wire.Code, wire.Message
	}
	t.Fatalf("calling %s from the orphan exited %d: %s", method, status, stderr)
	return "", ""
}

// awaitOffChain waits until no process on pid's parent chain is one of
// pids and none is in the foreground of its controlling terminal, which is
// the kernel having rewritten the chain away from whoever started pid.
func awaitOffChain(t *testing.T, pid int, pids []int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		reached, why := "", ""
		current, found, err := agents.ReadLineage(pid)
		if err != nil || !found {
			t.Fatalf("process %d is gone before its chain could be read: %v, %v", pid, found, err)
		}
		for {
			for _, p := range pids {
				if current.PID == p {
					reached = "it still descends from process " + strconv.Itoa(p)
				}
			}
			if current.Foreground() {
				why = "process " + strconv.Itoa(current.PID) + " on it is in the foreground of a terminal"
			}
			if current.Parent <= 1 || current.Parent == current.PID {
				break
			}
			parent, found, err := agents.ReadLineage(current.Parent)
			if err != nil || !found {
				break // a parent that exited mid-read: read the chain again
			}
			current = parent
		}
		if why != "" {
			t.Fatalf("the orphan's chain is not the shape under test: %s", why)
		}
		if reached == "" {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("process %d: %s", pid, reached)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A worker's command that detaches itself the way `setsid -f` does, so the
// kernel reparents it away from the worker and it has no terminal, is
// refused the coordinator's methods with the unplaced code and records
// nothing, where it used to be placed as the coordinator's and served
// (#765). The same request from the same worker with its chain standing is
// refused as the worker's, which is the control that the detach is what
// the refusal turns on; and the coordinator's own request, from this
// process at a terminal, is served.
func TestACoordinatorMethodFromAWorkersDetachedCommandIsRefusedUnplaced(t *testing.T) {
	t.Parallel()
	launchedWorker(t, "task/detached", func(w launched) {
		investigation := machine.TaskCreateRequest{
			Shape: "investigation", Project: "one", Instructions: "Find out why the build is slow.\n",
			Repository: w.repository.ID,
		}
		before := taskCount(t, w.home)
		if _, code := callAs(t, w, ipc.MethodTaskCreate, investigation); code != ipc.CodeWorker {
			t.Fatalf("task.create from the worker's own chain answered %q, want %q", code, ipc.CodeWorker)
		}

		code, message := orphanedCall(t, w.running.service.Socket(), ipc.MethodTaskCreate, investigation, []int{w.group},
			func(script string) {
				ran := asWorker(t, w, detach+" /bin/sh -c "+shellQuote(script)+" </dev/null >/dev/null 2>&1")
				if ran.Exit != 0 {
					t.Fatalf("the worker's detach exited %d: %s%s", ran.Exit, ran.Stdout, ran.Stderr)
				}
			})
		if code != unplaced {
			t.Fatalf("task.create from the worker's detached command answered %q (%s), want %q", code, message, unplaced)
		}
		if !strings.Contains(message, "process ") {
			t.Errorf("the refusal does not name the chain that was read: %s", message)
		}
		if got := taskCount(t, w.home); got != before {
			t.Fatalf("a refused task.create recorded a task: %d tasks, want %d", got, before)
		}

		var created machine.TaskCreated
		if err := w.running.client.Call(t.Context(), ipc.MethodTaskCreate, investigation, &created); err != nil {
			t.Fatalf("task.create from the coordinator, at a terminal: %v", err)
		}
	})
}

// A person's own coordinator command whose chain the kernel rewrote, with
// no worker launched at all, is refused the same way: the chain is read for
// every coordinator request, and one with no worker, no subordinate and no
// terminal's foreground on it is placed as nobody's. The control is the
// same call from a process in a session of its own whose parent, this test,
// stands at a terminal, which is served.
func TestAPersonsOwnOrphanedCoordinatorCommandIsRefusedUnplaced(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	h := newHome(t)
	withService(t, h, func(running serviceUnderTest) {
		if _, code := callDetached(t, running, ipc.MethodWakesList, machine.WakesRequest{}); code != "" {
			t.Fatalf("wakes.list from a detached child of a test at a terminal answered %q, want it served", code)
		}
		code, message := orphanedCall(t, running.service.Socket(), ipc.MethodWakesList, machine.WakesRequest{}, nil,
			func(script string) {
				starter := exec.Command("/bin/sh", "-c", "/bin/sh -c "+shellQuote(script)+" </dev/null >/dev/null 2>&1 &")
				starter.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
				if raw, err := starter.CombinedOutput(); err != nil {
					t.Fatalf("starting the orphan: %v: %s", err, raw)
				}
			})
		if code != unplaced {
			t.Fatalf("wakes.list from a person's orphaned command answered %q (%s), want %q", code, message, unplaced)
		}
	})
}
