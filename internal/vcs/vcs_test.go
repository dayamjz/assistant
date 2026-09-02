package vcs_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/vcs"
)

// A bare repository must be usable under safe.bareRepository=explicit, which
// forbids git from discovering a bare repository from the working directory.
// The control at the top of this test is what makes the rest of it mean
// anything: it proves the setting is actually in effect, so that if explicit
// addressing were dropped this test would fail rather than pass silently.
func TestBareRepositoryWorksUnderExplicitBareSafety(t *testing.T) {
	cfg := gitEnvironment(t)
	appendConfig(t, cfg, "[safe]\n\tbareRepository = explicit\n")
	c := ctx(t)

	source, first, second := sourceRepo(t)
	barePath := filepath.Join(t.TempDir(), "gate.git")

	bare, err := vcs.InitBare(c, barePath)
	if err != nil {
		t.Fatalf("InitBare: %v", err)
	}

	// The control. Discovery from the working directory must fail here; if it
	// succeeds, the setting is not in effect and everything below would pass
	// even without explicit addressing.
	if out, err := tryRawGit(bare.Path(), "rev-parse", "--is-bare-repository"); err == nil {
		t.Fatalf("safe.bareRepository=explicit is not in effect: discovery succeeded and reported %q", strings.TrimSpace(out))
	}

	if err := bare.Fetch(c, vcs.FetchSpec{
		Remote:   source,
		Refspecs: []string{"+refs/heads/*:refs/heads/*"},
	}); err != nil {
		t.Fatalf("Fetch into a bare repository: %v", err)
	}

	if got, err := bare.ResolveCommit(c, "main"); err != nil || got != second {
		t.Fatalf("ResolveCommit(main) = %q, %v; want %q", got, err, second)
	}
	if got, err := bare.HeadBranch(c); err != nil || got != "main" {
		t.Fatalf("HeadBranch = %q, %v; want main", got, err)
	}
	refs, err := bare.ListRefs(c, "refs/heads/")
	if err != nil || len(refs) != 1 || refs[0].Name != "refs/heads/main" {
		t.Fatalf("ListRefs = %+v, %v; want one refs/heads/main", refs, err)
	}
	if _, err := bare.Diff(c, first, second); err != nil {
		t.Fatalf("Diff in a bare repository: %v", err)
	}
	if _, err := bare.ChangedFiles(c, first, second); err != nil {
		t.Fatalf("ChangedFiles in a bare repository: %v", err)
	}
	if data, err := bare.FileAt(c, first, "edited.txt"); err != nil || string(data) != "first version\n" {
		t.Fatalf("FileAt = %q, %v; want %q", data, err, "first version\n")
	}
	remote, err := bare.RemoteRefs(c, source)
	if err != nil || len(remote) == 0 {
		t.Fatalf("RemoteRefs = %+v, %v; want at least one reference", remote, err)
	}

	// The worktree lifecycle is driven from the bare repository too, so it
	// carries the same addressing requirement.
	wtPath := filepath.Join(t.TempDir(), "copy")
	wt, err := bare.AddWorktree(c, vcs.WorktreeSpec{Path: wtPath, Commit: "main"})
	if err != nil {
		t.Fatalf("AddWorktree from a bare repository: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt.Path(), "edited.txt")); err != nil {
		t.Fatalf("worktree was not checked out: %v", err)
	}
	if _, err := wt.HeadBranch(c); !errors.Is(err, vcs.ErrDetachedHead) {
		t.Fatalf("HeadBranch of a detached worktree = %v; want ErrDetachedHead", err)
	}
	list, err := bare.ListWorktrees(c)
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if !hasWorktree(list, wt.Path()) {
		t.Fatalf("ListWorktrees = %+v; want an entry for %s", list, wt.Path())
	}
	if err := bare.RemoveWorktree(c, wtPath); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatalf("worktree directory still present after RemoveWorktree: %v", err)
	}
}

