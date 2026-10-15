package service

import (
	"context"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
)

// The bell the loop rings when it appends a wake is what answers an armed
// wait, and the recheck is not: the recheck is pushed to an hour, past the
// wait's own minute, so a wait that answers within seconds of the terminal
// dying was woken by the append. Whatever the loop queued for the planted
// task before the wait is armed is acknowledged first, so the wait starts
// over an empty queue and does not answer while the terminal is live and
// unchanged inside the escalation window; the scripted terminal is then
// read dead, and the wait answers with the terminal-gone row the loop
// appended. Deleting the ring in record leaves this wait blocked for its
// minute, which fails the test.
//
// It cites P9: an armed coordinator is woken by an actionable wake, not on
// a timer.
func TestAnArmedWaitIsWokenByTheLoopsAppendAndNotByItsRecheck(t *testing.T) {
	principles.Cite(t, principles.P9)
	h, records := loopHome(t)
	ctx := t.Context()
	task := plantRunning(t, records, "armedwaitaaa", "pane-armed").ID
	terminals := newScriptedTerminals()
	terminals.live("pane-armed", "working\n")
	l := openLoopHooked(t, h, records, terminals, nil, nil, func(hooks *supervisionHooks) { hooks.wakeRecheck = time.Hour })
	served := make(chan error, 1)
	go func() { served <- l.service.Serve(context.Background()) }()
	defer func() {
		_ = l.service.Close()
		<-served
	}()
	client, err := ipc.Dial(ctx, l.service.Socket(), ipc.ClientConfig{})
	if err != nil {
		t.Fatalf("dialing the service: %v", err)
	}
	defer func() { _ = client.Close() }()

	l.pass(t)
	l.pass(t)
	before, err := l.records(t).QueuedWakes(ctx, machine.DefaultWakesLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) > 0 {
		ids := make([]int64, len(before))
		for i, w := range before {
			ids[i] = w.ID
		}
		if err := l.records(t).AcknowledgeWakes(ctx, ids, store.AcknowledgedByCoordinator()); err != nil {
			t.Fatal(err)
		}
	}

	type answer struct {
		wakes machine.Wakes
		err   error
	}
	armed := make(chan answer, 1)
	go func() {
		var out machine.Wakes
		err := client.Call(ctx, ipc.MethodWakesWait, machine.WakeWaitRequest{TimeoutSeconds: 60}, &out)
		armed <- answer{out, err}
	}()
	select {
	case early := <-armed:
		t.Fatalf("the wait answered over an empty queue and a live terminal: %+v", early)
	case <-time.After(1500 * time.Millisecond):
	}

	terminals.dead("pane-armed")
	select {
	case got := <-armed:
		if got.err != nil {
			t.Fatalf("wakes.wait: %v", got.err)
		}
		if len(wakesOfKind(t, l.records(t), task, supervisor.KindTerminalGone)) == 0 {
			t.Fatalf("the wait answered %+v and the loop recorded no terminal-gone wake", got.wakes)
		}
		found := false
		for _, w := range got.wakes.Wakes {
			found = found || (w.TaskID == task && w.Kind == string(supervisor.KindTerminalGone) && w.HandedOut())
		}
		if !found {
			t.Fatalf("the wait answered %+v, want the terminal-gone wake of task %s", got.wakes, task)
		}
	case <-time.After(loopWait):
		t.Fatal("the wait did not answer after the loop surfaced the dead terminal, so nothing woke it")
	}
}
