package principles_test

import (
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
)

// moduleRoot is the module this check is about. A Go test runs with its own
// package directory as the working directory, so the module root is two above
// this file.
const moduleRoot = "../.."

// TestEveryPrincipleIsClaimedOrDeclaredUnclaimed is the check itself, run over
// this repository. It fails when the PRD lists a principle that no test cites
// and unclaimed.go does not account for, when a declared gap turns out to be
// cited, and when the PRD's list and this package's constants disagree in
// either direction.
//
// A failure here does not say a principle is broken. It says a claim about
// this repository's tests is no longer true, and the report says which.
func TestEveryPrincipleIsClaimedOrDeclaredUnclaimed(t *testing.T) {
	c, err := principles.Check(moduleRoot)
	if err != nil {
		t.Fatalf("checking the principles against %s: %v", moduleRoot, err)
	}
	if len(c.Cited) == 0 {
		t.Fatalf("the scan read %d test files under %s and found no citation at all, which is a broken scan rather than a repository whose tests claim nothing", c.Files, moduleRoot)
	}
	if !c.OK() {
		t.Fatalf("the PRD's principles and this repository's tests do not account for each other:\n\n%s", c.Report())
	}
}
