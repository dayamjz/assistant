package vcs_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/vcs"
)

// Clone's claim is a copy with exactly the remotes the caller named. The
// remote set is read back with git directly rather than through the package,
// and read from the configuration keys rather than from a remote listing, so
// a URL the clone left behind under any remote name would show.
func TestCloneCarriesExactlyTheNamedRemote(t *testing.T) {
	gitEnvironment(t)
	src, _, second := sourceRepo(t)
	to := filepath.Join(t.TempDir(), "copy")

	cloned, err := vcs.Clone(ctx(t), src, to, "assistant")
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if got := cloned.Path(); got != to {
		t.Errorf("the handle addresses %s, want %s", got, to)
	}
	urls := remoteURLKeys(t, to)
	if want := []string{"remote.assistant.url " + src}; strings.Join(urls, "\n") != strings.Join(want, "\n") {
		t.Errorf("the copy's remote URLs are %q, want exactly %q", urls, want)
	}
	// The copy holds the source's history, checked out on the branch the
	// source's HEAD names.
	head, err := cloned.ResolveCommit(ctx(t), "HEAD")
	if err != nil {
		t.Fatalf("ResolveCommit in the copy: %v", err)
	}
	if head != second {
		t.Errorf("the copy's HEAD is %s, want the source's %s", head, second)
	}
	if branch, err := cloned.HeadBranch(ctx(t)); err != nil || branch != "main" {
		t.Errorf("the copy's branch is %q, %v; want main", branch, err)
	}
	if _, err := os.Stat(filepath.Join(to, "added.txt")); err != nil {
		t.Errorf("the copy's tree is not checked out: %v", err)
	}
}

// An empty remote name buys a copy with no remote at all, which is the
// investigation shape PRD section 5 gives a copy that pushes nothing.
func TestCloneWithNoRemoteNameCarriesNone(t *testing.T) {
	gitEnvironment(t)
	src, _, _ := sourceRepo(t)
	to := filepath.Join(t.TempDir(), "copy")

	if _, err := vcs.Clone(ctx(t), src, to, ""); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if urls := remoteURLKeys(t, to); len(urls) != 0 {
		t.Errorf("the copy carries remote URLs %q, want none", urls)
	}
}

// The name git makes a clone under is the one Clone removes, so asking for
// that name yields one remote at the source and not a remote left over from
// the clone beside the one added.
func TestCloneUnderTheRemovedNameYieldsOneRemote(t *testing.T) {
	gitEnvironment(t)
	src, _, _ := sourceRepo(t)
	to := filepath.Join(t.TempDir(), "copy")

	if _, err := vcs.Clone(ctx(t), src, to, "origin"); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	urls := remoteURLKeys(t, to)
	if want := "remote.origin.url " + src; len(urls) != 1 || urls[0] != want {
		t.Errorf("the copy's remote URLs are %q, want exactly %q", urls, want)
	}
}

// A destination that already exists is refused before git runs, whatever it
// holds, and nothing is written into it.
func TestCloneRefusesAnExistingDestination(t *testing.T) {
	gitEnvironment(t)
	src, _, _ := sourceRepo(t)
	to := t.TempDir()
	writeFile(t, filepath.Join(to, "keep.txt"), "mine\n")

	_, err := vcs.Clone(ctx(t), src, to, "assistant")
	if !errors.Is(err, vcs.ErrDestinationExists) {
		t.Fatalf("Clone into an existing directory: %v, want ErrDestinationExists", err)
	}
	entries, readErr := os.ReadDir(to)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 1 || entries[0].Name() != "keep.txt" {
		t.Errorf("the destination was written into: %v", entries)
	}
}

// A source that is not a repository is a *CommandError from git, and an
// argument git would read as an option is refused before it runs.
func TestCloneRefusesABadSource(t *testing.T) {
	gitEnvironment(t)
	parent := t.TempDir()

	_, err := vcs.Clone(ctx(t), filepath.Join(parent, "nowhere"), filepath.Join(parent, "copy"), "assistant")
	var ce *vcs.CommandError
	if !errors.As(err, &ce) {
		t.Errorf("Clone from a missing source: %v, want a *CommandError", err)
	}
	if _, err := vcs.Clone(ctx(t), "--upload-pack=true", filepath.Join(parent, "copy2"), ""); !errors.Is(err, vcs.ErrInvalidArgument) {
		t.Errorf("Clone from an option-shaped source: %v, want ErrInvalidArgument", err)
	}
	if _, err := vcs.Clone(ctx(t), parent, filepath.Join(parent, "copy3"), "-x"); !errors.Is(err, vcs.ErrInvalidArgument) {
		t.Errorf("Clone with an option-shaped remote: %v, want ErrInvalidArgument", err)
	}
}

// remoteURLKeys reads every remote URL the working copy's configuration
// carries, as "key value" lines, with git run directly.
func remoteURLKeys(t *testing.T, dir string) []string {
	t.Helper()
	out, err := tryRawGit(dir, "config", "--local", "--get-regexp", `^remote\..*\.(url|pushurl)$`)
	if err != nil {
		if strings.TrimSpace(out) == "" {
			// Exit status 1 with no output is git's answer for no match.
			return nil
		}
		t.Fatalf("git config --get-regexp in %s: %v\n%s", dir, err, out)
	}
	return strings.Split(strings.TrimSpace(out), "\n")
}
