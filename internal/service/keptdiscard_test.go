package service_test

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/store"
)

// discardRequest is the discard answer for a run's entry, with or without a
// confirmation.
func discardRequest(entry store.KeptWork, confirm string) machine.KeptWorkAnswerRequest {
	return machine.KeptWorkAnswerRequest{Run: entry.RunID.Or(""), Hold: entry.Key(), Answer: string(store.KeptDiscard), Confirm: confirm}
}

// PRD section 6's discard as this build takes it (docs/prd.md, sections 6
// and 9, the owner's decision of 2026-10-25), through the real socket on a
// run's kept copy, a worktree of the gate. A discard alone shows exactly
// what would be lost, the commit the default branch lacks and the digest
// of the copy's state, naming no path of its working tree, the .git file
// pointing at the gate among them; it records nothing, the entry reads as
// it did and is still listed, and the worktree's files, index, references
// and logs are as they were (its read may add objects it fetches). A
// confirmation, with the digest shown or with another, is refused with the
// row saying the removal is not in this build, before the copy is read:
// the copy, its objects included, and the gate are byte for byte as they
// were, and the entry, field for field but when it last surfaced, still
// open and listed, so it still
// holds its project's new tasks (P6, a refused discard keeps the copy and
// its entry). Before, the confirmation on the digest shown removed the
// copy and answered the entry.
func TestADiscardIsShownAndItsConfirmationRefusedChangingNothing(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)
	submitted := git(t, subject, "rev-parse", "HEAD")
	entry := keptEntryOf(t, h, subject)
	gate := git(t, entry.CopyPath, "rev-parse", "--path-format=absolute", "--git-common-dir")
	record := git(t, entry.CopyPath, "rev-parse", "--absolute-git-dir")

	withService(t, h, func(under serviceUnderTest) {
		ctx := t.Context()
		answer := func(confirm string) (machine.KeptWorkAnswered, error) {
			var out machine.KeptWorkAnswered
			err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, discardRequest(entry, confirm), &out)
			return out, err
		}
		kept, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID)
		if err != nil {
			t.Fatal(err)
		}
		// The supervisor surfaces the entry on its own, which moves
		// SurfacedAt forward and is no part of what an answer changes.
		same := func(a, b store.KeptWork) bool {
			a.SurfacedAt, b.SurfacedAt = store.Optional[time.Time]{}, store.Optional[time.Time]{}
			return reflect.DeepEqual(a, b)
		}
		listed := func() bool {
			open, err := recordsOf(t, h).OpenKeptWork(ctx)
			return err == nil && slices.ContainsFunc(open, func(k store.KeptWork) bool { return k.ID == entry.ID })
		}
		copyBefore, recordBefore := fileTree(t, entry.CopyPath, nil), fileTree(t, record, nil)

		shown, err := answer("")
		if err != nil || shown.Effect != machine.KeptWorkDiscardShown || shown.Loss == nil || shown.Loss.Digest == "" ||
			len(shown.Loss.Lacks) != 1 || shown.Loss.Lacks[0] != submitted || shown.Loss.Head != submitted {
			t.Fatalf("a discard shown = %+v, %v; want what would be lost, the commit %s, with a digest", shown, err, submitted)
		}
		if slices.ContainsFunc(shown.Loss.Files, func(f machine.KeptWorkLostPath) bool { return f.Kind != "repository" }) {
			t.Fatalf("a discard of a worktree holding its commit alone names %+v; want no path of its working tree, "+
				"its .git pointer to the gate among them", shown.Loss.Files)
		}
		if now, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID); err != nil || !same(now, kept) || !listed() {
			t.Fatalf("after the discard was shown the entry reads %+v, %v; want it as it was and listed:\n%+v", now, err, kept)
		}
		if now := fileTree(t, entry.CopyPath, nil); now != copyBefore {
			t.Fatalf("showing the discard changed the worktree:\n%s\nwant\n%s", now, copyBefore)
		}
		if now := fileTree(t, record, nil); now != recordBefore {
			t.Fatalf("showing the discard changed the worktree's record:\n%s\nwant\n%s", now, recordBefore)
		}

		copyShown, gateShown := fileTree(t, entry.CopyPath, nil), fileTree(t, gate, nil)
		for _, confirm := range []string{shown.Loss.Digest, "not-the-digest"} {
			_, err := answer(confirm)
			if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), machine.PhraseDiscardRemovalDeferred.String()) {
				t.Fatalf("a confirmation with %q = %v; want it refused with the row saying removal is not in this build", confirm, err)
			}
			if now := fileTree(t, entry.CopyPath, nil); now != copyShown {
				t.Fatalf("the refused confirmation changed the worktree:\n%s\nwant\n%s", now, copyShown)
			}
			if now := fileTree(t, gate, nil); now != gateShown {
				t.Fatalf("the refused confirmation changed the gate:\n%s\nwant\n%s", now, gateShown)
			}
			if now, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID); err != nil || !same(now, kept) || !now.Open() || !listed() {
				t.Fatalf("after the refused confirmation the entry reads %+v, %v; want it as it was, open and listed:\n%+v", now, err, kept)
			}
		}
	})
}

