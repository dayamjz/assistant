package forge_test

import (
	"fmt"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/forge"
)

// A pull request with no checks registered is not passing. The checks stage
// keeps waiting, bounded by checks_timeout, rather than reporting green.
func ExampleChecksReport_Evaluate_noChecks() {
	report := forge.ChecksReport{HeadCommit: "6f1a3c2"}
	result := report.Evaluate(forge.DeclaredNoCI(config.Defaults()))
	fmt.Println(result.Green())
	fmt.Println(result)
	// Output:
	// false
	// no-checks: no checks are registered and nothing declares this repository has none
}

// The one thing that turns an empty check list into a pass is the positive
// declaration in configuration, and the result then names it.
func ExampleChecksReport_Evaluate_declaredNoCI() {
	cfg := config.Defaults()
	cfg.NoCI = true

	report := forge.ChecksReport{HeadCommit: "6f1a3c2"}
	result := report.Evaluate(forge.DeclaredNoCI(cfg))
	fmt.Println(result.Green())
	fmt.Println(result.Declaration.Key())
	fmt.Println(result)
	// Output:
	// true
	// no_ci
	// passed: no checks are registered and the no_ci declaration says this repository has none
}

// A cancelled check has a published conclusion, so the run stops waiting on it
// and reports instead. An unrecognized state is the other way round.
func ExampleChecksReport_Evaluate_cancelled() {
	report := forge.ChecksReport{
		HeadCommit: "6f1a3c2",
		Runs: []forge.CheckRun{
			{Name: "unit", State: forge.CheckStateSucceeded},
			{Name: "integration", State: forge.CheckStateCancelled},
		},
	}
	result := report.Evaluate(forge.NoCIDeclaration{})
	fmt.Println(result.Verdict, len(result.Waiting()))
	fmt.Println(result)
	// Output:
	// failed 0
	// failed: 2 checks, blocked by integration=cancelled
}

// An unrecognized state is treated as still running, deliberately: guessing
// that an unknown state is terminal risks acting on a verdict that has not
// arrived.
func ExampleChecksReport_Evaluate_unrecognized() {
	report := forge.ChecksReport{
		HeadCommit: "6f1a3c2",
		Runs: []forge.CheckRun{
			{Name: "unit", State: forge.CheckStateSucceeded},
			{Name: "integration", State: forge.CheckStateUnrecognized, Reported: "MOONWALKING"},
		},
	}
	result := report.Evaluate(forge.NoCIDeclaration{})
	fmt.Println(result)
	// Output:
	// running: 2 checks, waiting on integration=unrecognized(MOONWALKING)
}
