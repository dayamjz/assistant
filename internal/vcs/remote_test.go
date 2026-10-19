package vcs_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/dayamjz/assistant/internal/vcs"
)

// PushURL is the fetch URL until a push URL is configured, and then it is
// that push URL. Neither read contacts the remote: neither path has to exist.
// A remote that is not there is ErrRemoteNotFound, as RemoteURL reports it.
func TestPushURLFollowsAConfiguredPushAddress(t *testing.T) {
	gitEnvironment(t)
	c := ctx(t)
	dir, _, _ := sourceRepo(t)
	fetch := filepath.Join(t.TempDir(), "fetch.git")
	push := filepath.Join(t.TempDir(), "push.git")
	rawGit(t, dir, "remote", "add", "origin", fetch)

	repo, err := vcs.OpenWorktree(c, dir)
	if err != nil {
		t.Fatalf("OpenWorktree: %v", err)
	}
	if got, err := repo.PushURL(c, "origin"); err != nil || got != fetch {
		t.Fatalf("PushURL with no push address = %q, %v; want the fetch URL %q", got, err, fetch)
	}

	rawGit(t, dir, "remote", "set-url", "--push", "origin", push)
	if got, err := repo.RemoteURL(c, "origin"); err != nil || got != fetch {
		t.Fatalf("RemoteURL after set-url --push = %q, %v; want %q", got, err, fetch)
	}
	if got, err := repo.PushURL(c, "origin"); err != nil || got != push {
		t.Fatalf("PushURL after set-url --push = %q, %v; want %q", got, err, push)
	}

	if _, err := repo.PushURL(c, "nowhere"); !errors.Is(err, vcs.ErrRemoteNotFound) {
		t.Errorf("PushURL of a remote that is not there = %v, want ErrRemoteNotFound", err)
	}
}
