package agents

import (
	"bytes"
	"context"
	"os"
	"strings"
	"time"

	"github.com/dayamjz/assistant/internal/procgroup"
)

const jevVersionBound = 5 * time.Second

// jevVersionForTest, when non-empty, replaces claude --version so a test can
// clear the version gate without a real Claude Code binary.
var jevVersionForTest string

// prepareJevLaunch records the compaction path in configDir and returns any
// --plugin-dir args. configDir must already exist.
func (r *claudeRunner) prepareJevLaunch(configDir string, env []string) ([]string, error) {
	optIn := r.settings.jevOptIn()
	version := ""
	if optIn {
		version = r.claudeVersion()
	}
	_, args, err := InstallJev(configDir, DecideJev(optIn, env, version))
	return args, err
}

// attachJevScratch makes a seeded scratch CLAUDE_CONFIG_DIR only when DecideJev
// selects the plugin and InstallJev keeps it. Opt-in that falls closed leaves
// the caller's environment alone: no remap to an empty directory. A mkdir or
// seed failure falls closed the same way and never refuses the launch.
func (r *claudeRunner) attachJevScratch(workspace string, env []string) (configDir string, pluginArgs []string, cleanup func()) {
	if !r.settings.jevOptIn() {
		return "", nil, nil
	}
	d := DecideJev(true, env, r.claudeVersion())
	if d.Path != CompactionJev {
		return "", nil, nil
	}
	dir, err := newJevConfigDir()
	if err != nil {
		return "", nil, nil
	}
	remove := func() { _ = os.RemoveAll(dir) }
	if err := writeClaudeConfigState(workspace, lastValue(env, ClaudeKeyVariable), dir); err != nil {
		remove()
		return "", nil, nil
	}
	got, args, err := InstallJev(dir, d)
	if err != nil || got.Path != CompactionJev || len(args) == 0 {
		remove()
		return "", nil, nil
	}
	return dir, args, remove
}

// openFixerJevDir returns a seeded scratch config dir only when InstallJev
// keeps CompactionJev, matching attachJevScratch. Fall-closed leaves the
// empty string so invoke does not remap. The fixer keeps the directory for
// later rounds (no cleanup); a drop of the fixer is the residual gap
// newJevConfigDir names.
func (r *claudeRunner) openFixerJevDir(workspace string, env []string) string {
	dir, _, _ := r.attachJevScratch(workspace, env)
	return dir
}

func (r *claudeRunner) claudeVersion() string {
	if jevVersionForTest != "" {
		return jevVersionForTest
	}
	cmd := procgroup.Command(r.resolved, "--version")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	ctx, cancel := context.WithTimeout(context.Background(), jevVersionBound)
	defer cancel()
	if err := procgroup.Run(ctx, cmd, time.Second); err != nil {
		return ""
	}
	fields := strings.Fields(stdout.String())
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// newJevConfigDir makes a scratch CLAUDE_CONFIG_DIR for stream-json workers
// and fixers. Residual gap: a fixer dir is not removed when the fixer is dropped.
func newJevConfigDir() (string, error) {
	return os.MkdirTemp("", "assistant-claude-jev-*")
}

// insertBeforeDashDash inserts extra ahead of the first "--" in args, or
// appends when args have no "--".
func insertBeforeDashDash(args []string, extra ...string) []string {
	if len(extra) == 0 {
		return args
	}
	for i, a := range args {
		if a == "--" {
			out := make([]string, 0, len(args)+len(extra))
			out = append(out, args[:i]...)
			out = append(out, extra...)
			out = append(out, args[i:]...)
			return out
		}
	}
	return append(append([]string{}, args...), extra...)
}