// fileTree is every entry under root, root itself aside, by its path
// relative to root, its type and permission bits, and for a regular file
// the hash of its bytes or for a link its target, read from the file
// system alone and never through git. keep, when not nil, says which paths
// count.
func fileTree(t *testing.T, root string, keep func(rel string) bool) string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if keep != nil && !keep(filepath.ToSlash(rel)) {
			return nil
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		held := ""
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			held, err = os.Readlink(p)
		case info.Mode().IsRegular():
			var raw []byte
			raw, err = os.ReadFile(p)
			held = fmt.Sprintf("%x", sha256.Sum256(raw))
		}
		if err != nil {
			return err
		}
		lines = append(lines, fmt.Sprintf("%s %v %s", filepath.ToSlash(rel), info.Mode().Type()|info.Mode().Perm(), held))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}

// A discard whose basis cannot be established now is refused naming why,
// shows no list and records nothing, whatever the entry recorded when it
// was kept: the upstream is made unreadable while the copy stands, and the
// discard is refused as not established; with the upstream back, a task's
// clone is shown on the same terms as a run's worktree. Without the
// refusal a basis that cannot be read shows a list that may overstate or
// understate what is lost.
func TestADiscardRefusesAnUnestablishedEntryAndShowsATasksClone(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	upstream := repository.UpstreamURL

	withService(t, h, func(under serviceUnderTest) {
		ctx := t.Context()
		var out machine.KeptWorkAnswered
		request := machine.KeptWorkAnswerRequest{Task: old, Hold: entry.Key(), Answer: string(store.KeptDiscard)}
		if err := os.Rename(upstream, upstream+".gone"); err != nil {
			t.Fatal(err)
		}
		err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &out)
		if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), "not established") {
			t.Fatalf("a discard with the upstream unreadable = %v; want it refused as not established", err)
		}
		if err := os.Rename(upstream+".gone", upstream); err != nil {
			t.Fatal(err)
		}
		if still, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID); err != nil || !still.Open() {
			t.Fatalf("after the refused discard the entry reads %+v, %v; want it open", still, err)
		}
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &out); err != nil || out.Effect != machine.KeptWorkDiscardShown || out.Loss == nil {
			t.Fatalf("the discard shown with the upstream back = %+v, %v", out, err)
		}
	})
}

// advanceUpstream commits content to path on the bare upstream's default
// branch from a clone of its own, the way another person's landing moves
// it, and returns the commit.
func advanceUpstream(t *testing.T, upstream, path, content, message string) string {
	t.Helper()
	clone := t.TempDir()
	git(t, clone, "clone", "--quiet", upstream, ".")
	if err := os.WriteFile(filepath.Join(clone, path), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, clone, "add", "-A")
	git(t, clone, "commit", "--quiet", "-m", message)
	git(t, clone, "push", "--quiet", "origin", "HEAD:refs/heads/main")
	return git(t, clone, "rev-parse", "HEAD")
}

