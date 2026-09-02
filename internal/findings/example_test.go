package findings_test

import (
	"fmt"

	"github.com/dayamjz/assistant/internal/findings"
)

// A stage's output is prose with the report at the end of it, and one of these
// findings never said what to do with it.
func ExampleParseReport() {
	raw := "I reviewed the diff against the stated intent.\n\n" +
		"```json\n" +
		`{
		  "summary": "two problems, one of them yours to decide",
		  "risk": "medium",
		  "findings": [
		    {"severity": "error", "action": "fix", "location": "internal/gate/push.go:88",
		     "description": "the returned error is dropped"},
		    {"severity": "warning", "description": "the retry was removed on purpose?"}
		  ]
		}` + "\n```\n"

	report, err := findings.ParseReport(raw)
	if err != nil {
		fmt.Println("refused:", err)
		return
	}
	fmt.Println("risk:", report.Risk)
	for _, f := range report.Findings {
		fmt.Printf("%s %s %s: %s\n", f.Severity, f.Action, f.Location, f.Description)
	}
	fmt.Println("fix rounds may touch:", len(report.Fixable()))
	fmt.Println("waiting on a person:", report.HasParked())

	// Output:
	// risk: medium
	// error fix internal/gate/push.go:88: the returned error is dropped
	// warning ask : the retry was removed on purpose?
	// fix rounds may touch: 1
	// waiting on a person: true
}

// A finding that arrives with no action becomes an ask, so nothing can fix it
// automatically. This is P3.
func ExampleReport_Normalize() {
	report := findings.Report{
		Summary:  "one finding nobody classified",
		Findings: []findings.Finding{{Description: "the retry was removed"}},
	}.Normalize()

	f := report.Findings[0]
	fmt.Println("action:", f.Action)
	fmt.Println("severity:", f.Severity)
	fmt.Println("fix eligible:", f.FixEligible())

	// Output:
	// action: ask
	// severity: warning
	// fix eligible: false
}
