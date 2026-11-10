package agents_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
)

func TestDecideJevFallsClosed(t *testing.T) {
	key := agents.JevKeyVariable + "=test-key-not-printed"
	cases := []struct {
		name    string
		optIn   bool
		env     []string
		version string
		path    agents.CompactionPath
		reason  string
	}{
		{"opt-in off", false, []string{key}, agents.JevMinClaudeVersion, agents.CompactionBuiltin, "opt-in off"},
		{"no key", true, nil, agents.JevMinClaudeVersion, agents.CompactionBuiltin, "no " + agents.JevKeyVariable},
		{"empty key", true, []string{agents.JevKeyVariable + "="}, agents.JevMinClaudeVersion, agents.CompactionBuiltin, "no " + agents.JevKeyVariable},
		{"unknown version", true, []string{key}, "", agents.CompactionBuiltin, "claude version unknown"},
		{"old CLI", true, []string{key}, "2.1.273", agents.CompactionBuiltin, "older than " + agents.JevMinClaudeVersion},
		{"ok", true, []string{key}, agents.JevMinClaudeVersion, agents.CompactionJev, ""},
		{"newer CLI", true, []string{key}, "2.1.308", agents.CompactionJev, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := agents.DecideJev(tc.optIn, tc.env, tc.version)
			if got.Path != tc.path {
				t.Fatalf("path = %q, want %q (reason %q)", got.Path, tc.path, got.Reason)
			}
			if tc.reason == "" {
				if got.Reason != "" || got.Plugin != agents.JevPluginVersion {
					t.Fatalf("jev decision = %+v, want plugin %s and empty reason", got, agents.JevPluginVersion)
				}
				return
			}
			if !strings.Contains(got.Reason, tc.reason) {
				t.Fatalf("reason %q, want it to name %q", got.Reason, tc.reason)
			}
		})
	}
}

func TestInstallJevWritesPluginAndRecord(t *testing.T) {
	dir := t.TempDir()
	d := agents.DecideJev(true, []string{agents.JevKeyVariable + "=k"}, agents.JevMinClaudeVersion)
	got, args, err := agents.InstallJev(dir, d)
	if err != nil {
		t.Fatalf("InstallJev: %v", err)
	}
	if got.Path != agents.CompactionJev || len(args) != 2 || args[0] != "--plugin-dir" {
		t.Fatalf("got %+v args %v, want jev with --plugin-dir", got, args)
	}
	for _, rel := range []string{
		"hooks/fast-jev.ts",
		"src/compact.ts",
		"src/request.ts",
		".claude-plugin/plugin.json",
		"package.json",
	} {
		if _, err := os.Stat(filepath.Join(args[1], filepath.FromSlash(rel))); err != nil {
			t.Fatalf("plugin %s missing under %s: %v", rel, args[1], err)
		}
	}
	pluginMeta, err := os.ReadFile(filepath.Join(args[1], ".claude-plugin", "plugin.json"))
	if err != nil {
		t.Fatalf("plugin.json: %v", err)
	}
	var plugin struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(pluginMeta, &plugin); err != nil {
		t.Fatalf("plugin.json: %v", err)
	}
	if plugin.Version != agents.JevPluginVersion {
		t.Fatalf("plugin.json version = %q, want %s", plugin.Version, agents.JevPluginVersion)
	}
	pkgMeta, err := os.ReadFile(filepath.Join(args[1], "package.json"))
	if err != nil {
		t.Fatalf("package.json: %v", err)
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(pkgMeta, &pkg); err != nil {
		t.Fatalf("package.json: %v", err)
	}
	if pkg.Version != agents.JevPluginVersion {
		t.Fatalf("package.json version = %q, want %s", pkg.Version, agents.JevPluginVersion)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatalf("settings.json: %v", err)
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("settings.json: %v", err)
	}
	env, _ := settings["env"].(map[string]any)
	if env[agents.JevFunctionHooksEnv] != "1" {
		t.Fatalf("settings env = %#v, want %s=1", env, agents.JevFunctionHooksEnv)
	}
	recorded, err := agents.ReadCompactionDecision(dir)
	if err != nil {
		t.Fatalf("ReadCompactionDecision: %v", err)
	}
	if recorded.Path != agents.CompactionJev || recorded.Plugin != agents.JevPluginVersion {
		t.Fatalf("recorded %+v", recorded)
	}
}

func TestInstallJevRewritesStalePlugin(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "plugins", "fast-jev-compaction")
	hook := filepath.Join(pluginDir, "hooks", "fast-jev.ts")
	if err := os.MkdirAll(filepath.Dir(hook), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hook, []byte("stale-hook"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := agents.DecideJev(true, []string{agents.JevKeyVariable + "=k"}, agents.JevMinClaudeVersion)
	got, args, err := agents.InstallJev(dir, d)
	if err != nil {
		t.Fatalf("InstallJev: %v", err)
	}
	if got.Path != agents.CompactionJev {
		t.Fatalf("got %+v, want jev", got)
	}
	raw, err := os.ReadFile(filepath.Join(args[1], "hooks", "fast-jev.ts"))
	if err != nil {
		t.Fatalf("hook: %v", err)
	}
	if string(raw) == "stale-hook" {
		t.Fatal("left the planted stale hook in place")
	}
	if _, err := os.Stat(filepath.Join(args[1], "src", "compact.ts")); err != nil {
		t.Fatalf("src/compact.ts missing after rewrite: %v", err)
	}
}

func TestInstallJevMergesSettings(t *testing.T) {
	dir := t.TempDir()
	prior := []byte(`{"env":{"KEEP_ME":"1"},"other":true}` + "\n")
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), prior, 0o600); err != nil {
		t.Fatal(err)
	}
	d := agents.DecideJev(true, []string{agents.JevKeyVariable + "=k"}, agents.JevMinClaudeVersion)
	if _, _, err := agents.InstallJev(dir, d); err != nil {
		t.Fatalf("InstallJev: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	if settings["other"] != true {
		t.Fatalf("settings = %#v, want other kept", settings)
	}
	env, _ := settings["env"].(map[string]any)
	if env["KEEP_ME"] != "1" || env[agents.JevFunctionHooksEnv] != "1" {
		t.Fatalf("env = %#v, want KEEP_ME and %s", env, agents.JevFunctionHooksEnv)
	}
}

func TestInstallJevFallsClosedWhenSettingsWriteTruncates(t *testing.T) {
	dir := t.TempDir()
	prior := []byte(`{"env":{"KEEP_ME":"1","` + agents.JevFunctionHooksEnv + `":"1"},"other":true}` + "\n")
	settingsPath := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(settingsPath, prior, 0o600); err != nil {
		t.Fatal(err)
	}
	// Truncate then fail: the restore arm must put prior bytes back. A hook
	// that only fails on open would leave the file untouched and make the
	// restore assertion vacuous.
	agents.SetSettingsWriteForTest(func(path string, data []byte, perm os.FileMode) error {
		if err := os.WriteFile(path, []byte("{truncated"), 0o600); err != nil {
			return err
		}
		return os.ErrInvalid
	})
	t.Cleanup(func() { agents.SetSettingsWriteForTest(nil) })
	d := agents.DecideJev(true, []string{agents.JevKeyVariable + "=k"}, agents.JevMinClaudeVersion)
	got, args, err := agents.InstallJev(dir, d)
	if err != nil {
		t.Fatalf("InstallJev returned error %v, want nil (fall closed)", err)
	}
	if got.Path != agents.CompactionBuiltin || args != nil {
		t.Fatalf("got %+v args %v, want builtin with no plugin args", got, args)
	}
	if !strings.Contains(got.Reason, "settings install failed") {
		t.Fatalf("reason %q, want settings install failed", got.Reason)
	}
	if _, err := os.Stat(filepath.Join(dir, "plugins", "fast-jev-compaction")); !os.IsNotExist(err) {
		t.Fatalf("jev plugin tree left after settings failure: %v", err)
	}
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("settings.json: %v", err)
	}
	if string(raw) != string(prior) {
		t.Fatalf("settings.json = %s, want prior bytes restored after truncate", raw)
	}
}