func hasWorktree(list []vcs.WorktreeInfo, path string) bool {
	for _, w := range list {
		if sameFile(w.Path, path) {
			return true
		}
	}
	return false
}

// sameFile compares two paths that may differ only by a symbolic link in a
// temporary directory prefix, which is what macOS does with /var and /tmp.
func sameFile(a, b string) bool {
	if a == b {
		return true
	}
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(ai, bi)
}

func TestChangedFilesCoversAddModifyDeleteAndRename(t *testing.T) {
	gitEnvironment(t)
	c := ctx(t)
	source, first, second := sourceRepo(t)

	repo, err := vcs.OpenWorktree(c, source)
	if err != nil {
		t.Fatalf("OpenWorktree: %v", err)
	}
	changes, err := repo.ChangedFiles(c, first, second)
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })

	want := []vcs.FileChange{
		{Status: vcs.StatusAdded, Path: "added.txt"},
		{Status: vcs.StatusRenamed, Path: "arrived.txt", OldPath: "moved.txt", Similarity: 100},
		{Status: vcs.StatusModified, Path: "edited.txt"},
		{Status: vcs.StatusDeleted, Path: "removed.txt"},
	}
	if len(changes) != len(want) {
		t.Fatalf("ChangedFiles = %+v; want %+v", changes, want)
	}
	for i := range want {
		if changes[i] != want[i] {
			t.Errorf("change %d = %+v; want %+v", i, changes[i], want[i])
		}
	}

	// The reverse comparison must classify the same pair the other way round,
	// so a deletion is not simply whatever the second argument lacks.
	reverse, err := repo.ChangedFiles(c, second, first)
	if err != nil {
		t.Fatalf("ChangedFiles reversed: %v", err)
	}
	if !hasChange(reverse, vcs.FileChange{Status: vcs.StatusAdded, Path: "removed.txt"}) {
		t.Errorf("reversed comparison = %+v; want removed.txt added", reverse)
	}
	if !hasChange(reverse, vcs.FileChange{Status: vcs.StatusDeleted, Path: "added.txt"}) {
		t.Errorf("reversed comparison = %+v; want added.txt deleted", reverse)
	}

	diff, err := repo.Diff(c, first, second)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	for _, want := range []string{
		"rename from moved.txt",
		"rename to arrived.txt",
		"+second version",
		"-first version",
		"deleted file",
		"new file",
	} {
		if !strings.Contains(diff, want) {
			t.Errorf("diff does not contain %q:\n%s", want, diff)
		}
	}
	if strings.Contains(diff, "kept, unchanged") {
		t.Errorf("diff mentions an unchanged file:\n%s", diff)
	}
}

// A copy is reported as a copy, with the file it came from, rather than as an
// unrelated addition.
func TestChangedFilesReportsACopy(t *testing.T) {
	gitEnvironment(t)
	c := ctx(t)
	dir := t.TempDir()
	rawGit(t, dir, "init", "--quiet", ".")
	writeFile(t, filepath.Join(dir, "original.txt"), longUniqueContent)
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "--quiet", "-m", "first")
	first := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD"))

	// Git detects a copy when the file it was copied from also changed, so
	// both happen in the same commit.
	writeFile(t, filepath.Join(dir, "original.txt"), longUniqueContent+"and one more line\n")
	writeFile(t, filepath.Join(dir, "duplicate.txt"), longUniqueContent+"and a different line\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "--quiet", "-m", "second")

	repo, err := vcs.OpenWorktree(c, dir)
	if err != nil {
		t.Fatalf("OpenWorktree: %v", err)
	}
	changes, err := repo.ChangedFiles(c, first, "HEAD")
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}
	var copied *vcs.FileChange
	for i := range changes {
		if changes[i].Path == "duplicate.txt" {
			copied = &changes[i]
		}
	}
	if copied == nil {
		t.Fatalf("ChangedFiles = %+v; want an entry for duplicate.txt", changes)
	}
	if copied.Status != vcs.StatusCopied || copied.OldPath != "original.txt" || copied.Similarity == 0 {
		t.Errorf("copy = %+v; want a copy of original.txt with a similarity score", *copied)
	}
}

