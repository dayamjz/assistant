package service_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/store"
)

// discardRequest is the discard answer for a run's entry, with or without a
// confirmation.
func discardRequest(entry store.KeptWork, confirm string) machine.KeptWorkAnswerRequest {
	return machine.KeptWorkAnswerRequest{Run: entry.RunID.Or(""), Hold: entry.Key(), Answer: string(store.KeptDiscard), Confirm: confirm}
}

// PRD section 6's discard, through the real socket on a run's kept copy, a
// worktree of the gate: a discard alone shows exactly what would be lost,
// the commit the default branch lacks and the digest of the copy's state,
// naming no path of its working tree, the .git file pointing at the gate
// among them, and records and removes nothing; a confirmation with another digest is
// refused; a confirmation with the digest shown is refused once the copy
// holds a file it did not, and taken once the copy is back as shown, which
// removes the copy, answers the entry discard with the digest it was shown
// and the machine interface as who, and marks it discarded; and the same
// confirmation again reports the discard and errs on nothing. Without the
// digest comparison the changed copy is removed on the stale confirmation.
func TestDiscardingKeptWorkShowsWhatIsLostAndRemovesOnlyOnTheDigest(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)
	submitted := git(t, subject, "rev-parse", "HEAD")
	entry := keptEntryOf(t, h, subject)

	withService(t, h, func(under serviceUnderTest) {
		ctx := t.Context()
		answer := func(confirm string) (machine.KeptWorkAnswered, error) {
			var out machine.KeptWorkAnswered
			err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, discardRequest(entry, confirm), &out)
			return out, err
		}
		shown, err := answer("")
		if err != nil || shown.Effect != machine.KeptWorkDiscardShown || shown.Loss == nil || shown.Loss.Digest == "" ||
			len(shown.Loss.Lacks) != 1 || shown.Loss.Lacks[0] != submitted || shown.Loss.Head != submitted {
			t.Fatalf("a discard shown = %+v, %v; want what would be lost, the commit %s, with a digest", shown, err, submitted)
		}
		if slices.ContainsFunc(shown.Loss.Files, func(f machine.KeptWorkLostPath) bool { return f.Kind != "repository" }) {
			t.Fatalf("a discard of a worktree holding its commit alone names %+v; want no path of its working tree, "+
				"its .git pointer to the gate among them", shown.Loss.Files)
		}
		if still, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID); err != nil || !still.Open() {
			t.Fatalf("after the discard was shown the entry reads %+v, %v; want it open", still, err)
		}
		if _, err := os.Stat(entry.CopyPath); err != nil {
			t.Fatalf("showing the discard removed the copy: %v", err)
		}
		if _, err := answer("not-the-digest"); !errors.Is(err, ipc.ErrRefused) {
			t.Fatalf("a confirmation with another digest = %v; want it refused", err)
		}
		stray := entry.CopyPath + "/stray.txt"
		if err := os.WriteFile(stray, []byte("not committed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := answer(shown.Loss.Digest); !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), "changed") {
			t.Fatalf("a confirmation once the copy changed = %v; want it refused as changed", err)
		}
		if _, err := os.Stat(stray); err != nil {
			t.Fatalf("the refused discard removed the copy: %v", err)
		}
		if still, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID); err != nil || !still.Open() {
			t.Fatalf("after the refused confirmation the entry reads %+v, %v; want it open", still, err)
		}
		if err := os.Remove(stray); err != nil {
			t.Fatal(err)
		}
		done, err := answer(shown.Loss.Digest)
		if err != nil || done.Effect != machine.KeptWorkDiscarded || done.Loss == nil || done.Loss.Digest != shown.Loss.Digest ||
			done.Entry.Answer.Or("") != store.KeptDiscard || done.Entry.DiscardDigest.Or("") != shown.Loss.Digest || !done.Entry.DiscardedAt.IsKnown() {
			t.Fatalf("the confirmed discard = %+v, %v; want the entry answered discard with the digest shown and marked discarded", done, err)
		}
		if _, err := os.Lstat(entry.CopyPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the discarded copy still stands: %v", err)
		}
		recorded, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID)
		if err != nil || recorded.AnsweredBy.Or(store.Resolver{}) != store.ResolvedByMachineInterface() {
			t.Fatalf("the entry records who discarded as %+v (%v); want the machine interface, which is all this surface witnesses", recorded.AnsweredBy, err)
		}
		if again, err := answer(shown.Loss.Digest); err != nil || again.Effect != machine.KeptWorkDiscarded {
			t.Fatalf("the confirmation again = %+v, %v; want the discard reported", again, err)
		}
	})
}

// A discard whose basis cannot be established now is refused naming why,
// shows no list and records nothing, whatever the entry recorded when it
// was kept: the upstream is made unreadable while the copy stands, and the
// discard is refused as not established; with the upstream back, a task's
// clone is discarded on the same terms as a run's worktree: shown, then
// removed on the digest. Without the refusal a basis that cannot be read
// shows a list that may overstate or understate what is lost.
func TestADiscardRefusesAnUnestablishedEntryAndRemovesATasksClone(t *testing.T) {
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
		request.Confirm = out.Loss.Digest
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &out); err != nil || out.Effect != machine.KeptWorkDiscarded {
			t.Fatalf("the confirmed discard = %+v, %v", out, err)
		}
		if _, err := os.Lstat(entry.CopyPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the discarded clone still stands: %v", err)
		}
	})
}

