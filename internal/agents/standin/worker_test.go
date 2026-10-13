package standin

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
)

// workerAgent builds a stand-in whose worker greets, answers two kinds of
// line, and ignores the rest, which is enough to tell a reply from an echo
// and a scripted answer from a default.
func workerAgent(t *testing.T) *Agent {
	t.Helper()
	return NewWorker(t, Script{Worker: &WorkerScript{
		Greeting: "worker ready",
		Exchanges: []Exchange{
			{Contains: "status", Reply: "still working on it"},
			{Contains: "stop", Reply: "stopping"},
		},
	}})
}

// waitForOutput polls the handle until its capture carries want, which is how
// a test reads a reply that arrives on the worker's own schedule.
func waitForOutput(t *testing.T, w agents.Worker, want string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		got := w.Capture().Text
		if strings.Contains(got, want) {
			return got
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the worker's output never carried %q; it is:\n%s", want, got)
		}
		time.Sleep(time.Millisecond)
	}
}

// waitForLines polls until the worker's one recorded call carries n lines, on
// the terms Agent.Calls states for a worker's record.
func waitForLines(t *testing.T, agent *Agent, n int) Call {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		calls := agent.Calls()
		if len(calls) == 1 && len(calls[0].Lines) >= n {
			return calls[0]
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the worker recorded %s, waited for one call with %d lines", calls, n)
		}
		time.Sleep(time.Millisecond)
	}
}

// A scripted reply to a line on standard input reaches the caller through the
// real adapter's handle, and the recorded Call carries the line. Both halves
// are read off the wire: the reply out of what the adapter captured from the
// process, and the line out of what the process wrote down about itself.
func TestAWorkerAnswersALineOverTheWireAndRecordsIt(t *testing.T) {
	agent := workerAgent(t)
	runner := agent.Runner()
	if !runner.Capabilities().Has(agents.CapabilityWorkerSessions) {
		t.Fatalf("the scripted adapter declares %v, want worker sessions among them", runner.Capabilities())
	}

	worker, err := agents.OpenWorker(t.Context(), runner, agents.WorkerSpec{
		Dir:          t.TempDir(),
		Instructions: "implement the thing\nand say when you are done",
	})
	if err != nil {
		t.Fatalf("starting the worker: %v", err)
	}
	defer worker.Close()
	if worker.Group() <= 0 {
		t.Errorf("the worker reports group %d, want a positive identifier", worker.Group())
	}
	waitForOutput(t, worker, "worker ready")

	if err := worker.Send(t.Context(), "status?"); err != nil {
		t.Fatalf("sending a line: %v", err)
	}
	got := waitForOutput(t, worker, "still working on it")
	if strings.Contains(got, "status?") {
		t.Errorf("the worker's output carries the line it was sent, so this would pass on an echo:\n%s", got)
	}

	call := waitForLines(t, agent, 3)
	if !call.Worker {
		t.Error("the recorded call does not say it was a worker")
	}
	want := []string{"implement the thing", "and say when you are done", "status?"}
	if len(call.Lines) != len(want) {
		t.Fatalf("the worker recorded lines %q, want %q", call.Lines, want)
	}
	for i := range want {
		if call.Lines[i] != want[i] {
			t.Errorf("line %d is %q, want %q", i, call.Lines[i], want[i])
		}
	}
	if call.Prompt != "" {
		t.Errorf("a worker's call carries a prompt %q, want its input in Lines", call.Prompt)
	}
	if strings.Contains(strings.Join(call.Args, " "), printFlag) {
		t.Errorf("the worker was started with %s, which is the one-shot invocation's flag: %v", printFlag, call.Args)
	}
}

// Close ends the worker, after which a send is refused rather than reported
// delivered, and the worker exits with the status its script named once its
// input closed.
func TestCloseEndsTheWorkerAndASendAfterItIsRefused(t *testing.T) {
	agent := NewWorker(t, Script{Worker: &WorkerScript{Exit: 0}})
	worker, err := agents.OpenWorker(t.Context(), agent.Runner(), agents.WorkerSpec{
		Dir: t.TempDir(), Instructions: "wait for input",
	})
	if err != nil {
		t.Fatalf("starting the worker: %v", err)
	}
	select {
	case <-worker.Done():
		t.Fatal("the worker ended before anything closed it")
	default:
	}

	worker.Close()
	select {
	case <-worker.Done():
	default:
		t.Fatal("Close returned and Done is still open")
	}
	if err := worker.Send(t.Context(), "anything"); !errors.Is(err, agents.ErrWorkerEnded) {
		t.Errorf("a send after Close answered %v, want ErrWorkerEnded", err)
	}
	// Idempotent: a second Close costs nothing and changes nothing.
	worker.Close()
}

// A line no exchange answers produces nothing on standard output and a note
// on standard error, both of which the capture carries, so a worker's silence
// is visible as the stand-in's own rather than mistaken for a reply.
func TestAnUnscriptedLineIsNotedRatherThanAnswered(t *testing.T) {
	agent := workerAgent(t)
	worker, err := agents.OpenWorker(t.Context(), agent.Runner(), agents.WorkerSpec{
		Dir: t.TempDir(), Instructions: "begin",
	})
	if err != nil {
		t.Fatalf("starting the worker: %v", err)
	}
	defer worker.Close()

	got := waitForOutput(t, worker, "no exchange of the script answers this line: begin")
	if strings.Contains(got, "still working") || strings.Contains(got, "stopping") {
		t.Errorf("an unscripted line was answered out of an exchange it does not match:\n%s", got)
	}
}

// The one-shot half of a script still answers one-shot invocations on the
// same adapter, so a test can script a worker and the invocations beside it
// in one Agent.
func TestAWorkerAgentStillAnswersOneShotInvocations(t *testing.T) {
	agent := NewWorker(t, Script{
		Steps:  []Step{{Times: Always, Reply: Text("one-shot answer")}},
		Worker: &WorkerScript{Greeting: "hello"},
	})
	result, err := agent.Runner().Run(t.Context(), agents.PurposeReview, invocation(t, agents.ShapeText))
	if err != nil {
		t.Fatalf("a one-shot invocation on the worker adapter failed: %v", err)
	}
	if result.Text != "one-shot answer" {
		t.Errorf("the invocation answered %q, want the scripted step", result.Text)
	}
	if result.Record.Agent != agents.ScriptedName {
		t.Errorf("the record names agent %q, want %q", result.Record.Agent, agents.ScriptedName)
	}
	if call := agent.Call(); call.Worker || len(call.Lines) != 0 {
		t.Errorf("a one-shot invocation recorded itself as a worker: %s", call)
	}
}
