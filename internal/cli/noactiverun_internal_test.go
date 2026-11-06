package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
)

// A status carrying no run in flight has two reasons, and each gets its own
// refusal: a working copy the home has no repository record for knows of no
// run at all, and a working copy bound to the gate whose branch simply has
// none. One sentence for both tells a caller standing in an unbound directory
// that the run is not there when the run exists and is held.
//
// The domain is machine.Status's own distinction: Repository is documented as
// absent exactly when the working copy has none. The positive control is the
// bound status, which really does have a repository record and must keep the
// sentence it has, so a function that answered the unbound text to everything
// fails here.
func TestNoRunInFlightTellsAnUnboundCopyApartFromABoundOneWithNoRun(t *testing.T) {
	t.Parallel()
	const working = "/w/copy"

	unbound := noRunInFlight(&machine.Status{}, working, "answer")
	bound := noRunInFlight(&machine.Status{Repository: &store.Repository{ID: "abc"}}, working, "answer")
	for _, err := range []error{unbound, bound} {
		if err == nil {
			t.Fatalf("a status with no run in flight refused nothing")
		}
		if !isUsage(err) {
			t.Errorf("%q is not reported as incorrect usage", err)
		}
	}
	if unbound.Error() == bound.Error() {
		t.Fatalf("both reasons refuse with one sentence, so neither says which it is:\n%s", unbound)
	}

	// The control keeps the sentence a bound copy has always had, so this
	// test cannot pass by rewording every refusal.
	if want := "this branch has no active run to answer"; bound.Error() != want {
		t.Errorf("a bound copy with no run refuses with\n%s\nwant\n%s", bound, want)
	}

	// The unbound refusal has to say the thing the caller cannot see: that
	// the absence is the directory's, not the run's, and how a run is
	// reached without a bound copy.
	for _, must := range []string{working, "no repository record", "--cancel=RUN", "--run"} {
		if !strings.Contains(unbound.Error(), must) {
			t.Errorf("the refusal for an unbound copy does not name %q:\n%s", must, unbound)
		}
	}
}

// isUsage says whether an error is reported as incorrect usage, which is how
// both refusals cross back to the caller.
func isUsage(err error) bool {
	var u usageError
	return errors.As(err, &u)
}
