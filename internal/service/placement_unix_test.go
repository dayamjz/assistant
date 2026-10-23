//go:build unix

package service_test

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// A task of a repository whose default branch sets placement.arch stays
// queued rather than being dispatched on this machine, because this build
// cannot place by architecture and a task no eligible machine satisfies stays
// queued rather than running where it was told not to (P7; PRD section 10's
// placement.arch row). Both ways a task reaches dispatch under that copy are
// covered: an investigation taken by task.create while the key is set, and a
// delivery task accepted before the key was added and dispatched once the
// service opens after it. Neither claims an attempt, and each still resolves
// Dispatched. Each is surfaced to the coordinator by the supervisor loop's
// terminal-unchanged wake, as any Dispatched task with no terminal is, which
// is what unhonored.go's placeable says of it. Once the key is removed, the
// next dispatch pass dispatches both, so the task was kept queued by the key
// and not lost.
func TestATaskOfARepositoryNarrowedByPlacementArchStaysQueued(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)
	requiresIdentifiedPeer(t)
	elsewhere := "amd64"
	if runtime.GOARCH == elsewhere {
		elsewhere = "arm64"
	}
	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	writeOperatorConfig(t, h.ConfigFile(), `{"supervision_beacon_interval": "1s"}`+"\n")
	accepted := recordDispatchedTask(t, h, repository, store.TaskDelivery, "task/accepted-before")
	plantTrustedDocument(t, subject, `{"placement": {"arch": ["`+elsewhere+`"]}}`)

	withServiceOptions(t, workerlessOptions(t, h), func(running serviceUnderTest) {
		investigate := func() string {
			var created machine.TaskCreated
			if err := running.client.Call(t.Context(), ipc.MethodTaskCreate, machine.TaskCreateRequest{
				Shape: "investigation", Project: "one", Instructions: "Find out why the build is slow.\n",
				Repository: repository.ID,
			}, &created); err != nil {
				t.Fatalf("task.create of an investigation: %v", err)
			}
			return created.Record.ID
		}
		investigation := investigate()
		for _, id := range []string{accepted, investigation} {
			awaitQueued(t, h, id)
			if got := taskState(t, running.client, id); got.State != taskstate.Dispatched {
				t.Fatalf("task %s resolves %+v under placement.arch [%s], want it still Dispatched", id, got, elsewhere)
			}
			awaitWake(t, h, id, supervisor.KindTerminalUnchanged)
		}

		plantTrustedDocument(t, subject, `{"ignore_patterns": ["vendor/**"]}`)
		third := investigate()
		for _, id := range []string{accepted, investigation, third} {
			awaitAttempt(t, h, id)
		}
	})
}

// awaitQueued waits for the dispatch pass to log that it left the task
// queued, failing at once if the task claimed an attempt instead.
func awaitQueued(t *testing.T, h *home.Home, id string) {
	t.Helper()
	deadline := time.Now().Add(launchWait)
	for {
		if attempts := attemptsOf(t, h, id); len(attempts) != 0 {
			t.Fatalf("task %s was dispatched under a placement.arch this build cannot honor: attempts %+v", id, attempts)
		}
		if strings.Contains(serviceLog(t, h), "task "+id+" stays queued") {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("task %s was neither dispatched nor logged as staying queued within %s", id, launchWait)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// awaitWake waits for the supervisor loop to queue a wake of kind about the
// task.
func awaitWake(t *testing.T, h *home.Home, id string, kind supervisor.Kind) {
	t.Helper()
	deadline := time.Now().Add(launchWait)
	for {
		wakes, err := recordsOf(t, h).TaskWakes(t.Context(), id)
		if err != nil {
			t.Fatalf("reading the wakes of task %s: %v", id, err)
		}
		for _, w := range wakes {
			if w.Kind == string(kind) {
				return
			}
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("no %s wake was queued about task %s within %s; its wakes are %+v", kind, id, launchWait, wakes)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// awaitAttempt waits for the task to claim its first attempt.
func awaitAttempt(t *testing.T, h *home.Home, id string) {
	t.Helper()
	deadline := time.Now().Add(launchWait)
	for len(attemptsOf(t, h, id)) == 0 {
		if !time.Now().Before(deadline) {
			t.Fatalf("task %s claimed no attempt within %s once placement.arch was removed", id, launchWait)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