func TestInstallJevFallsClosedWhenRecordUnwritable(t *testing.T) {
	dir := t.TempDir()
	prior := []byte(`{"env":{"KEEP_ME":"1","` + agents.JevFunctionHooksEnv + `":"1"},"other":true}` + "\n")
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), prior, 0o600); err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(dir, "plugins", "other-plugin", "marker")
	if err := os.MkdirAll(filepath.Dir(sibling), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sibling, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "compaction.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	d := agents.DecideJev(true, []string{agents.JevKeyVariable + "=k"}, agents.JevMinClaudeVersion)
	got, args, err := agents.InstallJev(dir, d)
	if err != nil {
		t.Fatalf("InstallJev returned error %v, want nil (fall closed)", err)
	}
	if got.Path != agents.CompactionBuiltin || args != nil {
		t.Fatalf("got %+v args %v, want builtin with no plugin args", got, args)
	}
	if !strings.Contains(got.Reason, "record write failed") {
		t.Fatalf("reason %q, want record write failed", got.Reason)
	}
	if _, err := os.Stat(filepath.Join(dir, "plugins", "fast-jev-compaction")); !os.IsNotExist(err) {
		t.Fatalf("jev plugin tree left after record failure: %v", err)
	}
	if raw, err := os.ReadFile(sibling); err != nil || string(raw) != "keep" {
		t.Fatalf("sibling plugin marker = %q err %v, want keep", raw, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatalf("settings.json: %v", err)
	}
	if string(raw) != string(prior) {
		t.Fatalf("settings.json = %s, want prior bytes restored", raw)
	}
}

func TestInstallJevBuiltinWritesRecordWithoutPlugin(t *testing.T) {
	dir := t.TempDir()
	d := agents.DecideJev(false, nil, "")
	got, args, err := agents.InstallJev(dir, d)
	if err != nil {
		t.Fatalf("InstallJev: %v", err)
	}
	if got.Path != agents.CompactionBuiltin || args != nil {
		t.Fatalf("got %+v args %v", got, args)
	}
	if _, err := os.Stat(filepath.Join(dir, "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("settings.json exists for builtin: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "plugins")); !os.IsNotExist(err) {
		t.Fatalf("plugins dir exists for builtin: %v", err)
	}
	recorded, err := agents.ReadCompactionDecision(dir)
	if err != nil || recorded.Path != agents.CompactionBuiltin || recorded.Reason != "opt-in off" {
		t.Fatalf("recorded %+v err %v", recorded, err)
	}
}

func TestClaudeVersionAtLeast(t *testing.T) {
	cases := []struct {
		v, min string
		ok     bool
	}{
		{"2.1.274", "2.1.274", true},
		{"2.1.308", "2.1.274", true},
		{"2.1.273", "2.1.274", false},
		{"2.1.274 (Claude Code)", "2.1.274", true},
		{"3.0.0", "2.1.274", true},
		{"", "2.1.274", false},
	}
	for _, tc := range cases {
		if got := agents.ClaudeVersionAtLeast(tc.v, tc.min); got != tc.ok {
			t.Errorf("ClaudeVersionAtLeast(%q, %q) = %v, want %v", tc.v, tc.min, got, tc.ok)
		}
	}
}
