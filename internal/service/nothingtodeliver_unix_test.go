//go:build unix

package service_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// These tests drive PRD section 13's "Nothing to deliver" row through the
// real service over its socket: a delivery task's stand-in worker commits in
// its real copy and pushes its branch to the gate through the real hooks, and
// the run's real rebase stage replays the commit onto an upstream whose
// default branch already carries the same edit under another commit. The
// upstream is a local path, so no code host is involved. Only the review,
// test, document and lint bodies are stand-ins, and the review's counts
// whether it ran.

// nothingLine is the edit the worker makes, and the content the default
// branch carries when it already holds the same change.
const nothingLine = "the worker's line\n"

// nothingShape is what one walk varies from the landing case.
type nothingShape struct {
	// sameEdit lands the worker's edit on the default branch under another
	// commit before the worker pushes.
	sameEdit bool
	// reverted lands that edit and then reverts it on the default branch,
	// so the replay drops the worker's commit while the branch lacks it.
	reverted bool
	// published puts the task's branch on the upstream, at the commit the
	// copy was cut from, before the worker pushes.
	published bool
	// later commits again in the copy after the commit the worker pushes.
	later bool
	// untracked leaves a file no commit records in the copy.
	untracked bool
	// person pushes the task's branch with the subject's own identity
	// rather than the worker's.
	person bool
	// holds is a run that goes on past rebase, which holds at the first
	// stage stages.Holding has a person decide rather than passing.
	holds bool
}

// nothingWalked is what a walk hands its body once the run passed.
type nothingWalked struct {
	w launched
	// worker is the commit pushed to the gate, and run the run it started.
	worker string
	run    store.Run
	// main is the default branch's tip on the upstream when the worker
	// pushed.
	main string
	// local is the upstream's path.
	local string
	// successor is a task queued behind the walk's task.
	successor string
	// reviews is how many times the review stage's body ran.
	reviews *atomic.Int32
}

