package cli

import (
	"testing"

	"github.com/dayamjz/assistant/internal/machine"
)

// An answer to a task's hold exits with the operational failure exactly when
// the person's retry began no work, so a caller learns that the decision was
// taken and the work did not start without reading the document. The oracle
// is written from each effect's meaning, the domain is machine.AnswerEffects,
// so an effect added there fails here until it is decided, and the positive
// control is a run that failed, which exits with the failure through the
// same function, so a function that answered success to everything fails.
func TestAnAnswerExitsWithFailureExactlyWhenARetryBeganNoWork(t *testing.T) {
	t.Parallel()
	if got := exitFor(machine.Run{Outcome: machine.OutcomeFailed}); got != machine.ExitFailure {
		t.Fatalf("the control, a failed run, exits %v, want %v", got, machine.ExitFailure)
	}
	succeeds := map[machine.AnswerEffect]bool{
		machine.AnswerRecorded:      true,
		machine.AnswerRelaunched:    true,
		machine.AnswerRedispatched:  true,
		machine.AnswerRetryDeferred: false,
		machine.AnswerRetryHeld:     false,
		machine.AnswerRetryAtBound:  false,
		machine.AnswerRetryRefused:  false,
		machine.AnswerStopped:       true,
	}
	for _, effect := range machine.AnswerEffects() {
		ok, decided := succeeds[effect]
		if !decided {
			t.Errorf("%s has no expected exit here", effect)
			continue
		}
		want := machine.ExitOK
		if !ok {
			want = machine.ExitFailure
		}
		if got := exitFor(machine.TaskAnswered{Effect: effect}); got != want {
			t.Errorf("an answer whose effect is %s exits %v, want %v", effect, got, want)
		}
	}
}