func hasChange(changes []vcs.FileChange, want vcs.FileChange) bool {
	for _, c := range changes {
		if c == want {
			return true
		}
	}
	return false
}

func TestFileAt(t *testing.T) {
	gitEnvironment(t)
	c := ctx(t)
	source, first, second := sourceRepo(t)
	repo, err := vcs.OpenWorktree(c, source)
	if err != nil {
		t.Fatalf("OpenWorktree: %v", err)
	}

	if data, err := repo.FileAt(c, second, "edited.txt"); err != nil || string(data) != "second version\n" {
		t.Errorf("FileAt(second) = %q, %v; want %q", data, err, "second version\n")
	}
	if data, err := repo.FileAt(c, first, "docs/nested.txt"); err != nil || string(data) != "nested\n" {
		t.Errorf("FileAt(nested) = %q, %v; want %q", data, err, "nested\n")
	}
	if data, err := repo.FileAt(c, first, "removed.txt"); err != nil || string(data) != "goes away\n" {
		t.Errorf("FileAt(first, removed.txt) = %q, %v; want the file as it was", data, err)
	}
	if _, err := repo.FileAt(c, second, "removed.txt"); !errors.Is(err, vcs.ErrPathNotFound) {
		t.Errorf("FileAt of a deleted path = %v; want ErrPathNotFound", err)
	}
	if _, err := repo.FileAt(c, second, "never-existed.txt"); !errors.Is(err, vcs.ErrPathNotFound) {
		t.Errorf("FileAt of an unknown path = %v; want ErrPathNotFound", err)
	}
	if _, err := repo.FileAt(c, "no-such-revision", "edited.txt"); !errors.Is(err, vcs.ErrRefNotFound) {
		t.Errorf("FileAt at an unknown revision = %v; want ErrRefNotFound", err)
	}
	// A directory resolves but is not a blob, which is documented as a
	// *CommandError rather than ErrPathNotFound.
	_, err = repo.FileAt(c, first, "docs")
	var cmdErr *vcs.CommandError
	if !errors.As(err, &cmdErr) {
		t.Errorf("FileAt of a directory = %v; want a *CommandError", err)
	}
}

func TestResolveAndCompare(t *testing.T) {
	gitEnvironment(t)
	c := ctx(t)
	source, first, second := sourceRepo(t)
	repo, err := vcs.OpenWorktree(c, source)
	if err != nil {
		t.Fatalf("OpenWorktree: %v", err)
	}

	if got, err := repo.ResolveCommit(c, "HEAD"); err != nil || got != second {
		t.Errorf("ResolveCommit(HEAD) = %q, %v; want %q", got, err, second)
	}
	if _, err := repo.ResolveCommit(c, "no-such-revision"); !errors.Is(err, vcs.ErrRefNotFound) {
		t.Errorf("ResolveCommit of an unknown revision = %v; want ErrRefNotFound", err)
	}

	// Both answers matter: the accepting one and the refusing one.
	if ok, err := repo.IsAncestor(c, first, second); err != nil || !ok {
		t.Errorf("IsAncestor(first, second) = %v, %v; want true", ok, err)
	}
	if ok, err := repo.IsAncestor(c, second, first); err != nil || ok {
		t.Errorf("IsAncestor(second, first) = %v, %v; want false", ok, err)
	}
	if ok, err := repo.IsAncestor(c, second, second); err != nil || !ok {
		t.Errorf("IsAncestor(second, second) = %v, %v; want true", ok, err)
	}
	if got, err := repo.MergeBase(c, first, second); err != nil || got != first {
		t.Errorf("MergeBase(first, second) = %q, %v; want %q", got, err, first)
	}

	// An unrelated history shares no ancestor, and that is an answer rather
	// than a command failure.
	rawGit(t, source, "checkout", "--quiet", "--orphan", "unrelated")
	rawGit(t, source, "rm", "-rq", "--cached", ".")
	writeFile(t, filepath.Join(source, "alone.txt"), "alone\n")
	rawGit(t, source, "add", "-A")
	rawGit(t, source, "commit", "--quiet", "-m", "unrelated")
	if _, err := repo.MergeBase(c, "unrelated", second); !errors.Is(err, vcs.ErrNoMergeBase) {
		t.Errorf("MergeBase of unrelated histories = %v; want ErrNoMergeBase", err)
	}
}

