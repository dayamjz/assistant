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

// A discard's confirmation is admitted by the service only from a peer in
// the foreground of its controlling terminal, foreground.go's relation,
// decided from the kernel's lineage and not from the command line: the
// preview is served to a peer with no terminal, a confirmation from that
// peer over the socket itself is refused with the not-at-terminal code and
// the row, nothing is recorded and the copy stands, and the same
// confirmation from a process in a terminal's foreground removes the copy.
// Without the relation in the service the detached peer's confirmation
// removes the copy.
func TestADiscardIsConfirmedOnlyFromATerminalsForeground(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)

	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)
	entry := keptEntryOf(t, h, subject)

	withService(t, h, func(under serviceUnderTest) {
		ctx := t.Context()
		raw, code := callDetached(t, under, ipc.MethodKeptWorkAnswer, discardRequest(entry, ""))
		if code != "" {
			var out machine.KeptWorkAnswered
			again := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, discardRequest(entry, ""), &out)
			t.Fatalf("the preview from a peer with no terminal was refused with %q (the same preview in process answers %v)", code, again)
		}
		var shown machine.KeptWorkAnswered
		if err := json.Unmarshal(raw, &shown); err != nil || shown.Loss == nil || shown.Loss.Digest == "" {
			t.Fatalf("the preview does not decode to a loss: %v\n%s", err, raw)
		}
		if _, code := callDetached(t, under, ipc.MethodKeptWorkAnswer, discardRequest(entry, shown.Loss.Digest)); code != ipc.CodeNotAtTerminal {
			t.Fatalf("a confirmation from a peer with no terminal answered %q, want %q", code, ipc.CodeNotAtTerminal)
		}
		if still, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID); err != nil || !still.Open() {
			t.Fatalf("after the refused confirmation the entry reads %+v, %v; want it open", still, err)
		}
		if _, err := os.Stat(entry.CopyPath); err != nil {
			t.Fatalf("the refused confirmation removed the copy: %v", err)
		}
		raw, code = callAtTerminal(t, under, ipc.MethodKeptWorkAnswer, discardRequest(entry, shown.Loss.Digest))
		if code != "" {
			t.Fatalf("a confirmation from the foreground of a terminal was refused with %q", code)
		}
		var done machine.KeptWorkAnswered
		if err := json.Unmarshal(raw, &done); err != nil || done.Effect != machine.KeptWorkDiscarded {
			t.Fatalf("the confirmation does not decode to a discard: %v\n%s", err, raw)
		}
		if gone, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID); err != nil || gone.Answer.Or("") != store.KeptDiscard || !gone.DiscardedAt.IsKnown() {
			t.Fatalf("after the confirmation the entry reads %+v, %v; want it discarded", gone, err)
		}
		if _, err := os.Lstat(entry.CopyPath); !os.IsNotExist(err) {
			t.Fatalf("the discarded copy still stands: %v", err)
		}
	})
}
