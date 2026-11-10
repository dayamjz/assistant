package service_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
)

// Fail-before: remove-anyway was not an answer. After: a shallow kept
// clone is shown and then released with --force, keeping the copy; a
// discardable copy is refused naming discard (PRD section 6; P6/P12/P15).
func TestARemoveAnywayReleasesAShallowKeptCopyAndKeepsIt(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P6)
	principles.Cite(t, principles.P12)
	principles.Cite(t, principles.P15)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	// Shallow the clone so Loss refuses (layout unsupported) and
	// remove-anyway is offered.
	git(t, entry.CopyPath, "fetch", "-q", "--depth=1", ".", "HEAD")

	withService(t, h, func(under serviceUnderTest) {
		ctx := t.Context()
		request := machine.KeptWorkAnswerRequest{Task: old, Hold: entry.Key(), Answer: string(store.KeptRemoveAnyway)}
		var shown machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &shown); err != nil ||
			shown.Effect != machine.KeptWorkRemoveAnywayShown || shown.Refusal == "" {
			t.Fatalf("remove-anyway shown = %+v, %v; want remove-anyway-shown with a refusal", shown, err)
		}
		if !strings.Contains(shown.Refusal, "shallow") && !strings.Contains(shown.Refusal, "layout") {
			t.Fatalf("refusal = %q; want it name the shallow/layout refuse", shown.Refusal)
		}
		if still, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID); err != nil || !still.Open() {
			t.Fatalf("after show the entry reads %+v, %v; want it open", still, err)
		}
		if _, err := os.Stat(entry.CopyPath); err != nil {
			t.Fatalf("show must leave the copy standing: %v", err)
		}

		request.Force = true
		var done machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &done); err != nil ||
			done.Effect != machine.KeptWorkRemovedAnyway || done.Entry.Answer.Or("") != store.KeptRemoveAnyway {
			t.Fatalf("confirmed remove-anyway = %+v, %v; want removed-anyway", done, err)
		}
		if _, err := os.Stat(entry.CopyPath); err != nil {
			t.Fatalf("remove-anyway must keep the copy standing: %v", err)
		}
		if still, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID); err != nil || still.Open() ||
			still.Answer.Or("") != store.KeptRemoveAnyway || !still.RemovedAnywayAt.IsKnown() {
			t.Fatalf("after confirmation the entry reads %+v, %v; want it answered remove-anyway", still, err)
		}
	})
}

// A copy a discard can remove is refused for remove-anyway, naming discard.
func TestARemoveAnywayIsRefusedWhenADiscardCanRemoveTheCopy(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)

	withService(t, h, func(under serviceUnderTest) {
		ctx := t.Context()
		err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, machine.KeptWorkAnswerRequest{
			Task: old, Hold: entry.Key(), Answer: string(store.KeptRemoveAnyway),
		}, &machine.KeptWorkAnswered{})
		if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), "discard can remove") {
			t.Fatalf("remove-anyway of a discardable copy = %v; want refused naming discard", err)
		}
		if still, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID); err != nil || !still.Open() {
			t.Fatalf("after the refuse the entry reads %+v, %v; want it open", still, err)
		}
	})
}

// A discard refusal of a shallow copy names --answer remove-anyway
// (PRD section 9).
func TestADiscardRefusalOfAShallowCopyNamesRemoveAnyway(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	git(t, entry.CopyPath, "fetch", "-q", "--depth=1", ".", "HEAD")

	withService(t, h, func(under serviceUnderTest) {
		err := under.client.Call(t.Context(), ipc.MethodKeptWorkAnswer, machine.KeptWorkAnswerRequest{
			Task: old, Hold: entry.Key(), Answer: string(store.KeptDiscard),
		}, &machine.KeptWorkAnswered{})
		if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), "remove-anyway") {
			t.Fatalf("discard of a shallow copy = %v; want it name remove-anyway", err)
		}
	})
}

