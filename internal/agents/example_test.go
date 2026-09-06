package agents_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/findings"
)

// A run resolves its agent once, before any stage starts, and a run that
// cannot resolve one refuses rather than proceeding with whatever local checks
// happen to work.
func ExampleResolve() {
	entries := []string{"codex", agents.AutoEntry}
	resolution, err := agents.Resolve(context.Background(), entries, agents.DefaultCatalog())
	if err != nil {
		var refusal *agents.ResolutionError
		if errors.As(err, &refusal) {
			for _, tried := range refusal.Tried {
				fmt.Println("passed over", tried.Entry)
			}
		}
		return
	}
	fmt.Println("resolved", resolution.Name)
}

// A review reads the change and returns findings. It goes through Run, which
// has no session to give it, so P4 holds without the stage doing anything to
// hold it.
func ExampleRunner_Run() {
	var runner agents.Runner // from Resolve.
	if runner == nil {
		return
	}
	inv := agents.Invocation{
		Prompt: "Review the change against the intent below. Answer with a report.",
		Shape:  agents.ShapeReport,
		Dir:    "/absolute/path/to/the/isolated/copy",
	}
	result, err := runner.Run(context.Background(), agents.PurposeReview, inv)
	if err != nil {
		// A refusal is a typed result to handle, not a warning to continue
		// past. Output that is not a report never arrives as an empty one.
		var refusal *agents.InvocationError
		if errors.As(err, &refusal) {
			fmt.Println("review did not deliver:", refusal.Failure)
		}
		return
	}
	for _, finding := range findings.Fixable(result.Report.Findings) {
		fmt.Println("eligible for the fix round:", finding.ID)
	}
}

// The fixer is the one role with memory across rounds. It is a separate type
// from the Runner, so a round of fixing cannot be pointed at a review, and
// OpenFixer is the route a caller holding a Runner takes to one: an adapter
// that has not declared resumable sessions is refused here rather than served
// by a weaker path.
func ExampleOpenFixer() {
	var runner agents.Runner // from Resolve.
	if runner == nil {
		return
	}
	fixer, err := agents.OpenFixer(context.Background(), runner, "")
	if err != nil {
		return
	}
	for round := range 2 {
		_, err := fixer.Apply(context.Background(), agents.Invocation{
			Prompt: fmt.Sprintf("Apply the selected findings, round %d.", round+1),
			Shape:  agents.ShapeReport,
			Dir:    "/absolute/path/to/the/isolated/copy",
		})
		if err != nil {
			return
		}
	}
	// Persist this so a restarted service resumes the same conversation.
	fmt.Println("session:", fixer.Reference())
}
