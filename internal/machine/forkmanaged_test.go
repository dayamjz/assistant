package machine_test

import (
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/managed"
)

// Every refusal of a fork and every refusal internal/managed makes has a row
// of its own, each listed in the table the vocabulary test walks; each fork
// refusal names the flag that is its remedy, and the protected-folder row
// names both remedies, moving out and granting access with the flag that
// says so. A member outside either set reads as a row that still refuses.
func TestEveryForkAndManagedRowIsDistinctListedAndNamesItsRemedy(t *testing.T) {
	t.Parallel()
	listed := map[machine.Phrase]bool{}
	for _, row := range machine.TablePhrases() {
		listed[row] = true
	}
	seen := map[machine.Phrase]string{}
	distinct := func(name string, row machine.Phrase) {
		t.Helper()
		if other, ok := seen[row]; ok {
			t.Errorf("%s reads the same as %s: %q", name, other, row)
		}
		seen[row] = name
		if !listed[row] {
			t.Errorf("%s's row %q is not in TablePhrases, so nothing held it to the vocabulary rule", name, row)
		}
	}
	remedies := map[machine.ForkRefusal]string{
		machine.ForkUnaddressable:         "--fork-url",
		machine.ForkUpstreamUnaddressable: "--upstream",
		machine.ForkIsUpstream:            "--fork-url",
	}
	for _, refusal := range machine.ForkRefusals() {
		row := machine.ForkRefusalPhrase(refusal)
		distinct("fork refusal "+string(refusal), row)
		if !strings.Contains(row.Decision, remedies[refusal]) {
			t.Errorf("the %s refusal's decision %q does not name %s", refusal, row.Decision, remedies[refusal])
		}
	}
	distinct("fork recorded", machine.PhraseInitFork)
	if got := machine.ForkRefusalPhrase("never-a-refusal"); got != machine.ForkRefusalPhrase(machine.ForkUnaddressable) {
		t.Errorf("a fork refusal outside the set reads %q", got)
	}

	for _, reason := range managed.Reasons() {
		distinct("managed refusal "+string(reason), machine.ManagedRefusalPhrase(reason))
	}
	protected := machine.ManagedRefusalPhrase(managed.ReasonProtectedFolder).Decision
	for _, remedy := range []string{"Move them out", "Full Disk Access", "--full-disk-access-granted"} {
		if !strings.Contains(protected, remedy) {
			t.Errorf("the protected-folder decision %q does not name %q", protected, remedy)
		}
	}
	if got := machine.ManagedRefusalPhrase("never-a-reason"); got != machine.ManagedRefusalPhrase(managed.ReasonManagerRefused) {
		t.Errorf("a managed refusal outside the set reads %q", got)
	}
	for name, row := range map[string]machine.Phrase{
		"service up":      machine.PhraseManagedServiceUp,
		"service managed": machine.PhraseServiceManaged,
		"removed":         machine.PhraseManagedRemoved,
		"restart":         machine.PhraseManagedRestart,
		"still up":        machine.PhraseManagedStillUp,
	} {
		distinct(name, row)
	}
}