// A discard's digest seals the whole stash list, not the stash's tip, the
// reviewer's sequence on a task's clone, which a discard removes with its
// stash: work is stashed and the stash's tip saved, the discard shown, new
// work stashed over it, and the saved tip stored back on top with git stash
// store, so refs/stash names the commit it named when the discard was shown
// and the new work is the list's second entry. The confirmation with the
// digest shown is refused as changed, the copy stands and the new stash is
// still in it. Without the list in the digest the copy and the stashed work
// are removed on the stale confirmation.
func TestADiscardDigestSealsTheWholeStashList(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	if err := writeFile(entry.CopyPath+"/finding.txt", "the first stashed edit\n"); err != nil {
		t.Fatal(err)
	}
	git(t, entry.CopyPath, "stash", "--quiet")
	saved := git(t, entry.CopyPath, "rev-parse", "refs/stash")

	withService(t, h, func(under serviceUnderTest) {
		ctx := t.Context()
		request := machine.KeptWorkAnswerRequest{Task: old, Hold: entry.Key(), Answer: string(store.KeptDiscard)}
		var shown machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &shown); err != nil || shown.Loss == nil || !shown.Loss.Stash {
			t.Fatalf("the discard shown = %+v, %v; want what would be lost, the stash among it", shown, err)
		}
		if err := writeFile(entry.CopyPath+"/finding.txt", "new work, stashed after the discard was shown\n"); err != nil {
			t.Fatal(err)
		}
		git(t, entry.CopyPath, "stash", "--quiet")
		unseen := git(t, entry.CopyPath, "rev-parse", "refs/stash")
		git(t, entry.CopyPath, "stash", "store", "--quiet", "-m", "the saved tip, stored back", saved)
		if tip := git(t, entry.CopyPath, "rev-parse", "refs/stash"); tip != saved {
			t.Fatalf("the stash's tip is %s, want it back at %s", tip, saved)
		}
		var again machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &again); err != nil || again.Loss == nil ||
			again.Loss.Digest == shown.Loss.Digest || !slices.Contains(again.Loss.Lacks, unseen) {
			t.Fatalf("the discard shown once new work was stashed under the shown tip = %+v, %v; want %s lost and another digest", again, err, unseen)
		}
	})
}

// A discard is shown and confirmed whatever date format the operator's git
// configuration asks git log for: an investigation's copy kept with a
// stash, and log.date set to relative in the copy's own configuration,
// which the program reads as git does, is shown with the stash among what
// would be lost, and the confirmation with the digest shown removes it.
// Were the stash list read in a form that setting changes, the preview and
// the confirmation would both be refused and the copy could never be
// discarded.
func TestADiscardIsShownAndConfirmedWhateverTheOperatorsLogDate(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	if err := writeFile(entry.CopyPath+"/finding.txt", "a stashed edit\n"); err != nil {
		t.Fatal(err)
	}
	git(t, entry.CopyPath, "stash", "--quiet")
	git(t, entry.CopyPath, "config", "log.date", "relative")

	withService(t, h, func(under serviceUnderTest) {
		ctx := t.Context()
		request := machine.KeptWorkAnswerRequest{Task: old, Hold: entry.Key(), Answer: string(store.KeptDiscard)}
		var shown machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &shown); err != nil || shown.Loss == nil || !shown.Loss.Stash {
			t.Fatalf("the discard shown under log.date=relative = %+v, %v; want what would be lost, the stash among it", shown, err)
		}
	})
}

// A discard shows every file it destroys that is nowhere else, a file git
// ignores among them, the reviewer's sequence: an investigation's copy is
// kept, and a file named in the copy's own exclude list, secret.txt, is
// written into it, which no commit, stash or index entry holds. The
// discard shown names it as new, and names the edited exclude list as a
// file of the repository directory, but none of the sample hooks git
// writes into every repository; the confirmation with the digest shown
// removes it with the copy. Without the walk of what the copy holds the
// preview reports no untracked file and names nothing, and the
// confirmation deletes the file unseen.
func TestADiscardShowsAnIgnoredFileItDestroys(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	exclude := filepath.Join(entry.CopyPath, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(exclude), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(exclude, "secret.txt\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(filepath.Join(entry.CopyPath, "secret.txt"), "the only copy of this\n"); err != nil {
		t.Fatal(err)
	}
	if status := git(t, entry.CopyPath, "status", "--porcelain"); status != "" {
		t.Fatalf("git status reads %q; want the file ignored, so git reports nothing", status)
	}

	withService(t, h, func(under serviceUnderTest) {
		ctx := t.Context()
		request := machine.KeptWorkAnswerRequest{Task: old, Hold: entry.Key(), Answer: string(store.KeptDiscard)}
		var shown machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &shown); err != nil || shown.Loss == nil {
			t.Fatalf("the discard shown = %+v, %v", shown, err)
		}
		files := shown.Loss.Files
		for _, want := range []machine.KeptWorkLostPath{{Kind: "new", Path: "secret.txt"}, {Kind: "repository", Path: "info/exclude"}} {
			if !hasLostPath(files, want) {
				t.Fatalf("the discard shown names %+v; want %+v, which the removal destroys and nothing else holds, among them", files, want)
			}
		}
		if slices.ContainsFunc(files, func(f machine.KeptWorkLostPath) bool { return strings.HasPrefix(f.Path, "hooks/") }) {
			t.Fatalf("the discard shown names %+v; want none of the sample hooks git writes into every repository", files)
		}
	})
}

