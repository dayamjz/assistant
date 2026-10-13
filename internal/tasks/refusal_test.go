package tasks

import (
	"errors"
	"fmt"
	"testing"

	"github.com/dayamjz/assistant/internal/taskstate"
)

// Every comparison the assertion can fail on maps to a reason the
// "isolation-refused" kind's decoder accepts, the mapping is the one the doc
// states, and an error that is no refused assertion maps to nothing.
func TestRefusalMapsEveryComparisonToARecognizedReason(t *testing.T) {
	want := map[Comparison]taskstate.IsolationRefusal{
		ComparisonEnvironment: taskstate.RefusedNotIsolated,
		ComparisonToplevel:    taskstate.RefusedNotIsolated,
		ComparisonCommonDir:   taskstate.RefusedNotIsolated,
		ComparisonGitDir:      taskstate.RefusedNotIsolated,
		ComparisonPrimary:     taskstate.RefusedPrimaryCheckout,
		ComparisonGate:        taskstate.RefusedNotIsolated,
		ComparisonRemotes:     taskstate.RefusedWrongShape,
		ComparisonHead:        taskstate.RefusedWrongShape,
	}
	row, ok := taskstate.Lookup("isolation-refused")
	if !ok || row.Detail == nil {
		t.Fatal("isolation-refused has no detail decoder to check the mapping against")
	}
	for comparison, reason := range want {
		err := fmt.Errorf("wrapped: %w", &IsolationError{Comparison: comparison, Detail: "detail"})
		got, ok := Refusal(err)
		if !ok || got != reason {
			t.Errorf("Refusal(%s) = %q, %v; want %q", comparison, got, ok, reason)
		}
		if err := row.Detail(string(got)); err != nil {
			t.Errorf("the decoder refuses the reason %s maps to: %v", comparison, err)
		}
	}
	if got, ok := Refusal(errors.New("not an assertion")); ok || got != "" {
		t.Errorf("Refusal of an unrelated error = %q, %v; want nothing", got, ok)
	}
	if got, ok := Refusal(ErrExpectation); ok || got != "" {
		t.Errorf("Refusal of an expectation that could not be asserted = %q, %v; want nothing", got, ok)
	}
}