// A discard confirmation recorded by a service that stopped before it
// removed the copy is carried out by the next start, though the upstream
// moved in between: the confirmation with the digest and the commits it
// was shown, written as the stopped service left it, and then a commit the
// copy does not hold lands on the default branch upstream; the next start
// removes the copy, which still holds exactly what was shown, and answers
// the entry discard and marks it. Without the completion at open the copy
// stands and the entry stays owed; with the default branch's tip sealed in
// the digest the moved upstream withdraws the confirmation.
func TestADiscardAStoppedServiceDidNotCarryOutIsCompletedAtTheNextStart(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)
	entry := keptEntryOf(t, h, subject)
	var shown machine.KeptWorkLoss
	withService(t, h, func(under serviceUnderTest) {
		var out machine.KeptWorkAnswered
		if err := under.client.Call(t.Context(), ipc.MethodKeptWorkAnswer, discardRequest(entry, ""), &out); err != nil || out.Loss == nil {
			t.Fatalf("the discard shown = %+v, %v", out, err)
		}
		shown = *out.Loss
	})
	records, err := store.Open(t.Context(), h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := records.ConfirmKeptWorkDiscard(t.Context(), entry.ID, shown.Digest, shown.Lacks, store.ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	_ = records.Close()
	advanceUpstream(t, git(t, subject, "remote", "get-url", "origin"), "unrelated.txt", "someone else's work\n", "someone else's work")

	withService(t, h, func(under serviceUnderTest) {
		deadline := time.Now().Add(30 * time.Second)
		for {
			got, err := recordsOf(t, h).KeptWorkEntry(t.Context(), entry.ID)
			if err == nil && got.DiscardedAt.IsKnown() {
				if _, err := os.Lstat(entry.CopyPath); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("the entry is marked discarded and the copy stands: %v", err)
				}
				return
			}
			if !time.Now().Before(deadline) {
				t.Fatalf("the next start did not complete the discard; the log reads:\n%s", readServiceLog(t, h))
			}
			time.Sleep(20 * time.Millisecond)
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
		request.Confirm = shown.Loss.Digest
		var out machine.KeptWorkAnswered
		err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &out)
		if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), "changed") {
			t.Fatalf("a confirmation once new work was stashed under the shown tip = %+v, %v; want it refused as changed", out, err)
		}
		if list := git(t, entry.CopyPath, "stash", "list", "--format=%H"); !strings.Contains(list, unseen) {
			t.Fatalf("after the refused discard the stash list reads %q; want the new work %s still in it", list, unseen)
		}
		if still, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID); err != nil || !still.Open() {
			t.Fatalf("after the refused confirmation the entry reads %+v, %v; want it open", still, err)
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
		request.Confirm = shown.Loss.Digest
		var done machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &done); err != nil || done.Effect != machine.KeptWorkDiscarded {
			t.Fatalf("the confirmed discard under log.date=relative = %+v, %v; want it discarded", done, err)
		}
		if _, err := os.Lstat(entry.CopyPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the discarded copy still stands: %v", err)
		}
	})
}

// Discarding a copy settles every entry of it, the reviewer's sequence:
// an investigation's copy is kept at the worker's commit A, the worker
// commits B, and the copy is kept again, both through the real cleanup and
// Keep, so two open entries name the one copy. Discarding the first, shown
// and confirmed, removes the copy and answers and marks both discarded with
// the digest the person confirmed, so neither is left holding the
// project's new tasks over a copy that is gone, and answering the second
// discard again reports it discarded. Without the siblings in the answer's
// and the mark's writes the second entry stays open, and every answer to it
// is refused because its copy is gone.
func TestDiscardingACopySettlesEveryEntryOfIt(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, first := plantKeptInvestigation(t, h, subject, repository.ID)
	if err := writeFile(first.CopyPath+"/more.txt", "what the worker found next\n"); err != nil {
		t.Fatal(err)
	}
	git(t, first.CopyPath, "add", "-A")
	git(t, first.CopyPath, "commit", "--quiet", "-m", "the worker's second commit")
	second := keepTaskCopy(t, h, old)
	if second.ID == first.ID || second.CopyPath != first.CopyPath || !second.Open() || !first.Open() {
		t.Fatalf("the copy kept again reads %+v beside %+v; want a second open entry of the same copy", second, first)
	}

	withService(t, h, func(under serviceUnderTest) {
		ctx := t.Context()
		request := machine.KeptWorkAnswerRequest{Task: old, Hold: first.Key(), Answer: string(store.KeptDiscard)}
		var shown machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &shown); err != nil || shown.Loss == nil {
			t.Fatalf("the discard shown = %+v, %v", shown, err)
		}
		request.Confirm = shown.Loss.Digest
		var done machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &done); err != nil || done.Effect != machine.KeptWorkDiscarded {
			t.Fatalf("the confirmed discard = %+v, %v", done, err)
		}
		if _, err := os.Lstat(first.CopyPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the discarded copy still stands: %v", err)
		}
		for _, id := range []int64{first.ID, second.ID} {
			got, err := recordsOf(t, h).KeptWorkEntry(ctx, id)
			if err != nil || got.Open() || got.Answer.Or("") != store.KeptDiscard || got.DiscardDigest.Or("") != shown.Loss.Digest ||
				!got.DiscardedAt.IsKnown() {
				t.Fatalf("entry %d reads %+v, %v; want it answered discard with the digest %s and marked discarded", id, got, err, shown.Loss.Digest)
			}
		}
		var again machine.KeptWorkAnswered
		other := machine.KeptWorkAnswerRequest{Task: old, Hold: second.Key(), Answer: string(store.KeptDiscard)}
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, other, &again); err != nil || again.Effect != machine.KeptWorkDiscarded {
			t.Fatalf("answering the second entry discard = %+v, %v; want it reported discarded", again, err)
		}
	})
}

