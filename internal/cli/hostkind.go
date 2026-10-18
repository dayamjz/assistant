package cli

import (
	"fmt"
	"path/filepath"
	"strings"
)

// HostKind is which coordinator host an install targets. The stop guard's
// hook command is the same for every host; what differs is where the host
// reads hooks and how a blocked turn is signalled back to the host.
type HostKind string

// The two hosts this build installs for.
const (
	// HostClaude is Claude Code: hooks live in settings.json under the host's
	// config directory, and a blocked turn is exit status 2 with the reason on
	// standard error.
	HostClaude HostKind = "claude"
	// HostCodex is the OpenAI Codex CLI: hooks live in hooks.json under
	// CODEX_HOME or ~/.codex, and a blocked turn is the same exit status 2
	// with the reason on standard error, which the Stop hook reads as a
	// continuation prompt rather than ending the turn.
	HostCodex HostKind = "codex"
)

// hostInstallHostFl names the flag that selects the host.
const hostInstallHostFl = "host"

// parseHostKind reads one of the closed host names or refuses.
func parseHostKind(name string) (HostKind, error) {
	switch HostKind(name) {
	case HostClaude, HostCodex:
		return HostKind(name), nil
	default:
		return "", fmt.Errorf("%q is not a host this installer knows; want %q or %q", name, HostClaude, HostCodex)
	}
}

// hostHooksFile is the hooks or settings file the selected host reads for
// turn-end hooks, from the environment this invocation was given.
func hostHooksFile(kind HostKind, getenv func(string) string) (string, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	switch kind {
	case HostClaude:
		return hostSettingsPath(getenv)
	case HostCodex:
		return codexHooksPath(getenv)
	default:
		return "", fmt.Errorf("no hooks file for host %q", kind)
	}
}

// codexHooksPath is the Codex CLI's user hooks file. CODEX_HOME names the
// directory when set and must be absolute and exist as a directory; otherwise
// the file is HOME/.codex/hooks.json, which is what the CLI's own
// documentation names as ~/.codex/hooks.json.
const (
	codexHomeVar     = "CODEX_HOME"
	codexHomeDirName = ".codex"
	codexHooksName   = "hooks.json"
)

func codexHooksPath(getenv func(string) string) (string, error) {
	if dir := getenv(codexHomeVar); dir != "" {
		if !filepath.IsAbs(dir) {
			return "", fmt.Errorf("%s is %q, which is not an absolute path, so the host's hooks file could not be placed", codexHomeVar, dir)
		}
		return filepath.Join(dir, codexHooksName), nil
	}
	userHome := getenv("HOME")
	if userHome == "" || !filepath.IsAbs(userHome) {
		return "", fmt.Errorf("HOME is %q, which is not an absolute path, so the host's hooks file could not be placed", userHome)
	}
	return filepath.Join(userHome, codexHomeDirName, codexHooksName), nil
}

// hostKinds is every host the installer accepts, for help and tests.
func hostKinds() []HostKind {
	return []HostKind{HostClaude, HostCodex}
}

// hostKindNames is the flag's allowed values as strings.
func hostKindNames() string {
	names := make([]string, len(hostKinds()))
	for i, k := range hostKinds() {
		names[i] = string(k)
	}
	return strings.Join(names, " or ")
}
