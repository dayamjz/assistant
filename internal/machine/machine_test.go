package machine_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/store"
)

// Text a stage reported travels through this surface, and a stage's text is
// whatever an agent wrote. A control character that arrived raw on a terminal
// would be an instruction to that terminal rather than something a reader sees.
func TestControlCharactersInAnAnswerAreEscaped(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	answer := machine.Failure{Error: "an agent said \x1b[2J\x07 and then \x00 stopped"}
	if err := machine.NewEncoder(&out).Encode(answer); err != nil {
		t.Fatalf("encoding: %v", err)
	}
	line := out.String()
	for _, raw := range []string{"\x1b", "\x07", "\x00"} {
		if strings.Contains(line, raw) {
			t.Fatalf("the answer carries a raw control character: %q", line)
		}
	}
	if !strings.Contains(line, `\u001b`) {
		t.Fatalf("the escape was not written visibly: %q", line)
	}
	// It is still one document a consumer can decode back to what was said.
	var back machine.Failure
	if err := json.Unmarshal([]byte(line), &back); err != nil {
		t.Fatalf("decoding what was written: %v", err)
	}
	if back.Error != answer.Error {
		t.Fatalf("the round trip changed the text: %q", back.Error)
	}
}

func TestOneAnswerIsOneLine(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	enc := machine.NewEncoder(&out)
	for _, outcome := range machine.Outcomes() {
		if err := enc.Encode(machine.Run{Outcome: outcome}); err != nil {
			t.Fatalf("encoding %s: %v", outcome, err)
		}
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != len(machine.Outcomes()) {
		t.Fatalf("%d answers produced %d lines", len(machine.Outcomes()), len(lines))
	}
	for _, line := range lines {
		var back machine.Run
		if err := json.Unmarshal([]byte(line), &back); err != nil {
			t.Fatalf("a line does not decode on its own: %q: %v", line, err)
		}
	}
}

// Every outcome carries a next action, because PRD section 9 refuses silence
// after a terminal one. Reading it off the table rather than listing the
// members again is what keeps a member added later from arriving without one.
func TestEveryOutcomeSaysWhatToDoNext(t *testing.T) {
	t.Parallel()
	for _, outcome := range machine.Outcomes() {
		if strings.TrimSpace(outcome.NextAction()) == "" {
			t.Fatalf("%s carries no next action", outcome)
		}
	}
}

func TestOnlyADecisionIsNotTerminal(t *testing.T) {
	t.Parallel()
	for _, outcome := range machine.Outcomes() {
		terminal := outcome != machine.OutcomeDecision
		if outcome.Terminal() != terminal {
			t.Fatalf("%s reports Terminal %v", outcome, outcome.Terminal())
		}
	}
}

func TestWhereARunStoppedDecidesTheOutcomeOfARunThatMayStillMove(t *testing.T) {
	t.Parallel()
	cancelled := cancelledState(t)
	cases := []struct {
		name   string
		status graph.Status
		state  graph.State
		want   machine.Outcome
	}{
		{"halted", graph.StatusHalted, graph.State{}, machine.OutcomeDecision},
		{"completed", graph.StatusCompleted, graph.State{}, machine.OutcomeChecksPassed},
		{"completed after a cancellation", graph.StatusCompleted, cancelled, machine.OutcomeCancelled},
		{"rounds exhausted", graph.StatusRoundsExhausted, graph.State{}, machine.OutcomeFailed},
		{"budget exhausted", graph.StatusBudgetExhausted, graph.State{}, machine.OutcomeFailed},
		{"converged", graph.StatusConverged, graph.State{}, machine.OutcomeFailed},
	}
	for _, c := range cases {
		if got := machine.OutcomeOf(store.RunRunning, c.status, c.state); got != c.want {
			t.Fatalf("a %s run reports %s, want %s", c.name, got, c.want)
		}
	}
}

// A terminal record wins over the checkpoint, which is what keeps a run that
// was ended while it stood at a hold from still offering an answer.
func TestATerminalRecordDecidesOverWhereExecutionStopped(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		record store.RunStatus
		status graph.Status
		want   machine.Outcome
	}{
		{"ended while holding", store.RunTerminated, graph.StatusHalted, machine.OutcomeCancelled},
		{"ended while running", store.RunTerminated, graph.StatusRunning, machine.OutcomeCancelled},
		{"terminated by a bound", store.RunTerminated, graph.StatusBudgetExhausted, machine.OutcomeFailed},
		{"passed", store.RunPassed, graph.StatusCompleted, machine.OutcomeChecksPassed},
		{"failed", store.RunFailed, graph.StatusCompleted, machine.OutcomeFailed},
	}
	for _, c := range cases {
		if got := machine.OutcomeOf(c.record, c.status, graph.State{}); got != c.want {
			t.Fatalf("a run %s reports %s, want %s", c.name, got, c.want)
		}
	}
}

// Every status internal/store defines has an answer here, so a run this
// package cannot interpret is never one a caller is invited to drive.
func TestEveryRecordedStatusHasAnOutcome(t *testing.T) {
	t.Parallel()
	for _, status := range store.RunStatuses() {
		got := machine.OutcomeOf(status, graph.StatusHalted, graph.State{})
		if got == "" {
			t.Fatalf("a run recorded as %s reports no outcome", status)
		}
	}
	if got := machine.OutcomeOf(store.RunStatus("invented"), graph.StatusHalted, graph.State{}); got != machine.OutcomeFailed {
		t.Fatalf("a run in a status this build does not define reports %s, want failed", got)
	}
}

// cancelledState is the state a run a person cancelled stands in. It is
// reached by driving the real pipeline to it rather than by stating the key
// here, so a test cannot state a state the mechanism could not produce.
func cancelledState(t *testing.T) graph.State {
	t.Helper()
	holding := pipeline.ConstantStages("held for a decision", findings.Finding{
		ID:          "held",
		Action:      findings.ActionAsk,
		Description: "a decision a person has to make",
	})
	p, err := pipeline.New(pipeline.Options{Stages: holding, Budget: 40})
	if err != nil {
		t.Fatalf("building a pipeline: %v", err)
	}
	initial, err := p.NewState(pipeline.Start{Branch: "b", Base: "main", Submitted: "abc"})
	if err != nil {
		t.Fatalf("building the initial state: %v", err)
	}
	executor, err := p.Executor(graph.NewMemoryStore())
	if err != nil {
		t.Fatalf("building an executor: %v", err)
	}
	result, err := executor.Run(t.Context(), "run", initial)
	if err != nil {
		t.Fatalf("running: %v", err)
	}
	if result.Status != graph.StatusHalted {
		t.Fatalf("the first stage did not hold: %s", result.Status)
	}
	answered, err := executor.Answer(t.Context(), "run", string(pipeline.OutcomeCancelled))
	if err != nil {
		t.Fatalf("cancelling at the hold: %v", err)
	}
	if answered.Status != graph.StatusCompleted {
		t.Fatalf("a cancelled run stopped at %s", answered.Status)
	}
	return answered.State
}
