package service_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// Fail-before: remove-anyway was not an answer. After: a shallow kept
// clone, whose discard is refused, is shown remove anyway's preview: the
// refusal and no loss, with nothing recorded or released and the copy
// standing (PRD section 6; P6/P12).
func TestARemoveAnywayPreviewShowsAShallowKeptCopysRefusal(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P6)
	principles.Cite(t, principles.P12)

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
			shown.Effect != machine.KeptWorkRemoveAnywayShown || !strings.Contains(shown.Refusal, "discard refused") {
			t.Fatalf("remove-anyway shown = %+v, %v; want remove-anyway-shown with the discard's refusal", shown, err)
		}
		if shown.Loss != nil {
			t.Fatalf("shown loss = %+v; want none, since a discard that refuses reads none of it", shown.Loss)
		}
		if _, err := os.Stat(entry.CopyPath); err != nil {
			t.Fatalf("the preview must keep the copy standing: %v", err)
		}
		if still, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID); err != nil || !still.Open() ||
			still.RemoveAnywayClaimed() || still.Answer.IsKnown() {
			t.Fatalf("after the preview the entry reads %+v, %v; want it open with nothing recorded", still, err)
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

// A copy that cannot be verified is offered for remove-anyway (decision 31 /
// section 13), with the refusal alone and no loss list.
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
		}, &shown); err != nil || shown.Effect != machine.KeptWorkRemoveAnywayShown || shown.Refusal == "" || shown.Loss != nil {
			t.Fatalf("remove-anyway of a gone copy = %+v, %v; want shown with a refusal and no loss list", shown, err)
		}
	})
}
