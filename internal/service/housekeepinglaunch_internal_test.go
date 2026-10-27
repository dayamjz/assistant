package service

import (
	"errors"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/runs"
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

// retirementHoldOf registers the pass's retirement hold for its first
// interval, keyed as the loop keys one, against the attempts as recorded.
func retirementHoldOf(t *testing.T, records *store.Store, task string) (store.Hold, retireHold) {
	t.Helper()
	rh := retireHold{Task: task, Epoch: 1, Attempt: latestSequence(recordedAttempts(t, records, task))}
	hold, err := records.RegisterHold(t.Context(), store.Hold{Key: rh.key(), TaskID: store.Known(task), Subject: "retire?", Detail: "d"})
	if err != nil {
		t.Fatalf("registering the retirement hold: %v", err)
	}
	return hold, rh
}

// PRD section 6's "a retire ... is refused, naming what moves it, while the
// hold stays open" and P10, for the one thing that moves a pass without a
// record of its own: a launch under way. The dispatch's slot stays
// unsettled from its claim until the launch has recorded the worker's
// session, so meanwhile the pass stays open and resolves Dispatched, which
// is in flight, and the stop hook's reading, taken with no probe, blocks a
// turn from ending while the worker is being created; no retirement hold is
// offered meanwhile (TestTheRetirementHoldIsNotOfferedWhileThePassesSlotIsHeld),
// and a retire answered on a hold planted past that offer is refused as a
// stop would be, writing nothing. Once the launch has recorded its session,
// the same answer is refused on the probe reading the worker live. Before
// the slot covered the launch, the answer at that point read the attempt as
// never launched and was written, and the hook read nothing in flight while
// the worker ran on until the launch's record was refused, the P10 window
// the review at 3674920 reproduced with host stop.
func TestARetireAnsweredWhileItsPassIsLaunchingIsRefusedAndThePassStaysInFlight(t *testing.T) {
	principles.Cite(t, principles.P10)
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
	if guard := feed.Guard(ctx, s.home, supervisor.FreshnessFor(time.Second), time.Now()); guard.Allowed() || guard.Reason == supervisor.StopNothingInFlight {
		t.Fatalf("with the pass's launch under way the stop hook reads %+v, want a turn blocked from ending", guard)
	}
	hold, rh := retirementHoldOf(t, w.records, w.task)
	retire := machine.TaskRespondRequest{Task: w.task, Answer: machine.AnswerRetire}

	answered, err := s.answerRetirement(ctx, ipc.Request{}, retire, hold, rh)
	if !errors.Is(err, ipc.ErrRefused) || !errors.Is(err, errSteerInFlight) {
		t.Fatalf("retire answered while the pass's launch was under way = %+v, %v, with the stop hook reading %s; "+
			"want it refused while the launch holds the pass's slot",
			answered.Effect, err, feed.Guard(ctx, s.home, supervisor.FreshnessFor(time.Second), time.Now()).Reason)
	}
	if _, retired, err := w.records.RetirementOf(ctx, w.task); err != nil || retired {
		t.Fatalf("the refused answer left the pass retired %v (%v)", retired, err)
	}
	if h, err := w.records.Hold(ctx, hold.Key); err != nil || !h.Open() {
		t.Fatalf("the refused answer left the retirement hold %+v, %v; want it open", h, err)
	}

	w.letGo()
	out := <-w.launched
	w.launched <- out
	if out.err != nil {
		t.Fatalf("the launch, with the answer refused, returned %v; want the worker recorded", out.err)
	}
	if got := latestRef(recordedAttempts(t, w.records, w.task)); got != ref {
		t.Fatalf("the launch recorded session %q, want %q", got, ref)
	}
	if _, err := s.answerRetirement(ctx, ipc.Request{}, retire, hold, rh); !errors.Is(err, store.ErrPassMoves) {
		t.Fatalf("retire answered with the worker recorded and live = %v, want ErrPassMoves on the probe", err)
	}
}

// offerRetirementHoldOf registers the pass's retirement hold through the
// store as the loop's offer does, against the attempts as recorded, under
// a fresh incarnation.
func offerRetirementHoldOf(t *testing.T, records *store.Store, task string) store.Hold {
	t.Helper()
	ctx := t.Context()
	inc, err := records.BeginIncarnation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	attempts := recordedAttempts(t, records, task)
	rh := retireHold{Task: task, Epoch: 1, Attempt: latestSequence(attempts)}
	hold, err := records.OfferRetirementHold(ctx, inc, store.RetirementOffer{
		Hold:    store.Hold{Key: rh.key(), TaskID: store.Known(task), Subject: "retire?", Detail: "d"},
		Attempt: rh.Attempt, Session: latestRef(attempts), Ended: runs.Finished, Delivered: taskstate.Delivered,
	})
	if err != nil {
		t.Fatalf("offering the retirement hold: %v", err)
	}
	return hold
}

// The window the launch's slot does not cover, P10: the loop reads no
// launch in flight and offers the retirement hold after a dispatcher has
// read the pass as Dispatched and before it claims its attempt. The claim
// is decided in the store against the hold, so the dispatch is refused
// (store.ErrPassHeld), claims no attempt, creates no worker and gives the
// slot back; a pass that resolves Held from that hold has nothing running
// in it, which is what its not being in flight says. Once the hold is
// answered keep the next dispatch begins the pass, the control that the
// refusal is the hold's. Before the claim read the hold, the dispatch went
// on to claim its attempt and launch, and the worker then ran in a pass
// the hook read as nothing in flight, the shape the review at 3ebce63
// reproduced.
func TestADispatchThatReadAPassBeforeItsRetirementHoldWasOfferedClaimsNoAttempt(t *testing.T) {
	principles.Cite(t, principles.P10)
	ctx := t.Context()
	sub := newDispatchSubjectOf(t, func(id, copyPath string) (store.Task, store.Disposition) {
		return store.Task{
			ID: id, Shape: store.TaskInvestigation, Project: "one", Mode: "interactive", WorktreePath: copyPath,
			RepositoryID: store.Known("subject"), Role: config.RoleHousekeeping, Housekeeping: true,
		}, store.Disposition{Delivered: taskstate.Delivered}
	})
	s := sub.service
	var hold store.Hold
	s.betweenAttemptReadAndClaim = func() { hold = offerRetirementHoldOf(t, sub.records, sub.task) }
	d, err := s.dispatchTask(ctx, sub.task)
	attempts := recordedAttempts(t, sub.records, sub.task)
	if !errors.Is(err, store.ErrPassHeld) {
		t.Fatalf("the dispatch of a pass whose retirement hold was offered before its claim = %+v, %v, with attempts %d; "+
			"want it refused with ErrPassHeld", d.attempt, err, len(attempts))
	}
	if len(attempts) != 0 {
		t.Fatalf("the refused dispatch claimed attempts %+v, want none", attempts)
	}
	if _, held := s.isolationOf(sub.task); held {
		t.Fatal("the refused dispatch kept the pass's slot, so the next pass could not dispatch it")
	}
	if _, err := sub.records.ResolveHold(ctx, hold.Key, machine.AnswerKeep, store.ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	s.betweenAttemptReadAndClaim = nil
	d, err = s.dispatchTask(ctx, sub.task)
	if err != nil || d.attempt != 1 {
		t.Fatalf("the dispatch once the hold was answered keep = %+v, %v; want attempt 1", d, err)
	}
	d.settleSlot()
}
