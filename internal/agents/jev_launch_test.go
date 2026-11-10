package agents_test

import (
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
)

// A terminal worker always writes a compaction path record into its config
// dir. With opt-in off the path is builtin; with opt-in on it is jev when the
// CLI and key allow, otherwise builtin with a reason. The launch is never refused.
func TestATerminalWorkerRecordsBuiltinWhenJevOptInIsOff(t *testing.T) {
	worker, term, _ := openClaudeTerminal(t, standin.Script{}, workerSpec(t))
	defer worker.Close()
	got, err := agents.ReadCompactionDecision(agents.ClaudeConfigFor(term))
	if err != nil {
		t.Fatalf("ReadCompactionDecision: %v", err)
	}
	if got.Path != agents.CompactionBuiltin || got.Reason != "opt-in off" {
		t.Fatalf("got %+v", got)
	}
}

func TestATerminalWorkerWithJevOptInRecordsACompactionPath(t *testing.T) {
	worker, term, _ := openClaudeTerminal(t, standin.Script{}, workerSpec(t),
		agents.WithJevCompaction(func() bool { return true }),
		agents.WithBaseEnvironment(append(claudeTerminalEnv(t), agents.JevKeyVariable+"=test-key-not-printed")),
	)
	defer worker.Close()
	got, err := agents.ReadCompactionDecision(agents.ClaudeConfigFor(term))
	if err != nil {
		t.Fatalf("ReadCompactionDecision: %v", err)
	}
	if got.Path != agents.CompactionBuiltin && got.Path != agents.CompactionJev {
		t.Fatalf("path = %q reason %q", got.Path, got.Reason)
	}
}
