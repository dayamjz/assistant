package fixture

// The subject repository's content. It is a Go module because the stage plants
// need commands that really pass and really fail: `go test ./...` and
// `go vet ./...` give a failing targeted test and a lint violation whose
// messages are git's and the toolchain's rather than this package's invention.
// Nothing about it is meant to look like a project somebody would write.
const (
	subjectGoMod = `module example.com/subject

go 1.21
`

	// subjectTotalGo is the correct implementation, on the default branch.
	subjectTotalGo = `// Package subject is the body of code the delivery gate is pointed at. It is
// a fixture: every line exists so that a stage has a known-correct answer.
package subject

import "strconv"

// Total returns the sum of every element of xs.
func Total(xs []int) int {
	sum := 0
	for i := 0; i < len(xs); i++ {
		sum += xs[i]
	}
	return sum
}

// Format renders n followed by unit. An empty unit renders as "bytes".
func Format(n int, unit string) string {
	if unit == "" {
		unit = "bytes"
	}
	return strconv.Itoa(n) + " " + unit
}
`

	// subjectTotalGoBuggy is the same file with the loop bound moved in by
	// one, which is the planted logic bug. It is a bug rather than a change of
	// intent: the doc comment above it still says every element, and the test
	// below still asserts the sum of all three.
	subjectTotalGoBuggy = `// Package subject is the body of code the delivery gate is pointed at. It is
// a fixture: every line exists so that a stage has a known-correct answer.
package subject

import "strconv"

// Total returns the sum of every element of xs.
func Total(xs []int) int {
	sum := 0
	for i := 0; i < len(xs)-1; i++ {
		sum += xs[i]
	}
	return sum
}

// Format renders n followed by unit. An empty unit renders as "bytes".
func Format(n int, unit string) string {
	if unit == "" {
		unit = "bytes"
	}
	return strconv.Itoa(n) + " " + unit
}
`

	// subjectTotalGoBuggyShortUnit carries the logic bug and the deliberate
	// change of the default unit that makes the documentation stale. The two
	// are separate commits, so a stage that finds one and not the other is
	// distinguishable.
	subjectTotalGoBuggyShortUnit = `// Package subject is the body of code the delivery gate is pointed at. It is
// a fixture: every line exists so that a stage has a known-correct answer.
package subject

import "strconv"

// Total returns the sum of every element of xs.
func Total(xs []int) int {
	sum := 0
	for i := 0; i < len(xs)-1; i++ {
		sum += xs[i]
	}
	return sum
}

// Format renders n followed by unit. An empty unit renders as "B".
func Format(n int, unit string) string {
	if unit == "" {
		unit = "B"
	}
	return strconv.Itoa(n) + " " + unit
}
`

	// subjectTotalTestGo passes against the correct implementation.
	subjectTotalTestGo = `package subject

import "testing"

func TestTotalSumsEveryElement(t *testing.T) {
	if got := Total([]int{1, 2, 3}); got != 6 {
		t.Fatalf("Total([1 2 3]) = %d, want 6", got)
	}
}

func TestFormatDefaultsToBytes(t *testing.T) {
	if got := Format(3, ""); got != "3 bytes" {
		t.Fatalf("Format(3, \"\") = %q, want \"3 bytes\"", got)
	}
}
`

	// subjectTotalTestGoShortUnit follows the deliberate unit change, so that
	// after it the only failing test is the one the logic bug breaks. A second
	// failing test would make the failing-test condition ambiguous.
	subjectTotalTestGoShortUnit = `package subject

import "testing"

func TestTotalSumsEveryElement(t *testing.T) {
	if got := Total([]int{1, 2, 3}); got != 6 {
		t.Fatalf("Total([1 2 3]) = %d, want 6", got)
	}
}

func TestFormatDefaultsToBytes(t *testing.T) {
	if got := Format(3, ""); got != "3 B" {
		t.Fatalf("Format(3, \"\") = %q, want \"3 B\"", got)
	}
}
`

	// subjectDocsBehavior documents both functions as the default branch
	// implements them. The deliberate unit change makes its second paragraph
	// false without touching it, which is the stale-documentation plant.
	subjectDocsBehavior = "# Behavior\n" +
		"\n" +
		"`Total` returns the sum of every element of the slice it is given.\n" +
		"\n" +
		"`Format` renders a number followed by a unit. When no unit is given it\n" +
		"renders the unit as `bytes`.\n"

	// subjectReportGo is the lint violation. The check it trips is
	// deliberately one `go test` does not run for itself: `go test` runs a
	// subset of vet before it builds, and a violation inside that subset would
	// fail the build and take the failing-test condition with it, leaving two
	// conditions that cannot both be observed in one run.
	subjectReportGo = `package subject

import "context"

// Watch returns a cancellable context derived from parent.
func Watch(parent context.Context) context.Context {
	ctx, _ := context.WithCancel(parent)
	return ctx
}
`
)

// subjectConfig is the repository configuration document on the default
// branch. It is the trusted copy: the commands and the agent a run is supposed
// to use come from here and from nowhere else, which is what the pushed-branch
// condition is measured against.
const subjectConfig = `{
  "commands": {
    "test": "go test ./...",
    "lint": "go vet ./..."
  },
  "agent": "fixture-trusted-agent",
  "fix_rounds": {
    "review": 1
  }
}
`

// pushedAgentName is the agent a branch's own configuration document names. It
// differs from the trusted document's on purpose, so which layer a run read is
// observable from what it launched rather than inferred from it not having
// launched anything.
const pushedAgentName = "fixture-pushed-agent"

// subjectPushedConfig is a well-formed, readable configuration document for the
// branch under validation, for the two scenarios whose default-branch copy is
// the condition. Without it the branch carries whatever the default branch had,
// and a run that mistakenly read the pushed copy as trusted would fail on the
// same document for the same reason, so the outcome would not say which layer
// was read.
//
// It is otherwise an ordinary document. The condition in those scenarios is
// what happens to the trusted copy, and a branch document that also carried a
// second condition would put two answers behind one outcome.
const subjectPushedConfig = `{
  "commands": {
    "test": "go test ./...",
    "lint": "go vet ./..."
  },
  "agent": "` + pushedAgentName + `",
  "ignore_patterns": ["vendor/**"]
}
`