// A discard of a copy with two entries that a stopped service confirmed
// and did not carry out is completed at the next start for both: the
// confirmation is written on the first as the stopped service left it, and
// the next start removes the copy once and answers and marks both in the
// mark's one write, so nothing is logged as left undone. Without the
// copy's other open entries in the mark, the second stays open over a copy
// that is gone.
func TestADiscardOfTwoEntriesAStoppedServiceLeftIsCompletedOnceForBoth(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, first := plantKeptInvestigation(t, h, subject, repository.ID)
	if err := writeFile(first.CopyPath+"/more.txt", "what the worker found next\n"); err != nil {
		t.Fatal(err)
	}
	git(t, first.CopyPath, "add", "-A")
	git(t, first.CopyPath, "commit", "--quiet", "-m", "the worker's second commit")
	second := keepTaskCopy(t, h, old)
	var shown machine.KeptWorkLoss
	withService(t, h, func(under serviceUnderTest) {
		var out machine.KeptWorkAnswered
		request := machine.KeptWorkAnswerRequest{Task: old, Hold: first.Key(), Answer: string(store.KeptDiscard)}
		if err := under.client.Call(t.Context(), ipc.MethodKeptWorkAnswer, request, &out); err != nil || out.Loss == nil {
			t.Fatalf("the discard shown = %+v, %v", out, err)
		}
		shown = *out.Loss
	})
	records, err := store.Open(t.Context(), h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := records.ConfirmKeptWorkDiscard(t.Context(), first.ID, shown.Digest, shown.Lacks, store.ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	_ = records.Close()

	withService(t, h, func(under serviceUnderTest) {
		for _, id := range []int64{first.ID, second.ID} {
			if got, err := recordsOf(t, h).KeptWorkEntry(t.Context(), id); err != nil || !got.DiscardedAt.IsKnown() {
				t.Fatalf("after the start entry %d reads %+v, %v; want it marked discarded", id, got, err)
			}
		}
		if _, err := os.Lstat(first.CopyPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the discarded copy still stands: %v", err)
		}
		if log := readServiceLog(t, h); strings.Contains(log, "could not be completed") {
			t.Fatalf("the start logged a discard left undone:\n%s", log)
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
			if !slices.Contains(files, want) {
				t.Fatalf("the discard shown names %+v; want %+v, which the removal destroys and nothing else holds, among them", files, want)
			}
		}
		if slices.ContainsFunc(files, func(f machine.KeptWorkLostPath) bool { return strings.HasPrefix(f.Path, "hooks/") }) {
			t.Fatalf("the discard shown names %+v; want none of the sample hooks git writes into every repository", files)
		}
		request.Confirm = shown.Loss.Digest
		var done machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &done); err != nil || done.Effect != machine.KeptWorkDiscarded {
			t.Fatalf("the confirmed discard = %+v, %v", done, err)
		}
		if _, err := os.Lstat(entry.CopyPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the discarded copy still stands: %v", err)
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
			if !slices.Contains(shown.Loss.Files, want) {
				t.Fatalf("the discard shown = %+v; want %+v, a deletion the removal destroys, among its files", *shown.Loss, want)
			}
		}
		request.Confirm = shown.Loss.Digest
		var done machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &done); err != nil || done.Effect != machine.KeptWorkDiscarded {
			t.Fatalf("the confirmed discard = %+v, %v", done, err)
		}
		if _, err := os.Lstat(entry.CopyPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the discarded copy still stands: %v", err)
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
		request.Confirm = shown.Loss.Digest
		var done machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &done); err != nil || done.Effect != machine.KeptWorkDiscarded {
			t.Fatalf("the confirmed discard = %+v, %v", done, err)
		}
		if _, err := os.Lstat(entry.CopyPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the discarded copy still stands: %v", err)
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
		request.Confirm = shown.Loss.Digest
		var done machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &done); err != nil || done.Effect != machine.KeptWorkDiscarded {
			t.Fatalf("the confirmed discard = %+v, %v", done, err)
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
		confirm := request
		confirm.Confirm = shown.Loss.Digest
		var out machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, confirm, &out); !errors.Is(err, ipc.ErrRefused) {
			t.Fatalf("the confirmation shown before the blob = %+v, %v; want it refused", out, err)
		}
		if _, err := os.Stat(entry.CopyPath); err != nil {
			t.Fatalf("the refused discard removed the copy: %v", err)
		}
		var again machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &again); err != nil || again.Loss == nil ||
			!slices.ContainsFunc(again.Loss.Objects, func(o machine.KeptWorkLostObject) bool { return o.Object == blob && o.Type == "blob" }) {
			t.Fatalf("the discard shown again = %+v, %v; want the blob %s among the objects lost", again, err, blob)
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
		var refused machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, discardRequest(entry, shown.Loss.Digest), &refused); !errors.Is(err, ipc.ErrRefused) {
			t.Fatalf("confirming the first digest over the run's own stash = %+v, %v; want it refused", refused, err)
		}
		git(t, entry.CopyPath, "stash", "drop", "--quiet")
		stashed := git(t, gate, "rev-parse", "refs/stash")
		if stashed != theirs {
			t.Fatalf("the gate's stash reads %s after the drop; want the other worktree's %s", stashed, theirs)
		}
		var done machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, discardRequest(entry, shown.Loss.Digest), &done); err != nil ||
			done.Effect != machine.KeptWorkDiscarded {
			t.Fatalf("the confirmed discard = %+v, %v; want it discarded", done, err)
		}
		if at := git(t, gate, "rev-parse", "refs/heads/gate-side"); at != side {
			t.Fatalf("the gate's branch reads %s after the discard; want it standing at %s", at, side)
		}
		if at := git(t, gate, "rev-parse", "refs/stash"); at != stashed {
			t.Fatalf("the gate's stash reads %s after the discard; want it standing at %s", at, stashed)
		}
	})
}

