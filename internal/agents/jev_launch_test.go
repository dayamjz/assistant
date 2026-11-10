package agents_test

import (
	"os"
	"path/filepath"
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

func TestATerminalWorkerWithJevOptInInstallsThePlugin(t *testing.T) {
	agents.SetJevVersionForTest(agents.JevMinClaudeVersion)
	t.Cleanup(func() { agents.SetJevVersionForTest("") })
	worker, term, agent := openClaudeTerminal(t, standin.Script{}, workerSpec(t),
		agents.WithJevCompaction(func() bool { return true }),
		agents.WithBaseEnvironment(append(claudeTerminalEnv(t), agents.JevKeyVariable+"=test-key-not-printed")),
	)
	defer worker.Close()
	config := agents.ClaudeConfigFor(term)
	got, err := agents.ReadCompactionDecision(config)
	if err != nil {
		t.Fatalf("ReadCompactionDecision: %v", err)
	}
	if got.Path != agents.CompactionJev || got.Plugin != agents.JevPluginVersion {
		t.Fatalf("got %+v, want jev %s", got, agents.JevPluginVersion)
	}
	plugin := filepath.Join(config, "plugins", "fast-jev-compaction", ".claude-plugin", "plugin.json")
	if _, err := os.Stat(plugin); err != nil {
		t.Fatalf("plugin missing under config dir: %v", err)
	}
	call := awaitInteractive(t, agent, answered(0))
	dash := -1
	pluginDir := false
	for i, a := range call.Args {
		if a == "--" {
			dash = i
		}
		if a == "--plugin-dir" {
			pluginDir = true
			if dash >= 0 {
				t.Fatalf("--plugin-dir after -- in args %v", call.Args)
			}
		}
	}
	if !pluginDir || dash < 0 {
		t.Fatalf("args %v, want --plugin-dir before --", call.Args)
	}
}
