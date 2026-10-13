package machine_test

import (
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/supervisor"
)

// P13 for the pull guard's text, in both directions section 13 asks for: the
// internal vocabulary is absent, with the forbidden set derived from the
// packages that own each kind of word, and the required wording is present,
// an outcome, a consequence and a next decision. The claim is for the phrases
// in machine's table and nothing else: internal/cli's human rendering still
// prints identifiers, stage names and paths, which #110 is for.
func TestThePhrasesNameOutcomesAndNoMechanics(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P13)
	vocabtest.Control(t)
	phrases := machine.Phrases()
	if len(phrases) == 0 {
		t.Fatal("the table is empty, so nothing is held to anything")
	}
	for _, phrase := range phrases {
		vocabtest.Assert(t, phrase)
	}
	// The required wording: what happened, what it costs, what to do, with
	// the next decision the one each situation calls for.
	for phrase, wants := range map[string][]string{
		machine.PhraseServiceDown:        {"nothing is watching", "go unnoticed", "assistant service start"},
		machine.PhraseServiceNotWatching: {"the service is up", "go unnoticed", "assistant service restart"},
		machine.PhraseUndecided:          {"could not be decided", "may go unnoticed", "assistant doctor"},
	} {
		for _, want := range wants {
			if !strings.Contains(phrase, want) {
				t.Errorf("the phrase %q does not say %q", phrase, want)
			}
		}
	}
	// Every blocking reason internal/supervisor can answer with has a
	// phrase, derived from its set and not listed here, and no allowing
	// reason has one.
	listed := map[string]bool{}
	for _, phrase := range phrases {
		listed[phrase] = true
	}
	for _, reason := range supervisor.StopReasons() {
		phrase, ok := machine.PhraseFor(reason)
		blocks := !(supervisor.StopVerdict{Reason: reason}).Allowed()
		switch {
		case blocks && !ok:
			t.Errorf("%s blocks and has no phrase", reason)
		case blocks && !listed[phrase]:
			t.Errorf("%s's phrase %q is not in Phrases, so nothing held it to the rule", reason, phrase)
		case !blocks && ok:
			t.Errorf("%s allows and has the phrase %q", reason, phrase)
		}
	}
	// The derivation catches a phrase that does carry mechanics, written
	// in the shape a careless renderer would produce.
	careless := "Task abcdefghij23 is running under " + vocabtest.SampleRoot() + "; the terminal-gone wake surfaced."
	if len(vocabtest.Violations(t, careless)) < 4 {
		t.Fatalf("a phrase carrying a task identifier, a state, a home path and a wake kind was caught %d times: %v",
			len(vocabtest.Violations(t, careless)), vocabtest.Violations(t, careless))
	}
}

// SupervisionOf is the one translation: a verdict that allows carries no
// warning, every one that blocks carries the phrase, and the reason travels
// unchanged beside it.
func TestSupervisionOfWarnsExactlyWhenTheVerdictBlocks(t *testing.T) {
	t.Parallel()
	for _, reason := range supervisor.StopReasons() {
		verdict := supervisor.StopVerdict{Reason: reason}
		got := machine.SupervisionOf(verdict)
		if got.Reason != reason {
			t.Errorf("%s: the reason was rewritten to %s", reason, got.Reason)
		}
		if verdict.Allowed() && got.Warning != "" {
			t.Errorf("%s allows and carries the warning %q", reason, got.Warning)
		}
		if want, _ := machine.PhraseFor(reason); !verdict.Allowed() && got.Warning != want {
			t.Errorf("%s blocks and carries %q, want its own phrase %q", reason, got.Warning, want)
		}
	}
	// A free lock is a service to start and a held lock with no loop is one
	// to restart: the two the build can produce today read differently.
	if got := machine.SupervisionOf(supervisor.StopVerdict{Reason: supervisor.StopLockFree}).Warning; got != machine.PhraseServiceDown {
		t.Errorf("lock-free carries %q, want the service-down phrase", got)
	}
	if got := machine.SupervisionOf(supervisor.StopVerdict{Reason: supervisor.StopNoBeacon}).Warning; got != machine.PhraseServiceNotWatching {
		t.Errorf("no-beacon carries %q, want the not-watching phrase", got)
	}
	// A reason outside the set still warns rather than blocking silently.
	if got := machine.SupervisionOf(supervisor.StopVerdict{Reason: "never-a-reason"}).Warning; got != machine.PhraseUndecided {
		t.Errorf("an unknown reason carries %q, want the undecided phrase", got)
	}
}
