package config_test

import (
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/vcs"
)

// The default of git_deadline and of gh_deadline is the value internal/vcs
// and internal/forge fall back to for a handle built without one. PRD section
// 8 says the command line's own git and gh, which are built without the
// operator's settings, run under those keys' defaults, and this is what keeps
// that sentence true when either value moves: the packages that run the
// programs read no configuration, so the two values cannot be one constant.
func TestTheInvocationDeadlineDefaultsAreThePackagesFallbacks(t *testing.T) {
	t.Parallel()
	defaults := config.Defaults()
	if defaults.GitDeadline != vcs.DefaultInvocationDeadline {
		t.Errorf("git_deadline defaults to %v, and internal/vcs falls back to %v", defaults.GitDeadline, vcs.DefaultInvocationDeadline)
	}
	if defaults.GHDeadline != forge.DefaultInvocationDeadline {
		t.Errorf("gh_deadline defaults to %v, and internal/forge falls back to %v", defaults.GHDeadline, forge.DefaultInvocationDeadline)
	}
}
