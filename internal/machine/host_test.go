package machine_test

import (
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/supervisor"
)

// HostStopOf is the one translation: a verdict that allows is allowed with
// no message whatever the signal, a verdict that blocks is blocked with the
// turn's phrase and then the pull guard's, and the same verdict with the
// host's re-entry signal is allowed with the phrase that says why. The
// reason travels unchanged beside all three.
func TestHostStopOfAllowsExactlyWhenTheVerdictAllowsOrTheHostReEntered(t *testing.T) {
	t.Parallel()
	for _, reason := range supervisor.StopReasons() {
		verdict := supervisor.StopVerdict{Reason: reason}
		for _, reEntered := range []bool{false, true} {
			got := machine.HostStopOf(verdict, reEntered)
			if got.Supervision.Reason != reason {
				t.Errorf("%s: the reason was rewritten to %s", reason, got.Supervision.Reason)
			}
			if got.ReEntered != reEntered {
				t.Errorf("%s: the signal %v was recorded as %v", reason, reEntered, got.ReEntered)
			}
			switch {
			case verdict.Allowed():
				if !got.Allowed || got.Message != "" {
					t.Errorf("%s allows and answered allowed=%v message=%q", reason, got.Allowed, got.Message)
				}
			case reEntered:
				if !got.Allowed || !strings.HasPrefix(got.Message, machine.PhraseTurnReEntered) {
					t.Errorf("%s re-entered answered allowed=%v message=%q", reason, got.Allowed, got.Message)
				}
			default:
				if got.Allowed || !strings.HasPrefix(got.Message, machine.PhraseTurnBlocked) {
					t.Errorf("%s blocks and answered allowed=%v message=%q", reason, got.Allowed, got.Message)
				}
			}
			if !verdict.Allowed() && !strings.HasSuffix(got.Message, got.Supervision.Warning) {
				t.Errorf("%s: the message %q does not end with the pull guard's %q", reason, got.Message, got.Supervision.Warning)
			}
		}
	}
	// A guard that was not computed, which the host adapter hands over as
	// the zero verdict, blocks and says the situation is undecided.
	got := machine.HostStopOf(supervisor.StopVerdict{}, false)
	if got.Allowed || !strings.HasSuffix(got.Message, machine.PhraseUndecided) {
		t.Errorf("an uncomputed guard answered allowed=%v message=%q", got.Allowed, got.Message)
	}
	// And the two phrases are in the table the vocabulary test walks.
	listed := map[string]bool{}
	for _, phrase := range machine.Phrases() {
		listed[phrase] = true
	}
	for _, phrase := range []string{machine.PhraseTurnBlocked, machine.PhraseTurnReEntered} {
		if !listed[phrase] {
			t.Errorf("%q is not in Phrases, so nothing held it to the vocabulary rule", phrase)
		}
	}
}
