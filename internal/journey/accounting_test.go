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
//
// Three of those ways are about one clause rather than about the check. A
// clause no counterfeit reaches is invisible while its neighbours keep the
// check honest, which is how this package shipped three clauses that
// established nothing; an absence clause with no precondition is one nobody
// could tell from a clause about a world where the thing could not have been
// there; and a precondition on a clause that asserts no absence is a writer
// who has not decided which kind of clause they are writing.
func TestEveryCheckHereRefusesACheckThatCannotFail(t *testing.T) {
	zero := journey.Clause[int]{States: "the number is zero", Holds: errFor}
	anything := journey.Clause[int]{States: "anything at all", Holds: func(int) error { return nil }}
	counts := func(n int) journey.Counterfeit[int] {
		return journey.Counterfeit[int]{
			Named: "the number came back wrong",
			Break: func(int) int { return n },
		}
	}
	for _, c := range []struct {
		what  string
		check journey.Check[int]
	}{
		{"a check that names no counterfeit", journey.Check[int]{
			What:    "something",
			Clauses: []journey.Clause[int]{zero},
		}},
		{"a check that asserts nothing at all", journey.Check[int]{
			What:         "something",
			Counterfeits: []journey.Counterfeit[int]{counts(1)},
		}},
		{"a check whose counterfeit every one of its clauses accepts", journey.Check[int]{
			What:         "something",
			Clauses:      []journey.Clause[int]{anything},
			Counterfeits: []journey.Counterfeit[int]{counts(1)},
		}},
		{"a check carrying a clause no counterfeit reaches", journey.Check[int]{
			What:         "something",
			Clauses:      []journey.Clause[int]{zero, anything},
			Counterfeits: []journey.Counterfeit[int]{counts(1)},
		}},
		{"a check whose subject carries a claim rather than naming one", journey.Check[int]{
			What: "the number is zero and was read off a real observation rather than stated, and " +
				"nothing else about it was assumed",
			Clauses:      []journey.Clause[int]{{States: "the number is zero", Holds: errFor}},
			Counterfeits: []journey.Counterfeit[int]{{Named: "it came back as something else", Break: func(n int) int { return n + 1 }}},
		}},
		{"a check that says nothing about what it establishes", journey.Check[int]{
			Clauses:      []journey.Clause[int]{zero},
			Counterfeits: []journey.Counterfeit[int]{counts(1)},
		}},
		{"a clause that says nothing about what it asserts", journey.Check[int]{
			What:         "something",
			Clauses:      []journey.Clause[int]{{Holds: errFor}},
			Counterfeits: []journey.Counterfeit[int]{counts(1)},
		}},
		{"a clause with no predicate at all", journey.Check[int]{
			What:         "something",
			Clauses:      []journey.Clause[int]{{States: "the number is zero"}},
			Counterfeits: []journey.Counterfeit[int]{counts(1)},
		}},
		{"an absence clause that says nothing about how the thing could have been there",
			journey.Check[int]{
				What:         "something",
				Clauses:      []journey.Clause[int]{{States: "the number is zero", Absence: true, Holds: errFor}},
				Counterfeits: []journey.Counterfeit[int]{counts(1)},
			}},
		{"a clause carrying a precondition without declaring itself an absence", journey.Check[int]{
			What: "something",
			Clauses: []journey.Clause[int]{{
				States:   "the number is zero",
				Possible: func(int) error { return nil },
				Holds:    errFor,
			}},
			Counterfeits: []journey.Counterfeit[int]{counts(1)},
		}},
		{"an absence clause over a world in which the thing could not have been there",
			journey.Check[int]{
				What: "something",
				Clauses: []journey.Clause[int]{{
					States:   "the number is zero",
					Absence:  true,
					Possible: func(int) error { return errNotZero },
					Holds:    errFor,
				}},
				Counterfeits: []journey.Counterfeit[int]{counts(1)},
			}},
		{"a counterfeit that says nothing about what went wrong", journey.Check[int]{
			What:         "something",
			Clauses:      []journey.Clause[int]{zero},
			Counterfeits: []journey.Counterfeit[int]{{Break: func(n int) int { return n + 1 }}},
		}},
		{"a counterfeit that derives nothing", journey.Check[int]{
			What:         "something",
			Clauses:      []journey.Clause[int]{zero},
			Counterfeits: []journey.Counterfeit[int]{{Named: "x"}},
		}},
	} {
		t.Run(c.what, func(t *testing.T) {
			if err := c.check.Verify(0); err == nil {
				t.Fatalf("%s was accepted, so every check in this package could be one", c.what)
			}
		})
	}

	// And the other direction: a check whose every clause discriminates is
	// accepted, so what is above is a refusal of the defective cases rather
	// than of everything. The absence clause is here too, because a mechanism
	// that refused every one of those would be refusing the shape half this
	// package's checks are written in.
	sound := journey.Check[int]{
		What: "the number is zero and could have been something else",
		Clauses: []journey.Clause[int]{
			zero,
			{
				States:   "the number is not one",
				Absence:  true,
				Possible: func(int) error { return nil },
				Holds: func(n int) error {
					if n == 1 {
						return errNotZero
					}
					return nil
				},
			},
		},
		Counterfeits: []journey.Counterfeit[int]{
			{Named: "the number came back as one", Break: func(int) int { return 1 }},
			{Named: "the number came back as something else again", Break: func(int) int { return 2 }},
		},
	}
	if err := sound.Verify(0); err != nil {
		t.Fatalf("a check whose every clause discriminates was refused: %v", err)
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
