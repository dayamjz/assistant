package scope_test

import (
	"fmt"

	"github.com/dayamjz/assistant/internal/scope"
)

// The review stage asks its reviewer to account for each touched path, and
// turns the paths the answer does not account for into notes. Nothing here
// blocks the run.
func ExampleObserve() {
	change := scope.Change{
		Intent:   "Add a bounded retry to the fetch path.",
		Supplied: true,
		Touched:  []string{"internal/fetch/retry.go", "internal/log/format.go"},
	}

	// What the reviewer answered, having read the guidance and the diff.
	traces := []scope.Trace{
		{Path: "internal/fetch/retry.go", Reason: "the bounded retry the intent asks for"},
	}

	observations, err := scope.Observe(change, traces)
	if err != nil {
		fmt.Println("refused:", err)
		return
	}
	for _, f := range observations {
		fmt.Printf("%s %s: %s\n", f.Action, f.Location, "unexplained by the intent")
	}
	// Output:
	// note internal/log/format.go: unexplained by the intent
}
