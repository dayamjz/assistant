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
	if _, err := os.Stat(filepath.Join(args[1], "hooks", "fast-jev.ts")); err != nil {
		t.Fatalf("plugin hook missing under %s: %v", args[1], err)
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
