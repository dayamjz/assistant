package service

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
)

// A retry refused because the task's record cannot be dispatched reports
// AnswerRetryUndispatchable, and what that effect says - nothing begins the
// work until the record changes - is what the mechanism does: every path
// that begins an attempt reads the record through expectationOf before it
// claims one. Both retry paths are driven through task.respond's handler
// with the task's instructions file removed after its hold was raised: a
// dispatch retried on its no-sweep hold, and a relaunch retried on its
// no-sweep hold. Each reports the new effect naming the record's refusal,
// begins no attempt and appends no history, and a service built over the
// same records, which is what the next start is, runs its start-time passes
// and the direct asks and still begins nothing. The positive control is the
// same subject once the file is back: the answer was not spent, so the next
// start's dispatch or relaunch begins attempt 2 on it, which is what shows
// the refusal was the record's and not something else that keeps the task
// from being begun. The retries over an intact copy that
// TestTheDispatchNoSweepHoldIsAnsweredRetryOrStop and
// TestTheNoSweepHoldIsAnsweredRetryOrStop drive are the controls for the
// answer path itself.
func TestARetryOnARecordThatCannotBeDispatchedSaysNothingBeginsItAndNothingDoes(t *testing.T) {
	ctx := t.Context()
	answer := func(s *Service, task string) machine.TaskAnswered {
		t.Helper()
		out, err := s.respondTask(ctx, ipc.Request{}, machine.TaskRespondRequest{Task: task, Answer: machine.AnswerRetry})
		if err != nil {
			t.Fatalf("answering task %s's hold retry: %v", task, err)
		}
		return out
	}
	attemptsOf := func(records *store.Store, task string) int {
		t.Helper()
		got, err := records.TaskAttempts(ctx, task)
		if err != nil {
			t.Fatal(err)
		}
		return len(got)
	}
	// unfile removes the task's instructions file and returns what puts it
	// back, which is the one part of what expectationOf reads a test can
	// change without a second writer of the task's record.
	unfile := func(sub *launchSubject) func() {
		t.Helper()
		path, err := sub.service.home.TaskInstructions(sub.task)
		if err != nil {
			t.Fatal(err)
		}
		saved, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		return func() {
			t.Helper()
			if err := os.WriteFile(path, saved, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	undispatchable := func(name string, out machine.TaskAnswered, sub *launchSubject, before []string) {
		t.Helper()
		if out.Effect != machine.AnswerRetryUndispatchable || out.Relaunch != nil ||
			!strings.Contains(out.NotRelaunched, errTaskRecord.Error()) ||
			!strings.Contains(out.NotRelaunched, "no instructions file") {
			t.Fatalf("%s: the retry answered %+v, want %s naming the missing instructions file and no attempt",
				name, out, machine.AnswerRetryUndispatchable)
		}
		if out.Hold.Open() || out.Hold.Resolution.Or("") != machine.AnswerRetry {
			t.Fatalf("%s: the hold stands %+v, want it resolved %q", name, out.Hold, machine.AnswerRetry)
		}
		if n := attemptsOf(sub.records, sub.task); n != 1 {
			t.Fatalf("%s: the refused retry left %d attempts, want 1", name, n)
		}
		if after := eventKinds(t, sub.records, sub.task); len(after) != len(before) {
			t.Fatalf("%s: the history moved from %v to %v on a refused retry", name, before, after)
		}
	}

	// A dispatch retried on its no-sweep hold.
	dispatch, restart := crashedDispatch(t)
	if _, err := restart.dispatchTask(ctx, dispatch.task); err == nil {
		t.Fatal("the dispatch with no sweep went ahead with no answer")
	}
	refile := unfile(dispatch)
	before := eventKinds(t, dispatch.records, dispatch.task)
	undispatchable("dispatch", answer(restart, dispatch.task), dispatch, before)
	next, _ := restartedService(t, dispatch)
	next.sweepCopy = sweepUnsupported
	next.reconcileTasks(ctx)
	next.dispatchDue(ctx)
	if _, err := next.dispatchClaimed(ctx, dispatch.task); !errors.Is(err, errTaskRecord) {
		t.Fatalf("a dispatch at the next start answered %v, want the record's refusal", err)
	}
	if n := attemptsOf(dispatch.records, dispatch.task); n != 1 {
		t.Fatalf("the next start left the dispatched task with %d attempts, want 1", n)
	}
	refile()
	fixed, _ := restartedService(t, dispatch)
	fixed.sweepCopy = sweepUnsupported
	if d, err := fixed.dispatchTask(ctx, dispatch.task); err != nil || d.attempt != 2 {
		t.Fatalf("the dispatch once the record was put right returned attempt %d (%v), want attempt 2 on the standing answer",
			d.attempt, err)
	}

	// A relaunch retried on its no-sweep hold.
	relaunch := newRelaunchSubject(t)
	relaunch.restart.sweepCopy = sweepUnsupported
	var unswept *relaunchUnswept
	if _, err := relaunch.restart.relaunchTask(ctx, relaunch.task); !errors.As(err, &unswept) {
		t.Fatalf("the relaunch with no sweep answered %v, want its hold", err)
	}
	refile = unfile(relaunch.launchSubject)
	before = eventKinds(t, relaunch.records, relaunch.task)
	undispatchable("relaunch", answer(relaunch.restart, relaunch.task), relaunch.launchSubject, before)
	again, _ := restartedService(t, relaunch.launchSubject)
	again.sweepCopy = sweepUnsupported
	again.reconcileTasks(ctx)
	if _, err := again.relaunchTask(ctx, relaunch.task); !errors.Is(err, errTaskRecord) {
		t.Fatalf("a relaunch at the next start answered %v, want the record's refusal", err)
	}
	if n := attemptsOf(relaunch.records, relaunch.task); n != 1 {
		t.Fatalf("the next start left the relaunched task with %d attempts, want 1", n)
	}
	refile()
	if r, err := again.relaunchTask(ctx, relaunch.task); err != nil || r.launch.attempt != 2 {
		t.Fatalf("the relaunch once the record was put right returned attempt %d (%v), want attempt 2 on the standing answer",
			r.launch.attempt, err)
	}
}

// retryNotBegun tells the record's refusal apart from the rest and from the
// isolation assertion's, however it is wrapped, and keeps
// AnswerRetryDeferred for every refusal its doc lists as deferred. The
// oracle is written from that doc rather than read off the function, and
// the refusals are the values the paths return, wrapped as they wrap them.
func TestRetryNotBegunSplitsTheUndispatchableRecordFromTheDeferredRefusals(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		err  error
		want machine.AnswerEffect
	}{
		{fmt.Errorf("%w: task t names no repository to cut a copy of", errTaskRecord), machine.AnswerRetryUndispatchable},
		{fmt.Errorf("%w: task t: %w", errTaskRecord, errors.New("the copy path is not absolute")), machine.AnswerRetryUndispatchable},
		{&isolationRefused{Task: "t", Cause: errors.New("not a repository")}, machine.AnswerRetryRefused},
		{fmt.Errorf("%w: task t", errRelaunchInFlight), machine.AnswerRetryDeferred},
		{fmt.Errorf("%w: task t", errRelaunchNotDue), machine.AnswerRetryDeferred},
		{fmt.Errorf("%w: task t", errTaskNotDispatched), machine.AnswerRetryDeferred},
		{fmt.Errorf("%w: task t", errTaskLaunched), machine.AnswerRetryDeferred},
		{fmt.Errorf("%w: task t", errRelaunchNotDead), machine.AnswerRetryDeferred},
		{&relaunchHeld{Task: "t", Holder: "u"}, machine.AnswerRetryDeferred},
		{&copyUnswept{Task: "t", Cause: errors.New("listing failed")}, machine.AnswerRetryDeferred},
		{fmt.Errorf("store: %w", store.ErrTaskMoved), machine.AnswerRetryDeferred},
		{errors.New("service: reading the attempts: disk I/O error"), machine.AnswerRetryDeferred},
	} {
		if got := retryNotBegun(c.err); got != c.want {
			t.Errorf("retryNotBegun(%v) = %s, want %s", c.err, got, c.want)
		}
	}
}
