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

// A reviewer that declares the whole diff and nothing else keeps its finding
// about the diff and forfeits the one about a caller it never said it read.
func ExampleParseReviewReport() {
	raw := "I reviewed the change against the stated intent.\n\n" +
		"```json\n" +
		`{
		  "summary": "one problem in the change and one in its caller",
		  "revision": "c0ffeeb4be",
		  "read": ["internal/total/total.go"],
		  "findings": [
		    {"id": "loop-bound", "severity": "error", "action": "fix",
		     "location": "internal/total/total.go:10",
		     "description": "the loop stops one element short"},
		    {"id": "caller-sums-twice", "severity": "error", "action": "fix",
		     "location": "internal/report/render.go:42",
		     "description": "the caller adds the same slice again"}
		  ]
		}` + "\n```\n"

	demand := findings.Demand{
		Revision: "c0ffeeb4be",
		Touched:  []string{"internal/total/total.go"},
	}
	report, binding, err := findings.ParseReviewReport(raw, demand)
	if err != nil {
		fmt.Println("refused:", err)
		return
	}
	for _, f := range report.Fixable() {
		fmt.Println("fixable:", f.ID)
	}
	for _, r := range binding.Refused {
		fmt.Println("refused:", r.Finding.ID, "names", r.Path)
	}
	fmt.Println("read beyond the change:", binding.ReadBeyondChange())

	// Output:
	// fixable: loop-bound
	// refused: caller-sums-twice names internal/report/render.go
	// read beyond the change: false
}