// nothingWalk builds a dispatched delivery task with a successor queued
// behind it, serves the service over a stand-in worker, shapes the upstream
// and the copy as shape says, commits the worker's edit and pushes it to the
// gate, and hands body what it did once the run passed.
func nothingWalk(t *testing.T, branch string, shape nothingShape, adjust func(service.Options) service.Options,
	body func(nw nothingWalked)) {
	t.Helper()
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	quietMaintenance(t, subject)
	local := git(t, subject, "remote", "get-url", "origin")
	// The copy is a clone of the subject and carries its work branch, which
	// is put on the default branch here so the one commit of the copy the
	// default branch lacks by identity is the worker's.
	git(t, subject, "push", "--quiet", "origin", "work:main")
	repository := recordRepository(t, h, subject)
	gateRepo := gateRepositoryOf(t, h, subject)
	quietMaintenance(t, gateRepo)
	if err := os.WriteFile(h.ConfigFile(), []byte(passingConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	task := recordDispatchedTaskWith(t, h, repository, store.TaskDelivery, branch, store.Unknown[config.Delivery](),
		"one", "Write the line in landed.txt.\n")
	successor := queuedBehind(t, h, repository, task)

	o, _ := workerOptions(t, h)
	var reviews atomic.Int32
	o.NewStages = func(deps stages.StageDeps) pipeline.Stages {
		all := stages.All(deps)
		all.Review = countingStage(pipeline.StageReview, &reviews)
		all.Test = passingStage(pipeline.StageTest)
		all.Document = passingStage(pipeline.StageDocument)
		all.Lint = passingStage(pipeline.StageLint)
		return all
	}
	if adjust != nil {
		o = adjust(o)
	}

	withServiceOptions(t, o, func(running serviceUnderTest) {
		sessions, held := launchedSession(t, running.service, task)
		waitCapture(t, sessions, held, "worker ready")
		w := launched{running: running, home: h, repository: repository, gateRepo: gateRepo, task: task,
			group: held.Reference().PID, provider: sessions, session: held}
		copyPath := resolvedCopy(t, h, task)
		quietMaintenance(t, copyPath)

		if shape.sameEdit || shape.reverted {
			advanceUpstream(t, local, "landed.txt", nothingLine, "land the same line first")
		}
		if shape.reverted {
			clone := t.TempDir()
			git(t, clone, "clone", "--quiet", local, ".")
			git(t, clone, "revert", "--no-edit", "HEAD")
			git(t, clone, "push", "--quiet", "origin", "HEAD:refs/heads/main")
		}
		if shape.published {
			git(t, copyPath, "push", "--quiet", local, "HEAD:refs/heads/"+branch)
		}
		if err := os.WriteFile(filepath.Join(copyPath, "landed.txt"), []byte(nothingLine), 0o600); err != nil {
			t.Fatal(err)
		}
		mustGitAs(t, w, copyPath, "add", "-A")
		mustGitAs(t, w, copyPath, "commit", "--quiet", "-m", "write the line")
		worker := mustGitAs(t, w, copyPath, "rev-parse", "HEAD")
		if shape.later {
			if err := os.WriteFile(filepath.Join(copyPath, "later.txt"), []byte("written after the push\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			mustGitAs(t, w, copyPath, "add", "-A")
			mustGitAs(t, w, copyPath, "commit", "--quiet", "-m", "a commit after the pushed one")
		}
		if shape.untracked {
			if err := os.WriteFile(filepath.Join(copyPath, "notes.txt"), []byte("not committed\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		main := git(t, local, "rev-parse", "refs/heads/main")
		refspec := worker + ":refs/heads/" + branch
		if shape.person {
			git(t, copyPath, "push", "--quiet", gate.RemoteName, refspec)
		} else if out, err := gitAs(t, w, copyPath, "push", gate.RemoteName, refspec); err != nil {
			t.Fatalf("the worker's push of its own branch was refused:\n%s", out)
		}
		records := openRecords(t, h)
		defer func() { _ = records.Close() }()
		run := runSubmitted(t, records, repository.ID, worker)
		if shape.holds {
			waitRunHeld(t, w, run.ID)
		} else {
			waitRunPassed(t, h, records, run.ID)
		}
		body(nothingWalked{w: w, worker: worker, run: run, main: main, local: local, successor: successor, reviews: &reviews})
	})
}

// waitRunHeld waits for the run to hold at a decision, and fails with the
// service log if it ends or never does.
func waitRunHeld(t *testing.T, w launched, id string) machine.Run {
	t.Helper()
	deadline := time.Now().Add(2 * launchWait)
	for {
		var run machine.Run
		if err := w.running.client.Call(t.Context(), ipc.MethodRunGet, machine.RunRequest{Run: id}, &run); err != nil {
			t.Fatalf("reading run %s: %v", id, err)
		}
		if run.Record.Status == store.RunHeld {
			return run
		}
		if run.Record.Status != store.RunPending && run.Record.Status != store.RunRunning || !time.Now().Before(deadline) {
			log, _ := os.ReadFile(w.home.ServiceLog())
			t.Fatalf("run %s reads %s, want held\nthe service log:\n%s", id, run.Record.Status, log)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// queuedBehind records a delivery task queued behind blocker, on another
// branch of the same repository, as intake queues one behind live work.
func queuedBehind(t *testing.T, h *home.Home, repository store.Repository, blocker string) string {
	t.Helper()
	records := openRecords(t, h)
	defer func() { _ = records.Close() }()
	b, err := records.Task(t.Context(), blocker)
	if err != nil {
		t.Fatal(err)
	}
	id := "successor" + blocker[:3]
	copyPath, err := h.TaskCopy(id)
	if err != nil {
		t.Fatal(err)
	}
	s, err := records.CreateTask(t.Context(), store.Task{
		ID: id, Shape: store.TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: copyPath,
		RepositoryID: store.Known(repository.ID), Branch: store.Known("task/after-" + blocker),
	}, store.Disposition{Blocker: store.Known(b.ID)})
	if err != nil {
		t.Fatalf("recording the successor: %v", err)
	}
	return s.ID
}

// countingStage is passingStage for stage, counting every time its body
// runs.
func countingStage(stage pipeline.Stage, ran *atomic.Int32) pipeline.Implementation {
	return pipeline.Implementation{
		NewBody: func() pipeline.Body {
			return func(context.Context, pipeline.Input) (pipeline.Output, error) {
				ran.Add(1)
				return pipeline.Output{Report: findings.Report{Summary: stage.String() + " found nothing"}}, nil
			}
		},
	}
}

// nothingRecord is the run's nothing-to-deliver record, if it has one.
func nothingRecord(t *testing.T, nw nothingWalked) (store.NothingToDeliver, bool) {
	t.Helper()
	got, found, err := recordsOf(t, nw.w.home).NothingToDeliver(t.Context(), nw.run.ID)
	if err != nil {
		t.Fatalf("reading the nothing-to-deliver record of run %s: %v", nw.run.ID, err)
	}
	return got, found
}

// upstreamHolds reports whether the upstream holds a branch of that name.
func upstreamHolds(t *testing.T, local, branch string) bool {
	t.Helper()
	_, err := tryGitIn(t, local, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// catchUp reads the catch-up through the socket.
func catchUp(t *testing.T, w launched) machine.CatchUp {
	t.Helper()
	var got machine.CatchUp
	if err := w.running.client.Call(t.Context(), ipc.MethodCatchUpRead, struct{}{}, &got); err != nil {
		t.Fatalf("reading the catch-up: %v", err)
	}
	return got
}

// PRD section 13's "Nothing to deliver" row (P6, P8, P15), the landing. The
// worker's one commit makes the edit the default branch already carries
// under another commit, on a branch the upstream does not hold, so the run's
// rebase really replays the commit and drops it. Asserted: the review stage
// never ran; the upstream gained no branch; exactly one nothing-to-deliver
// record names the run, the default branch's commit, the submitted commit
// and a checkpoint entry; the task resolves Landed from that record in the
// terminal tier, worded as already on the shared branch and never as merged;
// its worker is ended with no hold registered; the cleanup removes the copy
// on the default-branch content proof; the task resolves Complete; the task
// queued behind it is released; and the catch-up lists the record, never as
// merged. The positive control is the same walk whose commit the default
// branch does not carry: the run goes on to review and on to the first stage
// that holds for a person, no record is written, the task resolves from its
// run as before, and the task behind it stays queued through a dispatch
// pass.
func TestATaskWhoseRebaseLeavesNothingToDeliverLands(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	principles.Cite(t, principles.P8)
	principles.Cite(t, principles.P15)

	t.Run("the default branch carries the edit", func(t *testing.T) {
		t.Parallel()
		nothingWalk(t, "task/nothing-lands", nothingShape{sameEdit: true}, nil, func(nw nothingWalked) {
			if n := nw.reviews.Load(); n != 0 {
				t.Fatalf("the review stage ran %d time(s) on a change that no longer exists", n)
			}
			if upstreamHolds(t, nw.local, "task/nothing-lands") {
				t.Fatal("the upstream gained the task's branch, so the run pushed")
			}
			record, found := nothingRecord(t, nw)
			if !found {
				log, _ := os.ReadFile(nw.w.home.ServiceLog())
				t.Fatalf("run %s wrote no nothing-to-deliver record\nthe service log:\n%s", nw.run.ID, log)
			}
			if record.Run != nw.run.ID || record.DefaultCommit != nw.main || record.Submitted != nw.worker ||
				len(record.Examined) != 1 || record.Examined[0] != nw.worker || record.Checkpoint < 1 ||
				record.Answer != store.NothingToDeliverAnswer {
				t.Fatalf("the record reads %+v, want run %s's %s present in %s", record, nw.run.ID, nw.worker, nw.main)
			}

			got := waitTask(t, nw.w, "complete", func(r taskstate.Resolution) bool { return r.State == taskstate.Complete })
			if got.Source != taskstate.SourceRecord || got.Basis.LandedRun != nw.run.ID || !got.Basis.NothingToDeliver {
				t.Fatalf("the task reads %+v, want Complete from the run's nothing-to-deliver record", got)
			}
			if detail, _ := got.Detail.Get(); strings.Contains(detail, "merged") || strings.Contains(detail, "validated") {
				t.Fatalf("the task's detail says %q, which words a change nothing delivered as merged or validated", detail)
			}
			if obs := nw.w.provider.Probe(t.Context(), nw.w.session.Reference().String()); obs.Liveness != taskstate.Dead {
				t.Fatalf("the landed task's worker probes %+v, want dead", obs)
			}
			if holds := stopHoldsOf(t, nw.w); len(holds) != 0 {
				t.Fatalf("ending the worker registered %+v, want no hold", holds)
			}
			cleanup, err := recordsOf(t, nw.w.home).TaskCleanup(t.Context(), nw.w.task)
			if err != nil || cleanup.Proof != store.CleanupProofDefaultBranch || !removed(t, nw.w) {
				t.Fatalf("the cleanup record is %+v (%v) and the copy removed is %v, want removed on the default-branch proof",
					cleanup, err, removed(t, nw.w))
			}
			successor := listedTask(t, nw.w.running.client, nw.successor)
			deadline := time.Now().Add(launchWait)
			for successor.State == taskstate.Queued && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
				successor = listedTask(t, nw.w.running.client, nw.successor)
			}
			if successor.State == taskstate.Queued {
				t.Fatalf("the task queued behind a landed one still reads %+v", successor)
			}

			caught := catchUp(t, nw.w)
			var shown bool
			for _, l := range caught.Landed {
				if l.Task.Record.ID != nw.w.task {
					continue
				}
				if l.Merge != nil {
					t.Fatalf("the catch-up lists a merge record for a task nothing delivered: %+v", l.Merge)
				}
				if phrase := machine.LandedPhrase(l); strings.Contains(phrase.Outcome, "merged") {
					t.Fatalf("the catch-up says %q of a change nothing delivered", phrase.Outcome)
				}
				if l.NothingToDeliver != nil && l.NothingToDeliver.Run == nw.run.ID {
					shown = true
				}
			}
			if !shown {
				t.Fatalf("the catch-up's landed part %+v does not list the run's nothing-to-deliver record", caught.Landed)
			}
		})
	})

	t.Run("control: the default branch lacks the edit", func(t *testing.T) {
		t.Parallel()
		nothingWalk(t, "task/nothing-control", nothingShape{holds: true}, nil, func(nw nothingWalked) {
			if nw.reviews.Load() == 0 {
				t.Fatal("the review stage never ran on a change the default branch lacks")
			}
			if _, found := nothingRecord(t, nw); found {
				t.Fatal("a run that delivered a change wrote a nothing-to-deliver record")
			}
			if got := listedTask(t, nw.w.running.client, nw.w.task); got.Source != taskstate.SourceRun || got.State != taskstate.Held {
				t.Fatalf("the task reads %+v, want Held from its run, which went on to a person", got)
			}
			nw.w.running.service.DispatchDueForTest(t.Context())
			if got := listedTask(t, nw.w.running.client, nw.successor); got.State != taskstate.Queued {
				t.Fatalf("the task queued behind one whose run goes on reads %+v, want still queued", got)
			}
		})
	})
}

// PRD section 13's "Nothing to deliver" row, the runs that write no record:
// each takes the same short circuit or none, and its task resolves from its
// run tier, Ready, with the task behind it still queued. A default branch
// that applied the same edit and then reverted it, so the replay drops the
// commit cleanly while the default branch lacks the edit, which shows the
// content check can fail; and the same branch already present on the
// upstream when the run's rebase observes it. The landing above is the
// positive control.
func TestARunThatDoesNotProveItsWorkPresentWritesNoRecord(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	for _, tc := range []struct {
		name   string
		branch string
		shape  nothingShape
	}{
		{"an edit the default branch applied and reverted", "task/nothing-reverted", nothingShape{reverted: true}},
		{"a branch already published", "task/nothing-published", nothingShape{sameEdit: true, published: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			nothingWalk(t, tc.branch, tc.shape, nil, func(nw nothingWalked) {
				if n := nw.reviews.Load(); n != 0 {
					t.Fatalf("the review stage ran %d time(s), so the run did not take the short circuit this case is about", n)
				}
				if record, found := nothingRecord(t, nw); found {
					t.Fatalf("the run wrote %+v", record)
				}
				if got := listedTask(t, nw.w.running.client, nw.w.task); got.State != taskstate.Ready || got.Source != taskstate.SourceRun {
					t.Fatalf("the task reads %+v, want Ready from its run", got)
				}
				nw.w.running.service.DispatchDueForTest(t.Context())
				if got := listedTask(t, nw.w.running.client, nw.successor); got.State != taskstate.Queued {
					t.Fatalf("the task queued behind it reads %+v, want still queued", got)
				}
			})
		})
	}
}

// PRD section 13's "Nothing to deliver" row, P6 and P15: the record lands
// the task and says nothing about its copy, which can hold more than the run
// was given. With a commit made in the copy after the one pushed, and
// separately with a file no commit records, the task still resolves Landed
// from the record, but the cleanup refuses the copy, nothing is removed, and
// a kept-work entry names the task's copy. The landing above, whose copy
// held only what was pushed and was removed, is the positive control.
func TestALandedCopyHoldingMoreThanTheRunIsKept(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	principles.Cite(t, principles.P15)
	for _, tc := range []struct {
		name   string
		branch string
		shape  nothingShape
	}{
		{"a commit after the push", "task/nothing-later", nothingShape{sameEdit: true, later: true}},
		{"an uncommitted file", "task/nothing-untracked", nothingShape{sameEdit: true, untracked: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			nothingWalk(t, tc.branch, tc.shape, nil, func(nw nothingWalked) {
				if _, found := nothingRecord(t, nw); !found {
					t.Fatal("the run wrote no nothing-to-deliver record")
				}
				var entry store.KeptWork
				deadline := time.Now().Add(60 * time.Second)
				for {
					open, err := recordsOf(t, nw.w.home).OpenKeptWork(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					for _, k := range open {
						if id, _ := k.TaskID.Get(); id == nw.w.task {
							entry = k
						}
					}
					if entry.ID != 0 || !time.Now().Before(deadline) {
						break
					}
					time.Sleep(20 * time.Millisecond)
				}
				if entry.ID == 0 {
					log, _ := os.ReadFile(nw.w.home.ServiceLog())
					t.Fatalf("no kept-work entry names the task's copy\nthe service log:\n%s", log)
				}
				got := listedTask(t, nw.w.running.client, nw.w.task)
				if got.State != taskstate.Landed || !got.Basis.NothingToDeliver {
					t.Fatalf("the task reads %+v, want Landed from the run's record", got)
				}
				if removed(t, nw.w) {
					t.Fatal("the copy was removed while it held work the default branch lacks")
				}
				copyPath := resolvedCopy(t, nw.w.home, nw.w.task)
				for _, name := range []string{"landed.txt"} {
					if _, err := os.Stat(filepath.Join(copyPath, name)); err != nil {
						t.Fatalf("the kept copy lost %s: %v", name, err)
					}
				}
			})
		})
	}
}

// PRD section 13's "Nothing to deliver" row: attribution is by branch and
// not by who pushed, so a person's push of a task's own branch, which
// leaves nothing to deliver, lands that task as its worker's push would.
func TestAPersonsPushOfATasksBranchLandsTheTask(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P8)
	nothingWalk(t, "task/nothing-person", nothingShape{sameEdit: true, person: true}, nil, func(nw nothingWalked) {
		if _, found := nothingRecord(t, nw); !found {
			t.Fatal("the person's run wrote no nothing-to-deliver record")
		}
		got := waitTask(t, nw.w, "landed", func(r taskstate.Resolution) bool { return r.Source == taskstate.SourceRecord })
		if got.Basis.LandedRun != nw.run.ID || !got.Basis.NothingToDeliver {
			t.Fatalf("the task reads %+v, want it landed by the person's run", got)
		}
	})
}

// PRD section 13's "Nothing to deliver" row, P6 and P8: stopping the service
// around the run's end leaves the run ended with its record, or neither. The
// service's settle is interrupted once, before it writes anything, as a
// service that stopped between the run's last checkpoint and the record's
// move leaves it: the run's checkpoint says it completed, its record still
// says running, and there is no nothing-to-deliver record. The next service
// over the same home reconciles the run from its checkpoint, and the run
// then reads passed with its record, in the one write.
func TestAServiceStoppedAroundTheRunsEndWritesTheRecordWithTheEndOrNeither(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	principles.Cite(t, principles.P8)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	quietMaintenance(t, subject)
	local := git(t, subject, "remote", "get-url", "origin")
	// The default branch carries the subject's work under another commit, and
	// the branch pushed to the gate is not on the upstream.
	content, err := os.ReadFile(filepath.Join(subject, "work.txt"))
	if err != nil {
		t.Fatal(err)
	}
	advanceUpstream(t, local, "work.txt", string(content), "land the same change first")
	git(t, subject, "switch", "--quiet", "-c", "nothing/stopped")
	submitted := git(t, subject, "rev-parse", "HEAD")
	repository := recordRepository(t, h, subject)
	quietMaintenance(t, gateRepositoryOf(t, h, subject))

	var interrupted atomic.Bool
	o := service.WithSettleInterruptForTest(options(t, h), func(string) error {
		if interrupted.CompareAndSwap(false, true) {
			return errors.New("the service stopped before the run's end was written")
		}
		return nil
	})
	running, closeService := openService(t, o)
	var started machine.Run
	err = running.client.Call(t.Context(), ipc.MethodRunStart, machine.StartRequest{Working: machine.Working{WorkingPath: subject}, Intent: "a change somebody already landed"}, &started)
	if err == nil {
		closeService()
		t.Fatalf("the interrupted run answered %+v rather than the interruption", started)
	}
	records := openRecords(t, h)
	defer func() { _ = records.Close() }()
	run := runSubmitted(t, records, repository.ID, submitted)
	if run.Status != store.RunRunning {
		closeService()
		t.Fatalf("the interrupted run reads %s, want still running", run.Status)
	}
	if _, found, err := records.NothingToDeliver(t.Context(), run.ID); err != nil || found {
		closeService()
		t.Fatalf("an interrupted end left a record (%v, %v) on a run that did not move", found, err)
	}
	closeService()

	withServiceOptions(t, options(t, h), func(serviceUnderTest) {
		waitRunPassed(t, h, records, run.ID)
		record, found, err := records.NothingToDeliver(t.Context(), run.ID)
		if err != nil || !found || record.Submitted != submitted {
			t.Fatalf("the reconciled run reads passed with record %+v (%v, %v)", record, found, err)
		}
	})
}
