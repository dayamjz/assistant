package machine_test

import (
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/machine"
)

// Every onboarding refusal has a row of its own, each row is in the table the
// vocabulary test walks, and the two that a flag or an install answers name
// that remedy. A refusal outside the set still refuses.
func TestEveryOnboardRefusalIsDistinctAndListed(t *testing.T) {
	t.Parallel()
	listed := map[machine.Phrase]bool{}
	for _, row := range machine.TablePhrases() {
		listed[row] = true
	}
	seen := map[machine.Phrase]string{}
	for _, refusal := range machine.OnboardRefusals() {
		row := machine.OnboardRefusalPhrase(refusal)
		if other, ok := seen[row]; ok {
			t.Errorf("%s reads the same as %s: %q", refusal, other, row)
		}
		seen[row] = string(refusal)
		if !listed[row] {
			t.Errorf("%s's row %q is not in TablePhrases", refusal, row)
		}
		if row.Outcome == "" || row.Consequence == "" || row.Decision == "" {
			t.Errorf("%s's row is missing a field: %+v", refusal, row)
		}
	}
	ambiguous := machine.OnboardRefusalPhrase(machine.OnboardIdentityAmbiguous)
	if !strings.Contains(ambiguous.Decision, "--upstream") {
		t.Errorf("the ambiguous-identity decision %q does not name --upstream", ambiguous.Decision)
	}
	missing := machine.OnboardRefusalPhrase(machine.OnboardGitMissing)
	if !strings.Contains(missing.Decision, "git") {
		t.Errorf("the missing-git decision %q does not name git", missing.Decision)
	}
	if got := machine.OnboardRefusalPhrase("never-a-refusal"); got != machine.OnboardRefusalPhrase(machine.OnboardGitMissing) {
		t.Errorf("a refusal outside the set reads %q, want the missing-git row", got)
	}
}
