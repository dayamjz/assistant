package agents

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The interrupt fixture is the stream-json control_request, control_response,
// and interrupted result Claude Code's CLI emits for a pipe cancel. Playing
// the result through observe settles an armed turn, so the stand-in and
// adapter cannot drift from that claimed shape with every test still green.
func TestSchemaClaudeStreamInterruptFramesSettleTheWorker(t *testing.T) {
	raw, err := os.ReadFile("testdata/claude-stream-interrupt-control-request.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	w := &claudePipeWorker{}
	w.armTurn()
	settled := w.settled
	close(w.begun) // interrupt waits on init; fixture starts after it
	var sawRequest, sawResult bool
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var msg struct {
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
			Request   struct {
				Subtype string `json:"subtype"`
			} `json:"request"`
			Result string `json:"result"`
		}
		if json.Unmarshal([]byte(line), &msg) != nil {
			t.Fatalf("fixture line does not decode: %s", line)
		}
		switch msg.Type {
		case "control_request":
			if msg.Request.Subtype != "interrupt" || msg.RequestID == "" {
				t.Fatalf("control_request is %s, want subtype interrupt with request_id", line)
			}
			sawRequest = true
		case "result":
			if msg.Result == "" {
				t.Fatalf("result line has empty result: %s", line)
			}
			sawResult = true
			w.observe([]byte(line))
		}
	}
	if !sawRequest || !sawResult {
		t.Fatalf("fixture missing control_request (%t) or result (%t)", sawRequest, sawResult)
	}
	select {
	case <-settled:
	default:
		t.Fatal("interrupted result did not finish the turn")
	}
}