// A run's discard removes its worktree only when, with what it showed set
// aside, the removal would lose nothing it did not show, the owner's
// decision of 2026-10-24 after rounds 15 to 21: a commit made in it
// immediately before what was shown is set aside, which writes the
// worktree's own directory in the gate and no file of the worktree's
// (the final full review of 74949eb, finding 1), a commit only the
// worktree's reflog names, made with plumbing and HEAD moved back (round
// 23's first finding, which a discard that set the reflogs aside whole
// destroyed), and a file written into that directory once what was shown
// is set aside (where the delta review
// of 8ddafcb saw git's forced removal delete one), each refuse the
// confirmed discard with the worktree standing at its HEAD with its
// files, the written file where it was written, nothing left set aside and
// the entry open with its confirmation withdrawn. A worktree whose .git
// file points at a record outside the gate's worktrees directory is
// refused with nothing removed, the record standing where it was; and so
// is one whose .git file points at another worktree's record in the same
// gate (the delta review of e036141), the other worktree keeping its HEAD,
// its index with the work staged there alone, and a status git can read;
// and so is one whose .git is a symbolic link to the other worktree's .git
// file (the delta review of e5d54d3), with the same left standing. The
// positive control is the same confirmed discard with nothing written,
// which removes both. Each change is made by the test while the discard
// waits at its seam, so it falls in the moment named.
func TestARunsDiscardRemovesItsWorktreeOnlyAsTheCopyWasRead(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P6)
	const (
		beforeRemoval = "a commit immediately before what was shown is set aside"
		reflogOnly    = "a commit only a reflog names before what was shown is set aside"
		afterAside    = "a file written into the record once what was shown is set aside"
		outsideGate   = "a record outside the gate"
		siblingRecord = "the record of another copy"
		siblingLink   = "a .git linked to another copy's"
		control       = "nothing written, the positive control"
	)
	for _, c := range []string{beforeRemoval, reflogOnly, afterAside, outsideGate, siblingRecord, siblingLink, control} {
		t.Run(c, func(t *testing.T) {
			t.Parallel()
			h := newHome(t)
			subject := newSubject(t)
			recordRepository(t, h, subject)
			entry := keptEntryOf(t, h, subject)
			record := git(t, entry.CopyPath, "rev-parse", "--absolute-git-dir")
			gate := git(t, entry.CopyPath, "rev-parse", "--path-format=absolute", "--git-common-dir")
			if c == outsideGate {
				// The record moved out of the gate, with the worktree's .git
				// file and the record's own pointer to the gate rewritten so
				// git still reads the worktree through it.
				outside := filepath.Join(t.TempDir(), "record")
				if err := os.Rename(record, outside); err != nil {
					t.Fatal(err)
				}
				if err := writeFile(filepath.Join(outside, "commondir"), gate+"\n"); err != nil {
					t.Fatal(err)
				}
				if err := writeFile(filepath.Join(entry.CopyPath, ".git"), "gitdir: "+outside+"\n"); err != nil {
					t.Fatal(err)
				}
				record = git(t, entry.CopyPath, "rev-parse", "--absolute-git-dir")
				if resolved, err := filepath.EvalSymlinks(outside); err != nil || record != resolved && record != outside {
					t.Fatalf("git reads the worktree's record at %s; want it at %s (%v)", record, outside, err)
				}
			}
			var sibling string
			if c == siblingRecord || c == siblingLink {
				// Another worktree of the same gate, with work staged in its
				// index alone, and this copy's .git file pointed at its record.
				sibling = filepath.Join(t.TempDir(), "sibling")
				git(t, gate, "worktree", "add", "--quiet", "--detach", sibling, git(t, entry.CopyPath, "rev-parse", "HEAD"))
				if err := writeFile(filepath.Join(sibling, "staged.txt"), "staged in the other worktree alone\n"); err != nil {
					t.Fatal(err)
				}
				git(t, sibling, "add", "staged.txt")
				theirs := git(t, sibling, "rev-parse", "--absolute-git-dir")
				if c == siblingLink {
					if err := os.Remove(filepath.Join(entry.CopyPath, ".git")); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(filepath.Join(sibling, ".git"), filepath.Join(entry.CopyPath, ".git")); err != nil {
						t.Fatal(err)
					}
				} else if err := writeFile(filepath.Join(entry.CopyPath, ".git"), "gitdir: "+theirs+"\n"); err != nil {
					t.Fatal(err)
				}
				if record = git(t, entry.CopyPath, "rev-parse", "--absolute-git-dir"); record != theirs {
					t.Fatalf("git reads this copy's record at %s; want the other worktree's, %s", record, theirs)
				}
			}
			paused, resume := make(chan struct{}), make(chan struct{})
			var once sync.Once
			pause := func() { once.Do(func() { close(paused); <-resume }) }
			o := options(t, h)
			switch c {
			case beforeRemoval, reflogOnly:
				o = service.WithDiscardRemovalSeamsForTest(t, o, pause, nil)
			case afterAside:
				o = service.WithDiscardAsideSeamForTest(t, o, func() error { pause(); return nil })
			}
			withServiceOptions(t, o, func(under serviceUnderTest) {
				ctx := t.Context()
				// A failure below must not leave the service waiting at a seam.
				release := sync.OnceFunc(func() { close(resume) })
				defer release()
				var shown machine.KeptWorkAnswered
				if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, discardRequest(entry, ""), &shown); err != nil || shown.Loss == nil {
					t.Fatalf("the discard shown = %+v, %v", shown, err)
				}
				var done machine.KeptWorkAnswered
				answered := make(chan error, 1)
				go func() {
					answered <- under.client.Call(ctx, ipc.MethodKeptWorkAnswer, discardRequest(entry, shown.Loss.Digest), &done)
				}()
				var committed, headBefore string
				headBefore = git(t, entry.CopyPath, "rev-parse", "HEAD")
				if c != control && c != outsideGate && c != siblingRecord && c != siblingLink {
					select {
					case <-paused:
					case err := <-answered:
						t.Fatalf("the confirmed discard returned %+v, %v before its seam ran", done, err)
					}
					switch c {
					case beforeRemoval:
						git(t, entry.CopyPath, "commit", "--quiet", "--allow-empty", "-m", "made immediately before what was shown is set aside")
						committed = git(t, entry.CopyPath, "rev-parse", "HEAD")
					case reflogOnly:
						// Round 23's sequence, with plumbing so nothing but the
						// worktree's own reflog records it.
						head := git(t, entry.CopyPath, "rev-parse", "HEAD")
						committed = git(t, entry.CopyPath, "commit-tree", head+"^{tree}", "-p", head, "-m", "only a reflog names this")
						git(t, entry.CopyPath, "update-ref", "-m", "to the commit", "HEAD", committed)
						git(t, entry.CopyPath, "update-ref", "-m", "and back", "HEAD", head)
					case afterAside:
						if err := writeFile(filepath.Join(record, "written-once-set-aside"), "kept with the record\n"); err != nil {
							t.Fatal(err)
						}
					}
					release()
				}
				err := <-answered
				switch c {
				case beforeRemoval, reflogOnly, afterAside:
					if !errors.Is(err, ipc.ErrRefused) {
						t.Fatalf("the confirmed discard over %s = %+v, %v; want it refused", c, done, err)
					}
					want := headBefore
					if c == beforeRemoval {
						want = committed
					}
					if head := git(t, entry.CopyPath, "rev-parse", "HEAD"); head != want {
						t.Fatalf("the worktree's HEAD reads %s after the refusal; want %s", head, want)
					}
					if c == reflogOnly {
						if _, err := os.Stat(record); err != nil {
							t.Fatalf("the worktree's record, whose reflog alone names %s, is gone: %v", committed, err)
						}
						if log := git(t, entry.CopyPath, "reflog", "--format=%H", "HEAD"); !strings.Contains(log, committed) {
							t.Fatalf("HEAD's reflog no longer names %s after the refusal:\n%s", committed, log)
						}
					}
					if c == afterAside {
						if raw, err := os.ReadFile(filepath.Join(record, "written-once-set-aside")); err != nil || string(raw) != "kept with the record\n" {
							t.Fatalf("the file written into the record reads %q, %v; want it standing", raw, err)
						}
					}
					if aside, err := filepath.Glob(filepath.Join(filepath.Dir(entry.CopyPath), ".*.discarding")); err != nil || len(aside) != 0 {
						t.Fatalf("set aside after the refusal: %v, %v; want nothing (the service log reads:\n%s)", aside, err, readServiceLog(t, h))
					}
					if got, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID); err != nil || !got.Open() || got.DiscardDigest.IsKnown() {
						t.Fatalf("the entry reads %+v, %v after the refusal; want it open with its confirmation withdrawn", got, err)
					}
				case outsideGate, siblingRecord, siblingLink:
					if !errors.Is(err, ipc.ErrRefused) {
						t.Fatalf("the confirmed discard of a worktree whose record is %s = %+v, %v; want it refused", c, done, err)
					}
					if c == siblingRecord || c == siblingLink {
						if status := git(t, sibling, "status", "--porcelain"); status != "A  staged.txt" {
							t.Fatalf("the other worktree's status reads %q after the refusal; want its staged work, A  staged.txt", status)
						}
					}
					if _, err := os.Stat(filepath.Join(record, "HEAD")); err != nil {
						t.Fatalf("the record outside the gate lost its HEAD: %v; want it standing", err)
					}
					if _, err := os.Lstat(filepath.Join(entry.CopyPath, ".git")); err != nil {
						t.Fatalf("the worktree lost its .git file: %v; want it standing", err)
					}
					if got, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID); err != nil || !got.Open() || got.DiscardDigest.IsKnown() {
						t.Fatalf("the entry reads %+v, %v after the refusal; want it open with its confirmation withdrawn", got, err)
					}
				default:
					if err != nil || done.Effect != machine.KeptWorkDiscarded {
						t.Fatalf("the confirmed discard = %+v, %v; want it discarded", done, err)
					}
					if _, err := os.Stat(record); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("the worktree's record stands at %s after the discard: %v", record, err)
					}
					if list := git(t, gate, "worktree", "list", "--porcelain"); strings.Contains(list, entry.CopyPath) {
						t.Fatalf("the gate still lists the worktree:\n%s", list)
					}
					if _, err := os.Stat(entry.CopyPath); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("the worktree stands after the discard: %v", err)
					}
				}
			})
		})
	}
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

		// The confirmed discard removes the worktree and touches none of
		// the gate's references, the branch at the lost commit among them.
		var shown, done machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, discardRequest(entry, ""), &shown); err != nil || shown.Loss == nil {
			t.Fatalf("the discard shown = %+v, %v", shown, err)
		}
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, discardRequest(entry, shown.Loss.Digest), &done); err != nil ||
			done.Effect != machine.KeptWorkDiscarded {
			t.Fatalf("the confirmed discard = %+v, %v; want it discarded", done, err)
		}
		if at := git(t, gate, "rev-parse", "refs/heads/keep"); at != kept {
			t.Fatalf("the gate's branch keep reads %s after the discard; want it standing at %s", at, kept)
		}
		if log := git(t, gate, "reflog", "--format=%H", "refs/heads/keep"); !strings.Contains(log, kept) {
			t.Fatalf("the gate's branch keep's reflog reads %q after the discard; want it standing", log)
		}
	})
}

