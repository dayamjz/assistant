package standin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
)

// waitWrote polls a stream-mode worker's output until it carries the Wrote
// of path.
func waitWrote(t *testing.T, w agents.Worker, path string) Wrote {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		for _, line := range strings.Split(w.Capture().Text, "\n") {
			var turn struct {
				Type   string `json:"type"`
				Result string `json:"result"`
			}
			if json.Unmarshal([]byte(line), &turn) != nil || turn.Type != "result" {
				continue
			}
			var wrote Wrote
			if json.Unmarshal([]byte(turn.Result), &wrote) == nil && wrote.Path == path {
				return wrote
			}
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the worker never answered a write of %q; its output is:\n%s", path, w.Capture().Text)
		}
		time.Sleep(time.Millisecond)
	}
}

// A Write exchange writes the file its line names inside the worker's own
// directory, with the content after the path's line exactly as sent, and
// refuses a path outside that directory without writing it, which #267 saw
// the Claude Code CLI ask an approval for. A script a Write exchange cannot
// answer as written is refused when the Agent is built.
func TestAWriteExchangeWritesOnlyInsideTheWorkersDirectory(t *testing.T) {
	t.Parallel()
	agent := New(t, Script{Worker: &WorkerScript{Exchanges: []Exchange{{Contains: "write: ", Write: true}}}},
		agents.WithTerminationGrace(300*time.Millisecond))
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.json")
	worker, err := agents.OpenWorker(t.Context(), agent.Runner(), agents.WorkerSpec{Dir: dir, Instructions: "work"})
	if err != nil {
		t.Fatalf("starting the worker: %v", err)
	}
	defer worker.Close()

	const content = "{\"summary\": \"x\"}\nsecond line"
	if err := worker.Send(t.Context(), "write: report.json\n"+content); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if wrote := waitWrote(t, worker, "report.json"); wrote.Refused != "" {
		t.Fatalf("a write inside the directory answered %+v", wrote)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "report.json")); err != nil || string(got) != content {
		t.Fatalf("the written file reads %q (%v), want %q", got, err, content)
	}
	for _, path := range []string{outside, "../escaped.json"} {
		if err := worker.Send(t.Context(), "write: "+path+"\n"+content); err != nil {
			t.Fatalf("Send: %v", err)
		}
		if wrote := waitWrote(t, worker, path); wrote.Refused == "" {
			t.Errorf("a write to %q was not refused", path)
		}
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Errorf("the refused write left %s: %v", outside, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escaped.json")); !os.IsNotExist(err) {
		t.Errorf("the refused relative write left a file outside the directory: %v", err)
	}

	for _, bad := range []Exchange{{Write: true}, {Contains: "write: ", Write: true, Reply: "done"}, {Contains: "x", Write: true, Run: true}} {
		fake := &fatalTB{TB: t}
		run(func() { New(fake, Script{Worker: &WorkerScript{Exchanges: []Exchange{bad}}}) })
		if !fake.failed {
			t.Errorf("a script with the exchange %+v was built", bad)
		}
	}
}
