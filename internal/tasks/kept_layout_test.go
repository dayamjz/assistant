package tasks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/forge/forgetest"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/safety"
	"github.com/dayamjz/assistant/internal/store"
)

// keptWithWork is a delivery task's clone kept for a commit of its own and a
// file no commit records, with its entry and the loss shown.
func keptWithWork(t *testing.T) (*cleanupSubject, task, store.KeptWork, Loss) {
	t.Helper()
	s := newCleanupSubject(t, forgetest.Script{})
	ctx := context.Background()
	tk := s.deliveryTask(t, "task/aside")
	commit(t, tk.copy, "own.txt", "the task's own work\n", "the task's own commit")
	_, err := s.cleanUp(t, tk.id)
	kept, ok := KeptOf(refusedWith(t, err, ReasonUnproven))
	if !ok {
		t.Fatal("an unproven refusal is not kept work")
	}
	got, err := s.cleaner.Keep(ctx, kept)
	if err != nil || !got.Entry.Established() {
		t.Fatalf("Keep = %+v, %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(tk.copy, "scratch.txt"), []byte("not committed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	shown, err := s.cleaner.Loss(ctx, got.Entry, "")
	if err != nil {
		t.Fatal(err)
	}
	return s, tk, got.Entry, shown
}

// A discard's preview, and a land, read a task's clone only in the one
// layout they support, round 25's direction after rounds 24 and 25 each found another
// layout the set-aside mishandled: a .git that is a directory of the
// clone's own with no symbolic link anywhere in it, no commondir file and
// no linked worktree, git reading the clone as its own working tree,
// core.symlinks true, no sparse checkout, and references in files. Each layout below is made once the
// clone is kept: .git a symbolic link to another clone's, or a gitdir file
// naming it (round 24); a commondir file naming the other's, a
// core.worktree naming the directory the clone stands in, which moves the
// working tree git reads; .git/refs/heads
// a symbolic link into the other clone, with its objects fetched (round
// 25's first finding, which deleted the other clone's branch); a changed
// tracked symbolic link under core.symlinks=false (round 25's second,
// whose restore could not tell git's checkout from somebody's file); a
// directory of the working tree a link to another checkout (round 28,
// refused before anything is set aside); a sparse checkout; a linked worktree; and references in a reftable. Each
// preview refuses with ErrLayoutUnsupported naming why, with the clone and
// the other clone exactly as they were. The positive control is the same
// clone in the ordinary layout, which is shown. (The removal a confirmed
// discard would make is deferred to AGE-486; the layouts were found
// against it, and a land reads the copy the same way.)
func TestALossRefusesALayoutItDoesNotSupport(t *testing.T) {
	principles.Cite(t, principles.P6)
	ctx := context.Background()
	for _, c := range []struct {
		name, reason string
		make         func(t *testing.T, copyPath, other string)
	}{
		{"the ordinary layout, the positive control", "", nil},
		{".git a symbolic link", "has no repository directory of its own", func(t *testing.T, copyPath, other string) {
			if err := os.RemoveAll(filepath.Join(copyPath, ".git")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(other, ".git"), filepath.Join(copyPath, ".git")); err != nil {
				t.Fatal(err)
			}
		}},
		{".git a gitdir file", "has no repository directory of its own", func(t *testing.T, copyPath, other string) {
			if err := os.RemoveAll(filepath.Join(copyPath, ".git")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(copyPath, ".git"), []byte("gitdir: "+filepath.Join(other, ".git")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"a commondir file naming its own repository", "has a commondir file", func(t *testing.T, copyPath, _ string) {
			// Naming the clone's own .git, git reads the same shared
			// directory, so the verification passes and it is the layout
			// that refuses the file.
			if err := os.WriteFile(filepath.Join(copyPath, ".git", "commondir"), []byte(filepath.Join(copyPath, ".git")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"a commondir file naming another repository", "is read by git as sharing the repository directory", func(t *testing.T, copyPath, other string) {
			// Naming another repository, the verification refuses it
			// before the layout is read (round 44).
			if err := os.WriteFile(filepath.Join(copyPath, ".git", "commondir"), []byte(filepath.Join(other, ".git")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"a core.worktree", "is read by git as part of the working tree", func(t *testing.T, copyPath, _ string) {
			rawGit(t, copyPath, "config", "core.worktree", filepath.Dir(copyPath))
		}},
		{"refs/heads a symbolic link into another clone", "has a symbolic link in its repository directory", func(t *testing.T, copyPath, other string) {
			rawGit(t, copyPath, "fetch", "-q", other, "only-here")
			heads := filepath.Join(copyPath, ".git", "refs", "heads")
			if err := os.RemoveAll(heads); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(other, ".git", "refs", "heads"), heads); err != nil {
				t.Fatal(err)
			}
		}},
		{"core.symlinks false with a tracked link changed", "sets core.symlinks false", func(t *testing.T, copyPath, _ string) {
			if err := os.Symlink("own.txt", filepath.Join(copyPath, "link")); err != nil {
				t.Fatal(err)
			}
			rawGit(t, copyPath, "add", "link")
			rawGit(t, copyPath, "commit", "--quiet", "-m", "a link")
			if err := os.Remove(filepath.Join(copyPath, "link")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("scratch.txt", filepath.Join(copyPath, "link")); err != nil {
				t.Fatal(err)
			}
			rawGit(t, copyPath, "config", "core.symlinks", "false")
		}},
		{"a directory of the working tree a link to another checkout", "has a symbolic link a path inside it could pass through", func(t *testing.T, copyPath, other string) {
			if err := os.Symlink(other, filepath.Join(copyPath, "linked")); err != nil {
				t.Fatal(err)
			}
		}},
		{"a sparse checkout", "is a sparse checkout", func(t *testing.T, copyPath, _ string) {
			rawGit(t, copyPath, "sparse-checkout", "set", "--no-cone", "/own.txt")
		}},
		{"a linked worktree", "has a linked worktree", func(t *testing.T, copyPath, _ string) {
			rawGit(t, copyPath, "worktree", "add", "--quiet", "--detach", filepath.Join(t.TempDir(), "linked"))
		}},
		{"a shallow history", "is shallow", func(t *testing.T, copyPath, _ string) {
			rawGit(t, copyPath, "fetch", "-q", "--depth=1", ".", "HEAD")
		}},
		{"a graft file", "has a graft file", func(t *testing.T, copyPath, _ string) {
			head := rawGit(t, copyPath, "rev-parse", "HEAD")
			if err := os.MkdirAll(filepath.Join(copyPath, ".git", "info"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(copyPath, ".git", "info", "grafts"), []byte(head+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"a replacement reference", "has replacement references", func(t *testing.T, copyPath, _ string) {
			rawGit(t, copyPath, "replace", rawGit(t, copyPath, "rev-parse", "HEAD"), rawGit(t, copyPath, "rev-parse", "HEAD~1"))
		}},
		{"a promisor remote, as a partial clone records it", "is a partial clone", func(t *testing.T, copyPath, _ string) {
			remote := strings.Fields(rawGit(t, copyPath, "remote"))[0]
			rawGit(t, copyPath, "config", "remote."+remote+".promisor", "true")
			rawGit(t, copyPath, "config", "remote."+remote+".partialclonefilter", "blob:none")
		}},
		{"extensions.partialClone, as an older git records a partial clone", "is a partial clone", func(t *testing.T, copyPath, _ string) {
			rawGit(t, copyPath, "config", "extensions.partialClone", strings.Fields(rawGit(t, copyPath, "remote"))[0])
		}},
		{"a promisor pack", "is a partial clone", func(t *testing.T, copyPath, other string) {
			// A real partial clone of the other clone, whose promisor packs
			// are then the copy's: the objects such a pack holds may lack
			// what they name.
			rawGit(t, other, "config", "uploadpack.allowFilter", "true")
			partial := filepath.Join(t.TempDir(), "partial")
			rawGit(t, filepath.Dir(partial), "clone", "-q", "--filter=blob:none", "file://"+other, partial)
			packs, err := filepath.Glob(filepath.Join(partial, ".git", "objects", "pack", "pack-*"))
			if err != nil || len(packs) == 0 {
				t.Fatalf("the partial clone has packs %v, %v", packs, err)
			}
			for _, pack := range packs {
				raw, err := os.ReadFile(pack)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(copyPath, ".git", "objects", "pack", filepath.Base(pack)), raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{"references in a reftable", "keeps its references in a reftable", func(t *testing.T, copyPath, _ string) {
			if out, err := rawGitErr(copyPath, "refs", "migrate", "--ref-format=reftable"); err != nil {
				t.Skipf("this git cannot move references into a reftable: %v\n%s", err, out)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, tk, entry, _ := keptWithWork(t)
			other := filepath.Join(t.TempDir(), "other")
			rawGit(t, filepath.Dir(other), "clone", "-q", tk.copy, other)
			rawGit(t, other, "switch", "-q", "-c", "only-here")
			commit(t, other, "only-here.txt", "the other clone's own work\n", "the other clone's own commit")
			if c.make == nil {
				if _, err := s.cleaner.Loss(ctx, entry, ""); err != nil {
					t.Fatalf("the preview of the clone in the ordinary layout: %v", err)
				}
				return
			}
			c.make(t, tk.copy, other)
			beforeCopy, beforeOther := treeAndGitState(t, tk.copy), treeAndGitState(t, other)
			if _, err := s.cleaner.Loss(ctx, entry, ""); !errors.Is(err, ErrLayoutUnsupported) || !refusedFor(err, tk.copy, c.reason) {
				t.Errorf("the preview of a clone with %s = %v; want ErrLayoutUnsupported saying it %s", c.name, err, c.reason)
			}
			if now := treeAndGitState(t, tk.copy); now != beforeCopy {
				t.Errorf("the clone reads\n%s\nafter the refusal; want it as it was\n%s", now, beforeCopy)
			}
			if now := treeAndGitState(t, other); now != beforeOther {
				t.Errorf("the other clone reads\n%s\nafter the refusal; want it as it was\n%s", now, beforeOther)
			}
		})
	}
}

// refusedFor reports whether err, a refusal of the copy at copyPath, says
// the copy why: the words right after the copy's path, never a path that
// happens to hold them, as a test's directory named after the case does.
func refusedFor(err error, copyPath, why string) bool {
	_, after, ok := strings.Cut(err.Error(), copyPath+" ")
	return ok && strings.HasPrefix(after, why)
}

// treeAndGitState is every entry under copyPath, its .git included, with
// what it holds, read from the file system alone so a layout git reads
// strangely is still read as it stands.
func treeAndGitState(t *testing.T, copyPath string) string {
	t.Helper()
	state, err := readCopyState(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for p, e := range state {
		lines = append(lines, fmt.Sprintf("%s %v %s", strings.TrimPrefix(p, copyPath), e.mode, e.held))
	}
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}

// A discard's preview never leaves out a commit of a shallow history,
// round 32's blocker, the reviewer's sequence: a kept clone's
// branch holds B, which adds work, and U, which takes it out again and
// adds a file of its own, so the loss read with the full history lacks
// both. The clone is then made
// shallow at U (a fetch of its own HEAD at depth 1, its reflogs expired),
// so git reads U as a root while B stands in its objects, still U's
// recorded parent. Its preview is refused, and the clone stands as it
// was, B in it. Before, the preview lacked U alone, and the discard
// confirmed on it removed the clone, B with it, which nobody was shown.
func TestAShallowCloneIsRefusedBeforeItsPreviewLeavesOutACommit(t *testing.T) {
	principles.Cite(t, principles.P6)
	ctx := context.Background()
	s := newCleanupSubject(t, forgetest.Script{})
	tk := s.deliveryTask(t, "task/shallow")
	b := commit(t, tk.copy, "work.txt", "work only B holds\n", "B adds work")
	rawGit(t, tk.copy, "rm", "-q", "work.txt")
	if err := os.WriteFile(filepath.Join(tk.copy, "u.txt"), []byte("U's own\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rawGit(t, tk.copy, "add", "u.txt")
	rawGit(t, tk.copy, "commit", "-q", "-m", "U takes it out again, adding its own")
	u := rawGit(t, tk.copy, "rev-parse", "HEAD")
	_, err := s.cleanUp(t, tk.id)
	kept, ok := KeptOf(refusedWith(t, err, ReasonUnproven))
	if !ok {
		t.Fatal("an unproven refusal is not kept work")
	}
	got, err := s.cleaner.Keep(ctx, kept)
	if err != nil || !got.Entry.Established() {
		t.Fatalf("Keep = %+v, %v", got, err)
	}
	full, err := s.cleaner.Loss(ctx, got.Entry, "")
	if err != nil || !slices.Contains(full.Lacks, b) || !slices.Contains(full.Lacks, u) {
		t.Fatalf("the loss of the full history = %+v, %v; want B %s and U %s, the positive control", full.Lacks, err, b, u)
	}

	rawGit(t, tk.copy, "fetch", "-q", "--depth=1", ".", "HEAD")
	rawGit(t, tk.copy, "reflog", "expire", "--expire=now", "--all")
	if parent := rawGit(t, tk.copy, "cat-file", "-p", u); !strings.Contains(parent, "parent "+b) {
		t.Fatalf("U no longer records B as its parent:\n%s", parent)
	}
	before := treeAndGitState(t, tk.copy)
	if shallow, err := s.cleaner.Loss(ctx, got.Entry, ""); !errors.Is(err, ErrLayoutUnsupported) || !refusedFor(err, tk.copy, "is shallow") {
		t.Fatalf("the preview of the shallow clone = %+v, %v; want ErrLayoutUnsupported saying it is shallow", shallow.Lacks, err)
	}
	if now := treeAndGitState(t, tk.copy); now != before {
		t.Fatalf("the shallow clone reads\n%s\nafter the refusal; want it as it was\n%s", now, before)
	}
	if kind := rawGit(t, tk.copy, "cat-file", "-t", b); kind != "commit" {
		t.Fatalf("B reads as %q in the clone after the refusal; want it still there", kind)
	}
}

// Git must read a task's clone through the clone's own .git and working
// tree, which git itself is asked, rounds 26 and 27: the directory a kept
// clone stands in is a checkout of its own, and the clone's .git loses
// its HEAD,
// so git takes the clone for part of that parent checkout. In the first
// case git reads the parent's working tree; in the second, the parent's
// core.worktree names the clone and the parent holds the clone's objects,
// so git reports the clone as its working tree and reads the parent's
// .git (round 27's sequence). The preview refuses with
// ErrLayoutUnsupported, with the parent's repository and the clone exactly
// as they were. Before, the layout checks passed, in round 26 on the file
// system alone and in round 27 on the working tree alone, and a discard's
// put-back wrote the clone's branch and file into the parent's repository
// and index.
func TestAClonesRepositoryIsTheOneGitReadsForIt(t *testing.T) {
	principles.Cite(t, principles.P6)
	ctx := context.Background()
	for _, c := range []struct {
		name    string
		prepare func(t *testing.T, parent, copyPath string)
	}{
		{"the parent's working tree", func(t *testing.T, parent, copyPath string) {}},
		{"the parent's core.worktree naming the clone", func(t *testing.T, parent, copyPath string) {
			rawGit(t, parent, "fetch", "--quiet", copyPath, "+refs/*:refs/fetched/*")
			rawGit(t, parent, "config", "core.worktree", copyPath)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, tk, entry, _ := keptWithWork(t)
			parent := filepath.Dir(tk.copy)
			rawGit(t, parent, "init", "--quiet", ".")
			if err := os.WriteFile(filepath.Join(parent, "parent.txt"), []byte("the parent's own\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			rawGit(t, parent, "add", "parent.txt")
			rawGit(t, parent, "commit", "--quiet", "-m", "the parent's own commit")
			c.prepare(t, parent, tk.copy)
			if err := os.Remove(filepath.Join(tk.copy, ".git", "HEAD")); err != nil {
				t.Fatal(err)
			}
			if dir := rawGit(t, tk.copy, "rev-parse", "--absolute-git-dir"); dir == filepath.Join(tk.copy, ".git") {
				t.Fatalf("git still reads the clone's own .git; the case would test nothing")
			}
			beforeParent := treeAndGitState(t, filepath.Join(parent, ".git"))
			beforeCopy := treeAndGitState(t, tk.copy)
			if _, err := s.cleaner.Loss(ctx, entry, ""); !errors.Is(err, ErrLayoutUnsupported) {
				t.Errorf("the preview of a clone git reads through another checkout = %v; want ErrLayoutUnsupported", err)
			}
			if now := treeAndGitState(t, filepath.Join(parent, ".git")); now != beforeParent {
				t.Errorf("the parent's repository reads\n%s\nwant it as it was\n%s", now, beforeParent)
			}
			if now := treeAndGitState(t, tk.copy); now != beforeCopy {
				t.Errorf("the clone reads\n%s\nwant it as it was\n%s", now, beforeCopy)
			}
		})
	}
}

// A run's worktree reads and writes its objects and references in the gate's
// repository, which its record's commondir file names. A record made to name
// another repository is refused before anything of the copy is read, by the
// verification and so by the loss a discard shows and a land reads, with
// safety.ErrCopyNotVerified under ErrLayoutUnsupported (round 44: both
// passed, and a discard preview fetched into the other repository's object
// database). The positive control is the same worktree with its record as
// the gate wrote it, and git still reads the worktree through the redirected
// record, so it is the binding alone that refuses.
func TestARunsWorktreeRecordedAsSharingAnotherRepositoryIsRefused(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newCleanupSubject(t, forgetest.Script{})
	ctx := context.Background()
	const runID = "run-shared-elsewhere"
	if _, err := s.store.CreateRun(ctx, store.Run{
		ID: runID, RepositoryID: cleanupRepository, Branch: "topic", SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "validate", IntentSource: "push", ConfigDigest: "cfg",
		Build: store.Build{Version: "v0", Revision: "0123456789abcdef", Go: "go1.25.0"},
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	gateRepo := filepath.Join(t.TempDir(), "gate.git")
	rawGit(t, filepath.Dir(gateRepo), "clone", "-q", "--bare", s.upstream, gateRepo)
	copyPath := s.home.Worktree(cleanupRepository, runID)
	if err := os.MkdirAll(filepath.Dir(copyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	rawGit(t, gateRepo, "worktree", "add", "-q", "--detach", copyPath, "main")
	commit(t, copyPath, "left.txt", "left by a body\n", "a body's commit")
	kept, err := s.cleaner.Keep(ctx, Kept{Run: runID, Reason: string(ReasonCopyChanged)})
	if err != nil || !kept.Recorded {
		t.Fatalf("Keep = %+v, %v; want the run's copy kept", kept, err)
	}
	if err := s.cleaner.VerifyKeptCopy(ctx, kept.Entry, gateRepo); err != nil {
		t.Fatalf("VerifyKeptCopy of the worktree as the gate recorded it = %v; want it verified", err)
	}

	// The other repository holds the worktree's commit, so git reads the
	// worktree, HEAD and all, through the redirected record.
	other := filepath.Join(t.TempDir(), "other")
	rawGit(t, filepath.Dir(other), "clone", "-q", gateRepo, other)
	record := rawGit(t, copyPath, "rev-parse", "--absolute-git-dir")
	if err := os.WriteFile(filepath.Join(record, "commondir"), []byte(filepath.Join(other, ".git")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := filepath.EvalSymlinks(rawGit(t, copyPath, "rev-parse", "--git-common-dir"))
	if err != nil {
		t.Fatal(err)
	}
	if want, err := filepath.EvalSymlinks(filepath.Join(other, ".git")); err != nil || got != want {
		t.Fatalf("git reads the worktree as sharing %s, want the other repository %s (%v)", got, want, err)
	}
	err = s.cleaner.VerifyKeptCopy(ctx, kept.Entry, gateRepo)
	if !errors.Is(err, ErrLayoutUnsupported) || !errors.Is(err, safety.ErrCopyNotVerified) ||
		!strings.Contains(err.Error(), "is read by git as sharing the repository directory") {
		t.Fatalf("VerifyKeptCopy of the worktree whose record names another repository = %v; want it refused as not verified", err)
	}
	if _, err := s.cleaner.Loss(ctx, kept.Entry, gateRepo); !errors.Is(err, safety.ErrCopyNotVerified) {
		t.Fatalf("Loss of that worktree = %v; want it refused as not verified before anything is read", err)
	}
}
