package vcs_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/vcs"
)

// BlobObject is git's own identity of a working-tree entry's content: in a
// repository of each object format, a file, an executable and a symbolic
// link are committed, and for each the identifier BlobObject computes from
// the working tree is the object AllTreeEntries reads for it from HEAD's
// tree, and for a file the one git hash-object gives with no filter. The
// link is identified by its target, not by the file it points at. A file
// edited since reads differently, so equality is not a constant, and a
// directory or an unknown format is refused. AllTreeEntries also reads the
// directory's own entry as a tree, and ObjectFormat the format git reports.
func TestBlobObjectIsGitsIdentityOfTheEntry(t *testing.T) {
	gitEnvironment(t)
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			dir := t.TempDir()
			rawGit(t, dir, "init", "--quiet", "--object-format="+format, ".")
			writeFile(t, filepath.Join(dir, "plain.txt"), "plain content\n")
			writeFile(t, filepath.Join(dir, "docs", "tool.sh"), "#!/bin/sh\necho tool\n")
			if err := os.Chmod(filepath.Join(dir, "docs", "tool.sh"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("plain.txt", filepath.Join(dir, "link")); err != nil {
				t.Fatal(err)
			}
			rawGit(t, dir, "add", "-A")
			rawGit(t, dir, "commit", "--quiet", "-m", "first")
			repo, err := vcs.OpenWorktree(ctx(t), dir)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := repo.ObjectFormat(ctx(t)); err != nil || got != format {
				t.Fatalf("ObjectFormat = %q, %v; want %s", got, err, format)
			}
			head, err := repo.AllTreeEntries(ctx(t), "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			if e := head["docs"]; e.Type != "tree" {
				t.Errorf("AllTreeEntries reads docs as %+v; want its own entry, a tree", e)
			}
			for path, mode := range map[string]string{"plain.txt": "100644", "docs/tool.sh": "100755", "link": "120000"} {
				e, ok := head[path]
				if !ok || e.Mode != mode || e.Type != "blob" {
					t.Fatalf("AllTreeEntries reads %s as %+v, %v; want a blob of mode %s", path, e, ok, mode)
				}
				got, err := vcs.BlobObject(format, filepath.Join(dir, path))
				if err != nil || got != e.Object {
					t.Errorf("BlobObject(%s) = %s, %v; want HEAD's %s", path, got, err, e.Object)
				}
				if mode != "120000" {
					if hashed := strings.TrimSpace(rawGit(t, dir, "hash-object", "--no-filters", path)); got != hashed {
						t.Errorf("BlobObject(%s) = %s; want git hash-object's %s", path, got, hashed)
					}
				}
			}
			if linked, err := vcs.BlobObject(format, filepath.Join(dir, "link")); err != nil || linked == head["plain.txt"].Object {
				t.Errorf("BlobObject(link) = %s, %v; want the target's identity, not the file it points at", linked, err)
			}
			writeFile(t, filepath.Join(dir, "plain.txt"), "edited since\n")
			if got, err := vcs.BlobObject(format, filepath.Join(dir, "plain.txt")); err != nil || got == head["plain.txt"].Object {
				t.Errorf("BlobObject of an edited file = %s, %v; want it to differ from HEAD's", got, err)
			}
			if _, err := vcs.BlobObject(format, filepath.Join(dir, "docs")); !errors.Is(err, vcs.ErrInvalidArgument) {
				t.Errorf("BlobObject of a directory = %v; want ErrInvalidArgument", err)
			}
		})
	}
	if _, err := vcs.BlobObject("md5", t.TempDir()); !errors.Is(err, vcs.ErrInvalidArgument) {
		t.Errorf("BlobObject in an unknown format = %v; want ErrInvalidArgument", err)
	}
}
