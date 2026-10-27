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
	commits, others, err := repo.NotReachedFrom(ctx(t), vcs.Reach{Objects: names}, kept)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(commits, []string{past}) {
		t.Errorf("commits = %v; want only %s, the commit past the kept one", commits, past)
	}
	if want := []string{keptTree, pastTree, blob, loose, onKept, onPast}; !slices.Equal(others, want) {
		t.Errorf("others = %v; want %v, every named object that is not a commit", others, want)
	}
	// Held: the tag on the past commit holds that commit, through the tag,
	// and itself; the loose blob holds itself; the kept tree, held by
	// nothing but a commit of incorporated, is listed still.
	commits, others, err = repo.NotReachedFrom(ctx(t), vcs.Reach{Objects: names}, kept, onPast, loose)
	if err != nil || len(commits) != 0 {
		t.Errorf("commits with the tag on the past commit held = %v, %v; want none", commits, err)
	}
	if want := []string{keptTree, pastTree, blob, onKept}; !slices.Equal(others, want) {
		t.Errorf("others with the tag and the loose blob held = %v; want %v", others, want)
	}
	if commits, _, err := repo.NotReachedFrom(ctx(t), vcs.Reach{Objects: names}, kept, past); err != nil || len(commits) != 0 {
		t.Errorf("commits with the past commit held = %v, %v; want none", commits, err)
	}
	if _, _, err := repo.NotReachedFrom(ctx(t), vcs.Reach{Objects: names}, kept, strings.Repeat("1", len(kept))); err == nil {
		t.Error("NotReachedFrom holding an object the repository does not hold succeeded; want an error")
	}
	if commits, others, err := repo.NotReachedFrom(ctx(t), vcs.Reach{Objects: []string{kept}}, kept); err != nil || len(commits) != 0 || len(others) != 0 {
		t.Errorf("NotReachedFrom of the kept commit alone = %v, %v, %v; want nothing", commits, others, err)
	}
	missing := strings.Repeat("1", len(kept))
	if _, _, err := repo.NotReachedFrom(ctx(t), vcs.Reach{Objects: []string{missing}}, kept); err == nil {
		t.Error("NotReachedFrom of an object the repository does not hold succeeded; want an error")
	}
	for object, want := range map[string]string{past: "commit", pastTree: "tree", loose: "blob", onPast: "tag"} {
		if got, err := repo.ObjectType(ctx(t), object); err != nil || got != want {
			t.Errorf("ObjectType(%s) = %q, %v; want %s", object, got, err, want)
		}
	}
	if _, err := repo.ObjectType(ctx(t), missing); !errors.Is(err, vcs.ErrObjectNotFound) {
		t.Errorf("ObjectType of an object the repository does not hold = %v; want ErrObjectNotFound", err)
	}
}

