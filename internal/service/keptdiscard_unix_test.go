//go:build unix

package service_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
)

// A discard's confirmation is admitted by the service only from a peer in
// the foreground of its controlling terminal, foreground.go's relation,
// decided from the kernel's lineage and not from the command line (PRD
// section 6, "Removing a discarded copy"). The preview is served to a peer
// with no terminal. A confirmation from that peer over the socket itself is
// refused with the not-at-terminal code and the row saying so, and nothing
// is recorded and the copy stands. The same confirmation from a process in
// a terminal's foreground passes the relation and meets the deferral,
// since removal is not in this build. Without the relation in the service,
// the detached peer's confirmation is refused as deferred instead.
func TestADiscardIsConfirmedOnlyFromATerminalsForeground(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)
	entry := keptEntryOf(t, h, subject)

	withService(t, h, func(under serviceUnderTest) {
		ctx := t.Context()
		raw, code := callDetached(t, under, ipc.MethodKeptWorkAnswer, discardRequest(entry, ""))
		if code != "" {
			t.Fatalf("the preview from a peer with no terminal was refused with %q", code)
		}
		var shown machine.KeptWorkAnswered
		if err := json.Unmarshal(raw, &shown); err != nil || shown.Loss == nil || shown.Loss.Digest == "" {
			t.Fatalf("the preview does not decode to a loss: %v\n%s", err, raw)
		}
		copyBefore := fileTree(t, entry.CopyPath, nil)

		if _, code := callDetached(t, under, ipc.MethodKeptWorkAnswer, discardRequest(entry, shown.Loss.Digest)); code != ipc.CodeNotAtTerminal {
			t.Fatalf("a confirmation from a peer with no terminal answered %q, want %q", code, ipc.CodeNotAtTerminal)
		}
		if still, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID); err != nil || !still.Open() {
			t.Fatalf("after the refused confirmation the entry reads %+v, %v; want it open", still, err)
		}
		if now := fileTree(t, entry.CopyPath, nil); now != copyBefore {
			t.Fatalf("the refused confirmation changed the copy:\n%s\nwant\n%s", now, copyBefore)
		}

		_, code = callAtTerminal(t, under, ipc.MethodKeptWorkAnswer, discardRequest(entry, shown.Loss.Digest))
		if code != ipc.CodeRefused {
			t.Fatalf("a confirmation from the foreground of a terminal answered %q, want it refused as deferred (%q)", code, ipc.CodeRefused)
		}
		var out machine.KeptWorkAnswered
		err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, discardRequest(entry, shown.Loss.Digest), &out)
		if err == nil || !strings.Contains(err.Error(), machine.PhraseDiscardRemovalDeferred.String()) {
			t.Fatalf("a confirmation from the test's own terminal = %v; want the deferred row", err)
		}
		if _, err := os.Stat(entry.CopyPath); err != nil {
			t.Fatalf("a refused confirmation removed the copy: %v", err)
		}
		if still, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID); err != nil || !still.Open() {
			t.Fatalf("after the refused confirmations the entry reads %+v, %v; want it open", still, err)
		}
	})
}