func TestRefsAndRemoteRefsPeelAnnotatedTags(t *testing.T) {
	gitEnvironment(t)
	c := ctx(t)
	source, first, _ := sourceRepo(t)
	rawGit(t, source, "tag", "-a", "v1", "-m", "release one", first)
	rawGit(t, source, "tag", "light", first)

	repo, err := vcs.OpenWorktree(c, source)
	if err != nil {
		t.Fatalf("OpenWorktree: %v", err)
	}

	refs, err := repo.ListRefs(c, "refs/tags/")
	if err != nil {
		t.Fatalf("ListRefs: %v", err)
	}
	byName := map[string]vcs.Ref{}
	for _, r := range refs {
		byName[r.Name] = r
	}
	annotated, ok := byName["refs/tags/v1"]
	if !ok {
		t.Fatalf("ListRefs = %+v; want refs/tags/v1", refs)
	}
	if annotated.Commit != first {
		t.Errorf("annotated tag Commit = %q; want the tagged commit %q", annotated.Commit, first)
	}
	if annotated.Object == first {
		t.Errorf("annotated tag Object = %q; want the tag object, not the commit", annotated.Object)
	}
	light, ok := byName["refs/tags/light"]
	if !ok {
		t.Fatalf("ListRefs = %+v; want refs/tags/light", refs)
	}
	if light.Object != first || light.Commit != first {
		t.Errorf("lightweight tag = %+v; want both fields to be %q", light, first)
	}

	remote, err := repo.RemoteRefs(c, source, "refs/tags/*")
	if err != nil {
		t.Fatalf("RemoteRefs: %v", err)
	}
	found := false
	for _, r := range remote {
		if r.Name == "refs/tags/v1" {
			found = true
			if r.Commit != first {
				t.Errorf("RemoteRefs annotated tag Commit = %q; want %q", r.Commit, first)
			}
			if r.Object == first {
				t.Errorf("RemoteRefs annotated tag Object = %q; want the tag object", r.Object)
			}
		}
	}
	if !found {
		t.Errorf("RemoteRefs = %+v; want refs/tags/v1", remote)
	}
}

func TestSetRemoteAddsThenUpdates(t *testing.T) {
	gitEnvironment(t)
	c := ctx(t)
	source, _, _ := sourceRepo(t)
	repo, err := vcs.OpenWorktree(c, source)
	if err != nil {
		t.Fatalf("OpenWorktree: %v", err)
	}

	if _, err := repo.RemoteURL(c, "assistant"); !errors.Is(err, vcs.ErrRemoteNotFound) {
		t.Fatalf("RemoteURL before the remote exists = %v; want ErrRemoteNotFound", err)
	}
	if err := repo.SetRemote(c, "assistant", "/tmp/one.git"); err != nil {
		t.Fatalf("SetRemote adding: %v", err)
	}
	if got, err := repo.RemoteURL(c, "assistant"); err != nil || got != "/tmp/one.git" {
		t.Fatalf("RemoteURL = %q, %v; want /tmp/one.git", got, err)
	}
	if err := repo.SetRemote(c, "assistant", "/tmp/two.git"); err != nil {
		t.Fatalf("SetRemote updating: %v", err)
	}
	if got, err := repo.RemoteURL(c, "assistant"); err != nil || got != "/tmp/two.git" {
		t.Fatalf("RemoteURL after update = %q, %v; want /tmp/two.git", got, err)
	}
}

