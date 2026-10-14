package machine_test

import (
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/machine"
)

// Every refusal to record a default branch names both remedies the owner's
// decision on issue 199 requires, every source and every refusal has a row of
// its own, and every one is in the table the vocabulary test walks. A source
// or refusal outside the set reads as a row that claims nothing and still
// refuses, never as one of the members.
func TestEveryDefaultBranchRowIsDistinctListedAndNamesItsRemedies(t *testing.T) {
	t.Parallel()
	listed := map[machine.Phrase]bool{}
	for _, row := range machine.TablePhrases() {
		listed[row] = true
	}
	seen := map[machine.Phrase]string{}
	distinct := func(name string, row machine.Phrase) {
		if other, ok := seen[row]; ok {
			t.Errorf("%s reads the same as %s: %q", name, other, row)
		}
		seen[row] = name
		if !listed[row] {
			t.Errorf("%s's row %q is not in TablePhrases, so nothing held it to the vocabulary rule", name, row)
		}
	}
	for _, source := range machine.DefaultBranchSources() {
		distinct("source "+string(source), machine.DefaultBranchPhrase(source))
	}
	for _, refusal := range machine.DefaultBranchRefusals() {
		row := machine.DefaultBranchRefusalPhrase(refusal)
		distinct("refusal "+string(refusal), row)
		for _, remedy := range []string{"--default-branch", "git remote set-head origin --auto"} {
			if !strings.Contains(row.Decision, remedy) {
				t.Errorf("the %s refusal's decision %q does not name %q", refusal, row.Decision, remedy)
			}
		}
	}
	unsaid := machine.DefaultBranchPhrase("never-a-source")
	for _, source := range machine.DefaultBranchSources() {
		if unsaid == machine.DefaultBranchPhrase(source) {
			t.Errorf("a source outside the set reads as %s", source)
		}
	}
	if !listed[unsaid] {
		t.Errorf("the row for an unnamed source %q is not in TablePhrases", unsaid)
	}
	if got := machine.DefaultBranchRefusalPhrase("never-a-refusal"); got != machine.DefaultBranchRefusalPhrase(machine.DefaultBranchUnreadable) {
		t.Errorf("a refusal outside the set reads %q, want the unreadable row", got)
	}
}
