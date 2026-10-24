//go:build unix

package vcs_test

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/vcs"
)

// TestUncommittedDigestDoesNotBlockOnAFifo is a tracked file a fixer replaced
// with a FIFO that has no writer. Opening that path waits until a writer
// appears, and the wait does not observe the context, so the digest has to
// record the file type without opening it. P15 ends a wait when its deadline
// passes.
func TestUncommittedDigestDoesNotBlockOnAFifo(t *testing.T) {
	principles.Cite(t, principles.P15)
	gitEnvironment(t)
	dir := t.TempDir()
	rawGit(t, dir, "init", "--quiet", "-b", "main", ".")
	commitFile(t, dir, "note.txt", "tracked\n", "track a file")
	repo, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	before, err := repo.UncommittedDigest(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(dir, "note.txt")
	if err := os.Remove(full); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(full, 0o644); err != nil {
		t.Fatal(err)
	}

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
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("digest of a FIFO: %v", got.err)
		}
		if got.digest == before {
			t.Fatal("replacing a tracked file with a FIFO left the digest unchanged")
		}
		again, err := repo.UncommittedDigest(ctx(t))
		if err != nil || again != got.digest {
			t.Fatalf("second digest %q, error %v; want the same %q", again, err, got.digest)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("UncommittedDigest did not return after its context expired")
	}
}
