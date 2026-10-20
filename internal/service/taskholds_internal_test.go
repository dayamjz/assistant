package service

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// PRD principle P6 at the relaunch bound: a retry answered on the bound's
// hold is good for one attempt and no more. With relaunch_attempts at zero
// the first dead terminal is past the bound, so nothing relaunches until the
// hold for that attempt is answered; once it is answered retry, two
// relaunchers that read the same allowance, held between their read of the
// attempts and their claim so the test is of the anchor and not of
// scheduling, begin one attempt between them, and the loser is refused with
// ErrTaskMoved. The allowance does not carry over: a third relaunch, made
// after the winner's attempt is the latest and with that attempt's terminal
// read Dead, is refused as not due, because no hold for the new attempt has
// been answered. Run under the race detector, which make test does.
func TestARetryPastTheBoundBeginsOneAttemptAndNoMore(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	const unestablished = "that a retry past the relaunch bound begins one attempt"
	requireSweepClearsHere(t, unestablished)
	sub := newRelaunchSubject(t)
	ctx := t.Context()
	sub.restart.cfg.RelaunchAttempts = 0

	// Unanswered, the bound refuses the relaunch and records nothing.
	if _, err := sub.restart.relaunchClaimed(ctx, sub.task, ""); !errors.Is(err, errRelaunchNotDue) {
		t.Fatalf("a relaunch past the bound with no answer answered %v, want not due", err)
	}
	attempts, err := sub.records.TaskAttempts(ctx, sub.task)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("the refused relaunch began an attempt: %+v (%v)", attempts, err)
	}
	decision := reconcileDecision{reason: "it has been relaunched 0 times and relaunch_attempts is 0"}
	hold, err := sub.restart.holdAtBound(ctx, sub.task, attempts, decision)
	if err != nil {
		t.Fatal(err)
	}
	// Open, the hold is still no allowance.
	if _, err := sub.restart.relaunchClaimed(ctx, sub.task, ""); !errors.Is(err, errRelaunchNotDue) {
		t.Fatalf("a relaunch past the bound with the hold open answered %v, want not due", err)
	}
	if _, err := sub.records.ResolveHold(ctx, hold.Key, machine.AnswerRetry, store.ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}

	arrived := newRendezvous(2)
	sub.restart.betweenAttemptReadAndClaim = arrived.Arrive
	var wg sync.WaitGroup
	errs := make([]error, 2)
	outs := make([]relaunched, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer arrived.Leave()
			outs[i], errs[i] = sub.restart.relaunchClaimed(ctx, sub.task, "")
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
			fatalUnlessSweepsStoppedClearing(t, unestablished, "a relauncher failed for a reason other than the claim: %v", err)
		}
	}
	if won < 0 || lost != 1 {
		t.Fatalf("the relaunchers answered %v, want one winner and one refused claim", errs)
	}
	if outs[won].launch.attempt != 2 {
		t.Fatalf("the winner began attempt %d, want 2", outs[won].launch.attempt)
	}
	if detail := relaunchedDetail(t, sub.records, sub.task); !strings.Contains(detail, "relaunch_attempts is 0") || !strings.Contains(detail, "answered "+machine.AnswerRetry) {
		t.Fatalf("the relaunched note says %q, want the bound and the answer that let it past", detail)
	}

	// The winner's attempt launched and ended: its terminal is dead, the
	// task is past the bound again, and the retry already spent says
	// nothing about this attempt.
	launched, err := sub.restart.launchTask(ctx, outs[won].launch)
	if err != nil {
		t.Fatalf("launching the retried attempt: %v", err)
	}
	provider, _, _ := sub.restart.sessionOf(sub.task)
	for _, err := range sub.restart.closeSessions() {
		t.Fatal(err)
	}
	waitLiveness(t, provider, launched.Reference().String(), taskstate.Dead)
	if _, err := sub.restart.relaunchClaimed(ctx, sub.task, ""); !errors.Is(err, errRelaunchNotDue) {
		t.Fatalf("a relaunch of the retried attempt answered %v, want not due: the allowance was the first attempt's", err)
	}
	if attempts, err := sub.records.TaskAttempts(ctx, sub.task); err != nil || len(attempts) != 2 {
		t.Fatalf("the task has attempts %+v (%v), want the first and the one retry", attempts, err)
	}
}
