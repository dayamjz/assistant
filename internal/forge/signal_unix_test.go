//go:build unix

package forge_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dayamjz/assistant/internal/forge"
)

// A provider that started and was then ended by something else reports no exit
// status of its own, so it never answered the request. Reading that as a
// rejection would tell a caller the provider considered the request and
// refused it, and the remedy for that is not the remedy for a provider the
// system killed part way through an answer.
//
// The case is unix-only, because on Windows every process that ran reports a
// status and there is no shape to model there.
func TestAProviderEndedBySomethingElseIsUnavailableRatherThanRejected(t *testing.T) {
	h := newHarness(t, ghScript{"checks": {{
		Stderr: "gh: fetching the check rollup\n",
		Kill:   true,
	}}})
	report, err := h.gh.Checks(context.Background(), 12)
	var refusal *forge.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Checks returned %v, want a *Refusal", err)
	}
	if refusal.Reason != forge.ReasonUnavailable {
		t.Errorf("reason is %q, want %q", refusal.Reason, forge.ReasonUnavailable)
	}
	if got := report.Evaluate(forge.NoCIDeclaration{}); got.Green() {
		t.Errorf("a killed provider produced a green verdict: %v", got)
	}
}
