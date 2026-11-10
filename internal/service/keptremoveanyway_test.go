package service_test

import (
	"encoding/json"
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

// A remove-anyway confirmation from a peer with no terminal is refused;
// one from a terminal's foreground releases the entry.
func TestARemoveAnywayIsConfirmedOnlyFromATerminalsForeground(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	git(t, entry.CopyPath, "fetch", "-q", "--depth=1", ".", "HEAD")

	withService(t, h, func(under serviceUnderTest) {
		ctx := t.Context()
		req := machine.KeptWorkAnswerRequest{Task: old, Hold: entry.Key(), Answer: string(store.KeptRemoveAnyway)}
		raw, code := callDetached(t, under, ipc.MethodKeptWorkAnswer, req)
		if code != "" {
			t.Fatalf("the show from a peer with no terminal was refused with %q", code)
		}
		var shown machine.KeptWorkAnswered
		if err := json.Unmarshal(raw, &shown); err != nil || shown.Effect != machine.KeptWorkRemoveAnywayShown {
			t.Fatalf("the show does not decode: %v\n%s", err, raw)
		}
		req.Force = true
		if _, code := callDetached(t, under, ipc.MethodKeptWorkAnswer, req); code != ipc.CodeNotAtTerminal {
			t.Fatalf("a force from a peer with no terminal answered %q, want %q", code, ipc.CodeNotAtTerminal)
		}
		if still, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID); err != nil || !still.Open() {
			t.Fatalf("after the refused force the entry reads %+v, %v; want it open", still, err)
		}
		var done machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, req, &done); err != nil ||
			done.Effect != machine.KeptWorkRemovedAnyway {
			t.Fatalf("force from the test terminal = %+v, %v; want removed-anyway", done, err)
		}
		if _, err := os.Stat(entry.CopyPath); err != nil {
			t.Fatalf("the copy must still stand: %v", err)
		}
	})
}
