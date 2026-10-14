package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// requireSweepClearsHere skips the test, recording the limit, when this
// host refuses every sweep: the real sweep of an empty task copy of a home
// created for the purpose does not clear, and what keeps it from clearing
// is only processes whose working directory it cannot read and which
// neither of agents.Sweeper's carve-outs sets aside. Nothing was started in
// that copy, so they are processes of this host outside it, and every
// relaunch and every dispatch retry here is refused before its claim while
// they run, which is P6's refusal working. unestablished names what the
// test would have established, and the skip names those processes as
// agents.DescribeBlockers does, so a log shows which they were. A platform
// with no sweep, and a host whose empty copy sweeps clear, run the test;
// any other answer from the empty copy fails it, because it is not the
// host's standing refusal.
func requireSweepClearsHere(t *testing.T, unestablished string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h, err := home.Open(base)
	if err != nil {
		t.Fatal(err)
	}
	id, err := home.NewTaskID()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := h.TaskCopy(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	r, err := agents.NewSweeper(h, 0, nil).Sweep(t.Context(), dir)
	switch {
	case errors.Is(err, agents.ErrSweepUnsupported), err == nil && r.Clear():
		return
	case err != nil:
		t.Fatalf("sweeping an empty copy for the host's baseline: %v", err)
	case len(r.Ended)+len(r.Exited)+len(r.Replaced)+len(r.Survived)+len(r.Unverified) != 0 || len(r.UnreadableBlocking) == 0:
		t.Fatalf("the sweep of an empty copy did %s (%+v), want it clear or blocked only by processes whose working directory it cannot read", r, r)
	}
	t.Skipf("this host has %d process(es) outside any copy under test whose working directory this service cannot read and "+
		"which neither of agents.Sweeper's carve-outs sets aside (%s), so every sweep here refuses to clear, as P6 requires, and "+
		"%s is not established here", len(r.UnreadableBlocking), agents.DescribeBlockers(r.UnreadableBlocking), unestablished)
}

// relaunchedDetail is the detail of the task's newest "relaunched" note, and
// fails the test when there is none.
func relaunchedDetail(t *testing.T, records *store.Store, id string) string {
	t.Helper()
	events, err := records.TaskEvents(t.Context(), id, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == relaunchedKind {
			return events[i].Detail
		}
	}
	t.Fatalf("task %s has no %q note", id, relaunchedKind)
	return ""
}

// sweepUnsupported is the answer agents.Sweeper gives on a platform with no
// listing, wrapped the way Sweep wraps a listing's error, so a test on Linux
// can put the consumer in front of it.
func sweepUnsupported(_ context.Context, dir string) (agents.SweepResult, error) {
	return agents.SweepResult{}, fmt.Errorf("agents: listing processes under %s: %w", dir, agents.ErrSweepUnsupported)
}

// PRD principle P6 where the platform has no sweep: a safety fact that
// cannot be verified is refused and reported, and a person decides. With
// the sweep answering agents.ErrSweepUnsupported, which is how the seam
// reaches the case on a platform that has a sweep, the relaunch is refused
// before the claim: no attempt is begun, no note is appended, and a hold
// naming the task is registered, keyed by the attempt whose terminal is
// gone and distinct from the relaunch bound's. Asked again by a service
// built over the same records, which is what the next start is, and by the
// coordinator's task.relaunch, the relaunch is refused on the same hold and
// no second one is raised. Once the hold is answered retry, two relaunchers
// that read that answer, held between their read of the attempts and their
// claim so the test is of the anchor and not of scheduling, begin one
// attempt between them, the loser refused with ErrTaskMoved, and the note
// says nothing was proven and that a person answered. The answer does not
// carry over: once the new attempt's terminal is gone too, its relaunch is
// refused on a new hold for that attempt. The positive control is the same
// relaunch with the real sweep on a platform that has one, which proceeds
// and raises no hold. Run under the race detector, which make test does.
func TestAnUnsupportedSweepRefusesTheRelaunchUntilARetryAnswer(t *testing.T) {
	principles.Cite(t, principles.P6)
	sub := newRelaunchSubject(t)
	ctx := t.Context()
	sub.restart.sweepCopy = sweepUnsupported
	before := eventKinds(t, sub.records, sub.task)
	key := relaunchUnsweptKey(sub.task, 1)

	refusedOn := func(err error) store.Hold {
		t.Helper()
		var unproven *relaunchUnswept
		if !errors.As(err, &unproven) {
			t.Fatalf("the relaunch with no sweep answered %v, want *relaunchUnswept", err)
		}
		if unproven.Hold.Key != key || !unproven.Hold.Open() {
			t.Fatalf("the relaunch was refused on hold %+v, want %s open", unproven.Hold, key)
		}
		if attempts, err := sub.records.TaskAttempts(ctx, sub.task); err != nil || len(attempts) != 1 {
			t.Fatalf("the refused relaunch left attempts %+v (%v), want only the first", attempts, err)
		}
		if after := eventKinds(t, sub.records, sub.task); len(after) != len(before) {
			t.Fatalf("the history moved from %v to %v on a refused relaunch", before, after)
		}
		return unproven.Hold
	}

	// Refused, and held for a person on a key of its own.
	_, err := sub.restart.relaunchTask(ctx, sub.task)
	hold := refusedOn(err)
	if task, ok := hold.TaskID.Get(); !ok || task != sub.task || hold.RunID.IsKnown() {
		t.Fatalf("the hold names task %v and run %v, want the task and no run", hold.TaskID, hold.RunID)
	}
	if isRelaunchBound(hold.Key, sub.task) || relaunchBoundKey(sub.task, 1) == hold.Key {
		t.Fatalf("the no-sweep hold %s is in the relaunch bound's key space", hold.Key)
	}
	if _, err := sub.records.Hold(ctx, relaunchBoundKey(sub.task, 1)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the refusal raised the relaunch bound's hold too: %v", err)
	}

	// The next start, and the coordinator, are refused on the same hold.
	next, _ := restartedService(t, sub.launchSubject)
	next.sweepCopy = sweepUnsupported
	_, err = next.relaunchTask(ctx, sub.task)
	refusedOn(err)
	_, err = next.relaunchTaskFor(ctx, ipc.Request{}, machine.TaskRequest{Task: sub.task})
	if !errors.Is(err, ipc.ErrRefused) {
		t.Fatalf("task.relaunch with no sweep answered %v, want it refused", err)
	}
	refusedOn(err)
	open, err := sub.records.OpenHolds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var naming int
	for _, h := range open {
		if task, ok := h.TaskID.Get(); ok && task == sub.task {
			naming++
		}
	}
	if naming != 1 {
		t.Fatalf("%d open holds name the task after three refusals, want the one", naming)
	}

	// Answered retry: one attempt between two relaunchers.
	if _, err := sub.records.ResolveHold(ctx, key, machine.AnswerRetry, store.ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	var arrived sync.WaitGroup
	arrived.Add(2)
	sub.restart.betweenAttemptReadAndClaim = func() {
		arrived.Done()
		arrived.Wait()
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	outs := make([]relaunched, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			outs[i], errs[i] = sub.restart.relaunchClaimed(ctx, sub.task)
		}()
	}
	wg.Wait()
	sub.restart.betweenAttemptReadAndClaim = nil
	won, lost := -1, 0
	for i, err := range errs {
		switch {
		case err == nil:
			won = i
		case errors.Is(err, store.ErrTaskMoved):
			lost++
		default:
			t.Fatalf("a relauncher failed for a reason other than the claim: %v", err)
		}
	}
	if won < 0 || lost != 1 {
		t.Fatalf("the relaunchers answered %v, want one winner and one refused claim", errs)
	}
	if outs[won].launch.attempt != 2 {
		t.Fatalf("the winner began attempt %d, want 2", outs[won].launch.attempt)
	}
	detail := relaunchedDetail(t, sub.records, sub.task)
	for _, text := range []string{detail, outs[won].launch.note} {
		if !strings.Contains(text, "none is proven absent") || !strings.Contains(text, "answered "+machine.AnswerRetry) ||
			strings.Contains(text, "was swept") || strings.Contains(text, "reclaimed") {
			t.Fatalf("the relaunch on a retry says %q, want it to say nothing is proven and that a person answered retry", text)
		}
	}

	// The answer was the first attempt's: the next one holds again.
	launched, err := sub.restart.launchTask(ctx, outs[won].launch)
	if err != nil {
		t.Fatalf("launching the retried attempt: %v", err)
	}
	provider, _, _ := sub.restart.sessionOf(sub.task)
	for _, err := range sub.restart.closeSessions() {
		t.Fatal(err)
	}
	waitLiveness(t, provider, launched.Reference().String(), taskstate.Dead)
	_, err = sub.restart.relaunchClaimed(ctx, sub.task)
	var unproven *relaunchUnswept
	if !errors.As(err, &unproven) || unproven.Hold.Key != relaunchUnsweptKey(sub.task, 2) || !unproven.Hold.Open() {
		t.Fatalf("a relaunch of the retried attempt answered %v, want it held on a new hold for attempt 2", err)
	}
	if attempts, err := sub.records.TaskAttempts(ctx, sub.task); err != nil || len(attempts) != 2 {
		t.Fatalf("the task has attempts %+v (%v), want the first and the one retry", attempts, err)
	}

	// The control: a platform with a sweep relaunches with no hold.
	control := newRelaunchSubject(t)
	if _, err := agents.NewSweeper(control.restart.home, 0, nil).Sweep(ctx, control.copy); errors.Is(err, agents.ErrSweepUnsupported) {
		t.Logf("%s has no sweep, so the control, a relaunch that raises no hold, is not established here", runtime.GOOS)
		return
	}
	requireSweepClearsHere(t, "the control, a relaunch with a sweep that raises no hold,")
	if _, err := control.restart.relaunchTask(ctx, control.task); err != nil {
		t.Fatalf("relaunching the control: %v", err)
	}
	if _, err := control.records.Hold(ctx, relaunchUnsweptKey(control.task, 1)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the control's relaunch with a sweep raised the no-sweep hold: %v", err)
	}
	if detail := relaunchedDetail(t, control.records, control.task); !strings.Contains(detail, "was swept") {
		t.Fatalf("the control's relaunch says %q, want it to say the copy was swept", detail)
	}
}

// The no-sweep hold is answered the way the relaunch bound's is, through
// task.respond's handler: an answer it does not offer is refused and leaves
// it open, and a retry relaunches through the one anchored relaunch. A
// retry on the bound's hold that reaches a platform with no sweep begins
// nothing and reports the further hold it raised, AnswerRetryHeld, which is
// the next hold the task is answered on; a stop there leaves the task alone,
// so a later relaunch is not due.
func TestTheNoSweepHoldIsAnsweredRetryOrStop(t *testing.T) {
	ctx := t.Context()
	sub := newRelaunchSubject(t)
	sub.restart.sweepCopy = sweepUnsupported
	sub.restart.cfg.RelaunchAttempts = 0
	attempts, err := sub.records.TaskAttempts(ctx, sub.task)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := sub.restart.holdAtBound(ctx, sub.task, attempts,
		reconcileDecision{reason: "it has been relaunched 0 times and relaunch_attempts is 0"})
	if err != nil {
		t.Fatal(err)
	}
	answer := func(s *Service, task, text string) (machine.TaskAnswered, error) {
		return s.respondTask(ctx, ipc.Request{}, machine.TaskRespondRequest{Task: task, Answer: text})
	}
	held, err := answer(sub.restart, sub.task, machine.AnswerRetry)
	if err != nil {
		t.Fatalf("answering the bound's hold: %v", err)
	}
	key := relaunchUnsweptKey(sub.task, 1)
	if held.Hold.Key != bound.Key || held.Effect != machine.AnswerRetryHeld || !strings.Contains(held.NotRelaunched, key) {
		t.Fatalf("a retry on the bound with no sweep answered %+v, want %s on %s naming %s", held, machine.AnswerRetryHeld, bound.Key, key)
	}
	if _, err := answer(sub.restart, sub.task, "approve"); !errors.Is(err, ipc.ErrInvalidRequest) {
		t.Fatalf("an answer the no-sweep hold does not offer answered %v, want an invalid request", err)
	}
	if h, err := sub.records.Hold(ctx, key); err != nil || !h.Open() {
		t.Fatalf("after a refused answer the no-sweep hold is %+v (%v), want it open", h, err)
	}
	relaunched, err := answer(sub.restart, sub.task, machine.AnswerRetry)
	if err != nil {
		t.Fatalf("answering the no-sweep hold: %v", err)
	}
	if relaunched.Hold.Key != key || relaunched.Effect != machine.AnswerRelaunched ||
		relaunched.Relaunch == nil || relaunched.Relaunch.Attempt != 2 {
		t.Fatalf("a retry on the no-sweep hold answered %+v, want attempt 2 relaunched on %s", relaunched, key)
	}

	stopped := newRelaunchSubject(t)
	stopped.restart.sweepCopy = sweepUnsupported
	if _, err := stopped.restart.relaunchTask(ctx, stopped.task); err == nil {
		t.Fatal("the relaunch with no sweep went ahead with no answer")
	}
	out, err := answer(stopped.restart, stopped.task, machine.AnswerStop)
	if err != nil || out.Effect != machine.AnswerStopped || out.Hold.Key != relaunchUnsweptKey(stopped.task, 1) {
		t.Fatalf("a stop on the no-sweep hold answered %+v (%v), want it stopped", out, err)
	}
	if _, err := stopped.restart.relaunchTask(ctx, stopped.task); !errors.Is(err, errRelaunchNotDue) {
		t.Fatalf("a relaunch after a stop answered %v, want not due", err)
	}
}

// crashedDispatch is a task whose dispatch reached proven and whose service
// went away before any terminal was recorded: its copy stands, its one
// attempt names no session, and it still resolves Dispatched, which is what
// a retried dispatch finds after a crash in launch.go's window. It returns
// the subject and a restarted service over the same records whose sweep
// answers as the platform with no sweep does.
func crashedDispatch(t *testing.T) (*launchSubject, *Service) {
	t.Helper()
	sub := newLaunchSubject(t, workerStandIn(t))
	if _, err := sub.service.dispatchTask(t.Context(), sub.task); err != nil {
		t.Fatalf("dispatching: %v", err)
	}
	restart, _ := restartedService(t, sub)
	restart.sweepCopy = sweepUnsupported
	return sub, restart
}

// dispatchRefusedOn is the hold a dispatch refused for want of a sweep
// names, failing the test unless err is that refusal, it names want, and
// the refusal recorded no attempt beyond attempts and moved no history.
func dispatchRefusedOn(t *testing.T, sub *launchSubject, err error, want string, attempts int, before []string) store.Hold {
	t.Helper()
	var unproven *dispatchUnswept
	if !errors.As(err, &unproven) {
		t.Fatalf("the dispatch over a standing copy with no sweep answered %v, want *dispatchUnswept", err)
	}
	if unproven.Hold.Key != want {
		t.Fatalf("the dispatch was refused on hold %+v, want %s", unproven.Hold, want)
	}
	if got, err := sub.records.TaskAttempts(t.Context(), sub.task); err != nil || len(got) != attempts {
		t.Fatalf("the refused dispatch left attempts %+v (%v), want %d", got, err, attempts)
	}
	if after := eventKinds(t, sub.records, sub.task); len(after) != len(before) {
		t.Fatalf("the history moved from %v to %v on a refused dispatch", before, after)
	}
	return unproven.Hold
}

// PRD principle P6 at the retried dispatch, where the platform has no
// sweep: a dispatch retried over a copy a crash left standing, which the
// sweep cannot look in, is refused before the claim and holds for a person
// on a key of its own, naming the task and keyed by the attempt that left
// the copy, distinct from both relaunch holds; no attempt is begun and no
// history entry appended. A service built over the same records, which is
// what the next start is, is refused on the same hold and raises no second
// one. Once the hold is answered retry, two dispatchers that read that
// answer, held between their read of the attempts and their claim so the
// test is of the anchor and not of scheduling, begin one attempt between
// them, the loser refused with ErrTaskMoved. The answer does not carry
// over: the retried attempt, crashed in turn with its copy standing, holds
// again on a new key. The positive control is the same retried dispatch
// with the real sweep on a platform that has one, which begins the attempt
// and raises no hold. Run under the race detector, which make test does.
func TestAnUnsupportedSweepHoldsTheRetriedDispatchUntilARetryAnswer(t *testing.T) {
	principles.Cite(t, principles.P6)
	ctx := t.Context()
	sub, restart := crashedDispatch(t)
	before := eventKinds(t, sub.records, sub.task)
	key := dispatchUnsweptKey(sub.task, 1)

	// Refused, and held for a person on a key of its own.
	_, err := restart.dispatchTask(ctx, sub.task)
	hold := dispatchRefusedOn(t, sub, err, key, 1, before)
	if !hold.Open() {
		t.Fatalf("the hold is %+v, want it open", hold)
	}
	if task, ok := hold.TaskID.Get(); !ok || task != sub.task || hold.RunID.IsKnown() {
		t.Fatalf("the hold names task %v and run %v, want the task and no run", hold.TaskID, hold.RunID)
	}
	if isRelaunchHold(hold.Key, sub.task) {
		t.Fatalf("the dispatch's hold %s is in a relaunch hold's key space", hold.Key)
	}
	for _, other := range []string{relaunchUnsweptKey(sub.task, 1), relaunchBoundKey(sub.task, 1)} {
		if _, err := sub.records.Hold(ctx, other); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("the dispatch's refusal raised the relaunch hold %s too: %v", other, err)
		}
	}

	// The next start is refused on the same hold, through the dispatch pass.
	next, _ := restartedService(t, sub)
	next.sweepCopy = sweepUnsupported
	next.dispatchDue(ctx)
	_, err = next.dispatchClaimed(ctx, sub.task)
	dispatchRefusedOn(t, sub, err, key, 1, before)
	open, err := sub.records.OpenHolds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var naming int
	for _, h := range open {
		if task, ok := h.TaskID.Get(); ok && task == sub.task {
			naming++
		}
	}
	if naming != 1 {
		t.Fatalf("%d open holds name the task after three refusals, want the one", naming)
	}

	// Answered retry: one attempt between two dispatchers.
	if _, err := sub.records.ResolveHold(ctx, key, machine.AnswerRetry, store.ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	var arrived sync.WaitGroup
	arrived.Add(2)
	restart.betweenAttemptReadAndClaim = func() {
		arrived.Done()
		arrived.Wait()
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	outs := make([]dispatched, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			outs[i], errs[i] = restart.dispatchClaimed(ctx, sub.task)
		}()
	}
	wg.Wait()
	restart.betweenAttemptReadAndClaim = nil
	won, lost := -1, 0
	for i, err := range errs {
		switch {
		case err == nil:
			won = i
		case errors.Is(err, store.ErrTaskMoved):
			lost++
		default:
			t.Fatalf("a dispatcher failed for a reason other than the claim: %v", err)
		}
	}
	if won < 0 || lost != 1 {
		t.Fatalf("the dispatchers answered %v, want one winner and one refused claim", errs)
	}
	if outs[won].attempt != 2 || !outs[won].proof.Proven() {
		t.Fatalf("the winner returned %+v, want attempt 2 proven", outs[won])
	}

	// The answer was the first attempt's: the retried one, crashed with its
	// copy standing, holds again on a new key.
	_, err = next.dispatchClaimed(ctx, sub.task)
	again := dispatchRefusedOn(t, sub, err, dispatchUnsweptKey(sub.task, 2), 2, before)
	if !again.Open() {
		t.Fatalf("the hold for the retried attempt is %+v, want it open", again)
	}

	// The control: a platform with a sweep dispatches over the standing copy
	// with no hold.
	control, _ := crashedDispatch(t)
	if _, err := agents.NewSweeper(control.service.home, 0, nil).Sweep(ctx, control.copy); errors.Is(err, agents.ErrSweepUnsupported) {
		t.Logf("%s has no sweep, so the control, a retried dispatch that raises no hold, is not established here", runtime.GOOS)
		return
	}
	requireSweepClearsHere(t, "the control, a retried dispatch with a sweep that raises no hold,")
	swept, _ := restartedService(t, control)
	d, err := swept.dispatchTask(ctx, control.task)
	if err != nil || d.attempt != 2 {
		t.Fatalf("the control's retried dispatch returned attempt %d (%v), want attempt 2", d.attempt, err)
	}
	if _, err := control.records.Hold(ctx, dispatchUnsweptKey(control.task, 1)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the control's dispatch with a sweep raised the no-sweep hold: %v", err)
	}
}

// The dispatch's no-sweep hold is answered the way the relaunch holds are,
// through task.respond's handler: an answer it does not offer is refused
// and leaves it open, a retry begins one attempt over the standing copy
// through the anchored dispatch and reports it as a relaunch's retry is,
// with no superseded session because the crashed attempt recorded none,
// and a stop leaves the task alone, so the next start's dispatch is
// refused on the stopped hold and begins nothing.
func TestTheDispatchNoSweepHoldIsAnsweredRetryOrStop(t *testing.T) {
	ctx := t.Context()
	answer := func(s *Service, task, text string) (machine.TaskAnswered, error) {
		return s.respondTask(ctx, ipc.Request{}, machine.TaskRespondRequest{Task: task, Answer: text})
	}

	sub, restart := crashedDispatch(t)
	before := eventKinds(t, sub.records, sub.task)
	key := dispatchUnsweptKey(sub.task, 1)
	_, err := restart.dispatchTask(ctx, sub.task)
	dispatchRefusedOn(t, sub, err, key, 1, before)
	if _, err := answer(restart, sub.task, "approve"); !errors.Is(err, ipc.ErrInvalidRequest) {
		t.Fatalf("an answer the dispatch's hold does not offer answered %v, want an invalid request", err)
	}
	if h, err := sub.records.Hold(ctx, key); err != nil || !h.Open() {
		t.Fatalf("after a refused answer the hold is %+v (%v), want it open", h, err)
	}
	retried, err := answer(restart, sub.task, machine.AnswerRetry)
	if err != nil {
		t.Fatalf("answering the dispatch's hold: %v", err)
	}
	if retried.Hold.Key != key || retried.Effect != machine.AnswerRelaunched || retried.Relaunch == nil ||
		retried.Relaunch.Attempt != 2 || retried.Relaunch.Superseded != "" {
		t.Fatalf("a retry on the dispatch's hold answered %+v, want attempt 2 begun on %s with nothing superseded", retried, key)
	}
	if attempts, err := sub.records.TaskAttempts(ctx, sub.task); err != nil || len(attempts) != 2 {
		t.Fatalf("the task has attempts %+v (%v), want the crashed one and the retry", attempts, err)
	}

	stopped, stoppedAt := crashedDispatch(t)
	before = eventKinds(t, stopped.records, stopped.task)
	stopKey := dispatchUnsweptKey(stopped.task, 1)
	_, err = stoppedAt.dispatchTask(ctx, stopped.task)
	dispatchRefusedOn(t, stopped, err, stopKey, 1, before)
	out, err := answer(stoppedAt, stopped.task, machine.AnswerStop)
	if err != nil || out.Effect != machine.AnswerStopped || out.Hold.Key != stopKey {
		t.Fatalf("a stop on the dispatch's hold answered %+v (%v), want it stopped", out, err)
	}
	next, _ := restartedService(t, stopped)
	next.sweepCopy = sweepUnsupported
	_, err = next.dispatchTask(ctx, stopped.task)
	h := dispatchRefusedOn(t, stopped, err, stopKey, 1, before)
	if h.Open() || h.Resolution.Or("") != machine.AnswerStop {
		t.Fatalf("the dispatch after a stop was refused on %+v, want the stopped hold", h)
	}
}

// A sweep that failed refuses the relaunch before the claim: no attempt is
// begun, no note is appended, and the refusal is a *copyUnswept wrapping
// the sweep's error, so the next start asks again rather than relaunching
// into a copy a live process may still own.
func TestAFailedSweepRefusesTheRelaunchBeforeTheClaim(t *testing.T) {
	sub := newRelaunchSubject(t)
	ctx := t.Context()
	listingFailed := errors.New("reading /proc: permission denied")
	sub.restart.sweepCopy = func(_ context.Context, dir string) (agents.SweepResult, error) {
		return agents.SweepResult{}, fmt.Errorf("agents: listing processes under %s: %w", dir, listingFailed)
	}
	before := eventKinds(t, sub.records, sub.task)
	_, err := sub.restart.relaunchTask(ctx, sub.task)
	var unswept *copyUnswept
	if !errors.As(err, &unswept) || !errors.Is(err, listingFailed) {
		t.Fatalf("the relaunch answered %v, want *copyUnswept wrapping the listing's failure", err)
	}
	if attempts, err := sub.records.TaskAttempts(ctx, sub.task); err != nil || len(attempts) != 1 {
		t.Fatalf("the task has attempts %+v (%v), want only the first", attempts, err)
	}
	if after := eventKinds(t, sub.records, sub.task); len(after) != len(before) {
		t.Fatalf("the history moved from %v to %v on a refused relaunch", before, after)
	}
}

// PRD section 11's "proves no live process owns it", with the real sweep: a
// process left running in the copy after the worker's terminal is gone, in
// a session of its own so neither the worker's group nor its parent reaches
// it, is ended by the relaunch before the claim, and the relaunched note
// says the copy was reclaimed from whatever was in it. The wait is on the
// process's own exit, not on any record.
func TestARelaunchReclaimsTheCopyFromAProcessLeftInIt(t *testing.T) {
	requireSweepClearsHere(t, "a relaunch reclaiming its copy from a process left in it")
	sub := newRelaunchSubject(t)
	if _, err := agents.NewSweeper(sub.restart.home, 0, nil).Sweep(t.Context(), sub.copy); errors.Is(err, agents.ErrSweepUnsupported) {
		t.Skipf("%s has no sweep, so a relaunch reclaiming its copy is not established here: %v", runtime.GOOS, err)
	}
	left := exec.Command("sleep", "60")
	left.Dir = sub.copy
	detach(left)
	if err := left.Start(); err != nil {
		t.Skipf("no sleep to leave running in the copy, so the reclaim is not established here: %v", err)
	}
	exited := make(chan struct{})
	go func() { _ = left.Wait(); close(exited) }()
	t.Cleanup(func() {
		_ = left.Process.Kill()
		<-exited
	})

	out, err := sub.restart.relaunchTask(t.Context(), sub.task)
	if err != nil {
		t.Fatalf("relaunching over a process left in the copy: %v", err)
	}
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		t.Fatal("the process left in the copy is still running after the relaunch")
	}
	detail := relaunchedDetail(t, sub.records, sub.task)
	if !strings.Contains(detail, "reclaimed from whatever was running in it: 1 process(es)") ||
		!strings.Contains(out.launch.note, "reclaimed") {
		t.Fatalf("the relaunch says %q and tells the worker %q, want both to say the copy was reclaimed from one process", detail, out.launch.note)
	}
	if _, err := os.Stat(sub.copy); err != nil {
		t.Fatalf("the copy is gone after the relaunch: %v", err)
	}
}