func TestWorktreeRemovalRefusesWhenItWouldDiscardWork(t *testing.T) {
	gitEnvironment(t)
	c := ctx(t)
	source, _, second := sourceRepo(t)
	repo, err := vcs.OpenWorktree(c, source)
	if err != nil {
		t.Fatalf("OpenWorktree: %v", err)
	}
	wtPath := filepath.Join(t.TempDir(), "copy")
	wt, err := repo.AddWorktree(c, vcs.WorktreeSpec{Path: wtPath, Commit: second, Branch: "task/one"})
	if err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	if got, err := wt.HeadBranch(c); err != nil || got != "task/one" {
		t.Fatalf("HeadBranch of the new worktree = %q, %v; want task/one", got, err)
	}

	writeFile(t, filepath.Join(wtPath, "unsaved.txt"), "work nobody has seen\n")
	if err := repo.RemoveWorktree(c, wtPath); err == nil {
		t.Fatalf("RemoveWorktree removed a worktree holding untracked work")
	}
	if _, err := os.Stat(filepath.Join(wtPath, "unsaved.txt")); err != nil {
		t.Fatalf("the refused removal took the file anyway: %v", err)
	}
	if err := repo.RemoveWorktreeDiscardingChanges(c, wtPath); err != nil {
		t.Fatalf("RemoveWorktreeDiscardingChanges: %v", err)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatalf("worktree directory still present: %v", err)
	}
}

func TestOpenRefusesTheWrongKindOfPath(t *testing.T) {
	gitEnvironment(t)
	c := ctx(t)
	source, _, _ := sourceRepo(t)
	barePath := filepath.Join(t.TempDir(), "gate.git")
	if _, err := vcs.InitBare(c, barePath); err != nil {
		t.Fatalf("InitBare: %v", err)
	}

	if _, err := vcs.OpenBare(c, source); !errors.Is(err, vcs.ErrNotARepository) {
		t.Errorf("OpenBare on a working copy = %v; want ErrNotARepository", err)
	}
	if _, err := vcs.OpenWorktree(c, barePath); !errors.Is(err, vcs.ErrNotARepository) {
		t.Errorf("OpenWorktree on a bare repository = %v; want ErrNotARepository", err)
	}
	if _, err := vcs.OpenBare(c, t.TempDir()); !errors.Is(err, vcs.ErrNotARepository) {
		t.Errorf("OpenBare on an empty directory = %v; want ErrNotARepository", err)
	}
	if _, err := vcs.OpenBare(c, filepath.Join(t.TempDir(), "absent.git")); !errors.Is(err, vcs.ErrNotARepository) {
		t.Errorf("OpenBare on a path that does not exist = %v; want ErrNotARepository", err)
	}
	// The accepting path, so this test cannot pass by refusing everything.
	if _, err := vcs.OpenBare(c, barePath); err != nil {
		t.Errorf("OpenBare on a bare repository = %v; want it to succeed", err)
	}
	if _, err := vcs.OpenWorktree(c, source); err != nil {
		t.Errorf("OpenWorktree on a working copy = %v; want it to succeed", err)
	}
}

