//go:build unix

package vcs

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/principles"
)

// fifoHangLimit is how long the FIFO test waits for a digest before calling
// it stuck. It is the bound vcs_test's hangLimit sets for a git that never
// exits; a digest that does not wait on the FIFO returns well inside it on
// a loaded machine.
const fifoHangLimit = 20 * time.Second

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

	// The call has no deadline of its own: one would also bound the git
	// invocations the digest makes, and a loaded machine fails those for
	// reasons that have nothing to do with the FIFO. A digest that waited
	// in the open would wait for a writer forever, which fifoHangLimit
	// catches, and AwaitFifoDigest bounds every step after that too.
	type result struct {
		digest string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		digest, err := repo.UncommittedDigest(t.Context())
		done <- result{digest, err}
	}()
	got, err := AwaitFifoDigest(done, note, fifoHangLimit, FifoGrace)
	if err != nil {
		t.Fatal(err)
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
