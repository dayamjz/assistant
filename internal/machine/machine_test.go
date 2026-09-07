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

// The two outcomes a run can be answered or read past are the two that are not
// terminal: a decision waits for an answer, and an executing run is between
// two of them. Everything else has ended.
func TestOnlyADecisionAndAnExecutingRunAreNotTerminal(t *testing.T) {
	t.Parallel()
	moving := map[machine.Outcome]bool{
		machine.OutcomeDecision:  true,
		machine.OutcomeExecuting: true,
	}
	for _, outcome := range machine.Outcomes() {
		if outcome.Terminal() == moving[outcome] {
			t.Fatalf("%s reports Terminal %v", outcome, outcome.Terminal())
		}
	}
}

// A checkpoint that says a run is running is a run in flight, not one that
// ended without a verdict. internal/graph writes that status after every node
// that neither halts nor ends the run, so it is what a read of a run mid
// segment finds.
func TestARunInFlightIsReportedAsExecutingRatherThanFailed(t *testing.T) {
	t.Parallel()
	for _, record := range []store.RunStatus{store.RunPending, store.RunRunning, store.RunHeld} {
		got := machine.OutcomeOf(standingAt(record, graph.StatusRunning, graph.State{}))
		if got != machine.OutcomeExecuting {
			t.Fatalf("a %s run standing at a running checkpoint reports %s, want executing", record, got)
		}
		if got.Terminal() {
			t.Fatalf("%s reports that the run has ended", got)
		}
		if got.NextAction() == "" {
			t.Fatalf("%s says nothing about what to do next", got)
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
		if got := machine.OutcomeOf(standingAt(store.RunRunning, c.status, c.state)); got != c.want {
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
		if got := machine.OutcomeOf(standingAt(c.record, c.status, graph.State{})); got != c.want {
			t.Fatalf("a run %s reports %s, want %s", c.name, got, c.want)
		}
	}
}

// Every status internal/store defines has an answer here, so a run this
// package cannot interpret is never one a caller is invited to drive.
func TestEveryRecordedStatusHasAnOutcome(t *testing.T) {
	t.Parallel()
	for _, status := range store.RunStatuses() {
		got := machine.OutcomeOf(standingAt(status, graph.StatusHalted, graph.State{}))
		if got == "" {
			t.Fatalf("a run recorded as %s reports no outcome", status)
		}
	}
	if got := machine.OutcomeOf(standingAt(store.RunStatus("invented"), graph.StatusHalted, graph.State{})); got != machine.OutcomeFailed {
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

// Executing is the one outcome that describes both a run in flight and a run
// nothing is carrying on, and the two take opposite actions: pause, or attach.
// Every other outcome describes a run whose position nothing is moving either
// way, so knowing that nothing is moving it adds nothing to what to do about
// it.
//
// It reads the closed set rather than naming the members, so an outcome added
// later is held to the same rule instead of arriving outside it.
func TestOnlyAnExecutingRunHasTwoNextActions(t *testing.T) {
	t.Parallel()
	for _, outcome := range machine.Outcomes() {
		moving, still := outcome.NextActionFor(true), outcome.NextActionFor(false)
		if strings.TrimSpace(moving) == "" || strings.TrimSpace(still) == "" {
			t.Fatalf("%s carries no next action for one of the two: %q and %q", outcome, moving, still)
		}
		differs := moving != still
		if differs != (outcome == machine.OutcomeExecuting) {
			t.Fatalf("%s answers %v to whether the two differ, want %v",
				outcome, differs, outcome == machine.OutcomeExecuting)
		}
		if !differs && moving != outcome.NextAction() {
			t.Fatalf("%s answers %q here and %q from its own row", outcome, moving, outcome.NextAction())
		}
	}
}

// A run nothing is advancing is told to attach, and a run something is
// advancing is not. Getting these the wrong way round is the failure the
// distinction exists to prevent: it would have a reader wait on a run nothing
// is carrying on.
func TestTheActionForAStalledRunIsTheOneThatCarriesItOn(t *testing.T) {
	t.Parallel()
	stalled := machine.OutcomeExecuting.NextActionFor(false)
	moving := machine.OutcomeExecuting.NextActionFor(true)
	if !strings.Contains(strings.ToLower(stalled), "attach") {
		t.Fatalf("a stalled run is not told to attach: %q", stalled)
	}
	if strings.Contains(strings.ToLower(moving), "attach to carry it on") {
		t.Fatalf("a run already being advanced is told to attach to carry it on: %q", moving)
	}
}

// standingAt is a run standing at a checkpoint with nothing advancing it,
// which is the standing every case above is about.
func standingAt(record store.RunStatus, status graph.Status, state graph.State) machine.Standing {
	return machine.Standing{Record: record, Execution: machine.ExecutionAt(status, state)}
}

// A run whose record says it may still move and whose checkpoint does not
// exist yet has not failed, in either of the two runs that describes.
//
// The window is real and narrow: internal/service records a run as started and
// then claims the slot its segment advances in before internal/graph writes
// the first checkpoint, so a read landing there finds a record saying running
// and no position at all. A service that died in the same window leaves the
// identical shape from a process that runs no more code. Both were reported as
// failed, because "no checkpoint" was handed to the translation as
// graph.StatusInvalid and landed on the arm for a checkpoint this build cannot
// read.
//
// The two are not the same answer, and the fact that separates them is the one
// that separates a run in flight from a stalled one everywhere else. Neither
// is told to wait, because a run with no position cannot be attached to and
// carried on.
func TestARunWithNoRecordedPositionHasNotFailed(t *testing.T) {
	t.Parallel()
	for _, record := range []store.RunStatus{store.RunPending, store.RunRunning, store.RunHeld} {
		for _, advancing := range []bool{true, false} {
			standing := machine.Standing{
				Record:    record,
				Execution: machine.ExecutionUnrecorded(),
				Advancing: advancing,
			}
			got := machine.OutcomeOf(standing)
			if got == machine.OutcomeFailed {
				t.Fatalf("a %s run with no recorded position and advancing=%v reports failed", record, advancing)
			}
			if got != machine.OutcomeExecuting {
				t.Fatalf("a %s run with no recorded position reports %s, want executing", record, got)
			}
			if got.Terminal() {
				t.Fatalf("%s reports that a run nothing has finished with has ended", got)
			}
			action := machine.NextActionOf(standing)
			if strings.TrimSpace(action) == "" {
				t.Fatalf("a %s run with no recorded position says nothing about what to do", record)
			}
			if advancing && action != machine.OutcomeExecuting.NextActionFor(true) {
				t.Fatalf("a run a segment is inside is told %q, want the action for one being advanced", action)
			}
			if !advancing && action == machine.OutcomeExecuting.NextActionFor(false) {
				t.Fatalf("a run with no position is told to attach and carry it on: %q", action)
			}
		}
	}
}

// No standing produces an answer that tells a reader to wait on a run nothing
// is advancing, or to carry on one something already is.
//
// This is the class rather than one direction of it. Three directions of the
// same disagreement have been found in this surface - a stalled run rendered
// as working, a cancelled run still executing, and a run being advanced
// rendered as failed - and each was an answer that read some of the facts and
// dropped the one that resolved them. The sweep is over every record status
// this build defines, every place execution can stand including nowhere, and
// both answers to whether anything is advancing the run, so a branch that
// decided from two of the three fails here rather than in a fourth direction.
//
// A sweep is only worth what it swept, so this one is held to a positive
// control. A sweep that walked nothing, or that only ever reached standings
// with one answer between them, would pass every assertion below without
// checking anything, and its green would be indistinguishable from its being
// broken. The control names the three answers OutcomeExecuting has and the
// count of standings that must be reached, and it fails when any of them is
// missing.
func TestNoAnswerContradictsWhatIsAdvancingTheRun(t *testing.T) {
	t.Parallel()
	executions := []machine.Execution{machine.ExecutionUnrecorded()}
	for _, status := range []graph.Status{
		graph.StatusInvalid, graph.StatusRunning, graph.StatusCompleted, graph.StatusHalted,
		graph.StatusRoundsExhausted, graph.StatusBudgetExhausted, graph.StatusConverged,
	} {
		executions = append(executions, machine.ExecutionAt(status, graph.State{}))
	}
	records := append(store.RunStatuses(), store.RunStatus("invented"))
	swept := 0
	actions := map[string]int{}
	for _, record := range records {
		for _, execution := range executions {
			for _, advancing := range []bool{true, false} {
				standing := machine.Standing{Record: record, Execution: execution, Advancing: advancing}
				answer := machine.Run{}.Decide(standing)
				swept++
				actions[answer.NextAction()]++
				if strings.TrimSpace(answer.NextAction()) == "" {
					t.Fatalf("a %s run, recorded position %v, advancing=%v says nothing about what to do",
						record, execution.Recorded(), advancing)
				}
				if answer.Advancing != advancing {
					t.Fatalf("the answer reports advancing=%v for a run standing advancing=%v",
						answer.Advancing, advancing)
				}
				if advancing && answer.NextAction() != answer.Outcome.NextActionFor(true) {
					t.Fatalf("a %s run something is advancing is told %q, want %q",
						record, answer.NextAction(), answer.Outcome.NextActionFor(true))
				}
				if !advancing && answer.NextAction() == machine.OutcomeExecuting.NextActionFor(true) {
					t.Fatalf("a %s run nothing is advancing is told to wait: %q", record, answer.NextAction())
				}
			}
		}
	}

	// The positive control. Every standing the loops describe was reached, and
	// the three answers an executing run has were each produced by one of them:
	// wait, attach and carry it on, and end it because there is nothing to
	// carry on from. A sweep that reached fewer would be asserting over a
	// domain narrower than the one it claims.
	if want := len(records) * len(executions) * 2; swept != want {
		t.Fatalf("the sweep reached %d standings, want the %d the domain has", swept, want)
	}
	unrecordedAndStill := machine.NextActionOf(machine.Standing{
		Record:    store.RunRunning,
		Execution: machine.ExecutionUnrecorded(),
	})
	for _, want := range []string{
		machine.OutcomeExecuting.NextActionFor(true),
		machine.OutcomeExecuting.NextActionFor(false),
		unrecordedAndStill,
	} {
		if actions[want] == 0 {
			t.Fatalf("no standing in the sweep produced %q, so nothing here checked it", want)
		}
	}
	if unrecordedAndStill == machine.OutcomeExecuting.NextActionFor(false) {
		t.Fatal("a run with no position and a run standing at one are told the same thing")
	}
}

// A run's answer carries the most agent-written text of any shape here, and it
// travels with angle brackets and ampersands as they were written.
//
// machine.Encoder turns HTML escaping off and says why: those three characters
// are not control characters, and a reader who has to undo the escaping to
// read a diff or a finding is worse off. That setting cannot reach inside a
// Marshaler, which writes its own bytes, so Run has to make the same choice
// again - and did not, which is what this pins. The escaping of control
// characters is a separate pass over the finished document and is unaffected.
func TestARunTravelsWithItsTextUnescaped(t *testing.T) {
	t.Parallel()
	const written = "use <T> & fix a>b"
	answer := machine.Run{
		Record: store.Run{ID: "abc", Intent: written},
		Stages: []machine.Stage{{
			Stage: "review",
			Report: &findings.Report{Summary: written, Findings: []findings.Finding{
				{ID: "one", Action: findings.ActionNote, Description: written},
			}},
		}},
	}.Decide(machine.Standing{
		Record:    store.RunRunning,
		Execution: machine.ExecutionAt(graph.StatusRunning, graph.State{}),
		Advancing: true,
	})

	var out bytes.Buffer
	if err := machine.NewEncoder(&out).Encode(answer); err != nil {
		t.Fatalf("encoding: %v", err)
	}
	line := out.String()
	for _, escaped := range []string{`\u003c`, `\u003e`, `\u0026`} {
		if strings.Contains(line, escaped) {
			t.Fatalf("the answer rewrites %s into an escape a reader has to undo:\n%s", escaped, line)
		}
	}
	if strings.Count(line, written) < 3 {
		t.Fatalf("the answer does not carry the text as it was written:\n%s", line)
	}

	// It is still one document a consumer can decode back to what was said.
	var back machine.Run
	if err := json.Unmarshal([]byte(line), &back); err != nil {
		t.Fatalf("decoding what was written: %v\n%s", err, line)
	}
	if back.Record.Intent != written {
		t.Fatalf("the round trip changed the intent: %q", back.Record.Intent)
	}
	if back.NextAction() != answer.NextAction() {
		t.Fatalf("the round trip answered %q, want %q", back.NextAction(), answer.NextAction())
	}
}

// An answer decoded from the wire carries the next action the answering
// service decided, which is what keeps the field being unexported from
// changing what a caller reads.
func TestTheNextActionSurvivesTheWire(t *testing.T) {
	t.Parallel()
	answer := machine.Run{Record: store.Run{ID: "abc"}}.Decide(machine.Standing{
		Record:    store.RunRunning,
		Execution: machine.ExecutionAt(graph.StatusRunning, graph.State{}),
		Advancing: true,
	})
	encoded, err := json.Marshal(answer)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	var carried map[string]any
	if err := json.Unmarshal(encoded, &carried); err != nil {
		t.Fatalf("reading the document: %v", err)
	}
	if carried["next_action"] != answer.NextAction() {
		t.Fatalf("the document carries next_action %v, want %q", carried["next_action"], answer.NextAction())
	}
	var back machine.Run
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if back.NextAction() != answer.NextAction() {
		t.Fatalf("the round trip answered %q, want %q", back.NextAction(), answer.NextAction())
	}
	if back.Outcome != answer.Outcome || back.Advancing != answer.Advancing {
		t.Fatalf("the round trip changed the answer: %s advancing=%v", back.Outcome, back.Advancing)
	}
}
