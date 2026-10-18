package agents

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

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
	w := &codexWorker{
		procWorker: &procWorker{out: &tailWriter{limit: 1 << 20}, availability: &workerAvailability{}},
		idle:       make(chan struct{}, 1),
	}
	w.awaitingIdle = true
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
	if got := w.Capture().Text; got != "ready\n" {
		t.Fatalf("the recorded agent message captured %q, want %q", got, "ready\n")
	}
	select {
	case <-w.idle:
	default:
		t.Fatal("the recorded idle status did not finish the turn")
	}
}
