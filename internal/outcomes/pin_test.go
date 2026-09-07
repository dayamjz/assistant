package outcomes_test

import (
	"testing"

	"github.com/dayamjz/assistant/internal/outcomes"
	"github.com/dayamjz/assistant/internal/principles"
)

// moduleRoot is the module this check is about. A Go test runs with its own
// package directory as the working directory, so the module root is two above
// this file.
const moduleRoot = "../.."

// TestTheOutcomeSetIsPinnedToThePRD is the check itself, run over this
// repository. It fails when the PRD's outcome row and internal/machine's set
// declare different values, declare them in different orders, put one of them
// in different groups, or when an outcome the build declares carries no next
// action.
//
// It claims P14 for the outcome set: the PRD's row is the one owner, and the
// build points at it rather than restating it. Duplicated rules drifting apart
// is what P14 names, and these two had already done it once.
//
// A failure here does not say the machine interface is wrong. It says the two
// statements of its outcome set no longer match, and the report says how.
func TestTheOutcomeSetIsPinnedToThePRD(t *testing.T) {
	principles.Cite(t, principles.P14)

	a, err := outcomes.Check(moduleRoot)
	if err != nil {
		t.Fatalf("checking the outcome set against %s: %v", moduleRoot, err)
	}
	if !a.OK() {
		t.Fatalf("the PRD's outcome row and internal/machine's set do not account for each other:\n\n%s", a.Report())
	}
}