// A discard shows the deletions it destroys, the reviewer's sequence on
// #475: in a kept clone a tracked file is deleted and not staged and
// another is deleted with git rm. The commits HEAD stands on hold both
// files, so their content is not lost, but the deletions are, and nothing
// else holds them; each is named, and confirming the digest shown removes
// the copy. Without the deletions named, the preview says only that the
// copy is dirty and the confirmation destroys work it never named.
func TestADiscardShowsTheDeletionsItDestroys(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	if err := os.Remove(filepath.Join(entry.CopyPath, "finding.txt")); err != nil {
		t.Fatal(err)
	}
	tracked := strings.Fields(git(t, entry.CopyPath, "ls-files"))
	staged := ""
	for _, path := range tracked {
		if path != "finding.txt" {
			staged = path
			break
		}
	}
	if staged == "" {
		t.Fatalf("the copy tracks %q; want a second file to delete with git rm", tracked)
	}
	git(t, entry.CopyPath, "rm", "--quiet", staged)

	withService(t, h, func(under serviceUnderTest) {
		ctx := t.Context()
		request := machine.KeptWorkAnswerRequest{Task: old, Hold: entry.Key(), Answer: string(store.KeptDiscard)}
		var shown machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &shown); err != nil || shown.Loss == nil {
			t.Fatalf("the discard shown = %+v, %v", shown, err)
		}
		for _, want := range []machine.KeptWorkLostPath{{Kind: "deleted", Path: "finding.txt"}, {Kind: "deleted", Path: staged}} {
			if !hasLostPath(shown.Loss.Files, want) {
				t.Fatalf("the discard shown = %+v; want %+v, a deletion the removal destroys, among its files", *shown.Loss, want)
			}
		}
	})
}

// A discard shows the commits it destroys that only a reflog names, the
// reviewer's sequence on #475: the kept clone's worker commits once more
// and resets the commit away, so no reference reaches it and HEAD's reflog
// does, and a file it writes keeps the copy's state moving as the
// reviewer's untracked note did. The commit is lost with the copy and
// nothing else holds it, so it is among the commits shown as lacking, and
// confirming the digest shown removes the copy. Without the reflogs read,
// the preview lists the worker's first commit alone and the confirmation
// destroys the second unshown.
func TestADiscardShowsACommitOnlyAReflogNames(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	if err := writeFile(filepath.Join(entry.CopyPath, "second.txt"), "a second finding\n"); err != nil {
		t.Fatal(err)
	}
	git(t, entry.CopyPath, "add", "second.txt")
	git(t, entry.CopyPath, "commit", "--quiet", "-m", "the worker's second commit")
	reset := git(t, entry.CopyPath, "rev-parse", "HEAD")
	git(t, entry.CopyPath, "reset", "--quiet", "--hard", "HEAD~1")
	if err := writeFile(filepath.Join(entry.CopyPath, "note.txt"), "an untracked note\n"); err != nil {
		t.Fatal(err)
	}

	withService(t, h, func(under serviceUnderTest) {
		ctx := t.Context()
		request := machine.KeptWorkAnswerRequest{Task: old, Hold: entry.Key(), Answer: string(store.KeptDiscard)}
		var shown machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &shown); err != nil || shown.Loss == nil {
			t.Fatalf("the discard shown = %+v, %v", shown, err)
		}
		if !slices.Contains(shown.Loss.Lacks, reset) {
			t.Fatalf("the discard shown = %+v; want %s, the commit only HEAD's reflog names, among the commits lacking", *shown.Loss, reset)
		}
	})
}

