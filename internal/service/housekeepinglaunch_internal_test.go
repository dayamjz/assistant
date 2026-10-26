package service

import (
	"errors"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/supervisor/feed"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// passRecordWindow is a recordWindow whose task is a housekeeping pass, as
// intake records one: a marked investigation of the repository under the
// housekeeping role, with the record tier as its disposition.
func passRecordWindow(t *testing.T) *recordWindow {
	t.Helper()
	sub := newDispatchSubjectOf(t, func(id, copyPath string) (store.Task, store.Disposition) {
		return store.Task{
			ID: id, Shape: store.TaskInvestigation, Project: "one", Mode: "interactive", WorktreePath: copyPath,
			RepositoryID: store.Known("subject"), Role: config.RoleHousekeeping, Housekeeping: true,
		}, store.Disposition{Delivered: taskstate.Delivered}
	})
	return recordWindowOver(t, launchSubjectOver(t, workerStandIn(t), sub))
}

// retireAtSeam retires the pass as an answered retire does once the pass's
// latest attempt reads as never having had a session: the retirement hold
// is registered and the record written against the attempts as the store
// shows them, which is what answerRetirement reads before it writes.
func retireAtSeam(t *testing.T, records *store.Store, task string) {
	t.Helper()
	ctx := t.Context()
	attempts := recordedAttempts(t, records, task)
	if ref := latestRef(attempts); ref != "" {
		t.Fatalf("at the seam pass %s's attempt already runs in session %s, so the retirement would not read it as never launched", task, ref)
	}
	key := retireHold{Task: task, Epoch: 1, Attempt: latestSequence(attempts)}.key()
	if _, err := records.RegisterHold(ctx, store.Hold{Key: key, TaskID: store.Known(task), Subject: "retire?", Detail: "d"}); err != nil {
		t.Fatalf("registering the retirement hold: %v", err)
	}
	r := retirementOf(task, attempts, "")
	r.Hold, r.By = key, store.ResolvedByMachineInterface()
	if _, err := records.RetireHousekeepingPass(ctx, r); err != nil {
		t.Fatalf("retiring pass %s at the seam: %v", task, err)
	}
}

// PRD section 6's "a retired housekeeping pass is not in flight by its
// state alone" and P10, at the one window dispatch's slot does not cover:
// a retire answered after the pass's attempt was claimed and before its
// launch recorded a session reads the attempt as never launched and is
// written. The launch, already past the worker's creation, then records
// nothing: the record of the session refuses the retired pass under the
// store's serialization, the worker it started is ended, and the attempt
// stays without a session. So no worker of a retired pass runs, and the
// stop hook's reading, which takes no probe, is the truth: nothing is in
// flight, and a turn may end. Before this ordering, the launch recorded its
// session after the retirement and the worker ran on, live to the probe,
// while the hook read nothing in flight, which is the P10 failure the
// review at 14ed5c9 reproduced with host stop.
func TestALaunchUnderWayWhenItsPassIsRetiredRecordsNoSessionAndEndsItsWorker(t *testing.T) {
	principles.Cite(t, principles.P10)
	principles.Cite(t, principles.P6)
	ctx := t.Context()
	w := passRecordWindow(t)
	s := w.service
	reached := make(chan string, 1)
	s.beforeSessionRecord = func(_, ref string) {
		reached <- ref
		<-w.release
	}
	w.launch(ctx, t)
	ref := w.atSeam(t, reached)
	retireAtSeam(t, w.records, w.task)
	w.letGo()
	out := <-w.launched
	w.launched <- out

	attempts, err := w.records.TaskAttempts(ctx, w.task)
	if err != nil {
		t.Fatal(err)
	}
	guard := feed.Guard(ctx, s.home, supervisor.FreshnessFor(time.Second), time.Now())
	probed := w.terminals.Probe(ctx, ref)
	if !errors.Is(out.err, store.ErrPassRetired) {
		t.Fatalf("the launch of retired pass %s returned %v with the attempt's session %v and its worker probing %s, "+
			"while the stop hook reads %s; want the launch refused with ErrPassRetired",
			w.task, out.err, latestRefOf(attempts), probed.Liveness, guard.Reason)
	}
	if got := latestRef(attempts); got != "" {
		t.Fatalf("the retired pass's attempt records session %q, want none", got)
	}
	deadline := time.Now().Add(30 * time.Second)
	for probed.Liveness != taskstate.Dead {
		if !time.Now().Before(deadline) {
			t.Fatalf("the worker the refused launch started still probes %+v, want it ended", probed)
		}
		time.Sleep(5 * time.Millisecond)
		probed = w.terminals.Probe(ctx, ref)
	}
	if guard = feed.Guard(ctx, s.home, supervisor.FreshnessFor(time.Second), time.Now()); guard.Reason != supervisor.StopNothingInFlight {
		t.Fatalf("with the worker ended the stop hook reads %s, want %s", guard.Reason, supervisor.StopNothingInFlight)
	}
}

// recordedAttempts is the task's attempts as the store records them.
func recordedAttempts(t *testing.T, records *store.Store, task string) []store.TaskAttempt {
	t.Helper()
	attempts, err := records.TaskAttempts(t.Context(), task)
	if err != nil {
		t.Fatalf("reading the attempts of task %s: %v", task, err)
	}
	return attempts
}

// latestRefOf is the latest attempt's session as the record shows it, for
// a failure message.
func latestRefOf(attempts []store.TaskAttempt) store.Optional[string] {
	if n := len(attempts); n > 0 {
		return attempts[n-1].SessionRef
	}
	return store.Optional[string]{}
}