// A discard answers every entry of its copy that the removal's own read of
// the copy covers, not only the ones kept before the confirmation: the
// discard is confirmed and the service stops before the removal; then the
// copy's main branch, standing at the commit it was cloned at, which the
// default branch holds, is deleted, so nothing shown changes and the real
// cleanup's Keep records the copy again as a second entry. The next start removes the copy
// on the digest shown and answers both entries discard. Without the bound
// moved to the read, the second entry is left open over a copy that is
// gone, refusing every answer and holding its project's new tasks.
func TestADiscardAnswersAnEntryKeptBeforeItsRemovalRead(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	request := machine.KeptWorkAnswerRequest{Task: old, Hold: entry.Key(), Answer: string(store.KeptDiscard)}
	stopped := errors.New("the service stopped between the confirmation's record and the removal")
	withServiceOptions(t, service.WithDiscardInterruptForTest(t, options(t, h), func() error { return stopped }), func(under serviceUnderTest) {
		var shown machine.KeptWorkAnswered
		if err := under.client.Call(t.Context(), ipc.MethodKeptWorkAnswer, request, &shown); err != nil || shown.Loss == nil {
			t.Fatalf("the discard shown = %+v, %v", shown, err)
		}
		confirm := request
		confirm.Confirm = shown.Loss.Digest
		var out machine.KeptWorkAnswered
		if err := under.client.Call(t.Context(), ipc.MethodKeptWorkAnswer, confirm, &out); err == nil || !strings.Contains(err.Error(), "stopped between") {
			t.Fatalf("the confirmation past the stopped seam = %+v, %v; want the stop", out, err)
		}
	})
	git(t, entry.CopyPath, "branch", "--quiet", "-D", "main")
	second := keepTaskCopy(t, h, old)
	if second.ID == entry.ID || !second.Open() {
		t.Fatalf("the copy kept again reads %+v; want a second open entry", second)
	}

	withService(t, h, func(under serviceUnderTest) {
		ctx := t.Context()
		for _, id := range []int64{entry.ID, second.ID} {
			got := waitDiscarded(t, h, id)
			if got.Answer.Or("") != store.KeptDiscard {
				t.Fatalf("entry %d reads %+v; want it answered discard", id, got)
			}
		}
		if _, err := os.Lstat(entry.CopyPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the discarded copy still stands: %v", err)
		}
		if open, err := recordsOf(t, h).OpenKeptWork(ctx); err != nil || len(open) != 0 {
			t.Fatalf("the open kept work is %+v, %v; want none", open, err)
		}
	})
}

