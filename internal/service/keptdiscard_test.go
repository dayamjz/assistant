package service_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
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

// A discard answer recorded by a service that stopped before it removed
// the copy is completed by the next start, though the upstream moved in
// between: the entry answered discard with the digest and the commits it
// was shown, written as the stopped service left it, and then a commit the
// copy does not hold lands on the default branch upstream; the next start
// removes the copy, which still holds exactly what was shown, and marks the
// entry discarded. Without the completion at open the copy stands and the
// entry stays owed; with the default branch's tip sealed in the digest the
// moved upstream refuses the removal for good.
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
	if _, err := records.AnswerKeptWorkDiscard(t.Context(), entry.ID, shown.Digest, shown.Lacks, store.ResolvedByMachineInterface()); err != nil {
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

// A discard of a copy with two entries that a stopped service recorded and
// did not carry out is completed at the next start for both: the answer is
// written as the stopped service left it, which answers the copy's other
// entry with it, and the next start removes the copy once and marks both,
// the second read again after the first's mark took it, so nothing is
// logged as left undone. Without reading the entry again the second's mark
// is refused as already discarded and logged as a discard not completed.
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
	if _, err := records.AnswerKeptWorkDiscard(t.Context(), first.ID, shown.Digest, shown.Lacks, store.ResolvedByMachineInterface()); err != nil {
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
