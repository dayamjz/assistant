package vcs_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dayamjz/assistant/internal/vcs"
)

// TestUncommittedDigestFramesFileBytes is two untracked mode-0644 files whose
// bytes can stand in for the record boundary. a holds the bytes of an empty
// b's header, and b is empty. Swapping those contents is a different
// worktree. A digest that writes the bytes without a length hashes both
// states the same, and a fix round then treats the swap as no change.
func TestUncommittedDigestFramesFileBytes(t *testing.T) {
	gitEnvironment(t)
	dir := t.TempDir()
	rawGit(t, dir, "init", "--quiet", "-b", "main", ".")
	repo, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	// 00 62 00 01 a4 03 is the trailing NUL, the path b, the separator, the
	// 0644 mode bytes, and the regular-file tag. Written as a's contents,
	// with b empty, it runs into b's own record.
	swapped := []byte{0x00, 'b', 0x00, 0x01, 0xa4, 0x03}
	writeMode := func(name string, body []byte) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeMode("a", swapped)
	writeMode("b", nil)
	first, err := repo.UncommittedDigest(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	writeMode("a", nil)
	writeMode("b", swapped)
	second, err := repo.UncommittedDigest(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("swapping the two files left the digest %s unchanged", first)
	}
}