// A discard shows a commit only a reflog entry's previous value records,
// the reviewer's sequence on fdf1659: the worker commits once more and
// resets the commit away, and every older entry of HEAD's reflog is
// expired, so the reset's own entry is the one left and the commit is its
// previous value alone, recoverable as HEAD@{1} and named by no reference,
// ORIG_HEAD removed, and by no entry's new value. An untracked note keeps the copy kept. The
// commit is lost with the copy, so it is among the commits shown as lacking.
// Without the reflogs' previous values read, the preview lacked nothing
// and confirming its digest destroyed the commit unshown.
func TestADiscardShowsACommitOnlyAReflogsPreviousValueRecords(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	if err := writeFile(filepath.Join(entry.CopyPath, "second.txt"), "a second finding\n"); err != nil {
		t.Fatal(err)
	}
	git(t, entry.CopyPath, "add", "second.txt")
	git(t, entry.CopyPath, "commit", "--quiet", "-m", "the worker's second commit")
	reset := git(t, entry.CopyPath, "rev-parse", "HEAD")
	git(t, entry.CopyPath, "reset", "--quiet", "--hard", "HEAD~1")
	for range strings.Count(git(t, entry.CopyPath, "log", "--walk-reflogs", "--format=%H", "HEAD"), "\n") {
		git(t, entry.CopyPath, "reflog", "delete", "HEAD@{1}")
	}
	// The reset also left ORIG_HEAD naming the commit; it goes, so the
	// reflog entry's previous value is the one thing that records it.
	git(t, entry.CopyPath, "update-ref", "-d", "ORIG_HEAD")
	if got := git(t, entry.CopyPath, "log", "--walk-reflogs", "--format=%H", "HEAD"); strings.Contains(got, reset) {
		t.Fatalf("HEAD's reflog still names %s as an entry's new value:\n%s", reset, got)
	}
	if got := git(t, entry.CopyPath, "rev-parse", "HEAD@{1}"); got != reset {
		t.Fatalf("HEAD@{1} = %s; want the reset commit %s, the reset entry's previous value", got, reset)
	}
	if err := writeFile(filepath.Join(entry.CopyPath, "note.txt"), "an untracked note\n"); err != nil {
		t.Fatal(err)
	}

	withService(t, h, func(under serviceUnderTest) {
		ctx := t.Context()
		request := machine.KeptWorkAnswerRequest{Task: old, Hold: entry.Key(), Answer: string(store.KeptDiscard)}
		var shown machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &shown); err != nil || shown.Loss == nil {
			t.Fatalf("the discard shown = %+v, %v", shown, err)
		}
		if !slices.Contains(shown.Loss.Lacks, reset) {
			t.Fatalf("the discard shown = %+v; want %s, the commit only a reflog entry's previous value records, among the "+
				"commits lacking", *shown.Loss, reset)
		}
	})
}

// A discard shows, and seals, an object only a reflog entry's previous
// value records whatever its type, the reviewer's sequence on e484e92: the
// discard of a kept clone is shown, then a notes reference keeping a reflog
// is moved from the worker's commit to a blob nothing else holds and back,
// and the entry recording the blob as its new value is deleted, so the
// blob is the previous value of the entry left alone. The removal destroys
// it, so the digest shown before is refused, and the copy shown again lists
// it. Without every reflog value in the names, the blob is listed nowhere,
// the digest stands, and the confirmation destroys it unshown.
func TestADiscardShowsAnObjectOnlyAReflogsPreviousValueRecords(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	request := machine.KeptWorkAnswerRequest{Task: old, Hold: entry.Key(), Answer: string(store.KeptDiscard)}
	withService(t, h, func(under serviceUnderTest) {
		ctx := t.Context()
		var shown machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &shown); err != nil || shown.Loss == nil {
			t.Fatalf("the discard shown = %+v, %v", shown, err)
		}
		blob := hashObject(t, entry.CopyPath, "content nothing else holds\n")
		git(t, entry.CopyPath, "update-ref", "--create-reflog", "refs/notes/aside", "HEAD")
		git(t, entry.CopyPath, "update-ref", "refs/notes/aside", blob)
		git(t, entry.CopyPath, "update-ref", "refs/notes/aside", "HEAD")
		git(t, entry.CopyPath, "reflog", "delete", "refs/notes/aside@{1}")
		if values := git(t, entry.CopyPath, "log", "--walk-reflogs", "--format=%H", "--all"); strings.Contains(values, blob) {
			t.Fatalf("a reflog still names %s as an entry's new value:\n%s", blob, values)
		}
		var again machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &again); err != nil || again.Loss == nil ||
			again.Loss.Digest == shown.Loss.Digest ||
			!slices.ContainsFunc(again.Loss.Objects, func(o machine.KeptWorkLostObject) bool { return o.Object == blob && o.Type == "blob" }) {
			t.Fatalf("the discard shown again = %+v, %v; want the blob %s among the objects lost and another digest", again, err, blob)
		}
	})
}

// hashObject writes content into dir's repository as a blob and returns it.
func hashObject(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "blob")
	if err := writeFile(path, content); err != nil {
		t.Fatal(err)
	}
	return git(t, dir, "hash-object", "-w", path)
}

