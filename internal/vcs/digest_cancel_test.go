package vcs

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/principles"
)

// TestUncommittedDigestEndsWhileHashingALargeFile cancels the context once
// the digest has classified a large untracked file and is about to read it.
// Reading every byte of that file takes far longer than the call has, so a
// digest that hashes past its context keeps the fix round working after
// cancellation. P15 ends a wait when its deadline passes.
func TestUncommittedDigestEndsWhileHashingALargeFile(t *testing.T) {
	principles.Cite(t, principles.P15)
	dir := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, []byte("[user]\n\tname = Test\n\temail = test@example.invalid\n[gc]\n\tauto = 0\n[maintenance]\n\tauto = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "--quiet", "-b", "main", ".")
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("tracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "note.txt")
	git("commit", "--quiet", "-m", "track a file")
	big, err := os.Create(filepath.Join(dir, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := big.Truncate(16 << 30); err != nil {
		_ = big.Close()
		t.Skipf("this filesystem cannot hold a sparse 16 GiB file: %v", err)
	}
	if err := big.Close(); err != nil {
		t.Fatal(err)
	}
	repo, err := OpenWorktree(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	digestBeforeRegularOpen = func(path string) {
		if filepath.Base(path) == "big.bin" {
			time.AfterFunc(20*time.Millisecond, cancel)
		}
	}
	t.Cleanup(func() { digestBeforeRegularOpen = nil })

	started := time.Now()
	digest, err := repo.UncommittedDigest(ctx)
	elapsed := time.Since(started)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("digest %q, error %v after %s; want the cancellation while the file was read", digest, err, elapsed)
	}
	t.Logf("the digest returned %v after %s", err, elapsed)
}