// A discard completed at the next start marks every entry of the copy kept
// from the state it removed, the final reviews of c04c40f and 74949eb: the
// discard of a task's clone holding the worker's commit is shown and
// confirmed; after the confirmation and before the removal reads the copy,
// its main branch, at a commit the default branch holds, is deleted, so
// nothing shown changes, and the real cleanup's Keep records the copy
// again as a second entry, either then or inside the removal's window,
// after the copy is found as shown and before it is removed; the service
// stops before the mark. The next start finds the copy gone and marks both
// entries discarded and leaves nothing open. The control, the same discard
// marked by the call that removed the copy, does the same. A change inside
// the window refuses the removal (TestARunsDiscardRemovesItsWorktreeOnlyAsTheCopyWasRead,
// and internal/tasks' TestAnythingWrittenAfterADiscardsLastReadRefusesIt),
// so an entry kept there is of the state the removal destroyed. Without
// the moment recorded, the next start bounded the mark by its own instant;
// bounded by the confirmation, it left the second entry open over a copy
// that is gone, and bounded by a moment taken before the window, so did a
// second entry kept inside it.
func TestADiscardCompletedAtTheNextStartMarksEveryEntryOfTheStateItRemoved(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)

	for _, keptInside := range []bool{false, true} {
		for _, stop := range []bool{false, true} {
			h := newHome(t)
			subject := newSubject(t)
			repository := recordRepository(t, h, subject)
			old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
			request := machine.KeptWorkAnswerRequest{Task: old, Hold: entry.Key(), Answer: string(store.KeptDiscard)}
			// Each seam hands the test the copy and waits until it is done, so
			// every git and every failure stays on the test's goroutine.
			pause := func() (func() error, chan struct{}, func()) {
				reached, resume := make(chan struct{}), make(chan struct{})
				return func() error { close(reached); <-resume; return nil }, reached, sync.OnceFunc(func() { close(resume) })
			}
			beforeRead, atRead, releaseRead := pause()
			inWindow, atWindow, releaseWindow := pause()
			stopped := errors.New("the service stopped between the removal and the mark")
			var beforeMark func() error
			if stop {
				beforeMark = func() error { return stopped }
			}
			o := service.WithDiscardInterruptForTest(t, options(t, h), beforeRead)
			o = service.WithDiscardRemovalSeamsForTest(t, o, func() { _ = inWindow() }, beforeMark)
			var again store.KeptWork
			withServiceOptions(t, o, func(under serviceUnderTest) {
				// A failure below must not leave the service waiting at a seam.
				defer releaseRead()
				defer releaseWindow()
				var shown machine.KeptWorkAnswered
				if err := under.client.Call(t.Context(), ipc.MethodKeptWorkAnswer, request, &shown); err != nil || shown.Loss == nil {
					t.Fatalf("the discard shown = %+v, %v", shown, err)
				}
				confirm := request
				confirm.Confirm = shown.Loss.Digest
				type answered struct {
					out machine.KeptWorkAnswered
					err error
				}
				done := make(chan answered, 1)
				go func() {
					var out machine.KeptWorkAnswered
					err := under.client.Call(t.Context(), ipc.MethodKeptWorkAnswer, confirm, &out)
					done <- answered{out, err}
				}()
				reach := func(at chan struct{}, where string) {
					t.Helper()
					select {
					case <-at:
					case got := <-done:
						t.Fatalf("the confirmed discard ended before %s: %+v, %v", where, got.out, got.err)
					}
				}
				reach(atRead, "the removal's read")
				git(t, entry.CopyPath, "branch", "--quiet", "-D", "main")
				if !keptInside {
					again = keepTaskCopy(t, h, old)
				}
				releaseRead()
				reach(atWindow, "the removal's window")
				if keptInside {
					again = keepTaskCopy(t, h, old)
				}
				releaseWindow()
				got := <-done
				if again.ID == entry.ID || !again.Open() {
					t.Fatalf("the copy kept again reads %+v; want a second open entry", again)
				}
				switch {
				case stop && (got.err == nil || !strings.Contains(got.err.Error(), "between the removal and the mark")):
					t.Fatalf("the confirmed discard past the stopped seam = %+v, %v; want the stop", got.out, got.err)
				case !stop && (got.err != nil || got.out.Effect != machine.KeptWorkDiscarded):
					t.Fatalf("the confirmed discard = %+v, %v; want it discarded", got.out, got.err)
				}
			})
			if _, err := os.Lstat(entry.CopyPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("the copy stands after the removal: %v", err)
			}
			withService(t, h, func(under serviceUnderTest) {
				for _, id := range []int64{entry.ID, again.ID} {
					if got := waitDiscarded(t, h, id); got.Answer.Or("") != store.KeptDiscard {
						t.Fatalf("kept inside the window %v, stopped before the mark %v: entry %d reads %+v; want it answered discard",
							keptInside, stop, id, got)
					}
				}
				if open, err := recordsOf(t, h).OpenKeptWork(t.Context()); err != nil || len(open) != 0 {
					t.Fatalf("kept inside the window %v, stopped before the mark %v: the open kept work is %+v, %v; want none",
						keptInside, stop, open, err)
				}
			})
		}
	}
}

