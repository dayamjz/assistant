//go:build unix

package vcs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/principles"
)

// TestUncommittedDigestOpenDoesNotBlockWhenAFifoReplacesTheFile classifies a
// changed regular file, then replaces it with a FIFO that has no writer
// before the open. The stat has already called the path regular, so the open
// is what waits, and that wait does not observe the context. P15 ends a wait
// when its deadline passes.
func TestUncommittedDigestOpenDoesNotBlockWhenAFifoReplacesTheFile(t *testing.T) {
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
	note := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(note, []byte("tracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "note.txt")
	git("commit", "--quiet", "-m", "track a file")
	repo, err := OpenWorktree(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	clean, err := repo.UncommittedDigest(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(note, []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	regular, err := repo.UncommittedDigest(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if regular == clean {
		t.Fatal("editing the tracked file left the digest unchanged, so the open path was not exercised")
	}

	var swapErr error
	digestBeforeRegularOpen = func(path string) {
		if filepath.Base(path) != "note.txt" {
			return
		}
		if err := os.Remove(path); err != nil {
			swapErr = err
			return
		}
		swapErr = syscall.Mkfifo(path, 0o644)
	}
	t.Cleanup(func() { digestBeforeRegularOpen = nil })

	deadline, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	type result struct {
		digest string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		digest, err := repo.UncommittedDigest(deadline)
		done <- result{digest, err}
	}()
	var got result
	select {
	case got = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("UncommittedDigest did not return after its context expired")
	}
	if swapErr != nil {
		t.Fatalf("replacing the file with a FIFO: %v", swapErr)
	}
	if got.err != nil {
		t.Fatalf("digest after the file became a FIFO: %v", got.err)
	}
	if got.digest == regular {
		t.Fatal("a FIFO that replaced the regular file left the digest unchanged")
	}
	digestBeforeRegularOpen = nil
	again, err := repo.UncommittedDigest(t.Context())
	if err != nil || again != got.digest {
		t.Fatalf("second digest %q, error %v; want the same %q", again, err, got.digest)
	}
}
