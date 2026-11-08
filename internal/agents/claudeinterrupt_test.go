package agents_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
)

// A Claude Code stream-json worker running a long turn is interrupted over
// control_request subtype interrupt, stays alive, and answers the next
// message. Fail-before: InterruptPipeWorker refuses with CapabilityError
// naming pipe_interrupt because Claude declares none for the pipe.
func TestClaudePipeInterruptEndsATurnAndKeepsTheWorker(t *testing.T) {
	const long = "conformance: a turn long enough to interrupt"
	agent := standin.New(t, standin.Script{Worker: &standin.WorkerScript{Exchanges: []standin.Exchange{
		{Contains: "initial", Reply: "ready"},
		{Contains: long, Reply: "the long turn ended by itself", Turn: time.Hour},
		{Contains: "after", Reply: "heard after the interrupt"},
	}}})
	worker, err := agents.OpenWorker(t.Context(), agent.Runner(), agents.WorkerSpec{
		Dir: t.TempDir(), Instructions: "initial instructions",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(worker.Capture().Text, "ready") {
		if time.Now().After(deadline) {
			t.Fatalf("instructions never settled: %s", worker.Capture().Text)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := worker.Send(t.Context(), long); err != nil {
		t.Fatal(err)
	}
	interrupted, err := agents.InterruptPipeWorker(t.Context(), agent.Runner(), worker)
	var refusal *agents.CapabilityError
	if errors.As(err, &refusal) {
		t.Fatalf("fail-before: InterruptPipeWorker refused with %v (capability %s)", err, refusal.Capability)
	}
	if err != nil || !interrupted {
		t.Fatalf("InterruptPipeWorker: %t, %v", interrupted, err)
	}
	select {
	case <-worker.Done():
		t.Fatal("the worker ended on the interrupt")
	default:
	}
	if interrupted, err := agents.InterruptPipeWorker(t.Context(), agent.Runner(), worker); err != nil || interrupted {
		t.Fatalf("idle interrupt answered %t, %v, want false and nil", interrupted, err)
	}
	if err := worker.Send(t.Context(), "after the interrupt"); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for !strings.Contains(worker.Capture().Text, "heard after the interrupt") {
		if time.Now().After(deadline) {
			t.Fatalf("next message never answered: %s", worker.Capture().Text)
		}
		time.Sleep(5 * time.Millisecond)
	}
	calls := agent.Calls()
	if len(calls) != 1 || len(calls[0].Interrupted) != 1 || calls[0].Interrupted[0] != long {
		t.Fatalf("stand-in recorded %+v, want the long turn interrupted once", calls)
	}
}
