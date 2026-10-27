//go:build unix

package service_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// These drive PRD section 13's stalled-worker row against a real stand-in
// worker the service launched, through the product's own terminal seam: the
// probe reads the worker's process and the capture its screen, and nothing
// states either. The loop runs untimed under a clock the test moves, so a
// window elapses in one step and every pass is one the test kicked.

// stallWorkerConfig is the row's setting: escalation_window at its floor,
// stall_inspection_count and stall_hold_count both one, and relaunch_attempts
// at one, so a retry's relaunch is within the bound.
const stallWorkerConfig = `{"supervision_beacon_interval": "1s", "escalation_window": "1m",
	"stall_inspection_count": 1, "stall_hold_count": 1, "relaunch_attempts": 1}` + "\n"

// stalledWorker is streamWorker under stallWorkerConfig's shape with the
// loop driven by kicked passes under an injected clock.
func stalledWorker(t *testing.T, branch, config string, body func(w launched, k *service.KickedSupervision, agent *standin.Agent)) {
	t.Helper()
	var k *service.KickedSupervision
	streamWorkerWith(t, branch, config, func(o service.Options) service.Options {
		o, k = service.WithKickedSupervisionForTest(o, time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC))
		return o
	}, func(w launched, agent *standin.Agent) { body(w, k, agent) })
}

// stallHoldsOf is every stall hold naming the task, open or resolved, read
// from the store.
func stallHoldsOf(t *testing.T, h *home.Home, task string) []store.Hold {
	t.Helper()
	snap, err := recordsOf(t, h).TaskSnapshot(t.Context(), task)
	if err != nil {
		t.Fatalf("reading task %s: %v", task, err)
	}
	var out []store.Hold
	for _, hold := range snap.Holds {
		if _, ok := supervisor.ParseStallHold(hold.Key); ok {
			out = append(out, hold)
		}
	}
	return out
}

// openStallHoldsOf is the task's open stall holds.
func openStallHoldsOf(t *testing.T, h *home.Home, task string) []store.Hold {
	t.Helper()
	var out []store.Hold
	for _, hold := range stallHoldsOf(t, h, task) {
		if hold.Open() {
			out = append(out, hold)
		}
	}
	return out
}

// driveToHold takes the worker's still screen through a baseline pass and
// three windows: the first two surfaces register no hold, the second
// inspecting, and the third registers exactly one, with its wake, which it
// returns.
func driveToHold(t *testing.T, w launched, k *service.KickedSupervision) store.Hold {
	t.Helper()
	k.Pass(t, w.task)
	for i := 1; i <= 2; i++ {
		k.Advance(time.Minute)
		if appended := k.Pass(t, w.task); appended[string(supervisor.KindTerminalUnchanged)] != 1 || appended[string(supervisor.KindHoldOpened)] != 0 {
			t.Fatalf("surface %d appended %v, want one terminal-unchanged wake and no hold", i, appended)
		}
		if open := openStallHoldsOf(t, w.home, w.task); len(open) != 0 {
			t.Fatalf("surface %d registered %+v, want no hold before the third", i, open)
		}
	}
	k.Advance(time.Minute)
	if appended := k.Pass(t, w.task); appended[string(supervisor.KindTerminalUnchanged)] != 1 || appended[string(supervisor.KindHoldOpened)] != 1 {
		t.Fatalf("the third surface appended %v, want the surface and the hold's wake", appended)
	}
	open := openStallHoldsOf(t, w.home, w.task)
	if len(open) != 1 || open[0].TaskID.Or("") != w.task {
		t.Fatalf("the third surface left open stall holds %+v, want exactly one naming the task", open)
	}
	return open[0]
}