// waitDiscarded waits until the entry reads discarded and returns it.
func waitDiscarded(t *testing.T, h *home.Home, id int64) store.KeptWork {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		got, err := recordsOf(t, h).KeptWorkEntry(t.Context(), id)
		if err == nil && got.DiscardedAt.IsKnown() {
			return got
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("entry %d was never discarded (%+v, %v); the log reads:\n%s", id, got, err, readServiceLog(t, h))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A discard a stopped service confirmed and did not carry out, over a copy
// that changed before the next start, is never silent, the reviewer's
// sequence: the discard is shown and confirmed, and the service stops
// through the seam right after the confirmation is recorded; a file is then
// written into the copy. The next start does not remove the changed copy,
// and the entry is still listed by status, by the catch-up read and by the
// supervisor loop's read of open kept work, so the project's new tasks
// still wait on it; answered discard again, it is shown as it is now and
// removed on the new digest, and the entry is answered discard with that
// digest and marked discarded. Without the confirmation kept apart from
// the answer, the entry reads answered and is listed nowhere while the copy
// stands, and every answer to it is refused.
func TestADiscardStoppedOverACopyThatChangedIsNeverSilent(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	request := machine.KeptWorkAnswerRequest{Task: old, Hold: entry.Key(), Answer: string(store.KeptDiscard)}
	stopped := errors.New("the service stopped between the confirmation's record and the removal")
	var first string
	withServiceOptions(t, service.WithDiscardInterruptForTest(t, options(t, h), func() error { return stopped }), func(under serviceUnderTest) {
		var shown machine.KeptWorkAnswered
		if err := under.client.Call(t.Context(), ipc.MethodKeptWorkAnswer, request, &shown); err != nil || shown.Loss == nil {
			t.Fatalf("the discard shown = %+v, %v", shown, err)
		}
		first = shown.Loss.Digest
		confirm := request
		confirm.Confirm = first
		var out machine.KeptWorkAnswered
		if err := under.client.Call(t.Context(), ipc.MethodKeptWorkAnswer, confirm, &out); err == nil || !strings.Contains(err.Error(), "stopped between") {
			t.Fatalf("the confirmation past the stopped seam = %+v, %v; want the stop", out, err)
		}
	})
	if err := writeFile(filepath.Join(entry.CopyPath, "after.txt"), "work written after the discard was confirmed\n"); err != nil {
		t.Fatal(err)
	}

	withService(t, h, func(under serviceUnderTest) {
		ctx := t.Context()
		if _, err := os.Stat(filepath.Join(entry.CopyPath, "after.txt")); err != nil {
			t.Fatalf("the start removed the copy that changed since the discard was shown: %v", err)
		}
		listed := func(entries []store.KeptWork) bool {
			return slices.ContainsFunc(entries, func(k store.KeptWork) bool { return k.ID == entry.ID })
		}
		if status := statusOf(t, under.client, subject); !listed(status.KeptWork) {
			t.Fatalf("status lists kept work %+v; want entry %d, whose copy stands", status.KeptWork, entry.ID)
		}
		var caught machine.CatchUp
		if err := under.client.Call(ctx, ipc.MethodCatchUpRead, struct{}{}, &caught); err != nil || !listed(caught.Kept) {
			t.Fatalf("the catch-up read lists kept work %+v, %v; want entry %d", caught.Kept, err, entry.ID)
		}
		if open, err := recordsOf(t, h).OpenKeptWork(ctx); err != nil || !listed(open) {
			t.Fatalf("the open kept work the supervisor loop reads is %+v, %v; want entry %d", open, err, entry.ID)
		}

		var again machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &again); err != nil || again.Loss == nil || again.Loss.Digest == first {
			t.Fatalf("the discard shown again = %+v, %v; want what the changed copy would lose, under a new digest", again, err)
		}
		confirm := request
		confirm.Confirm = again.Loss.Digest
		var done machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, confirm, &done); err != nil || done.Effect != machine.KeptWorkDiscarded {
			t.Fatalf("the discard confirmed again = %+v, %v; want it discarded", done, err)
		}
		if _, err := os.Lstat(entry.CopyPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the discarded copy still stands: %v", err)
		}
		got, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID)
		if err != nil || got.Answer.Or("") != store.KeptDiscard || got.DiscardDigest.Or("") != again.Loss.Digest || !got.DiscardedAt.IsKnown() {
			t.Fatalf("the entry reads %+v, %v; want it answered discard with digest %s and marked discarded", got, err, again.Loss.Digest)
		}
	})
}

