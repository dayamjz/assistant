//go:build unix

package service_test

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// These tests drive what the service does about a task whose work landed:
// the real service over its socket, the shipped adapter's stream-mode worker
// as the stand-in plays it, and a real copy, upstream and gate. The landing
// is the merge record internal/runs writes when a pull request is read
// merged, written here through the same store transaction, because no code
// host is reachable from these tests; every effect is read back from the
// store, the provider and the filesystem.

// passingConfig runs the supervisor's pass at the beacon's floor, so the
// pass is what notices a landing the test wrote behind the service's back.
const passingConfig = `{"supervision_beacon_interval": "1s"}` + "\n"

// quietConfig puts the beacon at its ceiling, so after its first pass, made
// before the test lands anything, the supervisor does not look again within
// the test and what a stop does is the stop's own.
const quietConfig = `{"supervision_beacon_interval": "10m"}` + "\n"

// land writes what a merged pull request leaves behind for the task: the
// copy's branch pushed to the upstream's default branch, which is where a
// merge puts the work, and a run on the task's branch, attributed to it,
// ended passed by a merge record read merged. It returns the run.
func land(t *testing.T, w launched, branch string) store.Run {
	t.Helper()
	ctx := t.Context()
	copyPath := resolvedCopy(t, w.home, w.task)
	// The copy is a clone of the subject, so it carries the subject's work
	// branch, one commit past the default branch, beside the task's branch,
	// which the dispatch cut at the default branch. The work branch is
	// pushed to the upstream's default branch, so every commit the copy was
	// cut with is there; one made in the copy since is not.
	git(t, copyPath, "push", "--quiet", w.repository.UpstreamURL, "refs/heads/work:refs/heads/main")
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatal(err)
	}
	records := openRecords(t, w.home)
	defer func() { _ = records.Close() }()
	head := git(t, copyPath, "rev-parse", "HEAD")
	id := "run-landed-" + w.task
	if _, err := records.CreateRun(ctx, store.Run{
		ID: id, RepositoryID: w.repository.ID, Branch: branch, SubmittedHead: head, Base: head,
		Intent: "validate", IntentSource: "push", ConfigDigest: "cfg", Build: build,
	}); err != nil {
		t.Fatalf("recording the run: %v", err)
	}
	if _, err := records.TransitionRun(ctx, id, []store.RunStatus{store.RunPending}, store.RunRunning); err != nil {
		t.Fatalf("starting the run: %v", err)
	}
	landed, err := records.TransitionRunOnPullRequest(ctx, id, []store.RunStatus{store.RunRunning}, store.RunPassed,
		store.PullRequestRead{PullRequest: 7, Head: head, End: store.PullRequestMerged}, store.HoldClosure{})
	if err != nil {
		t.Fatalf("writing the merge record: %v", err)
	}
	return landed
}

