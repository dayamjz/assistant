//go:build unix

package service_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

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
