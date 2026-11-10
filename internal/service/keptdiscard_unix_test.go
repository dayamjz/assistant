//go:build unix

package service_test

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"testing"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// A discard's confirmation is admitted by the service only from a peer in
// the foreground of its controlling terminal. The preview is served to a
// peer with no terminal. A confirmation from that peer is refused with
// not-at-terminal. A confirmation from a terminal's foreground removes a
// run's worktree.
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

		var out machine.KeptWorkAnswered
		err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, discardRequest(entry, shown.Loss.Digest), &out)
		if err != nil || out.Effect != machine.KeptWorkDiscarded || out.Entry.Answer.Or("") != store.KeptDiscard {
			t.Fatalf("a confirmation from the test's own terminal = %+v, %v; want the worktree discarded", out, err)
		}
		if _, err := os.Stat(entry.CopyPath); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("a confirmed discard left the worktree standing: %v", err)
		}
		if still, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID); err != nil || still.Answer.Or("") != store.KeptDiscard {
			t.Fatalf("after confirmation the entry reads %+v, %v; want it answered discard", still, err)
		}
	})
}
