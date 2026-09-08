package journey_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/fixture"
	"github.com/dayamjz/assistant/internal/journey"
)

// TestAScenarioIsDrivenByOneTestAndReclaimedByThatOne drives both halves of
// the claim registry's rule, because a guard nobody has shown failing is a
// comment and one that refuses too much is worse than absent.
//
// The rule is about how many tests drive a scenario, so the holder is what it
// turns on: a different test is refused and told who took it, and the holder
// itself is given the scenario back. Nothing releases a claim, so without the
// second half a test running twice in one process - all `go test -count` does
// - is refused by its own earlier claim, which is a conflict naming one test
// on both sides and reads as flakiness rather than as the registry being wrong.
//
// It holds the unreadable-trusted-config scenario, which no test drives
// exclusively, so this takes nothing away from one that does.
func TestAScenarioIsDrivenByOneTestAndReclaimedByThatOne(t *testing.T) {
	const scenario = fixture.ScenarioUnreadableTrustedConfig

	first, err := journey.Claim(scenario, t.Name())
	if err != nil {
		t.Fatalf("claiming %s: %v", scenario, err)
	}
	if first.Name != scenario {
		t.Fatalf("claimed %s and was handed %s", scenario, first.Name)
	}

	// The holder asking again is still one test driving the scenario, so it
	// is handed the same one rather than refused.
	again, err := journey.Claim(scenario, t.Name())
	if err != nil {
		t.Fatalf("the holder re-claiming %s was refused: %v", scenario, err)
	}
	if !reflect.DeepEqual(again, first) {
		t.Fatalf("re-claiming %s handed back a different scenario:\n first: %+v\n again: %+v",
			scenario, first, again)
	}

	// A second test is what the guard exists to refuse, and it names the
	// first so the report says which two tests collided.
	const other = "TestSomeOtherTestWantingTheSameScenario"
	if _, err := journey.Claim(scenario, other); !errors.Is(err, journey.ErrScenarioClaimed) {
		t.Fatalf("a second test claiming %s: want %v, got %v", scenario, journey.ErrScenarioClaimed, err)
	} else if !strings.Contains(err.Error(), t.Name()) || !strings.Contains(err.Error(), other) {
		t.Fatalf("the refusal names neither the holder nor the asker: %v", err)
	}
}
