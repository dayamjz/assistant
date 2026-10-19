package vcs_test

import (
	"path/filepath"
	"testing"

	"github.com/dayamjz/assistant/internal/vcs"
)

// PushURL is the fetch URL until a push URL is configured, and then it is
// that push URL. Neither read contacts the remote: the other path does not
// have to exist.
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
	gotFetch, err := repo.RemoteURL(c, "origin")
	if err != nil {
		t.Fatalf("RemoteURL: %v", err)
	}
	gotPush, err := repo.PushURL(c, "origin")
	if err != nil {
		t.Fatalf("PushURL: %v", err)
	}
	if gotFetch != fetch || gotPush != fetch {
		t.Fatalf("fetch %q push %q, want both %q", gotFetch, gotPush, fetch)
	}

	rawGit(t, dir, "remote", "set-url", "--push", "origin", push)
	gotFetch, err = repo.RemoteURL(c, "origin")
	if err != nil {
		t.Fatalf("RemoteURL after set-url --push: %v", err)
	}
	gotPush, err = repo.PushURL(c, "origin")
	if err != nil {
		t.Fatalf("PushURL after set-url --push: %v", err)
	}
	if gotFetch != fetch || gotPush != push {
		t.Fatalf("fetch %q push %q, want %q and %q", gotFetch, gotPush, fetch, push)
	}
}