// Remove-anyway is refused while a land claim stands on the entry.
func TestARemoveAnywayIsRefusedWhileALandClaimStands(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	git(t, entry.CopyPath, "fetch", "-q", "--depth=1", ".", "HEAD")

	withService(t, h, func(under serviceUnderTest) {
		ctx := t.Context()
		writer, err := store.Open(ctx, h.Database(), store.WithRedactor(redact.New()))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.ClaimKeptWorkLanding(ctx, entry.ID, store.ResolvedByPerson()); err != nil {
			_ = writer.Close()
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		err = under.client.Call(ctx, ipc.MethodKeptWorkAnswer, machine.KeptWorkAnswerRequest{
			Task: old, Hold: entry.Key(), Answer: string(store.KeptRemoveAnyway),
		}, &machine.KeptWorkAnswered{})
		if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), "land claim") {
			t.Fatalf("remove-anyway under a land claim = %v; want refused naming the claim", err)
		}
	})
}

// Remove-anyway is refused while a discard set-aside directory stands.
func TestARemoveAnywayIsRefusedWhileADiscardSetAsideStands(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	git(t, entry.CopyPath, "fetch", "-q", "--depth=1", ".", "HEAD")
	dir, err := h.Discarding(entry.CopyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	withService(t, h, func(under serviceUnderTest) {
		err := under.client.Call(t.Context(), ipc.MethodKeptWorkAnswer, machine.KeptWorkAnswerRequest{
			Task: old, Hold: entry.Key(), Answer: string(store.KeptRemoveAnyway),
		}, &machine.KeptWorkAnswered{})
		if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), "set aside") {
			t.Fatalf("remove-anyway with a set-aside = %v; want refused naming the set-aside", err)
		}
	})
}

// A copy that cannot be verified is offered for remove-anyway (decision 31).
func TestARemoveAnywayIsOfferedForAnUnverifiableCopy(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P6)
	principles.Cite(t, principles.P15)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	if err := os.RemoveAll(entry.CopyPath); err != nil {
		t.Fatal(err)
	}

	withService(t, h, func(under serviceUnderTest) {
		var shown machine.KeptWorkAnswered
		if err := under.client.Call(t.Context(), ipc.MethodKeptWorkAnswer, machine.KeptWorkAnswerRequest{
			Task: old, Hold: entry.Key(), Answer: string(store.KeptRemoveAnyway),
		}, &shown); err != nil || shown.Effect != machine.KeptWorkRemoveAnywayShown || shown.Refusal == "" {
			t.Fatalf("remove-anyway of a gone copy = %+v, %v; want shown with a refusal", shown, err)
		}
	})
}

// A Force confirmation whose discard refusal no longer holds takes the
// claim back and changes nothing.
func TestARemoveAnywayForceIsRefusedWhenTheRefusalNoLongerHolds(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P6)
	principles.Cite(t, principles.P15)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	git(t, entry.CopyPath, "fetch", "-q", "--depth=1", ".", "HEAD")

	withService(t, h, func(under serviceUnderTest) {
		ctx := t.Context()
		request := machine.KeptWorkAnswerRequest{Task: old, Hold: entry.Key(), Answer: string(store.KeptRemoveAnyway)}
		var shown machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &shown); err != nil ||
			shown.Effect != machine.KeptWorkRemoveAnywayShown || shown.Refusal == "" {
			t.Fatalf("show = %+v, %v; want remove-anyway-shown", shown, err)
		}
		writer, err := store.Open(ctx, h.Database(), store.WithRedactor(redact.New()))
		if err != nil {
			t.Fatal(err)
		}
		commits := entry.Holds
		if shown.Loss != nil && len(shown.Loss.Lacks) > 0 {
			commits = shown.Loss.Lacks
		}
		if _, err := writer.ClaimKeptWorkRemoveAnyway(ctx, entry.ID, shown.Refusal, commits, store.ResolvedByPerson()); err != nil {
			_ = writer.Close()
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		// Clear the shallow marker so a discard can remove the copy again
		// (fetch --unshallow of this layout leaves it shallow).
		if err := os.Remove(entry.CopyPath + "/.git/shallow"); err != nil {
			t.Fatal(err)
		}

		request.Force = true
		var out machine.KeptWorkAnswered
		err = under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &out)
		if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), "discard can remove") {
			t.Fatalf("force after the refusal cleared = %+v, %v; want refused naming discard", out, err)
		}
		still, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID)
		if err != nil || !still.Open() || still.RemoveAnywayClaimed() {
			t.Fatalf("after the refuse the entry reads %+v, %v; want it open with no remove-anyway claim", still, err)
		}
	})
}
