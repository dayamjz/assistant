package service_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	advanceUpstream(t, git(t, subject, "remote", "get-url", "origin"), "unrelated.txt")

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

// advanceUpstream lands a commit on the upstream's main that no copy holds,
// from a clone of its own, the way another person's push moves it.
func advanceUpstream(t *testing.T, upstream, file string) string {
	t.Helper()
	pusher := filepath.Join(t.TempDir(), "pusher")
	git(t, filepath.Dir(pusher), "clone", "--quiet", upstream, pusher)
	if err := writeFile(filepath.Join(pusher, file), "someone else's work\n"); err != nil {
		t.Fatal(err)
	}
	git(t, pusher, "add", "-A")
	git(t, pusher, "commit", "--quiet", "-m", "someone else's work")
	git(t, pusher, "push", "--quiet", "origin", "HEAD:main")
	return git(t, pusher, "rev-parse", "HEAD")
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
