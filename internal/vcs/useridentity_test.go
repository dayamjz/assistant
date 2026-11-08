package vcs_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/dayamjz/assistant/internal/vcs"
)

// UserIdentity names the configured user.name and user.email under the
// process environment, and refuses with ErrNoUserIdentity when either is
// unset rather than settling a host-derived identity.
func TestUserIdentityNamesTheConfiguredIdentityAndRefusesWithoutOne(t *testing.T) {
	// No t.Parallel: the environment is the subject.
	clearIdentityVariables(t)
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	writeFile(t, cfg, "[user]\n\tname = Configured User\n\temail = configured@example.invalid\n")
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	name, email, err := vcs.UserIdentity(ctx(t))
	if err != nil {
		t.Fatalf("reading a configured identity: %v", err)
	}
	if name != "Configured User" || email != "configured@example.invalid" {
		t.Fatalf("UserIdentity = %q <%q>, want the configured pair", name, email)
	}

	empty := filepath.Join(t.TempDir(), "empty")
	writeFile(t, empty, "")
	t.Setenv("GIT_CONFIG_GLOBAL", empty)
	if _, _, err := vcs.UserIdentity(ctx(t)); !errors.Is(err, vcs.ErrNoUserIdentity) {
		t.Fatalf("UserIdentity with no configured identity answered %v, want ErrNoUserIdentity", err)
	}
}