// waitTask polls the task surface until the resolution satisfies want.
func waitTask(t *testing.T, w launched, what string, want func(taskstate.Resolution) bool) taskstate.Resolution {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		got := listedTask(t, w.running.client, w.task)
		if want(got) {
			return got
		}
		if !time.Now().Before(deadline) {
			log, _ := os.ReadFile(w.home.ServiceLog())
			t.Fatalf("the task never read %s; it reads %+v\nthe service log:\n%s", what, got, log)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// removed reports whether the cleanup record says the copy was removed and
// nothing stands at the copy's path.
func removed(t *testing.T, w launched) bool {
	t.Helper()
	record, err := recordsOf(t, w.home).TaskCleanup(t.Context(), w.task)
	if err != nil || !record.RemovedAt.IsKnown() {
		return false
	}
	copyPath, err := w.home.TaskCopy(w.task)
	if err != nil {
		t.Fatal(err)
	}
	_, err = os.Stat(copyPath)
	return errors.Is(err, os.ErrNotExist)
}

// PRD principle P12, through the supervisor's pass. A task whose work lands
// while its worker runs reads Landed from the merge record with its
// terminal live; nothing asks the service anything, and the pass that next
// resolves it ends the worker through the session the service holds, sees
// the terminal Dead, and asks the cleanup decision, which writes the
// cleanup record, removes the copy on the proof and leaves the task
// Complete. The removal is the proof's: the copy's work is on the
// upstream's default branch, which is what the decision examined.
func TestATaskThatLandsWhileItsWorkerRunsEndsWithTheWorkerGoneAndItsCopyRemoved(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P12)
	streamWorker(t, "task/landing", passingConfig, func(w launched, _ *standin.Agent) {
		ref := w.session.Reference().String()
		if got := observed(t, listedTask, w.running.client, w.task); !readsRunning(got) {
			t.Fatalf("before the landing the task reads %+v, want running", got)
		}
		land(t, w, "task/landing")

		got := waitTask(t, w, "complete", func(r taskstate.Resolution) bool { return r.State == taskstate.Complete })
		if got.Source != taskstate.SourceRecord {
			t.Fatalf("the task reads %+v, want Complete from the terminal tier", got)
		}
		if obs := w.provider.Probe(t.Context(), ref); obs.Liveness != taskstate.Dead {
			t.Fatalf("the landed task's worker probes %+v, want dead", obs)
		}
		if _, _, held := w.running.service.SessionOf(w.task); held {
			t.Fatal("the service still holds a session for the landed task")
		}
		if !removed(t, w) {
			t.Fatal("the task reads Complete but the cleanup record or the copy's absence says otherwise")
		}
		record, err := recordsOf(t, w.home).TaskCleanup(t.Context(), w.task)
		if err != nil || len(record.Examined) == 0 {
			t.Fatalf("the cleanup record is %+v (%v), want the commits the proof examined", record, err)
		}
		if holds := stopHoldsOf(t, w); len(holds) != 0 {
			t.Fatalf("ending a landed task's worker registered %+v, want no hold", holds)
		}
	})
}

// stopHoldsOf is every hold a stop registered for the task's attempts.
func stopHoldsOf(t *testing.T, w launched) []store.Hold {
	t.Helper()
	var out []store.Hold
	for _, a := range attemptsOf(t, w.home, w.task) {
		h, err := recordsOf(t, w.home).Hold(t.Context(), "task/"+w.task+"/stopped/"+strconv.FormatInt(a.Sequence, 10))
		switch {
		case errors.Is(err, store.ErrNotFound):
		case err != nil:
			t.Fatalf("reading the stop hold of attempt %d: %v", a.Sequence, err)
		default:
			out = append(out, h)
		}
	}
	return out
}

// PRD section 6's stop, against the terminal tier. The same stop of the
// same running worker is made twice, in two homes, and the one difference
// is whether the task's work landed first. Where it had not, which is the
// control, the stop registers its relaunch hold and the task reads Held
// from it. Where it had, the stop ends the worker, answers with the state
// the records resolve, registers no hold, and asks the cleanup decision,
// which removes the copy on P12's proof; the supervisor does not look
// again within the test, so the removal is the stop path's.
func TestAStopOfALandedTaskRaisesNoRelaunchHoldAndOfALiveOneDoes(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P12)
	for _, tc := range []struct {
		name   string
		landed bool
	}{{"live", false}, {"landed", true}} {
		t.Run(tc.name, func(t *testing.T) {
			streamWorker(t, "task/stopping", quietConfig, func(w launched, _ *standin.Agent) {
				if tc.landed {
					land(t, w, "task/stopping")
					waitTask(t, w, "landed", func(r taskstate.Resolution) bool { return r.State == taskstate.Landed })
				}
				var stopped machine.TaskStopped
				if err := w.running.client.Call(t.Context(), ipc.MethodTaskStop, machine.TaskRequest{Task: w.task}, &stopped); err != nil {
					t.Fatalf("task.stop: %v", err)
				}
				if obs := w.provider.Probe(t.Context(), w.session.Reference().String()); obs.Liveness != taskstate.Dead {
					t.Fatalf("the stopped worker probes %+v, want dead", obs)
				}
				holds := stopHoldsOf(t, w)
				if !tc.landed {
					if !stopped.Hold.Open() || stopped.Finished != "" || len(holds) != 1 || holds[0].Key != stopped.Hold.Key {
						t.Fatalf("the stop of a live task answered %+v with holds %+v, want its open relaunch hold", stopped, holds)
					}
					if got := listedTask(t, w.running.client, w.task); got.State != taskstate.Held || got.Basis.Hold != stopped.Hold.Key {
						t.Fatalf("the stopped live task reads %+v, want Held on the stop's hold", got)
					}
					if machine.StopPhrase(stopped) == machine.StopPhrase(machine.TaskStopped{Finished: taskstate.Landed}) {
						t.Fatal("a stop that registered a hold reads the same row as one that did not")
					}
					return
				}
				if stopped.Finished != taskstate.Landed || stopped.Hold.Key != "" || len(holds) != 0 {
					t.Fatalf("the stop of a landed task answered %+v with holds %+v, want Finished landed and no hold", stopped, holds)
				}
				got := waitTask(t, w, "complete", func(r taskstate.Resolution) bool { return r.State == taskstate.Complete })
				if got.Source != taskstate.SourceRecord || !removed(t, w) {
					t.Fatalf("after the stop the task reads %+v and the copy removed is %v, want Complete with the copy gone", got, removed(t, w))
				}
			})
		})
	}
}

