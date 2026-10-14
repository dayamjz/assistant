package machine_test

import (
	"slices"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
)

// A message's row is chosen from its record: read once acknowledged,
// whatever the delivery says, and otherwise one row per delivery fact, each
// different from the others, so a renderer cannot print "it reached the
// worker" over a record that says it was refused. Every row it chooses is
// one TablePhrases lists, which is what the vocabulary test holds to P13's
// wording, and the stop rows are listed too.
func TestAMessagesRowIsChosenFromItsRecord(t *testing.T) {
	at := store.Known(time.Unix(1, 0).UTC())
	written := store.Message{WrittenAt: at}
	refused := store.Message{RefusedAt: at, Refusal: store.Known("ended")}
	unknown := store.Message{}
	read := store.Message{WrittenAt: at, AcknowledgedAt: at, AcknowledgedEvent: store.Known(int64(3))}
	readUnknown := store.Message{AcknowledgedAt: at, AcknowledgedEvent: store.Known(int64(3))}

	rows := []machine.Phrase{
		machine.MessagePhrase(written), machine.MessagePhrase(refused),
		machine.MessagePhrase(unknown), machine.MessagePhrase(read),
	}
	for i := range rows {
		for j := i + 1; j < len(rows); j++ {
			if rows[i] == rows[j] {
				t.Errorf("deliveries %d and %d read the same row: %q", i, j, rows[i])
			}
		}
	}
	if machine.MessagePhrase(readUnknown) != machine.MessagePhrase(read) {
		t.Error("an acknowledged message whose send never reported does not read as read")
	}
	listed := machine.TablePhrases()
	for _, p := range append(rows, machine.StopPhrase(machine.TaskStopped{}), machine.PhraseStopUnverified) {
		if !slices.Contains(listed, p) {
			t.Errorf("the row %q is not one TablePhrases lists", p)
		}
	}
}