// copyWork gives the worker's copy a commit and an uncommitted file, as a
// worker part way through would leave it, and returns how the copy reads.
func copyWork(t *testing.T, w launched) string {
	t.Helper()
	copyPath := resolvedCopy(t, w.home, w.task)
	if err := os.WriteFile(filepath.Join(copyPath, "committed.txt"), []byte("the worker's commit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, copyPath, "add", "committed.txt")
	git(t, copyPath, "commit", "-q", "-m", "the worker's commit")
	if err := os.WriteFile(filepath.Join(copyPath, "half-done.txt"), []byte("the worker's unfinished change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return copyReads(t, w)
}

// copyReads is the copy's head commit, its status and the uncommitted
// file's text, which an end that kept the work leaves as they were.
func copyReads(t *testing.T, w launched) string {
	t.Helper()
	copyPath := resolvedCopy(t, w.home, w.task)
	half, err := os.ReadFile(filepath.Join(copyPath, "half-done.txt"))
	if err != nil {
		t.Fatalf("reading the uncommitted file: %v", err)
	}
	return git(t, copyPath, "rev-parse", "HEAD") + git(t, copyPath, "status", "--porcelain") + string(half)
}

// waitGroupGone waits, within launchWait, for the kernel to report no
// process in the worker's group, and fails naming what it reports.
func waitGroupGone(t *testing.T, group int) {
	t.Helper()
	deadline := time.Now().Add(launchWait)
	for {
		err := syscall.Kill(-group, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the worker's process group %d still answers %v", group, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// answerStall answers the task's one open hold over the service's socket.
func answerStall(t *testing.T, w launched, word, note string) (machine.TaskAnswered, error) {
	t.Helper()
	var answered machine.TaskAnswered
	err := w.running.client.Call(t.Context(), ipc.MethodTaskRespond,
		machine.TaskRespondRequest{Task: w.task, Answer: word, Note: note}, &answered)
	return answered, err
}

// PRD section 13's stalled-worker row, against a real stand-in worker whose
// agent process stays live and whose screen never changes: the third surface
// registers exactly one hold naming the task, after which the probe still
// reads the worker live and the copy's commits and uncommitted files are
// unchanged, which is the check's positive control. An approve leaves the
// worker live and the next hold comes only on the third surface of a new
// count. A stop then leaves no process of the worker's group, the copy's
// commits and uncommitted files all still there, and the stop's own hold
// open, carrying whether the work is begun again.
func TestAStalledStandInWorkerHoldsAndApproveOrStopAnswersIt(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)
	stalledWorker(t, "task/stall", stallWorkerConfig, func(w launched, k *service.KickedSupervision, _ *standin.Agent) {
		ref := w.session.Reference().String()
		before := copyWork(t, w)
		hold := driveToHold(t, w, k)
		if obs := w.provider.Probe(t.Context(), ref); obs.Liveness != taskstate.Live {
			t.Fatalf("after the hold the worker probes %+v, want live: the stall ends nothing", obs)
		}
		if got := copyReads(t, w); got != before {
			t.Fatalf("the hold changed the copy:\n%s\nwant\n%s", got, before)
		}
		if got := listedTask(t, w.running.client, w.task); got.State != taskstate.Held || got.Basis.Hold != hold.Key {
			t.Fatalf("the held task reads %+v, want Held on %s", got, hold.Key)
		}

		approved, err := answerStall(t, w, machine.AnswerApprove, "")
		if err != nil || approved.Effect != machine.AnswerRecorded || approved.Hold.Resolution.Or("") != machine.AnswerApprove {
			t.Fatalf("approve answered %+v (%v), want the hold recorded approve", approved, err)
		}
		if obs := w.provider.Probe(t.Context(), ref); obs.Liveness != taskstate.Live {
			t.Fatalf("after the approve the worker probes %+v, want live", obs)
		}
		next := driveToHold(t, w, k)
		if next.Key == hold.Key {
			t.Fatalf("the hold after the approve is the approved one, %s", hold.Key)
		}

		stopped, err := answerStall(t, w, machine.AnswerStop, "")
		if err != nil {
			t.Fatalf("stop: %v", err)
		}
		if stopped.Effect != machine.AnswerWorkerStopped || stopped.Hold.Key != next.Key || stopped.Hold.Resolution.Or("") != machine.AnswerStop ||
			stopped.Stopped == nil || !stopped.Stopped.Open() || !strings.HasSuffix(stopped.Stopped.Key, "/stopped/1") {
			t.Fatalf("stop answered %+v, want the stall hold answered stop and the stop's hold for attempt 1 open", stopped)
		}
		waitGroupGone(t, w.group)
		if got := copyReads(t, w); got != before {
			t.Fatalf("the stop changed the copy:\n%s\nwant\n%s", got, before)
		}
		if got := listedTask(t, w.running.client, w.task); got.State != taskstate.Held || got.Basis.Hold != stopped.Stopped.Key {
			t.Fatalf("the stopped task reads %+v, want Held on the stop's hold %s", got, stopped.Stopped.Key)
		}
	})
}

// PRD section 6's retry on a stall hold, against a real stand-in worker:
// the stop and then the retry to the stop's hold, in one answer, which is
// the one anchored relaunch in the same copy, counted by relaunch_attempts.
// With relaunch_attempts at one the work is begun again as attempt 2 over
// the stopped session and the copy keeps what the worker left there; with
// relaunch_attempts at zero the answer registers the bound's hold and starts
// nothing.
func TestAStalledStandInWorkersRetryIsTheAnchoredRelaunch(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	const unestablished = "a relaunch after a stop in the stopped worker's copy"
	service.RequireSweepClearsHere(t, unestablished)
	t.Run("within the bound", func(t *testing.T) {
		t.Parallel()
		stalledWorker(t, "task/stall-retry", stallWorkerConfig, func(w launched, k *service.KickedSupervision, _ *standin.Agent) {
			ref := w.session.Reference().String()
			before := copyWork(t, w)
			hold := driveToHold(t, w, k)
			sweptPath, err := w.home.TaskCopy(w.task)
			if err != nil {
				t.Fatal(err)
			}
			swept := w.sweeps.Of(sweptPath)
			retried, err := answerStall(t, w, machine.AnswerRetry, "narrow it to the parser")
			if err != nil {
				t.Fatalf("retry: %v", err)
			}
			if retried.Hold.Key != hold.Key || retried.Hold.Resolution.Or("") != machine.AnswerRetry ||
				retried.Stopped == nil || retried.Stopped.Resolution.Or("") != machine.AnswerRetry {
				t.Fatalf("retry answered %+v, want the stall hold and the stop's hold both answered retry", retried)
			}
			running := w.running
			switch {
			case retried.Effect == machine.AnswerRelaunched:
				if retried.Relaunch == nil || retried.Relaunch.Attempt != 2 || retried.Relaunch.Superseded != ref {
					t.Fatalf("retry answered %+v, want attempt 2 relaunched over %s", retried, ref)
				}
			case retried.Effect == machine.AnswerRetryDeferred && w.sweeps.Of(sweptPath) == swept+1:
				restarted := false
				w.sweeps.UntilClear(t, unestablished, sweptPath, func() {
					running = w.reopen()
					restarted = true
				})
				if !restarted {
					t.Fatalf("the retry was deferred (%s) though the sweep it asked of the copy cleared", retried.NotRelaunched)
				}
			default:
				t.Fatalf("retry answered %+v having asked %d sweep(s) of the copy, want attempt 2 relaunched over %s",
					retried, w.sweeps.Of(sweptPath)-swept, ref)
			}
			waitGroupGone(t, w.group)
			waitRelaunched(t, running, w.home, w.task, ref)
			if attempts := attemptsOf(t, w.home, w.task); len(attempts) != 2 {
				t.Fatalf("after the retry the task has attempts %+v, want the one relaunch", attempts)
			}
			if got := copyReads(t, w); got != before {
				t.Fatalf("the relaunch did not keep the copy:\n%s\nwant\n%s", got, before)
			}
		})
	})
	t.Run("relaunch_attempts zero", func(t *testing.T) {
		t.Parallel()
		config := strings.Replace(stallWorkerConfig, `"relaunch_attempts": 1`, `"relaunch_attempts": 0`, 1)
		stalledWorker(t, "task/stall-bound", config, func(w launched, k *service.KickedSupervision, _ *standin.Agent) {
			driveToHold(t, w, k)
			retried, err := answerStall(t, w, machine.AnswerRetry, "")
			if err != nil {
				t.Fatalf("retry: %v", err)
			}
			if retried.Effect != machine.AnswerRetryAtBound || retried.Relaunch != nil || !strings.Contains(retried.NotRelaunched, "relaunch-bound/1") {
				t.Fatalf("retry at relaunch_attempts zero answered %+v, want the bound's hold for attempt 1 and nothing begun", retried)
			}
			waitGroupGone(t, w.group)
			if attempts := attemptsOf(t, w.home, w.task); len(attempts) != 1 {
				t.Fatalf("past the bound the task has attempts %+v, want the one", attempts)
			}
			if got := listedTask(t, w.running.client, w.task); got.State != taskstate.Held || !strings.Contains(got.Basis.Hold, "relaunch-bound/1") {
				t.Fatalf("past the bound the task reads %+v, want Held on the bound's hold", got)
			}
		})
	})
}

// PRD section 6's "a stall that ends by itself closes the hold", against a
// real stand-in worker: with the hold open the agent exits on its own, the
// next pass surfaces terminal gone, and the program closes the hold,
// recording itself as the resolver and what the probe read.
func TestAStalledStandInWorkerThatExitsClosesItsHold(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	stalledWorker(t, "task/stall-exit", stallWorkerConfig, func(w launched, k *service.KickedSupervision, _ *standin.Agent) {
		ref := w.session.Reference().String()
		hold := driveToHold(t, w, k)
		if err := w.provider.Send(t.Context(), w.session, "finish"); err != nil {
			t.Fatalf("Send: %v", err)
		}
		waitCapture(t, w.provider, w.session, "worker finished")
		waitGroupGone(t, w.group)
		if appended := k.Pass(t, w.task); appended[string(supervisor.KindTerminalGone)] != 1 || appended[string(supervisor.KindStallClosed)] != 0 {
			t.Fatalf("the pass after the exit appended %v, want terminal gone and no closure wake of its own", appended)
		}
		var closed store.Hold
		for _, h := range stallHoldsOf(t, w.home, w.task) {
			if h.Key == hold.Key {
				closed = h
			}
		}
		by, known := closed.ResolvedBy.Get()
		if closed.Open() || closed.Resolution.Or("") != supervisor.StallClosedWorkerDead || !known || by != store.ResolvedByProgram() ||
			!strings.Contains(closed.ResolutionNote.Or(""), ref) {
			t.Fatalf("the hold after the exit = %+v, want it closed by the program on the worker read dead, naming %s", closed, ref)
		}
	})
}

// P15's stop half with a stall hold open: stopping the service while the
// stalled worker runs and its hold waits leaves no process of the worker's
// group, on the P15 row's terms, and the next start relaunches nothing,
// because the task still resolves from its hold.
func TestStoppingTheServiceWithAStallHoldOpenEndsTheWorker(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	stalledWorker(t, "task/stall-stop", stallWorkerConfig, func(w launched, k *service.KickedSupervision, _ *standin.Agent) {
		driveToHold(t, w, k)
		w.reopen()
		waitGroupGone(t, w.group)
		if attempts := attemptsOf(t, w.home, w.task); len(attempts) != 1 {
			t.Fatalf("after the restart the task has attempts %+v, want the one: a held task is not relaunched", attempts)
		}
	})
}
