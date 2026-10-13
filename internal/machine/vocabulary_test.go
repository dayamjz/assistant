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
	// The required wording: what happened, what it costs, what to do.
	for _, want := range []string{"nothing is watching", "go unnoticed", "assistant service start"} {
		if !strings.Contains(machine.PhraseUnsupervised, want) {
			t.Errorf("the unsupervised phrase %q does not say %q", machine.PhraseUnsupervised, want)
		}
	}
	// The derivation catches a phrase that does carry mechanics, written
	// in the shape a careless renderer would produce.
	careless := "Task abcdefghij23 is running under /home/someone/.assistant; the terminal-gone wake surfaced."
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
		if !verdict.Allowed() && got.Warning != machine.PhraseUnsupervised {
			t.Errorf("%s blocks and carries %q, want the unsupervised phrase", reason, got.Warning)
		}
	}
}
