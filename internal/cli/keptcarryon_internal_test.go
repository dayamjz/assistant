package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// The carry-on busy and already-started refusals reach a person through the
// CLI renderer as the entry key they typed and the phrase row, with no task
// identifier and none of the internal words the rows must not say (P13,
// #731 round 2). On 1ba201c the busy line named the task id and said
// "dispatch" / "claim owed".
func TestCarryOnSlotRefusalsPrintWithoutTaskIdentifiers(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P13)
	vocabtest.Control(t)

	taskID := "72vguqpau776"
	key := store.KeptWorkKey(1)
	for _, row := range []struct {
		name   string
		phrase machine.Phrase
	}{
		{"busy", machine.PhraseCarryOnSlotBusy},
		{"already started", machine.PhraseCarryOnAlreadyStarted},
	} {
		t.Run(row.name, func(t *testing.T) {
			vocabtest.Assert(t, row.phrase.String())
			// The service wraps ipc.ErrRefused with the hold key and the row;
			// the wire then the CLI print err.Error() on standard error.
			err := fmt.Errorf("%w: %s: %s", ipc.ErrRefused, key, row.phrase)
			var out, errs bytes.Buffer
			in := &invocation{env: Environment{Stdout: &out, Stderr: &errs}}
			if code := render(in, nil, err); code != machine.ExitFailure {
				t.Fatalf("render exited %s, want failure", code)
			}
			printed := errs.String()
			if !strings.Contains(printed, row.phrase.String()) {
				t.Fatalf("printed %q, want the phrase row", printed)
			}
			if !strings.Contains(printed, key) {
				t.Fatalf("printed %q, want the hold key %s the person typed", printed, key)
			}
			if strings.Contains(printed, taskID) {
				t.Fatalf("printed the task identifier %s:\n%s", taskID, printed)
			}
			for _, term := range []string{"dispatch", "claim", "slot", "settled"} {
				if strings.Contains(printed, term) {
					t.Fatalf("printed internal wording %q:\n%s", term, printed)
				}
			}
		})
	}
}