// ObjectType tells an object the repository does not have, ErrObjectNotFound,
// from one it has but cannot read, an error that does not match it (round
// 46, P6: a caller that takes absence for an answer must not take a failed
// read for it). The positive control is the blob read as a blob; then its
// loose object is emptied, and then overwritten with bytes that are not an
// object, and each is the error and not the absence.
func TestObjectTypeTellsAnAbsentObjectFromOneItCannotRead(t *testing.T) {
	gitEnvironment(t)
	dir := t.TempDir()
	rawGit(t, dir, "init", "--quiet", ".")
	writeFile(t, filepath.Join(dir, "a.txt"), "bytes\n")
	blob := strings.TrimSpace(rawGit(t, dir, "hash-object", "-w", "a.txt"))
	repo, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := repo.ObjectType(ctx(t), blob); err != nil || got != "blob" {
		t.Fatalf("ObjectType(%s) = %q, %v; want blob", blob, got, err)
	}
	missing := strings.Repeat("1", len(blob))
	if _, err := repo.ObjectType(ctx(t), missing); !errors.Is(err, vcs.ErrObjectNotFound) {
		t.Fatalf("ObjectType of an absent object = %v; want ErrObjectNotFound", err)
	}
	loose := filepath.Join(dir, ".git", "objects", blob[:2], blob[2:])
	if err := os.Chmod(loose, 0o644); err != nil {
		t.Fatal(err)
	}
	for name, bytes := range map[string][]byte{"emptied": nil, "overwritten": []byte("not an object")} {
		if err := os.WriteFile(loose, bytes, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.ObjectType(ctx(t), blob); err == nil || errors.Is(err, vcs.ErrObjectNotFound) {
			t.Errorf("ObjectType of the blob with its loose object %s = %v; want an error that is not ErrObjectNotFound", name, err)
		}
	}
}

// NotHeldBy answers from rev's whole history: of a commit, trees and blobs
// HEAD's history holds, a tree and a commit only a commit past HEAD holds,
// a blob nothing holds, annotated tags on both commits and an object the
// repository does not hold, it returns exactly the ones HEAD's history does
// not hold, each once and in order: the past commit and its tree, the
// loose blob, both tags and the missing object. A blob only an older commit
// of HEAD's history records, which HEAD's own tree does not, is held.
func TestNotHeldByAnswersFromTheWholeHistory(t *testing.T) {
	gitEnvironment(t)
	dir := t.TempDir()
	rawGit(t, dir, "init", "--quiet", ".")
	writeFile(t, filepath.Join(dir, "old.txt"), "only the first commit holds this\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "--quiet", "-m", "first")
	old := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD:old.txt"))
	rawGit(t, dir, "rm", "--quiet", "old.txt")
	writeFile(t, filepath.Join(dir, "kept.txt"), "kept\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "--quiet", "-m", "kept")
	kept := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD"))
	keptTree := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD^{tree}"))
	keptBlob := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD:kept.txt"))
	writeFile(t, filepath.Join(dir, "past.txt"), "past\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "--quiet", "-m", "past it")
	past := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD"))
	pastTree := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD^{tree}"))
	rawGit(t, dir, "tag", "--annotate", "--message", "on the past", "on-past", past)
	rawGit(t, dir, "reset", "--quiet", "--hard", kept)
	rawGit(t, dir, "tag", "--annotate", "--message", "on the kept", "on-kept", kept)
	onKept := strings.TrimSpace(rawGit(t, dir, "rev-parse", "on-kept"))
	onPast := strings.TrimSpace(rawGit(t, dir, "rev-parse", "on-past"))
	writeFile(t, filepath.Join(dir, "loose.txt"), "a blob nothing holds\n")
	loose := strings.TrimSpace(rawGit(t, dir, "hash-object", "-w", "loose.txt"))
	missing := strings.Repeat("1", len(kept))
	repo, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatal(err)
	}

	objects := []string{kept, keptTree, keptBlob, old, past, pastTree, pastTree, loose, onKept, onPast, missing}
	got, err := repo.NotHeldBy(ctx(t), "HEAD", objects)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{past, pastTree, loose, onKept, onPast, missing}; !slices.Equal(got, want) {
		t.Errorf("NotHeldBy = %v; want %v", got, want)
	}
	if got, err := repo.NotHeldBy(ctx(t), "HEAD", []string{kept, keptTree, keptBlob, old}); err != nil || len(got) != 0 {
		t.Errorf("NotHeldBy of what HEAD's history holds = %v, %v; want nothing", got, err)
	}
	if _, err := repo.NotHeldBy(ctx(t), missing, []string{kept}); err == nil {
		t.Error("NotHeldBy from a commit the repository does not hold succeeded; want an error")
	}
}

// ReflogValues reads every value of every reflog, previous values and
// values of any type among them: a commit a reset left recorded only as the
// previous value of the one entry left in HEAD's reflog, which git log
// --walk-reflogs names nowhere, and a blob a notes reference's reflog
// records only as the value it moved from are both read, and NotReachedFrom
// given them lists the commit as not reached and the blob among the others.
// A commit another worktree's HEAD reflog alone records is read too, since
// every worktree's HEAD's log is. RootRefs lists ORIG_HEAD, which the reset
// wrote naming the commit, and HEAD, and no reference under refs/.
func TestReflogValuesReadEveryValueOfEveryLog(t *testing.T) {
	gitEnvironment(t)
	dir := filepath.Join(t.TempDir(), "main")
	rawGit(t, filepath.Dir(dir), "init", "--quiet", dir)
	writeFile(t, filepath.Join(dir, "kept.txt"), "kept\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "--quiet", "-m", "kept")
	kept := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD"))
	rawGit(t, dir, "commit", "--quiet", "--allow-empty", "-m", "reset away")
	reset := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD"))
	rawGit(t, dir, "reset", "--quiet", "--hard", kept)
	for range strings.Count(rawGit(t, dir, "log", "--walk-reflogs", "--format=%H", "HEAD"), "\n") - 1 {
		rawGit(t, dir, "reflog", "delete", "HEAD@{1}")
	}
	// The branch's own log: @{0} the reset, @{1} the commit reset away.
	branch := strings.TrimSpace(rawGit(t, dir, "symbolic-ref", "HEAD"))
	rawGit(t, dir, "reflog", "delete", branch+"@{1}")
	loose := filepath.Join(t.TempDir(), "loose.txt")
	writeFile(t, loose, "a blob only a previous value records\n")
	blob := strings.TrimSpace(rawGit(t, dir, "hash-object", "-w", loose))
	rawGit(t, dir, "update-ref", "--create-reflog", "refs/notes/aside", kept)
	rawGit(t, dir, "update-ref", "refs/notes/aside", blob)
	rawGit(t, dir, "update-ref", "refs/notes/aside", kept)
	rawGit(t, dir, "reflog", "delete", "refs/notes/aside@{1}")
	if entries := rawGit(t, dir, "log", "--walk-reflogs", "--format=%H", "--all"); strings.Contains(entries, reset) || strings.Contains(entries, blob) {
		t.Fatalf("a reflog still names %s or %s as an entry's new value:\n%s", reset, blob, entries)
	}
	other := filepath.Join(filepath.Dir(dir), "other")
	rawGit(t, dir, "worktree", "add", "--quiet", "--detach", other, kept)
	rawGit(t, other, "commit", "--quiet", "--allow-empty", "-m", "the other worktree's")
	elsewhere := strings.TrimSpace(rawGit(t, other, "rev-parse", "HEAD"))
	rawGit(t, other, "reset", "--quiet", "--hard", kept)
	repo, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatal(err)
	}

	all, err := repo.ReflogValues(ctx(t))
	if err != nil || !slices.Contains(all, reset) || !slices.Contains(all, blob) || !slices.Contains(all, elsewhere) {
		t.Fatalf("every reflog's values = %v, %v; want %s, %s and %s among them", all, err, reset, blob, elsewhere)
	}
	commits, others, err := repo.NotReachedFrom(ctx(t), vcs.Reach{Objects: all}, kept)
	if err != nil || !slices.Contains(commits, reset) || !slices.Contains(others, blob) {
		t.Errorf("NotReachedFrom of every reflog value = %v, %v, %v; want %s among the commits and %s among the others",
			commits, others, err, reset, blob)
	}
	roots, err := repo.RootRefs(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]string{}
	for _, ref := range roots {
		if strings.HasPrefix(ref.Name, "refs/") {
			t.Errorf("RootRefs listed %s, a reference under refs/", ref.Name)
		}
		byName[ref.Name] = ref.Object
	}
	if byName["ORIG_HEAD"] != reset || byName["HEAD"] != kept {
		t.Errorf("RootRefs = %+v; want ORIG_HEAD at %s and HEAD at %s", roots, reset, kept)
	}
}

// oldGitVar names a git binary older than the one on PATH, the oldest this
// package supports among them, which TestRootRefsAreListedUnderEveryGit
// runs RootRefs under too when it is set; doc.go's Requirements say which.
const oldGitVar = "ASSISTANT_TEST_OLD_GIT"

// RootRefs lists the references git keeps outside refs/ for a working copy
// under every git this package supports, the git on PATH and, when
// ASSISTANT_TEST_OLD_GIT names one, an older one (the final full review of
// 221c3b1 found git 2.36 to 2.44 refusing the option RootRefs used): HEAD,
// ORIG_HEAD a reset left, and a root reference of another name, FOO_HEAD,
// each at its commit, and one at an annotated tag peeled to the tag's
// commit; never FETCH_HEAD or MERGE_HEAD, which git lists as no
// reference, nor a file or a directory named like one that is none, nor
// anything under refs/; and for a linked worktree its own
// ORIG_HEAD and one only it has, not the main working copy's.
func TestRootRefsAreListedUnderEveryGit(t *testing.T) {
	gitEnvironment(t)
	gits := []string{"git"}
	if old := os.Getenv(oldGitVar); old != "" {
		gits = append(gits, old)
	} else {
		t.Logf("%s is not set, so RootRefs is run under the git on PATH alone", oldGitVar)
	}
	for _, binary := range gits {
		t.Run(filepath.Base(filepath.Dir(filepath.Dir(binary)))+"/"+filepath.Base(binary), func(t *testing.T) {
			dir := t.TempDir()
			rawGit(t, dir, "init", "--quiet", ".")
			rawGit(t, dir, "commit", "--quiet", "--allow-empty", "-m", "A")
			a := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD"))
			rawGit(t, dir, "commit", "--quiet", "--allow-empty", "-m", "B")
			b := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD"))
			rawGit(t, dir, "reset", "--quiet", "--hard", a)
			rawGit(t, dir, "update-ref", "FOO_HEAD", b)
			rawGit(t, dir, "tag", "--annotate", "--message", "a tag a root reference names", "annotated", b)
			tag := strings.TrimSpace(rawGit(t, dir, "rev-parse", "annotated"))
			rawGit(t, dir, "update-ref", "TAG_HEAD", tag)
			writeFile(t, filepath.Join(dir, ".git", "FETCH_HEAD"), b+"\t\tbranch 'main' of somewhere\n")
			writeFile(t, filepath.Join(dir, ".git", "MERGE_HEAD"), b+"\n")
			// Named as a root reference and none: a file git reads as a
			// broken reference and a directory.
			writeFile(t, filepath.Join(dir, ".git", "JUNK_HEAD"), "not a reference\n")
			// Holding a commit, and named with a lowercase letter, which git
			// takes as no root reference.
			writeFile(t, filepath.Join(dir, ".git", "Mixed_HEAD"), b+"\n")
			if err := os.Mkdir(filepath.Join(dir, ".git", "DIR_HEAD"), 0o700); err != nil {
				t.Fatal(err)
			}
			other := filepath.Join(t.TempDir(), "other")
			rawGit(t, dir, "worktree", "add", "--quiet", "--detach", other, b)
			rawGit(t, other, "reset", "--quiet", "--hard", a)
			rawGit(t, other, "update-ref", "BAR_HEAD", a)

			read := func(path string) map[string]string {
				t.Helper()
				repo, err := vcs.OpenWorktree(ctx(t), path, vcs.WithGitBinary(binary))
				if err != nil {
					t.Fatal(err)
				}
				roots, err := repo.RootRefs(ctx(t))
				if err != nil {
					t.Fatalf("RootRefs under %s: %v", binary, err)
				}
				// The reading for a git without --include-root-refs, run
				// under this git too, finds the same references.
				byName, err := vcs.RootRefsByNameForTest(ctx(t), repo)
				if err != nil {
					t.Fatalf("RootRefs' reading by name under %s: %v", binary, err)
				}
				sortRefs := func(refs []vcs.Ref) []vcs.Ref {
					out := slices.Clone(refs)
					slices.SortFunc(out, func(x, y vcs.Ref) int { return strings.Compare(x.Name, y.Name) })
					return out
				}
				if !slices.Equal(sortRefs(roots), sortRefs(byName)) {
					t.Errorf("under %s RootRefs lists %+v and the reading by name %+v; want the same references", binary, roots, byName)
				}
				found := map[string]string{}
				for _, ref := range roots {
					if strings.HasPrefix(ref.Name, "refs/") {
						t.Errorf("RootRefs listed %s, a reference under refs/", ref.Name)
					}
					found[ref.Name] = ref.Commit
				}
				return found
			}
			mine := read(dir)
			if mine["HEAD"] != a || mine["ORIG_HEAD"] != b || mine["FOO_HEAD"] != b || mine["TAG_HEAD"] != b {
				t.Errorf("RootRefs under %s = %v; want HEAD at %s, ORIG_HEAD, FOO_HEAD and TAG_HEAD, peeled, at %s", binary, mine, a, b)
			}
			for _, special := range []string{"FETCH_HEAD", "MERGE_HEAD", "JUNK_HEAD", "DIR_HEAD", "Mixed_HEAD"} {
				if _, ok := mine[special]; ok {
					t.Errorf("RootRefs under %s listed %s, which git lists as no reference", binary, special)
				}
			}
			theirs := read(other)
			if theirs["HEAD"] != a || theirs["ORIG_HEAD"] != b || theirs["BAR_HEAD"] != a {
				t.Errorf("RootRefs of the linked worktree under %s = %v; want its own HEAD and BAR_HEAD at %s and ORIG_HEAD at %s", binary, theirs, a, b)
			}
			if _, ok := theirs["FOO_HEAD"]; ok {
				t.Errorf("RootRefs of the linked worktree under %s listed FOO_HEAD, the main working copy's", binary)
			}
		})
	}
}

// StoredObjects is the blob git would store for each file: a file checked
// out with CRLF under its attributes reads as the LF blob it was checked out
// from, though its bytes differ, a file with no conversion as its bytes, in
// the order asked; a directory, and a path holding a newline, are errors.
func TestStoredObjectsAreWhatGitWouldStore(t *testing.T) {
	gitEnvironment(t)
	dir := t.TempDir()
	rawGit(t, dir, "init", "--quiet", ".")
	writeFile(t, filepath.Join(dir, ".gitattributes"), "*.bat text eol=crlf\n")
	writeFile(t, filepath.Join(dir, "a.bat"), "echo one\necho two\n")
	writeFile(t, filepath.Join(dir, "plain.txt"), "plain\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "--quiet", "-m", "converted")
	if err := os.Remove(filepath.Join(dir, "a.bat")); err != nil {
		t.Fatal(err)
	}
	rawGit(t, dir, "checkout", "--", "a.bat")
	stored := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD:a.bat"))
	plain := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD:plain.txt"))
	raw, err := vcs.BlobObject("sha1", filepath.Join(dir, "a.bat"))
	if err != nil || raw == stored {
		t.Fatalf("the checked-out a.bat's bytes hash to %s, %v; want them to differ from the stored %s", raw, err, stored)
	}
	repo, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := repo.StoredObjects(ctx(t), []string{"plain.txt", "a.bat"}); err != nil || !slices.Equal(got, []string{plain, stored}) {
		t.Errorf("StoredObjects = %v, %v; want %s and %s", got, err, plain, stored)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.StoredObjects(ctx(t), []string{"sub"}); err == nil {
		t.Error("StoredObjects of a directory succeeded; want an error")
	}
	if _, err := repo.StoredObjects(ctx(t), []string{"a\nb"}); err == nil {
		t.Error("StoredObjects of a path holding a newline succeeded; want an error")
	}
}