func TestInitBareRepeatsAndRefusesAnOccupiedPath(t *testing.T) {
	gitEnvironment(t)
	c := ctx(t)
	source, _, _ := sourceRepo(t)
	barePath := filepath.Join(t.TempDir(), "gate.git")

	first, err := vcs.InitBare(c, barePath)
	if err != nil {
		t.Fatalf("InitBare: %v", err)
	}
	// Repeating it is how a home is repaired, so it has to succeed.
	if _, err := vcs.InitBare(c, barePath); err != nil {
		t.Fatalf("InitBare repeated: %v", err)
	}
	if got, err := first.HeadBranch(c); err != nil || got != "main" {
		t.Errorf("HeadBranch after a repeated InitBare = %q, %v; want main", got, err)
	}

	// An empty directory is not an obstacle.
	empty := filepath.Join(t.TempDir(), "empty.git")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := vcs.InitBare(c, empty); err != nil {
		t.Errorf("InitBare over an empty directory = %v; want it to succeed", err)
	}

	// A working copy is. Writing a bare repository into it would leave one
	// directory that is two repositories.
	if _, err := vcs.InitBare(c, source); !errors.Is(err, vcs.ErrNotARepository) {
		t.Errorf("InitBare over a working copy = %v; want ErrNotARepository", err)
	}
	if _, err := os.Stat(filepath.Join(source, "HEAD")); !os.IsNotExist(err) {
		t.Errorf("the refused InitBare wrote into the working copy anyway: %v", err)
	}
}

func TestArgumentsThatGitWouldReadAsOptionsAreRefused(t *testing.T) {
	gitEnvironment(t)
	c := ctx(t)
	source, first, _ := sourceRepo(t)
	repo, err := vcs.OpenWorktree(c, source)
	if err != nil {
		t.Fatalf("OpenWorktree: %v", err)
	}

	cases := map[string]func() error{
		"revision": func() error { _, err := repo.ResolveCommit(c, "--upload-pack=touch"); return err },
		"empty revision": func() error {
			_, err := repo.ResolveCommit(c, "")
			return err
		},
		"remote":  func() error { return repo.Fetch(c, vcs.FetchSpec{Remote: "--upload-pack=touch"}) },
		"refspec": func() error { return repo.Fetch(c, vcs.FetchSpec{Remote: source, Refspecs: []string{"--exec=touch"}}) },
		"pattern": func() error { _, err := repo.ListRefs(c, "--sort=-x"); return err },
		"path":    func() error { _, err := repo.FileAt(c, first, "-x"); return err },
		"remote name": func() error {
			_, err := repo.RemoteURL(c, "-x")
			return err
		},
		"worktree path": func() error {
			_, err := repo.AddWorktree(c, vcs.WorktreeSpec{Path: "", Commit: first})
			return err
		},
	}
	for name, call := range cases {
		if err := call(); !errors.Is(err, vcs.ErrInvalidArgument) {
			t.Errorf("%s: got %v; want ErrInvalidArgument", name, err)
		}
	}

	// The accepting path. A path whose name merely contains a dash is fine.
	writeFile(t, filepath.Join(source, "has-a-dash.txt"), "fine\n")
	rawGit(t, source, "add", "-A")
	rawGit(t, source, "commit", "--quiet", "-m", "dash")
	if data, err := repo.FileAt(c, "HEAD", "has-a-dash.txt"); err != nil || string(data) != "fine\n" {
		t.Errorf("FileAt of a path containing a dash = %q, %v; want it to succeed", data, err)
	}
}

func TestOutputBeyondTheLimitIsRefusedWholeRatherThanTruncated(t *testing.T) {
	gitEnvironment(t)
	c := ctx(t)
	source, first, second := sourceRepo(t)

	// The limit is above a commit identifier so revision resolution still
	// succeeds, and below the diff, so the refusal is about the diff.
	small, err := vcs.OpenWorktree(c, source, vcs.WithMaxOutput(100))
	if err != nil {
		t.Fatalf("OpenWorktree: %v", err)
	}
	got, err := small.Diff(c, first, second)
	if !errors.Is(err, vcs.ErrOutputTooLarge) {
		t.Fatalf("Diff under a small limit = %v; want ErrOutputTooLarge", err)
	}
	if got != "" {
		t.Errorf("Diff returned %d bytes alongside the refusal; want nothing", len(got))
	}

	// The accepting path, with the same repository and the same diff.
	large, err := vcs.OpenWorktree(c, source, vcs.WithMaxOutput(1<<20))
	if err != nil {
		t.Fatalf("OpenWorktree: %v", err)
	}
	if got, err := large.Diff(c, first, second); err != nil || got == "" {
		t.Fatalf("Diff under a large limit = %d bytes, %v; want the diff", len(got), err)
	}
}