// A run's discard counts only what removing its worktree destroys, and its
// own stash, the reviewers' sequences on e484e92 and 5e99360: the discard
// of a kept run's worktree is shown, then a branch keeping a reflog is
// made in the gate the worktree belongs to, at a commit the default branch
// lacks, and an edit is stashed in a second worktree of the gate standing
// on that commit, which stores it in the gate's shared stash. Those are
// the gate's, which the removal leaves standing, so the copy shown again
// lists neither commit nor a stash and digests as shown. An edit stashed
// in the run's own worktree is its work, which nothing will name once the
// worktree is gone, so the copy shown then lists that stash commit, reads
// Stash, and refuses the first confirmation as changed; with it dropped,
// the first confirmation removes the worktree and leaves the gate's branch
// and the other worktree's stash standing. Without the names scoped to the
// worktree the gate's commit is listed and the confirmation refused;
// without the run's own stash read, the stashed edit is destroyed unshown.
func TestARunsDiscardCountsOnlyWhatItsRemovalDestroys(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)
	entry := keptEntryOf(t, h, subject)
	gate := git(t, entry.CopyPath, "rev-parse", "--path-format=absolute", "--git-common-dir")
	withService(t, h, func(under serviceUnderTest) {
		ctx := t.Context()
		var shown machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, discardRequest(entry, ""), &shown); err != nil || shown.Loss == nil {
			t.Fatalf("the discard shown = %+v, %v", shown, err)
		}
		head := git(t, entry.CopyPath, "rev-parse", "HEAD")
		side := git(t, gate, "commit-tree", "-p", head, "-m", "a branch of the gate's own", head+"^{tree}")
		git(t, gate, "update-ref", "--create-reflog", "refs/heads/gate-side", side)
		// A stash made in another worktree of the gate, on the gate's own
		// commit, is the gate's.
		other := filepath.Join(t.TempDir(), "other")
		git(t, gate, "worktree", "add", "--quiet", "--detach", other, side)
		tracked := strings.Fields(git(t, entry.CopyPath, "ls-files"))[0]
		if err := writeFile(filepath.Join(other, tracked), "stashed in another worktree\n"); err != nil {
			t.Fatal(err)
		}
		git(t, other, "stash", "push", "--quiet", "-m", "another worktree's")
		theirs := git(t, gate, "rev-parse", "refs/stash")
		show := func() machine.KeptWorkLoss {
			t.Helper()
			var out machine.KeptWorkAnswered
			if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, discardRequest(entry, ""), &out); err != nil || out.Loss == nil {
				t.Fatalf("the discard shown again = %+v, %v", out, err)
			}
			return *out.Loss
		}
		if again := show(); slices.Contains(again.Lacks, side) || slices.Contains(again.Lacks, theirs) || again.Stash ||
			again.Digest != shown.Loss.Digest {
			t.Fatalf("the discard shown again = %+v; want %s and the stash %s, the gate's own, not lost and the digest %s as shown",
				again, side, theirs, shown.Loss.Digest)
		}
		// A stash made in the run's own worktree is its work.
		if err := writeFile(filepath.Join(entry.CopyPath, tracked), "stashed in the run's worktree\n"); err != nil {
			t.Fatal(err)
		}
		git(t, entry.CopyPath, "stash", "push", "--quiet", "-m", "the run's own")
		mine := git(t, gate, "rev-parse", "refs/stash")
		if again := show(); !slices.Contains(again.Lacks, mine) || slices.Contains(again.Lacks, theirs) || !again.Stash ||
			again.Digest == shown.Loss.Digest {
			t.Fatalf("the discard shown with the run's own stash = %+v; want %s lost, %s not, Stash read and the digest moved",
				again, mine, theirs)
		}
	})
}

