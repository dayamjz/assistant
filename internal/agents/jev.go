package agents

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// JevKeyVariable is what the pinned fast-jev-compaction plugin reads as its
// TypeSafe key. Emptiness only is tested; the value is never recorded.
const JevKeyVariable = "TYPESAFE_API_KEY"

// JevPluginVersion pins the embedded plugin (jevmod/.claude-plugin/plugin.json).
const JevPluginVersion = "0.3.0"

// JevMinClaudeVersion is the Claude Code release that added function hooks.
const JevMinClaudeVersion = "2.1.274"

// JevFunctionHooksEnv opts Claude Code into function hooks via settings.json.
const JevFunctionHooksEnv = "CLAUDE_CODE_ENABLE_FUNCTION_HOOKS"

// CompactionPath is which compaction a launch was prepared for.
type CompactionPath string

const (
	// CompactionJev means the pinned plugin was installed for this launch.
	CompactionJev CompactionPath = "jev"
	// CompactionBuiltin means Claude Code's own compaction runs.
	CompactionBuiltin CompactionPath = "builtin"
)

// CompactionDecision is the path record InstallJev writes into a config dir.
type CompactionDecision struct {
	Path   CompactionPath `json:"path"`
	Reason string         `json:"reason,omitempty"`
	Plugin string         `json:"plugin,omitempty"`
}

const (
	compactionRecordName = "compaction.json"
	jevPluginDirName     = "plugins/fast-jev-compaction"
)

// DecideJev chooses the compaction path. It never refuses a launch: a missed
// precondition is CompactionBuiltin with a reason.
func DecideJev(optIn bool, env []string, version string) CompactionDecision {
	switch {
	case !optIn:
		return CompactionDecision{Path: CompactionBuiltin, Reason: "opt-in off"}
	case lastValue(env, JevKeyVariable) == "":
		return CompactionDecision{Path: CompactionBuiltin, Reason: "no " + JevKeyVariable + " in the environment"}
	case version == "":
		return CompactionDecision{Path: CompactionBuiltin, Reason: "claude version unknown"}
	case !claudeVersionAtLeast(version, JevMinClaudeVersion):
		return CompactionDecision{Path: CompactionBuiltin, Reason: "claude " + version + " is older than " + JevMinClaudeVersion}
	default:
		return CompactionDecision{Path: CompactionJev, Plugin: JevPluginVersion}
	}
}

// InstallJev writes the path record into configDir and, for CompactionJev, the
// plugin and a function-hooks setting. PluginArgs is nil or ["--plugin-dir", abs].
// An install I/O failure falls back to builtin with a reason and a nil error:
// compaction never refuses a launch, and a CompactionJev Path is never returned
// alongside an error. configDir must already exist.
func InstallJev(configDir string, d CompactionDecision) (CompactionDecision, []string, error) {
	fallback := func(reason string) (CompactionDecision, []string, error) {
		out := CompactionDecision{Path: CompactionBuiltin, Reason: reason}
		_ = writeCompactionDecision(configDir, out)
		return out, nil, nil
	}
	if d.Path != CompactionJev {
		if err := writeCompactionDecision(configDir, d); err != nil {
			reason := d.Reason
			if reason == "" {
				reason = "record write failed: " + err.Error()
			} else {
				reason = reason + "; record write failed: " + err.Error()
			}
			return CompactionDecision{Path: CompactionBuiltin, Reason: reason}, nil, nil
		}
		return d, nil, nil
	}
	pluginDir := filepath.Join(configDir, filepath.FromSlash(jevPluginDirName))
	// Only this plugin tree is ours; sibling plugins in a reused config dir stay.
	undoPlugin := func() { _ = os.RemoveAll(pluginDir) }
	if err := materializeJevPlugin(pluginDir); err != nil {
		undoPlugin()
		return fallback("plugin install failed: " + err.Error())
	}
	if err := writeJevSettings(configDir); err != nil {
		undoPlugin()
		return fallback("settings install failed: " + err.Error())
	}
	// After settings are merged, a later failure must drop both the plugin and
	// the function-hooks env key so Path=builtin is not paired with hooks on.
	undo := func() {
		undoPlugin()
		clearJevFunctionHooks(configDir)
	}
	if err := writeCompactionDecision(configDir, d); err != nil {
		undo()
		return fallback("record write failed: " + err.Error())
	}
	abs, err := filepath.Abs(pluginDir)
	if err != nil {
		undo()
		return fallback("plugin path: " + err.Error())
	}
	return d, []string{"--plugin-dir", abs}, nil
}

