package agents_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
)

func TestAttachJevScratchFallClosedLeavesNoRemap(t *testing.T) {
	agent := standin.New(t, standin.Script{},
		agents.WithJevCompaction(func() bool { return true }),
		agents.WithBaseEnvironment(append(claudeTerminalEnv(t), agents.JevKeyVariable+"=")),
	)
	env := append(claudeTerminalEnv(t), agents.JevKeyVariable+"=")
	dir, args, cleanup := agents.AttachJevScratchForTest(agent.Runner(), t.TempDir(), env)
	if dir != "" || args != nil || cleanup != nil {
		t.Fatalf("fall-closed attach got dir=%q args=%v cleanup=%v, want none", dir, args, cleanup != nil)
	}
}

func TestAttachJevScratchRemapsOnlyWhenJevInstalls(t *testing.T) {
	agents.SetJevVersionForTest(agents.JevMinClaudeVersion)
	t.Cleanup(func() { agents.SetJevVersionForTest("") })
	env := append(claudeTerminalEnv(t), agents.JevKeyVariable+"=test-key-not-printed")
	agent := standin.New(t, standin.Script{},
		agents.WithJevCompaction(func() bool { return true }),
		agents.WithBaseEnvironment(env),
	)
	workspace := t.TempDir()
	dir, args, cleanup := agents.AttachJevScratchForTest(agent.Runner(), workspace, env)
	if cleanup == nil {
		t.Fatal("expected cleanup for a selected Jev scratch")
	}
	defer cleanup()
	if dir == "" || len(args) != 2 || args[0] != "--plugin-dir" {
		t.Fatalf("got dir=%q args=%v, want scratch and --plugin-dir", dir, args)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude.json")); err != nil {
		t.Fatalf("scratch was not seeded: %v", err)
	}
	got, err := agents.ReadCompactionDecision(dir)
	if err != nil || got.Path != agents.CompactionJev {
		t.Fatalf("decision %+v err %v, want jev", got, err)
	}
}

func TestAttachJevScratchDropsDirWhenInstallFallsClosed(t *testing.T) {
	agents.SetJevVersionForTest(agents.JevMinClaudeVersion)
	t.Cleanup(func() { agents.SetJevVersionForTest("") })
	agents.SetSettingsWriteForTest(func(path string, data []byte) error {
		if err := os.WriteFile(path, []byte("{truncated"), 0o600); err != nil {
			return err
		}
		return os.ErrInvalid
	})
	t.Cleanup(func() { agents.SetSettingsWriteForTest(nil) })
	env := append(claudeTerminalEnv(t), agents.JevKeyVariable+"=test-key-not-printed")
	agent := standin.New(t, standin.Script{},
		agents.WithJevCompaction(func() bool { return true }),
		agents.WithBaseEnvironment(env),
	)
	dir, args, cleanup := agents.AttachJevScratchForTest(agent.Runner(), t.TempDir(), env)
	if dir != "" || args != nil || cleanup != nil {
		t.Fatalf("InstallJev fall-closed attach got dir=%q args=%v cleanup=%v, want none", dir, args, cleanup != nil)
	}
}

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