// A run's worktree's own logs count whatever the gate also records, the
// final review of c04c40f: a commit is made in a kept run's worktree and
// kept under a branch of the gate's own, with a reflog recording it, and
// the worktree is reset back. The commit is still named by the worktree's
// HEAD reflog, which its removal destroys, and the gate's references hold
// nothing, so the discard lists it and a land of HEAD is refused naming
// it. With the HEAD entry that moved to it deleted, and ORIG_HEAD, which
// the reset left at it, so the worktree's log records it only as the
// value its reset moved from, both still hold.
// Before, that value was dropped because the gate's reflog records it too,
// so the same commit was listed when the worktree's log named it by one
// value and not by the other. The positive control, the gate's own branch
// at a commit no log of the worktree records, is never listed. A discard
// then confirmed removes the worktree and leaves the gate's branch at the
// lost commit standing with its reflog, since the gate's references are
// not the worktree's to delete (round 23).
func TestARunsDiscardCountsWhatItsOwnLogsRecordWhateverTheGateRecords(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)
	entry := keptEntryOf(t, h, subject)
	gate := git(t, entry.CopyPath, "rev-parse", "--path-format=absolute", "--git-common-dir")
	withService(t, h, func(under serviceUnderTest) {
		ctx := t.Context()
		head := git(t, entry.CopyPath, "rev-parse", "HEAD")
		side := git(t, gate, "commit-tree", "-p", head, "-m", "a branch of the gate's own", head+"^{tree}")
		git(t, gate, "update-ref", "--create-reflog", "refs/heads/gate-side", side)
		git(t, entry.CopyPath, "checkout", "--quiet", "--detach")
		git(t, entry.CopyPath, "commit", "--quiet", "--allow-empty", "-m", "kept under a branch of the gate's")
		kept := git(t, entry.CopyPath, "rev-parse", "HEAD")
		git(t, gate, "update-ref", "--create-reflog", "refs/heads/keep", kept)
		git(t, entry.CopyPath, "reset", "--quiet", "--hard", head)
		check := func(state string) {
			t.Helper()
			var out machine.KeptWorkAnswered
			if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, discardRequest(entry, ""), &out); err != nil || out.Loss == nil {
				t.Fatalf("%s: the discard shown = %+v, %v", state, out, err)
			}
			if !slices.Contains(out.Loss.Lacks, kept) || slices.Contains(out.Loss.Lacks, side) {
				t.Fatalf("%s: the discard lists %v; want %s, which the worktree's log records, and not %s, the gate's alone",
					state, out.Loss.Lacks, kept, side)
			}
			var landed machine.KeptWorkAnswered
			err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, machine.KeptWorkAnswerRequest{
				Run: entry.RunID.Or(""), Hold: entry.Key(), Answer: string(store.KeptLand)}, &landed)
			if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), kept) {
				t.Fatalf("%s: the land of HEAD = %+v, %v; want it refused naming %s", state, landed, err, kept)
			}
		}
		check("named by the worktree's HEAD log")
		if log := git(t, entry.CopyPath, "reflog", "--format=%H", "HEAD"); !slices.Equal(strings.Fields(log)[:min(2, len(strings.Fields(log)))], []string{head, kept}) {
			t.Fatalf("the worktree's HEAD log reads:\n%s\nwant the reset to %s and then the commit of %s", log, head, kept)
		}
		git(t, entry.CopyPath, "reflog", "delete", "HEAD@{1}")
		if log := git(t, entry.CopyPath, "reflog", "--format=%H", "HEAD"); strings.Contains(log, kept) {
			t.Fatalf("the worktree's HEAD log still names %s as a new value:\n%s", kept, log)
		}
		// The reset left ORIG_HEAD at the commit too, a name of its own.
		git(t, entry.CopyPath, "update-ref", "-d", "ORIG_HEAD")
		check("recorded by the worktree's HEAD log only as the value its reset moved from")

	})
}

// A run's discard is refused, before anything is read or changed, for a
// worktree whose layout a discard does not support (round 25): its record
// in the gate holding a symbolic link, or core.symlinks false. The discard
// answer is refused naming why, with the worktree, its record and the
// entry as they were. The positive control is
// TestARunsDiscardRemovesItsWorktreeOnlyAsTheCopyWasRead's, the ordinary
// layout, which discards.
func TestARunsDiscardRefusesALayoutItDoesNotSupport(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P6)
	for _, c := range []struct{ name, reason string }{
		{"a symbolic link in its record", "symbolic link"},
		{"core.symlinks false", "core.symlinks"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h := newHome(t)
			subject := newSubject(t)
			recordRepository(t, h, subject)
			entry := keptEntryOf(t, h, subject)
			record := git(t, entry.CopyPath, "rev-parse", "--absolute-git-dir")
			if c.reason == "symbolic link" {
				if err := os.Symlink(t.TempDir(), filepath.Join(record, "elsewhere")); err != nil {
					t.Fatal(err)
				}
			} else {
				git(t, entry.CopyPath, "config", "core.symlinks", "false")
			}
			head := git(t, entry.CopyPath, "rev-parse", "HEAD")
			withService(t, h, func(under serviceUnderTest) {
				ctx := t.Context()
				var shown machine.KeptWorkAnswered
				err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, discardRequest(entry, ""), &shown)
				if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), c.reason) {
					t.Fatalf("the discard shown for a worktree with %s = %+v, %v; want it refused naming %q", c.name, shown, err, c.reason)
				}
				if now := git(t, entry.CopyPath, "rev-parse", "HEAD"); now != head {
					t.Fatalf("the worktree's HEAD reads %s after the refusal; want %s", now, head)
				}
				if _, err := os.Stat(filepath.Join(record, "HEAD")); err != nil {
					t.Fatalf("the worktree's record lost its HEAD: %v", err)
				}
				if got, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID); err != nil || !got.Open() {
					t.Fatalf("the entry reads %+v, %v after the refusal; want it open", got, err)
				}
			})
		})
	}
}