// One call carries out a discard confirmation at a time: the first
// confirmation is held inside the seam, after it holds the copy's discard
// slot and before the removal, and a second confirmation of the same
// entry meanwhile is refused as under way and touches nothing; released,
// the first removes the copy and answers the entry. Without the slot the
// second carries the confirmation out beside the first, and either reads
// the copy half removed and withdraws the confirmation the other then
// cannot mark, or marks it first and the other fails on the mark.
func TestOneCallCarriesOutADiscardConfirmationAtATime(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	hold := func() error {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return nil
	}
	withServiceOptions(t, service.WithDiscardInterruptForTest(t, options(t, h), hold), func(under serviceUnderTest) {
		ctx := t.Context()
		request := machine.KeptWorkAnswerRequest{Task: old, Hold: entry.Key(), Answer: string(store.KeptDiscard)}
		var shown machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &shown); err != nil || shown.Loss == nil {
			t.Fatalf("the discard shown = %+v, %v", shown, err)
		}
		request.Confirm = shown.Loss.Digest
		type result struct {
			out machine.KeptWorkAnswered
			err error
		}
		first := make(chan result, 1)
		go func() {
			var out machine.KeptWorkAnswered
			err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &out)
			first <- result{out, err}
		}()
		<-entered
		var second machine.KeptWorkAnswered
		err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &second)
		if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), "being carried out") {
			close(release)
			t.Fatalf("a second confirmation while the first is carried out = %+v, %v; want it refused as under way", second, err)
		}
		if _, err := os.Stat(entry.CopyPath); err != nil {
			close(release)
			t.Fatalf("the refused second confirmation removed the copy: %v", err)
		}
		close(release)
		done := <-first
		if done.err != nil || done.out.Effect != machine.KeptWorkDiscarded {
			t.Fatalf("the first confirmation = %+v, %v; want it discarded", done.out, done.err)
		}
		if _, err := os.Lstat(entry.CopyPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the discarded copy still stands: %v", err)
		}
	})
}

// What a discard set aside and did not finish with is put back before an
// answer reads the copy, the owner's decision of 2026-10-24 that a discard
// deletes nothing it does not finish: a run's kept worktree holds a file
// no commit records, shown and confirmed. A discard that stops once it has
// set that file aside leaves its confirmation standing and the file beside
// the worktree, and the next discard answer carries the confirmation out
// on the worktree put back, which removes it. A discard over a file
// written where the shown one was moved from refuses, the confirmation
// withdrawn, and leaves both, so the next answer is refused naming what
// could not be put back; once the file written meanwhile is removed, the
// next answer puts the shown file back and shows the loss as it was. The
// positive control for the first is the worktree removed only then;
// without the put-back the second answer is refused for good.
func TestADiscardThatDidNotFinishIsPutBackBeforeTheCopyIsRead(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P6)
	for _, c := range []string{"stopped once set aside", "a file written where one was set aside"} {
		t.Run(c, func(t *testing.T) {
			t.Parallel()
			h := newHome(t)
			subject := newSubject(t)
			recordRepository(t, h, subject)
			entry := keptEntryOf(t, h, subject)
			scratch := filepath.Join(entry.CopyPath, "scratch.txt")
			if err := writeFile(scratch, "not committed\n"); err != nil {
				t.Fatal(err)
			}
			stopped := errors.New("stopped once what was shown was set aside")
			var once sync.Once
			seam := func() error {
				var err error
				once.Do(func() {
					if c == "stopped once set aside" {
						err = stopped
						return
					}
					err = writeFile(scratch, "written meanwhile\n")
				})
				return err
			}
			withServiceOptions(t, service.WithDiscardAsideSeamForTest(t, options(t, h), seam), func(under serviceUnderTest) {
				ctx := t.Context()
				var shown machine.KeptWorkAnswered
				if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, discardRequest(entry, ""), &shown); err != nil || shown.Loss == nil {
					t.Fatalf("the discard shown = %+v, %v", shown, err)
				}
				var done machine.KeptWorkAnswered
				if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, discardRequest(entry, shown.Loss.Digest), &done); err == nil {
					t.Fatalf("the confirmed discard = %+v; want it stopped or refused", done)
				}
				if _, err := os.Stat(entry.CopyPath); err != nil {
					t.Fatalf("the worktree is gone after a discard that did not finish: %v", err)
				}
				if c == "stopped once set aside" {
					if _, err := os.Lstat(scratch); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("scratch.txt stands in the worktree after the stop: %v; the case would test nothing", err)
					}
					var again machine.KeptWorkAnswered
					if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, discardRequest(entry, ""), &again); err != nil ||
						again.Effect != machine.KeptWorkDiscarded {
						t.Fatalf("the next discard answer = %+v, %v; want the standing confirmation carried out", again, err)
					}
					if _, err := os.Stat(entry.CopyPath); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("the worktree stands after the confirmation was carried out: %v", err)
					}
					return
				}
				var refused machine.KeptWorkAnswered
				err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, discardRequest(entry, ""), &refused)
				if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), "scratch.txt") {
					t.Fatalf("the next discard answer = %+v, %v; want it refused naming scratch.txt", refused, err)
				}
				if raw, err := os.ReadFile(scratch); err != nil || string(raw) != "written meanwhile\n" {
					t.Fatalf("scratch.txt holds %q, %v; want what was written meanwhile", raw, err)
				}
				if err := os.Remove(scratch); err != nil {
					t.Fatal(err)
				}
				var again machine.KeptWorkAnswered
				if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, discardRequest(entry, ""), &again); err != nil || again.Loss == nil ||
					again.Loss.Digest != shown.Loss.Digest {
					t.Fatalf("the discard shown once the file is removed = %+v, %v; want the loss shown first, %s", again, err, shown.Loss.Digest)
				}
				if raw, err := os.ReadFile(scratch); err != nil || string(raw) != "not committed\n" {
					t.Fatalf("scratch.txt holds %q, %v once put back; want what was shown", raw, err)
				}
			})
		})
	}
}