// ReadCompactionDecision reads the record InstallJev wrote into configDir.
func ReadCompactionDecision(configDir string) (CompactionDecision, error) {
	raw, err := os.ReadFile(filepath.Join(configDir, compactionRecordName))
	if err != nil {
		return CompactionDecision{}, err
	}
	var d CompactionDecision
	if err := json.Unmarshal(raw, &d); err != nil {
		return CompactionDecision{}, fmt.Errorf("reading compaction decision: %w", err)
	}
	return d, nil
}

func writeCompactionDecision(configDir string, d CompactionDecision) error {
	raw, err := json.Marshal(d)
	if err != nil {
		return fmt.Errorf("encoding compaction decision: %w", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, compactionRecordName), append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing compaction decision: %w", err)
	}
	return nil
}

// writeJevSettings merges the function-hooks env into settings.json, keeping
// any other keys already present in the config dir.
func writeJevSettings(configDir string) error {
	path := filepath.Join(configDir, "settings.json")
	settings, err := readSettingsJSON(path)
	if err != nil {
		return fmt.Errorf("reading jev settings: %w", err)
	}
	env, _ := settings["env"].(map[string]any)
	if env == nil {
		env = map[string]any{}
	}
	env[JevFunctionHooksEnv] = "1"
	settings["env"] = env
	return writeSettingsJSON(path, settings)
}

// clearJevFunctionHooks removes the function-hooks env key InstallJev added,
// leaving every other settings entry in place. Best-effort on a fall-closed path.
func clearJevFunctionHooks(configDir string) {
	path := filepath.Join(configDir, "settings.json")
	settings, err := readSettingsJSON(path)
	if err != nil {
		return
	}
	env, _ := settings["env"].(map[string]any)
	if env == nil {
		return
	}
	delete(env, JevFunctionHooksEnv)
	if len(env) == 0 {
		delete(settings, "env")
	} else {
		settings["env"] = env
	}
	if len(settings) == 0 {
		_ = os.Remove(path)
		return
	}
	_ = writeSettingsJSON(path, settings)
}

func readSettingsJSON(path string) (map[string]any, error) {
	settings := map[string]any{}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return settings, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		return nil, err
	}
	return settings, nil
}

func writeSettingsJSON(path string, settings map[string]any) error {
	raw, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("encoding jev settings: %w", err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing jev settings: %w", err)
	}
	return nil
}

// materializeJevPlugin replaces dest with a fresh copy of the embedded plugin
// so a partial tree or an older pin cannot survive under a new record.
func materializeJevPlugin(dest string) error {
	if err := os.RemoveAll(dest); err != nil {
		return fmt.Errorf("clearing plugin directory: %w", err)
	}
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return fmt.Errorf("making plugin directory: %w", err)
	}
	const root = "jevmod"
	return fs.WalkDir(jevPluginFS, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil || rel == "." {
			return relErr
		}
		target := filepath.Join(dest, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		data, readErr := jevPluginFS.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
}

func claudeVersionAtLeast(version, min string) bool {
	v, m := versionParts(version), versionParts(min)
	n := len(v)
	if len(m) > n {
		n = len(m)
	}
	for i := 0; i < n; i++ {
		var a, b int
		if i < len(v) {
			a = v[i]
		}
		if i < len(m) {
			b = m[i]
		}
		if a != b {
			return a > b
		}
	}
	return true
}

func versionParts(s string) []int {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		s = s[:i]
	}
	var out []int
	for _, f := range strings.Split(s, ".") {
		n, err := strconv.Atoi(f)
		if err != nil {
			break
		}
		out = append(out, n)
	}
	return out
}