// A discard shown carries every name it lists exactly, byte for byte, over
// the socket: two tags of a task's clone whose names differ only in a byte
// that is not UTF-8 point at a blob only the clone holds, and the answer
// names both, distinct, as the bytes git stores (NameBytes, read through
// ExactNames). JSON alone carries a name with such a byte replaced, so both
// would arrive as the same text.
func TestADiscardShownCarriesNamesThatAreNotUTF8Exactly(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	if err := writeFile(entry.CopyPath+"/blob.txt", "only in the clone\n"); err != nil {
		t.Fatal(err)
	}
	blob := git(t, entry.CopyPath, "hash-object", "-w", "blob.txt")
	if err := os.Remove(entry.CopyPath + "/blob.txt"); err != nil {
		t.Fatal(err)
	}
	// A file system may refuse a loose reference's file of such a name, so
	// both are written packed, where git keeps a name's bytes as they are.
	names := []string{"refs/tags/invalid-\xfe", "refs/tags/invalid-\xff"}
	git(t, entry.CopyPath, "pack-refs", "--all")
	packed := filepath.Join(entry.CopyPath, ".git", "packed-refs")
	held, err := os.ReadFile(packed)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(held), "\n"), "\n")
	for _, name := range names {
		lines = append(lines, blob+" "+name)
	}
	slices.SortFunc(lines[1:], func(a, b string) int {
		return strings.Compare(a[strings.IndexByte(a, ' '):], b[strings.IndexByte(b, ' '):])
	})
	if err := os.WriteFile(packed, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if listed := git(t, entry.CopyPath, "for-each-ref", "--format=%(refname)", "refs/tags/"); listed != strings.Join(names, "\n") {
		t.Fatalf("git lists the tags as %q, want %q", listed, names)
	}

	withService(t, h, func(under serviceUnderTest) {
		var out machine.KeptWorkAnswered
		request := machine.KeptWorkAnswerRequest{Task: old, Hold: entry.Key(), Answer: string(store.KeptDiscard)}
		if err := under.client.Call(t.Context(), ipc.MethodKeptWorkAnswer, request, &out); err != nil || out.Loss == nil {
			t.Fatalf("the discard shown = %+v, %v", out, err)
		}
		var got []string
		for _, o := range out.Loss.Objects {
			if o.Object == blob {
				if len(o.NameBytes) != len(names) {
					t.Fatalf("the blob arrived with %d names' bytes beside %q, want %d", len(o.NameBytes), o.Names, len(names))
				}
				got = o.ExactNames()
			}
		}
		if !reflect.DeepEqual(got, names) {
			t.Fatalf("the blob's names arrived as %q, want %q exactly", got, names)
		}
	})
}

// hasLostPath is whether files names want's path, by its exact bytes, as
// lost for want's reason.
func hasLostPath(files []machine.KeptWorkLostPath, want machine.KeptWorkLostPath) bool {
	return slices.ContainsFunc(files, func(f machine.KeptWorkLostPath) bool {
		return f.Kind == want.Kind && f.ExactPath() == want.ExactPath()
	})
}

// A discard of an entry already answered is refused as answered, with a
// confirmation as without one (the full review of 04c4696): the entry is
// carried on, so its copy moved and its project's dispatch is free, and a
// discard given --confirm is refused naming the first answer and never
// with the row saying removal is deferred, whose advice to answer land or
// carry on, and that the project still waits, would be wrong. Before, the
// confirmation was refused as deferred before the entry was read as
// answered.
func TestADiscardOfAnAnsweredEntryIsRefusedAsAnsweredWithOrWithoutAConfirmation(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	// The worker starts in the kept copy, which stands, so it is swept first.
	service.RequireSweepClearsHere(t, "the carried-on task's worker launching before its entry is discarded")
	principles.Cite(t, principles.P15)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)

	o, _ := workerOptions(t, h)
	withServiceOptions(t, o, func(under serviceUnderTest) {
		ctx := t.Context()
		var out machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, carryOnRequest(old, entry), &out); err != nil {
			t.Fatalf("carrying the entry on: %v", err)
		}
		for _, confirm := range []string{"", "not-the-digest"} {
			err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, machine.KeptWorkAnswerRequest{
				Task: old, Hold: entry.Key(), Answer: string(store.KeptDiscard), Confirm: confirm}, &out)
			if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), "already answered carry-on") ||
				strings.Contains(err.Error(), machine.PhraseDiscardRemovalDeferred.Outcome) {
				t.Fatalf("a discard with confirmation %q of a carried-on entry = %v; want it refused as already answered, not as deferred", confirm, err)
			}
		}
		waitLaunched(t, h, out.Task)
	})
}
