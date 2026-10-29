package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/procgroup"
)

// P6 for the reviewer's exact order at 8bfaa6f: giveBackSettledSlot and
// noteRedispatchWhenSteerFrees were two locked steps, so a steer could make
// giveBack a no-op, refuse the already-answered hold, and release before the
// note, leaving the settled slot behind. freeSettledSlotForKeep is one
// critical section under claimSteer's lock: with a steer held across keep,
// the note is recorded before any release can run, and that release begins
// the pass. The fail-before of the two-lock gap is the overlay that released
// between those steps (REPORT); this test is the order after the fix, under
// -race -count=20.
func TestKeepDispatchesWhenSteerReleasesBetweenGiveBackAndNote(t *testing.T) {
	principles.Cite(t, principles.P6)
	const unestablished = "that keep's give-back and redispatch note are one critical section"
	requireSweepClearsHere(t, unestablished)
	ctx := t.Context()
	sub := launchSubjectOver(t, workerStandIn(t), passDispatchSubject(t))
	s := sub.service
	broken := filepath.Join(t.TempDir(), "not-a-worker")
	if err := procgroup.WriteProgram(broken, []byte("this is not a program\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.catalog = agents.NewCatalog(agents.ClaudeFactory(agents.WithBinary(broken)), cursorFactory{runner: sub.agent.Runner()})
	s.cfg.Roles = config.Roles{config.RoleHousekeeping: config.Role{Agent: agents.ClaudeName}}
	s.terminals = heldTerminals{s}
	s.stopCtx, s.stopCancel = context.WithCancel(ctx)
	t.Cleanup(func() { s.stopCancel(); s.stopWork(); s.work.Wait() })

	d, err := s.dispatchTask(ctx, sub.task)
	if err != nil {
		t.Fatalf("dispatching: %v", err)
	}
	if launched, err := s.launchTask(ctx, d); launched != nil || err == nil {
		t.Fatalf("launch = %v, %v; want it failed", launched, err)
	}
	h := offerRetirementHoldOf(t, sub.records, sub.task)
	rh, ok := parseRetireHold(h.Key, sub.task)
	if !ok {
		t.Fatal(h.Key)
	}

	// The concurrent retire's claimSteer across keep's slot decision.
	release, err := s.claimSteer(sub.task)
	if err != nil {
		t.Fatalf("claimSteer: %v", err)
	}
	if _, err := s.answerRetirement(ctx, ipc.Request{}, machine.TaskRespondRequest{
		Task: sub.task, Answer: machine.AnswerKeep,
	}, h, rh); err != nil {
		release()
		t.Fatalf("keep: %v", err)
	}
	// Release as the refuse-and-return of that retire would: the noted
	// redispatch runs here. A gap between giveBack and note would have left
	// the slot settled with no note and this release would begin nothing.
	release()

	deadline := time.Now().Add(30 * time.Second)
	for {
		attempts := recordedAttempts(t, sub.records, sub.task)
		if len(attempts) >= 2 {
			return
		}
		if !time.Now().Before(deadline) {
			_, held := s.isolationOf(sub.task)
			fatalUnlessSweepsStoppedClearing(t, unestablished,
				"keep lost its dispatch in the giveBack/note gap: attempts=%+v isolationHeld=%v", attempts, held)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
