package agents

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

// The interrupt fixture is the request and follow-on notifications Codex
// CLI 0.166.0's generated app-server schema names for turn/interrupt
// (TurnInterruptParams with threadId and turnId; TurnStatus interrupted;
// then idle). It is not a live stdio capture: the ids are taken from the
// 0.163.0 happy-path recording so a reader can join the two, and the
// method names and status are what generate-json-schema emitted for
// 0.166.0. Playing frames through receive settles the turn on
// turn/completed interrupted alone: idle may follow, but Interrupt must
// not depend on it (the same no-idle shape failed turns already use).
func TestSchemaCodexTurnInterruptFramesSettleTheWorker(t *testing.T) {
	raw, err := os.ReadFile("testdata/codex-app-server-turn-interrupt-0.166.0.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	settled := make(chan struct{})
	w := &codexWorker{
		procWorker: &procWorker{out: &tailWriter{limit: 1 << 20}, availability: &workerAvailability{}},
		settled:    settled,
		turn:       "01a14d4d-cbd4-7262-95c0-856c2dab1db0",
		thread:     "01a14d4d-cb48-7a02-be16-3b3737212b9e",
	}
	var sawInterrupt, sawInterrupted bool
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var message codexMessage
		if json.Unmarshal([]byte(line), &message) != nil {
			t.Fatalf("fixture line does not decode: %s", line)
		}
		if message.Method == "turn/interrupt" {
			sawInterrupt = true
			var params struct {
				Thread string `json:"threadId"`
				Turn   string `json:"turnId"`
			}
			if json.Unmarshal(message.Params, &params) != nil || params.Thread == "" || params.Turn == "" {
				t.Fatalf("turn/interrupt params are %s, want threadId and turnId", message.Params)
			}
			continue
		}
		if message.Method == "thread/status/changed" {
			break // prove settle without the trailing idle
		}
		if message.Method == "turn/completed" {
			var completed struct {
				Turn struct {
					Status string `json:"status"`
				} `json:"turn"`
			}
			if json.Unmarshal(message.Params, &completed) != nil || completed.Turn.Status != "interrupted" {
				t.Fatalf("turn/completed is %s, want status interrupted", message.Params)
			}
			sawInterrupted = true
		}
		w.receive([]byte(line))
	}
	if !sawInterrupt || !sawInterrupted {
		t.Fatalf("fixture missing interrupt request (%t) or interrupted status (%t)", sawInterrupt, sawInterrupted)
	}
	select {
	case <-settled:
	default:
		t.Fatal("turn/completed interrupted did not finish the turn without idle")
	}
	if w.turn != "" {
		t.Fatalf("after interrupted the worker still names turn %q", w.turn)
	}
}

// The lines in testdata are the Codex CLI 0.163.0's app-server frames,
// recorded from a live stdio session that answered "ready". The home path
// in those frames is replaced; the method names, the thread id's place,
// the in-progress turn result, the agentMessage item, and the idle status
// are the bytes the CLI wrote.
func TestRecordedCodexAppServerFramesDriveTheWorker(t *testing.T) {
	raw, err := os.ReadFile("testdata/codex-app-server-0.163.0.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	settled := make(chan struct{})
	w := &codexWorker{
		procWorker: &procWorker{out: &tailWriter{limit: 1 << 20}, availability: &workerAvailability{}},
		settled:    settled,
	}
	var thread string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		w.receive([]byte(line))
		var message codexMessage
		if json.Unmarshal([]byte(line), &message) != nil || message.Method != "" {
			continue
		}
		var created struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		if json.Unmarshal(message.Result, &created) == nil && created.Thread.ID != "" {
			thread = created.Thread.ID
		}
	}
	if thread == "" {
		t.Fatal("the recorded thread/start result carried no thread id")
	}
	transcript := ReadTranscript(w.Capture().Text, OutputStreamJSON, false)
	var saw bool
	for _, event := range transcript.Events {
		if event.Kind == TranscriptUnrecognized {
			t.Fatalf("the recorded reply was unrecognized: %+v", event)
		}
		if event.Kind == TranscriptText && event.Text == "ready" {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("the recorded agent message captured %q, want assistant text ready", w.Capture().Text)
	}
	select {
	case <-settled:
	default:
		t.Fatal("the recorded idle status did not finish the turn")
	}
}

// An idle that arrives as the next Send checks the outstanding turn must
// finish that turn and no later one. The two run together so the check and
// the close meet in either order.
func TestAnIdleCannotFinishALaterTurn(t *testing.T) {
	w := &codexWorker{procWorker: &procWorker{done: make(chan struct{})}}
	for range 100 {
		w.mu.Lock()
		w.settled = make(chan struct{})
		w.mu.Unlock()
		go w.markIdle()
		if err := w.finishTurn(context.Background()); err != nil {
			t.Fatal(err)
		}
		w.mu.Lock()
		w.settled = make(chan struct{})
		fresh := w.settled
		w.mu.Unlock()
		select {
		case <-fresh:
			t.Fatal("an earlier idle closed the next turn's channel")
		default:
		}
		w.markIdle()
	}
}

// codexTurnFailure reads the structured field before any text. The
// recorded failed turn/completed of both credential recordings classifies
// as a credential; the same frame with its status changed to 500 does not,
// even though its message still names a status, and with the field removed
// the message alone is read through the marker table, which takes the
// recorded 401 line. The schema's "unauthorized" string is a credential
// too, and an unrecognized string with an unrecognized message is nil.
func TestCodexTurnFailureReadsTheStructuredFieldFirst(t *testing.T) {
	for _, name := range []string{"codex-app-server-unauthenticated-0.163.0.jsonl", "codex-app-server-rejected-0.163.0.jsonl"} {
		raw, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
		var completed struct {
			Params struct {
				Turn struct {
					Status string `json:"status"`
					Error  struct {
						Message string          `json:"message"`
						Info    json.RawMessage `json:"codexErrorInfo"`
					} `json:"error"`
				} `json:"turn"`
			} `json:"params"`
		}
		if err := json.Unmarshal([]byte(lines[len(lines)-1]), &completed); err != nil || completed.Params.Turn.Status != "failed" {
			t.Fatalf("%s does not end in a failed turn/completed: %v", name, err)
		}
		e := completed.Params.Turn.Error
		got := codexTurnFailure(e.Info, e.Message)
		if got == nil || got.Failure != FailureUnavailable || !errors.Is(got, ErrCredentialRefused) {
			t.Fatalf("%s: the recorded failure classified as %+v", name, got)
		}
		server := json.RawMessage(strings.ReplaceAll(string(e.Info), "401", "500"))
		if got := codexTurnFailure(server, "unexpected status 500 Internal Server Error"); got != nil {
			t.Fatalf("%s: a 500 classified as %+v", name, got)
		}
		if got := codexTurnFailure(nil, e.Message); got == nil || !errors.Is(got, ErrCredentialRefused) {
			t.Fatalf("%s: the message alone classified as %+v", name, got)
		}
	}
	if got := codexTurnFailure(json.RawMessage(`"unauthorized"`), "denied"); got == nil || !errors.Is(got, ErrCredentialRefused) {
		t.Fatalf("the unauthorized string classified as %+v", got)
	}
	if got := codexTurnFailure(json.RawMessage(`"contextWindowExceeded"`), "context window exceeded"); got != nil {
		t.Fatalf("an unrecognized failure classified as %+v", got)
	}
}
