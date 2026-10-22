package service_test

import (
	"errors"
	"os"
	"path/filepath"
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
// and records and removes nothing; a confirmation with another digest is
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