// unprovenLines counts the service log's lines saying the task's copy was
// left standing because the proof could not be established.
func unprovenLines(t *testing.T, w launched) int {
	t.Helper()
	log, err := os.ReadFile(w.home.ServiceLog())
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(log), "isolated copy of task "+w.task+" is left standing ("+string(tasks.ReasonUnproven)+")")
}

// PRD principle P12 holds past the pass and past a stop: a Dead terminal is
// the precondition the decision examines a copy under, never the proof. A
// task whose copy holds a commit that reached no upstream lands while its
// worker runs. The pass ends the worker and asks the decision once, which
// refuses as unproven; further passes, at the beacon's floor, do not ask
// again for the same terminal, which is the pass's bound. A stop then asks
// once more and registers no hold, and the decision refuses again: the copy
// stands with the commit in it, no cleanup record is written, and the task
// stays Landed. P6: nothing the worker committed is lost to the stop or to
// the cleanup. The bound is read over three intervals of wall time, so a
// pass delayed past them would leave it unexercised rather than failed.
func TestALandedCopyWhoseProofFailsIsKeptAfterTheStop(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P12)
	principles.Cite(t, principles.P6)
	streamWorker(t, "task/unproven", passingConfig, func(w launched, _ *standin.Agent) {
		// The commit that reached no upstream is made on the task's branch
		// before the landing, so no pass can see the task Landed without
		// it, and the landing pushes only what the copy was cut with.
		copyPath := resolvedCopy(t, w.home, w.task)
		if err := os.WriteFile(copyPath+"/unpushed.txt", []byte("work that reached no upstream\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		git(t, copyPath, "add", "unpushed.txt")
		git(t, copyPath, "commit", "--quiet", "-m", "work that reached no upstream")
		unpushed := git(t, copyPath, "rev-parse", "HEAD")
		land(t, w, "task/unproven")

		deadline := time.Now().Add(60 * time.Second)
		for unprovenLines(t, w) == 0 {
			if !time.Now().Before(deadline) {
				log, _ := os.ReadFile(w.home.ServiceLog())
				t.Fatalf("the pass never asked the decision over the landed task:\n%s", log)
			}
			time.Sleep(20 * time.Millisecond)
		}
		if obs := w.provider.Probe(t.Context(), w.session.Reference().String()); obs.Liveness != taskstate.Dead {
			t.Fatalf("the landed task's worker probes %+v after the pass, want dead", obs)
		}
		time.Sleep(3 * time.Second)
		if n := unprovenLines(t, w); n != 1 {
			t.Fatalf("the decision was asked %d times over one dead terminal, want once", n)
		}

		var stopped machine.TaskStopped
		if err := w.running.client.Call(t.Context(), ipc.MethodTaskStop, machine.TaskRequest{Task: w.task}, &stopped); err != nil {
			t.Fatalf("task.stop: %v", err)
		}
		if stopped.Finished != taskstate.Landed || stopped.Hold.Key != "" || len(stopHoldsOf(t, w)) != 0 {
			t.Fatalf("the stop answered %+v, want Finished landed and no hold", stopped)
		}
		if n := unprovenLines(t, w); n != 2 {
			t.Fatalf("after the stop the decision was asked %d times, want twice", n)
		}
		if got := listedTask(t, w.running.client, w.task); got.State != taskstate.Landed || got.Basis.Process != taskstate.Dead {
			t.Fatalf("after the stop the task reads %+v, want still Landed over a dead terminal", got)
		}
		if _, err := recordsOf(t, w.home).TaskCleanup(t.Context(), w.task); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("a cleanup record was written for a copy whose work reached no upstream: %v", err)
		}
		if head := git(t, copyPath, "rev-parse", "HEAD"); head != unpushed {
			t.Fatalf("the copy's head is %s after the stop, want the unpushed commit %s", head, unpushed)
		}
	})
}