// A fetch that needs a credential must fail rather than wait for one. The
// server answers every request with a challenge, which is what makes git ask.
func TestFetchNeedingACredentialFailsInsteadOfWaiting(t *testing.T) {
	gitEnvironment(t)
	source, _, _ := sourceRepo(t)
	repo, err := vcs.OpenWorktree(context.Background(), source)
	if err != nil {
		t.Fatalf("OpenWorktree: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	remote, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse the test server URL: %v", err)
	}
	remote.Path = "/repo.git"
	// A username with no password is what makes git go looking for one.
	remote.User = url.User("someone")

	c, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	start := time.Now()
	err = repo.Fetch(c, vcs.FetchSpec{Remote: remote.String(), Refspecs: []string{"+refs/heads/*:refs/heads/probe/*"}})
	if err == nil {
		t.Fatal("Fetch against a server demanding a credential succeeded")
	}
	if c.Err() != nil {
		t.Fatalf("Fetch waited for the deadline instead of failing after %s: %v", time.Since(start), err)
	}
	var cmdErr *vcs.CommandError
	if !errors.As(err, &cmdErr) {
		t.Fatalf("Fetch error = %v; want a *CommandError", err)
	}
}

func TestCredentialsInAURLDoNotReachTheError(t *testing.T) {
	gitEnvironment(t)
	c := ctx(t)
	source, _, _ := sourceRepo(t)
	repo, err := vcs.OpenWorktree(c, source)
	if err != nil {
		t.Fatalf("OpenWorktree: %v", err)
	}

	const password = "hunter2-should-never-appear"
	remote := "https://someone:" + password + "@127.0.0.1:1/repo.git"
	err = repo.Fetch(c, vcs.FetchSpec{Remote: remote, Refspecs: []string{"+refs/heads/*:refs/heads/probe/*"}})
	if err == nil {
		t.Fatal("Fetch from an unreachable remote succeeded")
	}
	var cmdErr *vcs.CommandError
	if !errors.As(err, &cmdErr) {
		t.Fatalf("Fetch error = %v; want a *CommandError", err)
	}
	if strings.Contains(err.Error(), password) {
		t.Errorf("the error message carries the password: %v", err)
	}
	joined := strings.Join(cmdErr.Args, " ")
	if strings.Contains(joined, password) {
		t.Errorf("the recorded arguments carry the password: %s", joined)
	}
	if !strings.Contains(joined, "REDACTED") {
		t.Errorf("the recorded arguments do not show the credential was removed: %s", joined)
	}
	// The operation is named, so a reader knows which git call failed.
	if cmdErr.Op != "fetch" {
		t.Errorf("CommandError.Op = %q; want fetch", cmdErr.Op)
	}

	// A caller that supplies its own Redactor gets it applied instead.
	custom, err := vcs.OpenWorktree(c, source, vcs.WithRedactor(vcs.RedactorFunc(func(s string) string {
		return strings.ReplaceAll(s, password, "«removed»")
	})))
	if err != nil {
		t.Fatalf("OpenWorktree with a redactor: %v", err)
	}
	err = custom.Fetch(c, vcs.FetchSpec{Remote: remote, Refspecs: []string{"+refs/heads/*:refs/heads/probe/*"}})
	if !errors.As(err, &cmdErr) {
		t.Fatalf("Fetch error = %v; want a *CommandError", err)
	}
	if joined := strings.Join(cmdErr.Args, " "); !strings.Contains(joined, "«removed»") {
		t.Errorf("the supplied redactor was not used: %s", joined)
	}
}
