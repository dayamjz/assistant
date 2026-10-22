//go:build unix

package tasks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
)

// A copy's identity is what only that directory has: a rename keeps it, a
// clone of the same repository at the same path has another, and a copy
// whose .git is replaced is another too, though its root is the same
// directory. The written form reads back to the same identity and nothing
// else is read as one. Without the .git in it a copy whose repository was
// swapped under the same root would pass as the kept one.
func TestACopysIdentityIsItsDirectoriesAndNotItsPath(t *testing.T) {
	principles.Cite(t, principles.P15)
	dir := t.TempDir()
	kept := filepath.Join(dir, "kept")
	rawGit(t, dir, "init", "-q", kept)
	first, err := IdentifyCopy(kept)
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(dir, "moved")
	if err := os.Rename(kept, moved); err != nil {
		t.Fatal(err)
	}
	if after, err := IdentifyCopy(moved); err != nil || !after.Same(first) {
		t.Fatalf("the moved copy reads %v, %v; want %v, the same copy", after, err, first)
	}
	rawGit(t, dir, "init", "-q", kept)
	if other, err := IdentifyCopy(kept); err != nil || other.Same(first) {
		t.Fatalf("a new repository at the kept copy's old path reads %v, %v; want another identity than %v", other, err, first)
	}

	if err := os.Rename(filepath.Join(moved, ".git"), filepath.Join(dir, "old-git")); err != nil {
		t.Fatal(err)
	}
	rawGit(t, dir, "init", "-q", filepath.Join(dir, "donor"))
	if err := os.Rename(filepath.Join(dir, "donor", ".git"), filepath.Join(moved, ".git")); err != nil {
		t.Fatal(err)
	}
	if swapped, err := IdentifyCopy(moved); err != nil || swapped.Same(first) {
		t.Fatalf("the copy with its .git replaced reads %v, %v; want another identity than %v", swapped, err, first)
	}

	parsed, err := ParseCopyIdentity(first.String())
	if err != nil || !parsed.Same(first) {
		t.Fatalf("ParseCopyIdentity(%q) = %v, %v; want %v", first.String(), parsed, err, first)
	}
	for _, text := range []string{"", "1:2:3", "1:2:3:x", "planted-copy", "1:2:3:4:5"} {
		if _, err := ParseCopyIdentity(text); err == nil {
			t.Errorf("ParseCopyIdentity(%q) read an identity", text)
		}
	}
}
