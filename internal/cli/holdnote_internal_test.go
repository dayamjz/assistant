package cli

import (
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// The stream shows a hold as it is answered, and a note recorded with the
// answer is read back there too, after its label and escaped as every text
// a person or an agent wrote is. The control is the same hold answered with
// no note, whose line carries no label, so the label is the note's and not
// part of every answered hold's line.
func TestAnAnsweredHoldsNoteIsReadBackOnTheStream(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P13)
	vocabtest.Control(t)
	hold := store.Hold{Key: "k", TaskID: store.Known("a task"), Subject: "whether the cache should move",
		Resolution: store.Known("approved")}
	bare := eventLine(mustEvent(t, ipc.TypeHoldState, hold))
	if strings.Contains(bare, machine.HoldNoteLabel) {
		t.Fatalf("a hold answered with no note reads %q, carrying the note's label", bare)
	}
	vocabtest.Assert(t, bare)

	hold.Note = store.Known("not this quarter\x1b[2J")
	line := eventLine(mustEvent(t, ipc.TypeHoldState, hold))
	if !strings.HasPrefix(line, bare) || !strings.HasSuffix(line, " "+machine.HoldNoteLabel+` not this quarter\x1b[2J`) {
		t.Fatalf("a hold answered with a note reads %q, want %q followed by the note escaped after its label", line, bare)
	}
	if strings.ContainsRune(line, '\x1b') {
		t.Fatalf("the note reached the line unescaped: %q", line)
	}
}
