package journey_test

import (
	"testing"

	"github.com/dayamjz/assistant/internal/journey"
)

// TestEveryPlantedConditionIsDrivenOrDeclaredUndriven fails when this harness
// has been handed a planted condition and says nothing about it.
//
// internal/fixture plants conditions and records what each must produce; most
// of them are refusals, and a refusal nobody triggered is indistinguishable
// from one that does not work. This is what stops a condition being added to
// the fixture and quietly falling outside everything here, and it is what
// stops a row surviving the test it named.
func TestEveryPlantedConditionIsDrivenOrDeclaredUndriven(t *testing.T) {
	root := moduleRoot(t)
	report, err := journey.DrivesReport(root)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if report != "" {
		t.Fatalf("this harness and internal/fixture's catalog disagree:\n\n%s", report)
	}
}

// TestEveryPrincipleIsDrivenHereOrDeclaredNotToBe fails when the table of what
// this harness establishes and the principles its tests cite disagree.
//
// Both directions matter and the second is the one that holds the line. A row
// claiming a principle is driven here with no test here citing it describes a
// harness somebody meant to write. A row saying a principle is not driven here
// with a test here citing it is a gap declaration that outlived its gap.
func TestEveryPrincipleIsDrivenHereOrDeclaredNotToBe(t *testing.T) {
	report, err := journey.CoverageReport(moduleRoot(t))
	if err != nil {
		t.Fatalf("%v", err)
	}
	if report != "" {
		t.Fatalf("the coverage table and the citations in this package disagree:\n\n%s", report)
	}
}

// TestEveryQuestionTheFixtureLeftOpenIsSettledHere fails when internal/fixture
// records a decision it declined to make and nothing here makes it.
//
// The questions were recorded rather than answered because a fixture that
// decides how a harness drives a condition has stopped being a fixture. This
// harness is who they were left to, so a question with no settlement is work
// that was handed over and dropped.
func TestEveryQuestionTheFixtureLeftOpenIsSettledHere(t *testing.T) {
	report, err := journey.SettlementReport()
	if err != nil {
		t.Fatalf("%v", err)
	}
	if report != "" {
		t.Fatalf("this harness and the questions internal/fixture left open disagree:\n\n%s", report)
	}
}

// TestEveryCheckHereRefusesACheckThatCannotFail proves the mechanism every
// principle test in this package rests on actually refuses a check nobody has
// shown can fail.
//
// Every check here runs its counterfeits on every invocation, so a predicate
// that stopped discriminating fails where it is used. What that leaves
// unproven is the mechanism itself: a Verify that quietly accepted anything
// would make every check in this package report green while proving nothing,
// which is the exact failure the whole design is against. This drives Verify
// over a check that is wrong in each of the ways it is supposed to catch.
func TestEveryCheckHereRefusesACheckThatCannotFail(t *testing.T) {
	holds := func(int) error { return nil }
	for _, c := range []struct {
		what  string
		check journey.Check[int]
	}{
		{"a check that names no counterfeit", journey.Check[int]{
			What:  "something",
			Holds: holds,
		}},
		{"a check whose counterfeit its own predicate accepts", journey.Check[int]{
			What:  "something",
			Holds: holds,
			Counterfeits: []journey.Counterfeit[int]{
				{Named: "the number came back wrong", Break: func(n int) int { return n + 1 }},
			},
		}},
		{"a check that says nothing about what it establishes", journey.Check[int]{
			Holds:        holds,
			Counterfeits: []journey.Counterfeit[int]{{Named: "x", Break: func(n int) int { return n }}},
		}},
		{"a check with no predicate at all", journey.Check[int]{
			What:         "something",
			Counterfeits: []journey.Counterfeit[int]{{Named: "x", Break: func(n int) int { return n }}},
		}},
		{"a counterfeit that says nothing about what went wrong", journey.Check[int]{
			What:         "something",
			Holds:        func(n int) error { return errFor(n) },
			Counterfeits: []journey.Counterfeit[int]{{Break: func(n int) int { return n + 1 }}},
		}},
		{"a counterfeit that derives nothing", journey.Check[int]{
			What:         "something",
			Holds:        func(n int) error { return errFor(n) },
			Counterfeits: []journey.Counterfeit[int]{{Named: "x"}},
		}},
	} {
		t.Run(c.what, func(t *testing.T) {
			if err := c.check.Verify(0); err == nil {
				t.Fatalf("%s was accepted, so every check in this package could be one", c.what)
			}
		})
	}

	// And the other direction: a check that does discriminate is accepted, so
	// what is above is a refusal of the defective cases rather than of
	// everything.
	sound := journey.Check[int]{
		What:  "the number is zero",
		Holds: errFor,
		Counterfeits: []journey.Counterfeit[int]{
			{Named: "the number came back as something else", Break: func(n int) int { return n + 1 }},
		},
	}
	if err := sound.Verify(0); err != nil {
		t.Fatalf("a check that discriminates was refused: %v", err)
	}
	if err := sound.Verify(1); err == nil {
		t.Fatal("a check was given an observation its own predicate rejects and reported nothing")
	}
}

// errFor reports a number that is not zero, for the sound check above.
func errFor(n int) error {
	if n != 0 {
		return errNotZero
	}
	return nil
}

// errNotZero is what errFor reports.
var errNotZero = errNotZeroType{}

// errNotZeroType is an error type of its own so the sound check's predicate
// depends on nothing else in this package.
type errNotZeroType struct{}

// Error names the failure.
func (errNotZeroType) Error() string { return "the number is not zero" }

// moduleRoot is the root of the module this harness is part of.
func moduleRoot(t *testing.T) string {
	t.Helper()
	root, err := journey.ModuleRoot()
	if err != nil {
		t.Fatalf("%v", err)
	}
	return root
}
