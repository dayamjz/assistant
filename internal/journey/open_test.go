package journey_test

import (
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/journey"
)

// TestAnAgentArgumentThatWouldNotSurviveTheEntryIsRefused drives Open over the
// seam every test here reaches the stand-in through.
//
// It cites no principle. What it is about is this harness: the agent entry is
// one configuration string, and both of its readers split it on whitespace, so
// an argument carrying a space arrives as two arguments and an empty one
// arrives as none. Neither has a symptom in this build, because no stage body
// launches an agent through the seam, so a control directory truncated at a
// space would simply never be reached and the harness would go on reporting
// green. The refusal is what keeps that from being silent, and the last case
// here is the words this harness actually uses, so the guard is shown not to
// refuse its only caller.
func TestAnAgentArgumentThatWouldNotSurviveTheEntryIsRefused(t *testing.T) {
	scenario, dir := cloned(t)

	for _, refused := range []struct {
		named    string
		argument string
	}{
		{"a control directory under a temporary root with a space in it", "--standin=/tmp/First Last/c"},
		{"an argument that is nothing but a tab", "\t"},
		{"an argument nobody filled in", ""},
	} {
		t.Run(refused.named, func(t *testing.T) {
			j, err := journey.Open(journey.Options{
				Scenario:       scenario,
				Dir:            dir,
				AgentArguments: []string{refused.argument},
			})
			if err == nil {
				t.Cleanup(func() { _ = j.Close() })
				t.Fatalf("Open accepted the agent argument %q, and the readers of an agent entry split "+
					"it on whitespace, so the agent would be given something else", refused.argument)
			}
			if !strings.Contains(err.Error(), "agent argument 0") {
				t.Fatalf("Open refused with %v, and a refusal has to name the argument it is about", err)
			}
		})
	}

	t.Run("the words this harness reaches the stand-in with", func(t *testing.T) {
		open(t, scenario, func(o *journey.Options) { o.Dir = dir })
	})
}
