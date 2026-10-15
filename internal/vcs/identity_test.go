package vcs_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/vcs"
)

// identityVariables are the environment variables git reads an identity from
// directly, which a test clears so that only the configuration it wrote
// decides the answer.
var identityVariables = []string{
	"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "EMAIL",
}

// clearIdentityVariables unsets every identity variable for the rest of the
// test and restores what was there afterwards.
func clearIdentityVariables(t *testing.T) {
	t.Helper()
	for _, name := range identityVariables {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unsetting %s: %v", name, err)
		}
	}
}

// TestCommitIdentityAgreesWithWhatCommitAllRecordsAndRefuses is the contract
// assistant doctor rests on: an identity CommitIdentity names is the one
// CommitAll records, and with no identity configured both refuse.
//
// The refusal half is reached through what git itself answers. Where git
// still settles an identity with nothing configured, by detecting one from the
// host, the test says so and skips that half rather than passing on a state it
// did not build.
func TestCommitIdentityAgreesWithWhatCommitAllRecordsAndRefuses(t *testing.T) {
	// No t.Parallel: the environment is the subject.
	repo, dir := newCommitSubject(t)
	clearIdentityVariables(t)

	identity, err := repo.CommitIdentity(ctx(t))
	if err != nil {
		t.Fatalf("reading the identity with one configured: %v", err)
	}
	want := "Test <test@example.invalid>"
	if identity.Author != want || identity.Committer != want {
		t.Fatalf("CommitIdentity named %+v, want %q for both", identity, want)
	}
	writeFile(t, filepath.Join(dir, "existing.txt"), "edited\n")
	if _, err := repo.CommitAll(ctx(t), "fix: record under the configured identity"); err != nil {
		t.Fatalf("committing with an identity configured: %v", err)
	}
	recorded := strings.TrimSpace(rawGit(t, dir, "log", "-1", "--format=%an <%ae>%n%cn <%ce>"))
	if recorded != identity.Author+"\n"+identity.Committer {
		t.Fatalf("CommitAll recorded %q, and CommitIdentity named %+v", recorded, identity)
	}

	// The control above passed, so the same repository with the identity taken
	// away is the only difference the refusal below can be answering.
	empty := filepath.Join(t.TempDir(), "gitconfig")
	writeFile(t, empty, "")
	t.Setenv("GIT_CONFIG_GLOBAL", empty)
	if out, err := tryRawGit(dir, "var", "GIT_COMMITTER_IDENT"); err == nil {
		t.Skipf("git settles an identity on this host with none configured (%s), so the refusal is not reachable here",
			strings.TrimSpace(out))
	}

	if got, err := repo.CommitIdentity(ctx(t)); err == nil {
		t.Fatalf("CommitIdentity named %+v with no identity configured", got)
	} else if ce := (*vcs.CommandError)(nil); !errors.As(err, &ce) {
		t.Fatalf("the refusal is %T %v, want git's own refusal as a *vcs.CommandError", err, err)
	}
	before, err := repo.ResolveCommit(ctx(t), "HEAD")
	if err != nil {
		t.Fatalf("reading HEAD: %v", err)
	}
	writeFile(t, filepath.Join(dir, "existing.txt"), "edited again\n")
	if _, err := repo.CommitAll(ctx(t), "fix: no identity"); err == nil || errors.Is(err, vcs.ErrNothingToCommit) {
		t.Fatalf("CommitAll with no identity configured returned %v, want git's refusal", err)
	}
	if after, err := repo.ResolveCommit(ctx(t), "HEAD"); err != nil || after != before {
		t.Fatalf("the refused commit moved HEAD from %s to %s (%v)", before, after, err)
	}
}

// A bare repository is where assistant doctor asks, because a run's copy is a
// linked worktree of one, so the read is checked to work there as well.
func TestCommitIdentityReadsABareRepository(t *testing.T) {
	gitEnvironment(t)
	clearIdentityVariables(t)
	bare, err := vcs.InitBare(ctx(t), filepath.Join(t.TempDir(), "bare.git"))
	if err != nil {
		t.Fatalf("creating a bare repository: %v", err)
	}
	identity, err := bare.CommitIdentity(ctx(t))
	if err != nil {
		t.Fatalf("reading the identity of a bare repository: %v", err)
	}
	if identity.Committer != "Test <test@example.invalid>" {
		t.Fatalf("the bare repository's committer is %q", identity.Committer)
	}
}
