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

func (r *claudeRunner) claudeVersion() string {
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
