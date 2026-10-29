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

// P6 for keep after a failed launch: a concurrent retire that holds
// claimSteer across keep's giveBackSettledSlot must not leave the settled
// slot behind. keep notes a redispatch for that steer's release, which
// gives the slot back and asks dispatchDue, so attempt 2 is claimed.
// Before, giveBackSettledSlot was a no-op under the steer, dispatch skipped
// the occupied slot, and after retire refused the already-answered hold the
// slot stayed settled with one sessionless attempt.
func TestKeepDispatchesAfterAConcurrentRetireReleasesTheSettledSlot(t *testing.T) {
	principles.Cite(t, principles.P6)
	const unestablished = "that keep's dispatch survives a concurrent retire"
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
	if s.launching(sub.task) {
		t.Fatal("failed launch left the slot unsettled")
	}
	h := offerRetirementHoldOf(t, sub.records, sub.task)
	rh, ok := parseRetireHold(h.Key, sub.task)
	if !ok {
		t.Fatal(h.Key)
	}

	steering := make(chan struct{})
	kept := make(chan struct{})
	released := make(chan struct{})
	go func() {
		release, err := s.claimSteer(sub.task)
		if err != nil {
			t.Errorf("claimSteer: %v", err)
			close(steering)
			close(released)
			return
		}
		close(steering)
		<-kept // keep has resolved the hold and noted or dispatched under this steer
		_, _ = s.answerRetirement(ctx, ipc.Request{}, machine.TaskRespondRequest{
			Task: sub.task, Answer: machine.AnswerRetire,
		}, h, rh)
		release()
		close(released)
	}()
	<-steering
	if _, err := s.answerRetirement(ctx, ipc.Request{}, machine.TaskRespondRequest{
		Task: sub.task, Answer: machine.AnswerKeep,
	}, h, rh); err != nil {
		t.Fatalf("keep: %v", err)
	}
	close(kept)
	<-released

	deadline := time.Now().Add(30 * time.Second)
	for {
		attempts := recordedAttempts(t, sub.records, sub.task)
		if len(attempts) >= 2 {
			return
		}
		if !time.Now().Before(deadline) {
			_, held := s.isolationOf(sub.task)
			fatalUnlessSweepsStoppedClearing(t, unestablished,
				"keep lost its dispatch to the concurrent retire: attempts=%+v isolationHeld=%v", attempts, held)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
