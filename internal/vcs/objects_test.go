package vcs_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
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

// NotReachedFrom lists what named objects hold that a commit does not
// reach, by object, as git decides it: in a repository with a commit kept
// and one past it that a reset left named only by the reflog, the commit
// past it is listed among the commits, and every named object that is not a
// commit is listed as one, whatever holds it: the kept commit's own tree and
// blob, a tree it does not hold, a loose blob, an annotated tag on the kept
// commit and one on the commit past it. A commit named twice is listed once,
// a commit the kept one reaches is not listed, and an object the repository
// does not hold is an error rather than an empty answer. ObjectType reads
// each type as git records it.
func TestNotReachedFromListsWhatOnlyTheNamesHold(t *testing.T) {
	gitEnvironment(t)
	dir := t.TempDir()
	rawGit(t, dir, "init", "--quiet", ".")
	writeFile(t, filepath.Join(dir, "kept.txt"), "kept\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "--quiet", "-m", "kept")
	kept := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD"))
	writeFile(t, filepath.Join(dir, "past.txt"), "past\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "--quiet", "-m", "past it")
	past := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD"))
	pastTree := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD^{tree}"))
	rawGit(t, dir, "tag", "--annotate", "--message", "on the past", "on-past", past)
	rawGit(t, dir, "reset", "--quiet", "--hard", kept)
	keptTree := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD^{tree}"))
	rawGit(t, dir, "tag", "--annotate", "--message", "on the kept", "on-kept", kept)
	onKept := strings.TrimSpace(rawGit(t, dir, "rev-parse", "on-kept"))
	onPast := strings.TrimSpace(rawGit(t, dir, "rev-parse", "on-past"))
	blob := strings.TrimSpace(rawGit(t, dir, "hash-object", "-w", "kept.txt"))
	writeFile(t, filepath.Join(dir, "loose.txt"), "a blob only a name holds\n")
	loose := strings.TrimSpace(rawGit(t, dir, "hash-object", "-w", "loose.txt"))
	repo, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatal(err)
	}

	names := []string{kept, past, past, keptTree, pastTree, blob, loose, onKept, onPast}
	commits, others, err := repo.NotReachedFrom(ctx(t), names, kept)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(commits, []string{past}) {
		t.Errorf("commits = %v; want only %s, the commit past the kept one", commits, past)
	}
	if want := []string{keptTree, pastTree, blob, loose, onKept, onPast}; !slices.Equal(others, want) {
		t.Errorf("others = %v; want %v, every named object that is not a commit", others, want)
	}
	if commits, others, err := repo.NotReachedFrom(ctx(t), []string{kept}, kept); err != nil || len(commits) != 0 || len(others) != 0 {
		t.Errorf("NotReachedFrom of the kept commit alone = %v, %v, %v; want nothing", commits, others, err)
	}
	missing := strings.Repeat("1", len(kept))
	if _, _, err := repo.NotReachedFrom(ctx(t), []string{missing}, kept); err == nil {
		t.Error("NotReachedFrom of an object the repository does not hold succeeded; want an error")
	}
	for object, want := range map[string]string{past: "commit", pastTree: "tree", loose: "blob", onPast: "tag"} {
		if got, err := repo.ObjectType(ctx(t), object); err != nil || got != want {
			t.Errorf("ObjectType(%s) = %q, %v; want %s", object, got, err, want)
		}
	}
	if _, err := repo.ObjectType(ctx(t), missing); err == nil {
		t.Error("ObjectType of an object the repository does not hold succeeded; want an error")
	}
}
